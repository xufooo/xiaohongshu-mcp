package browser

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/configs"
	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
)

const (
	pageCloseTimeout        = 2 * time.Second
	browserHealthTimeout    = 2 * time.Second
	defaultStartupTimeout   = 120 * time.Second
	operationAcquireTimeout = 5 * time.Second
	lifecycleAcquireTimeout = 30 * time.Second

	// defaultWarmPageTTL 是热页面保留时长。保留页面能省掉「新建 page」和
	// 「连续两次调用指向同一 URL 时的重复整页加载」（例如 check_login_status → start_page）。
	// 但实测一个已加载小红书 SPA 的页面 PSS ≈ +500MB（桌面口径），
	// 在 1GB 的 Pi 上不能常驻，所以只保留一个很短的时间窗。
	defaultWarmPageTTL = 30 * time.Second
)

// WithWarmPageTTL 设置热页面保留时长，<=0 表示每次 Release 立即关闭页面。
func WithWarmPageTTL(ttl time.Duration) ManagerOption {
	return func(m *Manager) {
		m.warmPageTTL = ttl
	}
}

// BusyError reports that the single browser is currently owned by another operation.
type BusyError struct {
	Owner     string
	StartedAt time.Time
	Waited    time.Duration
}

func (e *BusyError) Error() string {
	owner := e.Owner
	if owner == "" {
		owner = "unknown"
	}
	if len(owner) >= len("session:") && owner[:len("session:")] == "session:" {
		return fmt.Sprintf("browser busy - session active: session_id=%s since %s", owner[len("session:"):], e.StartedAt.Format(time.RFC3339))
	}
	return fmt.Sprintf("browser busy: owner=%s since %s", owner, e.StartedAt.Format(time.RFC3339))
}

// BrowserFactory 创建浏览器实例。
type BrowserFactory func(context.Context) (*hrod.Browser, error)

// ManagerOption 配置浏览器管理器。
type ManagerOption func(*Manager)

// WithIdleTimeout 设置空闲多久后关闭浏览器。小于等于零时不自动关闭。
func WithIdleTimeout(timeout time.Duration) ManagerOption {
	return func(m *Manager) {
		m.idleTimeout = timeout
	}
}

// WithSessionIdleGrace 设置 session 释放后浏览器的保留宽限。
// 小于等于零时跟随 WithIdleTimeout 配置。
func WithSessionIdleGrace(grace time.Duration) ManagerOption {
	return func(m *Manager) {
		m.sessionIdleGrace = grace
	}
}

// Manager 串行复用一个浏览器实例，避免树莓派频繁启动 Chromium。
type Manager struct {
	factory BrowserFactory
	token   chan struct{}

	mu        sync.Mutex
	wg        sync.WaitGroup
	browser   *hrod.Browser
	starting  *browserStartup
	startErr  error
	closed    bool
	resetting bool
	resetDone chan struct{}
	owner     string
	ownerAt   time.Time

	idleTimeout time.Duration
	idleTimer   *time.Timer
	idleVersion uint64

	// 运行时计数：用于在树莓派上量化"复用/新建页面"是否真的生效。
	pagesCreated   int64
	warmPageReused int64

	warmPage      *hrod.Page
	warmPageAt    time.Time
	warmPageTimer *time.Timer
	warmPageTTL   time.Duration

	sessionIdleGrace time.Duration
}

type browserStartup struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	timer  *time.Timer
	once   sync.Once
	err    error
}

func newBrowserStartup(timeout time.Duration) *browserStartup {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	return &browserStartup{
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
	}
}

func (s *browserStartup) finish() {
	s.once.Do(func() {
		close(s.done)
	})
}

// NewManager 创建浏览器管理器。
func NewManager(factory BrowserFactory, options ...ManagerOption) *Manager {
	m := &Manager{
		factory:          factory,
		token:            make(chan struct{}, 1),
		idleTimeout:      5 * time.Minute,
		sessionIdleGrace: time.Minute,
		warmPageTTL:      defaultWarmPageTTL,
	}
	for _, option := range options {
		option(m)
	}
	m.token <- struct{}{}
	return m
}

// Acquire 获取独占页面。浏览器启动不占用操作令牌。
func (m *Manager) Acquire(ctx context.Context) (*hrod.Page, error) {
	return m.AcquireFor(ctx, "browser_operation")
}

// AcquireFor 获取独占页面并记录当前拥有者。
func (m *Manager) AcquireFor(ctx context.Context, owner string) (*hrod.Page, error) {
	page, _, err := m.acquireFor(ctx, owner)
	return page, err
}

// AcquireForWithSource additionally reports whether a new page was created.
func (m *Manager) AcquireForWithSource(ctx context.Context, owner string) (*hrod.Page, bool, error) {
	return m.acquireFor(ctx, owner)
}

func (m *Manager) acquireFor(ctx context.Context, owner string) (*hrod.Page, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		b, err := m.getBrowser(ctx)
		if err != nil {
			return nil, false, err
		}
		if err := m.lockForOwner(ctx, owner); err != nil {
			return nil, false, err
		}

		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			m.releaseToken()
			return nil, false, errors.New("browser manager is closing")
		}
		if m.browser != b {
			m.mu.Unlock()
			m.releaseToken()
			continue
		}
		m.cancelIdleCloseLocked()
		m.mu.Unlock()
		if err := checkBrowserHealth(b); err != nil {
			m.discardBrowser(b)
			m.releaseToken()
			continue
		}

		page := m.takeWarmPage(b)
		if page != nil {
			atomic.AddInt64(&m.warmPageReused, 1)
			return page, false, nil
		}
		atomic.AddInt64(&m.pagesCreated, 1)
		page, err = newPage(b)
		if err != nil {
			m.discardBrowser(b)
			m.releaseToken()
			return nil, false, err
		}
		return page, true, nil
	}
}

// BrowserStats 是浏览器层的运行时计数，供 get_page_state 与日志量化优化效果。
type BrowserStats struct {
	PagesCreated    int64 `json:"pages_created"`
	WarmPageReused  int64 `json:"warm_page_reused"`
	WarmPageCached  bool  `json:"warm_page_cached"`
	IdleTimeoutSecs int64 `json:"idle_timeout_seconds"`
}

// Stats 返回当前计数（读快照，不加锁）。
func (m *Manager) Stats() BrowserStats {
	m.mu.Lock()
	cached := m.warmPage != nil
	idle := m.idleTimeout
	m.mu.Unlock()
	return BrowserStats{
		PagesCreated:    atomic.LoadInt64(&m.pagesCreated),
		WarmPageReused:  atomic.LoadInt64(&m.warmPageReused),
		WarmPageCached:  cached,
		IdleTimeoutSecs: int64(idle.Seconds()),
	}
}

// takeWarmPage 取出可复用的热页面；取不到（无缓存/已过期/不可用）返回 nil。
func (m *Manager) takeWarmPage(b *hrod.Browser) *hrod.Page {
	m.mu.Lock()
	page, parkedAt, ttl := m.warmPage, m.warmPageAt, m.warmPageTTL
	m.warmPage, m.warmPageAt = nil, time.Time{}
	if m.warmPageTimer != nil {
		m.warmPageTimer.Stop()
		m.warmPageTimer = nil
	}
	m.mu.Unlock()
	if page == nil {
		return nil
	}
	if !warmPageReusable(page, b, parkedAt, time.Now(), ttl) {
		_ = page.Close()
		return nil
	}
	logrus.Debug("reuse warm page")
	return page
}

// warmPageReusable 判断缓存的热页面是否还能复用：未过期、属于当前浏览器实例、CDP 仍然可用。
func warmPageReusable(page *hrod.Page, b *hrod.Browser, parkedAt, now time.Time, ttl time.Duration) bool {
	if page == nil || ttl <= 0 {
		return false
	}
	if now.Sub(parkedAt) > ttl {
		return false
	}
	if b != nil && page.Browser() != b {
		return false
	}
	if page.Rod == nil {
		return false
	}
	_, err := page.Rod.Info()
	return err == nil
}

// parkWarmPage 把页面放进热页面缓存，到期自动关闭。返回 false 表示调用方应直接关闭它。
func (m *Manager) parkWarmPage(page *hrod.Page) bool {
	m.mu.Lock()
	if m.closed || m.resetting || m.warmPageTTL <= 0 {
		m.mu.Unlock()
		return false
	}
	previous := m.warmPage
	if m.warmPageTimer != nil {
		m.warmPageTimer.Stop()
	}
	ttl := m.warmPageTTL
	m.warmPage = page
	m.warmPageAt = time.Now()
	m.warmPageTimer = time.AfterFunc(ttl, func() { m.expireWarmPage(page) })
	m.mu.Unlock()

	if previous != nil {
		_ = previous.Close()
	}
	return true
}

func (m *Manager) expireWarmPage(page *hrod.Page) {
	m.mu.Lock()
	if m.warmPage != page {
		m.mu.Unlock()
		return
	}
	m.warmPage, m.warmPageAt, m.warmPageTimer = nil, time.Time{}, nil
	m.mu.Unlock()
	_ = page.Close()
}

// clearWarmPageLocked 丢弃热页面缓存并返回需要关闭的页面，调用方持锁。
func (m *Manager) clearWarmPageLocked() *hrod.Page {
	page := m.warmPage
	m.warmPage, m.warmPageAt = nil, time.Time{}
	if m.warmPageTimer != nil {
		m.warmPageTimer.Stop()
		m.warmPageTimer = nil
	}
	return page
}

// Detach 归还独占权但保留页面句柄：页面既不关闭也不进入热页面缓存，
// 调用方继续独占使用，用完自行关闭。
//
// 用于「待扫码会话」这类需要在页内持续轮询、但又不该占着浏览器独占权
// 把其他工具饿死的场景（否则 4 分钟内所有调用都只能拿到 browser busy）。
func (m *Manager) Detach(page *hrod.Page) {
	if page == nil {
		m.releaseToken()
		return
	}
	m.mu.Lock()
	owner := m.owner
	configuredIdleTimeout := m.idleTimeout
	sessionGrace := m.sessionIdleGrace
	m.mu.Unlock()
	m.scheduleIdleCloseAfter(idleCloseDelay(owner, configuredIdleTimeout, sessionGrace))
	m.releaseToken()
}

// UpdateOwner updates the visible owner for the operation currently holding the browser.
func (m *Manager) UpdateOwner(owner string) {
	if owner == "" {
		owner = "browser_operation"
	}
	m.mu.Lock()
	m.owner = owner
	m.ownerAt = time.Now()
	m.mu.Unlock()
}

// Release 归还独占权，浏览器保持常驻。
// 页面进入热页面缓存（到期自动关闭），取不到缓存的下一次 Acquire 会新建页面。
func (m *Manager) Release(page *hrod.Page) {
	if page != nil && !m.parkWarmPage(page) {
		ctx, cancel := context.WithTimeout(context.Background(), pageCloseTimeout)
		err := page.Context(ctx).Close()
		cancel()
		if err != nil {
			m.discardBrowser(page.Browser())
		}
	}
	m.mu.Lock()
	owner := m.owner
	configuredIdleTimeout := m.idleTimeout
	sessionGrace := m.sessionIdleGrace
	m.mu.Unlock()
	m.scheduleIdleCloseAfter(idleCloseDelay(owner, configuredIdleTimeout, sessionGrace))
	m.releaseToken()
}

// ReleaseAndClose 关闭独占页面并归还浏览器独占权。
// 与 Release 不同，它不会把已关闭页面放入热页面缓存。
func (m *Manager) ReleaseAndClose(page *hrod.Page) error {
	var closeErr error
	if page != nil {
		ctx, cancel := context.WithTimeout(context.Background(), pageCloseTimeout)
		closeErr = page.Context(ctx).Close()
		cancel()
		if closeErr != nil {
			m.discardBrowser(page.Browser())
		}
	}
	m.mu.Lock()
	owner := m.owner
	configuredIdleTimeout := m.idleTimeout
	sessionGrace := m.sessionIdleGrace
	m.mu.Unlock()
	m.scheduleIdleCloseAfter(idleCloseDelay(owner, configuredIdleTimeout, sessionGrace))
	m.releaseToken()
	return closeErr
}

// idleCloseDelay 选择页面释放后的浏览器空闲关闭延迟。
// session owner 使用 sessionGrace 上限（sessionGrace<=0 时跟随 configured），
// 普通 owner 保留配置值；配置小于等于零表示不自动关闭，原样返回。
func idleCloseDelay(owner string, configured, sessionGrace time.Duration) time.Duration {
	if configured <= 0 {
		return configured
	}
	if len(owner) >= len("session:") && owner[:len("session:")] == "session:" && sessionGrace > 0 && configured > sessionGrace {
		return sessionGrace
	}
	return configured
}

// Reset 关闭常驻浏览器。下次 Acquire 会创建新实例。
func (m *Manager) Reset(ctx context.Context) error {
	if err := m.lifecycleLockForOwner(ctx, "reset"); err != nil {
		return err
	}
	defer m.releaseToken()

	m.mu.Lock()
	resetDone := make(chan struct{})
	m.resetting = true
	m.resetDone = resetDone
	m.cancelIdleCloseLocked()
	warmPage := m.clearWarmPageLocked()
	b := m.browser
	m.browser = nil
	if m.starting != nil {
		m.starting.err = errors.New("browser startup reset")
		m.starting.cancel()
		if m.starting.timer != nil {
			m.starting.timer.Stop()
		}
		m.starting.finish()
		m.starting = nil
	}
	m.startErr = nil
	m.mu.Unlock()
	m.wg.Wait()

	// 热页面属于旧浏览器实例，随浏览器一起关闭。
	if warmPage != nil {
		_ = warmPage.Close()
	}

	var closeErr error
	if b != nil {
		closeErr = b.Close()
	}

	m.mu.Lock()
	if m.resetDone == resetDone {
		m.resetting = false
		m.resetDone = nil
		close(resetDone)
	}
	m.mu.Unlock()

	if b == nil {
		return nil
	}
	return closeErr
}

// Close 阻止新的获取并关闭常驻浏览器。
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	m.cancelIdleCloseLocked()
	m.mu.Unlock()
	return m.Reset(ctx)
}

func (m *Manager) getBrowser(ctx context.Context) (*hrod.Browser, error) {
	for {
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return nil, errors.New("browser manager is closing")
		}
		if m.resetting {
			done := m.resetDone
			m.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-done:
				continue
			}
		}
		if m.browser != nil {
			b := m.browser
			m.mu.Unlock()
			return b, nil
		}
		started := m.starting
		if started == nil {
			startupTimeout := defaultStartupTimeout
			if configuredTimeout := configs.GetBrowserStartupTimeout(); configuredTimeout > 0 {
				startupTimeout = configuredTimeout
			}
			started = newBrowserStartup(startupTimeout)
			started.timer = time.AfterFunc(startupTimeout, func() {
				m.failStartup(started)
			})
			m.starting = started
			m.startErr = nil
			m.wg.Add(1)
			go m.startBrowser(started)
		}
		m.mu.Unlock()

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-started.done:
			m.mu.Lock()
			b, err := m.browser, started.err
			if err == nil {
				err = m.startErr
			}
			m.mu.Unlock()
			if b != nil {
				return b, nil
			}
			if err == nil {
				err = errors.New("browser startup failed")
			}
			return nil, err
		}
	}
}

func (m *Manager) startBrowser(started *browserStartup) {
	defer m.wg.Done()

	b, err := newBrowser(started.ctx, m.factory)

	m.mu.Lock()
	if m.starting != started {
		m.mu.Unlock()
		if b != nil {
			_ = b.Close()
		}
		return
	}
	if m.closed && b != nil {
		m.mu.Unlock()
		_ = b.Close()
		m.mu.Lock()
		b = nil
	}
	m.browser = b
	m.startErr = err
	started.err = err
	if started.timer != nil {
		started.timer.Stop()
	}
	m.starting = nil
	started.cancel()
	started.finish()
	m.mu.Unlock()
}

func newBrowser(ctx context.Context, factory BrowserFactory) (browser *hrod.Browser, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("browser startup failed: %v", recovered)
		}
	}()
	return factory(ctx)
}

func newPage(browser *hrod.Browser) (page *hrod.Page, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("create browser page failed: %v", recovered)
			logrus.WithError(fmt.Errorf("%v", recovered)).Debug("newPage panicked")
		}
	}()
	return browser.Page()
}

func (m *Manager) discardBrowser(target *hrod.Browser) {
	if target == nil {
		return
	}
	m.mu.Lock()
	if m.browser == target {
		m.browser = nil
	}
	warmPage := m.clearWarmPageLocked()
	m.cancelIdleCloseLocked()
	m.mu.Unlock()
	if warmPage != nil {
		_ = warmPage.Close()
	}
	_ = target.Close()
}

func checkBrowserHealth(browser *hrod.Browser) error {
	ctx, cancel := context.WithTimeout(context.Background(), browserHealthTimeout)
	defer cancel()
	return browser.Health(ctx)
}

func (m *Manager) failStartup(started *browserStartup) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.starting != started {
		return
	}
	started.err = errors.New("browser startup timed out")
	m.startErr = started.err
	started.cancel()
	m.starting = nil
	started.finish()
}

func (m *Manager) scheduleIdleCloseAfter(delay time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.browser == nil || delay <= 0 {
		return
	}
	m.cancelIdleCloseLocked()
	version := m.idleVersion
	m.idleTimer = time.AfterFunc(delay, func() {
		m.closeIfIdle(version)
	})
}

func (m *Manager) closeIfIdle(version uint64) {
	if err := m.lifecycleLockForOwner(context.Background(), "idle_close"); err != nil {
		return
	}
	defer m.releaseToken()

	m.mu.Lock()
	if m.closed || m.idleVersion != version {
		m.mu.Unlock()
		return
	}
	b := m.browser
	m.browser = nil
	m.idleTimer = nil
	m.idleVersion++
	m.mu.Unlock()
	logrus.Infof("browser idle close: pages_created=%d warm_page_reused=%d",
		atomic.LoadInt64(&m.pagesCreated), atomic.LoadInt64(&m.warmPageReused))

	if b != nil {
		_ = b.Close()
	}
}

func (m *Manager) cancelIdleCloseLocked() {
	m.idleVersion++
	if m.idleTimer != nil {
		m.idleTimer.Stop()
		m.idleTimer = nil
	}
}

func (m *Manager) lockForOwner(ctx context.Context, owner string) error {
	return m.lockForOwnerWithTimeout(ctx, owner, operationAcquireTimeout)
}

func (m *Manager) lifecycleLockForOwner(ctx context.Context, owner string) error {
	return m.lockForOwnerWithTimeout(ctx, owner, lifecycleAcquireTimeout)
}

func (m *Manager) lockForOwnerWithTimeout(ctx context.Context, owner string, acquireTimeout time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if owner == "" {
		owner = "browser_operation"
	}

	waitCtx := ctx
	cancel := func() {}
	if acquireTimeout > 0 {
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > acquireTimeout {
			waitCtx, cancel = context.WithTimeout(ctx, acquireTimeout)
		}
	}
	defer cancel()

	select {
	case <-waitCtx.Done():
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return m.currentBusyError(acquireTimeout)
	case <-m.token:
		m.mu.Lock()
		m.owner = owner
		m.ownerAt = time.Now()
		m.mu.Unlock()
		return nil
	}
}

func (m *Manager) currentBusyError(waited time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return &BusyError{
		Owner:     m.owner,
		StartedAt: m.ownerAt,
		Waited:    waited,
	}
}

func (m *Manager) releaseToken() {
	m.mu.Lock()
	m.owner = ""
	m.ownerAt = time.Time{}
	m.mu.Unlock()
	m.token <- struct{}{}
}

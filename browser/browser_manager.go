package browser

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-rod/rod/lib/proto"
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

	// Page creation includes serial CDP create/attach and per-page setup. On Pi 3B,
	// create+attach alone took 36.2s. Allow 83.8s more for setup/scheduling, bounded
	// at the existing two-minute cold-start scale, independently of lock waiting.
	// A shorter request deadline or cancellation always wins.
	pageCreateTimeout = 120 * time.Second

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

	mu                sync.Mutex
	wg                sync.WaitGroup
	browser           *hrod.Browser
	starting          *browserStartup
	startErr          error
	closed            bool
	resetting         bool
	resetOp           *resetOperation
	resetErr          error
	resetErrByBrowser map[*hrod.Browser]error
	browserCleanups   map[*hrod.Browser]*browserCleanupRecord
	resetPages        []*pendingPage
	pendingBrowsers   []*hrod.Browser
	owner             string
	ownerAt           time.Time

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

type resetOperation struct {
	done chan struct{}
	err  error
	ctx  context.Context
}

type browserCleanupRecord struct {
	browser *hrod.Browser
}

type pendingPage struct {
	page            *hrod.Page
	browser         *hrod.Browser
	operation       *resetOperation
	err             error
	targetDestroyed <-chan struct{}
	stopTargetWatch context.CancelFunc
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
		idleTimeout := m.idleTimeout
		m.mu.Unlock()
		if err := checkBrowserHealth(ctx, b); err != nil {
			if ctx.Err() != nil {
				m.scheduleIdleCloseAfter(idleTimeout)
				m.releaseToken()
				return nil, false, ctx.Err()
			}
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				// A bounded health probe cannot prove a slow browser is dead.
				m.scheduleIdleCloseAfter(idleTimeout)
				m.releaseToken()
				return nil, false, err
			}
			if closeErr := m.discardBrowser(ctx, b); closeErr != nil {
				m.releaseToken()
				return nil, false, errors.Join(err, closeErr)
			}
			m.releaseToken()
			continue
		}

		page, err := m.takeWarmPage(ctx, b)
		if err != nil {
			m.scheduleIdleCloseAfter(idleTimeout)
			m.releaseToken()
			return nil, false, err
		}
		if page != nil {
			atomic.AddInt64(&m.warmPageReused, 1)
			return page, false, nil
		}
		atomic.AddInt64(&m.pagesCreated, 1)
		page, err = newPage(ctx, b)
		if err != nil {
			cleanupErr := m.discardBrowser(ctx, b)
			m.releaseToken()
			return nil, false, errors.Join(err, cleanupErr)
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

// Only a successful browser-level target listing can establish this condition.
// Transport, timeout and renderer errors never imply that a target is absent.
var errWarmTargetAbsent = errors.New("warm target confirmed absent")

// takeWarmPage 取出可复用的热页面；取不到（无缓存/已过期/不可用）返回 nil。
func (m *Manager) takeWarmPage(ctx context.Context, b *hrod.Browser) (*hrod.Page, error) {
	m.mu.Lock()
	page, parkedAt, ttl := m.warmPage, m.warmPageAt, m.warmPageTTL
	m.warmPage, m.warmPageAt = nil, time.Time{}
	var pending *pendingPage
	if page != nil {
		pending = m.retainResetPageLocked(page, b)
	}
	if m.warmPageTimer != nil {
		m.warmPageTimer.Stop()
		m.warmPageTimer = nil
	}
	m.mu.Unlock()
	if page == nil {
		return nil, nil
	}
	probeCtx, cancelProbe := context.WithTimeout(ctx, browserHealthTimeout)
	reusable, probeErr := probeWarmPage(probeCtx, page, b, parkedAt, time.Now(), ttl)
	cancelProbe()
	if errors.Is(probeErr, errWarmTargetAbsent) {
		// There is no target left to close. Retire cleanup ownership and its
		// watcher, but keep the acquisition token for normal newPage below.
		m.mu.Lock()
		m.removeResetPageRecordLocked(pending)
		m.mu.Unlock()
		return nil, ctx.Err()
	}
	if probeErr != nil || ctx.Err() != nil {
		// Restore the same cache entry and its original expiry; a failed query
		// must not silently close the target or fall through to newPage.
		m.mu.Lock()
		if !m.closed && !m.resetting && m.browser == b && m.warmPage == nil {
			m.removeResetPageRecordLocked(pending)
			m.warmPage, m.warmPageAt = page, parkedAt
			m.warmPageTimer = time.AfterFunc(time.Until(parkedAt.Add(ttl)), func() { m.expireWarmPage(page, b) })
		}
		m.mu.Unlock()
		return nil, errors.Join(probeErr, ctx.Err())
	}
	if !reusable || ctx.Err() != nil {
		err := m.closePendingPageRecord(ctx, pending)
		if err != nil {
			return nil, fmt.Errorf("close unusable warm page: %w", err)
		}
		return nil, ctx.Err()
	}
	m.mu.Lock()
	m.removeResetPageRecordLocked(pending)
	m.mu.Unlock()
	logrus.Debug("reuse warm page")
	return page, nil
}

// warmPageReusable 判断缓存的热页面是否还能复用：未过期、属于当前浏览器实例、CDP 仍然可用。
func warmPageReusable(ctx context.Context, page *hrod.Page, b *hrod.Browser, parkedAt, now time.Time, ttl time.Duration) bool {
	reusable, err := probeWarmPage(ctx, page, b, parkedAt, now, ttl)
	return reusable && err == nil
}

func probeWarmPage(ctx context.Context, page *hrod.Page, b *hrod.Browser, parkedAt, now time.Time, ttl time.Duration) (bool, error) {
	if page == nil || ttl <= 0 {
		return false, nil
	}
	if now.Sub(parkedAt) > ttl {
		return false, nil
	}
	if b != nil && page.Browser() != b {
		return false, nil
	}
	if page.Rod == nil || page.Rod.Browser() == nil {
		return false, nil
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if page.Rod.GetContext().Err() != nil {
		return false, nil
	}
	// Query the browser target list with the probe context. A successful list
	// excluding this ID is authoritative absence, without parsing error strings.
	closed, err := pageTargetClosed(ctx, page)
	if err != nil || ctx.Err() != nil {
		return false, errors.Join(err, ctx.Err())
	}
	if closed {
		return false, errWarmTargetAbsent
	}
	return page.Rod.GetContext().Err() == nil, nil
}

// parkWarmPage 把页面放进热页面缓存，到期自动关闭。返回 false 表示调用方应直接关闭它。
func (m *Manager) parkWarmPage(page *hrod.Page) bool {
	pageBrowser := page.Browser()
	m.mu.Lock()
	if m.closed || m.resetting || m.warmPageTTL <= 0 {
		m.mu.Unlock()
		return false
	}
	previous := m.warmPage
	var previousPending *pendingPage
	if previous != nil {
		previousPending = m.retainResetPageLocked(previous, previous.Browser())
	}
	if m.warmPageTimer != nil {
		m.warmPageTimer.Stop()
	}
	ttl := m.warmPageTTL
	m.warmPage = page
	m.warmPageAt = time.Now()
	m.warmPageTimer = time.AfterFunc(ttl, func() { m.expireWarmPage(page, pageBrowser) })
	m.mu.Unlock()

	if previous != nil {
		ctx, cancel := context.WithTimeout(context.Background(), pageCloseTimeout)
		err := m.closePendingPageRecord(ctx, previousPending)
		cancel()
		if err != nil {
			logrus.WithError(err).Debug("close replaced warm page")
		}
	}
	return true
}

func (m *Manager) expireWarmPage(page *hrod.Page, browser *hrod.Browser) {
	m.mu.Lock()
	if m.warmPage != page {
		m.mu.Unlock()
		return
	}
	m.warmPage, m.warmPageAt, m.warmPageTimer = nil, time.Time{}, nil
	pending := m.retainResetPageLocked(page, browser)
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), pageCloseTimeout)
	err := m.closePendingPageRecord(ctx, pending)
	cancel()
	if err != nil {
		logrus.WithError(err).Debug("warm page cleanup pending")
	}
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

func (m *Manager) retainResetPageLocked(page *hrod.Page, browser *hrod.Browser) *pendingPage {
	if page == nil {
		return nil
	}
	if browser == nil {
		browser = page.Browser()
	}
	// Reset may have retired this browser's records before an old session retries.
	// Only confirmed process cleanup makes its pages terminal; transport loss does not.
	if browserCleanupComplete(browser) {
		m.removeResetPagesForBrowserLocked(browser)
		return nil
	}
	for _, current := range m.resetPages {
		if current.page == page {
			if current.browser == nil {
				current.browser = browser
			}
			return current
		}
	}
	targetDestroyed, stopTargetWatch := watchPageTargetDestroyed(page)
	pending := &pendingPage{
		page:            page,
		browser:         browser,
		targetDestroyed: targetDestroyed,
		stopTargetWatch: stopTargetWatch,
	}
	m.resetPages = append(m.resetPages, pending)
	return pending
}

func watchPageTargetDestroyed(page *hrod.Page) (<-chan struct{}, context.CancelFunc) {
	return watchPageTargetDestroyedWithObserver(page, nil)
}

// observed runs after a targetDestroyed event has been processed. Tests use it
// to distinguish watcher consumption from delivery to Rod's upstream event bus.
func watchPageTargetDestroyedWithObserver(page *hrod.Page, observed func(proto.TargetTargetDestroyed)) (<-chan struct{}, context.CancelFunc) {
	if page == nil || page.Rod == nil || page.Rod.Browser() == nil || page.Rod.TargetID == "" {
		return nil, nil
	}
	watchCtx, cancel := context.WithCancel(context.Background())
	events := page.Rod.Browser().Context(watchCtx).Event()
	destroyed := make(chan struct{})
	targetID := page.Rod.TargetID
	go func() {
		defer cancel()
		for message := range events {
			if message.Method != "Target.targetDestroyed" {
				continue
			}
			var event proto.TargetTargetDestroyed
			if !message.Load(&event) {
				continue
			}
			matched := event.TargetID == targetID
			if matched {
				close(destroyed)
			}
			if observed != nil {
				observed(event)
			}
			if matched {
				return
			}
		}
	}()
	return destroyed, cancel
}

func stopPendingPageTargetWatchLocked(pending *pendingPage) {
	if pending != nil && pending.stopTargetWatch != nil {
		pending.stopTargetWatch()
		pending.stopTargetWatch = nil
	}
}

func (m *Manager) closePendingPage(ctx context.Context, page *hrod.Page, browser *hrod.Browser) error {
	if page == nil {
		return nil
	}
	m.mu.Lock()
	pending := m.retainResetPageLocked(page, browser)
	m.mu.Unlock()
	return m.closePendingPageRecord(ctx, pending)
}

func (m *Manager) closePendingPageRecord(ctx context.Context, pending *pendingPage) error {
	if pending == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	if browserCleanupComplete(pending.browser) {
		m.removeResetPageRecordLocked(pending)
		m.mu.Unlock()
		return nil
	}
	recordCurrent := false
	for _, current := range m.resetPages {
		if current == pending {
			recordCurrent = true
			break
		}
	}
	if !recordCurrent {
		m.mu.Unlock()
		return nil
	}
	if pending.operation != nil {
		operation := pending.operation
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-operation.done:
			return operation.err
		}
	}
	operation := &resetOperation{done: make(chan struct{})}
	pending.operation = operation
	m.mu.Unlock()

	var closeErr error
	if !pendingTargetDestroyed(pending) {
		probeCtx, cancelProbe := context.WithTimeout(ctx, pageCloseTimeout)
		targetClosed, probeErr := pageTargetClosed(probeCtx, pending.page)
		probeCtxErr := probeCtx.Err()
		cancelProbe()
		if pendingTargetDestroyed(pending) {
			closeErr = nil
		} else if probeErr != nil {
			closeErr = errors.Join(probeErr, probeCtxErr)
		} else if targetClosed {
			closeErr = nil
		} else if pendingTargetDestroyed(pending) {
			closeErr = nil
		} else {
			pageCtx, cancelPage := context.WithTimeout(ctx, pageCloseTimeout)
			closeErr = pending.page.CloseContext(pageCtx)
			pageCtxErr := pageCtx.Err()
			cancelPage()
			if pendingTargetDestroyed(pending) {
				closeErr = nil
			} else {
				verifyCtx, cancelVerify := context.WithTimeout(context.Background(), pageCloseTimeout)
				targetClosed, verifyErr := pageTargetClosed(verifyCtx, pending.page)
				cancelVerify()
				if pendingTargetDestroyed(pending) {
					closeErr = nil
				} else if verifyErr != nil {
					closeErr = errors.Join(closeErr, pageCtxErr, verifyErr)
				} else if targetClosed {
					closeErr = nil
				} else {
					closeErr = errors.Join(closeErr, pageCtxErr, errors.New("page target remains after close request"))
				}
			}
		}
	}

	m.mu.Lock()
	// Reset can finish while the target query is in flight on the old transport.
	if browserCleanupComplete(pending.browser) {
		closeErr = nil
	}
	operation.err = closeErr
	recordCurrent = false
	for _, current := range m.resetPages {
		if current == pending {
			recordCurrent = true
			break
		}
	}
	if recordCurrent && pending.operation == operation {
		if pendingTargetDestroyed(pending) {
			closeErr = nil
			operation.err = nil
		}
		pending.err = closeErr
		pending.operation = nil
		if closeErr == nil {
			m.removeResetPageLocked(pending.page)
		}
	}
	close(operation.done)
	m.mu.Unlock()
	return closeErr
}

func browserCleanupComplete(browser *hrod.Browser) bool {
	return browser != nil && browser.Rod() != nil && browser.Rod().CleanupComplete()
}

func pendingTargetDestroyed(pending *pendingPage) bool {
	if pending == nil || pending.targetDestroyed == nil {
		return false
	}
	select {
	case <-pending.targetDestroyed:
		return true
	default:
		return false
	}
}

func pageTargetClosed(ctx context.Context, page *hrod.Page) (bool, error) {
	if page == nil || page.Rod == nil || page.Rod.Browser() == nil {
		return false, errors.New("page has no Rod browser for target confirmation")
	}
	result, err := (proto.TargetGetTargets{}).Call(page.Rod.Browser().Context(ctx))
	if err != nil {
		return false, err
	}
	for _, target := range result.TargetInfos {
		if target.TargetID == page.Rod.TargetID {
			return false, nil
		}
	}
	return true, nil
}

func (m *Manager) closePendingPagesForBrowser(ctx context.Context, browser *hrod.Browser) error {
	m.mu.Lock()
	pages := make([]*pendingPage, 0, len(m.resetPages))
	for _, pending := range m.resetPages {
		if pending.browser == browser {
			pages = append(pages, pending)
		}
	}
	m.mu.Unlock()
	var closeErr error
	for _, pending := range pages {
		closeErr = errors.Join(closeErr, m.closePendingPageRecord(ctx, pending))
	}
	return closeErr
}

func (m *Manager) browserCleanupRecordLocked(browser *hrod.Browser) *browserCleanupRecord {
	if browser == nil {
		return nil
	}
	if m.browserCleanups == nil {
		m.browserCleanups = make(map[*hrod.Browser]*browserCleanupRecord)
	}
	if record := m.browserCleanups[browser]; record != nil {
		return record
	}
	record := &browserCleanupRecord{browser: browser}
	m.browserCleanups[browser] = record
	return record
}

func (m *Manager) retireBrowserCleanupLocked(browser *hrod.Browser, record *browserCleanupRecord) bool {
	if browser == nil || record == nil || m.browserCleanups[browser] != record {
		return false
	}
	delete(m.browserCleanups, browser)
	m.removePendingBrowserLocked(browser)
	m.removeResetPagesForBrowserLocked(browser)
	m.setBrowserResetErrorLocked(browser, nil)
	return true
}

func (m *Manager) retainPendingBrowserLocked(browser *hrod.Browser) {
	if browser == nil {
		return
	}
	m.browserCleanupRecordLocked(browser)
	for _, current := range m.pendingBrowsers {
		if current == browser {
			return
		}
	}
	m.pendingBrowsers = append(m.pendingBrowsers, browser)
}

func (m *Manager) removePendingBrowserLocked(browser *hrod.Browser) {
	for index, current := range m.pendingBrowsers {
		if current == browser {
			m.pendingBrowsers = append(m.pendingBrowsers[:index], m.pendingBrowsers[index+1:]...)
			return
		}
	}
}

func (m *Manager) removeResetPagesForBrowserLocked(browser *hrod.Browser) {
	if browser == nil {
		return
	}
	remaining := m.resetPages[:0]
	for _, pending := range m.resetPages {
		if pending.browser != browser {
			remaining = append(remaining, pending)
		} else {
			stopPendingPageTargetWatchLocked(pending)
		}
	}
	m.resetPages = remaining
}

func (m *Manager) removeResetPageLocked(page *hrod.Page) {
	for index, current := range m.resetPages {
		if current.page == page {
			stopPendingPageTargetWatchLocked(current)
			m.resetPages = append(m.resetPages[:index], m.resetPages[index+1:]...)
			return
		}
	}
}

func (m *Manager) removeResetPageRecordLocked(target *pendingPage) {
	for index, current := range m.resetPages {
		if current == target {
			stopPendingPageTargetWatchLocked(current)
			m.resetPages = append(m.resetPages[:index], m.resetPages[index+1:]...)
			return
		}
	}
}

func (m *Manager) setBrowserResetErrorLocked(browser *hrod.Browser, err error) {
	if browser == nil {
		return
	}
	if err == nil {
		delete(m.resetErrByBrowser, browser)
		return
	}
	if m.resetErrByBrowser == nil {
		m.resetErrByBrowser = make(map[*hrod.Browser]error)
	}
	m.resetErrByBrowser[browser] = err
}

func (m *Manager) browserResetErrorLocked() error {
	var err error
	for _, browserErr := range m.resetErrByBrowser {
		err = errors.Join(err, browserErr)
	}
	return err
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
	_ = m.ReleaseContext(context.Background(), page)
}

func (m *Manager) ReleaseContext(ctx context.Context, page *hrod.Page) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var closeErr error
	if page != nil && !m.parkWarmPage(page) {
		closeErr = m.closePageWithBrowserFallback(ctx, page)
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

// ReleaseAndClose 关闭独占页面并归还浏览器独占权。
// 与 Release 不同，它不会把已关闭页面放入热页面缓存。
func (m *Manager) ReleaseAndClose(page *hrod.Page) error {
	return m.ReleaseAndCloseContext(context.Background(), page)
}

func (m *Manager) ReleaseAndCloseContext(ctx context.Context, page *hrod.Page) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var closeErr error
	if page != nil {
		closeErr = m.closePageWithBrowserFallback(ctx, page)
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

func (m *Manager) ClosePageContext(ctx context.Context, page *hrod.Page) error {
	if page == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// A session close can be upgraded to discard after Release parked this page.
	// Remove it before target cleanup so later Acquire cannot reuse the cache entry.
	m.mu.Lock()
	if m.warmPage == page {
		m.clearWarmPageLocked()
	}
	m.mu.Unlock()
	return m.closePendingPage(ctx, page, page.Browser())
}

// Only the caller still holding the browser lease may escalate target cleanup.
func (m *Manager) closePageWithBrowserFallback(ctx context.Context, page *hrod.Page) error {
	browser := page.Browser()
	err := m.ClosePageContext(ctx, page)
	if err != nil {
		browserErr := m.discardBrowser(ctx, browser)
		if browser != nil && browserErr == nil {
			return nil
		}
		return errors.Join(err, browserErr)
	}
	return nil
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
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	if m.resetting {
		operation := m.resetOp
		m.mu.Unlock()
		return m.waitReset(ctx, operation)
	}
	m.mu.Unlock()
	if err := m.lifecycleLockForOwner(ctx, "reset"); err != nil {
		return err
	}

	m.mu.Lock()
	operation := &resetOperation{done: make(chan struct{}), ctx: ctx}
	m.resetting = true
	m.resetOp = operation
	m.resetErr = nil
	m.cancelIdleCloseLocked()
	if page := m.clearWarmPageLocked(); page != nil {
		m.retainResetPageLocked(page, m.browser)
	}
	if m.starting != nil {
		m.starting.err = errors.New("browser startup reset")
		m.starting.cancel()
		if m.starting.timer != nil {
			m.starting.timer.Stop()
		}
	}
	m.mu.Unlock()

	go m.finishReset(ctx, operation)
	return m.waitReset(ctx, operation)
}

func (m *Manager) waitReset(ctx context.Context, operation *resetOperation) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-operation.done:
		return operation.err
	}
}

func (m *Manager) finishReset(ctx context.Context, operation *resetOperation) {
	defer m.releaseToken()
	m.wg.Wait()

	var resetErr error
	if err := ctx.Err(); err != nil {
		resetErr = err
	} else {
		m.mu.Lock()
		pages := append([]*pendingPage(nil), m.resetPages...)
		b := m.browser
		browserRecord := m.browserCleanupRecordLocked(b)
		pendingBrowsers := append([]*hrod.Browser(nil), m.pendingBrowsers...)
		pendingRecords := make(map[*hrod.Browser]*browserCleanupRecord, len(pendingBrowsers))
		for _, pending := range pendingBrowsers {
			pendingRecords[pending] = m.browserCleanupRecordLocked(pending)
		}
		m.mu.Unlock()
		pageErrors := make(map[*pendingPage]error)
		for _, pending := range pages {
			if ctx.Err() != nil {
				resetErr = ctx.Err()
				break
			}
			err := m.closePendingPageRecord(ctx, pending)
			if err != nil && ctx.Err() == nil {
				err = m.closePendingPageRecord(ctx, pending)
			}
			if err != nil {
				pageErrors[pending] = err
			}
		}
		if b != nil {
			if ctx.Err() != nil {
				resetErr = errors.Join(resetErr, ctx.Err())
			} else {
				browserCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				err := b.CloseContext(browserCtx)
				cancel()
				m.mu.Lock()
				if err == nil {
					if m.browserCleanups[b] == browserRecord {
						if m.browser == b {
							m.browser = nil
						}
						m.clearWarmPageLocked()
						m.retireBrowserCleanupLocked(b, browserRecord)
					}
				} else if m.browserCleanups[b] == browserRecord {
					resetErr = errors.Join(resetErr, fmt.Errorf("close browser during reset: %w", err))
				}
				m.mu.Unlock()
			}
		}
		for _, pending := range pendingBrowsers {
			if ctx.Err() != nil {
				resetErr = errors.Join(resetErr, ctx.Err())
				break
			}
			browserCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := pending.CloseContext(browserCtx)
			cancel()
			m.mu.Lock()
			if err == nil {
				if m.browserCleanups[pending] == pendingRecords[pending] {
					m.retireBrowserCleanupLocked(pending, pendingRecords[pending])
				}
			} else if m.browserCleanups[pending] == pendingRecords[pending] {
				resetErr = errors.Join(resetErr, fmt.Errorf("close pending browser during reset: %w", err))
			}
			m.mu.Unlock()
		}
		for pending, err := range pageErrors {
			m.mu.Lock()
			retained := false
			for _, current := range m.resetPages {
				if current == pending {
					retained = true
					break
				}
			}
			m.mu.Unlock()
			if retained {
				resetErr = errors.Join(resetErr, fmt.Errorf("close warm page during browser reset: %w", err))
			}
		}
		if resetErr == nil && ctx.Err() != nil {
			resetErr = ctx.Err()
		}
	}

	m.mu.Lock()
	operation.err = resetErr
	m.resetErr = resetErr
	m.resetting = false
	if m.resetOp == operation {
		m.resetOp = nil
	}
	close(operation.done)
	m.mu.Unlock()
}

// Close 阻止新的获取并关闭常驻浏览器。
func (m *Manager) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	m.closed = true
	m.cancelIdleCloseLocked()
	operation := m.resetOp
	m.mu.Unlock()
	if operation != nil {
		err := m.waitReset(ctx, operation)
		if err == nil || ctx.Err() != nil || operation.ctx == nil || operation.ctx.Err() == nil {
			return err
		}
		// 旧调用已取消时，用本次仍有效的 context 继续未完成的清理。
	}
	return m.Reset(ctx)
}

func (m *Manager) getBrowser(ctx context.Context) (*hrod.Browser, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		m.mu.Lock()
		for pending, record := range m.browserCleanups {
			if !browserCleanupComplete(pending) {
				continue
			}
			if m.browser == pending {
				m.browser = nil
				m.clearWarmPageLocked()
				m.cancelIdleCloseLocked()
			}
			m.retireBrowserCleanupLocked(pending, record)
		}
		if err := errors.Join(m.resetErr, m.browserResetErrorLocked()); err != nil {
			m.mu.Unlock()
			return nil, fmt.Errorf("browser reset cleanup pending: %w", err)
		}
		if len(m.pendingBrowsers) > 0 {
			err := m.resetErr
			if err == nil {
				err = errors.New("pending browser cleanup remains")
			}
			m.mu.Unlock()
			return nil, fmt.Errorf("browser reset cleanup pending: %w", err)
		}
		for _, pending := range m.resetPages {
			if pending.err != nil {
				err := pending.err
				m.mu.Unlock()
				return nil, fmt.Errorf("browser reset cleanup pending: %w", err)
			}
		}
		if m.closed {
			m.mu.Unlock()
			return nil, errors.New("browser manager is closing")
		}
		if m.resetting {
			operation := m.resetOp
			m.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-operation.done:
				continue
			}
		}
		if m.browser != nil {
			b := m.browser
			m.mu.Unlock()
			return b, nil
		}
		started := m.starting
		// A timed-out factory still owns its startup resources until it returns.
		// Do not launch a second Chromium against the same persistent profile.
		if started != nil && started.err != nil {
			err := started.err
			m.mu.Unlock()
			return nil, err
		}
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
	if startupErr := started.ctx.Err(); startupErr != nil {
		err = errors.Join(err, startupErr)
		m.mu.Unlock()
		if b != nil {
			cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 10*time.Second)
			cleanupErr := b.CloseContext(cleanupCtx)
			cancelCleanup()
			err = errors.Join(err, cleanupErr)
			if cleanupErr == nil {
				b = nil
			}
		}
		m.mu.Lock()
	}

	if m.starting != started {
		m.retainPendingBrowserLocked(b)
		m.mu.Unlock()
		return
	}
	if err != nil && b != nil {
		m.retainPendingBrowserLocked(b)
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

type pageCreator interface {
	PageContext(context.Context) (*hrod.Page, error)
}

func newPage(ctx context.Context, browser pageCreator) (page *hrod.Page, err error) {
	pageCtx, cancel := context.WithTimeout(ctx, pageCreateTimeout)
	defer cancel()
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("create browser page failed: %v", recovered)
			logrus.WithError(fmt.Errorf("%v", recovered)).Debug("newPage panicked")
		}
	}()
	return browser.PageContext(pageCtx)
}

func (m *Manager) discardBrowser(ctx context.Context, target *hrod.Browser) error {
	if target == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	current := m.browser == target
	record := m.browserCleanupRecordLocked(target)
	if current {
		if page := m.clearWarmPageLocked(); page != nil {
			m.retainResetPageLocked(page, target)
		}
		m.cancelIdleCloseLocked()
	}
	m.mu.Unlock()
	if !current {
		pageErr := m.closePendingPagesForBrowser(ctx, target)
		browserCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := target.CloseContext(browserCtx)
		cancel()
		if err == nil {
			m.mu.Lock()
			m.retireBrowserCleanupLocked(target, record)
			m.mu.Unlock()
			return nil
		}
		combinedErr := errors.Join(pageErr, err)
		m.mu.Lock()
		if m.browserCleanups[target] != record {
			m.mu.Unlock()
			return nil
		}
		m.retainPendingBrowserLocked(target)
		m.setBrowserResetErrorLocked(target, combinedErr)
		m.mu.Unlock()
		return combinedErr
	}

	pageErr := m.closePendingPagesForBrowser(ctx, target)
	browserCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	browserErr := target.CloseContext(browserCtx)
	cancel()
	if browserErr == nil {
		m.mu.Lock()
		if m.browserCleanups[target] == record {
			if m.browser == target {
				m.browser = nil
			}
			m.retireBrowserCleanupLocked(target, record)
		}
		m.mu.Unlock()
		return nil
	}

	combinedErr := errors.Join(pageErr, browserErr)
	m.mu.Lock()
	if m.browserCleanups[target] != record {
		m.mu.Unlock()
		return nil
	}
	m.setBrowserResetErrorLocked(target, combinedErr)
	m.mu.Unlock()
	return combinedErr
}

func checkBrowserHealth(ctx context.Context, browser *hrod.Browser) error {
	ctx, cancel := context.WithTimeout(ctx, browserHealthTimeout)
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
	browserRecord := m.browserCleanupRecordLocked(b)
	if page := m.clearWarmPageLocked(); page != nil {
		m.retainResetPageLocked(page, m.browser)
	}
	m.idleTimer = nil
	m.idleVersion++
	m.mu.Unlock()
	logrus.Infof("browser idle close: pages_created=%d warm_page_reused=%d",
		atomic.LoadInt64(&m.pagesCreated), atomic.LoadInt64(&m.warmPageReused))

	if b != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		pageErr := m.closePendingPagesForBrowser(ctx, b)
		err := b.CloseContext(ctx)
		cancel()
		m.mu.Lock()
		if m.browserCleanups[b] == browserRecord {
			if err == nil {
				if m.browser == b {
					m.browser = nil
				}
				m.retireBrowserCleanupLocked(b, browserRecord)
			} else {
				m.setBrowserResetErrorLocked(b, errors.Join(pageErr, err))
			}
		}
		m.mu.Unlock()
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
		if err := ctx.Err(); err != nil {
			m.token <- struct{}{}
			return err
		}
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

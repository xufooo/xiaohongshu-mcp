package browser

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/xpzouying/headless_browser"
	"github.com/xpzouying/xiaohongshu-mcp/humanize"
	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
	"github.com/xpzouying/xiaohongshu-mcp/xiaohongshu"
)

type blockedPageCloseCDPClient struct {
	eventOnce             sync.Once
	eventCh               chan *cdp.Event
	mu                    sync.Mutex
	eventClosed           bool
	closeCalls            int
	closeStarted          chan struct{}
	closeRelease          chan struct{}
	closeReleaseAck       chan struct{}
	closeResponsePermit   chan struct{}
	closeResponded        chan struct{}
	retryStarted          chan struct{}
	retryRelease          chan struct{}
	targetClosed          bool
	targetQueries         int
	failPreflight         bool
	failAfterClose        bool
	postCloseQueryStarted chan struct{}
	postCloseQueryPermit  chan struct{}
	postCloseQueryRelease chan struct{}
	destroyOnClose        int
	closeEventTarget      string
	closeErrAt            int
	closeErr              error
	postCloseErr          error
}

func (c *blockedPageCloseCDPClient) Event() <-chan *cdp.Event {
	c.eventOnce.Do(func() { c.eventCh = make(chan *cdp.Event) })
	return c.eventCh
}

func (c *blockedPageCloseCDPClient) Close() {
	c.Event()
	c.mu.Lock()
	if !c.eventClosed {
		c.eventClosed = true
		close(c.eventCh)
	}
	c.mu.Unlock()
}

func (c *blockedPageCloseCDPClient) emitEvent(ctx context.Context, event *cdp.Event) error {
	c.Event()
	if ctx == nil {
		ctx = context.Background()
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.eventClosed {
		return errors.New("CDP event channel is closed")
	}
	select {
	case c.eventCh <- event:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("CDP event send exceeded watchdog")
	}
}

func (c *blockedPageCloseCDPClient) Call(ctx context.Context, _ string, method string, _ interface{}) ([]byte, error) {
	if method == "Target.setDiscoverTargets" {
		return []byte(`{}`), nil
	}
	if method == "Target.attachToTarget" {
		return []byte(`{"sessionId":"session-close"}`), nil
	}
	if method == "Runtime.evaluate" {
		return []byte(`{"result":{"type":"object","objectId":"window"}}`), nil
	}
	if method == "Runtime.callFunctionOn" {
		return []byte(`{"result":{"type":"string","value":"{\"url\":\"about:blank\",\"scroll_y\":0}"}}`), nil
	}
	if method == "Target.getTargets" {
		c.mu.Lock()
		c.targetQueries++
		afterClose := c.closeCalls > 0
		closed := c.targetClosed
		failPreflight := c.failPreflight && !afterClose
		failAfterClose := c.failAfterClose && afterClose
		postCloseErr := c.postCloseErr
		queryStarted := c.postCloseQueryStarted
		queryPermit := c.postCloseQueryPermit
		queryRelease := c.postCloseQueryRelease
		c.mu.Unlock()
		if failPreflight {
			return nil, errors.New("injected Target.getTargets preflight failure")
		}
		if failAfterClose {
			if queryPermit != nil {
				timer := time.NewTimer(2 * time.Second)
				defer timer.Stop()
				select {
				case <-queryPermit:
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-timer.C:
					return nil, errors.New("post-close target query permit exceeded watchdog")
				}
			}
			if queryStarted != nil {
				timer := time.NewTimer(2 * time.Second)
				defer timer.Stop()
				select {
				case queryStarted <- struct{}{}:
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-timer.C:
					return nil, errors.New("post-close target query acknowledgement exceeded watchdog")
				}
			}
			if queryRelease != nil {
				timer := time.NewTimer(2 * time.Second)
				defer timer.Stop()
				select {
				case <-queryRelease:
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-timer.C:
					return nil, errors.New("post-close target query gate exceeded watchdog")
				}
			}
			if postCloseErr != nil {
				return nil, postCloseErr
			}
			return nil, errors.New("injected Target.getTargets post-close failure")
		}
		if closed {
			return []byte(`{"targetInfos":[]}`), nil
		}
		return []byte(`{"targetInfos":[{"targetId":"target-close-test","type":"page"}]}`), nil
	}
	if method == "Page.close" {
		c.mu.Lock()
		c.closeCalls++
		call := c.closeCalls
		destroy := c.destroyOnClose == call
		eventTarget := c.closeEventTarget
		closeErrAt, closeErr := c.closeErrAt, c.closeErr
		c.mu.Unlock()
		if call == 1 {
			if c.closeStarted != nil {
				select {
				case c.closeStarted <- struct{}{}:
				default:
				}
			}
			if c.closeRelease != nil {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-c.closeRelease:
				}
				if c.closeReleaseAck != nil {
					select {
					case c.closeReleaseAck <- struct{}{}:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				if c.closeResponsePermit != nil {
					timer := time.NewTimer(2 * time.Second)
					defer timer.Stop()
					select {
					case <-c.closeResponsePermit:
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-timer.C:
						return nil, errors.New("Page.close response permit exceeded watchdog")
					}
				}
			} else if err := ctx.Err(); err != nil {
				return nil, err
			}
			if c.closeResponded != nil {
				select {
				case c.closeResponded <- struct{}{}:
				default:
				}
			}
		} else if call == 2 && c.retryRelease != nil {
			if c.retryStarted != nil {
				select {
				case c.retryStarted <- struct{}{}:
				default:
				}
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-c.retryRelease:
			}
			c.mu.Lock()
			c.targetClosed = true
			c.mu.Unlock()
			if err := c.emitTargetDestroyed(ctx, "target-close-test"); err != nil {
				return nil, err
			}
		}
		if destroy {
			c.mu.Lock()
			c.targetClosed = true
			c.mu.Unlock()
			if eventTarget == "" {
				eventTarget = "target-close-test"
			}
		}
		if destroy || eventTarget != "" {
			if err := c.emitTargetDestroyed(ctx, eventTarget); err != nil {
				return nil, err
			}
		}
		if closeErrAt == call && closeErr != nil {
			return nil, closeErr
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []byte(`{}`), nil
}

func (c *blockedPageCloseCDPClient) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeCalls
}

func (c *blockedPageCloseCDPClient) queries() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.targetQueries
}

func (c *blockedPageCloseCDPClient) markTargetClosed() {
	c.mu.Lock()
	c.targetClosed = true
	c.mu.Unlock()
}

func (c *blockedPageCloseCDPClient) destroyTarget(ctx context.Context) error {
	c.markTargetClosed()
	return c.emitTargetDestroyed(ctx, "target-close-test")
}

func (c *blockedPageCloseCDPClient) emitTargetDestroyed(ctx context.Context, targetID string) error {
	return c.emitEvent(ctx, &cdp.Event{Method: "Target.targetDestroyed", Params: []byte(`{"targetId":"` + targetID + `"}`)})
}

func newBlockedPageCloseFixture(t *testing.T, client *blockedPageCloseCDPClient) *hrod.Page {
	t.Helper()
	browserCtx, cancelBrowser := context.WithCancel(context.Background())
	rodBrowser := rod.New().Context(browserCtx).NoDefaultDevice().ControlURL("").Client(client)
	if err := rodBrowser.Connect(); err != nil {
		cancelBrowser()
		t.Fatalf("初始化 fake CDP browser: %v", err)
	}
	page, err := rodBrowser.PageFromTarget("target-close-test")
	if err != nil {
		client.Close()
		cancelBrowser()
		t.Fatalf("初始化 fake CDP page: %v", err)
	}
	t.Cleanup(func() {
		client.Close()
		cancelBrowser()
	})
	return &hrod.Page{Rod: page}
}

func pendingPageForTest(manager *Manager, page *hrod.Page) *pendingPage {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	for _, pending := range manager.resetPages {
		if pending.page == page {
			return pending
		}
	}
	return nil
}

func acquireWithWatchdog(manager *Manager, ctx context.Context, cancel context.CancelFunc, watchdog time.Duration) (*hrod.Page, error) {
	type result struct {
		page *hrod.Page
		err  error
	}
	done := make(chan result, 1)
	go func() {
		page, err := manager.Acquire(ctx)
		done <- result{page: page, err: err}
	}()
	timer := time.NewTimer(watchdog)
	defer timer.Stop()
	select {
	case acquired := <-done:
		return acquired.page, acquired.err
	case <-ctx.Done():
		select {
		case acquired := <-done:
			return acquired.page, acquired.err
		default:
		}
	case <-timer.C:
	}
	cause := ctx.Err()
	if cause == nil {
		cause = errors.New("Acquire exceeded independent watchdog")
	}
	cancel()
	cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 10*time.Second)
	manager.mu.Lock()
	browsers := append([]*hrod.Browser(nil), manager.pendingBrowsers...)
	if manager.browser != nil {
		browsers = append(browsers, manager.browser)
	}
	manager.mu.Unlock()
	var cleanupErr error
	for _, b := range browsers {
		cleanupErr = errors.Join(cleanupErr, b.CloseContext(cleanupCtx))
	}
	cleanupErr = errors.Join(cleanupErr, manager.Close(cleanupCtx))
	cancelCleanup()
	var lateErr error
	select {
	case acquired := <-done:
		if acquired.page != nil {
			closeCtx, cancelClose := context.WithTimeout(context.Background(), 2*time.Second)
			lateErr = acquired.page.CloseContext(closeCtx)
			cancelClose()
		}
		lateErr = errors.Join(lateErr, acquired.err)
	case <-time.After(5 * time.Second):
		lateErr = errors.New("Acquire remained blocked after browser cleanup")
	}
	return nil, errors.Join(cause, cleanupErr, lateErr)
}

func TestCloakFingerprintEnabled(t *testing.T) {
	cases := []struct {
		cloak   bool
		ua      string
		enabled bool
	}{
		{true, "", true},                   // Cloak + 无 UA：启用 fingerprint
		{true, "Mozilla/5.0 (X11)", false}, // Cloak + 显式 UA：跳过 fingerprint
		{false, "", false},                 // 普通 Chrome：不启用
	}
	for _, c := range cases {
		if got := cloakFingerprintEnabled(c.cloak, c.ua); got != c.enabled {
			t.Errorf("cloakFingerprintEnabled(%v, %q) = %v, want %v", c.cloak, c.ua, got, c.enabled)
		}
	}
}

func TestCloakFingerprintOptions(t *testing.T) {
	opts := cloakFingerprintOptions(42, "zh-CN")
	if len(opts) != 4 {
		t.Fatalf("cloakFingerprintOptions 应返回 4 个选项, got %d", len(opts))
	}
	cfg := &headless_browser.Config{}
	for _, o := range opts {
		o(cfg)
	}
	if !cfg.Fingerprint {
		t.Error("应启用 fingerprint")
	}
	if cfg.FingerprintSeed != 42 {
		t.Errorf("FingerprintSeed = %d, want 42", cfg.FingerprintSeed)
	}
	if cfg.Language != "zh-CN" {
		t.Errorf("Language = %q, want zh-CN", cfg.Language)
	}
	if cfg.ExtraFlags["fingerprint-brand"] != "Chrome" {
		t.Errorf("ExtraFlags 应含 fingerprint-brand=Chrome, got %v", cfg.ExtraFlags)
	}
}
func TestMaskProxyCredentials(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"无认证", "http://proxy.example.com:8080", "http://proxy.example.com:8080"},
		{"仅用户名", "http://user123@proxy.example.com:8080", "http://***@proxy.example.com:8080"},
		{"用户名+密码", "http://user123:pass456@proxy.example.com:8080", "http://***:***@proxy.example.com:8080"},
		{"userinfo 转义 @", "http://user%40x:p%40ss@proxy.example.com:8080", "http://***:***@proxy.example.com:8080"},
		{"scheme-relative 带认证", "//user123:pass456@proxy.example.com:8080", "//***:***@proxy.example.com:8080"},
		{"scheme-relative 仅用户名", "//user123@proxy.example.com:8080", "//***@proxy.example.com:8080"},
		{"路径含 @ 不干扰边界", "http://user123:pass456@proxy.example.com:8080/path@x", "http://***:***@proxy.example.com:8080/path@x"},
		{"无 scheme 带 userinfo fail-closed", "user123:pass456@proxy.example.com:8080", "[invalid-proxy]"},
		{"畸形 URL", "http://[bad url", "[invalid-proxy]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := maskProxyCredentials(tc.in)
			if got != tc.want {
				t.Fatalf("maskProxyCredentials(%q) = %q, 期望 %q", tc.in, got, tc.want)
			}
			for _, secret := range []string{"user123", "pass456"} {
				if strings.Contains(got, secret) {
					t.Fatalf("凭据泄露: %q contains %q", got, secret)
				}
			}
		})
	}
}

func TestIdleCloseDelay(t *testing.T) {
	cases := []struct {
		name         string
		owner        string
		configured   time.Duration
		sessionGrace time.Duration
		want         time.Duration
	}{
		{"普通 owner 保留配置", "browser_operation", 5 * time.Minute, time.Minute, 5 * time.Minute},
		{"session owner 上限 grace", "session:test-id", 5 * time.Minute, time.Minute, time.Minute},
		{"session owner 尊重更短配置", "session:test-id", 30 * time.Second, time.Minute, 30 * time.Second},
		{"session owner 保持零禁用", "session:test-id", 0, time.Minute, 0},
		{"session owner 保持负值禁用", "session:test-id", -1 * time.Second, time.Minute, -1 * time.Second},
		{"无冒号前缀不算 session owner", "session", 5 * time.Minute, time.Minute, 5 * time.Minute},
		{"sessionGrace 零时跟随配置", "session:test-id", 5 * time.Minute, 0, 5 * time.Minute},
		{"sessionGrace 为负时跟随配置", "session:test-id", 5 * time.Minute, -1 * time.Second, 5 * time.Minute},
		{"sessionGrace 大于配置时用配置", "session:test-id", 30 * time.Second, 10 * time.Minute, 30 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := idleCloseDelay(tc.owner, tc.configured, tc.sessionGrace); got != tc.want {
				t.Fatalf("idleCloseDelay(%q, %v, %v) = %v, 期望 %v", tc.owner, tc.configured, tc.sessionGrace, got, tc.want)
			}
		})
	}
}

// TestWarmPageReusableExpiry 热页面缓存的过期/禁用判断（不触碰真实页面）。
func TestWarmPageReusableExpiry(t *testing.T) {
	now := time.Now()
	page := &hrod.Page{}
	cases := []struct {
		name     string
		page     *hrod.Page
		parkedAt time.Time
		ttl      time.Duration
		want     bool
	}{
		{"无缓存页面", nil, now, time.Minute, false},
		{"TTL 关闭", page, now, 0, false},
		{"TTL 负数", page, now, -time.Second, false},
		{"已过期", page, now.Add(-2 * time.Minute), time.Minute, false},
	}
	for _, c := range cases {
		if got := warmPageReusable(c.page, nil, c.parkedAt, now, c.ttl); got != c.want {
			t.Errorf("%s: warmPageReusable = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestClearWarmPageLockedClearsCache 缓存被取走后不得残留引用。
func TestClearWarmPageLockedClearsCache(t *testing.T) {
	m := NewManager(nil, WithWarmPageTTL(time.Minute))
	page := &hrod.Page{}
	if !m.parkWarmPage(page) {
		t.Fatal("TTL>0 时应能缓存热页面")
	}
	if got := m.clearWarmPageLocked(); got != page {
		t.Fatalf("clearWarmPageLocked 应返回缓存的页面")
	}
	if m.warmPage != nil || m.warmPageTimer != nil {
		t.Fatal("清理后不应残留页面或定时器")
	}
	if got := m.clearWarmPageLocked(); got != nil {
		t.Fatal("重复清理应返回 nil")
	}
}

// TestParkWarmPageDisabledWhenTTLZero TTL<=0 时 Release 直接关页面，不缓存。
func TestParkWarmPageDisabledWhenTTLZero(t *testing.T) {
	m := NewManager(nil, WithWarmPageTTL(0))
	if m.parkWarmPage(&hrod.Page{}) {
		t.Fatal("TTL<=0 时不应缓存热页面")
	}
}

func TestWarmTimerAndResetWaitForOneBlockedPageClose(t *testing.T) {
	client := &blockedPageCloseCDPClient{
		closeStarted:   make(chan struct{}, 1),
		closeRelease:   make(chan struct{}, 1),
		closeResponded: make(chan struct{}, 1),
		retryStarted:   make(chan struct{}, 1),
		retryRelease:   make(chan struct{}, 1),
	}
	page := newBlockedPageCloseFixture(t, client)
	defer func() {
		select {
		case client.retryRelease <- struct{}{}:
		default:
		}
	}()
	manager := NewManager(nil, WithWarmPageTTL(5*time.Millisecond))
	if !manager.parkWarmPage(page) {
		t.Fatal("受控页面应进入 warm cache")
	}
	select {
	case <-client.closeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("warm timer 未触发受控 Page.close CDP 请求")
	}
	shortCtx, cancelShort := context.WithTimeout(context.Background(), 50*time.Millisecond)
	resetDone := make(chan error, 1)
	go func() { resetDone <- manager.Reset(shortCtx) }()
	client.closeRelease <- struct{}{}
	select {
	case <-client.closeResponded:
	case <-time.After(time.Second):
		cancelShort()
		t.Fatal("Page.close 成功响应未到达受控 fixture")
	}
	var err error
	select {
	case err = <-resetDone:
	case <-time.After(2 * time.Second):
		cancelShort()
		t.Fatal("targetDestroyed 缺失时 Reset 未服从 deadline")
	}
	cancelShort()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Reset 应在等待同一页面 Close 时服从 deadline: %v", err)
	}
	if calls := client.calls(); calls != 1 {
		t.Fatalf("timer 与 Reset 不得并发重复 Page.close: calls=%d", calls)
	}
	manager.mu.Lock()
	resetOp := manager.resetOp
	manager.mu.Unlock()
	if resetOp != nil {
		select {
		case <-resetOp.done:
		case <-time.After(2 * time.Second):
			t.Fatal("Reset operation 未在独立 watchdog 内完成")
		}
	}
	manager.mu.Lock()
	var pageOperation *resetOperation
	for _, pending := range manager.resetPages {
		if pending.page == page {
			pageOperation = pending.operation
			break
		}
	}
	manager.mu.Unlock()
	if pageOperation != nil {
		select {
		case <-pageOperation.done:
		case <-time.After(5 * time.Second):
			t.Fatal("page cleanup operation 未在独立 watchdog 内完成")
		}
	}
	manager.mu.Lock()
	pendingRecords, resetErr := len(manager.resetPages), manager.resetErr
	manager.mu.Unlock()
	if pendingRecords != 1 || !errors.Is(resetErr, context.DeadlineExceeded) {
		t.Fatalf("Page.close 已成功但 targetDestroyed 缺失时必须保留清理责任: pending=%d err=%v", pendingRecords, resetErr)
	}
	retryCtx, cancelRetry := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelRetry()
	retryDone := make(chan error, 1)
	go func() { retryDone <- manager.Reset(retryCtx) }()
	select {
	case <-client.retryStarted:
	case <-retryCtx.Done():
		t.Fatalf("Page.close retry 未启动: %v", retryCtx.Err())
	case <-time.After(time.Second):
		t.Fatal("Page.close retry 未进入 CDP 调用")
	}
	client.retryRelease <- struct{}{}
	select {
	case err := <-retryDone:
		if err != nil {
			t.Fatalf("匹配 targetDestroyed 后 Reset retry 失败: %v", err)
		}
	case <-retryCtx.Done():
		t.Fatalf("匹配 targetDestroyed 后 Reset retry 超时: %v", retryCtx.Err())
	case <-time.After(2 * time.Second):
		t.Fatal("匹配 targetDestroyed 后 Reset retry 超过 watchdog")
	}
	manager.mu.Lock()
	pendingRecords = len(manager.resetPages)
	manager.mu.Unlock()
	if pendingRecords != 0 {
		t.Fatalf("确认 target 终态后必须退休 pending page: %d", pendingRecords)
	}
	factoryErr := errors.New("controlled factory failure")
	manager.factory = func(context.Context) (*hrod.Browser, error) { return nil, factoryErr }
	acquireCtx, cancelAcquire := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelAcquire()
	if _, err := manager.Acquire(acquireCtx); !errors.Is(err, factoryErr) {
		t.Fatalf("已退休 page 的旧结果不得让 Acquire 误报 cleanup pending: %v", err)
	}
}

func TestSessionCloseFailureAfterPageCloseStartsRetriesLeaseOnce(t *testing.T) {
	client := &blockedPageCloseCDPClient{
		closeStarted:   make(chan struct{}, 1),
		closeRelease:   make(chan struct{}, 1),
		closeResponded: make(chan struct{}, 1),
		retryStarted:   make(chan struct{}, 1),
		retryRelease:   make(chan struct{}, 1),
	}
	page := newBlockedPageCloseFixture(t, client)
	defer func() {
		select {
		case client.closeRelease <- struct{}{}:
		default:
		}
		select {
		case client.retryRelease <- struct{}{}:
		default:
		}
	}()
	manager := NewManager(nil, WithWarmPageTTL(0))
	leaseCtx, cancelLease := context.WithTimeout(context.Background(), time.Second)
	if err := manager.lockForOwner(leaseCtx, "browser_operation"); err != nil {
		cancelLease()
		t.Fatalf("占用生产 browser lease 失败: %v", err)
	}
	cancelLease()
	defer func() {
		manager.mu.Lock()
		manager.owner = ""
		manager.ownerAt = time.Time{}
		manager.mu.Unlock()
		select {
		case manager.token <- struct{}{}:
		default:
		}
	}()
	sessions := xiaohongshu.NewBrowseSessionManager(time.Minute)
	createCtx, cancelCreate := context.WithTimeout(context.Background(), 2*time.Second)
	session, err := sessions.CreateContextWithLifecycle(createCtx, page, nil,
		func(ctx context.Context, page *hrod.Page, discard, leaseReleased bool) error {
			if leaseReleased {
				return manager.ClosePageContext(ctx, page)
			}
			if discard {
				return manager.ReleaseAndCloseContext(ctx, page)
			}
			return manager.ReleaseContext(ctx, page)
		},
		func(session *xiaohongshu.BrowseSession, insert func()) error {
			manager.UpdateOwner("session:" + session.ID())
			insert()
			return nil
		},
	)
	cancelCreate()
	if err != nil {
		t.Fatalf("创建 production lifecycle session 失败: %v", err)
	}
	firstCtx, cancelFirst := context.WithTimeout(context.Background(), 100*time.Millisecond)
	firstDone := make(chan error, 1)
	go func() { firstDone <- sessions.CloseContext(firstCtx, session.ID()) }()
	select {
	case <-client.closeStarted:
	case <-time.After(2 * time.Second):
		cancelFirst()
		t.Fatal("首次 session Close 未进入受控 Page.close CDP 调用")
	}
	client.closeRelease <- struct{}{}
	select {
	case <-client.closeResponded:
	case <-time.After(time.Second):
		cancelFirst()
		t.Fatal("首次 Page.close 成功响应未到达，但 targetDestroyed 事件仍应缺失")
	}
	select {
	case err := <-firstDone:
		if err == nil {
			cancelFirst()
			t.Fatal("首次 Page.Close 未确认 target 终态时必须保留清理错误")
		}
	case <-time.After(2 * time.Second):
		cancelFirst()
		t.Fatal("首次 Page.Close 未在独立 watchdog 内返回")
	}
	cancelFirst()
	manager.mu.Lock()
	pendingAfterTimeout := len(manager.resetPages)
	var pendingCloseErr error
	if pendingAfterTimeout > 0 {
		pendingCloseErr = manager.resetPages[0].err
	}
	manager.mu.Unlock()
	if pendingAfterTimeout != 1 || pendingCloseErr == nil {
		t.Fatalf("Page.close 未确认 target 终态时必须保留 pending 责任: pending=%d err=%v", pendingAfterTimeout, pendingCloseErr)
	}

	retryCtx, cancelRetry := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelRetry()
	firstRetry := make(chan error, 1)
	go func() { firstRetry <- sessions.CloseContext(retryCtx, session.ID()) }()
	select {
	case <-client.retryStarted:
	case <-retryCtx.Done():
		t.Fatalf("首次 retry 未进入受控 Page.close: %v", retryCtx.Err())
	case <-time.After(2 * time.Second):
		t.Fatal("首次 retry 未进入受控 Page.close")
	}
	secondCtx, cancelSecond := context.WithTimeout(context.Background(), 100*time.Millisecond)
	secondRetry := make(chan error, 1)
	go func() { secondRetry <- sessions.CloseContext(secondCtx, session.ID()) }()
	select {
	case err := <-secondRetry:
		if !errors.Is(err, context.DeadlineExceeded) {
			cancelSecond()
			t.Fatalf("受控 Page.close 阻塞期间第二个 CloseContext 应在短于首个关闭预算的期限内超时: %v", err)
		}
	case <-time.After(time.Second):
		cancelSecond()
		t.Fatal("第二个 CloseContext 未在独立 watchdog 内完成关闭窗口等待")
	}
	cancelSecond()
	select {
	case err := <-firstRetry:
		t.Fatalf("第二个 caller 返回前不得让首个关闭操作完成: %v", err)
	default:
	}
	client.retryRelease <- struct{}{}
	select {
	case err := <-firstRetry:
		if err != nil {
			t.Fatalf("释放受控 Page.close 后首个并发 retry 失败: %v", err)
		}
	case <-retryCtx.Done():
		t.Fatalf("首个并发 retry 超时: %v", retryCtx.Err())
	case <-time.After(7 * time.Second):
		t.Fatal("首个并发 retry 超过 watchdog")
	}
	if calls := client.calls(); calls != 2 {
		t.Fatalf("首次失败加并发 retry 应只关闭两次 page: calls=%d", calls)
	}
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), time.Second)
	if err := manager.lockForOwner(probeCtx, "lease-probe"); err != nil {
		cancelProbe()
		t.Fatalf("首次失败后 lease 应可重新取得: %v", err)
	}
	cancelProbe()
	busyCtx, cancelBusy := context.WithTimeout(context.Background(), time.Second)
	err = manager.lockForOwnerWithTimeout(busyCtx, "lease-contender", 25*time.Millisecond)
	cancelBusy()
	var busy *BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("probe 持有 lease 时第二个调用应 busy: %v", err)
	}
	manager.releaseToken()
	retryLeaseCtx, cancelRetryLease := context.WithTimeout(context.Background(), time.Second)
	defer cancelRetryLease()
	if err := manager.lockForOwner(retryLeaseCtx, "lease-after-release"); err != nil {
		t.Fatalf("probe lease 归还后必须恰有一次 lease 可用: %v", err)
	}
	manager.releaseToken()
}

func TestPendingPageMissingBeforeRetrySkipsPageClose(t *testing.T) {
	client := &blockedPageCloseCDPClient{
		closeStarted:   make(chan struct{}, 1),
		closeRelease:   make(chan struct{}, 1),
		closeResponded: make(chan struct{}, 1),
	}
	page := newBlockedPageCloseFixture(t, client)
	defer func() {
		select {
		case client.closeRelease <- struct{}{}:
		default:
		}
		select {
		case client.retryRelease <- struct{}{}:
		default:
		}
	}()
	manager := NewManager(nil, WithWarmPageTTL(0))
	firstCtx, cancelFirst := context.WithTimeout(context.Background(), 100*time.Millisecond)
	firstDone := make(chan error, 1)
	go func() { firstDone <- manager.ClosePageContext(firstCtx, page) }()
	select {
	case <-client.closeStarted:
	case <-time.After(2 * time.Second):
		cancelFirst()
		t.Fatal("首次关闭未进入受控 Page.close")
	}
	select {
	case err := <-firstDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			cancelFirst()
			t.Fatalf("首轮 Page.close deadline 应保留 pending: %v", err)
		}
	case <-time.After(3 * time.Second):
		cancelFirst()
		t.Fatal("首次关闭未在独立 watchdog 内返回")
	}
	cancelFirst()
	if pending := pendingPageForTest(manager, page); pending == nil {
		t.Fatal("首次 close 超时后应保留 pending 清理责任")
	}

	client.markTargetClosed()
	retryCtx, cancelRetry := context.WithTimeout(context.Background(), time.Second)
	defer cancelRetry()
	if err := manager.ClosePageContext(retryCtx, page); err != nil {
		t.Fatalf("目标在 retry 前已消失时应直接退休: %v", err)
	}
	if calls := client.calls(); calls != 1 {
		t.Fatalf("retry preflight 确认 target 不存在时不得重复 Page.close: calls=%d", calls)
	}
	if pending := pendingPageForTest(manager, page); pending != nil {
		t.Fatal("确认 target 不存在后 pending 必须退休")
	}
}

func TestTargetDestroyedEventWinsOverPostCloseTargetQueryFailure(t *testing.T) {
	closeErr := errors.New("injected Page.close failure")
	client := &blockedPageCloseCDPClient{
		closeStarted:          make(chan struct{}, 1),
		closeRelease:          make(chan struct{}, 1),
		closeResponded:        make(chan struct{}, 1),
		failAfterClose:        true,
		postCloseQueryStarted: make(chan struct{}, 1),
		postCloseQueryRelease: make(chan struct{}, 1),
		closeErrAt:            1,
		closeErr:              closeErr,
	}
	page := newBlockedPageCloseFixture(t, client)
	defer func() {
		select {
		case client.closeRelease <- struct{}{}:
		default:
		}
		select {
		case client.postCloseQueryRelease <- struct{}{}:
		default:
		}
	}()
	manager := NewManager(nil, WithWarmPageTTL(0))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	closeDone := make(chan error, 1)
	go func() { closeDone <- manager.ClosePageContext(ctx, page) }()
	select {
	case <-client.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("Page.close 未在独立 watchdog 内启动")
	}
	client.closeRelease <- struct{}{}
	select {
	case <-client.closeResponded:
	case <-time.After(time.Second):
		t.Fatal("Page.close 响应未在 watchdog 内到达")
	}
	select {
	case <-client.postCloseQueryStarted:
	case <-time.After(time.Second):
		t.Fatal("post-close Target.getTargets 未进入阶段性故障屏障")
	}
	pending := pendingPageForTest(manager, page)
	if pending == nil {
		t.Fatal("post-close query 等待期间应保留 pending 责任")
	}
	if err := client.destroyTarget(ctx); err != nil {
		t.Fatalf("受控释放匹配 targetDestroyed 事件失败: %v", err)
	}
	select {
	case <-pending.targetDestroyed:
	case <-ctx.Done():
		t.Fatalf("匹配 targetDestroyed 未被 watcher 确认: %v", ctx.Err())
	case <-time.After(time.Second):
		t.Fatal("匹配 targetDestroyed watcher 等待超过 watchdog")
	}
	client.postCloseQueryRelease <- struct{}{}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("已观察到匹配 targetDestroyed 后查询失败不应撤销终态: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("关闭超过 deadline: %v", ctx.Err())
	case <-time.After(2 * time.Second):
		t.Fatal("ClosePageContext 未在独立 watchdog 内完成")
	}
	if calls := client.calls(); calls != 1 {
		t.Fatalf("事件终态确认后不得额外发送 Page.close: calls=%d", calls)
	}
	if pending := pendingPageForTest(manager, page); pending != nil {
		t.Fatal("匹配 targetDestroyed 且 query 失败后 pending 应退休")
	}
}

func TestKnownTargetDestroyedSkipsTargetQueryAndPageClose(t *testing.T) {
	client := &blockedPageCloseCDPClient{failPreflight: true}
	page := newBlockedPageCloseFixture(t, client)
	manager := NewManager(nil, WithWarmPageTTL(0))
	manager.mu.Lock()
	pending := manager.retainResetPageLocked(page, page.Browser())
	manager.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.destroyTarget(ctx); err != nil {
		t.Fatalf("受控释放匹配 targetDestroyed 事件失败: %v", err)
	}
	select {
	case <-pending.targetDestroyed:
	case <-ctx.Done():
		t.Fatalf("已知终态事件未被 watcher 确认: %v", ctx.Err())
	case <-time.After(time.Second):
		t.Fatal("已知终态事件 watcher 超过独立 watchdog")
	}
	if err := manager.ClosePageContext(ctx, page); err != nil {
		t.Fatalf("已知 targetDestroyed 终态应直接退休: %v", err)
	}
	if queries := client.queries(); queries != 0 {
		t.Fatalf("已知事件终态不得查询 Target.getTargets: queries=%d", queries)
	}
	if calls := client.calls(); calls != 0 {
		t.Fatalf("已知事件终态不得重复发送 Page.close: calls=%d", calls)
	}
	if pending := pendingPageForTest(manager, page); pending != nil {
		t.Fatal("已知事件终态提交后 pending 应退休")
	}
}

func TestFatalInvalidationUpgradesOrdinaryCloseWithDefaultWarmCache(t *testing.T) {
	for _, parkFirst := range []bool{false, true} {
		name := "invalidate before park"
		if parkFirst {
			name = "invalidate after park"
		}
		t.Run(name, func(t *testing.T) {
			client := &blockedPageCloseCDPClient{destroyOnClose: 1}
			page := newBlockedPageCloseFixture(t, client)
			manager := NewManager(nil)
			sessions := xiaohongshu.NewBrowseSessionManager(time.Minute)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := manager.lockForOwner(ctx, "session"); err != nil {
				t.Fatal(err)
			}
			entered, resume := make(chan struct{}), make(chan struct{})
			var resumeOnce sync.Once
			unblock := func() { resumeOnce.Do(func() { close(resume) }) }
			var releases atomic.Int32
			session, err := sessions.CreateContextWithLifecycle(ctx, page, nil,
				func(closeCtx context.Context, p *hrod.Page, discard, leaseReleased bool) error {
					if leaseReleased {
						return manager.ClosePageContext(closeCtx, p)
					}
					releases.Add(1)
					if discard {
						return manager.ReleaseAndCloseContext(closeCtx, p)
					}
					if parkFirst {
						if err := manager.ReleaseContext(closeCtx, p); err != nil {
							return err
						}
					}
					close(entered)
					select {
					case <-resume:
					case <-closeCtx.Done():
						return closeCtx.Err()
					}
					if !parkFirst {
						return manager.ReleaseContext(closeCtx, p)
					}
					return nil
				}, nil)
			if err != nil {
				manager.releaseToken()
				t.Fatal(err)
			}
			closed, joined := make(chan error, 1), make(chan struct{})
			go func() {
				defer close(joined)
				// Session TTL uses this same ordinary Close path.
				closed <- session.CloseContext(ctx)
			}()
			t.Cleanup(func() {
				unblock()
				cancel()
				sessions.StopCleanupWorkers()
				cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancelCleanup()
				if err := sessions.CloseAllContext(cleanupCtx); err != nil {
					t.Errorf("fatal upgrade session cleanup: %v", err)
				}
				select {
				case <-joined:
				case <-time.After(4 * time.Second):
					t.Error("ordinary close worker did not join")
				}
				manager.mu.Lock()
				manager.clearWarmPageLocked()
				manager.cancelIdleCloseLocked()
				manager.mu.Unlock()
			})
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("ordinary close did not reach its release barrier")
			}
			if parkFirst && !manager.Stats().WarmPageCached {
				t.Fatal("default warm cache must contain the released page before invalidation")
			}
			if !sessions.CloseInvalidatedAsync(session.ID()) {
				t.Fatal("fatal invalidation must find the session already undergoing ordinary close")
			}
			unblock()
			select {
			case err := <-closed:
				if err != nil {
					t.Fatalf("ordinary close failed its discard upgrade: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("ordinary close did not finish the discard upgrade")
			}
			if err := sessions.CloseAllContext(ctx); err != nil {
				t.Fatalf("join fatal worker: %v", err)
			}
			if manager.Stats().WarmPageCached || client.calls() != 1 || releases.Load() != 1 {
				t.Fatalf("discard must evict warm page, close target and release lease once: warm=%t closes=%d releases=%d", manager.Stats().WarmPageCached, client.calls(), releases.Load())
			}
			if err := manager.lockForOwner(ctx, "post-fatal-probe"); err != nil {
				t.Fatalf("returned lease must be available: %v", err)
			}
			defer manager.releaseToken()
			select {
			case <-manager.token:
				t.Fatal("fatal cleanup returned the browser lease twice")
			default:
			}
		})
	}
}

func TestWrongTargetDestroyedEventKeepsPendingPage(t *testing.T) {
	verifyErr := errors.New("injected target verification failure")
	client := &blockedPageCloseCDPClient{
		closeStarted:          make(chan struct{}, 1),
		closeRelease:          make(chan struct{}),
		failAfterClose:        true,
		postCloseQueryStarted: make(chan struct{}, 1),
		postCloseQueryRelease: make(chan struct{}),
		postCloseErr:          verifyErr,
	}
	page := newBlockedPageCloseFixture(t, client)
	manager := NewManager(nil, WithWarmPageTTL(0))
	observed := make(chan proto.TargetTargetDestroyed, 2)
	destroyed, stop := watchPageTargetDestroyedWithObserver(page, func(event proto.TargetTargetDestroyed) {
		observed <- event
	})
	defer stop()
	manager.mu.Lock()
	pending := manager.retainResetPageLocked(page, nil)
	stopPendingPageTargetWatchLocked(pending)
	pending.targetDestroyed, pending.stopTargetWatch = destroyed, stop
	manager.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		result <- manager.ClosePageContext(ctx, page)
	}()
	t.Cleanup(func() {
		cancel()
		stop()
		select {
		case <-joined:
		case <-time.After(4 * time.Second):
			t.Error("wrong-target close worker did not join")
		}
	})
	select {
	case <-client.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("production Page.close did not start")
	}
	close(client.closeRelease)
	select {
	case <-client.postCloseQueryStarted:
	case <-time.After(time.Second):
		t.Fatal("production verify query did not start")
	}
	if err := client.emitTargetDestroyed(ctx, "unrelated-target"); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-observed:
		if event.TargetID != "unrelated-target" {
			t.Fatalf("unexpected watcher acknowledgement: %s", event.TargetID)
		}
	case <-time.After(time.Second):
		t.Fatal("production watcher did not acknowledge consuming the wrong target")
	}
	if pendingTargetDestroyed(pending) || pendingPageForTest(manager, page) != pending {
		t.Fatal("consumed wrong-target event must retain the original pending responsibility")
	}
	close(client.postCloseQueryRelease)
	select {
	case err := <-result:
		if !errors.Is(err, verifyErr) {
			t.Fatalf("wrong target must not mask actual verification failure: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("production close did not return its verification failure")
	}
	if pendingPageForTest(manager, page) != pending || !errors.Is(pending.err, verifyErr) || pendingTargetDestroyed(pending) {
		t.Fatal("unknown target state must retain the record and error after consuming the wrong event")
	}
	if err := client.destroyTarget(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-pending.targetDestroyed:
	case <-time.After(time.Second):
		t.Fatal("matching target event was not consumed")
	}
	if err := manager.ClosePageContext(ctx, page); err != nil {
		t.Fatalf("matching event must allow cleanup retry: %v", err)
	}
	if pendingPageForTest(manager, page) != nil || client.calls() != 1 || client.queries() != 2 {
		t.Fatal("matching terminal evidence must retire the record without another close or query")
	}
}

func TestEndedTargetEventStreamDoesNotRetirePendingPage(t *testing.T) {
	verifyErr := errors.New("injected post-close Target.getTargets verification failure")
	client := &blockedPageCloseCDPClient{
		closeStarted:          make(chan struct{}, 1),
		closeRelease:          make(chan struct{}, 1),
		closeReleaseAck:       make(chan struct{}, 1),
		closeResponsePermit:   make(chan struct{}, 1),
		closeResponded:        make(chan struct{}, 1),
		failAfterClose:        true,
		postCloseQueryStarted: make(chan struct{}),
		postCloseQueryPermit:  make(chan struct{}, 1),
		postCloseQueryRelease: make(chan struct{}, 1),
		postCloseErr:          verifyErr,
	}
	page := newBlockedPageCloseFixture(t, client)
	manager := NewManager(nil, WithWarmPageTTL(0))
	manager.mu.Lock()
	pending := manager.retainResetPageLocked(page, page.Browser())
	manager.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	closeResult := make(chan error, 1)
	closeDone := make(chan struct{})
	go func() {
		closeResult <- manager.ClosePageContext(ctx, page)
		close(closeDone)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case client.closeRelease <- struct{}{}:
		default:
		}
		select {
		case client.closeResponsePermit <- struct{}{}:
		default:
		}
		select {
		case client.postCloseQueryPermit <- struct{}{}:
		default:
		}
		select {
		case client.postCloseQueryRelease <- struct{}{}:
		default:
		}
		client.Close()
		select {
		case <-closeDone:
		case <-time.After(4 * time.Second):
			t.Errorf("production close worker did not join within watchdog")
		}
	})
	select {
	case <-client.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("production Page.close did not reach the CDP client")
	}
	select {
	case client.closeRelease <- struct{}{}:
	case <-ctx.Done():
		t.Fatalf("production Page.close release send exceeded context: %v", ctx.Err())
	case <-time.After(time.Second):
		t.Fatal("production Page.close release send exceeded watchdog")
	}
	select {
	case <-client.closeReleaseAck:
	case <-ctx.Done():
		t.Fatalf("CDP fixture did not acknowledge reading Page.close release: %v", ctx.Err())
	case <-time.After(time.Second):
		t.Fatal("CDP fixture did not acknowledge reading Page.close release within watchdog")
	}
	client.Close()
	select {
	case client.closeResponsePermit <- struct{}{}:
	case <-ctx.Done():
		t.Fatalf("Page.close response permit send exceeded context: %v", ctx.Err())
	case <-time.After(time.Second):
		t.Fatal("Page.close response permit send exceeded watchdog")
	}
	select {
	case <-client.closeResponded:
	case <-ctx.Done():
		t.Fatalf("Page.close response did not reach the fixture: %v", ctx.Err())
	case <-time.After(time.Second):
		t.Fatal("Page.close response did not reach the fixture within watchdog")
	}
	select {
	case client.postCloseQueryPermit <- struct{}{}:
	case <-ctx.Done():
		t.Fatalf("verify stage permit send exceeded context: %v", ctx.Err())
	case <-time.After(time.Second):
		t.Fatal("verify stage permit send exceeded watchdog")
	}
	select {
	case <-client.postCloseQueryStarted:
	case <-ctx.Done():
		t.Fatalf("production verify Target.getTargets did not acknowledge its controlled stage: %v", ctx.Err())
	case <-time.After(time.Second):
		t.Fatal("production verify Target.getTargets did not acknowledge its controlled stage")
	}
	select {
	case client.postCloseQueryRelease <- struct{}{}:
	case <-ctx.Done():
		t.Fatalf("verify query release send exceeded context: %v", ctx.Err())
	case <-time.After(time.Second):
		t.Fatal("verify query release send exceeded watchdog")
	}
	select {
	case err := <-closeResult:
		if !errors.Is(err, verifyErr) {
			t.Fatalf("must return the actual verify query error and must not require Browser fallback: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("production close exceeded its context: %v", ctx.Err())
	case <-time.After(4 * time.Second):
		t.Fatal("production close/verify worker did not return within watchdog")
	}
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("production close result was returned before its worker completed")
	}
	if calls := client.calls(); calls != 1 {
		t.Fatalf("must reach exactly one production Page.close: calls=%d", calls)
	}
	if queries := client.queries(); queries != 2 {
		t.Fatalf("must run preflight and actual verify query: queries=%d", queries)
	}
	manager.mu.Lock()
	retained := false
	for _, current := range manager.resetPages {
		if current == pending {
			retained = true
			break
		}
	}
	pendingErr := pending.err
	manager.mu.Unlock()
	if !retained {
		t.Fatal("unknown verify result after event stream end must retain the original pending record")
	}
	if !errors.Is(pendingErr, verifyErr) {
		t.Fatalf("pending record must retain the actual verify error: %v", pendingErr)
	}
	select {
	case <-pending.targetDestroyed:
		t.Fatal("Page.close success response or event stream end must not fabricate targetDestroyed evidence")
	default:
	}
}

func TestUnknownTargetQueryDoesNotIncreasePageCloseCalls(t *testing.T) {
	client := &blockedPageCloseCDPClient{failPreflight: true}
	page := newBlockedPageCloseFixture(t, client)
	manager := NewManager(nil, WithWarmPageTTL(0))
	// A manager close flag and a browser without cleanup evidence are not success.
	unknownBrowser := hrod.NewBrowser(&headless_browser.Browser{}, humanize.Config{})
	manager.mu.Lock()
	manager.closed = true
	manager.retainResetPageLocked(page, unknownBrowser)
	manager.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := manager.ClosePageContext(ctx, page); err == nil {
		t.Fatal("preflight query 失败时应报告未知状态")
	}
	if calls := client.calls(); calls != 0 {
		t.Fatalf("target 状态未知时不得重发 Page.close: calls=%d", calls)
	}
	if pending := pendingPageForTest(manager, page); pending == nil {
		t.Fatal("target 状态未知时必须保留 pending 清理责任")
	}
	queriesBeforeRetry := client.queries()
	if err := manager.ClosePageContext(ctx, page); err == nil {
		t.Fatal("未知 target 状态的真实 retry 仍应报告查询错误")
	}
	if queries := client.queries(); queries <= queriesBeforeRetry {
		t.Fatalf("未知状态 retry 必须重新执行 target 查询: before=%d after=%d", queriesBeforeRetry, queries)
	}
	if calls := client.calls(); calls != 0 {
		t.Fatalf("未知状态 retry 不得发送 Page.close: calls=%d", calls)
	}
	if pending := pendingPageForTest(manager, page); pending == nil {
		t.Fatal("未知状态 retry 后仍必须保留 pending 清理责任")
	}
}

func TestOldPageCloseAfterResetDoesNotBlockAcquire(t *testing.T) {
	chromeBin := os.Getenv("ROD_BROWSER_BIN")
	if chromeBin == "" {
		var ok bool
		chromeBin, ok = launcher.LookPath()
		if !ok {
			t.Skip("未找到 Chrome/Chromium，跳过 Reset old-page retry fixture")
		}
	}
	profileDir := t.TempDir()
	manager := NewManager(func(ctx context.Context) (*hrod.Browser, error) {
		hb, err := headless_browser.New(ctx,
			headless_browser.WithChromeBinPath(chromeBin),
			headless_browser.WithHeadless(true),
			headless_browser.WithUserDataDir(profileDir),
			headless_browser.WithStealthJS(false),
		)
		if hb == nil {
			return nil, err
		}
		return hrod.NewBrowser(hb, humanize.Config{}), err
	}, WithWarmPageTTL(time.Minute))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Errorf("fixture cleanup: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	page, err := acquireWithWatchdog(manager, ctx, cancel, 40*time.Second)
	cancel()
	if err != nil || page == nil {
		t.Fatalf("first Acquire: page=%v err=%v", page, err)
	}
	oldBrowser := page.Browser()
	manager.Release(page)
	resetCtx, cancelReset := context.WithTimeout(context.Background(), 15*time.Second)
	err = manager.Reset(resetCtx)
	cancelReset()
	if err != nil || !oldBrowser.Rod().CleanupComplete() {
		t.Fatalf("Reset must confirm browser cleanup: %v", err)
	}
	// Install a new browser before retrying the old session's released-lease path.
	nextCtx, cancelNext := context.WithTimeout(context.Background(), 30*time.Second)
	next, err := acquireWithWatchdog(manager, nextCtx, cancelNext, 40*time.Second)
	cancelNext()
	if err != nil || next == nil {
		t.Fatalf("Acquire after Reset: page=%v err=%v", next, err)
	}
	manager.Release(next)
	for attempt := 0; attempt < 2; attempt++ {
		closeCtx, cancelClose := context.WithTimeout(context.Background(), time.Second)
		err = manager.ClosePageContext(closeCtx, page)
		cancelClose()
		if err != nil || pendingPageForTest(manager, page) != nil {
			t.Fatalf("old page retry must not query closed transport or retain pending: %v", err)
		}
	}
	if next.Browser() == oldBrowser || next.Browser().Rod().CleanupComplete() {
		t.Fatal("old page retry must not close the new browser")
	}
	retryCtx, cancelRetry := context.WithTimeout(context.Background(), 10*time.Second)
	retried, err := acquireWithWatchdog(manager, retryCtx, cancelRetry, 15*time.Second)
	cancelRetry()
	if err != nil || retried == nil {
		t.Fatalf("old page retry must not block subsequent Acquire: page=%v err=%v", retried, err)
	}
	manager.Release(retried)
}

// TestManagerStatsDefaults 计数与空闲回收时间的快照契约。
func TestBrowserResetErrorsAreScopedByBrowser(t *testing.T) {
	manager := NewManager(nil)
	oldBrowser, currentBrowser := &hrod.Browser{}, &hrod.Browser{}
	oldErr := errors.New("old browser cleanup failed")
	currentErr := errors.New("current browser cleanup failed")
	manager.mu.Lock()
	manager.setBrowserResetErrorLocked(oldBrowser, oldErr)
	manager.setBrowserResetErrorLocked(currentBrowser, currentErr)
	manager.setBrowserResetErrorLocked(oldBrowser, nil)
	remainingErr := manager.browserResetErrorLocked()
	manager.mu.Unlock()
	if !errors.Is(remainingErr, currentErr) || errors.Is(remainingErr, oldErr) {
		t.Fatalf("清除旧 browser 结果不得清除当前代错误: %v", remainingErr)
	}
}

func TestAcquireRetriesAfterCompletedStartupFailure(t *testing.T) {
	chromeBin := os.Getenv("ROD_BROWSER_BIN")
	if chromeBin == "" {
		var ok bool
		chromeBin, ok = launcher.LookPath()
		if !ok {
			t.Skip("未找到 Chrome/Chromium，跳过 Acquire startup retry fixture")
		}
	}
	startupCtx, cancelStartup := context.WithTimeout(context.Background(), 5*time.Second)
	failedHB, startupErr := headless_browser.New(startupCtx, headless_browser.WithChromeBinPath(filepath.Join(t.TempDir(), "missing-chrome")))
	cancelStartup()
	if startupErr == nil || failedHB == nil {
		t.Fatalf("invalid Chrome path must return its failed startup handle: browser=%v err=%v", failedHB, startupErr)
	}
	failedBrowser := hrod.NewBrowser(failedHB, humanize.Config{})
	cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelCleanup()
	cleanupPoll := time.NewTicker(10 * time.Millisecond)
	defer cleanupPoll.Stop()
	for !failedHB.CleanupComplete() {
		select {
		case <-cleanupCtx.Done():
			t.Fatalf("startup failure cleanup did not complete: %v", cleanupCtx.Err())
		case <-cleanupPoll.C:
		}
	}

	profileDir := t.TempDir()
	factoryCalls := 0
	manager := NewManager(func(ctx context.Context) (*hrod.Browser, error) {
		factoryCalls++
		if factoryCalls == 1 {
			return failedBrowser, startupErr
		}
		hb, err := headless_browser.New(ctx,
			headless_browser.WithChromeBinPath(chromeBin),
			headless_browser.WithHeadless(true),
			headless_browser.WithUserDataDir(profileDir),
			headless_browser.WithStealthJS(false),
		)
		if hb == nil {
			return nil, err
		}
		return hrod.NewBrowser(hb, humanize.Config{}), err
	}, WithWarmPageTTL(0))
	t.Cleanup(func() {
		closeCtx, cancelClose := context.WithTimeout(context.Background(), 15*time.Second)
		manager.mu.Lock()
		browsers := append([]*hrod.Browser(nil), manager.pendingBrowsers...)
		if manager.browser != nil {
			browsers = append(browsers, manager.browser)
		}
		manager.mu.Unlock()
		for _, b := range browsers {
			if err := b.CloseContext(closeCtx); err != nil {
				t.Errorf("startup retry browser fixture cleanup failed: %v", err)
			}
		}
		if err := manager.Close(closeCtx); err != nil {
			t.Errorf("startup retry manager cleanup failed: %v", err)
		}
		cancelClose()
	})
	firstCtx, cancelFirst := context.WithTimeout(context.Background(), 5*time.Second)
	first, err := acquireWithWatchdog(manager, firstCtx, cancelFirst, 10*time.Second)
	cancelFirst()
	if first != nil || !errors.Is(err, startupErr) {
		t.Fatalf("首次生产 Acquire 应返回 factory 错误: page=%v err=%v", first, err)
	}
	if !failedHB.CleanupComplete() {
		t.Fatal("首次失败句柄 cleanup 应已完成")
	}
	retryCtx, cancelRetry := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelRetry()
	page, err := acquireWithWatchdog(manager, retryCtx, cancelRetry, 40*time.Second)
	if err != nil || page == nil {
		t.Fatalf("完成失败清理后普通 Acquire 应允许重试成功: page=%v err=%v", page, err)
	}
	manager.Release(page)
	manager.mu.Lock()
	pendingCount := len(manager.pendingBrowsers)
	_, cleanupRetained := manager.browserCleanups[failedBrowser]
	manager.mu.Unlock()
	if pendingCount != 0 || cleanupRetained {
		t.Fatalf("已完成的失败句柄必须退休: pending=%d cleanupRetained=%v", pendingCount, cleanupRetained)
	}
	if factoryCalls != 2 {
		t.Fatalf("普通 Acquire 重试应调用 factory 两次，实际 %d 次", factoryCalls)
	}
}

func TestManagerStatsDefaults(t *testing.T) {
	m := NewManager(nil, WithIdleTimeout(30*time.Minute))
	stats := m.Stats()
	if stats.PagesCreated != 0 || stats.WarmPageReused != 0 || stats.WarmPageCached {
		t.Fatalf("初始计数应为零: %+v", stats)
	}
	if stats.IdleTimeoutSecs != int64((30 * time.Minute).Seconds()) {
		t.Fatalf("IdleTimeoutSecs = %d, 期望 1800", stats.IdleTimeoutSecs)
	}
}

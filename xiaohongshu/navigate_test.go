package xiaohongshu

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/launcher"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/headless_browser"
	"github.com/xpzouying/xiaohongshu-mcp/humanize"
	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
)

func navigateFixturePage(t *testing.T) *hrod.Page {
	t.Helper()
	bin := os.Getenv("ROD_BROWSER_BIN")
	if bin == "" {
		var ok bool
		bin, ok = launcher.LookPath()
		if !ok {
			t.Skip("未找到 Chrome/Chromium，跳过导航取消 fixture")
		}
	}
	// 复用生产环境的有界启动和清理，避免另写一套 launcher 生命周期。
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	instance, err := headless_browser.New(ctx,
		headless_browser.WithChromeBinPath(bin),
		headless_browser.WithStealthJS(false),
	)
	cancel()
	if instance != nil {
		t.Cleanup(func() {
			closeCtx, cancelClose := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancelClose()
			if err := instance.CloseContext(closeCtx); err != nil {
				t.Errorf("关闭 fixture 浏览器: %v", err)
			}
		})
	}
	if err != nil {
		t.Fatalf("启动 fixture 浏览器: %v", err)
	}
	page, err := hrod.NewBrowser(instance, humanize.Config{}).Page()
	if err != nil {
		t.Fatalf("创建 fixture 页面: %v", err)
	}
	return page
}

func navigateFixtureServer(t *testing.T, count *atomic.Int32) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/explore" {
			count.Add(1)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, "<!doctype html><div id='app'></div>")
	}))
	t.Cleanup(s.Close)
	return s
}

func TestPageCreationContextDetachesAfterSuccess(t *testing.T) {
	seed := navigateFixturePage(t)
	createCtx, cancelCreate := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelCreate()
	page, err := seed.Browser().PageContext(createCtx)
	if err != nil {
		t.Fatal(err)
	}
	cancelCreate()
	if err := page.Rod.GetContext().Err(); err != nil {
		t.Fatalf("returned page retained its creation context: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if result, err := page.Context(ctx).Eval("() => 42"); err != nil || result == nil || result.Value.Int() != 42 {
		t.Fatalf("page unusable after creation cancellation: result=%v err=%v", result, err)
	}
	if err := page.CloseContext(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureReadyOnCancelledContextDoesNotNavigate(t *testing.T) {
	p := navigateFixturePage(t)
	var count atomic.Int32
	s := navigateFixtureServer(t, &count)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := EnsureReadyOn(p.Context(ctx), s.URL+"/explore", XHSReadyHome, time.Second)
	if !errors.Is(err, context.Canceled) || count.Load() != 0 {
		t.Fatalf("取消请求应原样返回且不导航: err=%v requests=%d", err, count.Load())
	}
}

type quickConfirmGateHook struct {
	reached chan struct{}
	release chan struct{}
	message string
	once    sync.Once
}

func (h *quickConfirmGateHook) Levels() []logrus.Level { return logrus.AllLevels }

func (h *quickConfirmGateHook) Fire(entry *logrus.Entry) error {
	if strings.Contains(entry.Message, "phase=quick_confirm end") {
		h.once.Do(func() {
			h.message = entry.Message
			close(h.reached)
			select {
			case <-h.release:
			case <-time.After(10 * time.Second):
			}
		})
	}
	return nil
}

func installQuickConfirmGate(t *testing.T) *quickConfirmGateHook {
	t.Helper()
	logger := logrus.StandardLogger()
	oldLevel, oldOut := logger.GetLevel(), logger.Out
	oldHooks := logger.ReplaceHooks(logrus.LevelHooks{})
	logger.SetLevel(logrus.DebugLevel)
	logger.SetOutput(io.Discard)
	hook := &quickConfirmGateHook{reached: make(chan struct{}), release: make(chan struct{})}
	logger.AddHook(hook)
	t.Cleanup(func() {
		logger.SetLevel(oldLevel)
		logger.SetOutput(oldOut)
		logger.ReplaceHooks(oldHooks)
	})
	return hook
}

func TestEnsureReadyOnCancellationDuringQuickConfirmDoesNotReload(t *testing.T) {
	p := navigateFixturePage(t)
	var count atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/explore" {
			count.Add(1)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, "<!doctype html><div id='app'><section class='note-item'></section></div>")
	}))
	t.Cleanup(s.Close)
	target := s.URL + "/explore"
	if err := navigateFixtureTo(p, target); err != nil {
		t.Fatalf("准备目标 URL: %v", err)
	}
	if err := navigateFixtureWaitLoad(p); err != nil {
		t.Fatalf("等待 fixture: %v", err)
	}

	logger := logrus.StandardLogger()
	oldLevel, oldOut := logger.GetLevel(), logger.Out
	oldHooks := logger.ReplaceHooks(logrus.LevelHooks{})
	logger.SetLevel(logrus.DebugLevel)
	logger.SetOutput(io.Discard)
	hook := &quickConfirmGateHook{reached: make(chan struct{}), release: make(chan struct{})}
	logger.AddHook(hook)
	t.Cleanup(func() {
		logger.SetLevel(oldLevel)
		logger.SetOutput(oldOut)
		logger.ReplaceHooks(oldHooks)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- EnsureReadyOn(p.Context(ctx), target, XHSReadyHome, time.Second) }()
	select {
	case <-hook.reached:
		if !strings.Contains(hook.message, "ready=true err=<nil>") {
			close(hook.release)
			t.Fatalf("同步点必须位于成功的 ready probe 返回后: %s", hook.message)
		}
		cancel()
		close(hook.release)
	case <-time.After(3 * time.Second):
		close(hook.release)
		t.Fatal("未在 ready quick-confirm 返回边界触发同步点")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ready probe 返回时已取消的请求应返回 context.Canceled: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("取消后快速确认未及时结束")
	}
	if got := count.Load(); got != 1 {
		t.Fatalf("快速确认取消后不得再次导航，请求数=%d", got)
	}
}

func TestEnsureReadyOnCancellationAfterFailedQuickConfirmDoesNotNavigate(t *testing.T) {
	p := navigateFixturePage(t)
	var count atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/explore" {
			count.Add(1)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, "<!doctype html><div id='app'></div>")
	}))
	t.Cleanup(s.Close)
	target := s.URL + "/explore"
	if err := navigateFixtureTo(p, target); err != nil {
		t.Fatalf("准备目标 URL: %v", err)
	}
	if err := navigateFixtureWaitLoad(p); err != nil {
		t.Fatalf("等待 fixture: %v", err)
	}
	hook := installQuickConfirmGate(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- EnsureReadyOn(p.Context(ctx), target, XHSReadyHome, time.Second) }()
	select {
	case <-hook.reached:
		if strings.Contains(hook.message, "ready=true err=<nil>") {
			close(hook.release)
			t.Fatalf("同步点必须覆盖 quick-confirm 失败出口: %s", hook.message)
		}
		cancel()
		close(hook.release)
	case <-time.After(8 * time.Second):
		close(hook.release)
		t.Fatal("未在失败的 quick-confirm 返回边界触发同步点")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("失败 probe 返回时已取消的请求应返回 context.Canceled: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("失败 quick-confirm 取消后未及时结束")
	}
	if got := count.Load(); got != 1 {
		t.Fatalf("失败 quick-confirm 取消后不得再次导航，请求数=%d", got)
	}
}

func TestEnsureReadyOnCancellationDuringBlockedQuickConfirmDoesNotNavigate(t *testing.T) {
	p := navigateFixturePage(t)
	var count atomic.Int32
	probeStarted := make(chan struct{}, 1)
	probeRelease := make(chan struct{}, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/explore" {
			count.Add(1)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprint(w, "<!doctype html><div id='app'></div>")
			return
		}
		if r.URL.Path == "/probe-start" {
			select {
			case probeStarted <- struct{}{}:
			default:
			}
			select {
			case <-probeRelease:
			case <-r.Context().Done():
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(s.Close)
	target := s.URL + "/explore"
	if err := navigateFixtureTo(p, target); err != nil {
		t.Fatalf("准备目标 URL: %v", err)
	}
	if err := navigateFixtureWaitLoad(p); err != nil {
		t.Fatalf("等待 fixture: %v", err)
	}
	if err := navigateFixtureEval(p, `() => {
		Object.defineProperty(document.body, "innerText", {configurable: true, get() {
			const request = new XMLHttpRequest();
			request.open("GET", "/probe-start", false);
			request.send();
			return "";
		}});
	}`); err != nil {
		t.Fatalf("安装阻塞 probe getter: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- EnsureReadyOn(p.Context(ctx), target, XHSReadyHome, time.Second) }()
	select {
	case <-probeStarted:
		select {
		case err := <-done:
			probeRelease <- struct{}{}
			t.Fatalf("探测请求仍被服务端闸门阻塞时操作已提前返回: %v", err)
		default:
		}
		cancel()
		probeRelease <- struct{}{}
	case <-time.After(3 * time.Second):
		cancel()
		probeRelease <- struct{}{}
		t.Fatal("quick-confirm probe 未进入阻塞 getter")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("阻塞 probe 期间取消应返回 context.Canceled: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("阻塞 probe 取消后未及时结束")
	}
	if got := count.Load(); got != 1 {
		t.Fatalf("阻塞 probe 取消后不得再次导航，请求数=%d", got)
	}
}

func TestEnsureReadyOnUnreadyPageNavigatesOnce(t *testing.T) {
	p := navigateFixturePage(t)
	var count atomic.Int32
	s := navigateFixtureServer(t, &count)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := EnsureReadyOn(p.Context(ctx), s.URL+"/explore", XHSReadyHome, 200*time.Millisecond)
	if err == nil || count.Load() != 1 {
		t.Fatalf("未就绪路径应失败且只导航一次: err=%v requests=%d", err, count.Load())
	}
}
func navigateFixtureTo(page *hrod.Page, target string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- page.Context(ctx).Rod.Navigate(target) }()
	select {
	case err := <-result:
		return err
	case <-time.After(4 * time.Second):
		return fmt.Errorf("导航 fixture 超过 watchdog")
	}
}

func navigateFixtureWaitLoad(page *hrod.Page) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- page.Context(ctx).Rod.WaitLoad() }()
	select {
	case err := <-result:
		return err
	case <-time.After(4 * time.Second):
		return fmt.Errorf("等待 fixture 加载超过 watchdog")
	}
}

func navigateFixtureEval(page *hrod.Page, script string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := page.Context(ctx).Rod.Eval(script)
		result <- err
	}()
	select {
	case err := <-result:
		return err
	case <-time.After(4 * time.Second):
		return fmt.Errorf("fixture Eval 超过 watchdog")
	}
}

func TestEnsureReadyOnSlowTargetKeepsOriginalPage(t *testing.T) {
	page := navigateFixturePage(t)
	var navigations atomic.Int32
	server := navigateFixtureServer(t, &navigations)
	target := server.URL + "/explore"
	if err := navigateFixtureTo(page, target); err != nil {
		t.Fatal(err)
	}
	if err := navigateFixtureWaitLoad(page); err != nil {
		t.Fatal(err)
	}
	hook := installQuickConfirmGate(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- EnsureReadyOn(page.Context(ctx), target, XHSReadyHome, 5*time.Second) }()
	select {
	case <-hook.reached:
		if strings.Contains(hook.message, "ready=true") {
			close(hook.release)
			t.Fatal("fixture must exhaust the quick confirmation")
		}
		_, err := page.Context(ctx).Eval(`() => { const card = document.createElement('section'); card.className = 'note-item'; document.querySelector('#app').appendChild(card); }`)
		close(hook.release)
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		close(hook.release)
		t.Fatal("slow confirmation never reached the boundary")
	}
	select {
	case err := <-done:
		if err != nil || navigations.Load() != 1 {
			t.Fatalf("slow target was reloaded or rejected: navigations=%d err=%v", navigations.Load(), err)
		}
	case <-ctx.Done():
		t.Fatal("continued target wait did not finish")
	}
}

func TestHomeFeedRouteAcceptsBothEntryPointsButRejectsDetails(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want bool
	}{
		{HomeURL, true},
		{ExploreURL + "?channel=home", true},
		{ExploreURL + "/note-id", false},
		{"https://www.xiaohongshu.com/search_result?keyword=same", false},
		{"https://other.example/explore", false},
	} {
		client := &currentPageURLCDPClient{response: []byte(fmt.Sprintf(`{"targetInfo":{"targetId":"target-1","url":%q}}`, tc.url))}
		session := newCurrentPageURLSession(t, client)
		got, err := onHomeFeedPage(session.page)
		if err != nil || got != tc.want || client.runtimeCall != 0 {
			t.Fatalf("home route %q: got=%t err=%v", tc.url, got, err)
		}
	}
}

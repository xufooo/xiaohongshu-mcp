package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/xpzouying/headless_browser"
	"github.com/xpzouying/xiaohongshu-mcp/browser"
	"github.com/xpzouying/xiaohongshu-mcp/humanize"
	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
	"github.com/xpzouying/xiaohongshu-mcp/xiaohongshu"
)

const serviceCommentFeedID = "5f4d8e7b00000000010001a2"
const serviceCommentStateKey = "service-comment-production-path"

func serviceCommentHTML(feedID string, search bool) string {
	if search {
		return strings.ReplaceAll(`<!doctype html><meta charset="utf-8"><style>body{margin:0}section.note-item a.cover.mask.ld{display:block;width:420px;height:300px;background:#ddd}</style><section class="note-item"><a class="cover mask ld" data-feed-id="{{feedID}}" href="/explore/{{feedID}}?xsec_token=token">open</a></section>`, "{{feedID}}", feedID)
	}
	return strings.ReplaceAll(`<!doctype html><meta charset="utf-8"><style>.note-container{height:80px;overflow-y:auto}</style><div class="note-detail-mask"><div class="note-container"><div class="note-content"><div id="detail-title">Test Note Title</div><div class="author-wrapper"><span class="name">note author</span><a href="/user/profile/user-1">author</a></div><div id="detail-desc">state description text</div><div class="swiper"><img src="https://example.com/img1.jpg"></div></div><div class="interact-container"></div><div class="comments-container"><span class="total">共 7 条评论</span></div><div class="parent-comment" data-id="parent-1"><div class="comment-item" id="parent-1"><div class="content">parent content</div><div class="author-wrapper"><span class="name">parent user</span></div><div class="interactions"><span class="like">1</span></div></div><div class="reply-container"><div class="list-container"><div class="comment-item" id="reply-1"><div class="content">reply one</div></div><div class="comment-item" id="reply-2"><div class="content">reply two</div></div><div class="comment-item" id="reply-3"><div class="content">reply three</div></div></div></div></div><div class="end-container">THE END</div></div></div><script>window.__INITIAL_STATE__={note:{currentNoteId:"{{feedID}}",noteDetailMap:{"{{feedID}}":{note:{noteId:"{{feedID}}",title:"Test Note Title",desc:"state description text",user:{userId:"user-1",nickname:"note author"},interactInfo:{liked:false,collected:false,commentCount:"7"},imageList:[{urlDefault:"https://example.com/img1.jpg"}]}}}}};</script>`, "{{feedID}}", feedID)
}

func runFixtureCloseWithWatchdog(timeout, joinTimeout time.Duration, closeFn func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	done := make(chan error, 1)
	go func() { done <- closeFn(ctx) }()
	watchdog := time.NewTimer(timeout)
	defer watchdog.Stop()
	select {
	case err := <-done:
		cancel()
		return err
	case <-watchdog.C:
		cancel()
		watchdogErr := fmt.Errorf("fixture cleanup exceeded independent %s watchdog", timeout)
		join := time.NewTimer(joinTimeout)
		defer join.Stop()
		select {
		case err := <-done:
			return errors.Join(watchdogErr, err)
		case <-join.C:
			return errors.Join(watchdogErr, fmt.Errorf("fixture cleanup did not join within %s after cancellation", joinTimeout))
		}
	}
}

func acquireForWithWatchdog(manager *browser.Manager, profileDir string, ctx context.Context, cancel context.CancelFunc, owner string, watchdog time.Duration) (*hrod.Page, error) {
	type result struct {
		page *hrod.Page
		err  error
	}
	done := make(chan result, 1)
	go func() {
		page, err := manager.AcquireFor(ctx, owner)
		done <- result{page: page, err: err}
	}()
	watchdogTimer := time.NewTimer(watchdog)
	defer watchdogTimer.Stop()
	select {
	case acquired := <-done:
		return acquired.page, acquired.err
	case <-ctx.Done():
	case <-watchdogTimer.C:
	}
	select {
	case acquired := <-done:
		return acquired.page, acquired.err
	default:
	}
	cause := ctx.Err()
	if cause == nil {
		cause = fmt.Errorf("AcquireFor %s exceeded watchdog %s", owner, watchdog)
	}
	cancel()
	var processes []isolatedProcessIdentity
	var cleanupErr error
	if runtime.GOOS == "linux" {
		processes, cleanupErr = isolatedBrowserProcessesForProfile(profileDir)
	}
	closeErr := runFixtureCloseWithWatchdog(10*time.Second, 2*time.Second, manager.Close)
	cleanupErr = errors.Join(cleanupErr, closeErr)
	if runtime.GOOS == "linux" {
		late, scanErr := isolatedBrowserProcessesForProfile(profileDir)
		cleanupErr = errors.Join(cleanupErr, scanErr)
		processes = mergeIsolatedProcessIdentities(processes, late)
		cleanupErr = errors.Join(cleanupErr, killTrackedIsolatedBrowserProcesses(processes), killIsolatedBrowserProcesses(profileDir))
	}
	var lateErr error
	select {
	case acquired := <-done:
		if acquired.page != nil {
			releaseCtx, cancelRelease := context.WithTimeout(context.Background(), 2*time.Second)
			lateErr = manager.ReleaseAndCloseContext(releaseCtx, acquired.page)
			cancelRelease()
		}
		if acquired.err != nil {
			lateErr = errors.Join(lateErr, acquired.err)
		}
	case <-time.After(5 * time.Second):
		lateErr = errors.New("AcquireFor remained blocked after manager and browser cleanup")
	}
	return nil, errors.Join(cause, cleanupErr, lateErr)
}

func assertServiceBusyOwnerWithWatchdog(t *testing.T, manager *browser.Manager, owner string) {
	t.Helper()
	type acquireResult struct {
		page *hrod.Page
		err  error
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	done := make(chan acquireResult, 1)
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		page, err := manager.AcquireFor(ctx, "n1-contender")
		done <- acquireResult{page: page, err: err}
	}()
	watchdog := time.NewTimer(10 * time.Second)
	defer watchdog.Stop()
	var acquired acquireResult
	watchdogExpired := false
	select {
	case acquired = <-done:
	case <-watchdog.C:
		watchdogExpired = true
	}
	if watchdogExpired {
		cancel()
		select {
		case acquired = <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("AcquireFor contender 未在 watchdog 取消后 join")
		}
	}
	joinTimer := time.NewTimer(2 * time.Second)
	select {
	case <-joined:
		joinTimer.Stop()
	case <-joinTimer.C:
		cancel()
		if acquired.page != nil {
			if err := releaseAndCloseWithWatchdog(manager, acquired.page, 5*time.Second, 7*time.Second); err != nil {
				t.Fatalf("意外 Acquire page 单次归还失败: %v", err)
			}
		}
		t.Fatalf("AcquireFor contender 结果返回后未 join")
	}
	callerValid := ctx.Err() == nil
	cancel()
	if acquired.page != nil {
		if err := releaseAndCloseWithWatchdog(manager, acquired.page, 5*time.Second, 7*time.Second); err != nil {
			t.Fatalf("意外 Acquire page 单次归还失败: %v", err)
		}
	}
	if watchdogExpired {
		t.Fatalf("AcquireFor contender 超过独立 10s watchdog: err=%v", acquired.err)
	}
	if !callerValid {
		t.Fatalf("AcquireFor caller 在结果返回前已失效: %v", ctx.Err())
	}
	if acquired.page != nil {
		t.Fatalf("lease 仍由 %q 持有时 contender 意外取得 page", owner)
	}
	var busy *browser.BusyError
	if !errors.As(acquired.err, &busy) || busy.Owner != owner {
		t.Fatalf("lease BusyError owner 不匹配: want=%q err=%v", owner, acquired.err)
	}
}

func newServiceCommentBrowserFactory(t *testing.T) browser.BrowserFactory {
	return newServiceBrowserFactory(t, nil, nil, nil, nil)
}

func newIsolatedProcessBrowserFactory(t *testing.T, profileDir string) browser.BrowserFactory {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("isolated browser process fixture requires Linux /proc")
	}
	bin := os.Getenv("ROD_BROWSER_BIN")
	if bin == "" {
		var ok bool
		bin, ok = launcher.LookPath()
		if !ok {
			t.Skip("未找到 Chrome/Chromium，跳过隔离进程 fixture")
		}
	}
	return func(ctx context.Context) (*hrod.Browser, error) {
		instance, err := headless_browser.New(ctx,
			headless_browser.WithChromeBinPath(bin),
			headless_browser.WithHeadless(true),
			headless_browser.WithUserDataDir(profileDir),
			headless_browser.WithStealthJS(false),
		)
		if err != nil {
			if instance == nil {
				return nil, err
			}
			return hrod.NewBrowser(instance, humanize.Config{}), err
		}
		return hrod.NewBrowser(instance, humanize.Config{}), nil
	}
}

func newServiceStartPageBrowserFactory(t *testing.T, refreshStarted, refreshRelease chan struct{}) browser.BrowserFactory {
	return newServiceBrowserFactory(t, refreshStarted, refreshRelease, nil, nil)
}

func newServiceInitialRefreshBrowserFactory(t *testing.T, gate *serviceInitialRefreshGate, profileDir string) browser.BrowserFactory {
	factory := newServiceBrowserFactoryFixture(t, nil, nil, nil, nil, true, profileDir)
	return func(ctx context.Context) (*hrod.Browser, error) {
		instance, err := factory(ctx)
		if err != nil || instance == nil {
			return instance, err
		}
		page, err := instance.Page()
		if err != nil {
			return instance, fmt.Errorf("创建初始刷新 CDP fixture page: %w", err)
		}
		rodBrowser := page.Rod.Browser()
		if rodBrowser == nil {
			closeCtx, cancelClose := context.WithTimeout(context.Background(), 5*time.Second)
			closeErr := page.CloseContext(closeCtx)
			cancelClose()
			return instance, errors.Join(errors.New("初始刷新 CDP fixture page 缺少 Rod Browser"), closeErr)
		}
		upstream := rodBrowser.Context(rodBrowser.GetContext())
		rodBrowser.Client(&serviceInitialRefreshGateClient{upstream: upstream, gate: gate})
		closeCtx, cancelClose := context.WithTimeout(context.Background(), 5*time.Second)
		closeErr := page.CloseContext(closeCtx)
		cancelClose()
		if closeErr != nil {
			return instance, fmt.Errorf("关闭初始刷新 CDP fixture page: %w", closeErr)
		}
		return instance, nil
	}
}

func newServiceReuseBrowserFactory(t *testing.T, reuseStarted, reuseRelease chan struct{}) browser.BrowserFactory {
	return newServiceBrowserFactory(t, nil, nil, reuseStarted, reuseRelease)
}

func newServiceBrowserFactory(t *testing.T, refreshStarted, refreshRelease, reuseStarted, reuseRelease chan struct{}, profileDirs ...string) browser.BrowserFactory {
	return newServiceBrowserFactoryFixture(t, refreshStarted, refreshRelease, reuseStarted, reuseRelease, refreshStarted != nil || reuseStarted != nil, profileDirs...)
}

func newServiceBrowserFactoryFixture(t *testing.T, refreshStarted, refreshRelease, reuseStarted, reuseRelease chan struct{}, serveBrowseHome bool, profileDirs ...string) browser.BrowserFactory {
	t.Helper()
	profileDir := ""
	if len(profileDirs) > 0 {
		profileDir = profileDirs[0]
	} else {
		profileDir = t.TempDir()
	}
	var trackedMu sync.Mutex
	var trackedProcesses []isolatedProcessIdentity
	type profileProcessCapture struct {
		stop    chan struct{}
		done    chan struct{}
		ready   chan error
		once    sync.Once
		scanMu  sync.Mutex
		scanErr error
	}
	var capturesMu sync.Mutex
	var captures []*profileProcessCapture
	t.Cleanup(func() {
		capturesMu.Lock()
		activeCaptures := append([]*profileProcessCapture(nil), captures...)
		for _, capture := range activeCaptures {
			capture.once.Do(func() { close(capture.stop) })
		}
		capturesMu.Unlock()
		for _, capture := range activeCaptures {
			select {
			case <-capture.done:
			case <-time.After(5 * time.Second):
				t.Errorf("service fixture PID/starttime capture 未在清理 watchdog 内退出")
			}
		}
		trackedMu.Lock()
		trackedSnapshot := append([]isolatedProcessIdentity(nil), trackedProcesses...)
		trackedMu.Unlock()
		currentProcesses, err := isolatedBrowserProcessesForProfile(profileDir)
		if err != nil {
			t.Errorf("service fixture 清场前扩展 browser 进程集合失败: %v", err)
		} else {
			trackedSnapshot = mergeIsolatedProcessIdentities(trackedSnapshot, currentProcesses)
		}
		if err := killTrackedIsolatedBrowserProcesses(trackedSnapshot); err != nil {
			t.Errorf("service fixture 已跟踪 browser 清理失败: %v", err)
		}
		if err := killIsolatedBrowserProcesses(profileDir); err != nil {
			t.Errorf("service fixture 独立 browser 清理失败: %v", err)
		}
	})
	bin := os.Getenv("ROD_BROWSER_BIN")
	if bin == "" {
		var ok bool
		bin, ok = launcher.LookPath()
		if !ok {
			t.Skip("未找到 Chrome/Chromium，跳过 service 评论生产路径 fixture")
		}
	}
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if refreshStarted != nil && r.URL.Path == "/block-refresh" {
			select {
			case refreshStarted <- struct{}{}:
			default:
			}
			select {
			case <-refreshRelease:
			case <-r.Context().Done():
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if reuseStarted != nil && r.URL.Path == "/block-reuse" {
			select {
			case reuseStarted <- struct{}{}:
			default:
			}
			select {
			case <-reuseRelease:
			case <-r.Context().Done():
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if (serveBrowseHome || refreshStarted != nil || reuseStarted != nil) && r.URL.Path == "/explore" {
			script := ""
			if refreshStarted != nil || reuseStarted != nil {
				script = "const stringify=JSON.stringify;JSON.stringify=function(value,...args){"
				if refreshStarted != nil {
					script += `if(value&&typeof value==="object"&&Object.keys(value).sort().join(",")==="scroll_y,url"){const request=new XMLHttpRequest();request.open("GET","/block-refresh",false);request.send()}`
				}
				if reuseStarted != nil {
					script += `if(value&&typeof value==="object"&&Object.keys(value).sort().join(",")==="readyState,url"){const request=new XMLHttpRequest();request.open("GET","/block-reuse",false);request.send()}`
				}
				script += `return stringify.call(JSON,value,...args)}`
			}
			_, _ = fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><div id="app"><input id="search-input-in-feeds"><section class="note-item"></section></div><script>`+script+`</script>`)
			return
		}
		if strings.Contains(r.URL.Path, "search_result") {
			_, _ = w.Write([]byte(serviceCommentHTML(serviceCommentFeedID, true)))
			return
		}
		_, _ = w.Write([]byte(serviceCommentHTML(serviceCommentFeedID, false)))
	}))
	t.Cleanup(origin.Close)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT required", http.StatusMethodNotAllowed)
			return
		}
		client, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		upstream, err := net.Dial("tcp", origin.Listener.Addr().String())
		if err != nil {
			_, _ = rw.WriteString("HTTP/1.1 502 Bad Gateway\r\n\r\n")
			_ = rw.Flush()
			_ = client.Close()
			return
		}
		_, _ = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		if rw.Flush() != nil {
			_ = client.Close()
			_ = upstream.Close()
			return
		}
		go func() {
			_, _ = io.Copy(upstream, client)
			_ = upstream.Close()
		}()
		_, _ = io.Copy(client, upstream)
		_ = client.Close()
		_ = upstream.Close()
	}))
	t.Cleanup(proxy.Close)
	proxyAddr := strings.TrimPrefix(proxy.URL, "http://")
	return func(ctx context.Context) (*hrod.Browser, error) {
		options := []headless_browser.Option{
			headless_browser.WithChromeBinPath(bin),
			headless_browser.WithProxy(proxyAddr),
			headless_browser.WithStealthJS(false),
			headless_browser.WithExtraArgs([]string{"--ignore-certificate-errors"}),
		}
		options = append(options, headless_browser.WithUserDataDir(profileDir))
		capture := &profileProcessCapture{stop: make(chan struct{}), done: make(chan struct{}), ready: make(chan error, 1)}
		capturesMu.Lock()
		captures = append(captures, capture)
		capturesMu.Unlock()
		go func() {
			defer close(capture.done)
			ticker := time.NewTicker(50 * time.Millisecond)
			defer ticker.Stop()
			firstScan := true
			for {
				processes, scanErr := isolatedBrowserProcessesForProfile(profileDir)
				if scanErr == nil {
					trackedMu.Lock()
					trackedProcesses = mergeIsolatedProcessIdentities(trackedProcesses, processes)
					trackedMu.Unlock()
				} else {
					capture.scanMu.Lock()
					if capture.scanErr == nil {
						capture.scanErr = scanErr
					}
					capture.scanMu.Unlock()
				}
				if firstScan {
					capture.ready <- scanErr
					firstScan = false
				}
				select {
				case <-capture.stop:
					return
				case <-ticker.C:
				}
			}
		}()
		select {
		case <-capture.ready:
		case <-time.After(2 * time.Second):
			capture.once.Do(func() { close(capture.stop) })
			select {
			case <-capture.done:
			case <-time.After(2 * time.Second):
				t.Errorf("service fixture PID/starttime sampler 未在失败清场 watchdog 内退出")
			}
			return nil, errors.New("service fixture PID/starttime sampler did not become ready before browser startup")
		}
		b, err := headless_browser.New(ctx, options...)
		capture.once.Do(func() { close(capture.stop) })
		select {
		case <-capture.done:
		case <-time.After(2 * time.Second):
			t.Errorf("service fixture PID/starttime capture 未在 New 返回后退出")
		}
		capture.scanMu.Lock()
		scanErr := capture.scanErr
		capture.scanMu.Unlock()
		if scanErr != nil {
			t.Errorf("service fixture PID/starttime 尽力采样失败（仅覆盖已发现身份，不证明完整后代树）: %v", scanErr)
		}
		if b != nil {
			processes, processErr := isolatedBrowserProcessesForProfile(profileDir)
			if processErr != nil {
				t.Errorf("记录 service fixture browser PID/starttime 集合失败: %v", processErr)
			} else {
				trackedMu.Lock()
				trackedProcesses = mergeIsolatedProcessIdentities(trackedProcesses, processes)
				trackedMu.Unlock()
			}
		}
		if err != nil {
			if b == nil {
				return nil, err
			}
			return hrod.NewBrowser(b, humanize.Config{}), err
		}
		return hrod.NewBrowser(b, humanize.Config{}), nil
	}
}

func newServiceCommentPage(t *testing.T, factory browser.BrowserFactory, url, profileDir string) *hrod.Page {
	t.Helper()
	type factoryResult struct {
		browser *hrod.Browser
		err     error
	}
	factoryCtx, cancelFactory := context.WithTimeout(context.Background(), 30*time.Second)
	factoryDone := make(chan factoryResult, 1)
	go func() {
		b, err := factory(factoryCtx)
		factoryDone <- factoryResult{browser: b, err: err}
	}()
	var created factoryResult
	select {
	case created = <-factoryDone:
		cancelFactory()
	case <-time.After(32 * time.Second):
		cancelFactory()
		select {
		case late := <-factoryDone:
			if late.browser != nil {
				closeCtx, cancelClose := context.WithTimeout(context.Background(), 10*time.Second)
				_ = late.browser.CloseContext(closeCtx)
				cancelClose()
			}
		case <-time.After(5 * time.Second):
			t.Errorf("fixture browser factory 在取消后仍未退出")
		}
		t.Fatal("启动 fixture 浏览器超过 watchdog")
	}
	b := created.browser
	if b != nil {
		t.Cleanup(func() {
			cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancelCleanup()
			if err := b.CloseContext(cleanupCtx); err != nil {
				t.Errorf("fixture browser 关闭失败: %v", err)
			}
		})
	}
	if created.err != nil {
		t.Fatalf("启动 fixture 浏览器: %v", created.err)
	}
	if b == nil {
		t.Fatal("fixture browser factory 未返回 browser")
	}
	type pageResult struct {
		page *hrod.Page
		err  error
	}
	pageDone := make(chan pageResult, 1)
	pageCompleted := false
	t.Cleanup(func() {
		var processes []isolatedProcessIdentity
		if runtime.GOOS == "linux" {
			var err error
			processes, err = isolatedBrowserProcessesForProfile(profileDir)
			if err != nil {
				t.Errorf("fixture page cleanup 前扩展 browser 进程集合失败: %v", err)
			}
		}
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 10*time.Second)
		if err := b.CloseContext(cleanupCtx); err != nil {
			t.Errorf("fixture page cleanup 关闭 browser 失败: %v", err)
		}
		cancelCleanup()
		if runtime.GOOS == "linux" {
			late, err := isolatedBrowserProcessesForProfile(profileDir)
			if err != nil {
				t.Errorf("fixture page cleanup 后扩展 browser 进程集合失败: %v", err)
			} else {
				processes = mergeIsolatedProcessIdentities(processes, late)
			}
			if err := killTrackedIsolatedBrowserProcesses(processes); err != nil {
				t.Errorf("fixture page cleanup 已跟踪 browser 清理失败: %v", err)
			}
			if err := killIsolatedBrowserProcesses(profileDir); err != nil {
				t.Errorf("fixture page cleanup browser 清理失败: %v", err)
			}
		}
		if !pageCompleted {
			select {
			case late := <-pageDone:
				if late.page != nil {
					pageCtx, cancelPage := context.WithTimeout(context.Background(), 2*time.Second)
					if err := late.page.CloseContext(pageCtx); err != nil {
						t.Errorf("迟到 fixture page 关闭失败: %v", err)
					}
					cancelPage()
				}
			case <-time.After(5 * time.Second):
				t.Errorf("fixture page creation 在 browser 清理后仍未结束")
			}
		}
	})
	go func() {
		page, err := b.Page()
		pageDone <- pageResult{page: page, err: err}
	}()
	var result pageResult
	select {
	case result = <-pageDone:
		pageCompleted = true
	case <-time.After(10 * time.Second):
		t.Fatal("创建 fixture 页面超过 watchdog")
	}
	if result.err != nil {
		t.Fatalf("创建 fixture 页面: %v", result.err)
	}
	page := result.page
	navigateCtx, cancelNavigate := context.WithTimeout(context.Background(), 10*time.Second)
	navigateDone := make(chan error, 1)
	go func() { navigateDone <- page.Rod.Context(navigateCtx).Navigate(url) }()
	select {
	case err := <-navigateDone:
		cancelNavigate()
		if err != nil {
			t.Fatalf("导航 fixture 页面: %v", err)
		}
	case <-time.After(12 * time.Second):
		cancelNavigate()
		t.Fatal("导航 fixture 页面超过 watchdog")
	}
	loadCtx, cancelLoad := context.WithTimeout(context.Background(), 10*time.Second)
	loadDone := make(chan error, 1)
	go func() { loadDone <- page.Rod.Context(loadCtx).WaitLoad() }()
	select {
	case err := <-loadDone:
		cancelLoad()
		if err != nil {
			t.Fatalf("等待 fixture 页面: %v", err)
		}
	case <-time.After(12 * time.Second):
		cancelLoad()
		t.Fatal("等待 fixture 页面超过 watchdog")
	}
	return page
}

func serviceCommentStatePath(root string) string {
	sum := sha256.Sum256([]byte(serviceCommentStateKey))
	return filepath.Join(root, hex.EncodeToString(sum[:])+".json")
}

func blockServiceCommentStateWrite(t *testing.T, root string) {
	t.Helper()
	path := serviceCommentStatePath(root)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
			t.Fatalf("初始化状态文件: %v", err)
		}
	} else if err != nil {
		t.Fatalf("读取状态文件状态: %v", err)
	}
	if err := os.Mkdir(path+".tmp", 0700); err != nil {
		t.Fatalf("构造状态落盘失败: %v", err)
	}
}

func serviceCommentScope(entryType, sessionID string, maxItems int, token string, config xiaohongshu.CommentLoadConfig) replayScope {
	scope := replayScope{
		EntryType:           entryType,
		SessionID:           sessionID,
		FeedID:              serviceCommentFeedID,
		MaxItems:            maxItems,
		ClickMoreReplies:    config.ClickMoreReplies,
		MaxRepliesThreshold: config.MaxRepliesThreshold,
		ScrollSpeed:         normalizeScopeSpeed(config.ScrollSpeed),
	}
	if entryType == "standalone" {
		scope.XsecDigest = xsecDigest(token)
	}
	return scope
}

func assertServiceCommentBatch(t *testing.T, detail *xiaohongshu.FeedDetailResponse, maxItems int) {
	t.Helper()
	if detail == nil || len(detail.Comments.List) == 0 || !detail.Comments.HasMore || detail.Comments.TotalItems != 7 || detail.Comments.SeenCount != 3 || detail.Comments.Complete || detail.Comments.IncompleteReason != "more_comments_available" || detail.Comments.Cursor == "" {
		t.Fatalf("分页/完成状态错误: %+v", detail)
	}
	if len(detail.Note.ImageList) != 1 || detail.Note.ImageList[0].URLDefault != "https://example.com/img1.jpg" {
		t.Fatalf("service 批次未保留已读图片: %+v", detail.Note.ImageList)
	}
	seen := make(map[string]bool)
	count := 0
	var visit func([]xiaohongshu.Comment)
	visit = func(comments []xiaohongshu.Comment) {
		for _, comment := range comments {
			count++
			if comment.ID == "" || strings.TrimSpace(comment.Content) == "" || seen[comment.ID] {
				t.Fatalf("批次包含空内容、无稳定 ID 或重复评论: %+v", comment)
			}
			seen[comment.ID] = true
			visit(comment.SubComments)
		}
	}
	visit(detail.Comments.List)
	if count == 0 || count > maxItems {
		t.Fatalf("批次数量超出 maxItems: count=%d max=%d", count, maxItems)
	}
}

func TestCommitCommentBatchResultUsesEffectiveCursorProgress(t *testing.T) {
	input := &xiaohongshu.CommentCursor{
		FeedID:      serviceCommentFeedID,
		ReturnedIDs: []string{"old-comment", "old-comment", "idx_legacy", ""},
	}
	scope := serviceCommentScope("standalone", "", 2, "token", xiaohongshu.DefaultCommentLoadConfig())

	t.Run("cleaned cursor still grows", func(t *testing.T) {
		service := &XiaohongshuService{}
		detail := &xiaohongshu.FeedDetailResponse{Comments: xiaohongshu.CommentList{List: []xiaohongshu.Comment{{ID: "new-comment"}}}}
		next := &xiaohongshu.CommentCursor{FeedID: serviceCommentFeedID, ReturnedIDs: []string{"old-comment", "new-comment", "new-comment", "idx_legacy"}}
		result, err := service.commitCommentBatchResult(serviceCommentFeedID, "input", input, detail, next, true, 0, nil, scope)
		if err != nil {
			t.Fatalf("有效去重 ID 增长应允许提交: %v", err)
		}
		if result == nil {
			t.Fatal("有效进展提交返回空结果")
		}
		got, ok := result.Data.(*xiaohongshu.FeedDetailResponse)
		if !ok || got.Comments.SeenCount != 2 || got.Comments.Cursor == "" {
			t.Fatalf("提交结果必须使用清洗后的 SeenCount 并保留续页 cursor: %+v", result)
		}
	})

	t.Run("cleaned cursor does not grow", func(t *testing.T) {
		service := &XiaohongshuService{}
		service.setCommentCursor("input", input, scope)
		wantInput := *input
		wantInput.ReturnedIDs = append([]string(nil), input.ReturnedIDs...)
		detail := &xiaohongshu.FeedDetailResponse{Comments: xiaohongshu.CommentList{List: []xiaohongshu.Comment{{ID: "old-comment"}}}}
		next := &xiaohongshu.CommentCursor{FeedID: serviceCommentFeedID, ReturnedIDs: []string{"old-comment", "idx_discarded", ""}}
		if result, err := service.commitCommentBatchResult(serviceCommentFeedID, "input", input, detail, next, true, 0, nil, scope); err == nil || result != nil {
			t.Fatalf("清洗后无增长仍必须 fail-closed: result=%+v err=%v", result, err)
		}
		value, ok := service.commentCursors.Load("input")
		entry, entryOK := value.(*commentCursorEntry)
		if !ok || !entryOK || entry == nil || !reflect.DeepEqual(entry.Cursor, &wantInput) {
			t.Fatalf("无增长拒绝后必须完整保留输入 cursor: got=%+v want=%+v", entry, &wantInput)
		}
		cursorCount := 0
		service.commentCursors.Range(func(key, value any) bool {
			cursorCount++
			return true
		})
		if cursorCount != 1 {
			t.Fatalf("无增长拒绝不得新增 cursor: count=%d", cursorCount)
		}
		replayCount := 0
		service.commentReplays.Range(func(key, value any) bool {
			replayCount++
			return true
		})
		if replayCount != 0 {
			t.Fatalf("无增长拒绝不得新增 replay: count=%d", replayCount)
		}
	})

	t.Run("effective ID replacement is rejected", func(t *testing.T) {
		service := &XiaohongshuService{}
		input := &xiaohongshu.CommentCursor{FeedID: serviceCommentFeedID, ReturnedIDs: []string{"old-a", "old-b"}}
		service.setCommentCursor("input", input, scope)
		wantInput := *input
		wantInput.ReturnedIDs = append([]string(nil), input.ReturnedIDs...)
		detail := &xiaohongshu.FeedDetailResponse{Comments: xiaohongshu.CommentList{List: []xiaohongshu.Comment{{ID: "new-c"}}}}
		next := &xiaohongshu.CommentCursor{FeedID: serviceCommentFeedID, ReturnedIDs: []string{"old-a", "new-c", "new-d"}}
		if result, err := service.commitCommentBatchResult(serviceCommentFeedID, "input", input, detail, next, true, 0, nil, scope); err == nil || result != nil {
			t.Fatalf("有效 ID 数量增加但丢失旧 ID 必须拒绝: result=%+v err=%v", result, err)
		}
		value, ok := service.commentCursors.Load("input")
		entry, entryOK := value.(*commentCursorEntry)
		if !ok || !entryOK || entry == nil || !reflect.DeepEqual(entry.Cursor, &wantInput) {
			t.Fatalf("替换拒绝后必须完整保留输入 cursor: got=%+v want=%+v", entry, &wantInput)
		}
		cursorCount := 0
		service.commentCursors.Range(func(key, value any) bool {
			cursorCount++
			return true
		})
		if cursorCount != 1 {
			t.Fatalf("替换拒绝不得新增 cursor: count=%d", cursorCount)
		}
		replayCount := 0
		service.commentReplays.Range(func(key, value any) bool {
			replayCount++
			return true
		})
		if replayCount != 0 {
			t.Fatalf("替换拒绝不得新增 replay: count=%d", replayCount)
		}
	})
}

func TestRunReadyWithColdStartRecoveryRetriesOnlyNarrowStall(t *testing.T) {
	stall := fmt.Errorf("页面停止推进: %w", context.DeadlineExceeded)
	tests := []struct {
		name           string
		newPage        bool
		force          bool
		cancel         bool
		first          error
		second         error
		wantReadyCalls int
		wantWaitCalls  int
		wantResult     error
	}{
		{name: "cold stall", newPage: true, first: stall, wantReadyCalls: 1, wantWaitCalls: 1},
		{name: "ordinary error", newPage: true, first: errors.New("navigate failed"), wantReadyCalls: 1, wantResult: errors.New("navigate failed")},
		{name: "stall text only", newPage: true, first: errors.New("页面停止推进"), wantReadyCalls: 1, wantResult: errors.New("页面停止推进")},
		{name: "deadline only", newPage: true, first: context.DeadlineExceeded, wantReadyCalls: 1, wantResult: context.DeadlineExceeded},
		{name: "warm page", first: stall, wantReadyCalls: 1, wantResult: stall},
		{name: "force recreate", newPage: true, force: true, first: stall, wantReadyCalls: 1, wantResult: stall},
		{name: "caller canceled", newPage: true, cancel: true, first: stall, wantReadyCalls: 1, wantResult: stall},
		{name: "retry failure propagates", newPage: true, first: stall, second: errors.New("retry failed"), wantReadyCalls: 1, wantWaitCalls: 1, wantResult: errors.New("retry failed")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.cancel {
				cancel()
			}
			readyCalls := 0
			waitCalls := 0
			got := runReadyWithColdStartRecovery(ctx, tt.newPage, tt.force,
				func(context.Context) error {
					readyCalls++
					return tt.first
				},
				func(context.Context) error {
					waitCalls++
					return tt.second
				},
			)
			if readyCalls != tt.wantReadyCalls {
				t.Fatalf("ready 调用次数 = %d, want %d", readyCalls, tt.wantReadyCalls)
			}
			if waitCalls != tt.wantWaitCalls {
				t.Fatalf("wait-only 调用次数 = %d, want %d", waitCalls, tt.wantWaitCalls)
			}
			if (got == nil) != (tt.wantResult == nil) || (got != nil && got.Error() != tt.wantResult.Error()) {
				t.Fatalf("结果 = %v, want %v", got, tt.wantResult)
			}
		})
	}
}

func TestHandleSessionOperationErrorClosesFatalSession(t *testing.T) {
	manager := xiaohongshu.NewBrowseSessionManager(time.Minute)
	session := manager.Create(nil, nil, nil)
	service := &XiaohongshuService{browseSessions: manager}
	service.handleSessionOperationError(context.Background(), session.ID(), session, fmt.Errorf("wrapped: %w", xiaohongshu.ErrFatalRendererError))
	if _, err := manager.Get(session.ID()); err == nil {
		t.Fatal("fatal renderer error 后 session 应被移除")
	}
}

func TestHandleSessionOperationErrorKeepsCanceledContext(t *testing.T) {
	manager := xiaohongshu.NewBrowseSessionManager(time.Minute)
	session := manager.Create(nil, nil, nil)
	service := &XiaohongshuService{browseSessions: manager}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	service.handleSessionOperationError(ctx, session.ID(), session, fmt.Errorf("operation failed"))
	if _, err := manager.Get(session.ID()); err != nil {
		t.Fatalf("请求取消后应保留 session: %v", err)
	}
	session.Close()
}

func TestHandleSessionOperationErrorKeepsDeadlineSession(t *testing.T) {
	manager := xiaohongshu.NewBrowseSessionManager(time.Minute)
	session := manager.Create(nil, nil, nil)
	service := &XiaohongshuService{browseSessions: manager}
	service.handleSessionOperationError(context.Background(), session.ID(), session, fmt.Errorf("wrapped: %w", context.DeadlineExceeded))
	if _, err := manager.Get(session.ID()); err != nil {
		t.Fatalf("deadline error 后应保留 session: %v", err)
	}
	session.Close()
}

func TestHandleSessionOperationErrorKeepsOrdinarySession(t *testing.T) {
	manager := xiaohongshu.NewBrowseSessionManager(time.Minute)
	session := manager.Create(nil, nil, nil)
	service := &XiaohongshuService{browseSessions: manager}
	service.handleSessionOperationError(context.Background(), session.ID(), session, fmt.Errorf("business error"))
	if _, err := manager.Get(session.ID()); err != nil {
		t.Fatalf("普通业务错误应保留 session: %v", err)
	}
	session.Close()
}

func TestCookieSaveGateRetriesAfterFailureAndRedactsError(t *testing.T) {
	var gate cookieSaveGate
	calls := 0
	if err := gate.run(func() error {
		calls++
		return fmt.Errorf("cookie-value-secret-token")
	}); err != errCookieSaveFailed {
		t.Fatalf("首次保存错误 = %v, 期望稳定阶段错误", err)
	}
	if err := gate.run(func() error {
		calls++
		return nil
	}); err != nil {
		t.Fatalf("失败后第二次保存应成功: %v", err)
	}
	if strings.Contains(errCookieSaveFailed.Error(), "secret") {
		t.Fatal("保存错误泄露了敏感值")
	}
	if calls != 2 {
		t.Fatalf("保存调用次数 = %d, 期望失败后允许第二次", calls)
	}
	if err := gate.run(func() error {
		calls++
		return fmt.Errorf("should-not-run")
	}); err != nil || calls != 2 {
		t.Fatalf("成功落盘后不应重复保存: err=%v calls=%d", err, calls)
	}
}

func TestCookieSaveGateSingleFlightAndSingleSuccess(t *testing.T) {
	var gate cookieSaveGate
	started := make(chan struct{})
	release := make(chan struct{}, 1)
	releaseWorker := func() {
		select {
		case release <- struct{}{}:
		default:
		}
	}
	defer releaseWorker()
	done := make(chan error, 1)
	go func() {
		done <- gate.run(func() error {
			close(started)
			select {
			case <-release:
			case <-time.After(2 * time.Second):
				return context.DeadlineExceeded
			}
			return nil
		})
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		releaseWorker()
		t.Fatal("首个保存未在 watchdog 内启动")
	}
	if err := gate.run(func() error { return nil }); err != errCookieSaveInProgress {
		t.Fatalf("并发保存错误 = %v, 期望 in-progress", err)
	}
	releaseWorker()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("首个保存失败: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("首个保存未在 watchdog 内结束")
	}
	called := false
	if err := gate.run(func() error {
		called = true
		return nil
	}); err != nil || called {
		t.Fatalf("成功后不应再次落盘: err=%v called=%v", err, called)
	}
}

func TestCancelPendingLoginQrcodeWaitsForWorkerExit(t *testing.T) {
	service := &XiaohongshuService{}
	done := make(chan struct{}, 1)
	canceled := make(chan struct{}, 1)
	session := &loginQrcodeSession{
		cancel: func() { canceled <- struct{}{} },
		done:   done,
	}
	service.loginQR = session
	finishWorker := func() {
		select {
		case done <- struct{}{}:
		default:
		}
	}
	defer finishWorker()

	result := make(chan error, 1)
	go func() { result <- service.cancelPendingLoginQrcode(context.Background()) }()
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		finishWorker()
		t.Fatal("pending login cancel 未在 watchdog 内调用")
	}
	select {
	case err := <-result:
		t.Fatalf("worker 尚未退出就返回: %v", err)
	default:
	}
	finishWorker()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("等待 worker 退出失败: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("等待 worker 退出未在 watchdog 内完成")
	}
	if service.liveLoginQrcode() != nil {
		t.Fatal("取消后不应残留 pending session")
	}
}

func TestCloseDetachedPageUsesIndependentContext(t *testing.T) {
	for _, wantWorkerErr := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(wantWorkerErr.Error(), func(t *testing.T) {
			workerCtx, cancel := context.WithCancel(context.Background())
			cancel()
			if wantWorkerErr == context.DeadlineExceeded {
				workerCtx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				cancel()
			}
			errClose := errors.New("secret close detail")
			err := closeDetachedPage(func(cleanupCtx context.Context) error {
				if workerCtx.Err() != wantWorkerErr {
					t.Fatalf("worker context 状态错误: %v", workerCtx.Err())
				}
				if cleanupCtx.Err() != nil {
					t.Fatalf("清理 context 不应继承 worker 取消: %v", cleanupCtx.Err())
				}
				deadline, ok := cleanupCtx.Deadline()
				if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 2*time.Second {
					t.Fatalf("清理 context 应有约 2s deadline: %v", deadline)
				}
				return errClose
			})
			if err != errClose {
				t.Fatalf("应保留 Close 失败供调用方处理: %v", err)
			}
		})
	}
}

func TestWaitLoginQrcodeDoneReportsCleanupFailure(t *testing.T) {
	done := make(chan struct{})
	session := &loginQrcodeSession{done: done, cleanupErr: errLoginQrcodeCleanup}
	service := &XiaohongshuService{loginQR: session}
	close(done)
	err := service.cancelPendingLoginQrcode(context.Background())
	if err != errLoginQrcodeCleanup {
		t.Fatalf("等待方应收到清理失败: %v", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("清理错误不应泄露底层敏感详情")
	}
}

func TestCancelPendingLoginQrcodeInvalidatesGeneration(t *testing.T) {
	service := &XiaohongshuService{}
	session := &loginQrcodeSession{generation: 0}
	service.loginQR = session
	if !service.currentLoginQrcode(session) {
		t.Fatal("当前代 pending session 应有效")
	}
	if err := service.cancelPendingLoginQrcode(context.Background()); err != nil {
		t.Fatal(err)
	}
	if service.currentLoginQrcode(session) || service.loginQRGeneration == session.generation || service.currentLoginQrcodeGeneration(session.generation) {
		t.Fatal("取消后旧代不应继续有效")
	}
}

func TestLoginQrcodeGenerationRejectsConcurrentAndClosedPublish(t *testing.T) {
	service := &XiaohongshuService{loginQRGeneration: 1}
	first := service.loginQRGeneration
	service.loginQRGeneration++
	second := service.loginQRGeneration
	service.loginQR = &loginQrcodeSession{generation: first}
	if service.currentLoginQrcodeGeneration(first) || service.currentLoginQrcode(service.loginQR) {
		t.Fatal("并发创建的旧代不得发布")
	}
	service.loginQRBlocked = true
	if service.currentLoginQrcodeGeneration(second) {
		t.Fatal("关闭期间当前代也不得发布")
	}
}

func TestBuildBrowseSessionReuseResultRejectsUnextendedTTL(t *testing.T) {
	now := time.Now()
	previous := xiaohongshu.BrowseSessionInfo{ID: "session-1", ExpiresAt: now.Add(time.Minute)}
	renewed := xiaohongshu.BrowseSessionInfo{ID: "session-1", ExpiresAt: now.Add(time.Minute)}
	result := buildBrowseSessionReuseResult(previous, renewed, now)
	if result.Outcome != "blocked" {
		t.Fatalf("Outcome = %q, 期望 blocked", result.Outcome)
	}
	step := startPageBlockedStep(context.Background(), result)
	if step.Tool != "start_page" {
		t.Fatalf("next_step.tool = %q, 期望 start_page", step.Tool)
	}
	if result.Status.Status != xiaohongshu.SessionExpired {
		t.Fatalf("Status.Status = %v, 期望 SessionExpired", result.Status.Status)
	}
	if result.Status.Ready {
		t.Fatal("Status.Ready 应为 false")
	}
	if result.Status.LastError != "session 已过期" {
		t.Fatalf("Status.LastError = %q, 期望 session 已过期", result.Status.LastError)
	}
	if result.Session != nil {
		t.Fatal("顶层 Session 应为 nil")
	}
	if result.Status.Session != nil {
		t.Fatal("Status.Session 应为 nil")
	}
}

func TestBuildBrowseSessionReuseResultAcceptsExtendedTTL(t *testing.T) {
	now := time.Now()
	previous := xiaohongshu.BrowseSessionInfo{ID: "session-1", ExpiresAt: now}
	renewed := xiaohongshu.BrowseSessionInfo{ID: "session-1", ExpiresAt: now.Add(10 * time.Minute)}
	result := buildBrowseSessionReuseResult(previous, renewed, now)
	if result.Outcome != "reused" {
		t.Fatalf("Outcome = %q, 期望 reused", result.Outcome)
	}
	if result.Status.Status != xiaohongshu.SessionReady {
		t.Fatalf("Status.Status = %v, 期望 SessionReady", result.Status.Status)
	}
	if !result.Status.Ready {
		t.Fatal("Status.Ready 应为 true")
	}
	if result.Session == nil || result.Session.ExpiresAt != renewed.ExpiresAt {
		t.Fatal("顶层 Session 应为续期后信息")
	}
	if result.Status.Session == nil || result.Status.Session.ExpiresAt != renewed.ExpiresAt {
		t.Fatal("Status.Session 应为续期后信息")
	}
	if !result.Status.HealthCheckedAt.Equal(now) {
		t.Fatalf("HealthCheckedAt = %v, 期望 %v", result.Status.HealthCheckedAt, now)
	}
}

func TestTryReuseSessionDegradesToCreateOnUnhealthy(t *testing.T) {
	manager := xiaohongshu.NewBrowseSessionManager(time.Minute)
	service := &XiaohongshuService{browseSessions: manager}

	// page=nil 使 CheckReusable 返回 SessionNotReady，触发降级路径
	session := manager.Create(nil, nil, nil)

	result, err := service.tryReuseSession(context.Background())
	if err != nil || result != nil {
		t.Fatalf("not-ready session 应降级为创建（返回 nil），得到 %+v err=%v", result, err)
	}
	if _, err := manager.Get(session.ID()); err == nil {
		t.Fatalf("降级创建后旧 session 应已关闭")
	}
}

func TestTryReuseSessionCanceledContextDoesNotDegrade(t *testing.T) {
	manager := xiaohongshu.NewBrowseSessionManager(time.Minute)
	service := &XiaohongshuService{browseSessions: manager}

	session := manager.Create(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := service.tryReuseSession(ctx)
	if !errors.Is(err, context.Canceled) || result != nil {
		t.Fatalf("取消请求不应降级创建，得到 %+v err=%v", result, err)
	}
	if _, err := manager.Get(session.ID()); err != nil {
		t.Fatalf("取消请求不应关闭 session: %v", err)
	}
}

// TestToolResultWithStepOpenNoteImagePath 实际经过项目的 toolResultWithStep 外部包装，
// 断言 open_note 图片真实外部路径为 data.note.imageList[].urlDefault/urlPre。
func TestToolResultWithStepOpenNoteImagePath(t *testing.T) {
	open := &xiaohongshu.SessionOpenNoteResponse{
		BrowseSessionInfo: xiaohongshu.BrowseSessionInfo{ID: "s1", Opened: true, Read: true},
		Note: xiaohongshu.OpenedNoteContent{
			NoteID: "n1",
			ImageList: []xiaohongshu.DetailImageInfo{
				{Width: 400, Height: 300, URLDefault: "https://example.com/image-default.jpg", URLPre: "https://example.com/image-pre.jpg"},
			},
		},
		Comments: []xiaohongshu.Comment{{ID: "c1"}},
	}
	result := toolResultWithStep(open, xiaohongshu.NextStep{Tool: "get_note_detail"}, []string{"get_note_detail"})
	if result.IsError || len(result.Content) != 1 {
		t.Fatalf("toolResultWithStep 结果异常: %+v", result)
	}
	raw := []byte(result.Content[0].Text)
	var payload struct {
		Data struct {
			Note struct {
				ImageList []struct {
					URLDefault string `json:"urlDefault"`
					URLPre     string `json:"urlPre"`
				} `json:"imageList"`
			} `json:"note"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("外部 JSON 解析失败: %v\n%s", err, result.Content[0].Text)
	}
	images := payload.Data.Note.ImageList
	if len(images) != 1 || images[0].URLDefault != "https://example.com/image-default.jpg" || images[0].URLPre != "https://example.com/image-pre.jpg" {
		t.Fatalf("open_note 图片路径应为 data.note.imageList[].urlDefault/urlPre: %s", result.Content[0].Text)
	}
}

// TestPublishArgsKeepImagesVideoKeys 锁定发布参数合法 key：publish_content 的 images 与 publish_with_video 的 video 不得误删。
func TestPublishArgsKeepImagesVideoKeys(t *testing.T) {
	content := PublishContentArgs{Title: "图文", Content: "正文", Images: []string{"/tmp/a.jpg"}}
	raw, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("publish_content 序列化失败: %v", err)
	}
	var payload struct {
		Images []string `json:"images"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("publish_content 解析失败: %v", err)
	}
	if len(payload.Images) != 1 || payload.Images[0] != "/tmp/a.jpg" {
		t.Fatalf("publish_content images key 缺失或不准确: %s", raw)
	}
	video := PublishVideoArgs{Title: "视频", Content: "正文", Video: "/tmp/v.mp4"}
	rawV, err := json.Marshal(video)
	if err != nil {
		t.Fatalf("publish_with_video 序列化失败: %v", err)
	}
	var vp struct {
		Video string `json:"video"`
	}
	if err := json.Unmarshal(rawV, &vp); err != nil {
		t.Fatalf("publish_with_video 解析失败: %v", err)
	}
	if vp.Video != "/tmp/v.mp4" {
		t.Fatalf("publish_with_video video key 缺失或不准确: %s", rawV)
	}
}

// parseSessionErrorNextStep 取出错误响应里 next_step 的结构化部分。
func parseSessionErrorNextStep(t *testing.T, result *MCPToolResult) xiaohongshu.NextStep {
	t.Helper()
	if result == nil || !result.IsError || len(result.Content) != 1 {
		t.Fatalf("错误响应异常: %+v", result)
	}
	text := result.Content[0].Text
	index := strings.Index(text, "\n")
	if index < 0 {
		t.Fatalf("错误响应应带 next_step JSON: %s", text)
	}
	var payload struct {
		NextStep xiaohongshu.NextStep `json:"next_step"`
	}
	if err := json.Unmarshal([]byte(text[index+1:]), &payload); err != nil {
		t.Fatalf("next_step 解析失败: %v\n%s", err, text)
	}
	return payload.NextStep
}

// TestSessionErrorNextStepCarriesKnownArgs 错误响应里的 next_step 必须带已知参数，
// 让调用方直接照抄 args 就能发出下一次调用，而不是自己猜该调什么工具。
func TestSessionErrorNextStepCarriesKnownArgs(t *testing.T) {
	cases := []struct {
		name     string
		prefix   string
		errText  string
		fallback xiaohongshu.NextStep
		wantTool string
		wantArgs map[string]string
		dropArgs []string
	}{
		{
			name:     "未打开笔记指向 open_note",
			prefix:   "打开笔记失败",
			errText:  "必须先打开笔记",
			fallback: sessionNextStepState("s-1"),
			wantTool: "open_note",
			wantArgs: map[string]string{"session_id": "s-1"},
		},
		{
			name:     "引用失效指向 search_feeds 并继承 keyword",
			prefix:   "打开笔记失败",
			errText:  "未找到搜索结果引用: 9",
			fallback: sessionNextStepSearch("露营"),
			wantTool: "search_feeds",
			wantArgs: map[string]string{"keyword": "露营"},
		},
		{
			name:     "缺少评论内容留在 comment_feed",
			prefix:   "评论失败",
			errText:  "缺少content参数",
			fallback: sessionNextStepCommentInput("s-2"),
			wantTool: "comment_feed",
			wantArgs: map[string]string{"session_id": "s-2"},
		},
		{
			name:     "缺少 comment_id 指向 get_note_detail",
			prefix:   "回复评论失败",
			errText:  "缺少comment_id或user_id参数",
			fallback: sessionNextStepState("s-3"),
			wantTool: "get_note_detail",
			wantArgs: map[string]string{"session_id": "s-3"},
		},
		{
			name:     "会话过期指向 start_page 且不带 session_id",
			prefix:   "页面状态获取失败",
			errText:  "browse session 不存在或已过期",
			fallback: sessionNextStepState("s-4"),
			wantTool: "start_page",
			dropArgs: []string{"session_id"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := sessionMCPErrorFromErr(tc.prefix, fmt.Errorf("%s", tc.errText), tc.fallback)
			next := parseSessionErrorNextStep(t, result)
			if next.Tool != tc.wantTool {
				t.Fatalf("next_step.tool = %q, 期望 %q", next.Tool, tc.wantTool)
			}
			if next.Reason == "" || next.Hint == "" {
				t.Fatalf("next_step 必须带 reason 和 hint: %+v", next)
			}
			for key, want := range tc.wantArgs {
				if got, ok := next.Args[key]; !ok || got != want {
					t.Fatalf("next_step.args[%s] = %v, 期望 %q（args=%v）", key, got, want, next.Args)
				}
			}
			for _, key := range tc.dropArgs {
				if _, ok := next.Args[key]; ok {
					t.Fatalf("next_step.args 不应包含 %s: %v", key, next.Args)
				}
			}
		})
	}
}

// TestSessionNextStepOmitsUnknownArgs 没有已知参数时不能写出空参数（MCP 参数会按 schema 校验）。
func TestSessionNextStepOmitsUnknownArgs(t *testing.T) {
	next := sessionNextStepState("")
	if next.Args != nil {
		t.Fatalf("无 session_id 时 args 应为空: %v", next.Args)
	}
	result := toolResultWithStep(map[string]string{"ok": "1"}, next, []string{"get_page_state"})
	if strings.Contains(result.Content[0].Text, `"args"`) {
		t.Fatalf("空 args 不应出现在 JSON 里: %s", result.Content[0].Text)
	}
}

// TestStartPageBlockedStepPointsBackToStartPage start_page 拿不到会话时只能重新调 start_page。
func TestStartPageBlockedStepPointsBackToStartPage(t *testing.T) {
	busy := &xiaohongshu.CreateBrowseSessionResult{
		Outcome: "blocked",
		Status:  xiaohongshu.BrowseSessionStatusInfo{Status: xiaohongshu.SessionBusy, LastError: "session 正在执行操作"},
	}
	step := startPageBlockedStep(context.Background(), busy)
	if step.Tool != "start_page" {
		t.Fatalf("busy 时 next_step.tool = %q, 期望 start_page", step.Tool)
	}
	if _, ok := step.Args["force_recreate"]; ok {
		t.Fatalf("busy 时不应建议 force_recreate: %v", step.Args)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	retry := startPageBlockedStep(cancelled, busy)
	if retry.Tool != "start_page" || retry.Hint == "" {
		t.Fatalf("取消时指引异常: %+v", retry)
	}

	unknown := &xiaohongshu.CreateBrowseSessionResult{
		Outcome: "blocked",
		Status:  xiaohongshu.BrowseSessionStatusInfo{Status: xiaohongshu.SessionUnhealthy, LastError: "renderer 不可用"},
	}
	recreate := startPageBlockedStep(context.Background(), unknown)
	if recreate.Args["force_recreate"] != true {
		t.Fatalf("不可复用时应带 force_recreate=true: %v", recreate.Args)
	}
	if !strings.Contains(recreate.Reason, "unhealthy") {
		t.Fatalf("reason 应说明具体状态: %s", recreate.Reason)
	}
}

// TestNextStepArgsMatchToolSchemas 防止指引里塞入工具 schema 不存在的参数
// （曾误写 get_note_detail 的 load_comments）。合法键空间来自 MCP 参数结构体的 json tag。
func TestNextStepArgsMatchToolSchemas(t *testing.T) {
	valid := map[string]bool{}
	samples := []any{
		CreateBrowseSessionArgs{}, BrowseSessionIDArgs{}, ListFeedsArgs{}, SessionSearchArgs{},
		SessionOpenNoteArgs{}, SessionDetailArgs{}, SessionLikeArgs{}, FavoriteFeedArgs{},
		SessionCommentArgs{}, ReplyCommentArgs{}, UnreadNotificationCountArgs{},
		ListNotificationsArgs{}, LikeNotificationArgs{}, ReplyNotificationArgs{},
	}
	for _, sample := range samples {
		rt := reflect.TypeOf(sample)
		for i := 0; i < rt.NumField(); i++ {
			name := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]
			if name != "" && name != "-" {
				valid[name] = true
			}
		}
	}
	nextStepArgs := regexp.MustCompile(`NextStepArgs\(map\[string\]any\{([^}]*)\}`)
	argKey := regexp.MustCompile(`"([a-z_]+)":`)
	scanned := 0
	for _, file := range []string{"xiaohongshu/browse_session.go", "mcp_handlers.go"} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", file, err)
		}
		for _, match := range nextStepArgs.FindAllStringSubmatch(string(data), -1) {
			for _, key := range argKey.FindAllStringSubmatch(match[1], -1) {
				scanned++
				if !valid[key[1]] {
					t.Fatalf("%s 里 next_step 参数 %q 不在任何 MCP 工具的 schema 中", file, key[1])
				}
			}
		}
	}
	if scanned == 0 {
		t.Fatal("没有扫描到任何 next_step 参数，正则失配")
	}
}

// TestStartPageNextStepByRisk 用与页面探测同一份风险关键词表决定 start_page 失败后的下一步。
func TestStartPageNextStepByRisk(t *testing.T) {
	cases := []struct {
		name     string
		errText  string
		wantTool string
		wantHint string
	}{
		{"登录失效指向扫码", "等待探索页就绪失败: 页面出现风险信号: 登录已过期", "get_login_qrcode", "扫码登录"},
		{"验证码要求人工处理", "等待探索页就绪失败: 页面出现风险信号: 请完成安全验证", "start_page", "人工"},
		{"滑块要求人工处理", "页面出现风险信号: 请拖动滑块", "start_page", "人工"},
		{"访问异常要求等待", "页面出现风险信号: 操作频繁，请稍后再试", "start_page", "等待"},
		{"普通失败重建会话", "等待探索页就绪失败: 页面就绪超时", "start_page", "force_recreate=true"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			step := startPageNextStep(fmt.Errorf("%s", tc.errText))
			if step.Tool != tc.wantTool {
				t.Fatalf("tool = %q, 期望 %q", step.Tool, tc.wantTool)
			}
			if !strings.Contains(step.Hint, tc.wantHint) {
				t.Fatalf("hint = %q, 应包含 %q", step.Hint, tc.wantHint)
			}
		})
	}
}

type sessionCleanupPageClient struct {
	events chan *cdp.Event
}

func (c *sessionCleanupPageClient) Event() <-chan *cdp.Event { return c.events }

func (c *sessionCleanupPageClient) Call(ctx context.Context, _ string, method string, _ interface{}) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch method {
	case "Target.attachToTarget":
		return []byte(`{"sessionId":"cleanup-session"}`), nil
	case "Runtime.evaluate":
		return []byte(`{"result":{"type":"object","objectId":"window"}}`), nil
	case "Runtime.callFunctionOn":
		return []byte(`{"result":{"type":"string","value":"{\"url\":\"about:blank\",\"scroll_y\":0}"}}`), nil
	default:
		return []byte(`{}`), nil
	}
}

func TestCreateBrowseSessionOldCleanupHonorsRequestAndErrors(t *testing.T) {
	for _, mode := range []string{"cleanup error", "closing cancellation", "closing deadline", "service close"} {
		t.Run(mode, func(t *testing.T) {
			sessions := xiaohongshu.NewBrowseSessionManager(time.Minute)
			var factoryCalls atomic.Int32
			manager := browser.NewManager(func(context.Context) (*hrod.Browser, error) {
				factoryCalls.Add(1)
				return nil, errors.New("must not acquire a page after old cleanup fails")
			})
			service := &XiaohongshuService{browseSessions: sessions, browserManager: manager}
			browserCtx, cancelBrowser := context.WithCancel(context.Background())
			defer cancelBrowser()
			controller := rod.New().Context(browserCtx).NoDefaultDevice().ControlURL("").Client(&sessionCleanupPageClient{events: make(chan *cdp.Event)})
			if err := controller.Connect(); err != nil {
				t.Fatal(err)
			}
			rodPage, err := controller.PageFromTarget("cleanup-target")
			if err != nil {
				t.Fatal(err)
			}
			cleanupErr := errors.New("injected old session cleanup failure")
			var allowCleanup atomic.Bool
			var calls atomic.Int32
			entered := make(chan struct{}, 1)
			session, err := sessions.CreateContextWithLifecycle(context.Background(), &hrod.Page{Rod: rodPage}, nil,
				func(ctx context.Context, _ *hrod.Page, _, _ bool) error {
					call := calls.Add(1)
					if allowCleanup.Load() {
						return nil
					}
					if mode == "cleanup error" || call == 1 {
						return cleanupErr
					}
					entered <- struct{}{}
					<-ctx.Done()
					return ctx.Err()
				}, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				allowCleanup.Store(true)
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := sessions.CloseAllContext(ctx); err != nil {
					t.Errorf("old session fixture cleanup: %v", err)
				}
			})
			if mode != "cleanup error" {
				if err := sessions.CloseContext(context.Background(), session.ID()); !errors.Is(err, cleanupErr) {
					t.Fatalf("prepare retained closing session: %v", err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "closing deadline" {
				var deadlineCancel context.CancelFunc
				ctx, deadlineCancel = context.WithTimeout(ctx, 200*time.Millisecond)
				defer deadlineCancel()
			}
			done, joined := make(chan error, 1), make(chan struct{})
			go func() {
				defer close(joined)
				result, err := service.CreateBrowseSession(ctx, true)
				if result != nil {
					err = errors.Join(err, errors.New("unexpected session result after failed cleanup"))
				}
				done <- err
			}()
			t.Cleanup(func() {
				cancel()
				select {
				case <-joined:
				case <-time.After(2 * time.Second):
					t.Error("creation caller did not join after cancellation")
				}
			})
			wantErr := cleanupErr
			if mode != "cleanup error" {
				select {
				case <-entered:
				case <-time.After(time.Second):
					t.Fatal("creation did not enter retained session cleanup")
				}
				wantErr = context.Canceled
				switch mode {
				case "closing cancellation":
					cancel()
				case "closing deadline":
					wantErr = context.DeadlineExceeded
				case "service close":
					allowCleanup.Store(true)
					closeCtx, cancelClose := context.WithTimeout(context.Background(), time.Second)
					defer cancelClose()
					if err := service.Close(closeCtx); err != nil {
						t.Fatalf("service close must cancel cleanup and acquire the creation gate: %v", err)
					}
				}
			}
			select {
			case err := <-done:
				if !errors.Is(err, wantErr) {
					t.Fatalf("old cleanup must preserve its error or request cancellation: want=%v got=%v", wantErr, err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("creation cleanup ignored request cancellation/deadline")
			}
			if factoryCalls.Load() != 0 {
				t.Fatal("failed old cleanup must stop before browser acquisition")
			}
			probeCtx, cancelProbe := context.WithTimeout(context.Background(), time.Second)
			defer cancelProbe()
			gate, err := service.acquireCreateSessionGate(probeCtx, true)
			if err != nil {
				t.Fatalf("failed creation must return its gate: %v", err)
			}
			gate()
		})
	}
}

func TestRegisterReadyBrowseSessionCancellationDoesNotRegister(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	service := &XiaohongshuService{browseSessions: xiaohongshu.NewBrowseSessionManager(time.Minute)}
	releases, ownerUpdates := 0, 0
	result, err := service.registerReadyBrowseSession(ctx, nil, func(*hrod.Page) { releases++ }, func(string) { ownerUpdates++ })
	if !errors.Is(err, context.Canceled) || result != nil {
		t.Fatalf("应返回 context.Canceled 且不返回 session: result=%+v err=%v", result, err)
	}
	if _, ok := service.ActiveBrowseSessionInfo(); ok {
		t.Fatal("取消后不应登记 browse session")
	}
	if releases != 0 || ownerUpdates != 0 {
		t.Fatalf("nil page 的取消不应释放 browser lease 或更新 owner: releases=%d owner_updates=%d", releases, ownerUpdates)
	}
}

func TestRegisterReadyBrowseSessionCancellationDuringOwnerUpdateClosesOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := xiaohongshu.NewBrowseSessionManager(time.Minute)
	t.Cleanup(manager.CloseAll)
	service := &XiaohongshuService{browseSessions: manager}
	owner, releases := "browser_operation", 0
	result, err := service.registerReadyBrowseSession(ctx, nil, func(*hrod.Page) {
		releases++
		owner = "browser_operation"
	}, func(value string) {
		owner = value
		cancel()
	})
	if !errors.Is(err, context.Canceled) || result != nil {
		t.Fatalf("owner 更新期间取消必须返回取消且不返回 session: result=%+v err=%v", result, err)
	}
	if _, ok := service.ActiveBrowseSessionInfo(); ok {
		t.Fatal("owner 更新期间取消后不得留下 session")
	}
	if owner != "browser_operation" || releases != 0 {
		t.Fatalf("取消回滚应恢复原 owner，nil page 不应归还 lease: owner=%q releases=%d", owner, releases)
	}
}

func TestRegisterReadyBrowseSessionCreatesReadySession(t *testing.T) {
	profileDir := t.TempDir()
	factory := newServiceBrowserFactory(t, nil, nil, nil, nil, profileDir)
	page := newServiceCommentPage(t, factory, "https://www.xiaohongshu.com/explore", profileDir)
	manager := xiaohongshu.NewBrowseSessionManager(time.Minute)
	t.Cleanup(manager.CloseAll)
	service := &XiaohongshuService{browseSessions: manager}
	owner := ""
	result, err := service.registerReadyBrowseSession(context.Background(), page, func(*hrod.Page) {}, func(value string) { owner = value })
	if err != nil || result == nil || result.Outcome != "created" || result.Session == nil || result.Session.ID == "" || !result.Status.Ready {
		t.Fatalf("ready page 应创建并返回 session ID: result=%+v err=%v", result, err)
	}
	if owner != "session:"+result.Session.ID {
		t.Fatalf("成功登记后应更新 session owner: %q", owner)
	}
	if _, ok := service.ActiveBrowseSessionInfo(); !ok {
		t.Fatal("成功后应存在 active browse session")
	}
}

// Exercise a resource-only retry after lease return using the existing isolated
// browser fixture. This does not force the fatal-upgrade/close-completion race.
func TestOldTargetCleanupFailurePreservesNewOwnerPage(t *testing.T) {
	profileDir := t.TempDir()
	factory := newServiceBrowserFactory(t, nil, nil, nil, nil, profileDir)
	manager := browser.NewManager(factory, browser.WithWarmPageTTL(0))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	t.Cleanup(func() {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelCleanup()
		if err := manager.Close(cleanupCtx); err != nil {
			t.Errorf("fixture manager cleanup: %v", err)
		}
	})
	oldPage, err := manager.AcquireFor(ctx, "old-owner")
	if err != nil {
		t.Fatal(err)
	}
	manager.Detach(oldPage)
	newPage, err := manager.AcquireFor(ctx, "new-owner")
	if err != nil {
		t.Fatal(err)
	}
	if oldPage.Rod.TargetID == newPage.Rod.TargetID {
		t.Fatal("fixture must use distinct targets in the same browser")
	}
	failedCtx, cancelFailed := context.WithCancel(ctx)
	cancelFailed()
	if err := manager.ClosePageContext(failedCtx, oldPage); err == nil {
		t.Fatal("canceled old-target cleanup must report failure")
	}
	if _, err := newPage.Context(ctx).Eval(`() => document.readyState`); err != nil {
		t.Fatalf("old-target cleanup closed the new owner's browser/page: %v", err)
	}
	if err := manager.ClosePageContext(ctx, oldPage); err != nil {
		t.Fatalf("explicit old-target retry: %v", err)
	}
	if err := manager.ReleaseAndCloseContext(ctx, newPage); err != nil {
		t.Fatalf("new-owner release: %v", err)
	}
}

func TestCreateBrowseSessionCloseAllDuringRefreshDoesNotCommit(t *testing.T) {
	gate := newServiceInitialRefreshGate()
	profileDir := t.TempDir()
	factory := newServiceInitialRefreshBrowserFactory(t, gate, profileDir)
	browserManager := browser.NewManager(factory, browser.WithWarmPageTTL(0), browser.WithIdleTimeout(time.Minute))
	sessions := xiaohongshu.NewBrowseSessionManager(time.Minute)
	service := &XiaohongshuService{browserManager: browserManager, browseSessions: sessions}
	type createResult struct {
		result *xiaohongshu.CreateBrowseSessionResult
		err    error
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	done := make(chan createResult, 1)
	createJoined := false
	var probe *hrod.Page
	probeHeld := false
	probeReleaseAttempted := false
	releaseProbe := func() error {
		if !probeHeld || probeReleaseAttempted {
			return nil
		}
		probeReleaseAttempted = true
		return releaseAndCloseWithWatchdog(browserManager, probe, 5*time.Second, 7*time.Second)
	}
	t.Cleanup(func() {
		cancel()
		gate.abort()
		select {
		case <-gate.entered:
			select {
			case <-gate.released:
			default:
				t.Errorf("初始刷新 CDP gate 未确认 release 已被读取")
			}
		default:
		}
		if !createJoined {
			select {
			case <-done:
				createJoined = true
			case <-time.After(10 * time.Second):
				t.Errorf("CloseAll 创建 caller 未在 cleanup watchdog 内 join")
			}
		}
		select {
		case <-gate.entered:
			select {
			case <-gate.exited:
			case <-time.After(10 * time.Second):
				t.Errorf("初始刷新 CDP gate 未在 cleanup watchdog 内退出")
			}
		default:
		}
		reportServiceInitialRefreshFixtureError(t, gate, "初始刷新 fixture cleanup")
		if err := releaseProbe(); err != nil {
			t.Errorf("post-abort probe 单次归还失败: %v", err)
		}
		if err := runFixtureCloseWithWatchdog(10*time.Second, 2*time.Second, sessions.CloseAllContext); err != nil {
			t.Errorf("session creation fixture cleanup failed: %v", err)
		}
		if err := runFixtureCloseWithWatchdog(10*time.Second, 2*time.Second, browserManager.Close); err != nil {
			t.Errorf("session creation browser fixture cleanup failed: %v", err)
		}
	})
	go func() {
		result, err := service.CreateBrowseSession(ctx, false)
		done <- createResult{result, err}
	}()
	waitServiceInitialRefreshEntered(t, gate)
	if _, ok := service.ActiveBrowseSessionInfo(); ok {
		t.Fatal("初始状态刷新期间不得发布 session")
	}
	assertServiceBusyOwnerWithWatchdog(t, browserManager, "session")
	sessions.CloseAll()
	gate.open()
	select {
	case <-gate.released:
	case <-time.After(2 * time.Second):
		t.Fatal("CloseAll 测试未确认放行初始刷新 gate")
	}
	select {
	case created := <-done:
		createJoined = true
		if created.result != nil || created.err == nil || !strings.Contains(created.err.Error(), "closed during creation") {
			t.Fatalf("生产创建与内部 CloseAll 竞争时不得提交 ready session: result=%+v err=%v", created.result, created.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("CloseAll 后生产创建未在独立 watchdog 内结束")
	}
	waitServiceInitialRefreshExited(t, gate)
	if _, ok := service.ActiveBrowseSessionInfo(); ok {
		t.Fatal("内部 CloseAll 使创建失效后不得留下 session")
	}
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), 15*time.Second)
	page, err := acquireForWithWatchdog(browserManager, profileDir, probeCtx, cancelProbe, "post-abort-probe", 20*time.Second)
	cancelProbe()
	if err != nil {
		t.Fatalf("创建失败后浏览器独占权应已释放: %v", err)
	}
	probe = page
	probeHeld = true
	assertServiceBusyOwnerWithWatchdog(t, browserManager, "post-abort-probe")
	if err := releaseProbe(); err != nil {
		t.Fatalf("post-abort probe 单次归还失败: %v", err)
	}
}

func TestBrowseSessionTTLStartsAtProductionCommit(t *testing.T) {
	gate := newServiceInitialRefreshGate()
	const ttl = 200 * time.Millisecond
	profileDir := t.TempDir()
	factory := newServiceInitialRefreshBrowserFactory(t, gate, profileDir)
	browserManager := browser.NewManager(factory, browser.WithWarmPageTTL(0), browser.WithIdleTimeout(time.Minute))
	sessions := xiaohongshu.NewBrowseSessionManager(ttl)
	service := &XiaohongshuService{browserManager: browserManager, browseSessions: sessions}
	type createResult struct {
		result *xiaohongshu.CreateBrowseSessionResult
		err    error
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	done := make(chan createResult, 1)
	createJoined := false
	var probe *hrod.Page
	probeHeld := false
	probeReleaseAttempted := false
	releaseProbe := func() error {
		if !probeHeld || probeReleaseAttempted {
			return nil
		}
		probeReleaseAttempted = true
		return releaseAndCloseWithWatchdog(browserManager, probe, 5*time.Second, 7*time.Second)
	}
	t.Cleanup(func() {
		cancel()
		gate.abort()
		select {
		case <-gate.entered:
			select {
			case <-gate.released:
			default:
				t.Errorf("TTL 初始刷新 CDP gate 未确认 release 已被读取")
			}
		default:
		}
		if !createJoined {
			select {
			case <-done:
				createJoined = true
			case <-time.After(10 * time.Second):
				t.Errorf("TTL 创建 caller 未在 cleanup watchdog 内 join")
			}
		}
		select {
		case <-gate.entered:
			select {
			case <-gate.exited:
			case <-time.After(10 * time.Second):
				t.Errorf("TTL 初始刷新 CDP gate 未在 cleanup watchdog 内退出")
			}
		default:
		}
		reportServiceInitialRefreshFixtureError(t, gate, "TTL 初始刷新 fixture cleanup")
		if err := releaseProbe(); err != nil {
			t.Errorf("post-TTL probe 单次归还失败: %v", err)
		}
		if err := runFixtureCloseWithWatchdog(10*time.Second, 2*time.Second, sessions.CloseAllContext); err != nil {
			t.Errorf("session creation fixture cleanup failed: %v", err)
		}
		if err := runFixtureCloseWithWatchdog(10*time.Second, 2*time.Second, browserManager.Close); err != nil {
			t.Errorf("session creation browser fixture cleanup failed: %v", err)
		}
	})
	go func() {
		result, err := service.CreateBrowseSession(ctx, false)
		done <- createResult{result, err}
	}()
	waitServiceInitialRefreshEntered(t, gate)
	started := time.Now()
	if _, ok := service.ActiveBrowseSessionInfo(); ok {
		t.Fatal("TTL 未提交前不得提前登记 session")
	}
	assertServiceBusyOwnerWithWatchdog(t, browserManager, "session")
	if elapsed := time.Since(started); elapsed < 2*ttl {
		t.Fatalf("受控刷新等待未跨过 2*ttl: elapsed=%s ttl=%s", elapsed, ttl)
	}
	if _, ok := service.ActiveBrowseSessionInfo(); ok {
		t.Fatal("TTL 未提交前不得提前登记 session")
	}
	gate.open()
	select {
	case <-gate.released:
	case <-time.After(2 * time.Second):
		t.Fatal("TTL 测试未确认放行初始刷新 gate")
	}
	select {
	case created := <-done:
		createJoined = true
		if created.err != nil || created.result == nil || created.result.Outcome != "created" || created.result.Session == nil {
			t.Fatalf("刷新完成后 session 应提交并开始 TTL: result=%+v err=%v", created.result, created.err)
		}
		active, ok := service.ActiveBrowseSessionInfo()
		if !ok || active.ID != created.result.Session.ID {
			t.Fatalf("session TTL 必须从生产提交起算: created=%q active=%+v exists=%t", created.result.Session.ID, active, ok)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("释放初始刷新后 session 未在独立 watchdog 内提交")
	}
	waitServiceInitialRefreshExited(t, gate)
	ttlWatchdog := time.NewTimer(10 * time.Second)
	defer ttlWatchdog.Stop()
	ttlPoll := time.NewTicker(20 * time.Millisecond)
	defer ttlPoll.Stop()
	for {
		if _, ok := service.ActiveBrowseSessionInfo(); !ok {
			break
		}
		select {
		case <-ttlPoll.C:
		case <-ttlWatchdog.C:
			t.Fatal("提交后的 TTL 未在独立 watchdog 内关闭 session")
		}
	}
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), 15*time.Second)
	page, err := acquireForWithWatchdog(browserManager, profileDir, probeCtx, cancelProbe, "post-ttl-probe", 20*time.Second)
	cancelProbe()
	if err != nil {
		t.Fatalf("TTL 关闭后 browser owner 应已释放: %v", err)
	}
	probe = page
	probeHeld = true
	if err := releaseProbe(); err != nil {
		t.Fatalf("post-TTL probe 单次归还失败: %v", err)
	}
}

func TestCreateBrowseSessionCommitPublishesAndTransfersOwner(t *testing.T) {
	refreshStarted := make(chan struct{}, 1)
	refreshRelease := make(chan struct{}, 1)
	releaseRefresh := func() {
		select {
		case refreshRelease <- struct{}{}:
		default:
		}
	}
	defer releaseRefresh()
	profileDir := t.TempDir()
	factory := newServiceBrowserFactory(t, refreshStarted, refreshRelease, nil, nil, profileDir)
	browserManager := browser.NewManager(factory, browser.WithWarmPageTTL(0), browser.WithIdleTimeout(time.Minute))
	sessions := xiaohongshu.NewBrowseSessionManager(time.Minute)
	service := &XiaohongshuService{browserManager: browserManager, browseSessions: sessions}
	type createResult struct {
		result *xiaohongshu.CreateBrowseSessionResult
		err    error
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	done := make(chan createResult, 1)
	createJoined := false
	var closeCancel context.CancelFunc
	closeDone := make(chan error, 1)
	closeJoined := false
	var probe *hrod.Page
	probeHeld := false
	probeReleaseAttempted := false
	releaseProbe := func() error {
		if !probeHeld || probeReleaseAttempted {
			return nil
		}
		probeReleaseAttempted = true
		return releaseAndCloseWithWatchdog(browserManager, probe, 5*time.Second, 7*time.Second)
	}
	t.Cleanup(func() {
		cancel()
		releaseRefresh()
		if closeCancel != nil {
			closeCancel()
		}
		if !createJoined {
			select {
			case <-done:
				createJoined = true
			case <-time.After(10 * time.Second):
				t.Errorf("session commit 创建 caller 未在 cleanup watchdog 内 join")
			}
		}
		if closeCancel != nil && !closeJoined {
			select {
			case <-closeDone:
				closeJoined = true
			case <-time.After(10 * time.Second):
				t.Errorf("session close caller 未在 cleanup watchdog 内 join")
			}
		}
		if err := releaseProbe(); err != nil {
			t.Errorf("post-close probe 单次归还失败: %v", err)
		}
		if err := runFixtureCloseWithWatchdog(10*time.Second, 2*time.Second, sessions.CloseAllContext); err != nil {
			t.Errorf("创建提交 fixture session 清理失败: %v", err)
		}
		if err := runFixtureCloseWithWatchdog(10*time.Second, 2*time.Second, browserManager.Close); err != nil {
			t.Errorf("创建提交 fixture browser 清理失败: %v", err)
		}
	})

	go func() {
		result, err := service.CreateBrowseSession(ctx, false)
		done <- createResult{result: result, err: err}
	}()
	select {
	case <-refreshStarted:
	case <-ctx.Done():
		releaseRefresh()
		t.Fatalf("生产 CreateBrowseSession 未进入 session 初始状态刷新: %v", ctx.Err())
	}
	if _, ok := service.ActiveBrowseSessionInfo(); ok {
		releaseRefresh()
		t.Fatal("初始状态刷新期间不得发布 session")
	}
	releaseRefresh()
	var created createResult
	select {
	case created = <-done:
		createJoined = true
	case <-ctx.Done():
		t.Fatalf("生产 session 提交未完成: %v", ctx.Err())
	}
	cancel()
	if created.err != nil || created.result == nil || created.result.Outcome != "created" || created.result.Session == nil || !created.result.Status.Ready {
		t.Fatalf("生产创建应返回 committed ready session: result=%+v err=%v", created.result, created.err)
	}
	id := created.result.Session.ID
	info, ok := service.ActiveBrowseSessionInfo()
	if !ok || info.ID != id {
		t.Fatalf("返回 ID 与 manager 中 committed session 必须一致: result=%q active=%+v exists=%t", id, info, ok)
	}

	assertServiceBusyOwnerWithWatchdog(t, browserManager, "session:"+id)
	closeCtx, closeCancelFn := context.WithTimeout(context.Background(), 15*time.Second)
	closeCancel = closeCancelFn
	go func() { closeDone <- service.CloseBrowseSession(closeCtx, id) }()
	select {
	case err := <-closeDone:
		closeJoined = true
		closeCancel()
		if err != nil {
			t.Fatalf("提交后的 close_page 应成功: %v", err)
		}
	case <-time.After(17 * time.Second):
		t.Fatal("提交后的 close_page 超过独立 watchdog")
	}
	if _, ok := service.ActiveBrowseSessionInfo(); ok {
		t.Fatal("close_page 后不应保留 active session")
	}
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), 15*time.Second)
	page, err := acquireForWithWatchdog(browserManager, profileDir, probeCtx, cancelProbe, "post-close-probe", 20*time.Second)
	cancelProbe()
	if err != nil {
		t.Fatalf("关闭后浏览器独占权应可重新取得: %v", err)
	}
	probe = page
	probeHeld = true
}

func TestCloseBrowseSessionSerializesWithProductionForceRecreate(t *testing.T) {
	lockFile, lockLine := serviceCreateSessionLockPosition(t, "CloseBrowseSession")
	refreshStarted := make(chan struct{}, 1)
	refreshRelease := make(chan struct{}, 1)
	releaseRefresh := func() {
		select {
		case refreshRelease <- struct{}{}:
		default:
		}
	}
	defer releaseRefresh()
	profileDir := t.TempDir()
	factory := newServiceBrowserFactory(t, refreshStarted, refreshRelease, nil, nil, profileDir)
	browserManager := browser.NewManager(factory, browser.WithWarmPageTTL(0), browser.WithIdleTimeout(time.Minute))
	sessions := xiaohongshu.NewBrowseSessionManager(time.Minute)
	service := &XiaohongshuService{browserManager: browserManager, browseSessions: sessions}
	type createResult struct {
		result *xiaohongshu.CreateBrowseSessionResult
		err    error
	}
	firstDone := make(chan createResult, 1)
	firstExited := make(chan struct{})
	replacementDone := make(chan createResult, 1)
	replacementExited := make(chan struct{})
	closed := make(chan error, 1)
	oldCloseExited := make(chan struct{})
	target := make(chan serviceGoroutineIdentity, 1)
	closeResults := make(chan error, 2)
	parallelCloseExited := make(chan struct{}, 2)
	var firstCancel, replacementCancel, oldCloseCancel, parallelCancel1, parallelCancel2 context.CancelFunc
	firstStarted, firstJoined := false, false
	replacementStarted, replacementJoined := false, false
	oldCloseStarted, oldCloseJoined := false, false
	parallelCloseStarted, parallelCloseJoined := 0, 0
	var probe *hrod.Page
	probeHeld := false
	probeReleaseAttempted := false
	releaseProbe := func() error {
		if !probeHeld || probeReleaseAttempted {
			return nil
		}
		probeReleaseAttempted = true
		return releaseAndCloseWithWatchdog(browserManager, probe, 5*time.Second, 7*time.Second)
	}
	t.Cleanup(func() {
		if firstCancel != nil {
			firstCancel()
		}
		if replacementCancel != nil {
			replacementCancel()
		}
		if oldCloseCancel != nil {
			oldCloseCancel()
		}
		if parallelCancel1 != nil {
			parallelCancel1()
		}
		if parallelCancel2 != nil {
			parallelCancel2()
		}
		releaseRefresh()
		if firstStarted && !firstJoined {
			select {
			case <-firstExited:
				firstJoined = true
			case <-time.After(10 * time.Second):
				t.Errorf("首次 CreateBrowseSession 未在 cleanup watchdog 内 join")
			}
		}
		if replacementStarted && !replacementJoined {
			select {
			case <-replacementExited:
				replacementJoined = true
			case <-time.After(10 * time.Second):
				t.Errorf("force_recreate caller 未在 cleanup watchdog 内 join")
			}
		}
		if oldCloseStarted && !oldCloseJoined {
			select {
			case <-oldCloseExited:
				oldCloseJoined = true
			case <-time.After(10 * time.Second):
				t.Errorf("旧 ID close caller 未在 cleanup watchdog 内 join")
			}
		}
		for parallelCloseJoined < parallelCloseStarted {
			select {
			case <-parallelCloseExited:
				parallelCloseJoined++
			case <-time.After(10 * time.Second):
				t.Errorf("并发 close callers 未在 cleanup watchdog 内全部 join: %d/%d", parallelCloseJoined, parallelCloseStarted)
				parallelCloseJoined = parallelCloseStarted
			}
		}
		if err := releaseProbe(); err != nil {
			t.Errorf("post-close probe 单次归还失败: %v", err)
		}
		if err := runFixtureCloseWithWatchdog(10*time.Second, 2*time.Second, sessions.CloseAllContext); err != nil {
			t.Errorf("force_recreate fixture session cleanup 失败: %v", err)
		}
		if err := runFixtureCloseWithWatchdog(10*time.Second, 2*time.Second, browserManager.Close); err != nil {
			t.Errorf("force_recreate fixture browser cleanup 失败: %v", err)
		}
	})
	firstCtx, cancelFirst := context.WithTimeout(context.Background(), 30*time.Second)
	firstCancel = cancelFirst
	firstStarted = true
	go func() {
		defer close(firstExited)
		result, err := service.CreateBrowseSession(firstCtx, false)
		firstDone <- createResult{result: result, err: err}
	}()
	select {
	case <-refreshStarted:
		releaseRefresh()
	case <-firstCtx.Done():
		releaseRefresh()
		t.Fatalf("首次生产创建未进入受控 session 刷新: %v", firstCtx.Err())
	}
	var first createResult
	select {
	case first = <-firstDone:
		select {
		case <-firstExited:
			firstJoined = true
		case <-time.After(2 * time.Second):
			t.Fatal("首次 CreateBrowseSession 返回后未 join")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("首次生产创建结果未在独立 watchdog 内返回")
	}
	cancelFirst()
	if first.err != nil || first.result == nil || first.result.Outcome != "created" || first.result.Session == nil {
		t.Fatalf("首次生产创建失败: result=%+v err=%v", first.result, first.err)
	}
	replacementCtx, cancelReplacement := context.WithTimeout(context.Background(), 30*time.Second)
	replacementCancel = cancelReplacement
	replacementStarted = true
	go func() {
		defer close(replacementExited)
		result, err := service.CreateBrowseSession(replacementCtx, true)
		replacementDone <- createResult{result: result, err: err}
	}()
	select {
	case <-refreshStarted:
	case <-replacementCtx.Done():
		releaseRefresh()
		t.Fatalf("force_recreate 未进入受控生产创建刷新: %v", replacementCtx.Err())
	}
	if _, ok := service.ActiveBrowseSessionInfo(); ok {
		releaseRefresh()
		t.Fatal("force_recreate 提交前旧 session 不得继续作为 active session")
	}
	oldCloseCtx, cancelOldClose := context.WithTimeout(context.Background(), 15*time.Second)
	oldCloseCancel = cancelOldClose
	oldCloseStarted = true
	go func() {
		defer close(oldCloseExited)
		id, err := currentGoroutineID()
		target <- serviceGoroutineIdentity{id: id, err: err}
		if err != nil {
			closed <- err
			return
		}
		closed <- service.CloseBrowseSession(oldCloseCtx, first.result.Session.ID)
	}()
	var identity serviceGoroutineIdentity
	select {
	case identity = <-target:
	case <-time.After(2 * time.Second):
		releaseRefresh()
		t.Fatal("获取 close_page 目标 goroutine 身份超时")
	}
	if identity.err != nil {
		releaseRefresh()
		t.Fatalf("获取 close_page 目标 goroutine ID 失败: %v", identity.err)
	}
	var stack []byte
	for {
		waiting, err := serviceMethodWaitingAtCreateSessionLock(&stack, identity.id, "CloseBrowseSession", lockFile, lockLine)
		if err != nil {
			releaseRefresh()
			t.Fatalf("观察 close_page 目标 goroutine 栈失败: %v", err)
		}
		if waiting {
			break
		}
		select {
		case err := <-closed:
			select {
			case <-oldCloseExited:
				oldCloseJoined = true
			case <-time.After(2 * time.Second):
				t.Fatal("旧 ID close caller 返回后未 join")
			}
			releaseRefresh()
			t.Fatalf("close_page 未等待 production force_recreate 提交锁便已完成: %v", err)
		case <-oldCloseCtx.Done():
			releaseRefresh()
			t.Fatalf("close_page 未到达 production 创建锁竞争边界: %v", oldCloseCtx.Err())
		default:
			runtime.Gosched()
		}
	}
	releaseRefresh()
	var replacement createResult
	select {
	case replacement = <-replacementDone:
		select {
		case <-replacementExited:
			replacementJoined = true
		case <-time.After(2 * time.Second):
			t.Fatal("force_recreate caller 返回后未 join")
		}
	case <-replacementCtx.Done():
		t.Fatalf("force_recreate 未完成 production session 提交: %v", replacementCtx.Err())
	}
	cancelReplacement()
	if replacement.err != nil || replacement.result == nil || replacement.result.Outcome != "created" || replacement.result.Session == nil || replacement.result.Session.ID == first.result.Session.ID {
		t.Fatalf("force_recreate 应提交不同的新 session: result=%+v err=%v", replacement.result, replacement.err)
	}
	select {
	case err := <-closed:
		select {
		case <-oldCloseExited:
			oldCloseJoined = true
		case <-time.After(2 * time.Second):
			t.Fatal("旧 ID close caller 返回后未 join")
		}
		cancelOldClose()
		if err == nil || !strings.Contains(err.Error(), "browse session 不存在") {
			t.Fatalf("并发 close_page 只能看到已被替换的旧 ID，不能关闭新 session: %v", err)
		}
	case <-oldCloseCtx.Done():
		t.Fatalf("close_page 未在 production 提交后完成: %v", oldCloseCtx.Err())
	}
	active, ok := service.ActiveBrowseSessionInfo()
	if !ok || active.ID != replacement.result.Session.ID {
		t.Fatalf("并发关闭旧 ID 后应保留同一 production 新 session: active=%+v exists=%t", active, ok)
	}
	assertServiceBusyOwnerWithWatchdog(t, browserManager, "session:"+replacement.result.Session.ID)
	parallelCloseCtx1, cancelParallelClose1 := context.WithTimeout(context.Background(), 15*time.Second)
	parallelCancel1 = cancelParallelClose1
	parallelCloseCtx2, cancelParallelClose2 := context.WithTimeout(context.Background(), 15*time.Second)
	parallelCancel2 = cancelParallelClose2
	parallelCloseStarted = 2
	for _, closeCtx := range []context.Context{parallelCloseCtx1, parallelCloseCtx2} {
		go func(ctx context.Context) {
			defer func() { parallelCloseExited <- struct{}{} }()
			closeResults <- service.CloseBrowseSession(ctx, replacement.result.Session.ID)
		}(closeCtx)
	}
	closeSuccesses := 0
	for attempt := 0; attempt < 2; attempt++ {
		select {
		case err := <-closeResults:
			if err == nil {
				closeSuccesses++
			} else if !strings.Contains(err.Error(), "browse session 不存在") {
				t.Fatalf("并发生产 close_page 失败: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("并发生产 close_page 未在独立 watchdog 内收敛")
		}
	}
	for parallelCloseJoined < parallelCloseStarted {
		select {
		case <-parallelCloseExited:
			parallelCloseJoined++
		case <-time.After(2 * time.Second):
			t.Fatal("并发生产 close_page caller 返回后未 join")
		}
	}
	cancelParallelClose1()
	cancelParallelClose2()
	if closeSuccesses == 0 {
		t.Fatal("并发 production close_page 至少一个调用必须完成关闭")
	}
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), 15*time.Second)
	page, err := acquireForWithWatchdog(browserManager, profileDir, probeCtx, cancelProbe, "post-close-probe", 20*time.Second)
	cancelProbe()
	if err != nil {
		t.Fatalf("新 session 关闭后 browser 独占权应可重新取得: %v", err)
	}
	probe = page
	probeHeld = true
	assertServiceBusyOwnerWithWatchdog(t, browserManager, "post-close-probe")
	if err := releaseProbe(); err != nil {
		t.Fatalf("post-close probe 单次归还失败: %v", err)
	}
	nextCtx, cancelNext := context.WithTimeout(context.Background(), 15*time.Second)
	page, err = acquireForWithWatchdog(browserManager, profileDir, nextCtx, cancelNext, "post-double-close-probe", 20*time.Second)
	cancelNext()
	if err != nil {
		t.Fatalf("probe 释放后下一次 Acquire 应成功: %v", err)
	}
	probe = page
	probeHeld = true
	probeReleaseAttempted = false
	if err := releaseProbe(); err != nil {
		t.Fatalf("最终 probe lease 单次归还失败: %v", err)
	}
}

func TestFatalCleanupInvalidatesDuringProductionSessionReuse(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("isolated Chrome process assertion requires Linux /proc")
	}
	reuseStarted := make(chan struct{}, 1)
	reuseRelease := make(chan struct{}, 1)
	releaseReuse := func() {
		select {
		case reuseRelease <- struct{}{}:
		default:
		}
	}
	defer releaseReuse()
	profileDir := t.TempDir()
	factory := newServiceBrowserFactory(t, nil, nil, reuseStarted, reuseRelease, profileDir)
	browserManager := browser.NewManager(factory, browser.WithWarmPageTTL(0), browser.WithIdleTimeout(time.Minute))
	sessions := xiaohongshu.NewBrowseSessionManager(time.Minute)
	service := &XiaohongshuService{browserManager: browserManager, browseSessions: sessions}
	var trackedProcesses []isolatedProcessIdentity
	type createResult struct {
		result *xiaohongshu.CreateBrowseSessionResult
		err    error
	}
	firstDone := make(chan createResult, 1)
	firstCompleted := false
	var reusedDone chan createResult
	reuseCompleted := false
	var fatalDone chan struct{}
	fatalCompleted := false
	t.Cleanup(func() {
		processes, processErr := isolatedBrowserProcessesForProfile(profileDir)
		if processErr != nil {
			t.Errorf("fatal fixture 清理前进程扫描失败: %v", processErr)
		} else {
			trackedProcesses = mergeIsolatedProcessIdentities(trackedProcesses, processes)
		}
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelCleanup()
		_ = service.Close(cleanupCtx)
		processes, processErr = isolatedBrowserProcessesForProfile(profileDir)
		if processErr != nil {
			t.Errorf("fatal fixture 清理后进程扫描失败: %v", processErr)
		} else {
			trackedProcesses = mergeIsolatedProcessIdentities(trackedProcesses, processes)
		}
		if err := killTrackedIsolatedBrowserProcesses(trackedProcesses); err != nil {
			t.Errorf("fatal fixture 已跟踪进程清理失败: %v", err)
		}
		if err := killIsolatedBrowserProcesses(profileDir); err != nil {
			t.Errorf("fatal fixture 独立进程清理失败: %v", err)
		}
		if !firstCompleted {
			select {
			case <-firstDone:
			case <-time.After(5 * time.Second):
				t.Errorf("fatal fixture 首次 CreateBrowseSession 清理后仍未结束")
			}
		}
		if reusedDone != nil && !reuseCompleted {
			select {
			case <-reusedDone:
			case <-time.After(5 * time.Second):
				t.Errorf("fatal fixture 复用 CreateBrowseSession 清理后仍未结束")
			}
		}
		if fatalDone != nil && !fatalCompleted {
			select {
			case <-fatalDone:
			case <-time.After(5 * time.Second):
				t.Errorf("fatal fixture 错误处理清理后仍未结束")
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go func() {
		result, err := service.CreateBrowseSession(ctx, false)
		firstDone <- createResult{result: result, err: err}
	}()
	var firstResult createResult
	select {
	case firstResult = <-firstDone:
		firstCompleted = true
	case <-time.After(35 * time.Second):
		t.Fatal("fatal fixture 首次 CreateBrowseSession 超过独立 Acquire/Page watchdog")
	}
	first, err := firstResult.result, firstResult.err
	if err != nil || first == nil || first.Outcome != "created" || first.Session == nil {
		t.Fatalf("创建 fatal 清理竞争的初始 session 失败: result=%+v err=%v", first, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		processes, processErr := isolatedBrowserProcessesForProfile(profileDir)
		if processErr != nil {
			t.Fatalf("记录 fatal fixture 的隔离进程身份失败: %v", processErr)
		}
		trackedProcesses = mergeIsolatedProcessIdentities(trackedProcesses, processes)
		if len(trackedProcesses) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("无法在 fatal cleanup 前识别隔离 browser 进程集合")
		}
		<-time.After(25 * time.Millisecond)
	}
	session, err := sessions.Get(first.Session.ID)
	if err != nil {
		t.Fatalf("读取初始 production session: %v", err)
	}
	reusedDone = make(chan createResult, 1)
	go func() {
		result, err := service.CreateBrowseSession(ctx, false)
		reusedDone <- createResult{result: result, err: err}
	}()
	select {
	case <-reuseStarted:
	case <-ctx.Done():
		releaseReuse()
		t.Fatalf("生产 CreateBrowseSession 未进入受控复用探测: %v", ctx.Err())
	}
	if _, ok := service.ActiveBrowseSessionInfo(); !ok {
		releaseReuse()
		t.Fatal("复用探测期间原 session 应保持登记")
	}
	fatalDone = make(chan struct{}, 1)
	fatalCtx, cancelFatal := context.WithCancel(ctx)
	cancelFatal()
	go func() {
		service.handleSessionOperationError(fatalCtx, first.Session.ID, session, xiaohongshu.ErrFatalRendererError)
		fatalDone <- struct{}{}
	}()
	select {
	case <-fatalDone:
		fatalCompleted = true
	case <-ctx.Done():
		releaseReuse()
		t.Fatalf("fatal cleanup 不应等待被取消的复用请求: %v", ctx.Err())
	}
	if _, ok := service.ActiveBrowseSessionInfo(); ok {
		releaseReuse()
		t.Fatal("fatal cleanup 返回前必须将 session 从 active 集合摘除")
	}
	releaseReuse()
	var reuseResult createResult
	select {
	case reuseResult = <-reusedDone:
		reuseCompleted = true
	case <-ctx.Done():
		t.Fatalf("释放复用探测后生产创建未结束: %v", ctx.Err())
	}
	if errors.Is(reuseResult.err, context.DeadlineExceeded) || ctx.Err() != nil {
		t.Fatalf("fatal invalidation 后复用不得以 watchdog/context 超时冒充业务错误: err=%v ctx=%v", reuseResult.err, ctx.Err())
	}
	if reuseResult.err == nil && (reuseResult.result == nil || reuseResult.result.Outcome != "blocked" || strings.TrimSpace(reuseResult.result.Status.LastError) == "") {
		t.Fatalf("fatal invalidation 后复用必须返回可见错误而非成功: %+v", reuseResult.result)
	}
	if reuseResult.err != nil && strings.TrimSpace(reuseResult.err.Error()) == "" {
		t.Fatal("fatal invalidation 后生产复用返回空错误")
	}
	if reuseResult.result != nil && reuseResult.result.Session != nil && reuseResult.result.Session.ID == first.Session.ID {
		t.Fatalf("fatal cleanup 后生产复用不得返回失效旧 session: %+v", reuseResult.result)
	}
	if _, ok := service.ActiveBrowseSessionInfo(); ok {
		t.Fatal("fatal cleanup 后不得重新发布失效 session")
	}
	processes, processErr := isolatedBrowserProcessesForProfile(profileDir)
	if processErr != nil {
		t.Fatalf("整体关闭前扩展隔离 browser 进程身份集合失败: %v", processErr)
	}
	trackedProcesses = mergeIsolatedProcessIdentities(trackedProcesses, processes)
	closeCtx, cancelClose := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelClose()
	if err := service.Close(closeCtx); err != nil {
		t.Fatalf("fatal 最终清理的 Service.Close 失败: %v", err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		alive, err := aliveIsolatedProcessIdentities(trackedProcesses)
		if err != nil {
			t.Fatalf("复核 fatal fixture 已跟踪进程 PID/starttime 失败: %v", err)
		}
		if len(alive) == 0 {
			if pending := browserManagerPendingLifecycle(browserManager); pending != "" {
				t.Fatalf("fatal 复用清理退出进程后 manager 仍有 pending/reset operation: %s", pending)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("fatal 最终关闭后已跟踪隔离 Chrome 进程仍存在: %v", alive)
		}
		select {
		case <-time.After(25 * time.Millisecond):
		}
	}
	verifyCtx, cancelVerify := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelVerify()
	if err := sessions.CloseContext(verifyCtx, first.Session.ID); err == nil || !strings.Contains(err.Error(), "browse session 不存在") {
		t.Fatalf("Service.Close 完成后旧 session 与 worker 必须完全退休: %v", err)
	}
}

type serviceGoroutineIdentity struct {
	id  uint64
	err error
}

func currentGoroutineID() (uint64, error) {
	for size := 128; size <= 1<<20; size *= 2 {
		stack := make([]byte, size)
		n := runtime.Stack(stack, false)
		if n == len(stack) {
			continue
		}
		header, _, ok := strings.Cut(string(stack[:n]), "\n")
		fields := strings.Fields(header)
		if !ok || len(fields) < 2 || fields[0] != "goroutine" {
			return 0, fmt.Errorf("unsupported runtime.Stack goroutine header %q", header)
		}
		id, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse runtime.Stack goroutine ID from %q: %w", header, err)
		}
		return id, nil
	}
	return 0, fmt.Errorf("runtime.Stack goroutine header remained truncated at 1 MiB")
}

func serviceCreateSessionLockPosition(t *testing.T, method string) (string, int) {
	t.Helper()
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位 service_p0_test.go 以读取 service.go")
	}
	sourceFile := filepath.Clean(filepath.Join(filepath.Dir(testFile), "service.go"))
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, sourceFile, nil, parser.AllErrors)
	if err != nil {
		t.Fatalf("解析生产源码 %s 失败: %v", sourceFile, err)
	}
	methodFound := false
	var lockPositions []token.Position
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != method || function.Recv == nil || len(function.Recv.List) != 1 {
			continue
		}
		receiverDecl := function.Recv.List[0]
		receiver := receiverDecl.Type
		if pointer, ok := receiver.(*ast.StarExpr); ok {
			receiver = pointer.X
		}
		name, ok := receiver.(*ast.Ident)
		if !ok || name.Name != "XiaohongshuService" || len(receiverDecl.Names) != 1 {
			continue
		}
		receiverName := receiverDecl.Names[0].Name
		if receiverName == "" {
			continue
		}
		methodFound = true
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			gate, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || gate.Sel.Name != "acquireCreateSessionGate" {
				return true
			}
			gateReceiver, ok := gate.X.(*ast.Ident)
			if ok && gateReceiver.Name == receiverName {
				lockPositions = append(lockPositions, fset.Position(call.Pos()))
			}
			return true
		})
	}
	if !methodFound || len(lockPositions) != 1 {
		t.Fatalf("%s 中应恰有一个 acquireCreateSessionGate 调用；method_found=%t call_count=%d", method, methodFound, len(lockPositions))
	}
	return filepath.Clean(lockPositions[0].Filename), lockPositions[0].Line
}

func serviceMethodWaitingAtCreateSessionLock(stack *[]byte, targetID uint64, method, sourcePath string, sourceLine int) (bool, error) {
	const initialStackSize = 64 << 10
	const maxStackSize = 16 << 20
	if *stack == nil {
		*stack = make([]byte, initialStackSize)
	}
	var n int
	for {
		n = runtime.Stack(*stack, true)
		if n < len(*stack) {
			break
		}
		if len(*stack) >= maxStackSize {
			return false, fmt.Errorf("runtime.Stack 输出在 %d 字节后仍可能被截断；捕获片段:\n%s", maxStackSize, serviceStackExcerpt((*stack)[:n]))
		}
		nextSize := len(*stack) * 2
		if nextSize > maxStackSize {
			nextSize = maxStackSize
		}
		*stack = make([]byte, nextSize)
	}

	for _, goroutine := range strings.Split(string((*stack)[:n]), "\n\n") {
		if goroutine == "" {
			continue
		}
		frames := strings.Split(goroutine, "\n")
		if len(frames) == 0 {
			continue
		}
		id, state, err := parseServiceGoroutineHeader(frames[0])
		if err != nil {
			return false, fmt.Errorf("不支持 runtime.Stack goroutine header: %w；实际栈:\n%s", err, goroutine)
		}
		if id != targetID {
			continue
		}
		methodFrame := "(*XiaohongshuService)." + method + "("
		for i, frame := range frames {
			if !strings.Contains(frame, methodFrame) {
				continue
			}
			if i+1 >= len(frames) {
				return false, fmt.Errorf("目标方法栈帧缺少源码位置；实际栈:\n%s", goroutine)
			}
			stackPath, line, err := parseServiceStackSourceFrame(frames[i+1])
			if err != nil {
				return false, fmt.Errorf("不支持 runtime.Stack 源码帧: %w；实际栈:\n%s", err, goroutine)
			}
			if stackPath != sourcePath {
				return false, fmt.Errorf("runtime.Stack 源码路径与生产源码不一致: got=%q want=%q；实际栈:\n%s", stackPath, sourcePath, goroutine)
			}
			if line != sourceLine {
				return false, nil
			}
			switch state {
			case "select":
				return true, nil
			case "running", "runnable":
				return false, nil
			default:
				return false, fmt.Errorf("目标 goroutine 位于 acquireCreateSessionGate 调用行但等待状态 %q 不受支持；实际栈:\n%s", state, goroutine)
			}
		}
		if strings.Contains(goroutine, filepath.Base(sourcePath)+":") {
			return false, fmt.Errorf("目标 goroutine 栈包含 service.go 但未识别方法帧格式 %q；实际栈:\n%s", method, goroutine)
		}
	}
	return false, nil
}

func parseServiceGoroutineHeader(header string) (uint64, string, error) {
	fields := strings.Fields(header)
	if len(fields) < 2 || fields[0] != "goroutine" {
		return 0, "", fmt.Errorf("header=%q", header)
	}
	id, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0, "", fmt.Errorf("header=%q: %w", header, err)
	}
	open := strings.IndexByte(header, '[')
	close := strings.IndexByte(header, ']')
	if open < 0 || close <= open {
		return 0, "", fmt.Errorf("header=%q", header)
	}
	state := header[open+1 : close]
	if comma := strings.IndexByte(state, ','); comma >= 0 {
		state = state[:comma]
	}
	if state == "" {
		return 0, "", fmt.Errorf("header=%q", header)
	}
	return id, state, nil
}

func parseServiceStackSourceFrame(frame string) (string, int, error) {
	location := strings.TrimSpace(frame)
	if location == "" {
		return "", 0, fmt.Errorf("empty source frame %q", frame)
	}
	if offsetStart := strings.LastIndex(location, " +0x"); offsetStart > strings.LastIndexByte(location, ':') {
		offset := location[offsetStart+1:]
		if !strings.HasPrefix(offset, "+0x") {
			return "", 0, fmt.Errorf("invalid source offset=%q", offset)
		}
		if _, err := strconv.ParseUint(offset[1:], 0, 64); err != nil {
			return "", 0, fmt.Errorf("invalid source offset=%q: %w", offset, err)
		}
		location = location[:offsetStart]
	}
	colon := strings.LastIndexByte(location, ':')
	if colon <= 0 {
		return "", 0, fmt.Errorf("source location=%q", location)
	}
	line, err := strconv.Atoi(location[colon+1:])
	if err != nil {
		return "", 0, fmt.Errorf("source location=%q: %w", location, err)
	}
	if line <= 0 {
		return "", 0, fmt.Errorf("source line must be positive in %q", location)
	}
	sourcePath := location[:colon]
	return filepath.Clean(sourcePath), line, nil
}

func serviceStackExcerpt(stack []byte) string {
	const excerptSize = 4096
	if len(stack) <= 2*excerptSize {
		return string(stack)
	}
	return string(stack[:excerptSize]) + "\n... stack output may be truncated ...\n" + string(stack[len(stack)-excerptSize:])
}

func TestServiceCloseCancelsProductionBrowseSessionCreation(t *testing.T) {
	refreshStarted := make(chan struct{}, 1)
	refreshRelease := make(chan struct{}, 1)
	releaseRefresh := func() {
		select {
		case refreshRelease <- struct{}{}:
		default:
		}
	}
	defer releaseRefresh()
	profileDir := t.TempDir()
	factory := newServiceBrowserFactory(t, refreshStarted, refreshRelease, nil, nil, profileDir)
	browserManager := browser.NewManager(factory, browser.WithWarmPageTTL(0), browser.WithIdleTimeout(time.Minute))
	sessions := xiaohongshu.NewBrowseSessionManager(time.Minute)
	service := &XiaohongshuService{browserManager: browserManager, browseSessions: sessions}
	t.Cleanup(func() {
		if err := runFixtureCloseWithWatchdog(10*time.Second, 2*time.Second, sessions.CloseAllContext); err != nil {
			t.Errorf("session creation fixture cleanup failed: %v", err)
		}
		if err := runFixtureCloseWithWatchdog(10*time.Second, 2*time.Second, browserManager.Close); err != nil {
			t.Errorf("session creation browser fixture cleanup failed: %v", err)
		}
	})
	loginDone := make(chan struct{})
	close(loginDone)
	closeReached := make(chan struct{})
	service.loginQRMu.Lock()
	service.loginQR = &loginQrcodeSession{cancel: func() { close(closeReached) }, done: loginDone}
	service.loginQRMu.Unlock()

	type createResult struct {
		result *xiaohongshu.CreateBrowseSessionResult
		err    error
	}
	createCtx := context.Background()
	closeCtx, cancelClose := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelClose()
	createdDone := make(chan createResult, 1)
	go func() {
		result, err := service.CreateBrowseSession(createCtx, false)
		createdDone <- createResult{result: result, err: err}
	}()
	select {
	case <-refreshStarted:
	case <-closeCtx.Done():
		releaseRefresh()
		t.Fatalf("生产 CreateBrowseSession 未进入 session 初始状态刷新: %v", closeCtx.Err())
	}
	closedDone := make(chan error, 1)
	go func() {
		closedDone <- service.Close(closeCtx)
	}()
	select {
	case <-closeReached:
	case <-closeCtx.Done():
		releaseRefresh()
		t.Fatalf("服务关闭未进入清理流程: %v", closeCtx.Err())
	}
	select {
	case created := <-createdDone:
		if created.err == nil || !errors.Is(created.err, context.Canceled) {
			t.Fatalf("服务关闭必须取消生产创建且不得提交会话: result=%+v err=%v", created.result, created.err)
		}
	case <-closeCtx.Done():
		releaseRefresh()
		t.Fatalf("服务关闭后生产创建仍未结束: %v", closeCtx.Err())
	}
	select {
	case err := <-closedDone:
		if err != nil {
			t.Fatalf("取消生产创建后服务关闭失败: %v", err)
		}
	case <-closeCtx.Done():
		t.Fatalf("服务关闭未在期限内完成: %v", closeCtx.Err())
	}
	if _, ok := service.ActiveBrowseSessionInfo(); ok {
		t.Fatal("服务关闭完成后不应保留 active session")
	}
	if _, err := service.CreateBrowseSession(context.Background(), false); !errors.Is(err, errBrowseSessionServiceClosing) {
		t.Fatalf("关闭开始后必须拒绝新建会话: %v", err)
	}
}

func TestServiceCloseDeadlineWhileCreationGateHeld(t *testing.T) {
	service := &XiaohongshuService{}
	releaseGate, err := service.acquireCreateSessionGate(context.Background(), false)
	if err != nil {
		t.Fatalf("获取创建串行门失败: %v", err)
	}
	createCtx, cancelCreate, err := service.beginCreateSession(context.Background())
	if err != nil {
		releaseGate()
		t.Fatalf("登记在途创建失败: %v", err)
	}
	queued := make(chan error, 1)
	go func() {
		_, err := service.CreateBrowseSession(context.Background(), false)
		queued <- err
	}()
	closeCtx, cancelClose := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelClose()
	err = service.Close(closeCtx)
	if !errors.Is(err, context.DeadlineExceeded) {
		cancelCreate()
		releaseGate()
		t.Fatalf("关闭等待必须服从独立 deadline: %v", err)
	}
	select {
	case <-createCtx.Done():
	default:
		cancelCreate()
		releaseGate()
		t.Fatal("关闭开始时必须取消在途创建 context")
	}
	if _, err := service.CreateBrowseSession(context.Background(), false); !errors.Is(err, errBrowseSessionServiceClosing) {
		cancelCreate()
		releaseGate()
		t.Fatalf("关闭等待期间必须拒绝新建会话: %v", err)
	}
	select {
	case err := <-queued:
		if !errors.Is(err, errBrowseSessionServiceClosing) {
			cancelCreate()
			releaseGate()
			t.Fatalf("关闭开始后排队创建必须被拒绝: %v", err)
		}
	case <-time.After(time.Second):
		cancelCreate()
		releaseGate()
		t.Fatal("关闭开始后排队创建未及时退出")
	}
	service.endCreateSession(cancelCreate)
	releaseGate()
	retryCtx, cancelRetry := context.WithTimeout(context.Background(), time.Second)
	defer cancelRetry()
	if err := service.Close(retryCtx); err != nil {
		t.Fatalf("释放在途创建后重试 Close 应完成清理: %v", err)
	}
}

func TestCloseBrowseSessionWaitsForExplicitCreationGate(t *testing.T) {
	manager := xiaohongshu.NewBrowseSessionManager(time.Minute)
	service := &XiaohongshuService{browseSessions: manager}
	releases := 0
	result, err := service.registerReadyBrowseSession(context.Background(), nil, func(*hrod.Page) { releases++ }, func(string) {})
	if err != nil || result == nil || result.Session == nil {
		t.Fatalf("创建已提交 session 失败: result=%+v err=%v", result, err)
	}
	releaseGate, err := service.acquireCreateSessionGate(context.Background(), false)
	if err != nil {
		t.Fatalf("获取创建串行门失败: %v", err)
	}
	shortCtx, cancelShort := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelShort()
	closed := make(chan error, 1)
	go func() { closed <- service.CloseBrowseSession(shortCtx, result.Session.ID) }()
	select {
	case err := <-closed:
		if !errors.Is(err, context.DeadlineExceeded) {
			releaseGate()
			t.Fatalf("close_page 等待 gate 必须服从请求 deadline: %v", err)
		}
	case <-time.After(2 * time.Second):
		releaseGate()
		t.Fatal("close_page 未在独立 watchdog 内返回")
	}
	if active, ok := service.ActiveBrowseSessionInfo(); !ok || active.ID != result.Session.ID {
		releaseGate()
		t.Fatalf("deadline 返回前 session 应仍 active: info=%+v ok=%t", active, ok)
	}
	releaseGate()
	retryCtx, cancelRetry := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelRetry()
	if err := service.CloseBrowseSession(retryCtx, result.Session.ID); err != nil {
		t.Fatalf("释放 gate 后重试 close_page 失败: %v", err)
	}
	if _, ok := service.ActiveBrowseSessionInfo(); ok {
		t.Fatal("重试关闭后不应保留 active session")
	}
	if releases != 0 {
		t.Fatalf("nil page 不应归还 browser lease，实际调用 %d 次", releases)
	}
}

func TestRegisterReadyBrowseSessionCommitAtomicity(t *testing.T) {
	manager := xiaohongshu.NewBrowseSessionManager(time.Minute)
	service := &XiaohongshuService{browseSessions: manager}
	releases := 0
	ownerReached := make(chan string, 1)
	allowOwner := make(chan struct{}, 1)
	releaseOwner := func() {
		select {
		case allowOwner <- struct{}{}:
		default:
		}
	}
	type registerResult struct {
		result *xiaohongshu.CreateBrowseSessionResult
		err    error
	}
	registered := make(chan registerResult, 1)
	registrationReceived := false
	defer func() {
		releaseOwner()
		if !registrationReceived {
			select {
			case <-registered:
			case <-time.After(2 * time.Second):
			}
		}
	}()
	go func() {
		result, err := service.registerReadyBrowseSession(context.Background(), nil, func(*hrod.Page) { releases++ }, func(owner string) {
			ownerReached <- owner
			select {
			case <-allowOwner:
			case <-time.After(2 * time.Second):
			}
		})
		registered <- registerResult{result: result, err: err}
	}()

	var owner string
	select {
	case owner = <-ownerReached:
	case <-time.After(2 * time.Second):
		releaseOwner()
		t.Fatal("owner callback 未在独立 watchdog 内到达")
	}
	id := strings.TrimPrefix(owner, "session:")
	if id == "" {
		releaseOwner()
		t.Fatalf("owner 应带 session ID: %q", owner)
	}
	type activeResult struct {
		info xiaohongshu.BrowseSessionInfo
		ok   bool
	}
	active := make(chan activeResult, 1)
	go func() {
		info, ok := service.ActiveBrowseSessionInfo()
		active <- activeResult{info: info, ok: ok}
	}()
	select {
	case got := <-active:
		releaseOwner()
		t.Fatalf("owner 交接提交前不得暴露 session: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
	releaseOwner()
	var registeredResult registerResult
	select {
	case registeredResult = <-registered:
		registrationReceived = true
	case <-time.After(2 * time.Second):
		t.Fatal("session 注册未在独立 watchdog 内完成")
	}
	if registeredResult.err != nil || registeredResult.result == nil || registeredResult.result.Session == nil || registeredResult.result.Session.ID != id {
		t.Fatalf("原子提交应返回同一 session: %+v", registeredResult)
	}
	select {
	case got := <-active:
		if !got.ok || got.info.ID != id {
			t.Fatalf("owner 提交后应暴露同一 session: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("active reader 未在独立 watchdog 内完成")
	}
	closeCtx, cancelClose := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelClose()
	closed := make(chan error, 1)
	go func() { closed <- service.CloseBrowseSession(closeCtx, id) }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("关闭已提交 session 失败: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("close_page 未在独立 watchdog 内完成")
	}
	if releases != 0 {
		t.Fatalf("nil page 不应归还 browser lease，实际调用 %d 次", releases)
	}
}

type serviceCDPCaller interface {
	Call(context.Context, string, string, interface{}) ([]byte, error)
}

type serviceInitialRefreshGate struct {
	entered     chan struct{}
	release     chan struct{}
	released    chan struct{}
	exited      chan struct{}
	abortCh     chan struct{}
	mu          sync.Mutex
	claimed     bool
	releaseRead bool
	fixtureErr  error
	releaseOnce sync.Once
	abortOnce   sync.Once
	exitedOnce  sync.Once
}

func newServiceInitialRefreshGate() *serviceInitialRefreshGate {
	return &serviceInitialRefreshGate{
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
		released: make(chan struct{}),
		exited:   make(chan struct{}),
		abortCh:  make(chan struct{}),
	}
}

func (g *serviceInitialRefreshGate) open() {
	g.releaseOnce.Do(func() {
		close(g.release)
	})
}

func (g *serviceInitialRefreshGate) abort() {
	g.abortOnce.Do(func() {
		g.mu.Lock()
		if g.claimed && !g.releaseRead && g.fixtureErr == nil {
			g.fixtureErr = errors.New("initial refresh CDP fixture gate aborted during cleanup before release acknowledgement")
		}
		g.mu.Unlock()
		close(g.abortCh)
	})
}

func (g *serviceInitialRefreshGate) recordFixtureError(err error) {
	if err == nil {
		return
	}
	g.mu.Lock()
	g.fixtureErr = errors.Join(g.fixtureErr, err)
	g.mu.Unlock()
}

func (g *serviceInitialRefreshGate) fixtureError() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.fixtureErr
}

func (c *serviceInitialRefreshGateClient) Event() <-chan *cdp.Event {
	// 仅在 Connect 后拦截 Call，原始 CDP 事件泵继续工作。
	return nil
}

func serviceInitialRefreshCDPCall(method string, params interface{}) bool {
	if method != "Runtime.callFunctionOn" {
		return false
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return false
	}
	declaration := string(encoded)
	return strings.Contains(declaration, "url: location.href") && strings.Contains(declaration, "scroll_y: scrollY()")
}

func (c *serviceInitialRefreshGateClient) Call(ctx context.Context, sessionID, method string, params interface{}) ([]byte, error) {
	if serviceInitialRefreshCDPCall(method, params) {
		c.gate.mu.Lock()
		intercept := !c.gate.claimed
		if intercept {
			c.gate.claimed = true
			close(c.gate.entered)
		}
		c.gate.mu.Unlock()
		if intercept {
			defer c.gate.exitedOnce.Do(func() { close(c.gate.exited) })
			timer := time.NewTimer(30 * time.Second)
			defer timer.Stop()
			select {
			case <-c.gate.release:
				c.gate.mu.Lock()
				c.gate.releaseRead = true
				close(c.gate.released)
				c.gate.mu.Unlock()
			case <-c.gate.abortCh:
				err := errors.New("initial refresh CDP fixture gate aborted before release")
				c.gate.recordFixtureError(err)
				return nil, err
			case <-timer.C:
				err := errors.New("initial refresh CDP fixture release gate exceeded independent 30s watchdog")
				c.gate.recordFixtureError(err)
				return nil, err
			}
			result, err := c.upstream.Call(ctx, sessionID, method, params)
			return result, err
		}
	}
	return c.upstream.Call(ctx, sessionID, method, params)
}

type serviceInitialRefreshGateClient struct {
	upstream serviceCDPCaller
	gate     *serviceInitialRefreshGate
}

func waitServiceInitialRefreshEntered(t *testing.T, gate *serviceInitialRefreshGate) {
	t.Helper()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case <-gate.entered:
	case <-timer.C:
		t.Fatal("初始 {url,scroll_y} 同步 CDP 调用未在 30s fixture watchdog 内进入")
	}
}

func waitServiceInitialRefreshExited(t *testing.T, gate *serviceInitialRefreshGate) {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case <-gate.exited:
		if err := gate.fixtureError(); err != nil {
			t.Fatalf("初始刷新 CDP fixture 异常: %v", err)
		}
	case <-timer.C:
		t.Fatal("放行初始刷新 CDP 调用后未在 10s watchdog 内退出")
	}
}

func reportServiceInitialRefreshFixtureError(t *testing.T, gate *serviceInitialRefreshGate, phase string) {
	t.Helper()
	if err := gate.fixtureError(); err != nil {
		t.Errorf("%s: 初始刷新 CDP fixture 错误: %v", phase, err)
	}
}

type failingOwnedPageCDPClient struct {
	upstream             serviceCDPCaller
	mu                   sync.Mutex
	pageCloseCalls       int
	browserCloseCalls    int
	pageCloseFailures    int
	browserCloseFailures int
	pageCloseStarted     chan struct{}
	browserCloseStarted  chan struct{}
	browserCloseErr      error
}

func (c *failingOwnedPageCDPClient) Event() <-chan *cdp.Event {
	// 仅在 Connect 后拦截 Call，原始 CDP 事件泵继续工作。
	return nil
}

func (c *failingOwnedPageCDPClient) Call(ctx context.Context, sessionID, method string, params interface{}) ([]byte, error) {
	if method == "Page.close" {
		c.mu.Lock()
		c.pageCloseCalls++
		fail := c.pageCloseFailures > 0
		if fail {
			c.pageCloseFailures--
		}
		started := c.pageCloseStarted
		c.mu.Unlock()
		if fail {
			if started != nil {
				select {
				case started <- struct{}{}:
				default:
				}
			}
			timer := time.NewTimer(5 * time.Second)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-timer.C:
				return nil, errors.New("injected Page.close cancellation watchdog expired")
			}
		}
	}
	if method == "Browser.close" {
		c.mu.Lock()
		c.browserCloseCalls++
		fail := c.browserCloseFailures > 0
		if fail {
			c.browserCloseFailures--
		}
		started := c.browserCloseStarted
		closeErr := c.browserCloseErr
		c.mu.Unlock()
		if fail {
			if started != nil {
				select {
				case started <- struct{}{}:
				default:
				}
			}
			if closeErr == nil {
				closeErr = errors.New("injected Browser.close failure")
			}
			return nil, closeErr
		}
	}
	return c.upstream.Call(ctx, sessionID, method, params)
}

func (c *failingOwnedPageCDPClient) callCounts() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pageCloseCalls, c.browserCloseCalls
}

type browseSessionLifecycleState struct {
	session    reflect.Value
	inSessions bool
	inClosing  bool
}

func browseSessionLifecycleForTest(manager *xiaohongshu.BrowseSessionManager, id string) browseSessionLifecycleState {
	state := reflect.ValueOf(manager).Elem()
	key := reflect.ValueOf(id)
	sessions := state.FieldByName("sessions").MapIndex(key)
	closing := state.FieldByName("closing").MapIndex(key)
	result := browseSessionLifecycleState{inSessions: sessions.IsValid(), inClosing: closing.IsValid()}
	if closing.IsValid() && !closing.IsNil() {
		result.session = closing
	} else if sessions.IsValid() && !sessions.IsNil() {
		result.session = sessions
	}
	return result
}

func browserManagerRetainedPageFailureForTest(manager *browser.Manager, page *hrod.Page) string {
	state := reflect.ValueOf(manager).Elem()
	resetPages := state.FieldByName("resetPages")
	resetErrors := state.FieldByName("resetErrByBrowser")
	browserCleanups := state.FieldByName("browserCleanups")
	pendingBrowsers := state.FieldByName("pendingBrowsers")
	if !resetPages.IsValid() || !resetErrors.IsValid() || !browserCleanups.IsValid() || !pendingBrowsers.IsValid() {
		return "manager cleanup fields unavailable"
	}
	pagePointer := reflect.ValueOf(page).Pointer()
	browserPointer := reflect.ValueOf(page.Browser()).Pointer()
	pageRecordFound := false
	for index := 0; index < resetPages.Len(); index++ {
		record := resetPages.Index(index).Elem()
		if record.FieldByName("page").Pointer() != pagePointer || record.FieldByName("browser").Pointer() != browserPointer {
			continue
		}
		if record.FieldByName("err").IsNil() {
			return "matching pending page record has no retained error"
		}
		pageRecordFound = true
		break
	}
	if !pageRecordFound {
		return "matching pending page record was not retained"
	}
	browserKey := reflect.ValueOf(page.Browser())
	if resetErr := resetErrors.MapIndex(browserKey); !resetErr.IsValid() || resetErr.IsNil() {
		return "browser reset error bucket was not retained"
	}
	if cleanup := browserCleanups.MapIndex(browserKey); !cleanup.IsValid() || cleanup.IsNil() {
		return "browser cleanup record was not retained"
	}
	return ""
}

func releaseAndCloseWithWatchdog(manager *browser.Manager, page *hrod.Page, contextTimeout, watchdog time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), contextTimeout)
	defer cancel()
	result := make(chan error, 1)
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		result <- manager.ReleaseAndCloseContext(ctx, page)
	}()
	timer := time.NewTimer(watchdog)
	defer timer.Stop()
	select {
	case err := <-result:
		select {
		case <-joined:
			return err
		case <-time.After(2 * time.Second):
			cancel()
			return errors.Join(err, errors.New("lease release goroutine did not join"))
		}
	case <-timer.C:
		cancel()
		select {
		case <-joined:
			return errors.New("lease release exceeded independent watchdog")
		case <-time.After(2 * time.Second):
			return errors.New("lease release remained blocked after watchdog cancellation")
		}
	}
}

func TestProductionOwnedPageCloseFailureRetriesWithoutReleasingProbeLease(t *testing.T) {
	profileDir := t.TempDir()
	factory := newServiceBrowserFactory(t, nil, nil, nil, nil, profileDir)
	manager := browser.NewManager(factory, browser.WithWarmPageTTL(0), browser.WithIdleTimeout(time.Minute))
	sessions := xiaohongshu.NewBrowseSessionManager(time.Minute)
	service := &XiaohongshuService{browserManager: manager, browseSessions: sessions}
	t.Cleanup(func() {
		if err := runFixtureCloseWithWatchdog(25*time.Second, 2*time.Second, sessions.CloseAllContext); err != nil {
			t.Errorf("真实 Browser-owned session 清理失败: %v", err)
		}
		if err := runFixtureCloseWithWatchdog(25*time.Second, 2*time.Second, manager.Close); err != nil {
			t.Errorf("真实 Browser-owned manager 清理失败: %v", err)
		}
	})
	acquireCtx, cancelAcquire := context.WithTimeout(context.Background(), 30*time.Second)
	page, err := acquireForWithWatchdog(manager, profileDir, acquireCtx, cancelAcquire, "browser_operation", 35*time.Second)
	cancelAcquire()
	if err != nil {
		t.Fatalf("获取真实 Browser-owned page 失败: %v", err)
	}
	if page.Browser() == nil || page.Rod == nil || page.Rod.Browser() == nil {
		t.Fatal("fixture 必须经 headless_browser/hrod 生产构造链取得带 owner 的 page")
	}
	createCtx, cancelCreate := context.WithTimeout(context.Background(), 5*time.Second)
	created, err := service.registerReadyBrowseSession(createCtx, page, manager.Release, manager.UpdateOwner)
	cancelCreate()
	if err != nil || created == nil || created.Session == nil {
		t.Fatalf("真实 production session 注册失败: result=%+v err=%v", created, err)
	}
	rodBrowser := page.Rod.Browser()
	upstream := rodBrowser.Context(rodBrowser.GetContext())
	injected := &failingOwnedPageCDPClient{
		upstream: upstream, pageCloseFailures: 1, browserCloseFailures: 1,
		pageCloseStarted: make(chan struct{}, 1), browserCloseStarted: make(chan struct{}, 1),
		browserCloseErr: errors.New("injected Browser.close failure"),
	}
	rodBrowser.Client(injected)
	firstCtx, cancelFirst := context.WithTimeout(context.Background(), 20*time.Second)
	firstDone := make(chan error, 1)
	firstResultRead := false
	go func() { firstDone <- sessions.CloseContext(firstCtx, created.Session.ID) }()
	t.Cleanup(func() {
		cancelFirst()
		if !firstResultRead {
			select {
			case <-firstDone:
			case <-time.After(8 * time.Second):
				t.Errorf("生产 session close goroutine 未在失败清场 watchdog 内退出")
			}
		}
	})
	select {
	case <-injected.pageCloseStarted:
	case <-firstCtx.Done():
		cancelFirst()
		t.Fatalf("生产 Page.close 未到达注入点: %v", firstCtx.Err())
	case <-time.After(10 * time.Second):
		cancelFirst()
		t.Fatal("生产 Page.close 未在独立 watchdog 内到达")
	}
	cancelFirst()
	select {
	case <-injected.browserCloseStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("Page.close context 失败后生产 Browser.close 未到达注入点")
	}
	select {
	case err := <-firstDone:
		firstResultRead = true
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("首次真实关闭应保留 Page.close context 错误: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("首次真实 session close 超过独立 watchdog")
	}
	sessionState := browseSessionLifecycleForTest(sessions, created.Session.ID)
	if !sessionState.inClosing || sessionState.inSessions || !sessionState.session.IsValid() || sessionState.session.IsNil() {
		t.Fatalf("首次错误返回后 BrowseSession 必须留在 closing 而非丢失: sessions=%t closing=%t", sessionState.inSessions, sessionState.inClosing)
	}
	internalSession := sessionState.session.Elem()
	if internalSession.FieldByName("page").Pointer() != reflect.ValueOf(page).Pointer() || !internalSession.FieldByName("closed").Bool() || internalSession.FieldByName("closeErr").IsNil() || !internalSession.FieldByName("closeLeaseReleased").Bool() {
		t.Fatal("首次错误返回后 BrowseSession 必须保留 page、closing 与 lease 清理责任及错误")
	}
	if retained := browserManagerRetainedPageFailureForTest(manager, page); retained != "" {
		t.Fatalf("首次错误返回后 Manager 必须保留 page record 与 browser error: %s", retained)
	}
	pageCalls, browserCalls := injected.callCounts()
	if pageCalls != 1 || browserCalls != 1 {
		t.Fatalf("首次失败必须真实经过一次 Page.close 和 Browser.close: page=%d browser=%d", pageCalls, browserCalls)
	}
	resetCtx, cancelReset := context.WithTimeout(context.Background(), 25*time.Second)
	resetDone := make(chan error, 1)
	resetResultRead := false
	go func() { resetDone <- manager.Reset(resetCtx) }()
	t.Cleanup(func() {
		cancelReset()
		if !resetResultRead {
			select {
			case <-resetDone:
			case <-time.After(8 * time.Second):
				t.Errorf("production Reset goroutine 未在失败清场 watchdog 内退出")
			}
		}
	})
	select {
	case err := <-resetDone:
		resetResultRead = true
		if err != nil {
			cancelReset()
			t.Fatalf("production Reset 退休旧 browser 失败: %v", err)
		}
	case <-resetCtx.Done():
		cancelReset()
		t.Fatalf("旧 browser cleanup 未在 Reset watchdog 内完成: %v", resetCtx.Err())
	case <-time.After(27 * time.Second):
		cancelReset()
		t.Fatal("production Reset 超过独立 watchdog")
	}
	cancelReset()
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), 30*time.Second)
	probe, err := acquireForWithWatchdog(manager, profileDir, probeCtx, cancelProbe, "retry-lease-probe", 35*time.Second)
	cancelProbe()
	if err != nil {
		t.Fatalf("清理后实际取得 probe lease 失败: %v", err)
	}
	retryCtx, cancelRetry := context.WithTimeout(context.Background(), 15*time.Second)
	retryDone := make(chan error, 1)
	retryResultRead, busyResultRead := false, false
	go func() { retryDone <- sessions.CloseContext(retryCtx, created.Session.ID) }()
	busyCtx, cancelBusy := context.WithTimeout(context.Background(), 7*time.Second)
	busyDone := make(chan error, 1)
	go func() { _, err := manager.AcquireFor(busyCtx, "lease-contender"); busyDone <- err }()
	t.Cleanup(func() {
		cancelRetry()
		cancelBusy()
		if !retryResultRead {
			select {
			case <-retryDone:
			case <-time.After(8 * time.Second):
				t.Errorf("session retry goroutine 未在失败清场 watchdog 内退出")
			}
		}
		if !busyResultRead {
			select {
			case <-busyDone:
			case <-time.After(8 * time.Second):
				t.Errorf("lease contender goroutine 未在失败清场 watchdog 内退出")
			}
		}
	})
	select {
	case err := <-retryDone:
		retryResultRead = true
		if err != nil {
			cancelRetry()
			cancelBusy()
			t.Fatalf("session cleanup retry 失败: %v", err)
		}
	case <-retryCtx.Done():
		cancelRetry()
		cancelBusy()
		t.Fatalf("session cleanup retry 超时: %v", retryCtx.Err())
	case <-time.After(17 * time.Second):
		cancelRetry()
		cancelBusy()
		t.Fatal("session cleanup retry 超过独立 watchdog")
	}
	select {
	case err := <-busyDone:
		busyResultRead = true
		var busy *browser.BusyError
		if !errors.As(err, &busy) {
			cancelRetry()
			cancelBusy()
			t.Fatalf("probe lease 持有期间 contender 必须仍 busy: %v", err)
		}
	case <-busyCtx.Done():
		cancelRetry()
		cancelBusy()
		t.Fatalf("contender busy 检查超时: %v", busyCtx.Err())
	case <-time.After(8 * time.Second):
		cancelRetry()
		cancelBusy()
		t.Fatal("contender busy 检查超过独立 watchdog")
	}
	cancelRetry()
	cancelBusy()
	afterRetrySession := sessionState.session.Elem()
	if afterRetry := browseSessionLifecycleForTest(sessions, created.Session.ID); afterRetry.inSessions || afterRetry.inClosing {
		t.Fatal("cleanup retry 成功后 BrowseSession 仍留在活动或 closing map")
	}
	if !afterRetrySession.FieldByName("closed").Bool() || !afterRetrySession.FieldByName("page").IsNil() || !afterRetrySession.FieldByName("closeErr").IsNil() || !afterRetrySession.FieldByName("closeLeaseReleased").Bool() {
		t.Fatal("cleanup retry 成功后旧 BrowseSession 必须 closed、清空 page 并完成 lease 责任")
	}
	pageCalls, browserCalls = injected.callCounts()
	if pageCalls != 1 || browserCalls != 1 {
		t.Fatalf("session retry 不得重复 Page.close 或 Browser.close: page=%d browser=%d", pageCalls, browserCalls)
	}
	if err := releaseAndCloseWithWatchdog(manager, probe, 10*time.Second, 12*time.Second); err != nil {
		t.Fatalf("归还 probe lease 失败: %v", err)
	}
	nextCtx, cancelNext := context.WithTimeout(context.Background(), 30*time.Second)
	next, err := acquireForWithWatchdog(manager, profileDir, nextCtx, cancelNext, "after-retry-probe", 35*time.Second)
	cancelNext()
	if err != nil {
		t.Fatalf("probe lease 归还后重新 Acquire 失败: %v", err)
	}
	if err := releaseAndCloseWithWatchdog(manager, next, 10*time.Second, 12*time.Second); err != nil {
		t.Fatalf("归还最终 probe lease 失败: %v", err)
	}
	verifyCtx, cancelVerify := context.WithTimeout(context.Background(), 25*time.Second)
	if err := manager.Reset(verifyCtx); err != nil {
		cancelVerify()
		t.Fatalf("最终 cleanup record/reset error retirement 失败: %v", err)
	}
	cancelVerify()
	if pending := browserManagerPendingLifecycle(manager); pending != "" {
		t.Fatalf("退休后必须清空旧 records、pending pages/browsers 与 error buckets: %s", pending)
	}
}

func TestProductionSessionConcurrentCloseReturnsBrowserLeaseOnce(t *testing.T) {
	profileDir := t.TempDir()
	factory := newServiceBrowserFactory(t, nil, nil, nil, nil, profileDir)
	manager := browser.NewManager(factory, browser.WithWarmPageTTL(0), browser.WithIdleTimeout(time.Minute))
	sessions := xiaohongshu.NewBrowseSessionManager(time.Minute)
	service := &XiaohongshuService{browserManager: manager, browseSessions: sessions}
	type acquireResult struct {
		page *hrod.Page
		err  error
	}
	acquireDone := make(chan acquireResult, 1)
	var acquireCancel context.CancelFunc
	acquireCompleted := false
	t.Cleanup(func() {
		if acquireCancel != nil {
			acquireCancel()
		}
		if !acquireCompleted {
			select {
			case lateAcquire := <-acquireDone:
				acquireCompleted = true
				if lateAcquire.page != nil {
					if err := releaseAndCloseWithWatchdog(manager, lateAcquire.page, 5*time.Second, 7*time.Second); err != nil {
						t.Errorf("迟到 Acquire page 单次归还失败: %v", err)
					}
				}
			case <-time.After(10 * time.Second):
				t.Errorf("production Acquire caller 未在 cleanup watchdog 内 join")
			}
		}
		processes, processErr := isolatedBrowserProcessesForProfile(profileDir)
		if processErr != nil {
			t.Errorf("生产 lease fixture 清理前进程扫描失败: %v", processErr)
		}
		if err := runFixtureCloseWithWatchdog(20*time.Second, 2*time.Second, sessions.CloseAllContext); err != nil {
			t.Errorf("生产 lease session 清理失败: %v", err)
		}
		if err := runFixtureCloseWithWatchdog(20*time.Second, 2*time.Second, manager.Close); err != nil {
			t.Errorf("生产 browser 清理失败: %v", err)
		}
		late, lateErr := isolatedBrowserProcessesForProfile(profileDir)
		if lateErr != nil {
			t.Errorf("生产 lease fixture 清理后进程扫描失败: %v", lateErr)
		} else {
			processes = mergeIsolatedProcessIdentities(processes, late)
		}
		if err := killTrackedIsolatedBrowserProcesses(processes); err != nil {
			t.Errorf("生产 lease fixture 已跟踪进程清理失败: %v", err)
		}
		if err := killIsolatedBrowserProcesses(profileDir); err != nil {
			t.Errorf("生产 lease fixture browser 清理失败: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	acquireCancel = cancel
	go func() {
		page, err := manager.AcquireFor(ctx, "browser_operation")
		acquireDone <- acquireResult{page: page, err: err}
	}()
	var acquired acquireResult
	select {
	case acquired = <-acquireDone:
		acquireCompleted = true
	case <-time.After(25 * time.Second):
		t.Fatal("生产 Acquire/Page 阶段超过独立 watchdog")
	}
	page, err := acquired.page, acquired.err
	if err != nil {
		t.Fatalf("获取生产 browser lease 失败: %v", err)
	}
	created, err := service.registerReadyBrowseSession(ctx, page, manager.Release, manager.UpdateOwner)
	if err != nil || created == nil || created.Session == nil {
		t.Fatalf("生产 session 注册失败: result=%+v err=%v", created, err)
	}
	firstCtx, cancelFirst := context.WithTimeout(context.Background(), 15*time.Second)
	firstDone := make(chan error, 1)
	go func() { firstDone <- sessions.CloseContext(firstCtx, created.Session.ID) }()
	select {
	case firstErr := <-firstDone:
		if firstErr != nil {
			cancelFirst()
			t.Fatalf("首次真实页面关闭失败: %v", firstErr)
		}
	case <-time.After(17 * time.Second):
		cancelFirst()
		t.Fatal("首次真实页面关闭超过独立 watchdog")
	}
	cancelFirst()

	retryCtx, cancelRetry := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelRetry()
	retries := make(chan error, 2)
	retryEntered := make(chan struct{}, 2)
	retryCallerDone := make(chan struct{}, 2)
	retryStart := make(chan struct{})
	var retryStartOnce sync.Once
	releaseRetryStart := func() { retryStartOnce.Do(func() { close(retryStart) }) }
	retryCallerJoined := 0
	for range 2 {
		go func() {
			defer func() { retryCallerDone <- struct{}{} }()
			retryEntered <- struct{}{}
			select {
			case <-retryStart:
			case <-retryCtx.Done():
				retries <- retryCtx.Err()
				return
			}
			retries <- sessions.CloseContext(retryCtx, created.Session.ID)
		}()
	}
	t.Cleanup(func() {
		cancelRetry()
		releaseRetryStart()
		timer := time.NewTimer(8 * time.Second)
		defer timer.Stop()
		for retryCallerJoined < 2 {
			select {
			case <-retryCallerDone:
				retryCallerJoined++
			case <-timer.C:
				t.Errorf("retry close callers 未在 cleanup watchdog 内全部 join: %d/2", retryCallerJoined)
				return
			}
		}
	})
	for range 2 {
		select {
		case <-retryEntered:
		case <-time.After(2 * time.Second):
			releaseRetryStart()
			t.Fatal("两个退休后 close caller 未到达同步屏障")
		}
	}
	releaseRetryStart()
	var missingCount int
	for range 2 {
		select {
		case err := <-retries:
			if err != nil && !strings.HasPrefix(err.Error(), "browse session 不存在:") {
				t.Fatalf("并发生产 close retry 失败: %v", err)
			}
			if err != nil {
				missingCount++
			}
		case <-retryCtx.Done():
			t.Fatalf("并发生产 close retry 超时: %v", retryCtx.Err())
		case <-time.After(12 * time.Second):
			t.Fatal("并发生产 close retry 超过独立 watchdog")
		}
	}
	if missingCount != 2 {
		t.Fatalf("已退休 session 的并发调用应观察到不存在: %d", missingCount)
	}
	retryJoinTimer := time.NewTimer(2 * time.Second)
	for retryCallerJoined < 2 {
		select {
		case <-retryCallerDone:
			retryCallerJoined++
		case <-retryJoinTimer.C:
			t.Fatal("并发 production close retry caller 返回后未 join")
		}
	}
	retryJoinTimer.Stop()
	cancelRetry()

	probeCtx, cancelProbe := context.WithTimeout(context.Background(), 10*time.Second)
	probe, err := acquireForWithWatchdog(manager, profileDir, probeCtx, cancelProbe, "post-close-probe", 15*time.Second)
	cancelProbe()
	if err != nil {
		t.Fatalf("关闭重试后 browser lease 应可重新取得: %v", err)
	}
	probeHeld := true
	probeReleaseAttempted := false
	releaseProbe := func() error {
		if !probeHeld || probeReleaseAttempted {
			return nil
		}
		probeReleaseAttempted = true
		return releaseAndCloseWithWatchdog(manager, probe, 5*time.Second, 7*time.Second)
	}
	t.Cleanup(func() {
		if err := releaseProbe(); err != nil {
			t.Errorf("probe lease 单次归还失败: %v", err)
		}
	})
	busyCtx, cancelBusy := context.WithTimeout(context.Background(), 100*time.Millisecond)
	contenderPage, busyErr := manager.AcquireFor(busyCtx, "lease-contender")
	callerErr := busyCtx.Err()
	cancelBusy()
	var busy *browser.BusyError
	if contenderPage != nil {
		if err := releaseAndCloseWithWatchdog(manager, contenderPage, 5*time.Second, 7*time.Second); err != nil {
			t.Fatalf("意外 contender page 单次归还失败: %v", err)
		}
	}
	if !errors.Is(busyErr, context.DeadlineExceeded) || !errors.Is(callerErr, context.DeadlineExceeded) || contenderPage != nil || errors.As(busyErr, &busy) {
		if err := releaseProbe(); err != nil {
			t.Fatalf("probe lease 失败路径单次归还失败: %v", err)
		}
		t.Fatalf("probe lease contender 必须返回 caller DeadlineExceeded、nil page 且非 BusyError: page=%v err=%v caller=%v", contenderPage, busyErr, callerErr)
	}
	if err := releaseProbe(); err != nil {
		t.Fatalf("归还 probe lease 失败: %v", err)
	}
	nextCtx, cancelNext := context.WithTimeout(context.Background(), 10*time.Second)
	next, err := acquireForWithWatchdog(manager, profileDir, nextCtx, cancelNext, "post-release-probe", 15*time.Second)
	cancelNext()
	if err != nil {
		t.Fatalf("probe lease 归还后下一次 Acquire 应成功: %v", err)
	}
	nextHeld := true
	nextReleaseAttempted := false
	t.Cleanup(func() {
		if !nextHeld || nextReleaseAttempted {
			return
		}
		nextReleaseAttempted = true
		if err := releaseAndCloseWithWatchdog(manager, next, 5*time.Second, 7*time.Second); err != nil {
			t.Errorf("next lease 单次归还失败: %v", err)
		}
	})
	if err := releaseAndCloseWithWatchdog(manager, next, 5*time.Second, 7*time.Second); err != nil {
		nextReleaseAttempted = true
		t.Fatalf("归还最终 probe lease 失败: %v", err)
	}
	nextReleaseAttempted = true
}

type isolatedProcessIdentity struct {
	pid       int
	startTime uint64
}

func isolatedProcessInfo(pid int) (int, uint64, bool, error) {
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if errors.Is(err, os.ErrNotExist) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, fmt.Errorf("读取 PID %d stat: %w", pid, err)
	}
	end := strings.LastIndexByte(string(stat), ')')
	if end < 0 {
		return 0, 0, false, fmt.Errorf("解析 PID %d stat 失败", pid)
	}
	fields := strings.Fields(string(stat[end+1:]))
	if len(fields) <= 19 {
		return 0, 0, false, fmt.Errorf("解析 PID %d stat 字段失败", pid)
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, false, fmt.Errorf("解析 PID %d parent pid: %w", pid, err)
	}
	startTime, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, 0, false, fmt.Errorf("解析 PID %d starttime: %w", pid, err)
	}
	return parent, startTime, true, nil
}

func isolatedBrowserProcessesForProfile(profileDir string) ([]isolatedProcessIdentity, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	parents := make(map[int]int, len(entries))
	identities := make(map[int]isolatedProcessIdentity, len(entries))
	roots := make(map[int]struct{})
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		parentBefore, startBefore, exists, err := isolatedProcessInfo(pid)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("读取 PID %d cmdline: %w", pid, err)
		}
		parentAfter, startAfter, exists, err := isolatedProcessInfo(pid)
		if err != nil {
			return nil, err
		}
		if !exists || parentBefore != parentAfter || startBefore != startAfter {
			continue
		}
		identities[pid] = isolatedProcessIdentity{pid: pid, startTime: startAfter}
		parents[pid] = parentAfter
		if strings.Contains(string(cmdline), profileDir) {
			roots[pid] = struct{}{}
		}
	}
	matched := make(map[isolatedProcessIdentity]struct{}, len(roots))
	for pid, identity := range identities {
		current := pid
		for depth := 0; depth <= len(parents); depth++ {
			if _, ok := roots[current]; ok {
				matched[identity] = struct{}{}
				break
			}
			parent, ok := parents[current]
			if !ok || parent == 0 || parent == current {
				break
			}
			current = parent
		}
	}
	processes := make([]isolatedProcessIdentity, 0, len(matched))
	for identity := range matched {
		processes = append(processes, identity)
	}
	return processes, nil
}

func mergeIsolatedProcessIdentities(existing, added []isolatedProcessIdentity) []isolatedProcessIdentity {
	seen := make(map[isolatedProcessIdentity]struct{}, len(existing)+len(added))
	merged := make([]isolatedProcessIdentity, 0, len(existing)+len(added))
	for _, identity := range append(append([]isolatedProcessIdentity(nil), existing...), added...) {
		if _, ok := seen[identity]; !ok {
			seen[identity] = struct{}{}
			merged = append(merged, identity)
		}
	}
	return merged
}

func aliveIsolatedProcessIdentities(processes []isolatedProcessIdentity) ([]isolatedProcessIdentity, error) {
	alive := make([]isolatedProcessIdentity, 0, len(processes))
	for _, identity := range processes {
		_, startTime, exists, err := isolatedProcessInfo(identity.pid)
		if err != nil {
			return nil, err
		}
		if exists && startTime == identity.startTime {
			alive = append(alive, identity)
		}
	}
	return alive, nil
}

func killTrackedIsolatedBrowserProcesses(processes []isolatedProcessIdentity) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		alive, err := aliveIsolatedProcessIdentities(processes)
		if err != nil {
			return err
		}
		for _, identity := range alive {
			process, err := os.FindProcess(identity.pid)
			if err != nil {
				return fmt.Errorf("查找已跟踪隔离 browser PID %d: %w", identity.pid, err)
			}
			if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				_, startTime, exists, statErr := isolatedProcessInfo(identity.pid)
				if statErr != nil {
					return statErr
				}
				if exists && startTime == identity.startTime {
					return fmt.Errorf("Kill 已跟踪隔离 browser PID %d: %w", identity.pid, err)
				}
			}
		}
		alive, err = aliveIsolatedProcessIdentities(processes)
		if err != nil {
			return err
		}
		if len(alive) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("已跟踪隔离 browser 进程未在 watchdog 内退出: %v", alive)
		}
		<-time.After(25 * time.Millisecond)
	}
}

func killIsolatedBrowserProcesses(profileDir string) error {
	deadline := time.Now().Add(5 * time.Second)
	var tracked []isolatedProcessIdentity
	for {
		current, err := isolatedBrowserProcessesForProfile(profileDir)
		if err != nil {
			return err
		}
		tracked = mergeIsolatedProcessIdentities(tracked, current)
		alive, err := aliveIsolatedProcessIdentities(tracked)
		if err != nil {
			return err
		}
		for _, identity := range alive {
			process, err := os.FindProcess(identity.pid)
			if err != nil {
				return fmt.Errorf("查找隔离 browser PID %d: %w", identity.pid, err)
			}
			if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				_, currentStart, exists, statErr := isolatedProcessInfo(identity.pid)
				if statErr != nil {
					return statErr
				}
				if exists && currentStart == identity.startTime {
					return fmt.Errorf("Kill 隔离 browser PID %d: %w", identity.pid, err)
				}
			}
		}
		alive, err = aliveIsolatedProcessIdentities(tracked)
		if err != nil {
			return err
		}
		if len(alive) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("隔离 browser 进程未在 watchdog 内退出: %v", alive)
		}
		<-time.After(25 * time.Millisecond)
	}
}

func browserManagerPendingLifecycle(manager *browser.Manager) string {
	state := reflect.ValueOf(manager).Elem()
	resetPages := state.FieldByName("resetPages")
	pendingBrowsers := state.FieldByName("pendingBrowsers")
	resetOperation := state.FieldByName("resetOp")
	resetErr := state.FieldByName("resetErr")
	resetErrByBrowser := state.FieldByName("resetErrByBrowser")
	browserCleanups := state.FieldByName("browserCleanups")
	resetting := state.FieldByName("resetting")
	startup := state.FieldByName("starting")
	liveBrowser := state.FieldByName("browser")
	warmPage := state.FieldByName("warmPage")
	if !resetPages.IsValid() || !pendingBrowsers.IsValid() || !resetOperation.IsValid() || !resetErr.IsValid() || !resetErrByBrowser.IsValid() || !browserCleanups.IsValid() || !resetting.IsValid() || !startup.IsValid() || !liveBrowser.IsValid() || !warmPage.IsValid() {
		return "manager lifecycle fields unavailable"
	}
	if resetPages.Len() != 0 || pendingBrowsers.Len() != 0 || resetErrByBrowser.Len() != 0 || browserCleanups.Len() != 0 || !resetOperation.IsNil() || !resetErr.IsNil() || resetting.Bool() || !startup.IsNil() || !liveBrowser.IsNil() || !warmPage.IsNil() {
		return fmt.Sprintf("reset_pages=%d pending_browsers=%d reset_browser_errors=%d browser_cleanups=%d reset_operation=%t reset_error=%t resetting=%t startup=%t browser=%t warm_page=%t",
			resetPages.Len(), pendingBrowsers.Len(), resetErrByBrowser.Len(), browserCleanups.Len(), !resetOperation.IsNil(), !resetErr.IsNil(), resetting.Bool(), !startup.IsNil(), !liveBrowser.IsNil(), !warmPage.IsNil())
	}
	return ""
}

func TestFatalCleanupStopsIsolatedChromeProcess(t *testing.T) {
	profileDir := t.TempDir()
	factory := newIsolatedProcessBrowserFactory(t, profileDir)
	manager := browser.NewManager(factory, browser.WithWarmPageTTL(0), browser.WithIdleTimeout(time.Minute))
	sessions := xiaohongshu.NewBrowseSessionManager(time.Minute)
	service := &XiaohongshuService{browserManager: manager, browseSessions: sessions}
	var trackedProcesses []isolatedProcessIdentity
	type acquireResult struct {
		page *hrod.Page
		err  error
	}
	acquireDone := make(chan acquireResult, 1)
	var acquireCancel context.CancelFunc
	acquireCompleted := false
	t.Cleanup(func() {
		if acquireCancel != nil {
			acquireCancel()
		}
		processes, processErr := isolatedBrowserProcessesForProfile(profileDir)
		if processErr != nil {
			t.Errorf("fatal process fixture 清理前进程扫描失败: %v", processErr)
		} else {
			trackedProcesses = mergeIsolatedProcessIdentities(trackedProcesses, processes)
		}
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancelCleanup()
		if err := service.Close(cleanupCtx); err != nil {
			t.Errorf("fatal process fixture 最终清理失败: %v", err)
		}
		processes, processErr = isolatedBrowserProcessesForProfile(profileDir)
		if processErr != nil {
			t.Errorf("fatal process fixture 清理后进程扫描失败: %v", processErr)
		} else {
			trackedProcesses = mergeIsolatedProcessIdentities(trackedProcesses, processes)
		}
		if err := killTrackedIsolatedBrowserProcesses(trackedProcesses); err != nil {
			t.Errorf("fatal process fixture 已跟踪进程清理失败: %v", err)
		}
		if err := killIsolatedBrowserProcesses(profileDir); err != nil {
			t.Errorf("fatal process fixture 独立 Kill 失败: %v", err)
		}
		if !acquireCompleted {
			select {
			case lateAcquire := <-acquireDone:
				if lateAcquire.page != nil {
					pageCtx, cancelPage := context.WithTimeout(context.Background(), 2*time.Second)
					if err := lateAcquire.page.CloseContext(pageCtx); err != nil {
						t.Errorf("fatal fixture 迟到 Acquire page 关闭失败: %v", err)
					}
					cancelPage()
				}
			case <-time.After(5 * time.Second):
				t.Errorf("fatal fixture Acquire/Page 阶段在浏览器清场后仍未结束")
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	acquireCancel = cancel
	go func() {
		page, err := manager.AcquireFor(ctx, "browser_operation")
		acquireDone <- acquireResult{page: page, err: err}
	}()
	var acquired acquireResult
	select {
	case acquired = <-acquireDone:
		acquireCompleted = true
	case <-time.After(35 * time.Second):
		t.Fatal("启动隔离 Chrome 并获取页面超过独立 watchdog")
	}
	page, err := acquired.page, acquired.err
	if err != nil {
		t.Fatalf("启动隔离 Chrome 并获取页面失败: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		processes, err := isolatedBrowserProcessesForProfile(profileDir)
		if err != nil {
			t.Fatalf("读取隔离 Chrome 进程失败: %v", err)
		}
		trackedProcesses = mergeIsolatedProcessIdentities(trackedProcesses, processes)
		if len(trackedProcesses) != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("未能通过唯一 user-data-dir 识别隔离 Chrome 进程")
		}
		select {
		case <-time.After(25 * time.Millisecond):
		case <-ctx.Done():
			t.Fatalf("等待隔离 Chrome 进程超时: %v", ctx.Err())
		}
	}
	created, err := service.registerReadyBrowseSession(ctx, page, manager.Release, manager.UpdateOwner)
	if err != nil || created == nil || created.Session == nil {
		t.Fatalf("注册 fatal cleanup session 失败: result=%+v err=%v", created, err)
	}
	session, err := sessions.Get(created.Session.ID)
	if err != nil {
		t.Fatalf("读取 fatal cleanup 的 production session 失败: %v", err)
	}
	targetID := string(page.Rod.TargetID)
	if targetID == "" {
		t.Fatal("fatal cleanup page 没有 target ID")
	}
	service.handleSessionOperationError(ctx, created.Session.ID, session, xiaohongshu.ErrFatalRendererError)
	deadline = time.Now().Add(20 * time.Second)
	for {
		_, sessionErr := sessions.Get(created.Session.ID)
		processes, pidErr := isolatedBrowserProcessesForProfile(profileDir)
		if pidErr != nil {
			t.Fatalf("fatal cleanup 后读取隔离 Chrome 进程失败: %v", pidErr)
		}
		trackedProcesses = mergeIsolatedProcessIdentities(trackedProcesses, processes)
		if sessionErr != nil && len(processes) > 0 {
			targetCtx, cancelTarget := context.WithTimeout(ctx, 2*time.Second)
			pages, pagesErr := page.Rod.Browser().Context(targetCtx).Pages()
			cancelTarget()
			if pagesErr == nil {
				targetPresent := false
				for _, current := range pages {
					if string(current.TargetID) == targetID {
						targetPresent = true
						break
					}
				}
				if !targetPresent {
					break
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("fatal cleanup 未在 Service.Close 前退休 target=%s: session_err=%v processes=%v", targetID, sessionErr, processes)
		}
		select {
		case <-time.After(25 * time.Millisecond):
		case <-ctx.Done():
			t.Fatalf("等待 fatal cleanup 退休 target=%s 超时: %v", targetID, ctx.Err())
		}
	}
	closeCtx, cancelClose := context.WithTimeout(context.Background(), 20*time.Second)
	closeDone := make(chan error, 1)
	go func() { closeDone <- service.Close(closeCtx) }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("fatal cleanup 后 service.Close 失败: %v", err)
		}
	case <-time.After(22 * time.Second):
		cancelClose()
		t.Fatal("fatal cleanup 与 service.Close 未在独立 watchdog 内完成")
	}
	cancelClose()
	deadline = time.Now().Add(5 * time.Second)
	for {
		alive, err := aliveIsolatedProcessIdentities(trackedProcesses)
		if err != nil {
			t.Fatalf("复核隔离 Chrome PID/starttime 退出状态失败: %v", err)
		}
		if len(alive) == 0 {
			if pending := browserManagerPendingLifecycle(manager); pending != "" {
				t.Fatalf("fatal cleanup 退出进程后 manager 仍有生命周期状态: %s", pending)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("fatal cleanup 后已跟踪的隔离 Chrome 进程仍存在: %v", alive)
		}
		select {
		case <-time.After(25 * time.Millisecond):
		case <-ctx.Done():
			t.Fatalf("等待 fatal cleanup 退出进程超时: %v", ctx.Err())
		}
	}
}

func TestServiceCloseDeadlineWhileCreateIgnoresCancellation(t *testing.T) {
	factoryStarted := make(chan struct{}, 1)
	factoryRelease := make(chan struct{}, 1)
	manager := browser.NewManager(func(context.Context) (*hrod.Browser, error) {
		factoryStarted <- struct{}{}
		select {
		case <-factoryRelease:
			return nil, errors.New("fixture startup released")
		case <-time.After(10 * time.Second):
			return nil, context.DeadlineExceeded
		}
	})
	sessions := xiaohongshu.NewBrowseSessionManager(time.Minute)
	service := &XiaohongshuService{browserManager: manager, browseSessions: sessions}
	createDone := make(chan error, 1)
	createFinished := make(chan struct{})
	completed := false
	t.Cleanup(func() {
		if completed {
			return
		}
		select {
		case factoryRelease <- struct{}{}:
		default:
		}
		select {
		case <-createFinished:
		case <-time.After(5 * time.Second):
			t.Errorf("在途创建 fixture 清理未在 watchdog 内完成")
		}
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelCleanup()
		if err := service.Close(cleanupCtx); err != nil {
			t.Errorf("service Close fixture 清理失败: %v", err)
		}
	})
	go func() {
		defer close(createFinished)
		_, err := service.CreateBrowseSession(context.Background(), true)
		createDone <- err
	}()
	select {
	case <-factoryStarted:
	case <-time.After(2 * time.Second):
		select {
		case factoryRelease <- struct{}{}:
		default:
		}
		t.Fatal("在途创建未在 watchdog 内进入 browser factory")
	}

	closeCtx, cancelClose := context.WithTimeout(context.Background(), 50*time.Millisecond)
	closeDone := make(chan error, 1)
	go func() { closeDone <- service.Close(closeCtx) }()
	select {
	case err := <-closeDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("在途创建不响应取消时 Close 应返回期限错误: %v", err)
		}
	case <-time.After(2 * time.Second):
		select {
		case factoryRelease <- struct{}{}:
		default:
		}
		cancelClose()
		t.Fatal("Close 未在独立 watchdog 内受 deadline 约束")
	}
	cancelClose()
	newCtx, cancelNew := context.WithTimeout(context.Background(), time.Second)
	_, newErr := service.CreateBrowseSession(newCtx, true)
	cancelNew()
	if !errors.Is(newErr, errBrowseSessionServiceClosing) {
		t.Fatalf("Close 开始后必须拒绝新创建: %v", newErr)
	}
	select {
	case factoryRelease <- struct{}{}:
	default:
	}
	select {
	case err := <-createDone:
		if err == nil {
			t.Fatal("被取消的在途创建不应发布 session")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("释放不响应取消的 factory 后创建未在 watchdog 内退出")
	}
	retryCtx, cancelRetry := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelRetry()
	closeDone = make(chan error, 1)
	go func() { closeDone <- service.Close(retryCtx) }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("释放创建后重试 Close 失败: %v", err)
		}
	case <-time.After(12 * time.Second):
		t.Fatal("重试 Close 未在独立 watchdog 内完成")
	}
	completed = true
}

func TestManagerCloseDeadlineWithUncancellableStartup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("isolated Chrome process assertion requires Linux /proc")
	}
	profileDir := t.TempDir()
	baseFactory := newServiceBrowserFactory(t, nil, nil, nil, nil, profileDir)
	factoryStarted := make(chan struct{}, 1)
	factoryRelease := make(chan struct{}, 1)
	factoryBrowser := make(chan *hrod.Browser, 1)
	factoryDone := make(chan struct{})
	var trackedProcesses []isolatedProcessIdentity
	manager := browser.NewManager(func(context.Context) (*hrod.Browser, error) {
		defer close(factoryDone)
		factoryStarted <- struct{}{}
		select {
		case <-factoryRelease:
		case <-time.After(20 * time.Second):
			return nil, context.DeadlineExceeded
		}
		factoryCtx, cancelFactory := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancelFactory()
		lateBrowser, err := baseFactory(factoryCtx)
		if lateBrowser != nil {
			factoryBrowser <- lateBrowser
		}
		return lateBrowser, err
	})
	t.Cleanup(func() {
		factoryStopped := false
		select {
		case factoryRelease <- struct{}{}:
		default:
		}
		select {
		case <-factoryDone:
			factoryStopped = true
		case <-time.After(25 * time.Second):
			t.Errorf("uncancellable startup fixture factory 未在有界时间内退出")
		}
		if err := runFixtureCloseWithWatchdog(20*time.Second, 2*time.Second, manager.Close); err != nil {
			t.Errorf("uncancellable startup fixture 清理失败: %v", err)
		}
		select {
		case lateBrowser := <-factoryBrowser:
			browserCtx, cancelBrowser := context.WithTimeout(context.Background(), 10*time.Second)
			if err := lateBrowser.CloseContext(browserCtx); err != nil {
				t.Errorf("fixture browser fallback close 失败: %v", err)
			}
			cancelBrowser()
		case <-time.After(time.Second):
		}
		if err := killTrackedIsolatedBrowserProcesses(trackedProcesses); err != nil {
			t.Errorf("uncancellable startup fixture 已跟踪进程 Kill 失败: %v", err)
		}
		if err := killIsolatedBrowserProcesses(profileDir); err != nil {
			t.Errorf("uncancellable startup fixture 独立 Kill 失败: %v", err)
		}
		if !factoryStopped {
			select {
			case <-factoryDone:
			case <-time.After(5 * time.Second):
				t.Errorf("独立 Kill 后 startup factory 仍未退出")
			}
		}
	})
	defer func() {
		select {
		case factoryRelease <- struct{}{}:
		default:
		}
	}()
	acquireDone := make(chan error, 1)
	acquireCtx, cancelAcquire := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancelAcquire()
	go func() {
		_, err := manager.Acquire(acquireCtx)
		acquireDone <- err
	}()
	select {
	case <-factoryStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("browser factory did not start under watchdog")
	}

	closeCtx, cancelClose := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelClose()
	closeDone := make(chan error, 1)
	go func() { closeDone <- manager.Close(closeCtx) }()
	select {
	case err := <-closeDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Close must return the caller deadline while factory ignores cancellation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close exceeded its independent watchdog while factory was blocked")
	}
	factoryRelease <- struct{}{}
	select {
	case err := <-acquireDone:
		if err == nil {
			t.Fatal("closed manager must not publish a page from the late startup")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("acquire did not finish after releasing the blocked factory")
	}
	var lateBrowser *hrod.Browser
	select {
	case lateBrowser = <-factoryBrowser:
	case <-time.After(5 * time.Second):
		t.Fatal("factory returned no browser identity for late-start cleanup")
	}
	processes, processErr := isolatedBrowserProcessesForProfile(profileDir)
	if processErr != nil || len(processes) == 0 {
		t.Fatalf("startup Close 前未能保存完整 PID/starttime 集合: processes=%v err=%v", processes, processErr)
	}
	trackedProcesses = mergeIsolatedProcessIdentities(trackedProcesses, processes)
	factoryBrowser <- lateBrowser

	retryCtx, cancelRetry := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelRetry()
	for attempt := 0; attempt < 3; attempt++ {
		err := manager.Close(retryCtx)
		if err == nil {
			healthCtx, cancelHealth := context.WithTimeout(context.Background(), 2*time.Second)
			healthErr := lateBrowser.Health(healthCtx)
			cancelHealth()
			if healthErr == nil {
				t.Fatal("Close retry returned success while late startup browser remained reachable")
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				alive, err := aliveIsolatedProcessIdentities(trackedProcesses)
				if err != nil {
					t.Fatalf("读取 uncancellable startup fixture PID/starttime 失败: %v", err)
				}
				if len(alive) == 0 {
					if pending := browserManagerPendingLifecycle(manager); pending != "" {
						t.Fatalf("startup Close retry 后 manager 仍有 pending/reset operation: %s", pending)
					}
					return
				}
				if time.Now().After(deadline) {
					t.Fatalf("startup Close retry 后已跟踪浏览器进程仍存在: %v", alive)
				}
				select {
				case <-time.After(25 * time.Millisecond):
				case <-retryCtx.Done():
					t.Fatalf("等待 startup browser PID 退出超时: %v", retryCtx.Err())
				}
			}
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Close retry failed unexpectedly: %v", err)
		}
	}
	t.Fatal("Close retries did not complete cleanup of the late startup browser")
}

func TestStartPageErrorResultReturnsOriginalFailureWithoutService(t *testing.T) {
	server := &AppServer{}
	cases := []struct {
		name           string
		err            error
		wantNext       string
		wantLoginGuide bool
	}{
		{"ready timeout stays timeout", fmt.Errorf("等待探索页就绪失败: 页面就绪超时"), "start_page", false},
		{"ordinary error stays ordinary", fmt.Errorf("browser acquisition failed"), "start_page", false},
		{"explicit login risk keeps QR guidance", fmt.Errorf("等待探索页就绪失败: 页面提示登录已过期"), "get_login_qrcode", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := server.startPageErrorResult(tc.err)
			if result == nil || !result.IsError || len(result.Content) == 0 {
				t.Fatalf("应返回错误结果: %+v", result)
			}
			text := result.Content[0].Text
			if !strings.Contains(text, tc.err.Error()) {
				t.Fatalf("未保留原错误 %q: %s", tc.err, text)
			}
			if !strings.Contains(text, `"tool": "`+tc.wantNext+`"`) {
				t.Fatalf("next_step.tool 不符合预期 %q: %s", tc.wantNext, text)
			}
			if strings.Contains(text, "探索页已加载但处于未登录状态") {
				t.Fatalf("未知 timeout 不应被推断为未登录: %s", text)
			}
			if got := strings.Contains(text, "扫码登录"); got != tc.wantLoginGuide {
				t.Fatalf("登录风险指引=%t，期望 %t: %s", got, tc.wantLoginGuide, text)
			}
		})
	}
}

// TestLoginFlowNextStep 登录流程的两步工具也要给出下一步：扫码→check_login_status→start_page.
func TestLoginFlowNextStep(t *testing.T) {
	step := xiaohongshu.NextStep{Tool: "check_login_status", Reason: "二维码已生成"}
	contents := appendNextStep([]MCPContent{{Type: "text", Text: "扫码登录"}}, step)
	if len(contents) != 2 {
		t.Fatalf("应追加一段 next_step 文本: %+v", contents)
	}
	var payload struct {
		NextStep xiaohongshu.NextStep `json:"next_step"`
	}
	if err := json.Unmarshal([]byte(contents[1].Text), &payload); err != nil {
		t.Fatalf("next_step 块解析失败: %v\n%s", err, contents[1].Text)
	}
	if payload.NextStep.Tool != "check_login_status" {
		t.Fatalf("next_step.tool = %q, 期望 check_login_status", payload.NextStep.Tool)
	}
	if got := appendNextStep(contents, xiaohongshu.NextStep{}); len(got) != len(contents) {
		t.Fatal("空 next_step 不应追加内容块")
	}
}

// TestLoginQrcodeSessionReuse 待扫码会话句柄的复用与清理语义：
// 本地 TTL 不直接判定页面有效性；旧 goroutine 不得清新会话。
func TestLoginQrcodeSessionReuse(t *testing.T) {
	service := &XiaohongshuService{}
	now := time.Now()

	if got := service.liveLoginQrcode(); got != nil {
		t.Fatal("无会话时应返回 nil")
	}

	session := &loginQrcodeSession{img: "data:image/png;base64,AAA", expiresAt: now.Add(time.Minute)}
	service.loginQRMu.Lock()
	service.loginQR = session
	service.loginQRMu.Unlock()

	if got := service.liveLoginQrcode(); got != session {
		t.Fatal("有效期内应返回同一会话")
	}

	// liveLoginQrcode 只做并发安全的句柄读取；页面上的二维码/登录态由
	// CurrentQrcodeImage 在真实 page 上判断，不能用 nil page 走复用路径。
	session.expiresAt = now.Add(-time.Minute)
	if got := service.liveLoginQrcode(); got != session {
		t.Fatal("本地 TTL 过期不应伪装成页面状态并直接丢弃会话")
	}

	// 旧 goroutine 清理：会话已被换成新的，就不得清空
	fresh := &loginQrcodeSession{img: "new", expiresAt: now.Add(time.Minute)}
	service.loginQRMu.Lock()
	service.loginQR = fresh
	service.loginQRMu.Unlock()
	if service.takeLoginQrcode(session) {
		t.Fatal("旧会话不得清掉新会话")
	}
	if service.liveLoginQrcode() != fresh {
		t.Fatal("新会话应仍在")
	}
	if !service.takeLoginQrcode(fresh) {
		t.Fatal("同一会话应可清理")
	}
	if service.liveLoginQrcode() != nil {
		t.Fatal("清理后不应残留会话")
	}
}

// TestCommentContinueStep 覆盖「评论没读完 → 下一步继续读同一批」的接线：
// complete=false 且有 cursor 时必须给出同一个工具 + 新 cursor + 本批真正生效的参数；
// 读完 / cursor 为空 / payload 类型不符时都不给（返回 nil，交给状态推导）。
func TestCommentContinueStep(t *testing.T) {
	args := SessionDetailArgs{SessionID: "s-1"}
	config := xiaohongshu.DefaultCommentLoadConfig()
	config.ClickMoreReplies = true
	config.MaxRepliesThreshold = 0 // 0=全部展开子评论，必须原样回填
	config.ScrollSpeed = "fast"
	payload := func(complete bool, cursor, reason string) *FeedDetailResponse {
		return &FeedDetailResponse{FeedID: "f-1", Data: &xiaohongshu.FeedDetailResponse{
			Comments: xiaohongshu.CommentList{Complete: complete, Cursor: cursor, IncompleteReason: reason},
		}}
	}

	got := commentContinueStep(args, 50, config, payload(false, "cc_next", "more_comments_available"))
	if got == nil {
		t.Fatal("complete=false 且有 cursor 时必须给出续页下一步")
	}
	if got.Tool != "get_note_detail" {
		t.Fatalf("next_step.tool = %q, 期望 get_note_detail", got.Tool)
	}
	if got.Args["cursor"] != "cc_next" {
		t.Fatalf("next_step.args[cursor] = %v, 期望 cc_next", got.Args["cursor"])
	}
	if got.Args["session_id"] != "s-1" || got.Args["max_items"] != 50 {
		t.Fatalf("必须回填 session_id/max_items: %v", got.Args)
	}
	if got.Args["click_more_replies"] != true || got.Args["reply_limit"] != 0 || got.Args["scroll_speed"] != "fast" {
		t.Fatalf("必须回填本批生效的展开参数（reply_limit=0 表示全部展开）: %v", got.Args)
	}
	if !strings.Contains(got.Reason, "more_comments_available") {
		t.Fatalf("reason 应带上 incomplete_reason: %q", got.Reason)
	}

	if got := commentContinueStep(args, 50, config, payload(true, "cc_next", "")); got != nil {
		t.Fatalf("complete=true 时不得再续页: %+v", got)
	}
	if got := commentContinueStep(args, 50, config, payload(false, "", "more_comments_available")); got != nil {
		t.Fatalf("cursor 为空时不得续页: %+v", got)
	}
	if got := commentContinueStep(args, 50, config, &FeedDetailResponse{Data: "not-a-detail"}); got != nil {
		t.Fatalf("payload 类型不符时不得猜: %+v", got)
	}
}

func TestGetFeedDetailCommentsBatchSessionProductionCommitBoundary(t *testing.T) {
	tests := []struct {
		name       string
		failRecord bool
		noGrowth   bool
	}{
		{name: "successful batch commits and replays"},
		{name: "state recording failure preserves input cursor", failRecord: true},
		{name: "production no-growth failure preserves input cursor", noGrowth: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profileDir := t.TempDir()
			factory := newServiceBrowserFactory(t, nil, nil, nil, nil, profileDir)
			page := newServiceCommentPage(t, factory, "https://www.xiaohongshu.com/explore/"+serviceCommentFeedID+"?xsec_token=token", profileDir)
			root := t.TempDir()
			state, err := xiaohongshu.NewActionStateStore(root, serviceCommentStateKey)
			if err != nil {
				t.Fatalf("创建状态存储: %v", err)
			}
			if err := state.RecordOpen(serviceCommentFeedID, xiaohongshu.OpenSourceSearch); err != nil {
				t.Fatalf("初始化打开状态: %v", err)
			}
			sessions := xiaohongshu.NewBrowseSessionManager(time.Minute)
			t.Cleanup(sessions.CloseAll)
			session := sessions.Create(page, state, nil)
			if _, err := session.OpenNote(context.Background(), "", "https://www.xiaohongshu.com/explore/"+serviceCommentFeedID+"?xsec_token=token", ""); err != nil {
				t.Fatalf("建立已验证的生产 session: %v", err)
			}
			stateBefore, err := state.Load()
			if err != nil {
				t.Fatalf("初始化状态读取失败: %v", err)
			}
			service := &XiaohongshuService{
				browseSessions:   sessions,
				actionState:      state,
				commentCursorTTL: time.Minute,
				cursorGuardMap:   make(map[string]*cursorGuardEntry),
			}
			config := xiaohongshu.DefaultCommentLoadConfig()
			if tt.noGrowth {
				config.ClickMoreReplies = true
				config.MaxRepliesThreshold = 0
			}
			const maxItems = 2
			scope := serviceCommentScope("session", session.ID(), maxItems, "", config)
			inputCursor := &xiaohongshu.CommentCursor{FeedID: serviceCommentFeedID, ReturnedIDs: []string{"already-returned", "already-returned", "idx_legacy", ""}}
			if tt.noGrowth {
				inputCursor.ReturnedIDs = []string{"parent-1", "reply-1", "reply-2", "reply-3", "parent-1", "idx_legacy", ""}
			}
			wantInputCursor := *inputCursor
			wantInputCursor.ReturnedIDs = append([]string(nil), inputCursor.ReturnedIDs...)
			service.setCommentCursor("input", inputCursor, scope)
			if tt.failRecord {
				blockServiceCommentStateWrite(t, root)
			}

			result, callErr := service.GetFeedDetailCommentsBatch(context.Background(), serviceCommentFeedID, "", "input", maxItems, config)
			if tt.failRecord {
				if callErr == nil || !strings.Contains(callErr.Error(), "记录评论停留状态失败") || result != nil {
					t.Fatalf("状态记录失败应中止 service 成功提交: result=%+v err=%v", result, callErr)
				}
				entry, ok := service.commentCursors.Load("input")
				if !ok || !reflect.DeepEqual(entry.(*commentCursorEntry).Cursor, &wantInputCursor) {
					t.Fatalf("失败后输入 cursor 不应被消费或改写: got=%+v want=%+v", entry, &wantInputCursor)
				}
				if _, ok := service.commentReplays.Load("input"); ok {
					t.Fatal("失败后不应写入 replay")
				}
				stateAfter, err := state.Load()
				if err != nil || !reflect.DeepEqual(stateBefore, stateAfter) {
					t.Fatalf("落盘失败后 ActionState 不应部分更新: before=%+v after=%+v err=%v", stateBefore, stateAfter, err)
				}
				return
			}
			if tt.noGrowth {
				if callErr == nil || !strings.Contains(callErr.Error(), "评论总数未收敛") || result != nil {
					t.Fatalf("真实 session 评论路径无增长必须 fail-closed: result=%+v err=%v", result, callErr)
				}
				entry, ok := service.commentCursors.Load("input")
				if !ok || !reflect.DeepEqual(entry.(*commentCursorEntry).Cursor, &wantInputCursor) {
					t.Fatalf("真实路径拒绝后必须完整保留输入 cursor: got=%+v want=%+v", entry, &wantInputCursor)
				}
				cursorCount := 0
				service.commentCursors.Range(func(key, value any) bool {
					cursorCount++
					return true
				})
				if cursorCount != 1 {
					t.Fatalf("真实路径无增长拒绝不得新增 cursor: count=%d", cursorCount)
				}
				replayCount := 0
				service.commentReplays.Range(func(key, value any) bool {
					replayCount++
					return true
				})
				if replayCount != 0 {
					t.Fatalf("真实路径无增长拒绝不得新增 replay: count=%d", replayCount)
				}
				return
			}
			if callErr != nil {
				t.Fatalf("session 评论批次生产调用链失败: %v", callErr)
			}
			if result == nil || result.FeedID != serviceCommentFeedID {
				t.Fatalf("成功批次响应错误: %+v", result)
			}
			detail, ok := result.Data.(*xiaohongshu.FeedDetailResponse)
			if !ok {
				t.Fatalf("service payload 类型错误: %T", result.Data)
			}
			assertServiceCommentBatch(t, detail, maxItems)
			if _, ok := service.commentCursors.Load("input"); ok {
				t.Fatal("成功提交后输入 cursor 应被消费")
			}
			if _, ok := service.commentReplays.Load("input"); !ok {
				t.Fatal("成功提交后应缓存 replay")
			}
			replay, replayErr := service.GetFeedDetailCommentsBatch(context.Background(), serviceCommentFeedID, "", "input", maxItems, config)
			if replayErr != nil || !reflect.DeepEqual(result, replay) {
				t.Fatalf("成功批次 replay 应稳定返回原响应: equal=%v err=%v", reflect.DeepEqual(result, replay), replayErr)
			}
		})
	}
}

func TestGetFeedDetailCommentsBatchStandaloneProductionCommitBoundary(t *testing.T) {
	for _, failRecord := range []bool{false, true} {
		name := "successful batch commits and replays"
		if failRecord {
			name = "state recording failure preserves input cursor"
		}
		t.Run(name, func(t *testing.T) {
			profileDir := t.TempDir()
			factory := newServiceBrowserFactory(t, nil, nil, nil, nil, profileDir)
			manager := browser.NewManager(factory, browser.WithWarmPageTTL(time.Minute), browser.WithIdleTimeout(time.Minute))
			var activeCancel context.CancelFunc
			var activeDone <-chan struct{}
			t.Cleanup(func() {
				var processes []isolatedProcessIdentity
				if runtime.GOOS == "linux" {
					var err error
					processes, err = isolatedBrowserProcessesForProfile(profileDir)
					if err != nil {
						t.Errorf("standalone fixture cleanup 前扩展 browser 进程集合失败: %v", err)
					}
				}
				if activeCancel != nil {
					activeCancel()
				}
				if err := runFixtureCloseWithWatchdog(10*time.Second, 2*time.Second, manager.Close); err != nil {
					t.Errorf("standalone fixture manager cleanup failed: %v", err)
				}
				if runtime.GOOS == "linux" {
					late, err := isolatedBrowserProcessesForProfile(profileDir)
					if err != nil {
						t.Errorf("standalone fixture cleanup 后扩展 browser 进程集合失败: %v", err)
					} else {
						processes = mergeIsolatedProcessIdentities(processes, late)
					}
					if err := killTrackedIsolatedBrowserProcesses(processes); err != nil {
						t.Errorf("standalone fixture 已跟踪 browser 清理失败: %v", err)
					}
					if err := killIsolatedBrowserProcesses(profileDir); err != nil {
						t.Errorf("standalone fixture browser 清理失败: %v", err)
					}
				}
				if activeDone != nil {
					select {
					case <-activeDone:
					case <-time.After(5 * time.Second):
						t.Errorf("standalone fixture operation remained blocked after cleanup")
					}
				}
			})
			runOperation := func(name string, operation func(context.Context) error) error {
				opCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				result := make(chan error, 1)
				finished := make(chan struct{})
				activeCancel = cancel
				activeDone = finished
				go func() {
					defer close(finished)
					result <- operation(opCtx)
				}()
				select {
				case err := <-result:
					cancel()
					activeCancel = nil
					return err
				case <-time.After(20 * time.Second):
					cancel()
					return fmt.Errorf("%s operation exceeded watchdog", name)
				}
			}
			var page *hrod.Page
			if err := runOperation("AcquireFor", func(ctx context.Context) error {
				var err error
				page, err = manager.AcquireFor(ctx, "fixture")
				return err
			}); err != nil {
				t.Fatalf("获取 fixture page: %v", err)
			}
			if err := runOperation("Navigate", func(ctx context.Context) error {
				return page.Rod.Context(ctx).Navigate("https://www.xiaohongshu.com/search_result?keyword=fixture")
			}); err != nil {
				t.Fatalf("导航搜索结果 fixture: %v", err)
			}
			if err := runOperation("WaitLoad", func(ctx context.Context) error {
				return page.Rod.Context(ctx).WaitLoad()
			}); err != nil {
				t.Fatalf("等待搜索结果 fixture: %v", err)
			}
			manager.Release(page)
			root := t.TempDir()
			state, err := xiaohongshu.NewActionStateStore(root, serviceCommentStateKey)
			if err != nil {
				t.Fatalf("创建状态存储: %v", err)
			}
			service := &XiaohongshuService{
				browserManager:   manager,
				actionState:      state,
				commentCursorTTL: time.Minute,
				cursorGuardMap:   make(map[string]*cursorGuardEntry),
			}
			stateBefore, err := state.Load()
			if err != nil {
				t.Fatalf("初始化状态读取失败: %v", err)
			}
			config := xiaohongshu.DefaultCommentLoadConfig()
			const maxItems = 2
			scope := serviceCommentScope("standalone", "", maxItems, "token", config)
			inputCursor := &xiaohongshu.CommentCursor{FeedID: serviceCommentFeedID, ReturnedIDs: []string{"already-returned", "already-returned", "idx_legacy", ""}}
			service.setCommentCursor("input", inputCursor, scope)
			if failRecord {
				blockServiceCommentStateWrite(t, root)
			}

			var result *FeedDetailResponse
			var callErr error
			if err := runOperation("GetFeedDetailCommentsBatch", func(ctx context.Context) error {
				result, callErr = service.GetFeedDetailCommentsBatch(ctx, serviceCommentFeedID, "token", "input", maxItems, config)
				return nil
			}); err != nil {
				t.Fatalf("standalone 评论批次生产调用超过 watchdog: %v", err)
			}
			if failRecord {
				if callErr == nil || !strings.Contains(callErr.Error(), "记录评论停留状态失败") || result != nil {
					t.Fatalf("状态记录失败应中止 standalone 成功提交: result=%+v err=%v", result, callErr)
				}
				entry, ok := service.commentCursors.Load("input")
				if !ok || entry.(*commentCursorEntry).Cursor.ReturnedIDs[0] != "already-returned" {
					t.Fatalf("失败后输入 cursor 不应被消费或改写: %v", entry)
				}
				if _, ok := service.commentReplays.Load("input"); ok {
					t.Fatal("失败后不应写入 replay")
				}
				stateAfter, err := state.Load()
				if err != nil || !reflect.DeepEqual(stateBefore, stateAfter) {
					t.Fatalf("落盘失败后 ActionState 不应部分更新: before=%+v after=%+v err=%v", stateBefore, stateAfter, err)
				}
				return
			}
			if callErr != nil {
				t.Fatalf("standalone 评论批次生产调用链失败: %v", callErr)
			}
			if result == nil || result.FeedID != serviceCommentFeedID {
				t.Fatalf("成功批次响应错误: %+v", result)
			}
			detail, ok := result.Data.(*xiaohongshu.FeedDetailResponse)
			if !ok {
				t.Fatalf("service payload 类型错误: %T", result.Data)
			}
			assertServiceCommentBatch(t, detail, maxItems)
			if _, ok := service.commentCursors.Load("input"); ok {
				t.Fatal("成功提交后输入 cursor 应被消费")
			}
			if _, ok := service.commentReplays.Load("input"); !ok {
				t.Fatal("成功提交后应缓存 replay")
			}
			var replay *FeedDetailResponse
			var replayErr error
			if err := runOperation("GetFeedDetailCommentsBatch replay", func(ctx context.Context) error {
				replay, replayErr = service.GetFeedDetailCommentsBatch(ctx, serviceCommentFeedID, "token", "input", maxItems, config)
				return nil
			}); err != nil {
				t.Fatalf("standalone 评论批次 replay 超过 watchdog: %v", err)
			}
			if replayErr != nil || !reflect.DeepEqual(result, replay) {
				t.Fatalf("成功批次 replay 应稳定返回原响应: equal=%v err=%v", reflect.DeepEqual(result, replay), replayErr)
			}
		})
	}
}

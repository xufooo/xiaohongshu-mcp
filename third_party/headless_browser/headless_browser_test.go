package headless_browser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
)

type pageLifetimeCDPClient struct {
	events       chan *cdp.Event
	setupStarted chan struct{}
	setupMethod  string
}

func (c *pageLifetimeCDPClient) Event() <-chan *cdp.Event { return c.events }

func (c *pageLifetimeCDPClient) Call(ctx context.Context, _ string, method string, _ interface{}) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.setupStarted != nil && method == c.setupMethod {
		close(c.setupStarted)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	switch method {
	case "Target.createTarget":
		return []byte(`{"targetId":"lifetime-target"}`), nil
	case "Target.attachToTarget":
		return []byte(`{"sessionId":"lifetime-session"}`), nil
	case "Target.getTargetInfo":
		return []byte(`{"targetInfo":{"targetId":"lifetime-target","type":"page","url":"about:blank"}}`), nil
	}
	return []byte(`{}`), nil
}

func TestPageContextPreservesBrowserAndEventsAfterCreation(t *testing.T) {
	browserCtx, cancelBrowser := context.WithCancel(context.Background())
	defer cancelBrowser()
	client := &pageLifetimeCDPClient{events: make(chan *cdp.Event)}
	defer close(client.events)
	controller := rod.New().Context(browserCtx).NoDefaultDevice().ControlURL("").Client(client)
	if err := controller.Connect(); err != nil {
		t.Fatal(err)
	}
	b := &Browser{browser: controller, browserCtx: browserCtx}
	createCtx, cancelCreate := context.WithCancel(context.Background())
	defer cancelCreate()
	page, err := b.PageContext(createCtx)
	if err != nil {
		t.Fatal(err)
	}
	cancelCreate()
	if err := page.GetContext().Err(); err != nil {
		t.Fatalf("creation cancellation reached the returned page: %v", err)
	}
	info, err := page.Info()
	if err != nil || info == nil || info.TargetID != page.TargetID {
		t.Fatalf("page's Browser retained the creation budget: info=%+v err=%v", info, err)
	}
	events := page.Event()
	select {
	case client.events <- &cdp.Event{SessionID: string(page.SessionID), Method: "Runtime.executionContextsCleared", Params: []byte(`{}`)}:
	case <-time.After(time.Second):
		t.Fatal("browser event pump did not accept the page event")
	}
	select {
	case event, ok := <-events:
		if !ok || event.Method != "Runtime.executionContextsCleared" {
			t.Fatalf("page event pump did not survive creation cancellation: event=%+v open=%t", event, ok)
		}
	case <-time.After(time.Second):
		t.Fatal("page event pump stopped with the creation budget")
	}
	select {
	case client.events <- &cdp.Event{Method: "Target.detachedFromTarget", Params: []byte(`{"sessionId":"lifetime-session"}`)}:
	case <-time.After(time.Second):
		t.Fatal("browser event pump did not accept session retirement")
	}
	select {
	case <-page.Browser().GetContext().Done():
	case <-time.After(time.Second):
		t.Fatal("retired page retained its Browser clone's lifetime")
	}
	if page.GetContext().Err() == nil || controller.GetContext().Err() != nil {
		t.Fatal("session retirement must cancel the page without canceling the root Browser")
	}
}

func TestPageContextCancellationStillBoundsSetup(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "cancel"
		if deadline {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			testPageContextSetupCancellation(t, deadline)
		})
	}
}

func testPageContextSetupCancellation(t *testing.T, deadline bool) {
	t.Helper()
	browserCtx, cancelBrowser := context.WithCancel(context.Background())
	defer cancelBrowser()
	client := &pageLifetimeCDPClient{
		events: make(chan *cdp.Event), setupStarted: make(chan struct{}), setupMethod: "Page.enable",
	}
	if deadline {
		// Fail inside Rod rather than at the wrapper's final ctx.Err check.
		client.setupMethod = "Target.attachToTarget"
	}
	defer close(client.events)
	controller := rod.New().Context(browserCtx).NoDefaultDevice().ControlURL("").Client(client)
	if err := controller.Connect(); err != nil {
		t.Fatal(err)
	}
	b := &Browser{browser: controller, browserCtx: browserCtx}
	createCtx, cancelCreate := context.WithCancel(context.Background())
	wantErr := context.Canceled
	if deadline {
		cancelCreate()
		createCtx, cancelCreate = context.WithTimeout(context.Background(), 3*time.Second)
		wantErr = context.DeadlineExceeded
	}
	defer cancelCreate()
	done := make(chan error, 1)
	joined := false
	defer func() {
		cancelCreate()
		if !joined {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("page setup did not join after fixture cancellation")
			}
		}
	}()
	go func() {
		page, err := b.PageContext(createCtx)
		if page != nil {
			err = fmt.Errorf("canceled setup returned a page: %v", err)
		}
		done <- err
	}()
	select {
	case <-client.setupStarted:
	case <-time.After(time.Second):
		t.Fatal("page setup did not reach its CDP barrier")
	}
	if !deadline {
		cancelCreate()
	}
	select {
	case err := <-done:
		joined = true
		if !errors.Is(err, wantErr) {
			t.Fatalf("setup must respect creation cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("page setup did not stop after creation cancellation")
	}
	if controller.GetContext().Err() != nil {
		t.Fatal("failed page setup canceled the root Browser")
	}
}

type blockedBrowserCloseClient struct {
	events  chan *cdp.Event
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *blockedBrowserCloseClient) Event() <-chan *cdp.Event { return c.events }

func (c *blockedBrowserCloseClient) Call(ctx context.Context, _ string, method string, _ interface{}) ([]byte, error) {
	if method != "Browser.close" {
		return []byte(`{}`), nil
	}
	select {
	case c.started <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.release:
		return nil, errors.New("fixture Browser.close released")
	}
}

func (c *blockedBrowserCloseClient) Close() {
	c.once.Do(func() {
		close(c.events)
		close(c.release)
	})
}

func TestParseLaunchArg(t *testing.T) {
	tests := []struct {
		input    string
		name     string
		value    string
		hasValue bool
		ok       bool
	}{
		{"--lang=zh-CN", "lang", "zh-CN", true, true},
		{"--disable-gpu", "disable-gpu", "", false, true},
		{"lang=zh-CN", "", "", false, false},
		{"--bad flag", "", "", false, false},
	}

	for _, test := range tests {
		name, value, hasValue, ok := parseLaunchArg(test.input)
		if name != test.name || value != test.value || hasValue != test.hasValue || ok != test.ok {
			t.Fatalf("parseLaunchArg(%q) = (%q, %q, %v, %v)", test.input, name, value, hasValue, ok)
		}
	}
}

func TestApplyCloakLauncherProfile(t *testing.T) {
	l := launcher.New()
	if !l.Has("enable-automation") {
		t.Fatal("launcher default should include enable-automation")
	}

	// disable-features 不被 Cloak profile 修改，保持 rod 默认值不变。
	before, ok := l.GetFlags("disable-features")
	if !ok || len(before) == 0 {
		t.Fatal("launcher default should include disable-features")
	}
	before = append([]string(nil), before...)

	applyCloakLauncherProfile(l)

	if l.Has("enable-automation") {
		t.Fatal("cloak launcher profile should remove enable-automation")
	}
	after, ok := l.GetFlags("disable-features")
	if !ok || !reflect.DeepEqual(after, before) {
		t.Fatalf("disable-features = %v, want unchanged %v", after, before)
	}
}

// TestWithStealthAlias 验证 WithStealth 与 WithStealthJS 控制同一字段。
func TestWithStealthAlias(t *testing.T) {
	cfg := newDefaultConfig()
	WithStealthJS(false)(cfg)
	if cfg.StealthJS {
		t.Fatal("WithStealthJS(false) 应设置 StealthJS=false")
	}

	cfg2 := newDefaultConfig()
	WithStealth(false)(cfg2)
	if cfg2.StealthJS {
		t.Fatal("WithStealth(false) 应控制同一字段 StealthJS=false")
	}
}

// TestWithExtraFlagsDefensiveCopy 验证 WithExtraFlags 防御性复制 map。
func TestWithExtraFlagsDefensiveCopy(t *testing.T) {
	flagsMap := map[string]string{"fingerprint-brand": "Chrome"}
	cfg := newDefaultConfig()
	WithExtraFlags(flagsMap)(cfg)
	flagsMap["fingerprint-brand"] = "Mutated"
	if cfg.ExtraFlags["fingerprint-brand"] != "Chrome" {
		t.Fatal("WithExtraFlags 必须复制 map，调用方后续修改不能影响配置")
	}
}

// TestWithExtraArgsDefensiveCopy 验证 WithExtraArgs 防御性复制 slice。
func TestWithExtraArgsDefensiveCopy(t *testing.T) {
	args := []string{"--lang=zh-CN"}
	cfg := newDefaultConfig()
	WithExtraArgs(args)(cfg)
	args[0] = "--mutated"
	if cfg.ExtraArgs[0] != "--lang=zh-CN" {
		t.Fatal("WithExtraArgs 必须复制 slice，调用方后续修改不能影响配置")
	}
}

func TestPrimaryLang(t *testing.T) {
	if primaryLang("zh-CN") != "zh" {
		t.Fatalf("primaryLang(zh-CN) = %q, want zh", primaryLang("zh-CN"))
	}
	if primaryLang("en-US") != "en" {
		t.Fatalf("primaryLang(en-US) = %q, want en", primaryLang("en-US"))
	}
	if primaryLang("en") != "en" {
		t.Fatalf("primaryLang(en) = %q, want en", primaryLang("en"))
	}
}

// TestAutoFingerprintPlatform 验证按运行 OS 返回合法平台值。
func TestAutoFingerprintPlatform(t *testing.T) {
	platform := autoFingerprintPlatform()
	if platform != "windows" && platform != "macos" {
		t.Fatalf("autoFingerprintPlatform() = %q, 应为 windows 或 macos", platform)
	}
}
func TestWithFingerprintSeedConfig(t *testing.T) {
	c := newDefaultConfig()
	WithFingerprintSeed(20260804)(c)
	if c.FingerprintSeed != 20260804 {
		t.Fatalf("FingerprintSeed 未写入 Config: %d", c.FingerprintSeed)
	}
	if c.FingerprintSeed == 0 {
		t.Fatalf("显式 seed 不应为 0（0 表示随机）")
	}
}

// TestApplyLowMemoryLauncherProfile 低开销档必须逐项落下 flag，并带上堆上限与进程上限。
func TestApplyLowMemoryLauncherProfile(t *testing.T) {
	l := applyLowMemoryLauncherProfile(launcher.New(), 192, 2)

	for _, name := range []string{
		"disable-extensions",
		"disable-component-update",
		"no-default-browser-check",
		"aggressive-cache-discard",
		"mute-audio",
	} {
		if !l.Has(flags.Flag(name)) {
			t.Fatalf("低开销档缺少 flag %s", name)
		}
	}

	// WebGL 指纹相关 flag 不得出现在默认档：--disable-software-rasterizer 会让 WebGL 直接消失。
	for _, forbidden := range []string{"disable-gpu", "disable-software-rasterizer"} {
		if l.Has(flags.Flag(forbidden)) {
			t.Fatalf("低开销档不应默认添加 %s（见 docs/pi3b-optimization.md 实测）", forbidden)
		}
	}

	jsFlags, ok := l.GetFlags("js-flags")
	if !ok || len(jsFlags) != 1 || jsFlags[0] != "--max-old-space-size=192" {
		t.Fatalf("js-flags = %v, want [--max-old-space-size=192]", jsFlags)
	}
	limit, ok := l.GetFlags("renderer-process-limit")
	if !ok || len(limit) != 1 || limit[0] != "2" {
		t.Fatalf("renderer-process-limit = %v, want [2]", limit)
	}
}

// TestApplyLowMemoryLauncherProfileDefaults 未给出参数时用默认值（256MB / 2 个 renderer）。
func TestApplyLowMemoryLauncherProfileDefaults(t *testing.T) {
	l := applyLowMemoryLauncherProfile(launcher.New(), 0, 0)

	jsFlags, _ := l.GetFlags("js-flags")
	if len(jsFlags) != 1 || jsFlags[0] != "--max-old-space-size=256" {
		t.Fatalf("默认 js-flags = %v, want [--max-old-space-size=256]", jsFlags)
	}
	limit, _ := l.GetFlags("renderer-process-limit")
	if len(limit) != 1 || limit[0] != "2" {
		t.Fatalf("默认 renderer-process-limit = %v, want [2]", limit)
	}
}

// TestWithLowMemoryProfileConfig 选项必须写入 Config。
func TestWithLowMemoryProfileConfig(t *testing.T) {
	cfg := newDefaultConfig()
	WithLowMemoryProfile(128, 1)(cfg)
	if !cfg.LowMemory || cfg.JSHeapMB != 128 || cfg.RendererLimit != 1 {
		t.Fatalf("WithLowMemoryProfile 未写入 Config: %+v", cfg)
	}
}

// TestWithBlockedURLsDefensiveCopy 拦截列表必须防御性复制，空列表等同不拦截。
func TestWithBlockedURLsDefensiveCopy(t *testing.T) {
	patterns := []string{"*.mp4*"}
	cfg := newDefaultConfig()
	WithBlockedURLs(patterns)(cfg)
	patterns[0] = "*.mutated*"
	if len(cfg.BlockedURLs) != 1 || cfg.BlockedURLs[0] != "*.mp4*" {
		t.Fatalf("WithBlockedURLs 必须复制切片，got %v", cfg.BlockedURLs)
	}

	cfg2 := newDefaultConfig()
	WithBlockedURLs(nil)(cfg2)
	if cfg2.BlockedURLs != nil {
		t.Fatalf("空列表应表示不拦截，got %v", cfg2.BlockedURLs)
	}
}

// 空 cookie 列表必须"什么都不做"。
// 旧实现走 browser.SetCookies(nil)，而 rod 把 nil 实现成 Storage.clearCookies，
// 于是空 cookies.json（只有 seed、还没存过 cookie）会在每次启动清空持久 profile 的登录态。
// 这里故意传 nil browser：只要函数还去碰 browser 就会 panic，被 recover 成 error 暴露出来。
func TestSetBrowserCookiesEmptyListIsNoOp(t *testing.T) {
	if err := setBrowserCookies(nil, nil); err != nil {
		t.Fatalf("空 cookie 列表不应产生任何浏览器调用: %v", err)
	}
}

type launcherProcessIdentity struct {
	pid       int
	startTime uint64
}

func launcherProcessInfo(pid int) (int, uint64, bool, error) {
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if errors.Is(err, os.ErrNotExist) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, fmt.Errorf("read PID %d stat: %w", pid, err)
	}
	end := strings.LastIndexByte(string(stat), ')')
	if end < 0 {
		return 0, 0, false, fmt.Errorf("parse PID %d stat", pid)
	}
	fields := strings.Fields(string(stat[end+1:]))
	if len(fields) <= 19 {
		return 0, 0, false, fmt.Errorf("parse PID %d stat fields", pid)
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, false, fmt.Errorf("parse PID %d parent: %w", pid, err)
	}
	startTime, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, 0, false, fmt.Errorf("parse PID %d starttime: %w", pid, err)
	}
	return parent, startTime, true, nil
}

func launcherProcessesForProfile(profileDir string) ([]launcherProcessIdentity, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	parents := make(map[int]int, len(entries))
	identities := make(map[int]launcherProcessIdentity, len(entries))
	roots := make(map[int]struct{})
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		parentBefore, startBefore, exists, err := launcherProcessInfo(pid)
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
			return nil, fmt.Errorf("read PID %d cmdline: %w", pid, err)
		}
		parentAfter, startAfter, exists, err := launcherProcessInfo(pid)
		if err != nil {
			return nil, err
		}
		if !exists || parentBefore != parentAfter || startBefore != startAfter {
			continue
		}
		parents[pid] = parentAfter
		identities[pid] = launcherProcessIdentity{pid: pid, startTime: startAfter}
		if strings.Contains(string(cmdline), profileDir) {
			roots[pid] = struct{}{}
		}
	}
	matched := make(map[launcherProcessIdentity]struct{}, len(roots))
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
	processes := make([]launcherProcessIdentity, 0, len(matched))
	for identity := range matched {
		processes = append(processes, identity)
	}
	return processes, nil
}

func mergeLauncherProcessIdentities(existing, added []launcherProcessIdentity) []launcherProcessIdentity {
	seen := make(map[launcherProcessIdentity]struct{}, len(existing)+len(added))
	merged := make([]launcherProcessIdentity, 0, len(existing)+len(added))
	for _, identity := range append(append([]launcherProcessIdentity(nil), existing...), added...) {
		if _, ok := seen[identity]; !ok {
			seen[identity] = struct{}{}
			merged = append(merged, identity)
		}
	}
	return merged
}

func aliveLauncherProcessIdentities(processes []launcherProcessIdentity) ([]launcherProcessIdentity, error) {
	alive := make([]launcherProcessIdentity, 0, len(processes))
	for _, identity := range processes {
		_, startTime, exists, err := launcherProcessInfo(identity.pid)
		if err != nil {
			return nil, err
		}
		if exists && startTime == identity.startTime {
			alive = append(alive, identity)
		}
	}
	return alive, nil
}

func killTrackedLauncherProcesses(processes []launcherProcessIdentity) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		alive, err := aliveLauncherProcessIdentities(processes)
		if err != nil {
			return err
		}
		for _, identity := range alive {
			process, err := os.FindProcess(identity.pid)
			if err != nil {
				return fmt.Errorf("find tracked browser PID %d: %w", identity.pid, err)
			}
			if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				_, startTime, exists, statErr := launcherProcessInfo(identity.pid)
				if statErr != nil {
					return statErr
				}
				if exists && startTime == identity.startTime {
					return fmt.Errorf("kill tracked browser PID %d: %w", identity.pid, err)
				}
			}
		}
		alive, err = aliveLauncherProcessIdentities(processes)
		if err != nil {
			return err
		}
		if len(alive) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("tracked browser processes did not exit before watchdog: %v", alive)
		}
		<-time.After(25 * time.Millisecond)
	}
}

func killBrowserProcessesForProfile(profileDir string) error {
	deadline := time.Now().Add(5 * time.Second)
	var tracked []launcherProcessIdentity
	for {
		current, err := launcherProcessesForProfile(profileDir)
		if err != nil {
			return err
		}
		tracked = mergeLauncherProcessIdentities(tracked, current)
		alive, err := aliveLauncherProcessIdentities(tracked)
		if err != nil {
			return err
		}
		for _, identity := range alive {
			process, err := os.FindProcess(identity.pid)
			if err != nil {
				return fmt.Errorf("find isolated browser PID %d: %w", identity.pid, err)
			}
			if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				_, startTime, exists, statErr := launcherProcessInfo(identity.pid)
				if statErr != nil {
					return statErr
				}
				if exists && startTime == identity.startTime {
					return fmt.Errorf("kill isolated browser PID %d: %w", identity.pid, err)
				}
			}
		}
		alive, err = aliveLauncherProcessIdentities(tracked)
		if err != nil {
			return err
		}
		if len(alive) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("isolated browser processes did not exit before watchdog: %v", alive)
		}
		<-time.After(25 * time.Millisecond)
	}
}

func TestCloseContextKillsAndRetriesRealLauncherCleanup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("launcher process assertion requires Linux /proc")
	}
	bin := os.Getenv("ROD_BROWSER_BIN")
	if bin == "" {
		var ok bool
		bin, ok = launcher.LookPath()
		if !ok {
			t.Skip("未找到 Chrome/Chromium，跳过隔离 launcher cleanup fixture")
		}
	}
	profileDir := t.TempDir()
	startupCtx, cancelStartup := context.WithTimeout(context.Background(), 20*time.Second)
	type startupResult struct {
		browser *Browser
		err     error
	}
	startupDone := make(chan startupResult, 1)
	startupFinished := make(chan struct{})
	var b *Browser
	var trackedProcesses []launcherProcessIdentity
	t.Cleanup(func() {
		if processes, err := launcherProcessesForProfile(profileDir); err != nil {
			t.Errorf("launcher fixture cleanup 前进程快照失败: %v", err)
		} else {
			trackedProcesses = mergeLauncherProcessIdentities(trackedProcesses, processes)
		}
		cancelStartup()
		if b != nil {
			cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 10*time.Second)
			if err := b.CloseContext(cleanupCtx); err != nil {
				t.Errorf("launcher fixture cleanup failed: %v", err)
			}
			cancelCleanup()
		}
		if processes, err := launcherProcessesForProfile(profileDir); err != nil {
			t.Errorf("launcher fixture cleanup 后 process tree snapshot failed: %v", err)
		} else {
			trackedProcesses = mergeLauncherProcessIdentities(trackedProcesses, processes)
		}
		if err := killTrackedLauncherProcesses(trackedProcesses); err != nil {
			t.Errorf("launcher fixture tracked Kill failed: %v", err)
		}
		if err := killBrowserProcessesForProfile(profileDir); err != nil {
			t.Errorf("launcher fixture independent Kill failed: %v", err)
		}
		select {
		case <-startupFinished:
		case <-time.After(5 * time.Second):
			t.Errorf("launcher fixture New remained blocked after cancellation")
		}
		select {
		case late := <-startupDone:
			if b == nil {
				b = late.browser
			}
		default:
		}
		if b != nil {
			cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 10*time.Second)
			if err := b.CloseContext(cleanupCtx); err != nil {
				t.Errorf("launcher fixture late browser cleanup failed: %v", err)
			}
			cancelCleanup()
		}
		processes, err := launcherProcessesForProfile(profileDir)
		if err != nil {
			t.Errorf("launcher fixture final process snapshot failed: %v", err)
		} else {
			trackedProcesses = mergeLauncherProcessIdentities(trackedProcesses, processes)
		}
		if err := killTrackedLauncherProcesses(trackedProcesses); err != nil {
			t.Errorf("launcher fixture final tracked Kill failed: %v", err)
		}
		if err := killBrowserProcessesForProfile(profileDir); err != nil {
			t.Errorf("launcher fixture final independent Kill failed: %v", err)
		}
	})
	go func() {
		instance, err := New(startupCtx,
			WithChromeBinPath(bin),
			WithHeadless(true),
			WithUserDataDir(profileDir),
			WithStealthJS(false),
		)
		startupDone <- startupResult{browser: instance, err: err}
		close(startupFinished)
	}()
	var result startupResult
	select {
	case result = <-startupDone:
	case <-time.After(25 * time.Second):
		cancelStartup()
		t.Fatal("launcher fixture New exceeded watchdog")
	}
	b = result.browser
	if result.err != nil {
		t.Fatalf("启动隔离 launcher fixture: %v", result.err)
	}
	if b == nil {
		t.Fatal("launcher fixture New 未返回 browser")
	}
	cancelStartup()
	pid := b.launcher.PID()
	procPath := filepath.Join("/proc", strconv.Itoa(pid))
	if _, err := os.Stat(procPath); err != nil {
		t.Fatalf("launcher PID %d 未出现在 /proc: %v", pid, err)
	}
	processDeadline := time.Now().Add(5 * time.Second)
	for {
		processes, err := launcherProcessesForProfile(profileDir)
		if err != nil {
			t.Fatalf("记录 Close 前 launcher PID/starttime 集合失败: %v", err)
		}
		trackedProcesses = mergeLauncherProcessIdentities(trackedProcesses, processes)
		if len(trackedProcesses) > 0 {
			break
		}
		if time.Now().After(processDeadline) {
			t.Fatal("Close 前未识别任何隔离 launcher 进程")
		}
		<-time.After(25 * time.Millisecond)
	}
	closeClient := &blockedBrowserCloseClient{events: make(chan *cdp.Event), started: make(chan struct{}, 1), release: make(chan struct{})}
	b.browser.Client(closeClient)
	t.Cleanup(closeClient.Close)
	closeCtx, cancelClose := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelClose()
	closeResult := make(chan error, 1)
	go func() { closeResult <- b.CloseContext(closeCtx) }()
	select {
	case <-closeClient.started:
	case <-time.After(2 * time.Second):
		cancelClose()
		b.launcher.Kill()
		t.Fatal("首次 Close 未进入受控的真实 Browser.close 调用")
	}
	b.closeMu.Lock()
	cleanupOperation := b.cleanupDone
	b.closeMu.Unlock()
	if cleanupOperation == nil {
		cancelClose()
		b.launcher.Kill()
		t.Fatal("首次 Close 未创建 launcher cleanup operation")
	}
	select {
	case <-cleanupOperation:
		cancelClose()
		t.Fatal("触发 deadline 前 cleanup operation 已完成，未覆盖 pending cleanup")
	default:
	}
	var firstErr error
	select {
	case firstErr = <-closeResult:
	case <-time.After(7 * time.Second):
		cancelClose()
		b.launcher.Kill()
		t.Fatal("首次 Close 未在独立 watchdog 内返回")
	}
	if !errors.Is(firstErr, context.DeadlineExceeded) {
		t.Fatalf("首次 Close 必须因 deadline 失败: %v", firstErr)
	}
	select {
	case <-cleanupOperation:
	case <-time.After(5 * time.Second):
		t.Fatal("首次 deadline 后的 launcher Kill 未完成 Cleanup operation")
	}
	if _, err := os.Stat(procPath); !os.IsNotExist(err) {
		t.Fatalf("真实 launcher PID %d 在首次 Close cleanup 后仍存在: %v", pid, err)
	}
	retryCtx, cancelRetry := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelRetry()
	if err := b.CloseContext(retryCtx); err != nil {
		t.Fatalf("同一 launcher cleanup operation 的 Close retry 失败: %v", err)
	}
	b.closeMu.Lock()
	sameOperation, closed := b.cleanupDone == cleanupOperation, b.closed
	b.closeMu.Unlock()
	if !sameOperation {
		t.Fatal("Close retry 未复用首次 Close 创建的 cleanup operation")
	}
	if !closed {
		t.Fatal("Close retry 成功后 browser 应标记为 closed")
	}
	processDeadline = time.Now().Add(5 * time.Second)
	for {
		alive, err := aliveLauncherProcessIdentities(trackedProcesses)
		if err != nil {
			t.Fatalf("复核 Close 前已跟踪 PID/starttime 失败: %v", err)
		}
		if len(alive) == 0 {
			break
		}
		if time.Now().After(processDeadline) {
			t.Fatalf("Close 后仍有已跟踪 launcher 进程: %v", alive)
		}
		<-time.After(25 * time.Millisecond)
	}
}

// Package headless_browser provides a small go-rod wrapper with stealth mode.
package headless_browser

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/stealth"
	"github.com/sirupsen/logrus"
)

// Browser represents a browser instance with its launcher.
type Browser struct {
	browser       *rod.Browser
	browserCancel context.CancelFunc
	launcher      *launcher.Launcher
	stealthJS     bool // 是否注入 go-rod/stealth 的 JS 补丁

	// uaOverride：每个新页面套用的一致 UA/Client-Hints 覆盖。
	// 用浏览器实读的真实版本 + 平台补丁实读的 platform 三项拼出自洽元数据补回。
	// nil = 不覆盖。
	uaOverride *proto.NetworkSetUserAgentOverride

	closeOnce sync.Once
	closeErr  error
	traceFile *os.File
	browserCtx context.Context
	diagnostic *diagnosticCDPClient
}

// Config holds browser options.
type Config struct {
	Headless      bool
	UserAgent     string
	Cookies       string
	ChromeBinPath string
	UserDataDir   string
	Proxy         string
	Trace         bool
	StealthJS     bool
	ExtraArgs     []string
	CloakProfile  bool

	// Fingerprint 为 true 时，传入 CloakBrowser 的源码级指纹参数
	// （--fingerprint={seed} 与 --fingerprint-platform={platform}），激活其一致指纹引擎。
	Fingerprint         bool
	FingerprintPlatform string // 空 = 按运行 OS 自动：linux/windows→windows，darwin→macos
	FingerprintSeed     int    // 0 = 每次启动随机 seed

	// Language 覆盖 Accept-Language 与 navigator.languages（如 "zh-CN"）。空 = 不覆盖。
	// 仅在启用 Fingerprint 的一致 UA 覆盖时生效。
	Language string

	// ExtraFlags 透传任意浏览器启动 flag（如 fingerprint-chromium 的
	// "fingerprint-brand":"Chrome"）。键不带前导 "--"。
	ExtraFlags map[string]string
}

// Option configures a Browser.
type Option func(*Config)

func newDefaultConfig() *Config {
	return &Config{
		Headless:  true,
		StealthJS: true,
	}
}

func WithHeadless(headless bool) Option     { return func(c *Config) { c.Headless = headless } }
func WithUserAgent(userAgent string) Option { return func(c *Config) { c.UserAgent = userAgent } }
func WithCookies(cookies string) Option     { return func(c *Config) { c.Cookies = cookies } }
func WithChromeBinPath(path string) Option  { return func(c *Config) { c.ChromeBinPath = path } }
func WithUserDataDir(path string) Option    { return func(c *Config) { c.UserDataDir = path } }
func WithProxy(proxy string) Option         { return func(c *Config) { c.Proxy = proxy } }
func WithTrace() Option                     { return func(c *Config) { c.Trace = true } }


// WithStealthJS 控制是否注入 go-rod/stealth 的 JS 补丁。用 CloakBrowser 时应传 false。
func WithStealthJS(enabled bool) Option {
	return func(c *Config) { c.StealthJS = enabled }
}

// WithStealth 是 WithStealthJS 的别名，两者控制同一字段。
func WithStealth(enabled bool) Option {
	return WithStealthJS(enabled)
}

func WithCloakLauncherProfile(enabled bool) Option {
	return func(c *Config) { c.CloakProfile = enabled }
}

func CloakLauncherProfile() Option { return WithCloakLauncherProfile(true) }

// WithExtraArgs 设置附加浏览器启动参数。防御性复制，避免调用方后续修改。
func WithExtraArgs(args []string) Option {
	return func(c *Config) {
		c.ExtraArgs = append([]string(nil), args...)
	}
}

// WithFingerprint 启用 CloakBrowser 源码级指纹引擎。
// platform 传空则按运行 OS 自动选择（Linux 服务器呈现 Windows 画像，mac 呈现 macOS）。
func WithFingerprint(platform string) Option {
	return func(c *Config) {
		c.Fingerprint = true
		c.FingerprintPlatform = platform
	}
}

// WithFingerprintSeed 固定指纹 seed（0 表示每次随机）。同一 seed 产出同一套一致指纹。
func WithFingerprintSeed(seed int) Option {
	return func(c *Config) {
		c.FingerprintSeed = seed
	}
}

// WithLanguage 覆盖 Accept-Language 与 navigator.languages（如 "zh-CN"）。仅在启用 Fingerprint 时生效。
func WithLanguage(lang string) Option {
	return func(c *Config) {
		c.Language = lang
	}
}

// WithExtraFlags 透传任意浏览器启动 flag（键不带前导 "--"）。防御性复制 map。
func WithExtraFlags(flagsMap map[string]string) Option {
	return func(c *Config) {
		if flagsMap == nil {
			c.ExtraFlags = nil
			return
		}
		cp := make(map[string]string, len(flagsMap))
		for k, v := range flagsMap {
			cp[k] = v
		}
		c.ExtraFlags = cp
	}
}

// autoFingerprintPlatform 按运行 OS 返回 CloakBrowser 的指纹平台。
// Linux 服务器上呈现 Windows 画像（最常见的真实用户画像，且避免 Linux 桌面的稀有特征）。
func autoFingerprintPlatform() string {
	if runtime.GOOS == "darwin" {
		return "macos"
	}
	return "windows"
}

// New creates a browser with stealth enabled.
func New(ctx context.Context, options ...Option) (*Browser, error) {
	cfg := newDefaultConfig()
	for _, option := range options {
		option(cfg)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// Fingerprint 与显式 UA 互斥：会制造 UA/Client-Hints 冲突，直接拒绝。
	if cfg.Fingerprint && cfg.UserAgent != "" {
		return nil, fmt.Errorf("headless_browser: Fingerprint 与 UserAgent 互斥，禁止同时设置")
	}

	l := launcher.New().
		Headless(cfg.Headless).
		Set("--no-sandbox")
	if cfg.CloakProfile {
		applyCloakLauncherProfile(l)
	}
	if cfg.UserAgent != "" {
		l = l.Set("user-agent", cfg.UserAgent)
	}
	if cfg.Fingerprint {
		platform := cfg.FingerprintPlatform
		if platform == "" {
			platform = autoFingerprintPlatform()
		}
		seed := cfg.FingerprintSeed
		if seed == 0 {
			seed = rand.Intn(89999) + 10000 // 10000-99999
		}
		l = l.Set("fingerprint", strconv.Itoa(seed)).
			Set("fingerprint-platform", platform)
		logrus.Infof("fingerprint enabled: platform=%s", platform)
	}
	for k, v := range cfg.ExtraFlags {
		l = l.Set(flags.Flag(k), v)
	}
	if cfg.ChromeBinPath != "" {
		l = l.Bin(cfg.ChromeBinPath)
	}
	if cfg.UserDataDir != "" {
		l = l.UserDataDir(cfg.UserDataDir).
			Set("disk-cache-size", "16777216").
			Set("media-cache-size", "1048576")
	}
	if cfg.Proxy != "" {
		l = l.Proxy(cfg.Proxy)
	}
	for _, arg := range cfg.ExtraArgs {
		name, value, hasValue, ok := parseLaunchArg(arg)
		if !ok {
			logrus.Warn("忽略格式错误的浏览器启动参数")
			continue
		}
		flag := flags.Flag(name)
		if hasValue {
			l = l.Set(flag, value)
		} else {
			l = l.Set(flag)
		}
	}

	logrus.WithFields(logrus.Fields{
		"bin":       cfg.ChromeBinPath,
		"arg_count": len(l.FormatArgs()),
	}).Info("launching browser")
	url, err := l.Context(ctx).Launch()
	if err != nil {
		l.Kill()
		go l.Cleanup()
		return nil, fmt.Errorf("launch browser: %w", err)
	}
	logrus.WithFields(logrus.Fields{
		"pid": l.PID(),
		"url": url,
	}).Info("browser launched")

	browserCtx, browserCancel := context.WithCancel(context.Background())

	controller := rod.New().Trace(cfg.Trace)
	var traceFile *os.File
	var diagnostic *diagnosticCDPClient
	if tracePath := os.Getenv("XHS_CDP_TRACE_FILE"); tracePath != "" {
		traceFile, err = os.OpenFile(tracePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			browserCancel()
			l.Kill()
			go l.Cleanup()
			return nil, fmt.Errorf("open CDP trace file: %w", err)
		}
		client, err := cdp.StartWithURL(browserCtx, url, nil)
		if err != nil {
			if traceFile != nil {
				_ = traceFile.Close()
			}
			browserCancel()
			l.Kill()
			go l.Cleanup()
			return nil, fmt.Errorf("connect traced CDP client: %w", err)
		}
		logger, loggerErr := newRedactedCDPLogger(traceFile)
		if loggerErr != nil {
			_ = traceFile.Close()
			browserCancel()
			l.Kill()
			go l.Cleanup()
			return nil, fmt.Errorf("create CDP trace logger: %w", loggerErr)
		}
		client.Logger(logger)
		diagnostic = newDiagnosticCDPClient(client, browserCtx, logger)
		controller = controller.Client(diagnostic)
		defer func() { _ = traceFile.Sync() }()
	} else {
		controller = controller.ControlURL(url)
	}
	if cfg.CloakProfile {
		// CloakBrowser 已接管 UA 和视口指纹，避免 rod 默认设备再发覆盖指令。
		controller = controller.NoDefaultDevice()
	}
	controller = controller.Context(browserCtx)
	if err := controller.Connect(); err != nil {
		if traceFile != nil {
			_ = traceFile.Close()
		}
		browserCancel()
		l.Kill()
		go l.Cleanup()
		return nil, fmt.Errorf("connect browser: %w", err)
	}
	browser := controller
	logrus.WithField("pid", l.PID()).Info("browser connected")
	if cfg.Cookies != "" {
		var cookies []*proto.NetworkCookie
		if err := json.Unmarshal([]byte(cfg.Cookies), &cookies); err != nil {
			logrus.Warnf("failed to unmarshal cookies: %v", err)
		} else {
			if err := setBrowserCookies(browser, cookies); err != nil {
				logrus.Warnf("failed to set cookies: %v", err)
			}
		}
	}

	hb := &Browser{browser: browser, browserCancel: browserCancel, browserCtx: browserCtx, launcher: l, stealthJS: cfg.StealthJS, diagnostic: diagnostic}
	hb.traceFile = traceFile

	// 启用指纹时，构建一致 UA 覆盖，补回 go-rod 建页面丢失的 UA 版本保真度。
	// 构建失败必须终止启动，不能无声降级。
	if cfg.Fingerprint {
		ov, err := buildUAOverride(browser, cfg.Language)
		if err != nil {
			if traceFile != nil {
				_ = traceFile.Close()
			}
			browserCancel()
			l.Kill()
			go l.Cleanup()
			return nil, fmt.Errorf("build UA override: %w", err)
		}
		hb.uaOverride = ov
	}

	return hb, nil
}

func setBrowserCookies(browser *rod.Browser, cookies []*proto.NetworkCookie) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("set cookies: %v", recovered)
		}
	}()
	if len(cookies) == 0 {
		return browser.SetCookies(nil)
	}
	return browser.SetCookies(proto.CookiesToParams(cookies))
}

func applyCloakLauncherProfile(l *launcher.Launcher) {
	// CloakBrowser 自己处理自动化特征，去掉 rod 默认容易暴露的启动参数。
	// 不动 disable-features：rod 默认 site-per-process,TranslateUI，
	// 整体替换会静默覆盖未来 rod 新增的默认值，维护风险高。
	l.Delete("enable-automation")
}

// buildUAOverride 用浏览器实读的真实版本补回 go-rod 建页面丢失的 UA 版本保真度。
//
// 分工：版本相关字段（UA 串、brands、fullVersionList、uaFullVersion）由本覆盖负责，
// 版本随 Browser.getVersion 实读、零硬编码；
// platform/platformVersion/architecture 等平台字段留空，交给 CloakBrowser「始终生效」的
// 源码级补丁在真实页面上填充。
func buildUAOverride(browser *rod.Browser, language string) (*proto.NetworkSetUserAgentOverride, error) {
	ver, err := proto.BrowserGetVersion{}.Call(browser)
	if err != nil {
		return nil, err
	}
	fullVersion := strings.TrimPrefix(ver.Product, "Chrome/") // 如 145.0.7632.109
	major := fullVersion
	if i := strings.IndexByte(fullVersion, '.'); i > 0 {
		major = fullVersion[:i]
	}

	ov := &proto.NetworkSetUserAgentOverride{
		UserAgent: ver.UserAgent, // 浏览器真实 UA（正确平台+版本）
		UserAgentMetadata: &proto.EmulationUserAgentMetadata{
			Brands: []*proto.EmulationUserAgentBrandVersion{
				{Brand: "Not:A-Brand", Version: "99"},
				{Brand: "Google Chrome", Version: major},
				{Brand: "Chromium", Version: major},
			},
			FullVersionList: []*proto.EmulationUserAgentBrandVersion{
				{Brand: "Not:A-Brand", Version: "99.0.0.0"},
				{Brand: "Google Chrome", Version: fullVersion},
				{Brand: "Chromium", Version: fullVersion},
			},
			FullVersion: fullVersion,
			// 平台字段留空：交给 CloakBrowser 始终生效的补丁填充
		},
	}
	// Accept-Language 传不带 q 值的形式，否则 navigator.languages 会混入 "zh;q=0.9"
	if language != "" {
		ov.AcceptLanguage = language + "," + primaryLang(language)
	}

	logrus.Infof("UA coherence: version=%s", ver.Product)
	return ov, nil
}

// primaryLang 取语言主标签，如 "zh-CN" → "zh"。
func primaryLang(lang string) string {
	if i := strings.IndexByte(lang, '-'); i > 0 {
		return lang[:i]
	}
	return lang
}

// Close preserves the upstream API. Callers that need to handle a failed CDP
// shutdown should use CloseContext.
func (b *Browser) Close() {
	_ = b.CloseContext(context.Background())
}

// CloseContext never waits indefinitely for a hung Chrome renderer. The Rod
// launcher Cleanup method waits on Chrome's exit channel with no deadline, so
// it must be raced with ctx as well as Browser.close. If either stage does not
// finish in time, kill the launcher's process group before returning.
func (b *Browser) CloseContext(ctx context.Context) error {
	b.closeOnce.Do(func() {
		b.closeErr = b.close(ctx)
		if b.traceFile != nil {
			_ = b.traceFile.Close()
		}
	})
	return b.closeErr
}

// Health 检查 CDP 连接是否可用。
func (b *Browser) Health(ctx context.Context) error {
	_, err := proto.BrowserGetVersion{}.Call(b.browser.Context(ctx))
	return err
}

func (b *Browser) close(ctx context.Context) error {
	if b.browserCancel != nil {
		defer b.browserCancel()
	}

	err := b.browser.Context(ctx).Close()
	if err != nil {
		b.launcher.Kill()
		go b.launcher.Cleanup()
		return err
	}

	cleaned := make(chan struct{})
	go func() {
		b.launcher.Cleanup()
		close(cleaned)
	}()

	select {
	case <-cleaned:
		return nil
	case <-ctx.Done():
		b.launcher.Kill()
		return ctx.Err()
	}
}

// Page 创建页面。CloakBrowser 已在浏览器层处理指纹，不能再叠加 stealth 注入。
// 若配置了一致 UA 覆盖，则在返回前套用，补回 UA 版本保真度。
func (b *Browser) Page() (page *rod.Page, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("create page: %v", recovered)
			logrus.WithError(fmt.Errorf("%v", recovered)).Debugf("headless_browser.Page panicked (stealth=%v, browser=%v)", b.stealthJS, b.browser)
		}
	}()
	if b.stealthJS {
		page, err = stealth.Page(b.browser)
	} else {
		page, err = b.browser.Page(proto.TargetCreateTarget{})
	}
	if err != nil {
		logrus.WithError(err).Debug("rod.Browser.Page returned error")
		return nil, err
	}
	if b.uaOverride != nil {
		if err := b.uaOverride.Call(page); err != nil {
			return nil, fmt.Errorf("apply UA override: %w", err)
		}
	}
	if b.diagnostic != nil {
		ctx, cancel := context.WithTimeout(b.browserCtx, 2*time.Second)
		bound := page.Context(ctx)
		var setup sync.WaitGroup
		var runtimeErr, lifecycleErr error
		setup.Add(2)
		go func() { defer setup.Done(); runtimeErr = proto.RuntimeEnable{}.Call(bound) }()
		go func() { defer setup.Done(); lifecycleErr = (proto.PageSetLifecycleEventsEnabled{Enabled: true}).Call(bound) }()
		setup.Wait()
		cancel()
		b.diagnostic.logger.setup(runtimeErr, lifecycleErr)
		b.diagnostic.Arm(page.SessionID)
	}
	return page, nil
}

// NewPage preserves the upstream convenience API.
func (b *Browser) NewPage() *rod.Page {
	page, err := b.Page()
	if err != nil {
		panic(err)
	}
	return page
}

func parseLaunchArg(raw string) (name, value string, hasValue, ok bool) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "--") {
		return "", "", false, false
	}
	raw = strings.TrimPrefix(raw, "--")
	if raw == "" {
		return "", "", false, false
	}

	name, value, hasValue = strings.Cut(raw, "=")
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') &&
			(r < '0' || r > '9') && r != '-' && r != '_' && r != '.' {
			return "", "", false, false
		}
	}
	return name, value, hasValue, true
}

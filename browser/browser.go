package browser

import (
	"context"
	"net/url"
	"os"
	"strings"

	"github.com/sirupsen/logrus"
	"github.com/xpzouying/headless_browser"
	"github.com/xpzouying/xiaohongshu-mcp/cookies"
	"github.com/xpzouying/xiaohongshu-mcp/humanize"
	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
)

type browserConfig struct {
	binPath              string
	profileDir           string
	cloakBrowser         bool
	cloakLauncherProfile bool
	extraArgs            []string
	userAgent            string
	fingerprintSeed      int
	language             string
}

type Option func(*browserConfig)

func WithBinPath(binPath string) Option {
	return func(c *browserConfig) {
		c.binPath = binPath
	}
}

// WithProfileDir 设置浏览器持久 profile 目录。
func WithProfileDir(profileDir string) Option {
	return func(c *browserConfig) {
		c.profileDir = profileDir
	}
}

// WithCloakBrowser 设置是否使用 CloakBrowser。
func WithCloakBrowser(enabled bool) Option {
	return func(c *browserConfig) {
		c.cloakBrowser = enabled
	}
}

// WithCloakLauncherProfile 设置是否使用 CloakBrowser 专用 launcher 配置。
func WithCloakLauncherProfile(enabled bool) Option {
	return func(c *browserConfig) {
		c.cloakLauncherProfile = enabled
	}
}

// WithExtraArgs 设置附加浏览器启动参数。
func WithExtraArgs(args []string) Option {
	return func(c *browserConfig) {
		c.extraArgs = append([]string(nil), args...)
	}
}

func WithUserAgent(userAgent string) Option {
	return func(c *browserConfig) {
		c.userAgent = userAgent
	}
}

// WithFingerprintSeed 设置 CloakBrowser fingerprint 的持久 seed。
func WithFingerprintSeed(seed int) Option {
	return func(c *browserConfig) {
		c.fingerprintSeed = seed
	}
}

// WithLanguage 设置浏览器语言（如 zh-CN），仅在 Cloak fingerprint 启用时生效。
func WithLanguage(language string) Option {
	return func(c *browserConfig) {
		c.language = language
	}
}

// cloakFingerprintEnabled 判断 Cloak 模式下是否启用 fingerprint。
// 显式 UA 优先：一旦配置 XHS_BROWSER_USER_AGENT，跳过 fingerprint 和 UA override。
func cloakFingerprintEnabled(cloakBrowser bool, userAgent string) bool {
	return cloakBrowser && userAgent == ""
}

// cloakFingerprintOptions 构建 Cloak 模式的 fingerprint/language 启动选项。
func cloakFingerprintOptions(seed int, language string) []headless_browser.Option {
	return []headless_browser.Option{
		headless_browser.WithFingerprint(""),
		headless_browser.WithFingerprintSeed(seed),
		headless_browser.WithLanguage(language),
		headless_browser.WithExtraFlags(map[string]string{"fingerprint-brand": "Chrome"}),
	}
}

// maskProxyCredentials masks username and password in proxy URL for safe logging.
func maskProxyCredentials(proxyURL string) string {
	u, err := url.Parse(proxyURL)
	if err != nil {
		return "[invalid-proxy]"
	}
	if u.User == nil {
		// 无 scheme 前缀的 user:pass@host 会被 url.Parse 误判为 scheme，opaque 段含 @ 时无法安全确认。
		if strings.Contains(u.Opaque, "@") {
			return "[invalid-proxy]"
		}
		return proxyURL
	}
	masked := "***"
	if _, hasPwd := u.User.Password(); hasPwd {
		masked = "***:***"
	}
	// 定位 authority：支持 scheme:// 与 scheme-relative // 两种形态；只替换 authority 段内的 userinfo。
	start := 0
	if idx := strings.Index(proxyURL, "://"); idx >= 0 {
		start = idx + 3
	} else if strings.HasPrefix(proxyURL, "//") {
		start = 2
	} else {
		// 无 scheme 前缀却带 userinfo：无法安全确认 authority 边界，fail-closed。
		return "[invalid-proxy]"
	}
	authEnd := len(proxyURL)
	if slash := strings.IndexAny(proxyURL[start:], "/?#"); slash >= 0 {
		authEnd = start + slash
	}
	at := strings.LastIndex(proxyURL[start:authEnd], "@")
	if at < 0 {
		return proxyURL
	}
	end := start + at // @ 的位置
	return proxyURL[:start] + masked + proxyURL[end:]
}

func NewBrowser(ctx context.Context, headless bool, options ...Option) (*hrod.Browser, error) {
	cfg := &browserConfig{}
	for _, opt := range options {
		opt(cfg)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	opts := []headless_browser.Option{
		headless_browser.WithHeadless(headless),
	}
	if cfg.binPath != "" {
		opts = append(opts, headless_browser.WithChromeBinPath(cfg.binPath))
	}
	if cfg.userAgent != "" {
		opts = append(opts, headless_browser.WithUserAgent(cfg.userAgent))
	}
	if cfg.profileDir != "" {
		opts = append(opts, headless_browser.WithUserDataDir(cfg.profileDir))
	}
	if cfg.cloakBrowser {
		opts = append(opts, headless_browser.WithStealth(false))
		logrus.Info("using CloakBrowser without go-rod stealth injection")
		if cloakFingerprintEnabled(cfg.cloakBrowser, cfg.userAgent) {
			opts = append(opts, cloakFingerprintOptions(cfg.fingerprintSeed, cfg.language)...)
			logrus.Info("CloakBrowser fingerprint enabled (no explicit UA)")
		} else {
			logrus.Warn("CloakBrowser 配置了 XHS_BROWSER_USER_AGENT，跳过 fingerprint/UA override，显式 UA 优先")
		}
	}
	if cfg.cloakBrowser || cfg.cloakLauncherProfile {
		opts = append(opts, headless_browser.CloakLauncherProfile())
		logrus.Info("using CloakBrowser launcher profile")
	}
	if len(cfg.extraArgs) > 0 {
		opts = append(opts, headless_browser.WithExtraArgs(cfg.extraArgs))
		logrus.Infof("using %d extra browser launch args", len(cfg.extraArgs))
	}

	// Read proxy from environment variable
	if proxy := os.Getenv("XHS_PROXY"); proxy != "" {
		opts = append(opts, headless_browser.WithProxy(proxy))
		logrus.Infof("Using proxy: %s", maskProxyCredentials(proxy))
	}

	// 加载 cookies
	cookiePath := cookies.GetCookiesFilePath()
	cookieLoader := cookies.NewLoadCookie(cookiePath)

	if data, err := cookieLoader.LoadCookies(); err == nil {
		opts = append(opts, headless_browser.WithCookies(string(data)))
		logrus.Debugf("loaded cookies from file successfully")
	} else {
		logrus.Warnf("failed to load cookies: %v", err)
	}

	logrus.WithFields(logrus.Fields{
		"headless":    headless,
		"bin":         cfg.binPath,
		"profile_dir": cfg.profileDir,
	}).Info("starting browser")
	hb, err := headless_browser.New(ctx, opts...)
	if err != nil {
		logrus.WithError(err).Error("browser startup failed")
		return nil, err
	}
	logrus.Info("browser startup completed")

	return hrod.NewBrowser(hb, humanize.DefaultConfig()), nil
}

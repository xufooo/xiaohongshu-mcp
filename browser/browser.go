package browser

import (
	"net/url"
	"os"

	"github.com/sirupsen/logrus"
	"github.com/xpzouying/headless_browser"
	"github.com/xpzouying/xiaohongshu-mcp/cookies"
	"github.com/xpzouying/xiaohongshu-mcp/pkg/humanize"
	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

type browserConfig struct {
	binPath      string
	profileDir   string
	cloakBrowser bool
	extraArgs    []string
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

// WithExtraArgs 设置附加浏览器启动参数。
func WithExtraArgs(args []string) Option {
	return func(c *browserConfig) {
		c.extraArgs = append([]string(nil), args...)
	}
}

// maskProxyCredentials masks username and password in proxy URL for safe logging.
func maskProxyCredentials(proxyURL string) string {
	u, err := url.Parse(proxyURL)
	if err != nil || u.User == nil {
		return proxyURL
	}
	if _, hasPassword := u.User.Password(); hasPassword {
		u.User = url.UserPassword("***", "***")
	} else {
		u.User = url.User("***")
	}
	return u.String()
}

func NewBrowser(headless bool, options ...Option) *hrod.Browser {
	cfg := &browserConfig{}
	for _, opt := range options {
		opt(cfg)
	}

	opts := []headless_browser.Option{
		headless_browser.WithHeadless(headless),
	}
	if cfg.binPath != "" {
		opts = append(opts, headless_browser.WithChromeBinPath(cfg.binPath))
	}
	if cfg.profileDir != "" {
		opts = append(opts, headless_browser.WithUserDataDir(cfg.profileDir))
	}
	if cfg.cloakBrowser {
		opts = append(opts, headless_browser.WithStealth(false))
		logrus.Info("using CloakBrowser without go-rod stealth injection")
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

	hb := headless_browser.New(opts...)

	return hrod.NewBrowser(hb, humanize.DefaultConfig())
}

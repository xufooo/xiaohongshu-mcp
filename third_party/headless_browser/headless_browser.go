// Package headless_browser provides a small go-rod wrapper with stealth mode.
package headless_browser

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/stealth"
	"github.com/sirupsen/logrus"
)

// Browser represents a browser instance with its launcher.
type Browser struct {
	browser   *rod.Browser
	launcher  *launcher.Launcher
	stealth   bool
	closeOnce sync.Once
	closeErr  error
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
	Stealth       bool
	ExtraArgs     []string
	CloakProfile  bool
}

// Option configures a Browser.
type Option func(*Config)

func newDefaultConfig() *Config {
	return &Config{
		Headless: true,
		Stealth:  true,
	}
}

func WithHeadless(headless bool) Option     { return func(c *Config) { c.Headless = headless } }
func WithUserAgent(userAgent string) Option { return func(c *Config) { c.UserAgent = userAgent } }
func WithCookies(cookies string) Option     { return func(c *Config) { c.Cookies = cookies } }
func WithChromeBinPath(path string) Option  { return func(c *Config) { c.ChromeBinPath = path } }
func WithUserDataDir(path string) Option    { return func(c *Config) { c.UserDataDir = path } }
func WithProxy(proxy string) Option         { return func(c *Config) { c.Proxy = proxy } }
func WithTrace() Option                     { return func(c *Config) { c.Trace = true } }
func WithStealth(enabled bool) Option       { return func(c *Config) { c.Stealth = enabled } }
func WithCloakLauncherProfile(enabled bool) Option {
	return func(c *Config) { c.CloakProfile = enabled }
}

func CloakLauncherProfile() Option { return WithCloakLauncherProfile(true) }

func WithExtraArgs(args []string) Option {
	return func(c *Config) {
		c.ExtraArgs = append([]string(nil), args...)
	}
}

var lowMemoryLaunchArgs = []flags.Flag{
	"disable-dev-shm-usage",
	"disable-gpu",
	"disable-extensions",
	"disable-background-networking",
	"disable-sync",
	"disable-component-update",
	"no-first-run",
	"no-default-browser-check",
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

	l := launcher.New().
		Headless(cfg.Headless).
		Set("--no-sandbox")
	for _, arg := range lowMemoryLaunchArgs {
		l = l.Set(arg)
	}
	if cfg.CloakProfile {
		applyCloakLauncherProfile(l)
	}
	if cfg.UserAgent != "" {
		l = l.Set("user-agent", cfg.UserAgent)
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
		"bin":  cfg.ChromeBinPath,
		"args": l.FormatArgs(),
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

	controller := rod.New().ControlURL(url).Trace(cfg.Trace)
	if cfg.CloakProfile {
		// CloakBrowser 已接管 UA 和视口指纹，避免 rod 默认设备再发覆盖指令。
		controller = controller.NoDefaultDevice()
	}
	controller = controller.Context(ctx)
	if err := controller.Connect(); err != nil {
		l.Kill()
		go l.Cleanup()
		return nil, fmt.Errorf("connect browser: %w", err)
	}
	controller = controller.Context(context.Background())
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

	return &Browser{browser: browser, launcher: l, stealth: cfg.Stealth}, nil
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
	})
	return b.closeErr
}

// Health 检查 CDP 连接是否可用。
func (b *Browser) Health(ctx context.Context) error {
	_, err := proto.BrowserGetVersion{}.Call(b.browser.Context(ctx))
	return err
}

func (b *Browser) close(ctx context.Context) error {
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
func (b *Browser) Page() (page *rod.Page, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("create page: %v", recovered)
		}
	}()
	if b.stealth {
		return stealth.Page(b.browser)
	}
	return b.browser.Page(proto.TargetCreateTarget{})
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

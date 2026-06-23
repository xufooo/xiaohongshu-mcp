// Package headless_browser provides a small go-rod wrapper with stealth mode.
package headless_browser

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/stealth"
	"github.com/sirupsen/logrus"
)

// Browser represents a browser instance with its launcher.
type Browser struct {
	browser   *rod.Browser
	launcher  *launcher.Launcher
	closeOnce sync.Once
	closeErr  error
}

// Config holds browser options.
type Config struct {
	Headless      bool
	UserAgent     string
	Cookies       string
	ChromeBinPath string
	Proxy         string
	Trace         bool
}

// Option configures a Browser.
type Option func(*Config)

func newDefaultConfig() *Config {
	return &Config{
		Headless:  true,
		UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
	}
}

func WithHeadless(headless bool) Option     { return func(c *Config) { c.Headless = headless } }
func WithUserAgent(userAgent string) Option { return func(c *Config) { c.UserAgent = userAgent } }
func WithCookies(cookies string) Option     { return func(c *Config) { c.Cookies = cookies } }
func WithChromeBinPath(path string) Option  { return func(c *Config) { c.ChromeBinPath = path } }
func WithProxy(proxy string) Option         { return func(c *Config) { c.Proxy = proxy } }
func WithTrace() Option                     { return func(c *Config) { c.Trace = true } }

// New creates a browser with stealth enabled.
func New(options ...Option) *Browser {
	cfg := newDefaultConfig()
	for _, option := range options {
		option(cfg)
	}

	l := launcher.New().
		Headless(cfg.Headless).
		Set("--no-sandbox").
		Set("user-agent", cfg.UserAgent)
	if cfg.ChromeBinPath != "" {
		l = l.Bin(cfg.ChromeBinPath)
	}
	if cfg.Proxy != "" {
		l = l.Proxy(cfg.Proxy)
	}

	url := l.MustLaunch()
	browser := rod.New().ControlURL(url).Trace(cfg.Trace).MustConnect()
	if cfg.Cookies != "" {
		var cookies []*proto.NetworkCookie
		if err := json.Unmarshal([]byte(cfg.Cookies), &cookies); err != nil {
			logrus.Warnf("failed to unmarshal cookies: %v", err)
		} else {
			browser.MustSetCookies(cookies...)
		}
	}

	return &Browser{browser: browser, launcher: l}
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

// NewPage creates a stealth page.
func (b *Browser) NewPage() *rod.Page { return stealth.MustPage(b.browser) }

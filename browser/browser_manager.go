package browser

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/xpzouying/xiaohongshu-mcp/configs"
	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

const (
	pageCloseTimeout      = 2 * time.Second
	browserHealthTimeout  = 2 * time.Second
	defaultStartupTimeout = 120 * time.Second
)

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

// Manager 串行复用一个浏览器实例，避免树莓派频繁启动 Chromium。
type Manager struct {
	factory BrowserFactory
	token   chan struct{}

	mu       sync.Mutex
	browser  *hrod.Browser
	starting *browserStartup
	startErr error
	closed   bool

	idleTimeout time.Duration
	idleTimer   *time.Timer
	idleVersion uint64
}

type browserStartup struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
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
		factory:     factory,
		token:       make(chan struct{}, 1),
		idleTimeout: 5 * time.Minute,
	}
	for _, option := range options {
		option(m)
	}
	m.token <- struct{}{}
	return m
}

// Acquire 获取独占页面。浏览器启动不占用操作令牌。
func (m *Manager) Acquire(ctx context.Context) (*hrod.Page, error) {
	for {
		b, err := m.getBrowser(ctx)
		if err != nil {
			return nil, err
		}
		if err := m.lock(ctx); err != nil {
			return nil, err
		}

		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			m.releaseToken()
			return nil, errors.New("browser manager is closing")
		}
		if m.browser != b {
			m.mu.Unlock()
			m.releaseToken()
			continue
		}
		m.cancelIdleCloseLocked()
		m.mu.Unlock()
		if err := checkBrowserHealth(b); err != nil {
			m.discardBrowser(b)
			m.releaseToken()
			continue
		}

		page, err := newPage(b)
		if err != nil {
			m.discardBrowser(b)
			m.releaseToken()
			return nil, err
		}
		return page, nil
	}
}

// Release 关闭本次页面并归还独占权，浏览器保持常驻。
func (m *Manager) Release(page *hrod.Page) {
	if page != nil {
		ctx, cancel := context.WithTimeout(context.Background(), pageCloseTimeout)
		err := page.Context(ctx).Close()
		cancel()
		if err != nil {
			m.discardBrowser(page.Browser())
		}
	}
	m.scheduleIdleClose()
	m.releaseToken()
}

// Reset 关闭常驻浏览器。下次 Acquire 会创建新实例。
func (m *Manager) Reset(ctx context.Context) error {
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.releaseToken()

	m.mu.Lock()
	m.cancelIdleCloseLocked()
	b := m.browser
	m.browser = nil
	if m.starting != nil {
		m.starting.err = errors.New("browser startup reset")
		m.starting.cancel()
		m.starting.finish()
		m.starting = nil
	}
	m.startErr = nil
	m.mu.Unlock()

	if b == nil {
		return nil
	}
	return b.Close()
}

// Close 阻止新的获取并关闭常驻浏览器。
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	m.cancelIdleCloseLocked()
	m.mu.Unlock()
	return m.Reset(ctx)
}

func (m *Manager) getBrowser(ctx context.Context) (*hrod.Browser, error) {
	for {
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return nil, errors.New("browser manager is closing")
		}
		if m.browser != nil {
			b := m.browser
			m.mu.Unlock()
			return b, nil
		}
		started := m.starting
		if started == nil {
			startupTimeout := defaultStartupTimeout
			if configuredTimeout := configs.GetBrowserStartupTimeout(); configuredTimeout > 0 {
				startupTimeout = configuredTimeout
			}
			started = newBrowserStartup(startupTimeout)
			m.starting = started
			m.startErr = nil
			go m.startBrowser(started)
			time.AfterFunc(startupTimeout, func() {
				m.failStartup(started)
			})
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
	b, err := newBrowser(started.ctx, m.factory)

	m.mu.Lock()
	if m.starting != started {
		m.mu.Unlock()
		if b != nil {
			_ = b.Close()
		}
		return
	}
	if m.closed && b != nil {
		m.mu.Unlock()
		_ = b.Close()
		m.mu.Lock()
		b = nil
	}
	m.browser = b
	m.startErr = err
	started.err = err
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

func newPage(browser *hrod.Browser) (page *hrod.Page, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("create browser page failed: %v", recovered)
		}
	}()
	return browser.NewPage(), nil
}

func (m *Manager) discardBrowser(target *hrod.Browser) {
	if target == nil {
		return
	}
	m.mu.Lock()
	if m.browser == target {
		m.browser = nil
	}
	m.cancelIdleCloseLocked()
	m.mu.Unlock()
	_ = target.Close()
}

func checkBrowserHealth(browser *hrod.Browser) error {
	ctx, cancel := context.WithTimeout(context.Background(), browserHealthTimeout)
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
	m.starting = nil
	started.finish()
}

func (m *Manager) scheduleIdleClose() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.browser == nil || m.idleTimeout <= 0 {
		return
	}
	m.cancelIdleCloseLocked()
	version := m.idleVersion
	m.idleTimer = time.AfterFunc(m.idleTimeout, func() {
		m.closeIfIdle(version)
	})
}

func (m *Manager) closeIfIdle(version uint64) {
	if err := m.lock(context.Background()); err != nil {
		return
	}
	defer m.releaseToken()

	m.mu.Lock()
	if m.closed || m.idleVersion != version {
		m.mu.Unlock()
		return
	}
	b := m.browser
	m.browser = nil
	m.idleTimer = nil
	m.idleVersion++
	m.mu.Unlock()

	if b != nil {
		_ = b.Close()
	}
}

func (m *Manager) cancelIdleCloseLocked() {
	m.idleVersion++
	if m.idleTimer != nil {
		m.idleTimer.Stop()
		m.idleTimer = nil
	}
}

func (m *Manager) lock(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.token:
		return nil
	}
}

func (m *Manager) releaseToken() {
	m.token <- struct{}{}
}

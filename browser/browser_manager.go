package browser

import (
	"context"
	"errors"
	"math/rand"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/configs"
	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

// Manager 管理单个浏览器+页面的生命周期。
// 所有操作复用同一个页面，操作完成后随机空闲等待 30s~5min 后自动关闭。
// 下次操作到来时取消关闭，继续使用。
type Manager struct {
	mu         chan struct{}
	browser    *hrod.Browser
	page       *hrod.Page
	closeTimer *time.Timer
	idleGen    uint64
	closing    atomic.Bool

	minIdle time.Duration
	maxIdle time.Duration
}

// ManagerOption 可选配置
type ManagerOption func(*Manager)

// WithIdleRange 设置空闲关闭的随机范围
func WithIdleRange(min, max time.Duration) ManagerOption {
	return func(m *Manager) {
		m.minIdle = min
		m.maxIdle = max
	}
}

// NewManager 创建浏览器管理器
func NewManager(opts ...ManagerOption) *Manager {
	m := &Manager{
		minIdle: 30 * time.Second,
		maxIdle: 5 * time.Minute,
		mu:      make(chan struct{}, 1),
	}
	m.mu <- struct{}{}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Acquire 获取页面，阻塞直到可用或上下文取消。
// 返回后必须调用 Release() 归还，调用序列：acquire→use→release→acquire→...
func (m *Manager) Acquire(ctx context.Context) (*hrod.Page, error) {
	if err := m.lock(ctx); err != nil {
		return nil, err
	}
	if m.closing.Load() {
		m.unlock()
		return nil, errors.New("browser manager is closing")
	}

	// 有操作进来，取消待关闭定时器
	m.idleGen++
	if m.closeTimer != nil {
		m.closeTimer.Stop()
		m.closeTimer = nil
	}

	// 复用已有页面（浏览器还活着）
	if m.page != nil {
		if _, err := m.page.Rod.Eval(`1`); err == nil {
			return m.page, nil
		}
		logrus.Warn("浏览器页面已失效，重新创建")
		m.cleanup()
	}

	// 首次或重建
	b := NewBrowser(configs.IsHeadless(), WithBinPath(configs.GetBinPath()))
	m.browser = b
	m.page = b.NewPage()
	return m.page, nil
}

// Release 归还页面，开始随机空闲定时器，超时后自动关闭浏览器。
func (m *Manager) Release() {
	if m.closing.Load() {
		m.idleGen++
		if m.closeTimer != nil {
			m.closeTimer.Stop()
			m.closeTimer = nil
		}
		m.cleanup()
		m.unlock()
		return
	}

	wait := m.minIdle
	if m.maxIdle > m.minIdle {
		wait += time.Duration(rand.Int63n(int64(m.maxIdle - m.minIdle)))
	}
	logrus.Infof("操作完成，%.0f秒后自动关闭浏览器", wait.Seconds())

	m.idleGen++
	idleGen := m.idleGen
	m.closeTimer = time.AfterFunc(wait, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := m.lock(ctx); err != nil {
			return
		}
		defer m.unlock()
		if m.closing.Load() {
			m.cleanup()
			m.closeTimer = nil
			return
		}
		if idleGen != m.idleGen {
			return
		}
		if m.browser != nil {
			logrus.Info("空闲超时，关闭浏览器")
			m.cleanup()
			m.closeTimer = nil
		}
	})

	m.unlock()
}

// Close 立即关闭浏览器（服务关闭时调用）
func (m *Manager) Close() {
	m.closing.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := m.lock(ctx); err != nil {
		return
	}
	defer m.unlock()
	m.idleGen++
	if m.closeTimer != nil {
		m.closeTimer.Stop()
		m.closeTimer = nil
	}
	m.cleanup()
}

func (m *Manager) cleanup() {
	if m.browser != nil {
		m.browser.Close()
		m.browser = nil
		m.page = nil
	}
}

func (m *Manager) lock(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.mu:
		return nil
	}
}

func (m *Manager) unlock() {
	m.mu <- struct{}{}
}

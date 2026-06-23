package browser

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/configs"
	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

// Manager 管理单个浏览器+页面的生命周期。
// 每次操作完成后立即关闭浏览器。
type Manager struct {
	mu      chan struct{}
	browser *hrod.Browser
	page    *hrod.Page
	closing atomic.Bool
}

// NewManager 创建浏览器管理器
func NewManager() *Manager {
	m := &Manager{
		mu: make(chan struct{}, 1),
	}
	m.mu <- struct{}{}
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

// Release 归还页面并立即关闭浏览器。
func (m *Manager) Release() {
	m.cleanup()
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

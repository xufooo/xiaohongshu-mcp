package xiaohongshu

import (
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"
	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

const (
	loginReadySelector       = ".main-container .user .link-wrapper .channel"
	loginQRCodeSelector     = ".login-container .qrcode-img"
	defaultLoginWaitTimeout  = 4 * time.Minute
	defaultQRCodeWaitTimeout = 30 * time.Second
)

type LoginAction struct {
	page *hrod.Page
}

func NewLogin(page *hrod.Page) *LoginAction {
	return &LoginAction{page: page}
}

func (a *LoginAction) CheckLoginStatus(ctx context.Context) (bool, error) {
	pp := a.page.Context(ctx)
	if err := pp.Navigate("https://www.xiaohongshu.com/explore"); err != nil {
		return false, errors.Wrap(err, "navigate to explore")
	}

	// 等待页面加载（3s缓冲），然后检查元素
	if err := pp.Sleep(3 * time.Second); err != nil {
		return false, err
	}

	exists, _, err := pp.Has(loginReadySelector)
	if err != nil {
		return false, errors.Wrap(err, "check login status failed")
	}

	return exists, nil
}

func (a *LoginAction) Login(ctx context.Context) error {
	// 导航到小红书首页，这会触发二维码弹窗
	pp := a.page.Context(ctx)
	if err := pp.Navigate("https://www.xiaohongshu.com/explore"); err != nil {
		return errors.Wrap(err, "navigate to explore")
	}

	// 等待一小段时间让页面完全加载
	if err := pp.Sleep(3 * time.Second); err != nil {
		return err
	}

	// 检查是否已经登录
	if exists, _, _ := pp.Has(loginReadySelector); exists {
		// 已经登录，直接返回
		return nil
	}

	// 等待扫码成功提示或者登录完成
	// 这里我们等待登录成功的元素出现，这样更简单可靠
	loginCtx := ctx
	cancel := func() {}
	if _, ok := loginCtx.Deadline(); !ok {
		loginCtx, cancel = context.WithTimeout(ctx, defaultLoginWaitTimeout)
	}
	defer cancel()

	if a.WaitForLogin(loginCtx) {
		return nil
	}
	if err := loginCtx.Err(); err != nil {
		return err
	}
	return errors.New("等待登录完成超时")
}

func (a *LoginAction) FetchQrcodeImage(ctx context.Context) (string, bool, error) {
	// 导航到小红书首页，这会触发二维码弹窗
	pp := a.page.Context(ctx)
	if err := pp.Navigate("https://www.xiaohongshu.com/explore"); err != nil {
		return "", false, errors.Wrap(err, "navigate to explore")
	}

	// 等待一小段时间让页面完全加载
	if err := pp.Sleep(3 * time.Second); err != nil {
		return "", false, err
	}

	// 检查是否已经登录
	if exists, _, _ := pp.Has(loginReadySelector); exists {
		return "", true, nil
	}

	// 获取二维码图片
	qrcode, err := waitForElement(pp, loginQRCodeSelector, defaultQRCodeWaitTimeout)
	if err != nil {
		return "", false, errors.Wrap(err, "get qrcode element failed")
	}
	src, err := qrcode.Attribute("src")
	if err != nil {
		return "", false, errors.Wrap(err, "get qrcode src failed")
	}
	if src == nil || len(*src) == 0 {
		return "", false, errors.New("qrcode src is empty")
	}

	return *src, false, nil
}

func (a *LoginAction) WaitForLogin(ctx context.Context) bool {
	pp := a.page.Context(ctx)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			el, err := pp.Element(loginReadySelector)
			if err == nil && el != nil {
				return true
			}
		}
	}
}

func waitForElement(page *hrod.Page, selector string, timeout time.Duration) (*hrod.Element, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error

	for time.Now().Before(deadline) {
		if err := page.Err(); err != nil {
			return nil, err
		}
		el, err := page.Element(selector)
		if err == nil && el != nil {
			return el, nil
		}
		lastErr = err
		if err := page.Sleep(300 * time.Millisecond); err != nil {
			return nil, err
		}
	}

	if lastErr != nil {
		return nil, fmt.Errorf("等待元素 %s 超时(%s): %w", selector, timeout, lastErr)
	}
	return nil, fmt.Errorf("等待元素 %s 超时(%s)", selector, timeout)
}

package xiaohongshu

import (
	"fmt"
	"time"

	"github.com/sirupsen/logrus"
	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

const noteCloseProbeDelay = 500 * time.Millisecond

func closeNoteOverlay(page *hrod.Page, sourceURL string) (closeMethod string, err error) {
	if page == nil {
		return "", fmt.Errorf("页面不存在")
	}

	if err := page.Keyboard.Press("Escape"); err != nil {
		logrus.Debugf("Escape 关闭笔记面板失败: %v", err)
	}
	if closed, err := noteOverlayClosedAfterAttempt(page); err != nil {
		return "", err
	} else if closed {
		return "escape", nil
	}

	if _, err := page.Eval(`() => {
  const note = document.querySelector('.note-container');
  if (!note || !note.isConnected) return;
  const target = document.elementFromPoint(8, 8) || document.body;
  target.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, clientX: 8, clientY: 8 }));
}`); err != nil {
		logrus.Debugf("点击笔记面板外部区域失败: %v", err)
	} else if closed, err := noteOverlayClosedAfterAttempt(page); err != nil {
		return "", err
	} else if closed {
		return "outside_click", nil
	}

	if sourceURL == "" {
		return "", fmt.Errorf("面板关闭失败，所有非导航方案已尝试")
	}
	if err := page.Navigate(sourceURL); err != nil {
		return "", fmt.Errorf("降级导航回来源页失败: %w", err)
	}
	if err := WaitForXHSReady(page, XHSReadyOptions{Kind: inferXHSReadyKindFromURL(sourceURL)}); err != nil {
		return "", err
	}
	if closed, err := noteOverlayClosedAfterAttempt(page); err != nil {
		return "", err
	} else if !closed {
		return "", fmt.Errorf("笔记面板仍未关闭")
	}
	return "navigate", nil
}

func noteOverlayClosedAfterAttempt(page *hrod.Page) (bool, error) {
	time.Sleep(noteCloseProbeDelay)
	visibleCount, err := probePanelVisible(page)
	if err != nil {
		return false, err
	}
	return visibleCount == 0, nil
}

func probePanelVisible(page *hrod.Page) (visibleCount int, err error) {
	if page == nil {
		return 0, fmt.Errorf("页面不存在")
	}
	result, err := page.Eval(`() => document.querySelectorAll('.note-container').length`)
	if err != nil {
		return 0, err
	}
	if result == nil {
		return 0, fmt.Errorf("面板可见性探测返回为空")
	}
	return result.Value.Int(), nil
}

package xiaohongshu

import (
	"fmt"
	"time"

	"github.com/go-rod/rod/lib/input"
	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

const (
	noteCloseProbeInterval = 100 * time.Millisecond
	noteCloseProbeTimeout  = 5 * time.Second
)

func closeNoteOverlay(page *hrod.Page, sourceURL string) (closeMethod string, err error) {
	if page == nil {
		return "", fmt.Errorf("页面不存在")
	}

	if err := page.Actor().Keyboard.Press(input.Escape); err != nil {
		return "", fmt.Errorf("Escape 关闭笔记面板失败: %w", err)
	}
	if closed, err := noteOverlayClosedAfterAttempt(page); err != nil {
		return "", err
	} else if closed {
		return "escape", nil
	}

	return "", fmt.Errorf("Escape 后笔记面板未关闭")
}

func noteOverlayClosedAfterAttempt(page *hrod.Page) (bool, error) {
	deadline := time.Now().Add(noteCloseProbeTimeout)
	for {
		visibleCount, err := probePanelVisible(page)
		if err != nil {
			return false, err
		}
		if visibleCount == 0 {
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		time.Sleep(noteCloseProbeInterval)
	}
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

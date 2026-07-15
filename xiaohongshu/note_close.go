package xiaohongshu

import (
	"fmt"
	"strings"
	"time"

	"github.com/go-rod/rod/lib/input"
	"github.com/sirupsen/logrus"
	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

const noteCloseProbeDelay = 500 * time.Millisecond

func closeNoteOverlay(page *hrod.Page, sourceURL string) (closeMethod string, err error) {
	if page == nil {
		return "", fmt.Errorf("页面不存在")
	}

	urlResult, err := page.Eval(`() => location.href`)
	if err != nil {
		return "", fmt.Errorf("读取 URL 失败: %w", err)
	}
	if strings.Contains(urlResult.Value.Str(), "/explore/") {
		if _, err := page.Eval(`() => history.back()`); err != nil {
			return "", fmt.Errorf("history.back 失败: %w", err)
		}
		deadline := time.Now().Add(browseSessionRefreshTimeout)
		for time.Now().Before(deadline) {
			if err := page.Err(); err != nil {
				return "", err
			}
			currentResult, err := page.Eval(`() => location.href`)
			if err != nil {
				return "", fmt.Errorf("读取 URL 失败: %w", err)
			}
			if isSearchResultPage(currentResult.Value.Str()) {
				return "history_back", nil
			}
			if err := page.Sleep(noteCloseProbeDelay); err != nil {
				return "", err
			}
		}
		return "", fmt.Errorf("history.back 后超时")
	}

	if err := page.Keyboard.Press(input.Escape); err != nil {
		logrus.Debugf("Escape 关闭笔记面板失败: %v", err)
	}
	if closed, err := noteOverlayClosedAfterAttempt(page); err != nil {
		return "", err
	} else if closed {
		return "escape", nil
	}

	if _, err := page.Eval(`() => document.body.click()`); err != nil {
		logrus.Debugf("点击 body 关闭面板失败: %v", err)
	} else if closed, err := noteOverlayClosedAfterAttempt(page); err != nil {
		return "", err
	} else if closed {
		return "body_click", nil
	}

	return "", fmt.Errorf("笔记面板关闭失败")
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

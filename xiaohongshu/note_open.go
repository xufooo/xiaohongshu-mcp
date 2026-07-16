package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/go-rod/rod/lib/proto"
	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

type NoteOpenAction struct {
	page  *hrod.Page
	state *ActionStateStore
}

func NewNoteOpenAction(page *hrod.Page) *NoteOpenAction {
	return &NoteOpenAction{page: page}
}

func NewNoteOpenActionWithState(page *hrod.Page, state *ActionStateStore) *NoteOpenAction {
	return &NoteOpenAction{page: page, state: state}
}

func (a *NoteOpenAction) OpenFromCards(ctx context.Context, feedID, xsecToken, source string) error {
	page := a.page.Context(ctx).Timeout(60 * time.Second)
	if source == "" {
		inferred, err := inferOpenSource(page)
		if err != nil {
			return fmt.Errorf("推断打开来源失败: %w", err)
		}
		source = inferred
	}

	if err := markFeedCard(page, feedID); err != nil {
		return err
	}
	anchor, err := page.Element(`[data-xhs-open-target="1"]`)
	if err != nil {
		return fmt.Errorf("未找到目标笔记 anchor: %w", err)
	}
	if err := anchor.ScrollIntoView(); err != nil {
		return fmt.Errorf("滚动到目标 anchor 失败: %w", err)
	}
	if err := page.SleepRandom(600*time.Millisecond, 1800*time.Millisecond); err != nil {
		return err
	}
	point, err := feedCardClickPoint(page)
	if err != nil {
		return err
	}
	if err := page.ClickPoint(point); err != nil {
		return fmt.Errorf("点击目标 anchor 失败: %w", err)
	}
	if err := waitFeedDetailVisible(page, feedID); err != nil {
		return err
	}
	if a.state != nil {
		_ = a.state.RecordOpen(feedID, source)
	}
	return nil
}

func markFeedCard(page *hrod.Page, feedID string) error {
	result, err := page.Eval(`(anchorSel, feedID) => {
		document.querySelectorAll('[data-xhs-open-target="1"]').forEach((el) => el.removeAttribute("data-xhs-open-target"));
		for (const a of document.querySelectorAll(anchorSel)) {
			if (typeof a.checkVisibility === 'function' ? !a.checkVisibility({ checkOpacity: true, checkVisibilityCSS: true }) : a.offsetParent === null) continue;
			const href = a.getAttribute('href') || '';
			if (href.includes(feedID) || (a.dataset && a.dataset.feedId && a.dataset.feedId.includes(feedID)) || a.outerHTML.includes(feedID)) {
				a.setAttribute("data-xhs-open-target", "1");
				return "ok";
			}
		}
		return "";
	}`, "section.note-item a.cover.mask.ld", feedID)
	if err != nil {
		return err
	}
	if result == nil || result.Value.Str() != "ok" {
		return fmt.Errorf("当前列表中没有 feed_id=%s 的可见 anchor", feedID)
	}
	return nil
}

type feedCardPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

func feedCardClickPoint(page *hrod.Page) (proto.Point, error) {
	result, err := page.Eval(`() => {
		const anchor = document.querySelector('[data-xhs-open-target="1"]');
		if (!anchor) return "";
		const rect = anchor.getBoundingClientRect();
		const visible = anchor.isConnected &&
			getComputedStyle(anchor).display !== "none" &&
			getComputedStyle(anchor).visibility !== "hidden" &&
			Number(getComputedStyle(anchor).opacity || "1") > 0 &&
			rect.width > 1 && rect.height > 1 &&
			rect.bottom > 0 && rect.right > 0 &&
			rect.top < window.innerHeight && rect.left < window.innerWidth;
		if (!visible) return "";
		const x = Math.min(Math.max(rect.left + rect.width / 2, 1), window.innerWidth - 1);
		const y = Math.min(Math.max(rect.top + rect.height / 2, 1), window.innerHeight - 1);
		const hit = document.elementFromPoint(x, y);
		if (!hit || (hit !== anchor && !anchor.contains(hit))) return "";
		return JSON.stringify({x, y});
	}`)
	if err != nil {
		return proto.Point{}, fmt.Errorf("读取目标 anchor 点击坐标失败: %w", err)
	}
	if result == nil || result.Value.Str() == "" {
		return proto.Point{}, fmt.Errorf("目标 anchor 当前不可原生点击")
	}

	var point feedCardPoint
	if err := json.Unmarshal([]byte(result.Value.Str()), &point); err != nil {
		return proto.Point{}, fmt.Errorf("解析目标 anchor 点击坐标失败: %w", err)
	}
	return proto.Point{X: point.X, Y: point.Y}, nil
}

func waitFeedDetailVisible(page *hrod.Page, feedID string) error {
	deadline := time.Now().Add(15 * time.Second)
	var last currentFeedDetailProbe
	var lastErr error

	for time.Now().Before(deadline) {
		if err := page.Err(); err != nil {
			return err
		}
		probe, err := probeCurrentFeedDetail(page, feedID)
		if err != nil {
			if !isTransientCurrentDetailProbeError(err) {
				return fmt.Errorf("等待笔记详情可见失败: %w", err)
			}
			lastErr = err
		} else {
			last = probe
			lastErr = nil
			if currentFeedDetailMatched(probe, feedID) {
				return nil
			}
		}
		if err := page.SleepRandom(300*time.Millisecond, 500*time.Millisecond); err != nil {
			return err
		}
	}
	if lastErr != nil {
		return fmt.Errorf("等待笔记详情可见失败: %w", lastErr)
	}
	return fmt.Errorf("等待笔记详情可见超时: url=%s url_matched=%v visible=%d visible_matched=%d state_matched=%v",
		last.URL, last.URLMatched, last.VisibleDetailCount, last.VisibleMatchedDetailCount, last.StateMatched)
}

func inferOpenSource(page *hrod.Page) (string, error) {
	result, err := page.Eval(`() => location.href`)
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", fmt.Errorf("location.href 未返回结果")
	}
	u := result.Value.Str()
	switch {
	case strings.Contains(u, "search"):
		return OpenSourceSearch, nil
	case strings.Contains(u, "explore"):
		return OpenSourceHome, nil
	default:
		return OpenSourceRecommend, nil
	}
}

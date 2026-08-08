package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/go-rod/rod/lib/proto"
	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
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

func (a *NoteOpenAction) OpenFromCards(ctx context.Context, counter *evalTimeoutCounter, feedID, xsecToken string) error {
	page := a.page.Context(ctx).Timeout(60 * time.Second)
	anchor, err := findFeedCardAnchor(ctx, page, counter, feedID)
	if err != nil {
		return err
	}
	if err := anchor.ScrollIntoView(); err != nil {
		return fmt.Errorf("滚动到目标 anchor 失败: %w", err)
	}
	if err := page.SleepRandom(600*time.Millisecond, 1800*time.Millisecond); err != nil {
		return err
	}
	point, err := feedCardClickPoint(anchor)
	if err != nil {
		return err
	}
	if err := page.ClickPoint(point); err != nil {
		return fmt.Errorf("点击目标 anchor 失败: %w", err)
	}
	if err := waitFeedDetailVisible(ctx, page, counter, feedID); err != nil {
		return err
	}
	return nil
}

// 只接受已连接且有尺寸的候选；零匹配报未找到，多匹配报歧义；Go 侧只调用一次 Elements 按索引取 anchor。
func findFeedCardAnchor(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter, feedID string) (*hrod.Element, error) {
	result, err := evalJS(ctx, counter, page, `(feedID) => {
		const anchors = Array.from(document.querySelectorAll("section.note-item a.cover.mask.ld"));
		const matches = [];
		anchors.forEach((a, index) => {
			const href = a.getAttribute("href") || "";
			const dataFeedID = a.dataset?.feedId || "";
			if (!(href.includes(feedID) || dataFeedID.includes(feedID))) return;
			if (!a.isConnected) return;
			const rect = a.getBoundingClientRect();
			if (rect.width <= 0 || rect.height <= 0) return;
			matches.push(index);
		});
		return JSON.stringify({ matches });
	}`, feedID)
	if err != nil {
		return nil, fmt.Errorf("扫描卡片 anchor 失败: %w", err)
	}
	if result == nil || strings.TrimSpace(result.Value.Str()) == "" {
		return nil, fmt.Errorf("当前列表中没有 feed_id=%s 的搜索结果 anchor", feedID)
	}
	var scan struct {
		Matches []int `json:"matches"`
	}
	if err := json.Unmarshal([]byte(result.Value.Str()), &scan); err != nil {
		return nil, fmt.Errorf("解析卡片 anchor 扫描结果失败: %w", err)
	}
	if len(scan.Matches) == 0 {
		return nil, fmt.Errorf("当前列表中没有 feed_id=%s 的搜索结果 anchor", feedID)
	}
	if len(scan.Matches) > 1 {
		return nil, fmt.Errorf("feed_id=%s 在列表中出现 %d 个匹配 anchor，拒绝歧义点击", feedID, len(scan.Matches))
	}

	anchors, err := page.Elements("section.note-item a.cover.mask.ld")
	if err != nil {
		return nil, fmt.Errorf("读取搜索结果 anchor 失败: %w", err)
	}
	index := scan.Matches[0]
	if index >= len(anchors) {
		return nil, fmt.Errorf("feed_id=%s anchor 索引越界: %d>=%d", feedID, index, len(anchors))
	}
	return anchors[index], nil
}

type feedCardPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

func feedCardClickPoint(anchor *hrod.Element) (proto.Point, error) {
	result, err := anchor.Eval(`() => {
		const a = this, r = a.getBoundingClientRect(), s = getComputedStyle(a);
		const c = a.isConnected;
		const v = c && s.display !== "none" && s.visibility !== "hidden" && +s.opacity > 0 && r.width > 1 && r.height > 1 && r.bottom > 0 && r.right > 0 && r.top < window.innerHeight && r.left < window.innerWidth;
		const x = Math.min(Math.max(r.left + r.width / 2, 1), window.innerWidth - 1);
		const y = Math.min(Math.max(r.top + r.height / 2, 1), window.innerHeight - 1);
		const d = (fr, hit) => JSON.stringify({
			target_connected: c, visible: v,
			rect: {left: r.left, top: r.top, width: r.width, height: r.height, bottom: r.bottom, right: r.right},
			viewport: {width: window.innerWidth, height: window.innerHeight},
			center_x: x, center_y: y,
			hit_tag: hit ? (hit.tagName || "").toLowerCase() : "",
			target_contains_hit: hit ? a.contains(hit) : false,
			failure_reason: fr
		});
		if (!v) return d("not_visible", null);
		const hit = document.elementFromPoint(x, y);
		if (!hit) return d("no_hit_element", null);
		if (!a.contains(hit)) return d("center_miss", hit);
		return JSON.stringify({x, y});
	}`)
	if err != nil {
		return proto.Point{}, fmt.Errorf("读取目标 anchor 点击坐标失败: %w", err)
	}
	if result == nil || result.Value.Str() == "" {
		return proto.Point{}, fmt.Errorf("目标 anchor 当前不可原生点击")
	}

	var diag struct {
		FailureReason string `json:"failure_reason"`
	}
	if err := json.Unmarshal([]byte(result.Value.Str()), &diag); err == nil && diag.FailureReason != "" {
		return proto.Point{}, fmt.Errorf("目标 anchor 当前不可原生点击: %s", result.Value.Str())
	}

	var point feedCardPoint
	if err := json.Unmarshal([]byte(result.Value.Str()), &point); err != nil {
		return proto.Point{}, fmt.Errorf("解析目标 anchor 点击坐标失败: %w", err)
	}

	return proto.Point{X: point.X, Y: point.Y}, nil
}

func waitFeedDetailVisible(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter, feedID string) error {
	deadline := time.Now().Add(15 * time.Second)
	var last currentFeedDetailProbe
	var lastErr error

	for time.Now().Before(deadline) {
		if err := page.Err(); err != nil {
			return err
		}
		probe, err := probeCurrentFeedDetail(ctx, page, counter, feedID)
		if err != nil {
			if IsFatalRendererError(err) {
				return err
			}
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

func inferOpenSource(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter) (string, error) {
	result, err := evalJS(ctx, counter, page, `() => location.href`)
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

package xiaohongshu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-rod/rod/lib/cdp"
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
		if strings.Contains(err.Error(), "scroll made no progress") {
			// SPA 列表重挂载可能导致 anchor 失效，等待后重取 anchor 重试一次。
			if err := page.SleepRandom(300*time.Millisecond, 800*time.Millisecond); err != nil {
				return fmt.Errorf("滚动到目标 anchor 失败: %w", err)
			}
			anchor, err = findFeedCardAnchor(ctx, page, counter, feedID)
			if err == nil {
				err = anchor.ScrollIntoView()
			}
			if err != nil {
				return fmt.Errorf("滚动到目标 anchor 失败: %w", err)
			}
		} else {
			return fmt.Errorf("滚动到目标 anchor 失败: %w", err)
		}
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

const (
	feedDetailVisibleWaitBudget    = 15 * time.Second
	feedDetailProbeAttemptBudget   = 2 * time.Second
)

func waitFeedDetailVisible(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter, feedID string) error {
	return waitFeedDetailVisibleWith(ctx, feedID, page.Err,
		func(probeCtx context.Context) (currentFeedDetailProbe, error) {
			return probeCurrentFeedDetail(probeCtx, page, feedID)
		}, func(sleepCtx context.Context, min, max time.Duration) error {
			return page.Context(sleepCtx).SleepRandom(min, max)
		})
}

func waitFeedDetailVisibleWith(
	ctx context.Context,
	feedID string,
	pageErr func() error,
	probe func(context.Context) (currentFeedDetailProbe, error),
	sleep func(context.Context, time.Duration, time.Duration) error,
) error {
	deadline := time.Now().Add(feedDetailVisibleWaitBudget)
	waitCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	var last currentFeedDetailProbe
	var lastErr error
	consecutiveMatches := 0

	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := pageErr(); err != nil {
			return err
		}
		attemptCtx, attemptCancel := context.WithTimeout(waitCtx, feedDetailProbeAttemptBudget)
		probeResult, err := probe(attemptCtx)
		attemptCancel()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			consecutiveMatches = 0
			if IsFatalRendererError(err) {
				return err
			}
			if !isTransientCurrentDetailProbeError(err) {
				return newDetailVisibilityError("permanent_error", err)
			}
			lastErr = err
		} else {
			last = probeResult
			lastErr = nil
			if currentFeedDetailMatched(probeResult, feedID) {
				consecutiveMatches++
				if consecutiveMatches >= 2 {
					if time.Now().Before(deadline) {
						return nil
					}
					break
				}
			} else {
				consecutiveMatches = 0
			}
		}
		if !time.Now().Before(deadline) {
			break
		}
		if err := sleep(waitCtx, 300*time.Millisecond, 500*time.Millisecond); err != nil {
			if errors.Is(err, context.DeadlineExceeded) && !time.Now().Before(deadline) {
				break
			}
			return err
		}
	}
	if lastErr != nil {
		return newDetailVisibilityError("transient_error", lastErr)
	}
	return newDetailVisibilityError("unmatched", nil, last)
}

type DetailVisibilityError struct {
	probeTerminal             string
	urlMatched                string
	visibleDetailCount        string
	visibleMatchedDetailCount string
	stateMatched              string
	probeError                string
	cause                     error
}

func (e *DetailVisibilityError) Error() string {
	return "等待笔记详情可见失败"
}

func (e *DetailVisibilityError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *DetailVisibilityError) Diagnostic() string {
	if e == nil || e.probeTerminal == "" || e.urlMatched == "" || e.visibleDetailCount == "" || e.visibleMatchedDetailCount == "" || e.stateMatched == "" || e.probeError == "" {
		return ""
	}
	return fmt.Sprintf("probe_terminal=%s probe_error=%s url_matched=%s visible_detail_count=%s visible_matched_detail_count=%s state_matched=%s",
		e.probeTerminal, e.probeError, e.urlMatched, e.visibleDetailCount, e.visibleMatchedDetailCount, e.stateMatched)
}

func newDetailVisibilityError(terminal string, cause error, probes ...currentFeedDetailProbe) error {
	visibility := &DetailVisibilityError{
		probeTerminal:             terminal,
		urlMatched:                "unknown",
		visibleDetailCount:        "unknown",
		visibleMatchedDetailCount: "unknown",
		stateMatched:              "unknown",
		probeError:                detailProbeErrorCategory(terminal, cause),
		cause:                     cause,
	}
	if len(probes) == 1 {
		probe := probes[0]
		visibility.urlMatched = boolCategory(probe.URLMatched)
		visibility.visibleDetailCount = countCategory(probe.VisibleDetailCount)
		visibility.visibleMatchedDetailCount = countCategory(probe.VisibleMatchedDetailCount)
		visibility.stateMatched = boolCategory(probe.StateMatched)
	}
	return visibility
}

func detailProbeErrorCategory(terminal string, err error) string {
	if terminal != "transient_error" {
		return "unknown"
	}
	if err == nil {
		return "unknown"
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "context_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "context_deadline"
	case errors.Is(err, errCurrentDetailEvalTimeout):
		return "eval_timeout"
	case errors.Is(err, cdp.ErrCtxNotFound), errors.Is(err, cdp.ErrCtxDestroyed):
		return "execution_context_destroyed"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "execution context was destroyed"), strings.Contains(message, "cannot find context with specified id"):
		return "execution_context_destroyed"
	case strings.Contains(message, "context canceled"):
		return "context_canceled"
	case isEvalTimeout(err):
		return "eval_timeout"
	default:
		return "other_transient"
	}
}

func boolCategory(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func countCategory(value int) string {
	switch {
	case value <= 0:
		return "0"
	case value == 1:
		return "1"
	default:
		return "many"
	}
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

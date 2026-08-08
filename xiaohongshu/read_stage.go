package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-rod/rod/lib/proto"
	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
)

// contentMetrics 只保留实际参与阅读算法的图片数。
type contentMetrics struct {
	Images int `json:"images"`
}

type carouselReadState struct {
	contentMetrics
	ActiveIndex int `json:"activeIndex"`
}

type ReadStageAction struct {
	page  *hrod.Page
	state *ActionStateStore
}

func NewReadStageAction(page *hrod.Page, state *ActionStateStore) *ReadStageAction {
	return &ReadStageAction{page: page, state: state}
}

// CalculateDynamicDuration uses the visible title/body and real (de-duplicated)
// swiper image count. It deliberately excludes comments: comments are loaded by
// the detail API only when requested.
func (a *ReadStageAction) CalculateDynamicDuration(ctx context.Context) (time.Duration, error) {
	counter := &evalTimeoutCounter{}
	metrics, err := readContentMetrics(ctx, a.page.Context(ctx), counter)
	if err != nil {
		return 0, err
	}
	return dynamicReadDuration(metrics), nil
}

func dynamicReadDuration(m contentMetrics) time.Duration {
	if m.Images <= 1 {
		return 5 * time.Second
	}
	if m.Images >= 3 {
		return 10 * time.Second
	}
	// About two seconds for title/body, then about 2.5 seconds for each of the
	// first real image pages. We do not turn through an unbounded album.
	return 2*time.Second + time.Duration(m.Images)*2500*time.Millisecond
}

// Read reads title/body and, for a multi-image note, physically advances at
// most two pages using the report-verified .swiper-slide interaction. It never
// scrolls comments. A positive minDuration is an explicit caller lower bound;
// an unset duration uses the content-aware default above.
func (a *ReadStageAction) Read(ctx context.Context, feedID string, minDuration time.Duration) error {
	counter := &evalTimeoutCounter{}
	return a.read(ctx, counter, feedID, minDuration)
}

func (a *ReadStageAction) read(ctx context.Context, counter *evalTimeoutCounter, feedID string, minDuration time.Duration) error {
	if a.state == nil {
		return nil
	}
	page := a.page.Context(ctx)
	probe, err := carouselReadProbe(ctx, page, counter)
	if err != nil {
		return err
	}
	if minDuration <= 0 {
		minDuration = dynamicReadDuration(probe.contentMetrics)
	}

	start := time.Now()
	if err := page.SleepRandom(1*time.Second, 2*time.Second); err != nil {
		return err
	}
	if err := scrollNoteScroller(ctx, page, counter, 160); err != nil {
		return err
	}

	// 初始 probe 同时保留 Images 与 ActiveIndex。每次切换只用一次 Promise Eval 等
	// active index 变化，不再重复完整 carousel probe。
	activeIndex := probe.ActiveIndex
	for turn := 0; turn < minInt(probe.Images-1, 2); turn++ {
		if activeIndex < 0 {
			break
		}
		next, err := advanceCarouselRight(ctx, page, counter, activeIndex)
		if err != nil {
			break
		}
		activeIndex = next
		if err := page.SleepRandom(2*time.Second, 3*time.Second); err != nil {
			return err
		}
	}

	// 剩余停留一次可取消 Sleep 完成，不再 500ms 循环。
	if remaining := minDuration - time.Since(start); remaining > 0 {
		if err := page.Sleep(remaining); err != nil {
			return err
		}
	}
	// 成功完成仅一次落盘，同时更新阅读时长、滚动次数与 LastReadAt；失败/取消不记录。
	return a.state.RecordReadStage(feedID, time.Since(start), 1)
}

func readContentMetrics(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter) (contentMetrics, error) {
	probe, err := carouselReadProbe(ctx, page, counter)
	if err != nil {
		return contentMetrics{}, err
	}
	return probe.contentMetrics, nil
}

func carouselReadProbe(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter) (carouselReadState, error) {
	result, err := evalJS(ctx, counter, page, carouselReadProbeScript())
	if err != nil {
		return carouselReadState{}, err
	}
	if result == nil || result.Value.Str() == "" {
		return carouselReadState{}, fmt.Errorf("读取笔记内容指标时 JS 返回空")
	}
	var probe carouselReadState
	if err := json.Unmarshal([]byte(result.Value.Str()), &probe); err != nil {
		return carouselReadState{}, fmt.Errorf("解析笔记内容指标失败: %w", err)
	}
	return probe, nil
}

func carouselReadProbeScript() string {
	return `() => {
		const indices = new Set();
		document.querySelectorAll(".swiper-slide").forEach((slide) => {
			const index = slide.getAttribute("data-swiper-slide-index");
			if (index !== null && index !== "") indices.add(index);
		});
		const active = document.querySelector(".swiper-slide-active");
		const activeIndex = Number.parseInt(active?.getAttribute("data-swiper-slide-index") || "-1", 10);
		return JSON.stringify({
			images: indices.size,
			activeIndex,
		});
	}`
}

func advanceCarouselRight(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter, previousIndex int) (int, error) {
	slide, err := page.Element(".swiper-slide-active")
	if err != nil {
		return previousIndex, fmt.Errorf("当前笔记图片轮播页不可用: %w", err)
	}
	point, err := carouselRightClickPoint(ctx, page, counter)
	if err != nil {
		return previousIndex, err
	}
	if err := page.ClickPoint(point); err != nil {
		return previousIndex, fmt.Errorf("点击图片轮播右侧失败: %w", err)
	}

	// 用一次可等待 Promise/MutationObserver Eval 等待 active index 变化，最长 2 秒，
	// 不再每 100~150ms 重复完整 carousel probe。
	result, err := evalJS(ctx, counter, page, `(previousIndex) => new Promise((resolve) => {
		const read = () => {
			const el = document.querySelector(".swiper-slide-active");
			return Number.parseInt(el?.getAttribute("data-swiper-slide-index") || "-1", 10);
		};
		let observer;
		const check = () => {
			const index = read();
			if (index >= 0 && index !== previousIndex) {
				observer?.disconnect();
				resolve(index);
				return true;
			}
			return false;
		};
		if (!check()) {
			observer = new MutationObserver(check);
			observer.observe(document.body, { childList: true, subtree: true, attributes: true, attributeFilter: ["data-swiper-slide-index", "class"] });
		}
		setTimeout(() => { observer?.disconnect(); resolve(-1); }, 2000);
	})`, previousIndex)
	if err != nil {
		return previousIndex, err
	}
	index := -1
	if result != nil {
		index = int(result.Value.Int())
	}
	if index < 0 {
		return previousIndex, fmt.Errorf("图片轮播页未从 index=%d 切换", previousIndex)
	}
	return index, nil
}

func carouselRightClickPoint(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter) (proto.Point, error) {
	result, err := evalJS(ctx, counter, page, `() => {
			const slide = document.querySelector(".swiper-slide-active");
			if (!slide) return "";
			const r = slide.getBoundingClientRect();
			const x = Math.min(Math.max(r.left + r.width * 0.8, 1), window.innerWidth - 1);
			const y = Math.min(Math.max(r.top + r.height / 2, 1), window.innerHeight - 1);
			const hit = document.elementFromPoint(x, y);
			if (!slide.isConnected || r.width <= 1 || r.height <= 1 || !hit || !slide.contains(hit)) return "";
		return JSON.stringify({x, y});
	}`)
	if err != nil || result == nil || result.Value.Str() == "" {
		return proto.Point{}, fmt.Errorf("当前图片轮播页不可原生点击")
	}
	var point struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	}
	if err := json.Unmarshal([]byte(result.Value.Str()), &point); err != nil {
		return proto.Point{}, fmt.Errorf("解析图片轮播点击坐标失败: %w", err)
	}
	return proto.Point{X: point.X, Y: point.Y}, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ReadMin guarantees at least minDuration. It is for explicit caller requests;
// BrowseSession.Read intentionally calls Read with zero for the fast default.
func (a *ReadStageAction) ReadMin(ctx context.Context, feedID string, minDuration time.Duration) error {
	counter := &evalTimeoutCounter{}
	return a.readMin(ctx, counter, feedID, minDuration)
}

func (a *ReadStageAction) readMin(ctx context.Context, counter *evalTimeoutCounter, feedID string, minDuration time.Duration) error {
	if a.state == nil || minDuration <= 0 {
		return nil
	}
	return a.read(ctx, counter, feedID, minDuration)
}

// RecordCommentDwell 由 FeedDetail 在评论阅读累计时调用，保留为薄 wrapper。
func (a *ReadStageAction) RecordCommentDwell(feedID string, duration time.Duration, scrolled bool) {
	if a.state != nil {
		_ = a.state.RecordCommentDwell(feedID, duration, scrolled)
	}
}

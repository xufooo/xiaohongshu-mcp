package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-rod/rod/lib/proto"
	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

type contentMetrics struct {
	TitleLen int  `json:"titleLen"`
	DescLen  int  `json:"descLen"`
	Images   int  `json:"images"`
	Comments int  `json:"comments"`
	HasVideo bool `json:"hasVideo"`
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
	metrics, err := readContentMetrics(a.page.Context(ctx))
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
	if a.state == nil {
		return nil
	}
	page := a.page.Context(ctx)
	metrics, err := readContentMetrics(page)
	if err != nil {
		return err
	}
	if minDuration <= 0 {
		minDuration = dynamicReadDuration(metrics)
	}

	start := time.Now()
	if err := page.SleepRandom(1*time.Second, 2*time.Second); err != nil {
		return err
	}
	if err := scrollNoteScroller(page, 160); err != nil {
		return err
	}
	_ = a.state.RecordFeedScroll(feedID, 1)

	// The Phase 2 browser report verified a right-side click on .swiper-slide
	// advances 0 -> 1 -> 2. Confirm the active data-swiper-slide-index changes
	// after every click before accounting for a viewed page.
	for turn := 0; turn < minInt(metrics.Images-1, 2); turn++ {
		before, err := carouselReadProbe(page)
		if err != nil || before.ActiveIndex < 0 {
			break
		}
		if _, err := advanceCarouselRight(page, before.ActiveIndex); err != nil {
			break
		}
		if err := page.SleepRandom(2*time.Second, 3*time.Second); err != nil {
			return err
		}
	}

	for time.Since(start) < minDuration {
		remaining := minDuration - time.Since(start)
		pause := 500 * time.Millisecond
		if remaining < pause {
			pause = remaining
		}
		if err := page.SleepRandom(pause, pause); err != nil {
			return err
		}
	}
	return a.state.RecordRead(feedID, time.Since(start))
}

func readContentMetrics(page *hrod.Page) (contentMetrics, error) {
	probe, err := carouselReadProbe(page)
	if err != nil {
		return contentMetrics{}, err
	}
	return probe.contentMetrics, nil
}

func carouselReadProbe(page *hrod.Page) (carouselReadState, error) {
	result, err := page.Eval(carouselReadProbeScript())
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
		const clean = (v) => (v || "").trim();
		const title = clean(document.querySelector("#detail-title")?.innerText || document.querySelector(".title")?.innerText || "");
		const desc = clean(document.querySelector("#detail-desc")?.innerText || document.querySelector(".note-text")?.innerText || document.querySelector(".desc")?.innerText || "");
		const indices = new Set();
		document.querySelectorAll(".swiper-slide").forEach((slide) => {
			const index = slide.getAttribute("data-swiper-slide-index");
			if (index !== null && index !== "") indices.add(index);
		});
		const active = document.querySelector(".swiper-slide-active");
		const activeIndex = Number.parseInt(active?.getAttribute("data-swiper-slide-index") || "-1", 10);
		const fallbackImages = document.querySelectorAll(".note-content img, .media-container img, .carousel img").length;
		return JSON.stringify({
			titleLen: title.length,
			descLen: desc.length,
			images: indices.size || (fallbackImages > 0 ? 1 : 0),
			comments: 0,
			hasVideo: !!document.querySelector("video"),
			activeIndex,
		});
	}`
}

func advanceCarouselRight(page *hrod.Page, previousIndex int) (int, error) {
	slide, err := page.Element(".swiper-slide-active")
	if err != nil {
		return previousIndex, fmt.Errorf("当前笔记图片轮播页不可用: %w", err)
	}
	point, err := carouselRightClickPoint(slide)
	if err != nil {
		return previousIndex, err
	}
	if err := page.ClickPoint(point); err != nil {
		return previousIndex, fmt.Errorf("点击图片轮播右侧失败: %w", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		probe, err := carouselReadProbe(page)
		if err == nil && probe.ActiveIndex >= 0 && probe.ActiveIndex != previousIndex {
			return probe.ActiveIndex, nil
		}
		if err := page.SleepRandom(100*time.Millisecond, 150*time.Millisecond); err != nil {
			return previousIndex, err
		}
	}
	return previousIndex, fmt.Errorf("图片轮播页未从 index=%d 切换", previousIndex)
}

func carouselRightClickPoint(slide *hrod.Element) (proto.Point, error) {
	result, err := slide.Eval(`() => {
		const r = this.getBoundingClientRect();
		const x = Math.min(Math.max(r.left + r.width * 0.8, 1), window.innerWidth - 1);
		const y = Math.min(Math.max(r.top + r.height / 2, 1), window.innerHeight - 1);
		const hit = document.elementFromPoint(x, y);
		if (!this.isConnected || r.width <= 1 || r.height <= 1 || !hit || !this.contains(hit)) return "";
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
	if a.state == nil || minDuration <= 0 {
		return nil
	}
	return a.Read(ctx, feedID, minDuration)
}

// DwellInComments remains an explicit, separate operation for callers that
// genuinely need comment-area dwell. Read must not call it.
func (a *ReadStageAction) DwellInComments(ctx context.Context, feedID string, minDuration time.Duration) error {
	if a.state == nil || minDuration <= 0 {
		return nil
	}
	page := a.page.Context(ctx)
	if err := scrollToCommentsArea(page); err != nil {
		return err
	}
	start := time.Now()
	scrolled := false
	for time.Since(start) < minDuration {
		if err := scrollNoteScroller(page, 120); err != nil {
			return err
		}
		scrolled = true
		if err := page.SleepRandom(3*time.Second, 6*time.Second); err != nil {
			return err
		}
	}
	return a.state.RecordCommentDwell(feedID, time.Since(start), scrolled)
}

func (a *ReadStageAction) RecordCommentDwell(feedID string, duration time.Duration, scrolled bool) {
	if a.state != nil {
		_ = a.state.RecordCommentDwell(feedID, duration, scrolled)
	}
}

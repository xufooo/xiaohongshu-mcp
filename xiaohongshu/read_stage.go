package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

type contentMetrics struct {
	TitleLen int  `json:"titleLen"`
	DescLen  int  `json:"descLen"`
	Images   int  `json:"images"`
	Comments int  `json:"comments"`
	HasVideo bool `json:"hasVideo"`
}

type ReadStageAction struct {
	page  *hrod.Page
	state *ActionStateStore
}

func NewReadStageAction(page *hrod.Page, state *ActionStateStore) *ReadStageAction {
	return &ReadStageAction{page: page, state: state}
}

// CalculateDynamicDuration 从 DOM 提取内容指标，计算推荐阅读时长。
// 基础：标题 3s + 正文每字 0.3s + 每张图片 3s + 视频 12s + 每评论 4s
func (a *ReadStageAction) CalculateDynamicDuration(ctx context.Context) (time.Duration, error) {
	page := a.page.Context(ctx)
	result, err := page.Eval(`() => {
		const clean = (v) => (v || "").trim();
		const title = clean(document.querySelector("#detail-title")?.innerText || document.querySelector(".title")?.innerText || "");
		const desc = clean(document.querySelector("#detail-desc")?.innerText || document.querySelector(".note-text")?.innerText || document.querySelector(".desc")?.innerText || "");
		const images = document.querySelectorAll(".swiper img, .note-content img, .media-container img, .carousel img").length;
		const comments = document.querySelectorAll(".parent-comment").length;
		const hasVideo = !!document.querySelector("video");
		return JSON.stringify({ titleLen: title.length, descLen: desc.length, images, comments, hasVideo });
	}`)
	if err != nil {
		return 0, err
	}
	if result == nil || result.Value.Str() == "" {
		return 0, fmt.Errorf("计算阅读时长时 JS 返回空")
	}

	var metrics contentMetrics
	if err := json.Unmarshal([]byte(result.Value.Str()), &metrics); err != nil {
		return 0, fmt.Errorf("解析内容指标失败: %w", err)
	}
	return dynamicReadDuration(metrics), nil
}

func dynamicReadDuration(m contentMetrics) time.Duration {
	d := 3 * time.Second
	chars := m.TitleLen + m.DescLen
	d += time.Duration(float64(time.Second) * 0.3 * float64(chars))
	d += time.Duration(m.Images) * 3 * time.Second
	if m.HasVideo {
		d += 12 * time.Second
	}
	d += time.Duration(m.Comments) * 4 * time.Second
	if d < 20*time.Second {
		d = 20 * time.Second
	}
	if d > 180*time.Second {
		d = 180 * time.Second
	}
	return d
}

// Read 模拟自然阅读流程：看标题 → 正文停顿 → 看图/视频 → 小幅滚动 → 浏览评论 → 思考停顿。
// 当 minDuration <= 0 时自动从 DOM 内容动态计算时长。
func (a *ReadStageAction) Read(ctx context.Context, feedID string, minDuration time.Duration) error {
	if a.state == nil {
		return nil
	}
	page := a.page.Context(ctx)

	if minDuration <= 0 {
		dyn, err := a.CalculateDynamicDuration(ctx)
		if err == nil && dyn > 0 {
			minDuration = dyn
		} else {
			minDuration = 30 * time.Second
		}
	}

	start := time.Now()

	// 阶段1：看标题（停留 1~3s）
	if err := page.SleepRandom(1*time.Second, 3*time.Second); err != nil {
		return err
	}

	// 阶段2：正文区域分次小幅滚动，每2~5秒滚动一次
	scrollStep := 160
	if minDuration > 60*time.Second {
		scrollStep = 280
	}
	for time.Since(start) < minDuration*2/3 {
		if err := scrollNoteScroller(page, float64(scrollStep)); err != nil {
			return err
		}
		_ = a.state.RecordFeedScroll(feedID, 1)
		if err := page.SleepRandom(2*time.Second, 5*time.Second); err != nil {
			return err
		}
		if time.Since(start) >= minDuration*2/3 {
			break
		}
		// 偶尔回看（10%概率）
		if time.Since(start) > minDuration/3 && time.Duration(time.Now().UnixNano())%10 == 0 {
			_ = scrollNoteScroller(page, float64(-scrollStep/2))
		}
	}

	// 阶段3：浏览评论区域（留出 1/3 时长）
	commentBudget := minDuration/3 + 5*time.Second
	commentDeadline := time.Now().Add(commentBudget)
	commentStart := time.Now()
	commentScrolled := false
	for time.Now().Before(commentDeadline) {
		if err := scrollNoteScroller(page, 100); err != nil {
			return err
		}
		commentScrolled = true
		_ = a.state.RecordFeedScroll(feedID, 1)
		if err := page.SleepRandom(2*time.Second, 4*time.Second); err != nil {
			return err
		}
	}
	_ = a.state.RecordCommentDwell(feedID, time.Since(commentStart), commentScrolled)

	// 阶段4：最终停顿思考（1~5s）
	if err := page.SleepRandom(1*time.Second, 5*time.Second); err != nil {
		return err
	}

	readDuration := time.Since(start)
	return a.state.RecordRead(feedID, readDuration)
}

// ReadMin 保证至少阅读 minDuration 时长，不动态计算。
// 适用于调用方已确定最短时间的场景（如 session 场景）。
func (a *ReadStageAction) ReadMin(ctx context.Context, feedID string, minDuration time.Duration) error {
	if a.state == nil || minDuration <= 0 {
		return nil
	}
	return a.Read(ctx, feedID, minDuration)
}

// DwellInComments 在评论区实际停留至少 minDuration，并记录评论区停留状态。
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

// RecordCommentDwell 记录评论区停留时长。scrolled 表示是否在评论区发生过滚动。
func (a *ReadStageAction) RecordCommentDwell(feedID string, duration time.Duration, scrolled bool) {
	if a.state != nil {
		_ = a.state.RecordCommentDwell(feedID, duration, scrolled)
	}
}

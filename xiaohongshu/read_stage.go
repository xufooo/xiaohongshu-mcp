package xiaohongshu

import (
	"context"
	"time"

	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

type ReadStageAction struct {
	page  *hrod.Page
	state *ActionStateStore
}

func NewReadStageAction(page *hrod.Page, state *ActionStateStore) *ReadStageAction {
	return &ReadStageAction{page: page, state: state}
}

func (a *ReadStageAction) Read(ctx context.Context, feedID string, minDuration time.Duration) error {
	if a.state == nil || minDuration <= 0 {
		return nil
	}
	page := a.page.Context(ctx)
	start := time.Now()

	if err := page.Actor().Mouse.Scroll(0, 280); err != nil {
		return err
	}
	_ = a.state.RecordFeedScroll(feedID, 1)

	for time.Since(start) < minDuration {
		if err := page.SleepRandom(2*time.Second, 5*time.Second); err != nil {
			return err
		}
		if time.Since(start) >= minDuration {
			break
		}
		if err := page.Actor().Mouse.Scroll(0, 160); err != nil {
			return err
		}
		_ = a.state.RecordFeedScroll(feedID, 1)
	}
	return a.state.RecordRead(feedID, time.Since(start))
}

func (a *ReadStageAction) RecordCommentDwell(feedID string, duration time.Duration, scrolled bool) {
	if a.state != nil {
		_ = a.state.RecordCommentDwell(feedID, duration, scrolled)
	}
}

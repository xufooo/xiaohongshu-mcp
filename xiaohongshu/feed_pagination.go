package xiaohongshu

import (
	"context"
	"fmt"
	"time"

	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

type FeedPageKind string

const (
	FeedPageHome   FeedPageKind = "home"
	FeedPageSearch FeedPageKind = "search"
)

type FeedCursor struct {
	Kind        FeedPageKind `json:"kind"`
	Keyword     string       `json:"keyword,omitempty"`
	FilterKey   string       `json:"filter_key,omitempty"`
	Round       int          `json:"round"`
	ReturnedIDs []string     `json:"returned_ids"`
	CreatedAt   time.Time    `json:"created_at"`
}

func (c *FeedCursor) Validate(kind FeedPageKind, keyword, filterKey string) error {
	if c == nil {
		return nil
	}
	if c.Kind != kind || c.Keyword != keyword || c.FilterKey != filterKey {
		return fmt.Errorf("feed cursor 与当前查询不匹配")
	}
	return nil
}

func feedKey(feed Feed) string {
	if feed.ID != "" {
		return feed.ID
	}
	return "card:" + feed.NoteCard.DisplayTitle + "|" + feed.NoteCard.User.UserID + "|" + feed.NoteCard.User.Nickname
}

func takeNewFeeds(feeds []Feed, cursor *FeedCursor, maxItems int) []Feed {
	seen := make(map[string]bool, len(cursor.ReturnedIDs)+len(feeds))
	for _, id := range cursor.ReturnedIDs {
		seen[id] = true
	}
	batch := make([]Feed, 0, maxItems)
	for _, feed := range feeds {
		key := feedKey(feed)
		if key == "card:||" || seen[key] {
			continue
		}
		seen[key] = true
		cursor.ReturnedIDs = append(cursor.ReturnedIDs, key)
		batch = append(batch, feed)
		if len(batch) == maxItems {
			break
		}
	}
	return batch
}

type feedPageOps struct {
	collect       func() ([]Feed, error)
	scroll        func() error
	waitForGrowth func(context.Context, map[string]bool) (grew bool, atEnd bool, err error)
}

func feedKeySet(feeds []Feed) map[string]bool {
	s := make(map[string]bool, len(feeds))
	for _, feed := range feeds {
		s[feedKey(feed)] = true
	}
	return s
}

func loadFeedBatchWithOps(ctx context.Context, cursor *FeedCursor, maxItems int, ops feedPageOps) ([]Feed, bool, error) {
	batch := make([]Feed, 0, maxItems)
	for len(batch) < maxItems {
		feeds, err := ops.collect()
		if err != nil && len(batch) == 0 {
			return nil, true, err
		}
		before := feedKeySet(feeds)
		batch = append(batch, takeNewFeeds(feeds, cursor, maxItems-len(batch))...)
		if len(batch) == maxItems {
			return batch, true, nil
		}
		if err := ctx.Err(); err != nil {
			return batch, true, err
		}
		if err := ops.scroll(); err != nil {
			if len(batch) > 0 {
				return batch, true, nil
			}
			return nil, true, err
		}
		cursor.Round++
		grew, atEnd, err := ops.waitForGrowth(ctx, before)
		if err != nil {
			return batch, true, err
		}
		if atEnd {
			return batch, false, nil
		}
		if !grew {
			return batch, false, nil
		}
	}
	return batch, true, nil
}

func LoadFeedBatch(ctx context.Context, page *hrod.Page, kind FeedPageKind, cursor *FeedCursor, maxItems int, collect func() ([]Feed, error)) ([]Feed, *FeedCursor, bool, error) {
	if cursor == nil {
		cursor = &FeedCursor{Kind: kind, CreatedAt: time.Now()}
	}
	if cursor.ReturnedIDs == nil {
		cursor.ReturnedIDs = make([]string, 0)
	}
	ops := feedPageOps{
		collect: collect,
		scroll: func() error {
			_, err := page.Actor().Mouse.Scroll(0, 700)
			return err
		},
		waitForGrowth: func(ctx context.Context, before map[string]bool) (bool, bool, error) {
			deadline := time.Now().Add(8 * time.Second)
			for time.Now().Before(deadline) {
				if err := ctx.Err(); err != nil {
					return false, false, err
				}
				feeds, err := ops.collect()
				if err != nil {
					return false, false, err
				}
				after := feedKeySet(feeds)
				for key := range after {
					if !before[key] {
						return true, false, nil
					}
				}
				atEnd := hasEndSignal(page)
				if atEnd {
					return false, true, nil
				}
				if err := page.Context(ctx).SleepRandom(300*time.Millisecond, 500*time.Millisecond); err != nil {
					return false, false, err
				}
			}
			return false, false, nil
		},
	}
	feeds, hasMore, err := loadFeedBatchWithOps(ctx, cursor, maxItems, ops)
	return feeds, cursor, hasMore, err
}

func hasEndSignal(page *hrod.Page) bool {
	result, err := page.Eval(`() => {
		const text = (document.body?.innerText || "").toLowerCase();
		if (/没有更多|到底了|no more|end/.test(text)) return true;
		if (document.querySelector('.end-container, .no-more')) return true;
		return false;
	}`)
	if err != nil || result == nil {
		return false
	}
	return result.Value.Bool()
}

package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/xpzouying/xiaohongshu-mcp/errors"
	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

type FeedsListAction struct {
	page *hrod.Page
}

func NewFeedsListAction(page *hrod.Page) (*FeedsListAction, error) {
	pp := page.Timeout(60 * time.Second)

	pp.MustNavigate("https://www.xiaohongshu.com")
	if err := WaitForXHSReady(pp, XHSReadyOptions{Kind: XHSReadyHome, Timeout: 60 * time.Second}); err != nil {
		return nil, err
	}

	return &FeedsListAction{page: pp}, nil
}

// GetFeedsList 获取页面的 Feed 列表数据
func (f *FeedsListAction) GetFeedsList(ctx context.Context) ([]Feed, error) {
	page := f.page.Context(ctx).Timeout(60 * time.Second)
	page.MustWait(`() => {
		const feed = window.__INITIAL_STATE__?.feed;
		const feeds = feed?.feeds;
		return Array.isArray(feeds?.value) || Array.isArray(feeds?._value);
	}`)

	result := page.MustEval(`() => {
		if (window.__INITIAL_STATE__ &&
		    window.__INITIAL_STATE__.feed &&
		    window.__INITIAL_STATE__.feed.feeds) {
			const feeds = window.__INITIAL_STATE__.feed.feeds;
			const feedsData = feeds.value !== undefined ? feeds.value : feeds._value;
			if (feedsData) {
				return JSON.stringify(feedsData);
			}
		}
		return "";
	}`).String()

	if result == "" {
		return nil, errors.ErrNoFeeds
	}

	var feeds []Feed
	if err := json.Unmarshal([]byte(result), &feeds); err != nil {
		return nil, fmt.Errorf("failed to unmarshal feeds: %w", err)
	}

	return feeds, nil
}

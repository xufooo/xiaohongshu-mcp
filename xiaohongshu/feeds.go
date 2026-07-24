package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-rod/rod"
	"github.com/xpzouying/xiaohongshu-mcp/errors"
	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

type FeedsListAction struct {
	page *hrod.Page
}

func NewFeedsListAction(page *hrod.Page) (*FeedsListAction, error) {
	pp := page.Timeout(60 * time.Second)

	if err := pp.Navigate("https://www.xiaohongshu.com"); err != nil {
		return nil, fmt.Errorf("navigate to home failed: %w", err)
	}
	if err := WaitForXHSReady(pp, XHSReadyOptions{Kind: XHSReadyHome, Timeout: 60 * time.Second}); err != nil {
		return nil, err
	}

	return &FeedsListAction{page: pp}, nil
}

func readHomeFeedsFromState(page *hrod.Page) ([]Feed, error) {
	resultObj, err := page.Eval(`() => {
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
	}`)
	if err != nil {
		return nil, fmt.Errorf("extract home feeds failed: %w", err)
	}
	result := ""
	if resultObj != nil {
		result = resultObj.Value.Str()
	}
	if result == "" {
		return nil, errors.ErrNoFeeds
	}
	var feeds []Feed
	if err := json.Unmarshal([]byte(result), &feeds); err != nil {
		return nil, fmt.Errorf("failed to unmarshal home feeds: %w", err)
	}
	return feeds, nil
}

func collectHomeFeeds(page *hrod.Page) ([]Feed, error) {
	domFeeds, domErr := ExtractSearchFeedsFromDOM(page)
	stateFeeds, stateErr := readHomeFeedsFromState(page)
	if domErr == nil && len(domFeeds) > 0 {
		return mergeFeedsByID(domFeeds, stateFeeds), nil
	}
	if stateErr == nil && len(stateFeeds) > 0 {
		return stateFeeds, nil
	}
	if domErr != nil {
		return nil, domErr
	}
	return nil, stateErr
}

// GetFeedsList 获取页面的 Feed 列表数据
func (f *FeedsListAction) GetFeedsList(ctx context.Context) ([]Feed, error) {
	page := f.page.Context(ctx).Timeout(60 * time.Second)
	if err := page.Wait(rod.Eval(`() => {
		const feed = window.__INITIAL_STATE__?.feed;
		const feeds = feed?.feeds;
		return Array.isArray(feeds?.value) || Array.isArray(feeds?._value);
	}`)); err != nil {
		return nil, fmt.Errorf("wait for feeds failed: %w", err)
	}
	return collectHomeFeeds(page)
}

package xiaohongshu

import (
	"context"
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
		source = inferOpenSource(page)
	}

	if err := markFeedCard(page, feedID); err != nil {
		return err
	}
	card, err := page.Element(`[data-xhs-open-target="1"]`)
	if err != nil {
		return fmt.Errorf("未找到目标笔记卡片: %w", err)
	}
	if err := card.ScrollIntoView(); err != nil {
		return fmt.Errorf("滚动到目标卡片失败: %w", err)
	}
	if err := page.SleepRandom(600*time.Millisecond, 1800*time.Millisecond); err != nil {
		return err
	}
	if err := card.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("点击目标卡片失败: %w", err)
	}
	if err := waitFeedDetailVisible(page, feedID); err != nil {
		return err
	}
	if a.state != nil {
		_ = a.state.RecordOpen(feedID, source)
	}
	return nil
}

func (a *NoteOpenAction) OpenByURLFallback(ctx context.Context, feedID, xsecToken string) error {
	page := a.page.Context(ctx).Timeout(60 * time.Second)
	page.MustNavigate(makeFeedDetailURL(feedID, xsecToken)).MustWaitLoad()
	if err := waitFeedDetailVisible(page, feedID); err != nil {
		return err
	}
	if a.state != nil {
		_ = a.state.RecordOpen(feedID, OpenSourceDetailURLFallback)
	}
	return nil
}

func markFeedCard(page *hrod.Page, feedID string) error {
	result, err := page.Eval(`(selector, feedID) => {
		document.querySelectorAll('[data-xhs-open-target="1"]').forEach((el) => el.removeAttribute("data-xhs-open-target"));
		const cards = Array.from(document.querySelectorAll(selector));
		const target = cards.find((card) => {
			const data = JSON.stringify(card.dataset || {});
			const href = Array.from(card.querySelectorAll("a[href]")).map((a) => a.href).join(" ");
			const text = card.innerText || card.textContent || "";
			return data.includes(feedID) || href.includes(feedID) || text.includes(feedID);
		});
		if (!target) return "";
		target.setAttribute("data-xhs-open-target", "1");
		return "ok";
	}`, SelectorFeedCard, feedID)
	if err != nil {
		return err
	}
	if result == nil || result.Value.Str() != "ok" {
		return fmt.Errorf("当前列表中没有 feed_id=%s 的可见卡片", feedID)
	}
	return nil
}

func waitFeedDetailVisible(page *hrod.Page, feedID string) error {
	deadline := time.Now().Add(15 * time.Second).UnixMilli()
	page.MustWait(`(feedID, selector, deadline) => {
		const hrefMatched = location.href.includes(feedID);
		const ready = document.querySelector(selector) !== null;
		const map = window.__INITIAL_STATE__?.note?.noteDetailMap;
		const hasState = map && Object.prototype.hasOwnProperty.call(map, feedID);
		return hrefMatched || ready || hasState || Date.now() >= deadline;
	}`, feedID, SelectorFeedDetailReady, deadline)
	return nil
}

func inferOpenSource(page *hrod.Page) string {
	u := page.MustEval(`() => location.href`).String()
	switch {
	case strings.Contains(u, "search"):
		return OpenSourceSearch
	case strings.Contains(u, "explore"):
		return OpenSourceHome
	default:
		return OpenSourceRecommend
	}
}

package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	myerrors "github.com/xpzouying/xiaohongshu-mcp/errors"
)

// ActionResult 通用动作响应（点赞/收藏等）
type ActionResult struct {
	FeedID  string `json:"feed_id"`
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// 选择器常量
const (
	SelectorLikeButton    = ".interact-container .left .like-lottie"
	SelectorCollectButton = ".interact-container .left .reds-icon.collect-icon"
)

// interactActionType 交互动作类型
type interactActionType string

const (
	actionLike       interactActionType = "点赞"
	actionFavorite   interactActionType = "收藏"
	actionUnlike     interactActionType = "取消点赞"
	actionUnfavorite interactActionType = "取消收藏"
)

type interactAction struct {
	page  *hrod.Page
	state *ActionStateStore
}

func newInteractAction(page *hrod.Page) *interactAction {
	return &interactAction{page: page}
}

func newInteractActionWithState(page *hrod.Page, state *ActionStateStore) *interactAction {
	return &interactAction{page: page, state: state}
}

func (a *interactAction) preparePage(ctx context.Context, actionType interactActionType, feedID, xsecToken string) (*hrod.Page, error) {
	page := a.page.Context(ctx).Timeout(60 * time.Second)
	if a.state != nil {
		if err := a.state.ValidateInteraction(feedID, interactionValidationAction(actionType)); err != nil {
			return nil, fmt.Errorf("%s前置校验失败: %w", actionType, err)
		}
		if !isCurrentFeedDetail(page, feedID) {
			return nil, fmt.Errorf("%s前置校验失败: 当前页面不是最近打开的笔记 %s", actionType, feedID)
		}
		return page, nil
	}

	url := makeFeedDetailURL(feedID, xsecToken)
	logrus.Infof("Opening feed detail page for %s: %s", actionType, url)

	page.MustNavigate(url)
	if err := WaitForXHSReady(page, XHSReadyOptions{Kind: XHSReadyDetail, FeedID: feedID}); err != nil {
		return nil, err
	}
	if err := page.Sleep(time.Second); err != nil {
		return nil, err
	}

	return page, nil
}

func interactionValidationAction(actionType interactActionType) string {
	switch actionType {
	case actionLike, actionUnlike:
		return "like"
	case actionFavorite, actionUnfavorite:
		return "favorite"
	default:
		return ""
	}
}

func isCurrentFeedDetail(page *hrod.Page, feedID string) bool {
	return page.MustEval(`(feedID) => {
		const map = window.__INITIAL_STATE__?.note?.noteDetailMap;
		return location.href.includes(feedID) || Boolean(map && Object.prototype.hasOwnProperty.call(map, feedID));
	}`, feedID).Bool()
}

func (a *interactAction) performClick(page *hrod.Page, selector string) {
	element := page.MustElement(selector)
	element.MustClick()
}

// LikeAction 负责处理点赞相关交互
type LikeAction struct {
	*interactAction
}

func NewLikeAction(page *hrod.Page) *LikeAction {
	return &LikeAction{interactAction: newInteractAction(page)}
}

func NewLikeActionWithState(page *hrod.Page, state *ActionStateStore) *LikeAction {
	return &LikeAction{interactAction: newInteractActionWithState(page, state)}
}

// Like 点赞指定笔记，如果已点赞则直接返回
func (a *LikeAction) Like(ctx context.Context, feedID, xsecToken string) error {
	return a.perform(ctx, feedID, xsecToken, true)
}

// Unlike 取消点赞指定笔记，如果未点赞则直接返回
func (a *LikeAction) Unlike(ctx context.Context, feedID, xsecToken string) error {
	return a.perform(ctx, feedID, xsecToken, false)
}

func (a *LikeAction) perform(ctx context.Context, feedID, xsecToken string, targetLiked bool) error {
	actionType := actionLike
	if !targetLiked {
		actionType = actionUnlike
	}

	page, err := a.preparePage(ctx, actionType, feedID, xsecToken)
	if err != nil {
		return err
	}

	liked, _, err := a.getInteractState(page, feedID)
	if err != nil {
		return fmt.Errorf("读取点赞状态失败，取消点击: %w", err)
	}

	if targetLiked && liked {
		logrus.Infof("feed %s already liked, skip clicking", feedID)
		return nil
	}
	if !targetLiked && !liked {
		logrus.Infof("feed %s not liked yet, skip clicking", feedID)
		return nil
	}

	return a.toggleLike(page, feedID, targetLiked, actionType)
}

func (a *LikeAction) toggleLike(page *hrod.Page, feedID string, targetLiked bool, actionType interactActionType) error {
	a.performClick(page, SelectorLikeButton)
	if err := page.Sleep(3 * time.Second); err != nil {
		return err
	}

	liked, _, err := a.getInteractState(page, feedID)
	if err != nil {
		return fmt.Errorf("state_unknown: 验证%s状态失败，取消立即二次点击: %w", actionType, err)
	}
	if liked == targetLiked {
		logrus.Infof("feed %s %s成功", feedID, actionType)
		if a.state != nil {
			_ = a.state.RecordInteraction(feedID, interactionValidationAction(actionType))
		}
		return nil
	}

	return fmt.Errorf("state_unknown: %s后状态未确认，取消立即二次点击", actionType)
}

// FavoriteAction 负责处理收藏相关交互
type FavoriteAction struct {
	*interactAction
}

func NewFavoriteAction(page *hrod.Page) *FavoriteAction {
	return &FavoriteAction{interactAction: newInteractAction(page)}
}

func NewFavoriteActionWithState(page *hrod.Page, state *ActionStateStore) *FavoriteAction {
	return &FavoriteAction{interactAction: newInteractActionWithState(page, state)}
}

// Favorite 收藏指定笔记，如果已收藏则直接返回
func (a *FavoriteAction) Favorite(ctx context.Context, feedID, xsecToken string) error {
	return a.perform(ctx, feedID, xsecToken, true)
}

// Unfavorite 取消收藏指定笔记，如果未收藏则直接返回
func (a *FavoriteAction) Unfavorite(ctx context.Context, feedID, xsecToken string) error {
	return a.perform(ctx, feedID, xsecToken, false)
}

func (a *FavoriteAction) perform(ctx context.Context, feedID, xsecToken string, targetCollected bool) error {
	actionType := actionFavorite
	if !targetCollected {
		actionType = actionUnfavorite
	}

	page, err := a.preparePage(ctx, actionType, feedID, xsecToken)
	if err != nil {
		return err
	}

	_, collected, err := a.getInteractState(page, feedID)
	if err != nil {
		return fmt.Errorf("读取收藏状态失败，取消点击: %w", err)
	}

	if targetCollected && collected {
		logrus.Infof("feed %s already favorited, skip clicking", feedID)
		return nil
	}
	if !targetCollected && !collected {
		logrus.Infof("feed %s not favorited yet, skip clicking", feedID)
		return nil
	}

	return a.toggleFavorite(page, feedID, targetCollected, actionType)
}

func (a *FavoriteAction) toggleFavorite(page *hrod.Page, feedID string, targetCollected bool, actionType interactActionType) error {
	a.performClick(page, SelectorCollectButton)
	if err := page.Sleep(3 * time.Second); err != nil {
		return err
	}

	_, collected, err := a.getInteractState(page, feedID)
	if err != nil {
		return fmt.Errorf("state_unknown: 验证%s状态失败，取消立即二次点击: %w", actionType, err)
	}
	if collected == targetCollected {
		logrus.Infof("feed %s %s成功", feedID, actionType)
		if a.state != nil {
			_ = a.state.RecordInteraction(feedID, interactionValidationAction(actionType))
		}
		return nil
	}

	return fmt.Errorf("state_unknown: %s后状态未确认，取消立即二次点击", actionType)
}

// getInteractState 优先从渲染后的按钮状态读取，失败时降级到 __INITIAL_STATE__。
func (a *interactAction) getInteractState(page *hrod.Page, feedID string) (liked bool, collected bool, err error) {
	if liked, collected, err := ExtractInteractStateFromDOM(page, feedID); err == nil {
		return liked, collected, nil
	}

	result := page.MustEval(`() => {
		if (window.__INITIAL_STATE__ &&
		    window.__INITIAL_STATE__.note &&
		    window.__INITIAL_STATE__.note.noteDetailMap) {
			return JSON.stringify(window.__INITIAL_STATE__.note.noteDetailMap);
		}
		return "";
	}`).String()
	if result == "" {
		return false, false, myerrors.ErrNoFeedDetail
	}

	// 直接解析为 noteDetailMap
	var noteDetailMap map[string]struct {
		Note struct {
			InteractInfo struct {
				Liked     bool `json:"liked"`
				Collected bool `json:"collected"`
			} `json:"interactInfo"`
		} `json:"note"`
	}
	if err := json.Unmarshal([]byte(result), &noteDetailMap); err != nil {
		return false, false, errors.Wrap(err, "unmarshal noteDetailMap failed")
	}

	detail, ok := noteDetailMap[feedID]
	if !ok {
		return false, false, fmt.Errorf("feed %s not in noteDetailMap", feedID)
	}
	return detail.Note.InteractInfo.Liked, detail.Note.InteractInfo.Collected, nil
}

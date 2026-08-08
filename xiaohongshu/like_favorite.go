package xiaohongshu

import (
	"context"
	"fmt"
	"time"

	"github.com/go-rod/rod/lib/proto"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/humanize"
	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
)

// ActionResult 通用动作响应（点赞/收藏等）
type ActionResult struct {
	FeedID  string `json:"feed_id"`
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// 选择器常量
const (
	SelectorLikeButton    = ".interact-container .left .like-wrapper"
	SelectorCollectButton = ".interact-container .left .collect-wrapper"
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

func (a *interactAction) preparePage(ctx context.Context, counter *evalTimeoutCounter, actionType interactActionType, feedID, xsecToken string) (*hrod.Page, error) {
	if err := validateFeedAccessArgs(feedID, xsecToken); err != nil {
		return nil, fmt.Errorf("%s失败: %w", actionType, err)
	}

	page := a.page.Context(ctx).Timeout(60 * time.Second)
	if a.state != nil {
		if err := a.state.ValidateInteraction(feedID, interactionValidationAction(actionType)); err != nil {
			return nil, fmt.Errorf("%s前置校验失败: %w", actionType, err)
		}
		ok, err := isCurrentFeedDetail(ctx, page, counter, feedID)
		if err != nil {
			return nil, fmt.Errorf("%s前置校验失败: 检查当前笔记失败: %w", actionType, err)
		}
		if !ok {
			return nil, fmt.Errorf("%s前置校验失败: 当前页面不是最近打开的笔记 %s", actionType, feedID)
		}
		return page, nil
	}

	url := makeFeedDetailURL(feedID, xsecToken)
	logrus.Infof("Opening feed detail page for %s: %s", actionType, redactSensitiveURL(url))

	if err := page.Navigate(url); err != nil {
		return nil, fmt.Errorf("%s打开 feed 详情页失败: %w", actionType, err)
	}
	if err := WaitForXHSReady(page, XHSReadyOptions{Kind: XHSReadyDetail, FeedID: feedID}); err != nil {
		return nil, err
	}
	humanize.Delay(ctx, humanize.AfterNavigate)

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

func isCurrentFeedDetail(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter, feedID string) (bool, error) {
	probe, err := probeCurrentFeedDetail(ctx, page, counter, feedID)
	if err != nil {
		return false, err
	}
	return currentFeedDetailMatched(probe, feedID), nil
}

func (a *interactAction) performClick(page *hrod.Page, selector string) error {
	element, err := page.Element(selector)
	if err != nil {
		return err
	}
	return element.Click(proto.InputMouseButtonLeft, 1)
}

// interactActionSpec 描述一次读写交互动作所需的差异化信息；公共流程只依赖这些字段。
type interactActionSpec struct {
	actionType interactActionType
	selector   string
	// target 表示点击后期望达到的布尔状态。
	target bool
	// stateOf 从实时互动状态中选择本动作关注的字段。
	stateOf func(liked, collected bool) bool
}

// performInteraction 统一执行"阅读 -> 准备页面 -> 读取状态 -> 幂等跳过/至多一次点击并校验"。
func (a *interactAction) performInteraction(ctx context.Context, spec interactActionSpec, feedID, xsecToken string) error {
	counter := &evalTimeoutCounter{}
	var page *hrod.Page
	var err error
	if a.state != nil {
		page = a.page.Context(ctx).Timeout(60 * time.Second)
		reader := NewReadStageAction(page, a.state)
		if err := reader.readMin(ctx, counter, feedID, 20*time.Second); err != nil {
			return fmt.Errorf("%s前阅读阶段失败: %w", spec.actionType, err)
		}
		page, err = a.preparePage(ctx, counter, spec.actionType, feedID, xsecToken)
	} else {
		page, err = a.preparePage(ctx, counter, spec.actionType, feedID, xsecToken)
	}
	if err != nil {
		return err
	}

	liked, collected, err := a.getInteractState(ctx, page, counter, feedID)
	if err != nil {
		return fmt.Errorf("读取%s状态失败，取消点击: %w", spec.actionType, err)
	}
	current := spec.stateOf(liked, collected)
	if spec.target && current {
		logrus.Infof("feed %s 已处于目标状态，跳过点击", feedID)
		return nil
	}
	if !spec.target && !current {
		logrus.Infof("feed %s 未处于目标状态，跳过点击", feedID)
		return nil
	}

	return a.toggleInteractionOnce(ctx, page, counter, feedID, spec)
}

// toggleInteractionOnce 统一执行至多一次点击：点击 -> 拟人停留 -> 校验进入目标状态。
// 状态未确认时返回 state_unknown，取消立即二次点击。
func (a *interactAction) toggleInteractionOnce(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter, feedID string, spec interactActionSpec) error {
	if err := a.performClick(page, spec.selector); err != nil {
		return fmt.Errorf("%s点击按钮失败: %w", spec.actionType, err)
	}
	if err := page.SleepRandom(2*time.Second, 5*time.Second); err != nil {
		return err
	}

	liked, collected, err := a.getInteractState(ctx, page, counter, feedID)
	if err != nil {
		return fmt.Errorf("state_unknown: 验证%s状态失败，取消立即二次点击: %w", spec.actionType, err)
	}
	if spec.stateOf(liked, collected) == spec.target {
		logrus.Infof("feed %s %s成功", feedID, spec.actionType)
		if a.state != nil {
			_ = a.state.RecordInteraction(feedID, interactionValidationAction(spec.actionType))
		}
		humanize.Delay(ctx, humanize.AfterInteract)
		return nil
	}

	return fmt.Errorf("state_unknown: %s后状态未确认，取消立即二次点击", spec.actionType)
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
	spec := interactActionSpec{
		actionType: actionLike,
		selector:   SelectorLikeButton,
		target:     targetLiked,
		stateOf:    func(liked, _ bool) bool { return liked },
	}
	if !targetLiked {
		spec.actionType = actionUnlike
	}
	return a.performInteraction(ctx, spec, feedID, xsecToken)
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
	spec := interactActionSpec{
		actionType: actionFavorite,
		selector:   SelectorCollectButton,
		target:     targetCollected,
		stateOf:    func(_, collected bool) bool { return collected },
	}
	if !targetCollected {
		spec.actionType = actionUnfavorite
	}
	return a.performInteraction(ctx, spec, feedID, xsecToken)
}

// getInteractState 仅从渲染后的互动按钮读取状态；无法确认时返回错误。
func (a *interactAction) getInteractState(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter, feedID string) (bool, bool, error) {
	return ExtractInteractStateFromDOM(ctx, page, counter, feedID)
}

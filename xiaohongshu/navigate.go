package xiaohongshu

import (
	"context"
	"fmt"

	"github.com/go-rod/rod/lib/proto"
	"github.com/xpzouying/xiaohongshu-mcp/humanize"
	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
)

type NavigateAction struct {
	page *hrod.Page
}

func NewNavigate(page *hrod.Page) *NavigateAction {
	return &NavigateAction{page: page}
}

func (n *NavigateAction) ToExplorePage(ctx context.Context) error {
	page := n.page.Context(ctx)
	if err := page.Navigate("https://www.xiaohongshu.com/explore"); err != nil {
		return fmt.Errorf("导航到发现页失败: %w", err)
	}
	if err := WaitForXHSReady(page, XHSReadyOptions{Kind: XHSReadyHome}); err != nil {
		return err
	}
	if _, err := page.Element(`div#app`); err != nil {
		return fmt.Errorf("等待发现页应用容器失败: %w", err)
	}
	return nil
}

// profileEntrySelector 侧栏「我」入口。线上实测该选择器会命中 2 个节点：
// 1 个可见 [64,523,16,18] + 1 个 0×0 隐藏克隆（2026-09-20 内置浏览器实测）。
const profileEntrySelector = `div.main-container li.user.side-bar-component a.link-wrapper span.channel`

// findVisibleProfileEntry 在「我」入口里挑第一个可见的。
// 不能取第一个匹配：顺序无契约保证，取到 0×0 克隆会让点击在
// waitInteractable 里白等 5 秒后失败（慢机器上这 5 秒很贵）。
// 与通知入口（findVisibleNotificationEntry）保持同一策略。
func findVisibleProfileEntry(page *hrod.Page) (*hrod.Element, error) {
	elems, err := page.Elements(profileEntrySelector)
	if err != nil {
		return nil, fmt.Errorf("获取个人页入口失败: %w", err)
	}
	for _, elem := range elems {
		if isElementVisible(elem) {
			return elem, nil
		}
	}
	return nil, fmt.Errorf("个人页入口均不可见（命中 %d 个）", len(elems))
}

func (n *NavigateAction) ToProfilePage(ctx context.Context) error {
	page := n.page.Context(ctx)

	// First navigate to explore page
	if err := n.ToExplorePage(ctx); err != nil {
		return err
	}

	// Find and click the "我" channel link in sidebar
	profileLink, err := findVisibleProfileEntry(page)
	if err != nil {
		return err
	}
	humanize.Delay(ctx, humanize.BeforeClick)
	if err := profileLink.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("点击个人页入口失败: %w", err)
	}

	// Wait for navigation to complete
	if err := WaitForXHSReady(page, XHSReadyOptions{Kind: XHSReadyProfile}); err != nil {
		return err
	}

	return nil
}

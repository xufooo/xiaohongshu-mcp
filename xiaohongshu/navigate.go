package xiaohongshu

import (
	"context"
	"fmt"

	"github.com/go-rod/rod/lib/proto"
	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
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

func (n *NavigateAction) ToProfilePage(ctx context.Context) error {
	page := n.page.Context(ctx)

	// First navigate to explore page
	if err := n.ToExplorePage(ctx); err != nil {
		return err
	}

	// Find and click the "我" channel link in sidebar
	profileLink, err := page.Element(`div.main-container li.user.side-bar-component a.link-wrapper span.channel`)
	if err != nil {
		return fmt.Errorf("获取个人页入口失败: %w", err)
	}
	if err := profileLink.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("点击个人页入口失败: %w", err)
	}

	// Wait for navigation to complete
	if err := WaitForXHSReady(page, XHSReadyOptions{Kind: XHSReadyProfile}); err != nil {
		return err
	}

	return nil
}

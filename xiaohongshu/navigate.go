package xiaohongshu

import (
	"context"
	"fmt"
	"time"

	"github.com/go-rod/rod"
	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

// pageNavigationTimeout bounds the initial document bootstrap. XHS keeps
// background connections and continuously updates parts of the DOM, so waiting
// for the browser's load/stable signals can otherwise wait indefinitely.
const pageNavigationTimeout = 45 * time.Second

// navigateUntilReady navigates and waits for an application-level readiness
// condition. The timeout must be applied after Context: rod.Context replaces
// the page context and would otherwise discard a timeout set before it.
func navigateUntilReady(page *hrod.Page, ctx context.Context, url, readyJS string) (*hrod.Page, error) {
	p := page.Context(ctx).Timeout(pageNavigationTimeout)
	if err := p.Navigate(url); err != nil {
		return nil, fmt.Errorf("navigate to %s: %w", url, err)
	}
	if err := p.Wait(rod.Eval(readyJS)); err != nil {
		return nil, fmt.Errorf("wait for %s to become ready: %w", url, err)
	}
	return p, nil
}

type NavigateAction struct {
	page *hrod.Page
}

func NewNavigate(page *hrod.Page) *NavigateAction {
	return &NavigateAction{page: page}
}

func (n *NavigateAction) ToExplorePage(ctx context.Context) error {
	_, err := navigateUntilReady(n.page, ctx, "https://www.xiaohongshu.com/explore", `() => document.querySelector("div#app") !== null`)
	return err
}

func (n *NavigateAction) ToProfilePage(ctx context.Context) error {
	page := n.page.Context(ctx)

	// First navigate to explore page
	if err := n.ToExplorePage(ctx); err != nil {
		return err
	}

	page.MustWaitStable()

	// Find and click the "我" channel link in sidebar
	profileLink := page.MustElement(`div.main-container li.user.side-bar-component a.link-wrapper span.channel`)
	profileLink.MustClick()

	// Wait for navigation to complete
	page.MustWaitLoad()

	return nil
}

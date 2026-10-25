package xiaohongshu

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"

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

// 站点入口 URL：发现页与首页（feeds 列表）。
const (
	ExploreURL = "https://www.xiaohongshu.com/explore"
	HomeURL    = "https://www.xiaohongshu.com"
)

// navigationSkipped 记录「已在目标页因此跳过整页导航」的次数，
// 用于在真机上确认这条优化是否真的命中（默认不打印，读 get_page_state.browser）。
var navigationSkipped int64

// NavigationSkippedCount 返回跳过的整页导航次数。
func NavigationSkippedCount() int64 {
	return atomic.LoadInt64(&navigationSkipped)
}

// readyProbeTimeout 是「已在目标页」时的就绪确认预算：只做一次快速确认，
// 通过就跳过导航，未就绪则继续等待当前目标页。
const readyProbeTimeout = 5 * time.Second

// pageTargetInfo scopes only this browser-level query to the operation context.
// Page.Info in Rod v0.116.2 uses the creating Browser's context instead.
func pageTargetInfo(ctx context.Context, page *hrod.Page) (*proto.TargetTargetInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result, err := (proto.TargetGetTargetInfo{TargetID: page.Rod.TargetID}).Call(page.Rod.Browser().Context(ctx))
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if err != nil {
		return nil, err
	}
	if result == nil || result.TargetInfo == nil {
		return nil, fmt.Errorf("页面信息为空")
	}
	return result.TargetInfo, nil
}

// onURL 判断当前页是否已经是目标 URL（同 host、path 一致，忽略 query）。
func onURL(page *hrod.Page, target string) (bool, error) {
	info, err := pageTargetInfo(page.Rod.GetContext(), page)
	if err != nil {
		return false, err
	}
	current, err := url.Parse(info.URL)
	if err != nil {
		return false, nil
	}
	want, err := url.Parse(target)
	if err != nil {
		return false, nil
	}
	if !strings.EqualFold(current.Host, want.Host) {
		return false, nil
	}
	return strings.TrimRight(current.Path, "/") == strings.TrimRight(want.Path, "/"), nil
}

// onExplorePage 判断当前页是否已在 /explore。
// Pi 上一次全页导航是分钟级成本，已在目标页时不得重复导航。
func onExplorePage(page *hrod.Page) (bool, error) {
	return onURL(page, ExploreURL)
}

// Both routes are used by the tracked home-feed entry points.
func onHomeFeedPage(page *hrod.Page) (bool, error) {
	info, err := pageTargetInfo(page.Rod.GetContext(), page)
	if err != nil {
		return false, err
	}
	u, err := url.Parse(info.URL)
	if err != nil {
		return false, nil
	}
	path := strings.TrimRight(u.Path, "/")
	return u.Scheme == "https" && strings.EqualFold(u.Host, "www.xiaohongshu.com") && (path == "" || path == "/explore"), nil
}

// EnsureReadyOn 目标页已经就绪时跳过重复导航，否则导航过去再等就绪。
// 热页面复用（Manager.Release 保留页面）后，这条判断会经常命中：
// 例如 check_login_status 刚加载过 /explore，紧接着 start_page 不必再加载一次。
func EnsureReadyOn(page *hrod.Page, target string, kind XHSReadyKind, timeout time.Duration) error {
	ctx := page.Rod.GetContext()
	pageID := page.Rod.TargetID
	if err := ctx.Err(); err != nil {
		return err
	}
	alreadyOnTarget, err := onURL(page, target)
	if err != nil {
		return err
	}
	if alreadyOnTarget {
		quickStarted := time.Now()
		err := WaitForXHSReady(page, XHSReadyOptions{Kind: kind, Timeout: readyProbeTimeout})
		logrus.Debugf("ensure_ready target_id=%s phase=quick_confirm end elapsed_ms=%d executed=true ready=%t err=%v", pageID, time.Since(quickStarted).Milliseconds(), err == nil, err)
		if err == nil {
			if err := ctx.Err(); err != nil {
				return err
			}
			atomic.AddInt64(&navigationSkipped, 1)
			logrus.Infof("skip redundant navigation: already ready at %s", target)
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if isFatalWaitError(err) {
			return err
		}
		stillOnTarget, routeErr := onURL(page, target)
		if routeErr != nil {
			return routeErr
		}
		if stillOnTarget {
			if err := WaitForXHSReady(page, XHSReadyOptions{Kind: kind, Timeout: timeout}); err != nil {
				return err
			}
			atomic.AddInt64(&navigationSkipped, 1)
			return nil
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	navStarted := time.Now()
	if err := page.Navigate(target); err != nil {
		logrus.Debugf("ensure_ready target_id=%s phase=navigation end elapsed_ms=%d executed=true err=%v", pageID, time.Since(navStarted).Milliseconds(), err)
		return fmt.Errorf("navigate to %s failed: %w", target, err)
	}
	logrus.Debugf("ensure_ready target_id=%s phase=navigation end elapsed_ms=%d executed=true err=<nil>", pageID, time.Since(navStarted).Milliseconds())
	return WaitForXHSReady(page, XHSReadyOptions{Kind: kind, Timeout: timeout})
}

func (n *NavigateAction) ToProfilePage(ctx context.Context) error {
	page := n.page.Context(ctx)

	// 已在发现页时跳过重复导航（Pi 上一次全页加载是分钟级成本）。
	onExplore, err := onExplorePage(page)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !onExplore {
		if err := n.ToExplorePage(ctx); err != nil {
			return err
		}
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

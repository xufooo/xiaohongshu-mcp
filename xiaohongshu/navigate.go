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
// 通过就跳过导航，不通过就走正常导航路径。
const readyProbeTimeout = 5 * time.Second

// onURL 判断当前页是否已经是目标 URL（同 host、path 一致，忽略 query）。
func onURL(page *hrod.Page, target string) bool {
	info, err := page.Rod.Info()
	if err != nil || info == nil {
		return false
	}
	current, err := url.Parse(info.URL)
	if err != nil {
		return false
	}
	want, err := url.Parse(target)
	if err != nil {
		return false
	}
	if !strings.EqualFold(current.Host, want.Host) {
		return false
	}
	return strings.TrimRight(current.Path, "/") == strings.TrimRight(want.Path, "/")
}

// onExplorePage 判断当前页是否已在 /explore。
// Pi 上一次全页导航是分钟级成本，已在目标页时不得重复导航。
func onExplorePage(page *hrod.Page) bool {
	return onURL(page, ExploreURL)
}

// EnsureReadyOn 目标页已经就绪时跳过重复导航，否则导航过去再等就绪。
// 热页面复用（Manager.Release 保留页面）后，这条判断会经常命中：
// 例如 check_login_status 刚加载过 /explore，紧接着 start_page 不必再加载一次。
func EnsureReadyOn(page *hrod.Page, target string, kind XHSReadyKind, timeout time.Duration) error {
	if onURL(page, target) {
		if err := WaitForXHSReady(page, XHSReadyOptions{Kind: kind, Timeout: readyProbeTimeout}); err == nil {
			atomic.AddInt64(&navigationSkipped, 1)
			logrus.Infof("skip redundant navigation: already ready at %s", target)
			return nil
		}
	}
	if err := page.Navigate(target); err != nil {
		return fmt.Errorf("navigate to %s failed: %w", target, err)
	}
	return WaitForXHSReady(page, XHSReadyOptions{Kind: kind, Timeout: timeout})
}

func (n *NavigateAction) ToProfilePage(ctx context.Context) error {
	page := n.page.Context(ctx)

	// 已在发现页时跳过重复导航（Pi 上一次全页加载是分钟级成本）。
	if !onExplorePage(page) {
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

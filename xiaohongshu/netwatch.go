package xiaohongshu

import (
	"sync"
	"time"

	"github.com/go-rod/rod/lib/proto"
	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
)

// 页面网络活动观察：只订阅 CDP 的 Network 事件，不向页面注入任何脚本
// （注入会改变页面可见行为，有风控风险；CDP 事件对页面完全不可见）。
//
// 它解决的问题：等页面就绪时最耗时的一段是「机器正在取数据」。
// 这段时间页面没有 DOM 变化，于是任何「等多久」的判断都只能是猜——
// 同一台机器在负载 / 内存 / 温度不同时，这一段是 0.1s 还是 30s 都可能。
// 有了在途请求事件，等待就能这样写：有数据请求在途 → 一次探测都不做，
// 等事件把我们从等待里叫醒；请求落地 → 立刻探测。
// 等待时长于是由机器当时的状态决定，而不是由事先写下的秒数决定。
//
// 只统计会改变页面内容的请求（文档 / XHR / Fetch）：详情页 93% 的字节是图片，
// 把图片算进来只会让我们白等（也白白多收几百个事件）。
type networkActivity struct {
	mu       sync.Mutex
	active   map[proto.NetworkRequestID]time.Time // 在途数据请求 → 开始时刻
	lastSeen time.Time                            // 最近一次数据请求事件
}

func newNetworkActivity() *networkActivity {
	return &networkActivity{active: make(map[proto.NetworkRequestID]time.Time)}
}

func (n *networkActivity) touchedLocked(at time.Time) {
	n.lastSeen = at
}

func (n *networkActivity) started(id proto.NetworkRequestID, at time.Time) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.touchedLocked(at)
	if _, ok := n.active[id]; ok {
		return
	}
	n.active[id] = at
}

func (n *networkActivity) finished(id proto.NetworkRequestID, at time.Time) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.touchedLocked(at)
	delete(n.active, id)
}

// inFlight 返回当前在途的数据请求数。
func (n *networkActivity) inFlight() int {
	if n == nil {
		return 0
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.active)
}

// lastEvent 返回最近一次数据请求事件的时刻（零值表示还没见过）。
// 用途只有一个：判断"页面还活着吗"——慢机器只是慢，它的网络 / DOM 依然在动，
// 所以卡死判定看的是"毫无动静"，而不是"等了多久"。
func (n *networkActivity) lastEvent() time.Time {
	if n == nil {
		return time.Time{}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.lastSeen
}

// pageNetworks 按页面缓存观察器：一个页面一个，页面首次使用时建立。
var pageNetworks sync.Map // *rod.Page -> *networkActivity

// WatchPageNetwork 给页面挂上网络活动观察（幂等），页面创建后调用一次即可，
// 这样连文档请求本身都能被观察到。失败返回 nil：观察不到就退回原来的等待方式。
func WatchPageNetwork(page *hrod.Page) *networkActivity {
	if page == nil || page.Rod == nil {
		return nil
	}
	if cached, ok := pageNetworks.Load(page.Rod); ok {
		activity, _ := cached.(*networkActivity)
		return activity
	}

	activity := newNetworkActivity()
	actual, loaded := pageNetworks.LoadOrStore(page.Rod, activity)
	if loaded {
		cached, _ := actual.(*networkActivity)
		return cached
	}
	// 只打开事件通道，不拦改任何请求；页面关闭后事件流结束，协程自然退出。
	if err := (proto.NetworkEnable{}).Call(page.Rod); err != nil {
		pageNetworks.Delete(page.Rod)
		return nil
	}
	go activity.watch(page)
	return activity
}

// networkOf 取页面上已建立的观察器；没有（或建立失败）返回 nil。
func networkOf(page *hrod.Page) *networkActivity {
	if page == nil || page.Rod == nil {
		return nil
	}
	cached, ok := pageNetworks.Load(page.Rod)
	if !ok {
		return nil
	}
	activity, _ := cached.(*networkActivity)
	return activity
}

func (n *networkActivity) watch(page *hrod.Page) {
	page.Rod.Context(page.Rod.GetContext()).EachEvent(
		func(e *proto.NetworkRequestWillBeSent) {
			if !tracksRequestType(e.Type) {
				return
			}
			n.started(e.RequestID, time.Now())
		},
		func(e *proto.NetworkLoadingFinished) {
			n.finished(e.RequestID, time.Now())
		},
		func(e *proto.NetworkLoadingFailed) {
			n.finished(e.RequestID, time.Now())
		},
	)()
}

// tracksRequestType 只有会改变页面内容的请求才算「页面还在取数据」。
func tracksRequestType(t proto.NetworkResourceType) bool {
	switch t {
	case proto.NetworkResourceTypeDocument, proto.NetworkResourceTypeXHR, proto.NetworkResourceTypeFetch:
		return true
	default:
		return false
	}
}

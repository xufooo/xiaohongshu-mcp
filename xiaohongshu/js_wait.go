package xiaohongshu

import (
	"context"
	"encoding/json"
	"time"

	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
)

// xhsPageSignalJS 在页面内等待「DOM 变化后静默 settleMs」，最长 maxMs，返回结束原因。
//
// 为什么不用 Go 侧固定间隔轮询：每次轮询都是一次 CDP 往返 + 一次整页探测，
// 而检测时刻被钉在轮询网格上（实测合成页：300ms 网格的检测延迟 53ms、
// 5 次往返；页面内 25ms 自检只有 6ms、1 次往返）。等待越久差距越大——
// Pi 上 20–30s 的就绪等待，前者要 60–100 次往返。
//
// 换成"页面变化后再回来看"后：
//   - 页面在连续加载时，等它安静 settleMs 再看一次（一次探测就能判定就绪）；
//   - 页面卡住不动时，最多 maxMs 回来看一次（与原来最坏情况相当，不会更差）。
const xhsPageSignalJS = `
		(settleMs, maxMs) => new Promise((resolve) => {
			let settled = null;
			let hard = null;
			let finished = false;
			let mutations = 0;
			const finish = (reason) => {
				if (finished) return;
				finished = true;
				observer.disconnect();
				clearTimeout(settled);
				clearTimeout(hard);
				resolve(JSON.stringify({ reason, mutations }));
			};
			const observer = new MutationObserver(() => {
				if (finished) return;
				mutations++;
				clearTimeout(settled);
				settled = setTimeout(() => finish("settled"), settleMs);
			});
			const target = document.documentElement || document;
			observer.observe(target, {
				childList: true, subtree: true, attributes: true, characterData: true,
			});
			hard = setTimeout(() => finish("max"), maxMs);
		})
	`

// pageSignal 是一次「等页面变化」的结果：结束原因 + 这段时间里 DOM 变化次数。
// mutations=0 说明页面一动没动——结合"网络也没有在途请求"，就能判断页面是不是卡死了，
// 而不需要猜"它该用多久"。
type pageSignal struct {
	Reason    string `json:"reason"`
	Mutations int    `json:"mutations"`
}

// waitForPageSignal 让页面自己等到「变化后静默 settle」或最长 max。
// 返回错误说明连等待信号都拿不到（页面销毁 / 上下文取消），调用方按探测失败处理。
func waitForPageSignal(ctx context.Context, page *hrod.Page, settle, max time.Duration) (pageSignal, error) {
	if page == nil {
		return pageSignal{}, context.Canceled
	}
	waitCtx, cancel := context.WithTimeout(ctx, max+2*time.Second)
	defer cancel()
	obj, err := evalJSDirect(waitCtx, page, xhsPageSignalJS,
		int(settle.Milliseconds()), int(max.Milliseconds()))
	if err != nil {
		return pageSignal{}, err
	}
	if obj == nil {
		return pageSignal{}, nil
	}
	var signal pageSignal
	if err := json.Unmarshal([]byte(obj.Value.Str()), &signal); err != nil {
		return pageSignal{}, err
	}
	return signal, nil
}

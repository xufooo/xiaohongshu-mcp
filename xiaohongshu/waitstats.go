package xiaohongshu

import (
	"sort"
	"sync"
	"time"
)

// 等待统计与「失败上限」。
//
// 这里刻意**不**记录"应该等多久"：等待由事件决定（页面 DOM 变化、网络在途请求），
// 不预测时长——同一台机器每次打开的状态（负载 / 内存 / 温度）都不同，
// 预测出来的秒数只会在机器慢时误判超时、在机器快时白等。
//
// 保留的两件东西都是"观测量"而非"预测值"：
//   - WaitStat/WaitStatsSnapshot：真机上量到的次数与耗时，用于核对收敛；
//   - waitCeilings：各类等待的失败上限，只回答"多久还没成就别等了"，
//     取值刻意宽松——慢了只是慢，误判超时才是错的。

// waitSampleWindow 是近期样本窗口（够算 p90，又不占内存）。
const waitSampleWindow = 20

type WaitStat struct {
	Count   int64 `json:"count"`
	TotalMs int64 `json:"total_ms"`
	MaxMs   int64 `json:"max_ms"`
	// Probes 是这类等待里实际发生的页内探测次数之和（不适用时为 0）。
	// 用来直接对比「固定间隔轮询」与「事件驱动」的往返次数。
	Probes int64 `json:"probes,omitempty"`
}

var (
	waitStatsMu sync.Mutex
	waitStats   = map[string]*WaitStat{}
	waitSamples = map[string][]time.Duration{}
)

// observeWait 记录一次等待耗时；kind 形如 "ready:home_search"、"search_results"。
func observeWait(kind string, d time.Duration) {
	observeWaitWithProbes(kind, d, 0)
}

// observeWaitWithProbes 在耗时之外记录这次等待里的探测次数。
func observeWaitWithProbes(kind string, d time.Duration, probes int) {
	if kind == "" {
		return
	}
	ms := d.Milliseconds()
	waitStatsMu.Lock()
	stat := waitStats[kind]
	if stat == nil {
		stat = &WaitStat{}
		waitStats[kind] = stat
	}
	stat.Count++
	stat.TotalMs += ms
	stat.Probes += int64(probes)
	if ms > stat.MaxMs {
		stat.MaxMs = ms
	}
	ring := append(waitSamples[kind], d)
	if len(ring) > waitSampleWindow {
		ring = ring[len(ring)-waitSampleWindow:]
	}
	waitSamples[kind] = ring
	waitStatsMu.Unlock()
}

// waitPercentile 返回该类等待近期样本的 p 分位（样本不足时 ok=false）。
func waitPercentile(kind string, p float64) (time.Duration, bool) {
	waitStatsMu.Lock()
	samples := append([]time.Duration(nil), waitSamples[kind]...)
	waitStatsMu.Unlock()
	if len(samples) == 0 {
		return 0, false
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	idx := int(float64(len(samples)-1) * p)
	return samples[idx], true
}

// waitCeilings 登记所有「等条件成立」的等待的失败上限。
// 只登记条件等待；防封禁节奏（humanize 延时等）不在此列，那是特性。
var waitCeilings = map[string]time.Duration{
	"ready:home_search": 300 * time.Second,
	"ready:detail":      180 * time.Second,
	"ready:search":      180 * time.Second,
	"ready:home":        180 * time.Second,
	"ready:profile":     180 * time.Second,
	"ready:publish":     300 * time.Second,
	"ready:comment_box": 120 * time.Second,
	"search_results":    120 * time.Second,
	"publish_success":   180 * time.Second,
}

// waitCeilingForKind 返回该类等待的失败上限（未登记则 ok=false）。
func waitCeilingForKind(kind string) (time.Duration, bool) {
	ceiling, ok := waitCeilings[kind]
	return ceiling, ok
}

// pageSignalDebounce 是「等这一阵变化停下来」的合并窗（debounce）：
// 变化一来就重新计时，所以它不预测机器速度——窗短了只是多探测一两次，
// 而探测本身要花时间，天然就限了速（这就是不需要按探测成本放大它的原因）。
const pageSignalDebounce = 120 * time.Millisecond

// pageSignalWindow 给出「页面变化信号」的 (settle, max)：
//   - settle：DOM 停下来多久算这一阵结束（固定合并窗，见上）；
//   - max：页面一直不安静时的兜底节奏，按本机实测探测成本给（5×p90，clamp [0.5s, 3s]），
//     免得在慢机器上把时间花在探测上；没有样本时用调用方给的默认值。
func pageSignalWindow(kind string, fallbackMax time.Duration) (time.Duration, time.Duration) {
	p90, ok := waitPercentile("probe:"+kind, 0.9)
	if !ok {
		return pageSignalDebounce, fallbackMax
	}
	maxWait := 5 * p90
	if maxWait < 500*time.Millisecond {
		maxWait = 500 * time.Millisecond
	}
	if maxWait > 3*time.Second {
		maxWait = 3 * time.Second
	}
	return pageSignalDebounce, maxWait
}

// WaitStatsSnapshot 返回各类等待的观测快照（供 get_page_state.browser.waits 暴露）。
func WaitStatsSnapshot() map[string]WaitStat {
	waitStatsMu.Lock()
	defer waitStatsMu.Unlock()
	out := make(map[string]WaitStat, len(waitStats))
	for kind, stat := range waitStats {
		out[kind] = *stat
	}
	return out
}

package xiaohongshu

import (
	"sync"
	"time"
)

// WaitStat 是一类等待的观测量：次数、累计与最大耗时。
// 用途是**用数据校准预算**，而不是继续拍固定秒数（Pi 上先量出来，再改常量）。
type WaitStat struct {
	Count   int64 `json:"count"`
	TotalMs int64 `json:"total_ms"`
	MaxMs   int64 `json:"max_ms"`
}

var (
	waitStatsMu sync.Mutex
	waitStats   = map[string]*WaitStat{}
)

// observeWait 记录一次等待耗时；kind 形如 "ready:home_search"、"search_results"。
func observeWait(kind string, d time.Duration) {
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
	if ms > stat.MaxMs {
		stat.MaxMs = ms
	}
	waitStatsMu.Unlock()
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

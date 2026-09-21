package xiaohongshu

import (
	"testing"
	"time"
)

// isolateWaitStats 隔离全局观测状态，测试结束后恢复，避免污染其它用例。
func isolateWaitStats(t *testing.T) {
	t.Helper()
	waitStatsMu.Lock()
	savedStats, savedSamples := waitStats, waitSamples
	waitStats, waitSamples = map[string]*WaitStat{}, map[string][]time.Duration{}
	waitStatsMu.Unlock()
	t.Cleanup(func() {
		waitStatsMu.Lock()
		waitStats, waitSamples = savedStats, savedSamples
		waitStatsMu.Unlock()
	})
}

// 失败上限只是"别再等了"的线：必须存在且宽松，不能被观测值收紧
// （收紧就会在机器变慢时误判超时）。
func TestWaitCeilingsAreGenerousConstants(t *testing.T) {
	isolateWaitStats(t)

	for _, kind := range []string{"ready:detail", "ready:home_search"} {
		before, ok := waitCeilingForKind(kind)
		if !ok || before < time.Minute {
			t.Fatalf("%s 上限缺失或过紧: %v", kind, before)
		}
		// 灌入很慢的样本，上限也不能被"收敛"变小。
		for i := 0; i < waitSampleWindow; i++ {
			observeWait(kind, 30*time.Second)
		}
		if after, _ := waitCeilingForKind(kind); after != before {
			t.Fatalf("%s 上限随观测变化了: %v → %v", kind, before, after)
		}
	}

	if _, ok := waitCeilingForKind("net_gate:detail"); ok {
		t.Fatal("观测类等待不应登记失败上限")
	}
}

// 样本窗口只保留最近 waitSampleWindow 个：老数据不能永久影响节奏。
func TestWaitSamplesAreWindowed(t *testing.T) {
	isolateWaitStats(t)

	for i := 0; i < waitSampleWindow; i++ {
		observeWait("k", 10*time.Second) // 先灌满慢样本
	}
	for i := 0; i < waitSampleWindow; i++ {
		observeWait("k", time.Second) // 再灌满快样本，慢样本应被挤出
	}
	if p90, ok := waitPercentile("k", 0.9); !ok || p90 != time.Second {
		t.Fatalf("窗口未生效, p90 = %v", p90)
	}
}

// 页面信号窗口由本机探测成本推导：慢机器必须用更长的安静窗口。
func TestPageSignalWindowTracksProbeCost(t *testing.T) {
	isolateWaitStats(t)

	// 无样本：退回默认值。
	settle, maxWait := pageSignalWindow(XHSReadyDetail, 300*time.Millisecond, 3*time.Second)
	if settle != 300*time.Millisecond || maxWait != 3*time.Second {
		t.Fatalf("默认窗口 = %v/%v", settle, maxWait)
	}

	// 探测很便宜（x86，0.6ms）：clamp 到下限 120ms。
	for i := 0; i < waitSampleWindow; i++ {
		observeWait("probe:"+string(XHSReadyDetail), 600*time.Microsecond)
	}
	if settle, maxWait = pageSignalWindow(XHSReadyDetail, 300*time.Millisecond, 3*time.Second); settle != 120*time.Millisecond || maxWait != 360*time.Millisecond {
		t.Fatalf("快机窗口 = %v/%v, 期望 120ms/360ms", settle, maxWait)
	}

	// 探测很贵（Pi，50ms）：20×50ms = 1s。
	isolateWaitStats(t)
	for i := 0; i < waitSampleWindow; i++ {
		observeWait("probe:"+string(XHSReadyDetail), 50*time.Millisecond)
	}
	if settle, maxWait = pageSignalWindow(XHSReadyDetail, 300*time.Millisecond, 3*time.Second); settle != time.Second || maxWait != 3*time.Second {
		t.Fatalf("慢机窗口 = %v/%v, 期望 1s/3s", settle, maxWait)
	}
}

// 观测快照要能区分"等待次数"和"探测次数"，否则无法评估机制收益。
func TestWaitStatsSnapshotSeparatesProbes(t *testing.T) {
	isolateWaitStats(t)

	observeWaitWithProbes("ready:detail", 2*time.Second, 3)
	observeWaitWithProbes("ready:detail", 4*time.Second, 5)

	snap := WaitStatsSnapshot()["ready:detail"]
	if snap.Count != 2 || snap.Probes != 8 || snap.TotalMs != 6000 || snap.MaxMs != 4000 {
		t.Fatalf("快照 = %+v", snap)
	}
}

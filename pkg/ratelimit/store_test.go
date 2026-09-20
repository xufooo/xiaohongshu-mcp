package ratelimit

import (
	"testing"
	"time"
)

// TestPruneReportsChange 只在确有裁剪时报告变化：调用方据此跳过无效写盘（SD 卡写入放大）。
func TestPruneReportsChange(t *testing.T) {
	now := time.Now()

	fresh := &State{Actions: map[Action][]int64{ActionBrowse: {now.Unix()}}}
	if fresh.prune(now) {
		t.Fatal("窗口内事件不应报告变化")
	}

	stale := now.Add(-25 * time.Hour).Unix()
	expired := &State{
		Actions:     map[Action][]int64{ActionBrowse: {stale}},
		All:         []int64{stale},
		Interaction: []int64{stale},
		Write:       []int64{stale},
		Publish:     []int64{stale},
	}
	if !expired.prune(now) {
		t.Fatal("窗口外事件应报告变化")
	}
	if len(expired.Actions[ActionBrowse]) != 0 || len(expired.All) != 0 ||
		len(expired.Interaction) != 0 || len(expired.Write) != 0 || len(expired.Publish) != 0 {
		t.Fatalf("过期事件未被裁剪: %+v", expired)
	}

	// 第二次裁剪已无变化。
	if expired.prune(now) {
		t.Fatal("裁剪后再次 prune 不应报告变化")
	}
}

// TestFileStoreRoundTripCompactJSON 落盘为紧凑 JSON，仍可被正常读回。
func TestFileStoreRoundTripCompactJSON(t *testing.T) {
	account := AccountKey{AccountID: "test-account"}
	store, err := NewFileStore(t.TempDir(), account)
	if err != nil {
		t.Fatalf("NewFileStore 失败: %v", err)
	}

	now := time.Now()
	state := &State{Actions: map[Action][]int64{ActionBrowse: {now.Unix()}}, All: []int64{now.Unix()}}
	if err := store.Save(state); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if len(loaded.All) != 1 || loaded.All[0] != now.Unix() {
		t.Fatalf("往返后状态不一致: %+v", loaded)
	}
	if len(loaded.Actions[ActionBrowse]) != 1 {
		t.Fatalf("往返后 actions 不一致: %+v", loaded.Actions)
	}
}

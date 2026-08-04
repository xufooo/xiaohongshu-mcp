package xiaohongshu

import (
	"testing"
	"time"
)

// newTestActionStateStore 用真实临时路径构造 store，保证 Load/Save 持久化生效。
func newTestActionStateStore(t *testing.T) *ActionStateStore {
	t.Helper()
	store, err := NewActionStateStore(t.TempDir(), "test")
	if err != nil {
		t.Fatalf("NewActionStateStore 失败: %v", err)
	}
	return store
}

func TestValidateInteractionTarget(t *testing.T) {
	store := newTestActionStateStore(t)
	feedID := "feed1"
	if err := store.ValidateInteractionTarget(feedID); err == nil {
		t.Fatal("未打开笔记应失败")
	}
	if err := store.RecordOpen(feedID, "home"); err != nil {
		t.Fatalf("RecordOpen 失败: %v", err)
	}
	if err := store.ValidateInteractionTarget(feedID); err != nil {
		t.Fatalf("打开后应通过: %v", err)
	}
	if err := store.ValidateInteractionTarget("feed2"); err == nil {
		t.Fatal("feed 不匹配应失败")
	}
	// 冷却应失败
	if err := store.RecordRisk("test", time.Minute); err != nil {
		t.Fatalf("RecordRisk 失败: %v", err)
	}
	if err := store.ValidateInteractionTarget(feedID); err == nil {
		t.Fatal("冷却中应失败")
	}
}

func TestRecordCommentDwellCumulative(t *testing.T) {
	store := newTestActionStateStore(t)
	_ = store.RecordOpen("feed1", "home")
	_ = store.RecordCommentDwell("feed1", 20*time.Second, true)
	_ = store.RecordCommentDwell("feed1", 40*time.Second, false)
	state, err := store.Load()
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if state.CommentDwellTime != 60*time.Second {
		t.Fatalf("应累计 60s, 实际 %s", state.CommentDwellTime)
	}
	if state.CommentScrollCount != 1 {
		t.Fatalf("应累计 1 次滚动, 实际 %d", state.CommentScrollCount)
	}
	if state.LastReadAt.IsZero() {
		t.Fatal("有效停留应更新 LastReadAt")
	}
	// 不同 feed 不累计
	_ = store.RecordCommentDwell("feed2", 30*time.Second, true)
	state, _ = store.Load()
	if state.CommentDwellTime != 60*time.Second {
		t.Fatalf("不同 feed 不应累计, 实际 %s", state.CommentDwellTime)
	}
}

func TestValidateInteractionReplyThreshold(t *testing.T) {
	store := newTestActionStateStore(t)
	_ = store.RecordOpen("feed1", "home")
	if err := store.ValidateInteraction("feed1", "reply"); err == nil {
		t.Fatal("累计 0s 应失败")
	}
	_ = store.RecordCommentDwell("feed1", 59*time.Second, true)
	if err := store.ValidateInteraction("feed1", "reply"); err == nil {
		t.Fatal("累计 59 秒仍应失败")
	}
	_ = store.RecordCommentDwell("feed1", 1*time.Second, false)
	if err := store.ValidateInteraction("feed1", "reply"); err != nil {
		t.Fatalf("累计 60s 且至少一次确认滚动应通过: %v", err)
	}
}

func TestValidateInteractionCommentThreshold(t *testing.T) {
	store := newTestActionStateStore(t)
	_ = store.RecordOpen("feed1", "home")
	if err := store.ValidateInteraction("feed1", "comment"); err == nil {
		t.Fatal("未阅读应失败")
	}
	// 20 秒阅读且正文滚动
	_ = store.RecordRead("feed1", 20*time.Second)
	_ = store.RecordFeedScroll("feed1", 1)
	if err := store.ValidateInteraction("feed1", "comment"); err != nil {
		t.Fatalf("阅读 20s 且滚动后应通过: %v", err)
	}
}

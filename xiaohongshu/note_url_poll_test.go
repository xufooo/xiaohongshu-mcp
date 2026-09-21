package xiaohongshu

import (
	"context"
	"errors"
	"testing"
	"time"
)

// URL 连续若干次没变且不是笔记页时必须**立即**失败，而不是空等满超时
// （实测无效分享短链要等 60s 才报错）。
func TestNoteURLPollFailsOnceSettled(t *testing.T) {
	started := time.Now()
	_, err := waitForNoteURLStable(context.Background(), 60*time.Second, func(context.Context) (string, error) {
		return "https://www.xiaohongshu.com/404", nil
	})
	if err == nil {
		t.Fatal("非笔记页且 URL 已定型，应当报错")
	}
	elapsed := time.Since(started)
	if elapsed > 10*time.Second {
		t.Fatalf("应当在约 2s 内失败, 实际 %v", elapsed)
	}
	var pollErr *NoteURLPollError
	if !errors.As(err, &pollErr) {
		t.Fatalf("应保留 NoteURLPollError 以便诊断, got %v", err)
	}
	if pollErr.Diagnostic() == "" {
		t.Fatal("诊断信息不应为空")
	}
}

// URL 先变化后定型在笔记页：仍应正常返回（不能因为"变过"而误判）。
func TestNoteURLPollAcceptsNoteURLAfterRedirect(t *testing.T) {
	urls := []string{
		"https://xhslink.com/abc",
		"https://www.xiaohongshu.com/discovery/item/5f4d8e7b00000000010001a2?xsec_token=tok",
	}
	i := 0
	got, err := waitForNoteURLStable(context.Background(), 60*time.Second, func(context.Context) (string, error) {
		u := urls[i]
		if i < len(urls)-1 {
			i++
		}
		return u, nil
	})
	if err != nil {
		t.Fatalf("落到笔记页应成功: %v", err)
	}
	if got.NoteID != "5f4d8e7b00000000010001a2" {
		t.Fatalf("noteID = %q", got.NoteID)
	}
}

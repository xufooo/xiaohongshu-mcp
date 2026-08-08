package xiaohongshu

import "testing"

// canClickReplies 状态机测试：覆盖 Codex 复审要求的两个关键反例——
// replyStall=true + button!=nil、replyClicksTotal=8 + button!=nil 时不得继续点击。
func TestCanClickReplies(t *testing.T) {
	tests := []struct {
		name          string
		replyStall    bool
		clicked       int
		threshold     int
		want          bool
	}{
		{name: "正常可点击", replyStall: false, clicked: 0, threshold: 10, want: true},
		{name: "回复停滞不可点击", replyStall: true, clicked: 0, threshold: 10, want: false},
		{name: "8次上限不可点击", replyStall: false, clicked: 8, threshold: 10, want: false},
		{name: "超上限不可点击", replyStall: false, clicked: 9, threshold: 10, want: false},
		{name: "reply_limit=0不受8次限制", replyStall: false, clicked: 8, threshold: 0, want: true},
		{name: "reply_limit=-1停滞时仍不可点击", replyStall: true, clicked: 0, threshold: -1, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := canClickReplies(tt.replyStall, tt.clicked, tt.threshold); got != tt.want {
				t.Errorf("canClickReplies(%v, %d, %d) = %v, want %v", tt.replyStall, tt.clicked, tt.threshold, got, tt.want)
			}
		})
	}
}

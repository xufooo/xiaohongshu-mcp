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

func TestDecideCommentCompletion(t *testing.T) {
	full := CommentLoadConfig{ClickMoreReplies: true, MaxRepliesThreshold: 0}
	thresh := CommentLoadConfig{ClickMoreReplies: true, MaxRepliesThreshold: 10}
	none := CommentLoadConfig{ClickMoreReplies: false, MaxRepliesThreshold: 10}
	neg1 := CommentLoadConfig{ClickMoreReplies: true, MaxRepliesThreshold: -1}
	tests := []struct {
		name        string
		progress    commentProgress
		config      CommentLoadConfig
		totalItems  int
		seenCount   int
		hasMore     bool
		wantComp    bool
		wantReason  string
		wantHasMore bool
	}{
		{name: "无评论荒地", progress: commentProgress{NoComments: true, AtEnd: true}, config: full, wantComp: true, wantReason: ""},
		{name: "荒地但hasMore禁止complete", progress: commentProgress{NoComments: true, AtEnd: true}, config: full, hasMore: true, wantComp: false, wantReason: "more_comments_available"},
		{name: "父评论未到底", progress: commentProgress{AtEnd: false}, config: full, wantComp: false, wantReason: "parent_comments_not_at_end"},
		{name: "loader仍有更多禁止complete", progress: commentProgress{AtEnd: true}, config: full, totalItems: 133, seenCount: 133, hasMore: true, wantComp: false, wantReason: "more_comments_available"},
		{name: "不展开父评论到底", progress: commentProgress{AtEnd: true}, config: none, totalItems: 133, seenCount: 73, wantComp: false, wantReason: "reply_expansion_disabled"},
		{name: "reply_limit=-1一个都不展开", progress: commentProgress{AtEnd: true}, config: neg1, totalItems: 133, seenCount: 73, wantComp: false, wantReason: "reply_expansion_disabled"},
		{name: "阈值展开seen不足", progress: commentProgress{AtEnd: true}, config: thresh, totalItems: 133, seenCount: 73, wantComp: false, wantReason: "reply_limit_excludes_replies"},
		{name: "阈值展开seen足够", progress: commentProgress{AtEnd: true}, config: thresh, totalItems: 133, seenCount: 133, wantComp: true, wantReason: ""},
		{name: "全部展开seen不足强制续页", progress: commentProgress{AtEnd: true}, config: full, totalItems: 133, seenCount: 73, wantComp: false, wantReason: "seen_count_below_total_items", wantHasMore: true},
		{name: "全部展开seen足够", progress: commentProgress{AtEnd: true}, config: full, totalItems: 133, seenCount: 133, wantComp: true, wantReason: ""},
		{name: "全部展开total未知", progress: commentProgress{AtEnd: true}, config: full, totalItems: 0, seenCount: 10, wantComp: true, wantReason: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			comp, reason, forceHasMore := decideCommentCompletion(tt.progress, tt.config, tt.totalItems, tt.seenCount, tt.hasMore)
			if comp != tt.wantComp {
				t.Errorf("complete = %v, want %v", comp, tt.wantComp)
			}
			if reason != tt.wantReason {
				t.Errorf("incomplete_reason = %q, want %q", reason, tt.wantReason)
			}
			if forceHasMore != tt.wantHasMore {
				t.Errorf("forceHasMore = %v, want %v", forceHasMore, tt.wantHasMore)
			}
			if tt.hasMore || forceHasMore {
				if comp {
					t.Errorf("禁止组合: hasMore=%v forceHasMore=%v 但 complete=true", tt.hasMore, forceHasMore)
				}
				if reason == "" {
					t.Errorf("禁止组合: hasMore=%v forceHasMore=%v 但 incomplete_reason 为空", tt.hasMore, forceHasMore)
				}
			} else if !comp && reason == "" {
				t.Errorf("禁止组合: complete=false 但 incomplete_reason 为空")
			}
			if comp && reason != "" {
				t.Errorf("禁止组合: complete=true 但 incomplete_reason=%q", reason)
			}
		})
	}
}

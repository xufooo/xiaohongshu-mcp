package xiaohongshu

import "testing"

func TestDefaultCommentLoadConfigDoesNotLimitLoadAllComments(t *testing.T) {
	cfg := DefaultCommentLoadConfig()

	if cfg.MaxCommentItems != 0 {
		t.Fatalf("default MaxCommentItems should mean no explicit limit, got %d", cfg.MaxCommentItems)
	}

	loader := &commentLoader{config: cfg}
	if got := loader.calculateMaxAttempts(); got != defaultMaxAttempts {
		t.Fatalf("unlimited default should use defaultMaxAttempts, got %d", got)
	}
}

func TestCommentLoadConfigLimitControlsMaxAttempts(t *testing.T) {
	loader := &commentLoader{config: CommentLoadConfig{MaxCommentItems: 20}}

	if got := loader.calculateMaxAttempts(); got != 60 {
		t.Fatalf("expected limited attempts to scale by limit, got %d", got)
	}
}

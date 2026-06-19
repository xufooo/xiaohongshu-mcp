package humanize

import "testing"

func TestDefaultConfigMouseSpeedIsHumanScale(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Mouse.MoveSpeedPxPerSec < 200 || cfg.Mouse.MoveSpeedPxPerSec > 800 {
		t.Fatalf("default mouse speed should be human-scale, got %.0f px/s", cfg.Mouse.MoveSpeedPxPerSec)
	}

	if !(SlowConfig().Mouse.MoveSpeedPxPerSec < cfg.Mouse.MoveSpeedPxPerSec &&
		cfg.Mouse.MoveSpeedPxPerSec < FastConfig().Mouse.MoveSpeedPxPerSec) {
		t.Fatalf("expected slow < default < fast, got slow=%.0f default=%.0f fast=%.0f",
			SlowConfig().Mouse.MoveSpeedPxPerSec,
			cfg.Mouse.MoveSpeedPxPerSec,
			FastConfig().Mouse.MoveSpeedPxPerSec)
	}
}

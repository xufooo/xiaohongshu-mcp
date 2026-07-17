package xiaohongshu

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestApplyRiskPolicyUsesTieredCooldowns(t *testing.T) {
	tests := []struct {
		name        string
		kind        RiskKind
		want        time.Duration
		recoverable bool
	}{
		{name: "login expired", kind: RiskLoginExpired, want: 15 * time.Minute, recoverable: true},
		{name: "access anomaly", kind: RiskAccessAnomaly, want: 90 * time.Minute, recoverable: true},
		{name: "slider challenge", kind: RiskSliderChallenge, want: 2 * time.Hour, recoverable: true},
		{name: "captcha", kind: RiskCaptcha, want: 12 * time.Hour, recoverable: true},
		{name: "note not found", kind: RiskNoteNotFound, want: 0, recoverable: false},
		{name: "permission denied", kind: RiskPermissionDenied, want: 0, recoverable: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			signal := RiskSignal{Kind: test.kind}
			applyRiskPolicy(&signal)
			if signal.Cooldown != test.want {
				t.Fatalf("cooldown = %s, want %s", signal.Cooldown, test.want)
			}
			if signal.Recoverable != test.recoverable {
				t.Fatalf("recoverable = %v, want %v", signal.Recoverable, test.recoverable)
			}
		})
	}
}

func TestSliderRiskRequiresTextEvidence(t *testing.T) {
	source, err := os.ReadFile("risk.go")
	if err != nil {
		t.Fatalf("读取 risk.go 失败: %v", err)
	}
	text := string(source)
	if strings.Contains(text, `".slider"`) || strings.Contains(text, `[class*='slider']`) {
		t.Fatal("slider class 不能单独触发滑块风控")
	}
	if !strings.Contains(text, `keywords: ["滑块"],`) || !strings.Contains(text, `dom: []`) {
		t.Fatal("滑块风控必须保留文本证据且不使用通用 DOM class")
	}
}

package xiaohongshu

import (
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

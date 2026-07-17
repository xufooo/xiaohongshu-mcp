package xiaohongshu

import (
	"strings"
	"testing"
	"time"
)

func TestDynamicReadDurationUsesContentInsteadOfTwentySecondFloor(t *testing.T) {
	tests := []struct {
		name string
		m    contentMetrics
		want time.Duration
	}{
		{
			name: "text only",
			m:    contentMetrics{TitleLen: 8, DescLen: 80},
			want: 5 * time.Second,
		},
		{
			name: "one real image ignores swiper loop clones",
			m:    contentMetrics{TitleLen: 8, DescLen: 80, Images: 1},
			want: 5 * time.Second,
		},
		{
			name: "three real images allow two actual page turns",
			m:    contentMetrics{TitleLen: 8, DescLen: 80, Images: 3},
			want: 10 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dynamicReadDuration(tt.m); got != tt.want {
				t.Fatalf("dynamicReadDuration(%+v) = %s, want %s", tt.m, got, tt.want)
			}
		})
	}
}

func TestReadStageCarouselContractUsesVerifiedSwiperSlide(t *testing.T) {
	script := carouselReadProbeScript()
	for _, want := range []string{
		`.swiper-slide`,
		`data-swiper-slide-index`,
		`.swiper-slide-active`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("carousel probe is missing report-verified selector/attribute %q", want)
		}
	}
}

package xiaohongshu

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

type IdentityMetadata struct {
	Fingerprint        string    `json:"fingerprint"`
	UserAgent          string    `json:"user_agent,omitempty"`
	Platform           string    `json:"platform,omitempty"`
	Vendor             string    `json:"vendor,omitempty"`
	Languages          []string  `json:"languages,omitempty"`
	Timezone           string    `json:"timezone,omitempty"`
	ScreenWidth        int       `json:"screen_width,omitempty"`
	ScreenHeight       int       `json:"screen_height,omitempty"`
	ViewportWidth      int       `json:"viewport_width,omitempty"`
	ViewportHeight     int       `json:"viewport_height,omitempty"`
	DevicePixelRatio   float64   `json:"device_pixel_ratio,omitempty"`
	HardwareConcurrency int       `json:"hardware_concurrency,omitempty"`
	DeviceMemory       float64   `json:"device_memory,omitempty"`
	MaxTouchPoints      int       `json:"max_touch_points,omitempty"`
	Webdriver           bool      `json:"webdriver"`
	CreatedAt          time.Time `json:"created_at,omitempty"`
	CheckedAt          time.Time `json:"checked_at,omitempty"`
}

type IdentityDrift struct {
	Field    string `json:"field"`
	Before   string `json:"before,omitempty"`
	After    string `json:"after,omitempty"`
	Critical bool   `json:"critical"`
}

func CaptureIdentityMetadata(page *hrod.Page) (IdentityMetadata, error) {
	obj, err := page.Eval(`() => {
		const tz = (() => {
			try { return Intl.DateTimeFormat().resolvedOptions().timeZone || ""; } catch (_) { return ""; }
		})();
		return JSON.stringify({
			user_agent: navigator.userAgent || "",
			platform: navigator.platform || "",
			vendor: navigator.vendor || "",
			languages: Array.isArray(navigator.languages) ? navigator.languages.slice(0, 8) : [],
			timezone: tz,
			screen_width: screen?.width || 0,
			screen_height: screen?.height || 0,
			viewport_width: window.innerWidth || 0,
			viewport_height: window.innerHeight || 0,
			device_pixel_ratio: window.devicePixelRatio || 0,
			hardware_concurrency: navigator.hardwareConcurrency || 0,
			device_memory: navigator.deviceMemory || 0,
			max_touch_points: navigator.maxTouchPoints || 0,
			webdriver: Boolean(navigator.webdriver),
		});
	}`)
	if err != nil {
		return IdentityMetadata{}, err
	}
	if obj == nil {
		return IdentityMetadata{}, fmt.Errorf("identity probe returned nil")
	}

	var metadata IdentityMetadata
	if err := json.Unmarshal([]byte(obj.Value.Str()), &metadata); err != nil {
		return IdentityMetadata{}, err
	}
	now := time.Now()
	metadata.CheckedAt = now
	metadata.Fingerprint = identityFingerprint(metadata)
	return metadata, nil
}

func (s *ActionStateStore) CheckIdentity(current IdentityMetadata) (IdentityMetadata, []IdentityDrift, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.loadLocked()
	if err != nil {
		return IdentityMetadata{}, nil, err
	}
	if state.Identity == nil || state.Identity.Fingerprint == "" {
		current.CreatedAt = current.CheckedAt
		state.Identity = &current
		if err := s.saveLocked(state); err != nil {
			return IdentityMetadata{}, nil, err
		}
		return current, nil, nil
	}

	baseline := *state.Identity
	drift := CompareIdentityMetadata(baseline, current)
	current.CreatedAt = baseline.CreatedAt
	if current.CreatedAt.IsZero() {
		current.CreatedAt = current.CheckedAt
	}
	state.Identity = &current
	if err := s.saveLocked(state); err != nil {
		return baseline, nil, err
	}
	return baseline, drift, nil
}

func CompareIdentityMetadata(before, after IdentityMetadata) []IdentityDrift {
	if before.Fingerprint == "" || after.Fingerprint == "" || before.Fingerprint == after.Fingerprint {
		return nil
	}
	var drift []IdentityDrift
	add := func(field, oldValue, newValue string, critical bool) {
		if oldValue != newValue {
			drift = append(drift, IdentityDrift{
				Field:    field,
				Before:   oldValue,
				After:    newValue,
				Critical: critical,
			})
		}
	}
	add("user_agent", before.UserAgent, after.UserAgent, true)
	add("platform", before.Platform, after.Platform, true)
	add("vendor", before.Vendor, after.Vendor, true)
	add("languages", strings.Join(before.Languages, ","), strings.Join(after.Languages, ","), true)
	add("timezone", before.Timezone, after.Timezone, true)
	add("screen", fmt.Sprintf("%dx%d", before.ScreenWidth, before.ScreenHeight), fmt.Sprintf("%dx%d", after.ScreenWidth, after.ScreenHeight), true)
	add("viewport", fmt.Sprintf("%dx%d", before.ViewportWidth, before.ViewportHeight), fmt.Sprintf("%dx%d", after.ViewportWidth, after.ViewportHeight), false)
	add("device_pixel_ratio", fmt.Sprintf("%.3f", before.DevicePixelRatio), fmt.Sprintf("%.3f", after.DevicePixelRatio), true)
	add("hardware_concurrency", fmt.Sprintf("%d", before.HardwareConcurrency), fmt.Sprintf("%d", after.HardwareConcurrency), true)
	add("device_memory", fmt.Sprintf("%.3f", before.DeviceMemory), fmt.Sprintf("%.3f", after.DeviceMemory), false)
	add("max_touch_points", fmt.Sprintf("%d", before.MaxTouchPoints), fmt.Sprintf("%d", after.MaxTouchPoints), true)
	add("webdriver", fmt.Sprintf("%t", before.Webdriver), fmt.Sprintf("%t", after.Webdriver), true)
	if len(drift) == 0 {
		drift = append(drift, IdentityDrift{
			Field:    "fingerprint",
			Before:   before.Fingerprint,
			After:    after.Fingerprint,
			Critical: true,
		})
	}
	return drift
}

func identityFingerprint(metadata IdentityMetadata) string {
	stable := struct {
		UserAgent           string   `json:"user_agent"`
		Platform            string   `json:"platform"`
		Vendor              string   `json:"vendor"`
		Languages           []string `json:"languages"`
		Timezone            string   `json:"timezone"`
		ScreenWidth         int      `json:"screen_width"`
		ScreenHeight        int      `json:"screen_height"`
		ViewportWidth       int      `json:"viewport_width"`
		ViewportHeight      int      `json:"viewport_height"`
		DevicePixelRatio    float64  `json:"device_pixel_ratio"`
		HardwareConcurrency int      `json:"hardware_concurrency"`
		DeviceMemory        float64  `json:"device_memory"`
		MaxTouchPoints      int      `json:"max_touch_points"`
		Webdriver           bool     `json:"webdriver"`
	}{
		UserAgent:           metadata.UserAgent,
		Platform:            metadata.Platform,
		Vendor:              metadata.Vendor,
		Languages:           append([]string(nil), metadata.Languages...),
		Timezone:            metadata.Timezone,
		ScreenWidth:         metadata.ScreenWidth,
		ScreenHeight:        metadata.ScreenHeight,
		ViewportWidth:       metadata.ViewportWidth,
		ViewportHeight:      metadata.ViewportHeight,
		DevicePixelRatio:    metadata.DevicePixelRatio,
		HardwareConcurrency: metadata.HardwareConcurrency,
		DeviceMemory:        metadata.DeviceMemory,
		MaxTouchPoints:      metadata.MaxTouchPoints,
		Webdriver:           metadata.Webdriver,
	}
	data, _ := json.Marshal(stable)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

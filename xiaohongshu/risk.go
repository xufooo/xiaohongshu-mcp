package xiaohongshu

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

type RiskKind string

const (
	RiskNone             RiskKind = "none"
	RiskLoginExpired     RiskKind = "login_expired"
	RiskCaptcha          RiskKind = "captcha"
	RiskSliderChallenge  RiskKind = "slider_challenge"
	RiskAccessAnomaly    RiskKind = "access_anomaly"
	RiskNoteNotFound     RiskKind = "note_not_found"
	RiskPermissionDenied RiskKind = "permission_denied"
)

type RiskSignal struct {
	Kind        RiskKind      `json:"kind"`
	Reason      string        `json:"reason,omitempty"`
	URL         string        `json:"url,omitempty"`
	MatchedText string        `json:"matched_text,omitempty"`
	Cooldown    time.Duration `json:"-"`
	DetectedAt  time.Time     `json:"detected_at"`
	Recoverable bool          `json:"recoverable"`
}

type riskProbe struct {
	URL         string   `json:"url"`
	Title       string   `json:"title"`
	BodyText    string   `json:"body_text"`
	MatchedText string   `json:"matched_text"`
	Kind        RiskKind `json:"kind"`
	Reason      string   `json:"reason"`
}

func ClassifyRisk(page *hrod.Page) (RiskSignal, error) {
	now := time.Now()
	obj, err := page.Eval(`() => {
		const normalize = (value) => String(value || "").replace(/\s+/g, " ").trim();
		const bodyText = normalize(document.body?.innerText || "").slice(0, 2000);
		const title = normalize(document.title || "");
		const url = location.href.slice(0, 500);
		const haystack = `${title} ${bodyText}`;
		const findText = (keywords) => {
			const keyword = keywords.find((item) => haystack.includes(item));
			if (!keyword) return "";
			const index = haystack.indexOf(keyword);
			return haystack.slice(Math.max(0, index - 40), Math.min(haystack.length, index + 120));
		};
		const visible = (el) => {
			if (!el || !el.isConnected) return false;
			if (typeof el.checkVisibility === "function") {
				return el.checkVisibility({ checkOpacity: true, checkVisibilityCSS: true });
			}
			if (el.offsetParent !== null) return true;
			const rect = el.getBoundingClientRect();
			const style = window.getComputedStyle(el);
			return style.display !== "none" &&
				style.visibility !== "hidden" &&
				Number(style.opacity || "1") > 0 &&
				rect.width > 0 &&
				rect.height > 0;
		};
		const hasDOM = (selector) => {
			try {
				return Array.from(document.querySelectorAll(selector)).some(visible);
			} catch (_) {
				return false;
			}
		};
		const rules = [
			{
				kind: "login_expired",
				reason: "登录状态失效",
				keywords: ["登录已过期", "登录失效", "请先登录", "请登录", "扫码登录"],
				dom: [".login-container", ".login-qrcode", ".qrcode-img", "[class*='login-container']", "[class*='login-mask']"]
			},
			{
				kind: "slider_challenge",
				reason: "滑块验证",
				keywords: ["滑块"],
				dom: [".slider", "[class*='slider']", "[class*='captcha'] [class*='slide']"]
			},
			{
				kind: "captcha",
				reason: "验证码或安全验证",
				keywords: ["验证码", "安全验证", "请验证", "人机验证"],
				dom: [".captcha", "[class*='captcha']", "iframe[src*='captcha']"]
			},
			{
				kind: "access_anomaly",
				reason: "访问异常或操作频繁",
				keywords: ["操作频繁", "访问太频繁", "账号异常", "风险提示"],
				dom: []
			},
			{
				kind: "note_not_found",
				reason: "笔记不存在或已删除",
				keywords: ["笔记不存在", "内容不存在", "该笔记已被删除", "当前内容无法展示", "无法查看该笔记"],
				dom: []
			},
			{
				kind: "permission_denied",
				reason: "无权限访问",
				keywords: ["无权限", "暂无权限", "没有权限", "权限不足", "作者已设置", "仅自己可见"],
				dom: []
			}
		];
		for (const rule of rules) {
			const matchedText = findText(rule.keywords);
			const matchedDOM = (rule.dom || []).find(hasDOM) || "";
			if (matchedText || matchedDOM) {
				return JSON.stringify({
					url,
					title,
					body_text: bodyText,
					kind: rule.kind,
					reason: rule.reason,
					matched_text: matchedText || matchedDOM,
				});
			}
		}
		return JSON.stringify({
			url,
			title,
			body_text: bodyText,
			kind: "none",
		});
	}`)
	if err != nil {
		return RiskSignal{}, err
	}
	if obj == nil {
		return RiskSignal{}, fmt.Errorf("risk probe returned nil")
	}

	var probe riskProbe
	if err := json.Unmarshal([]byte(obj.Value.Str()), &probe); err != nil {
		return RiskSignal{}, err
	}
	signal := RiskSignal{
		Kind:        probe.Kind,
		Reason:      probe.Reason,
		URL:         probe.URL,
		MatchedText: strings.TrimSpace(probe.MatchedText),
		DetectedAt:  now,
	}
	if signal.Kind == "" {
		signal.Kind = RiskNone
	}
	applyRiskPolicy(&signal)
	return signal, nil
}

func IsRisk(signal RiskSignal) bool {
	return signal.Kind != "" && signal.Kind != RiskNone
}

func riskSignalFromReadyProbe(probe xhsReadyProbe) RiskSignal {
	text := strings.TrimSpace(probe.RiskText)
	signal := RiskSignal{
		Kind:        RiskNone,
		URL:         probe.URL,
		MatchedText: text,
		DetectedAt:  time.Now(),
	}
	if text == "" {
		return signal
	}

	switch {
	case strings.Contains(text, "登录已过期") ||
		strings.Contains(text, "登录失效") ||
		strings.Contains(text, "请先登录") ||
		strings.Contains(text, "请登录") ||
		strings.Contains(text, "扫码登录"):
		signal.Kind = RiskLoginExpired
		signal.Reason = "登录状态失效"
	case strings.Contains(text, "滑块"):
		signal.Kind = RiskSliderChallenge
		signal.Reason = "滑块验证"
	case strings.Contains(text, "验证码") ||
		strings.Contains(text, "安全验证") ||
		strings.Contains(text, "请验证") ||
		strings.Contains(text, "人机验证"):
		signal.Kind = RiskCaptcha
		signal.Reason = "验证码或安全验证"
	case strings.Contains(text, "操作频繁") ||
		strings.Contains(text, "访问太频繁") ||
		strings.Contains(text, "账号异常") ||
		strings.Contains(text, "风险提示"):
		signal.Kind = RiskAccessAnomaly
		signal.Reason = "访问异常或操作频繁"
	}
	applyRiskPolicy(&signal)
	return signal
}

func applyRiskPolicy(signal *RiskSignal) {
	switch signal.Kind {
	case RiskLoginExpired, RiskCaptcha, RiskSliderChallenge:
		signal.Cooldown = 6 * time.Hour
		signal.Recoverable = true
	case RiskAccessAnomaly:
		signal.Cooldown = 30 * time.Minute
		signal.Recoverable = true
	case RiskNoteNotFound, RiskPermissionDenied:
		signal.Cooldown = 0
		signal.Recoverable = false
	default:
		signal.Kind = RiskNone
		signal.Cooldown = 0
		signal.Recoverable = false
	}
}

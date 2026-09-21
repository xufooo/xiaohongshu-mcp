package xiaohongshu

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
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

const (
	RiskCooldownLoginExpired    = 15 * time.Minute
	RiskCooldownAccessAnomaly   = 90 * time.Minute
	RiskCooldownSliderChallenge = 2 * time.Hour
	RiskCooldownCaptcha         = 12 * time.Hour
)

type riskProbe struct {
	URL         string   `json:"url"`
	Title       string   `json:"title"`
	BodyText    string   `json:"body_text"`
	MatchedText string   `json:"matched_text"`
	Kind        RiskKind `json:"kind"`
	Reason      string   `json:"reason"`
}

// classifyRiskJS 是页面级风险分类的探针（var 而非 const：规则数组是运行时生成的）：规则数组由 riskRuleGroups 生成（唯一来源），
// 不再内联一份自己的清单——曾经页面侧多出 permission_denied 组且含"仅自己可见"，
// 于是"发布仅自己可见的笔记"必然被误判成风控。
var classifyRiskJS = `() => {` + xhsVisibleJS + `
		const normalize = (value) => String(value || "").replace(/\s+/g, " ").trim();
		const bodyText = normalize(document.body?.innerText || "").slice(0, 2000);
		const title = normalize(document.title || "");
		const url = location.href.slice(0, 500);
		const haystack = title + " " + bodyText;
		const findText = (keywords) => {
			const keyword = keywords.find((item) => haystack.includes(item));
			if (!keyword) return "";
			const index = haystack.indexOf(keyword);
			return haystack.slice(Math.max(0, index - 40), Math.min(haystack.length, index + 120));
		};
		const hasDOM = (selector) => {
			try {
				return Array.from(document.querySelectorAll(selector)).some((el) => visibleInCSS(el));
			} catch (_) {
				return false;
			}
		};
		const rules = ` + riskRulesJSList() + `;
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
	}`

func ClassifyRisk(page *hrod.Page) (RiskSignal, error) {
	now := time.Now()
	obj, err := page.Eval(classifyRiskJS)
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

	signal.Kind = RiskKindFromText(text)
	signal.Reason = riskKindReason(signal.Kind)
	applyRiskPolicy(&signal)
	return signal
}

// riskRuleGroup 是一条页面级风险规则。
type riskRuleGroup struct {
	Kind     RiskKind
	Reason   string
	Keywords []string
	DOM      []string
}

// riskRuleGroups 是页面级风险规则的**唯一来源**：
// 页面探针（classifyRiskJS 的规则数组）、文本探针（riskKeywordsJSList）、
// Go 侧文本分类（RiskKindFromText）都从这里生成，避免两份清单各自漂移。
// 顺序即优先级（如「滑块验证」先命中滑块，再考虑验证码）。
//
// 注意：**不要把用户能正常选择的可见性/设置文案放进来**。
// 「仅自己可见」曾经在 permission_denied 组里，导致发布私有笔记必然被误判成风控。
var riskRuleGroups = []riskRuleGroup{
	{
		Kind:     RiskLoginExpired,
		Reason:   "登录状态失效",
		Keywords: []string{"登录已过期", "登录失效", "请先登录", "请登录", "扫码登录"},
		DOM:      []string{".login-container", ".login-qrcode", ".qrcode-img", "[class*='login-container']", "[class*='login-mask']"},
	},
	{
		Kind:   RiskSliderChallenge,
		Reason: "滑块验证",
		// 通用 slider class 也会出现在正常页面组件中，不能单独作为风控证据。
		Keywords: []string{"滑块"},
	},
	{
		Kind:     RiskCaptcha,
		Reason:   "验证码或安全验证",
		Keywords: []string{"验证码", "安全验证", "请验证", "人机验证"},
		DOM:      []string{".captcha", "[class*='captcha']", "iframe[src*='captcha']"},
	},
	{
		Kind:     RiskAccessAnomaly,
		Reason:   "访问异常或操作频繁",
		Keywords: []string{"操作频繁", "访问太频繁", "账号异常"},
	},
	{
		Kind:     RiskNoteNotFound,
		Reason:   "笔记不存在或已删除",
		Keywords: []string{"笔记不存在", "内容不存在", "该笔记已被删除", "当前内容无法展示", "无法查看该笔记"},
	},
	{
		Kind:     RiskPermissionDenied,
		Reason:   "无权限访问",
		Keywords: []string{"无权限", "暂无权限", "没有权限", "权限不足", "作者已设置"},
	},
}

// RiskKindFromText 用风险关键词表把页面文本分类成 RiskKind。
func RiskKindFromText(text string) RiskKind {
	for _, group := range riskRuleGroups {
		for _, keyword := range group.Keywords {
			if strings.Contains(text, keyword) {
				return group.Kind
			}
		}
	}
	return RiskNone
}

// riskKeywordsJSList 生成页面内探针用的关键词 JSON 数组（与 Go 侧判定同源）。
func riskKeywordsJSList() string {
	all := make([]string, 0, 24)
	for _, group := range riskRuleGroups {
		all = append(all, group.Keywords...)
	}
	data, _ := json.Marshal(all) // []string 不会序列化失败
	return string(data)
}

// riskRulesJSList 生成页面探针用的规则 JSON 数组（kind/reason/keywords/dom 同源）。
func riskRulesJSList() string {
	type jsRule struct {
		Kind     string   `json:"kind"`
		Reason   string   `json:"reason"`
		Keywords []string `json:"keywords"`
		DOM      []string `json:"dom"`
	}
	rules := make([]jsRule, 0, len(riskRuleGroups))
	for _, group := range riskRuleGroups {
		dom := group.DOM
		if dom == nil {
			dom = []string{}
		}
		rules = append(rules, jsRule{Kind: string(group.Kind), Reason: group.Reason, Keywords: group.Keywords, DOM: dom})
	}
	data, _ := json.Marshal(rules) // 结构固定，不会序列化失败
	return string(data)
}

// writeFailureKeywords 返回写操作失败/被限流的关键词 JSON 数组：
// 公共词 + 动作专属词（评论/回复各自的后缀）。
func writeFailureKeywords(action string) string {
	keywords := []string{
		"操作频繁", "请验证", "滑块验证", "安全验证", "发送失败", "提交失败",
		action + "过于频繁", action + "失败", "禁止" + action,
	}
	data, _ := json.Marshal(keywords)
	return string(data)
}

func riskKindReason(kind RiskKind) string {
	switch kind {
	case RiskLoginExpired:
		return "登录状态失效"
	case RiskSliderChallenge:
		return "滑块验证"
	case RiskCaptcha:
		return "验证码或安全验证"
	case RiskAccessAnomaly:
		return "访问异常或操作频繁"
	}
	return ""
}

func applyRiskPolicy(signal *RiskSignal) {
	switch signal.Kind {
	case RiskLoginExpired:
		signal.Cooldown = RiskCooldownLoginExpired
		signal.Recoverable = true
	case RiskCaptcha:
		signal.Cooldown = RiskCooldownCaptcha
		signal.Recoverable = true
	case RiskSliderChallenge:
		signal.Cooldown = RiskCooldownSliderChallenge
		signal.Recoverable = true
	case RiskAccessAnomaly:
		signal.Cooldown = RiskCooldownAccessAnomaly
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

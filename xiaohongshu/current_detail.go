package xiaohongshu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
	"github.com/go-rod/rod/lib/proto"
	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
)

const xhsProbeVisibleJS = `
			const visible = (el) => {
				if (!el || !el.isConnected) return false;
				if (typeof el.checkVisibility === "function") {
					return el.checkVisibility({ checkOpacity: true, checkVisibilityCSS: true });
				}
				const rect = el.getBoundingClientRect();
				const style = window.getComputedStyle(el);
				return style.display !== "none" &&
					style.visibility !== "hidden" &&
					Number(style.opacity || "1") > 0 &&
					rect.width > 1 &&
					rect.height > 1;
			};
`

const xhsProbeFeedMatchJS = `
			const detailURLMatchesFeedID = (rawURL) => {
				if (!feedID) return false;
				try {
					const parsed = new URL(String(rawURL || ""), location.href);
					const segments = parsed.pathname.split("/").filter(Boolean).map((part) => decodeURIComponent(part));
					if (segments.includes(feedID)) return true;
					for (const value of parsed.searchParams.values()) {
						if (value === feedID) return true;
					}
				} catch (_) {
					return false;
				}
				return false;
			};
			const elementMatchesFeedID = (el) => {
				if (!feedID || !el) return false;
				if (Object.values(el.dataset || {}).some((value) => String(value || "") === feedID)) {
					return true;
				}
				return Array.from(el.querySelectorAll("a[href]")).some((a) => detailURLMatchesFeedID(a.href));
			};
`


const xhsProbeCollectionJS = `
			const count = (selector) => {
				try { return document.querySelectorAll(selector).length; } catch (_) { return 0; }
			};
			const visibleCount = (selector) => {
				try {
					return Array.from(document.querySelectorAll(selector)).filter(visible).length;
				} catch (_) {
					return 0;
				}
			};
		const unwrap = (value) => {
			if (value && typeof value === "object") {
				if ("value" in value) return value.value;
				if ("_value" in value) return value._value;
			}
			return value;
		};
		const sizeOf = (value) => {
			value = unwrap(value);
			if (Array.isArray(value)) return value.length;
			if (value && typeof value === "object") return Object.keys(value).length;
			return value ? 1 : 0;
		};
`

const xhsProbeRiskJS = `
		const riskOf = (text) => {
			const riskKeywords = [
				"登录已过期", "登录失效", "请先登录", "请登录", "扫码登录",
				"验证码", "滑块", "安全验证", "请验证", "人机验证",
				"操作频繁", "访问太频繁", "账号异常"
			];
			const risk = riskKeywords.find((keyword) => text.includes(keyword)) || "";
			const riskIndex = risk ? text.indexOf(risk) : -1;
			return risk
				? text.slice(Math.max(0, riskIndex - 40), Math.min(text.length, riskIndex + 100))
				: "";
		};
`

const xhsSearchInputReadyJS = `
		const searchInputReady = (selector) => {
			const candidates = Array.from(document.querySelectorAll(selector));
			return candidates.some(el => {
				if (!el || !el.isConnected) return false;
				if (!visible(el)) return false;
				if (el.disabled || el.readOnly) return false;
				const r = el.getBoundingClientRect();
				if (r.top >= window.innerHeight || r.bottom <= 0 ||
					r.left >= window.innerWidth || r.right <= 0) return false;
				const cx = r.left + r.width / 2;
				const cy = r.top + r.height / 2;
				const hit = document.elementFromPoint(cx, cy);
				return hit && (el === hit || el.contains(hit));
			});
		};
`
var errCurrentDetailEvalTimeout = errors.New("current detail eval timeout")
var errPermanentCurrentDetailProbe = errors.New("permanent current detail probe error")

const (
	currentDetailProbeCategoryAttemptContextDeadline   = "attempt_context_deadline"
	currentDetailProbeCategoryOtherCDPError            = "other_cdp_error"
	currentDetailProbeCategoryContextCanceled          = "context_canceled"
	currentDetailProbeCategoryEvalTimeout              = "eval_timeout"
	currentDetailProbeCategoryExecutionContextDestroyed = "execution_context_destroyed"
)

type currentFeedDetailProbe struct {
	URL                       string `json:"url"`
	URLMatched                bool   `json:"url_matched"`
	VisibleDetailCount        int    `json:"visible_detail_count"`
	VisibleMatchedDetailCount int    `json:"visible_matched_detail_count"`
	StateMatched              bool   `json:"state_matched"`
}

func currentDetailProbeExpression(probeJS, feedID, detailSelector string) (string, error) {
	encodedFeedID, err := json.Marshal(feedID)
	if err != nil {
		return "", err
	}
	encodedSelector, err := json.Marshal(detailSelector)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("(%s)(%s, %s)", probeJS, encodedFeedID, encodedSelector), nil
}

func probeCurrentFeedDetail(ctx context.Context, page *hrod.Page, feedID string) (currentFeedDetailProbe, error) {
	probeJS := `(feedID, detailSelector) => {` + xhsProbeVisibleJS + xhsProbeFeedMatchJS + `
			const visibleDetails = Array.from(document.querySelectorAll(detailSelector)).filter(visible);
			const visibleMatchedDetails = visibleDetails.filter(elementMatchesFeedID);
			const stateMap = window.__INITIAL_STATE__?.note?.noteDetailMap;
			return JSON.stringify({
				url: location.href.slice(0, 300),
				url_matched: detailURLMatchesFeedID(location.href),
				visible_detail_count: visibleDetails.length,
				visible_matched_detail_count: visibleMatchedDetails.length,
				state_matched: Boolean(feedID && stateMap && Object.prototype.hasOwnProperty.call(stateMap, feedID)),
		});
	}`
	expression, err := currentDetailProbeExpression(probeJS, feedID, SelectorFeedDetailReady)
	if err != nil {
		return currentFeedDetailProbe{}, fmt.Errorf("%w: %v", errPermanentCurrentDetailProbe, err)
	}
	result, err := (proto.RuntimeEvaluate{
		Expression:     expression,
		ReturnByValue:  true,
		AwaitPromise:    true,
	}).Call(page.Rod.Context(ctx))
	if err != nil {
		return currentFeedDetailProbe{}, normalizeCurrentDetailProbeError(ctx, err)
	}
	if result == nil {
		return currentFeedDetailProbe{}, fmt.Errorf("%w: 当前详情页探测无返回", errPermanentCurrentDetailProbe)
	}
	if result.ExceptionDetails != nil {
		return currentFeedDetailProbe{}, normalizeCurrentDetailProbeError(ctx, &rod.EvalError{result.ExceptionDetails})
	}
	if result.Result == nil {
		return currentFeedDetailProbe{}, fmt.Errorf("%w: 当前详情页探测无返回", errPermanentCurrentDetailProbe)
	}

	var probe currentFeedDetailProbe
	if err := json.Unmarshal([]byte(result.Result.Value.Str()), &probe); err != nil {
		return currentFeedDetailProbe{}, fmt.Errorf("%w: %v", errPermanentCurrentDetailProbe, err)
	}
	return probe, nil
}

func normalizeCurrentDetailProbeError(ctx context.Context, err error) error {
	if IsFatalRendererError(err) {
		return err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if isEvalTimeout(err) {
		return errCurrentDetailEvalTimeout
	}
	return err
}

func currentFeedDetailMatched(probe currentFeedDetailProbe, _ string) bool {
	return (probe.URLMatched && probe.VisibleDetailCount > 0) ||
		probe.VisibleMatchedDetailCount > 0
}

func detailURLMatchesFeedID(rawURL, feedID string) bool {
	if feedID == "" {
		return false
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	for _, segment := range strings.Split(strings.Trim(u.Path, "/"), "/") {
		if segment == feedID {
			return true
		}
	}
	for _, values := range u.Query() {
		for _, value := range values {
			if value == feedID {
				return true
			}
		}
	}
	return false
}

func isTransientCurrentDetailProbeError(err error) bool {
	if err == nil || errors.Is(err, errPermanentCurrentDetailProbe) {
		return false
	}
	if errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, errCurrentDetailEvalTimeout) ||
		errors.Is(err, cdp.ErrCtxNotFound) ||
		errors.Is(err, cdp.ErrCtxDestroyed) {
		return true
	}

	message := err.Error()
	return strings.Contains(message, "Execution context was destroyed") ||
		strings.Contains(message, "Cannot find context with specified id") ||
		strings.Contains(message, "context canceled")
}

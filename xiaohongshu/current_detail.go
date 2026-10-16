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
	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
)

const xhsProbeFeedMatchJS = `
			const detailURLMatchesFeedID = (rawURL) => {
				if (!feedID) return false;
				try {
					const parsed = new URL(String(rawURL || ""), location.href);
					if (parsed.protocol !== "https:" || parsed.hostname !== "www.xiaohongshu.com") return false;
					const segments = parsed.pathname.split("/").filter(Boolean).map((part) => {
						try { return decodeURIComponent(part); } catch (_) { return ""; }
					});
					if (segments.length === 2 && segments[0] === "explore" && segments[1] === feedID) return true;
					if (segments.length === 3 && segments[0] === "discovery" && segments[1] === "item" && segments[2] === feedID) return true;
				} catch (_) {
					return false;
				}
				return false;
			};
			const isStateObject = (value) => value !== null && typeof value === "object";
			const unwrapActiveValue = (value) => {
				const seen = new Set();
				let current = value;
				while (isStateObject(current) && !seen.has(current)) {
					seen.add(current);
					if ((current.__v_isReactive || current.__v_isReadonly) &&
						isStateObject(current.__v_raw) && current.__v_raw !== current) {
						current = current.__v_raw;
						continue;
					}
					if (current.__v_isRef === true && "value" in current) {
						const next = current.value;
						if (next === current) break;
						current = next;
						continue;
					}
					if (Object.prototype.hasOwnProperty.call(current, "_value")) {
						const next = current._value;
						if (next === current) break;
						current = next;
						continue;
					}
					if (Object.prototype.hasOwnProperty.call(current, "value")) {
						const next = current.value;
						if (next === current) break;
						current = next;
						continue;
					}
					break;
				}
				return current;
			};
			const stateNoteDetailMap = () => {
				try {
					const state = unwrapActiveValue(window.__INITIAL_STATE__);
					const note = unwrapActiveValue(state?.note);
					return unwrapActiveValue(note?.noteDetailMap);
				} catch (_) { return null; }
			};
			const activeNoteId = () => {
				try {
					const state = unwrapActiveValue(window.__INITIAL_STATE__);
					const note = unwrapActiveValue(state?.note);
					return String(unwrapActiveValue(note?.currentNoteId) || "");
				} catch (_) { return ""; }
			};
			const targetStateNoteId = (map, fid) => {
				try {
					map = unwrapActiveValue(map);
					if (!fid || !map || !Object.prototype.hasOwnProperty.call(map, fid)) return "";
					const detail = unwrapActiveValue(map[fid]);
					const note = unwrapActiveValue(detail?.note ?? detail);
					const raw = unwrapActiveValue(note?.noteId ?? note?.noteid ?? note?.id);
					return typeof raw === "string" ? raw : "";
				} catch (_) { return ""; }
			};
			const uniqueDetailRootCount = () => {
				try {
					const masks = Array.from(document.querySelectorAll(".note-detail-mask")).filter((el) => visibleWithSize(el, 1));
					const containers = Array.from(document.querySelectorAll(".note-container")).filter((el) => visibleWithSize(el, 1));
					if (masks.length === 1 && containers.length === 1 && masks[0].contains(containers[0])) return 1;
					if (masks.length === 0 && containers.length === 0) return 0;
					if (masks.length > 1) return masks.length;
					return 2;
				} catch (_) { return 0; }
			};
			const uniqueDetailRoot = () => {
				try {
					const masks = Array.from(document.querySelectorAll(".note-detail-mask")).filter((el) => visibleWithSize(el, 1));
					if (masks.length !== 1) return null;
					const root = masks[0];
					const containers = Array.from(root.querySelectorAll(".note-container")).filter((el) => visibleWithSize(el, 1));
					if (containers.length !== 1) return null;
					const allContainers = Array.from(document.querySelectorAll(".note-container")).filter((el) => visibleWithSize(el, 1));
					if (allContainers.length !== 1) return null;
					if (!root.contains(containers[0])) return null;
					return root;
				} catch (_) { return null; }
			};
			const cleanText = (value) => String(value || "").replace(/\s+/g, " ").trim();
			const rootTextBySelectors = (root, selectors) => {
				try {
					for (const selector of selectors) {
						const el = root.querySelector(selector);
						const text = cleanText(el?.innerText || el?.textContent);
						if (text) return text;
					}
				} catch (_) {}
				return "";
			};
			const userIdFromProfileHref = (href) => {
				try {
					const match = String(href || "").match(/\/user\/profile\/([^/?#]+)/);
					return match ? decodeURIComponent(match[1]) : "";
				} catch (_) { return ""; }
			};
			const stateNoteOf = (map, fid) => {
				try {
					map = unwrapActiveValue(map);
					if (!fid || !map || !Object.prototype.hasOwnProperty.call(map, fid)) return null;
					const detail = unwrapActiveValue(map[fid]);
					return unwrapActiveValue(detail?.note ?? detail) ?? null;
				} catch (_) { return null; }
			};
			const stateImageUrlsOf = (note) => {
				const out = [];
				try {
					note = unwrapActiveValue(note);
					const pushList = (rawList) => {
						const list = unwrapActiveValue(rawList);
						if (!Array.isArray(list)) return;
						for (const rawItem of list) {
							const item = unwrapActiveValue(rawItem);
							if (!item) continue;
							if (typeof item === "string") { out.push(item); continue; }
						const url = unwrapActiveValue(item.urlDefault ?? item.urlPre ?? item.url ?? item.src ?? "");
						if (url) out.push(String(url));
						}
					};
					pushList(note?.imageList);
					pushList(note?.images);
					const cover = unwrapActiveValue(note?.cover);
					if (typeof cover === "string" && cover) out.push(cover);
				} catch (_) {}
				return out;
			};
			const detailRootContentMatched = (fid) => {
				if (!fid) return false;
				try {
					const root = uniqueDetailRoot();
					if (!root) return false;
					const map = stateNoteDetailMap();
					const note = stateNoteOf(map, fid);
					if (!note) return false;
					const stateTitle = cleanText(unwrapActiveValue(note.title ?? note.displayTitle ?? ""));
					const rootTitle = rootTextBySelectors(root, ["#detail-title", ".note-content .title"]);
					if (stateTitle || rootTitle) {
						if (!stateTitle || !rootTitle || rootTitle !== stateTitle) return false;
					}
					const stateUser = unwrapActiveValue(note.user);
					const stateUserId = String(unwrapActiveValue(stateUser?.userId ?? stateUser?.user_id ?? stateUser?.id) ?? "");
					if (!stateUserId) return false;
					let authorOk = false;
					try {
						const zone = root;
						const links = Array.from(zone.querySelectorAll('a[href*="/user/profile/"]')).filter((a) => !a.closest(".comments-container, .comments-el, .parent-comment, .comment-item"));
						for (const a of links) {
							if (userIdFromProfileHref(a.href) === stateUserId) { authorOk = true; break; }
						}
					} catch (_) { authorOk = false; }
					if (!authorOk) return false;
					const stateDesc = cleanText(unwrapActiveValue(note.desc ?? note.description ?? note.content ?? ""));
					if (stateDesc) {
						const rootDesc = rootTextBySelectors(root, ["#detail-desc", ".note-content .desc", ".note-text"]);
						if (!rootDesc) return false;
						if (!(rootDesc === stateDesc || rootDesc.startsWith(stateDesc))) return false;
					}
					const stateImages = stateImageUrlsOf(note);
					if (stateImages.length > 0) {
						let mediaImgs = [];
						try {
							mediaImgs = Array.from(root.querySelectorAll(".swiper img, .media-container img"));
						} catch (_) { mediaImgs = []; }
						const rootImages = [];
						for (const img of mediaImgs) {
							const src = img.currentSrc || img.getAttribute("src") || "";
							if (src && !String(src).startsWith("data:image")) rootImages.push(String(src));
						}
						let allMatch = true;
						for (const src of rootImages) {
							let one = false;
							for (const su of stateImages) {
								if (!su) continue;
								if (src === su || String(su).split("?")[0].split("#")[0] === src.split("?")[0].split("#")[0]) { one = true; break; }
							}
							if (!one) { allMatch = false; break; }
						}
						if (!allMatch) return false;
					}
					return true;
				} catch (_) { return false; }
			};
			const detailRootMatched = (fid) => {
				if (!fid) return false;
				try {
					if (uniqueDetailRootCount() !== 1) return false;
					if (!uniqueDetailRoot()) return false;
					if (!detailURLMatchesFeedID(location.href)) return false;
					if (activeNoteId() !== fid) return false;
					const map = stateNoteDetailMap();
					if (targetStateNoteId(map, fid) !== fid) return false;
					if (!map || !Object.prototype.hasOwnProperty.call(map, fid)) return false;
					if (!detailRootContentMatched(fid)) return false;
					return true;
				} catch (_) { return false; }
			};
			const elementMatchesFeedID = (el) => {
				if (!feedID || !el) return false;
				try {
					const root = uniqueDetailRoot();
					if (!root) return false;
					if (!root.contains(el)) return false;
					return detailRootMatched(feedID);
				} catch (_) { return false; }
			};
`


const xhsProbeCollectionJS = `
			const count = (selector) => {
				try { return document.querySelectorAll(selector).length; } catch (_) { return 0; }
			};
			const visibleCount = (selector) => {
				try {
					return Array.from(document.querySelectorAll(selector)).filter((el) => visibleWithSize(el, 1)).length;
				} catch (_) {
					return 0;
				}
			};
		const unwrap = (value) => unwrapActiveValue(value);
		const sizeOf = (value) => {
			value = unwrap(value);
			if (Array.isArray(value)) return value.length;
			if (value && typeof value === "object") return Object.keys(value).length;
			return value ? 1 : 0;
		};
`

// xhsProbeRiskJS 生成页面内风险文本探针；关键词与 Go 侧风控判定同源。
func xhsProbeRiskJS() string {
	return `
		const riskOf = (text) => {
			const riskKeywords = ` + riskKeywordsJSList() + `;
			const risk = riskKeywords.find((keyword) => text.includes(keyword)) || "";
			const riskIndex = risk ? text.indexOf(risk) : -1;
			return risk
				? text.slice(Math.max(0, riskIndex - 40), Math.min(text.length, riskIndex + 100))
				: "";
		};
`
}

const xhsSearchInputReadyJS = `
		const searchInputReady = (selector) => {
			const candidates = Array.from(document.querySelectorAll(selector));
			return candidates.some(el => {
				if (!el || !el.isConnected) return false;
				if (!visibleWithSize(el, 1)) return false;
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
var ErrDetailIdentityConflict = errors.New("detail identity conflict")

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
	probeJS := `(feedID, detailSelector) => {` + xhsVisibleJS + xhsProbeFeedMatchJS + `
			const stateMap = stateNoteDetailMap();
			const rootCount = uniqueDetailRootCount();
			const matched = detailRootMatched(feedID);
			const hasState = Boolean(feedID && stateMap && Object.prototype.hasOwnProperty.call(stateMap, feedID) && activeNoteId() === feedID && targetStateNoteId(stateMap, feedID) === feedID);
			return JSON.stringify({
				url: location.href.slice(0, 300),
				url_matched: detailURLMatchesFeedID(location.href),
				visible_detail_count: rootCount,
				visible_matched_detail_count: matched ? 1 : 0,
				state_matched: hasState,
		});
	}`
	expression, err := currentDetailProbeExpression(probeJS, feedID, SelectorFeedDetailReady)
	if err != nil {
		return currentFeedDetailProbe{}, fmt.Errorf("%w: %v", errPermanentCurrentDetailProbe, err)
	}
	result, err := newRuntimeEvaluate(ctx, expression).Call(page.Rod.Context(ctx))
	if err != nil {
		return currentFeedDetailProbe{}, normalizeCurrentDetailProbeError(ctx, err)
	}
	if result == nil {
		return currentFeedDetailProbe{}, fmt.Errorf("%w: 当前详情页探测无返回", errPermanentCurrentDetailProbe)
	}
	if result.ExceptionDetails != nil {
		return currentFeedDetailProbe{}, normalizeCurrentDetailProbeError(ctx, &rod.EvalError{RuntimeExceptionDetails: result.ExceptionDetails})
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

func currentFeedDetailMatched(probe currentFeedDetailProbe, feedID string) bool {
	return strictFeedDetailMatched(probe, feedID)
}

// strictFeedDetailMatched 是唯一的 open_note 目标身份判定：官方精确路径、唯一可见详情、
// 目标匹配详情、目标页面状态四者缺一即 fail-closed。query 值、路径其他位置、子链接或
// 宽泛 dataset 命中不能单独证明身份。
func strictFeedDetailMatched(probe currentFeedDetailProbe, feedID string) bool {
	if feedID == "" {
		return false
	}
	return probe.URLMatched &&
		probe.StateMatched &&
		probe.VisibleDetailCount == 1 &&
		probe.VisibleMatchedDetailCount == 1
}

func detailURLMatchesFeedID(rawURL, feedID string) bool {
	if feedID == "" {
		return false
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	noteID, _, ok := parseOfficialNoteURL(u)
	if !ok {
		return false
	}
	return noteID == feedID
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

package xiaohongshu

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/xpzouying/xiaohongshu-mcp/errors"
	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
)

type snapshotStageError struct {
	stage string
	kind  string
	cause error
}

func (e *snapshotStageError) Error() string {
	if e.kind == "context_deadline" { return fmt.Sprintf("snapshot stage=%s kind=%s: deadline exceeded", e.stage, e.kind) }
	return fmt.Sprintf("snapshot stage=%s kind=%s", e.stage, e.kind)
}
func (e *snapshotStageError) Unwrap() error { return e.cause }
func (e *snapshotStageError) Stage() string { return e.stage }
func (e *snapshotStageError) Kind() string { return e.kind }

func classifySnapshotError(err error) string {
	if err == nil { return "other" }
	if stderrors.Is(err, context.Canceled) { return "context_canceled" }
	if stderrors.Is(err, context.DeadlineExceeded) { return "context_deadline" }
	if IsFatalRendererError(err) { return "fatal_renderer" }
	if stderrors.Is(err, errors.ErrNoFeedDetail) { return "no_detail" }
	if isEvalTimeout(err) { return "eval_timeout" }
	var syntaxErr *json.SyntaxError
	if stderrors.As(err, &syntaxErr) { return "json_decode" }
	var typeErr *json.UnmarshalTypeError
	if stderrors.As(err, &typeErr) { return "json_decode" }
	var evalErr *rod.EvalError
	if stderrors.As(err, &evalErr) { return "eval_exception" }
	return "other"
}

// interactStateJS 是唯一的互动状态来源：同时要求 like/collect wrapper 存在，
// like use 读取 xlink:href（兼容 href），#liked→true / #like→false，
// #collected→true / #collect→false。use 缺失、未知 href 或任一 wrapper 缺失
// 均返回 null（unknown，fail-closed）。
const interactStateJS = `
	const hrefHash = (href) => {
		const idx = href.indexOf("#");
		return idx >= 0 ? href.slice(idx) : "";
	};
	const likeWrapper = document.querySelector(likeSel);
	const collectWrapper = document.querySelector(collectSel);
	if (!likeWrapper || !collectWrapper) return null;
	const likeUse = likeWrapper.querySelector("use");
	if (!likeUse) return null;
	const likeRaw = likeUse.getAttribute("xlink:href") || likeUse.getAttribute("href") || "";
	const likeHref = hrefHash(likeRaw);
	let liked;
	if (likeHref === "#liked") liked = true;
	else if (likeHref === "#like") liked = false;
	else return null;
	const collectUse = collectWrapper.querySelector("use");
	if (!collectUse) return null;
	const collectRaw = collectUse.getAttribute("xlink:href") || collectUse.getAttribute("href") || "";
	const collectHref = hrefHash(collectRaw);
	let collected;
	if (collectHref === "#collected") collected = true;
	else if (collectHref === "#collect") collected = false;
	else return null;
	return { liked, collected };
`

// domCleanJS 只定义 clean（供三个 DOM 提取器共用）。
const domCleanJS = `
	const clean = (value) => (value || "").replace(/\s+/g, " ").trim();
`

// domNoteHelpersJS 只包含 pickText/pickAttr/countNear（依赖 domCleanJS 的 clean）。
const domNoteHelpersJS = `
	const pickText = (selectors) => {
		for (const selector of selectors) {
			const el = document.querySelector(selector);
			const text = clean(el?.innerText || el?.textContent);
			if (text) return text;
		}
		return "";
	};
	const pickAttr = (selectors, attr) => {
		for (const selector of selectors) {
			const el = document.querySelector(selector);
			const value = el?.getAttribute(attr) || "";
			if (value) return value;
		}
		return "";
	};
	const countNear = (selectors) => {
		for (const selector of selectors) {
			const el = document.querySelector(selector);
			if (!el) continue;
			const text = clean(el.innerText || el.textContent || el.parentElement?.innerText);
			const match = text.match(/([\d.万wWkK]+)/);
			if (match) return match[1];
		}
		return "";
	};
`

// domCommentExtractorJS 定义 extractComments(feedID)，内容逐字符源自三处相同评论遍历逻辑。
const domCommentExtractorJS = `
	const extractComments = (feedID) => Array.from(document.querySelectorAll(".parent-comment")).map((parent) => {
		const top = parent.querySelector(":scope > .comment-item") || parent;
		const content = clean(top.querySelector(".content, .note-text, [class*='content']")?.innerText || top.innerText);
		const user = clean(top.querySelector(".author-wrapper .name, .name, .nickname, [class*='name']")?.innerText);
		const likeText = clean(top.querySelector(".interactions .like, .like, [class*='like']")?.innerText);
		const subComments = Array.from(parent.querySelectorAll(":scope > .reply-container > .list-container > .comment-item")).map((sub) => {
			const subContent = clean(sub.querySelector(".content, .note-text, [class*='content']")?.innerText || sub.innerText);
			const subUser = clean(sub.querySelector(".author-wrapper .name, .name, .nickname, [class*='name']")?.innerText);
			const subLikeText = clean(sub.querySelector(".interactions .like, .like, [class*='like']")?.innerText);
			return {
				id: sub.getAttribute("id") || sub.dataset?.id || sub.getAttribute("data-comment-id") || "",
				noteId: feedID,
				content: subContent,
				likeCount: (subLikeText.match(/([\d.万wWkK]+)/) || ["", ""])[1],
				userInfo: { nickname: subUser, nickName: subUser },
				subComments: [],
				showTags: []
			};
		}).filter((subComment) => subComment.content);
		return {
			id: top.getAttribute("id") || parent.dataset?.id || parent.getAttribute("data-comment-id") || top.dataset?.id || top.getAttribute("data-comment-id") || "",
			noteId: feedID,
			content,
			likeCount: (likeText.match(/([\d.万wWkK]+)/) || ["", ""])[1],
			userInfo: { nickname: user, nickName: user },
			subCommentCount: subComments.length ? String(subComments.length) : "",
			subComments,
			showTags: []
		};
	}).filter((comment) => comment.content);
`

// OpenedNoteSnapshot 是打开笔记后的一次 DOM 快照：正文、图片、href 互动状态和当前首屏评论。
type OpenedNoteSnapshot struct {
	Note     OpenedNoteContent `json:"note"`
	Comments []Comment         `json:"comments"`
}

func ExtractOpenedNoteSnapshotFromDOM(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter, feedID string) (*OpenedNoteSnapshot, error) {
	content, err := extractOpenedNoteFieldsFromDOM(ctx, page, feedID)
	if err != nil {
		if counter != nil && counter.stageDiagnostics {
			err = &snapshotStageError{"note_fields", classifySnapshotError(err), err}
		}
		return nil, finishOpenedNoteSnapshotAttempt(ctx, counter, page, err)
	}
	comments, err := extractOpenedNoteCommentsFromDOM(ctx, page, feedID)
	if err != nil {
		if counter != nil && counter.stageDiagnostics {
			err = &snapshotStageError{"comments", classifySnapshotError(err), err}
		}
		return nil, finishOpenedNoteSnapshotAttempt(ctx, counter, page, err)
	}
	if strings.TrimSpace(content.Title) == "" && strings.TrimSpace(content.Desc) == "" && len(comments) == 0 {
		err := fmt.Errorf("DOM快照返回为空: %w", errors.ErrNoFeedDetail)
		if counter != nil && counter.stageDiagnostics {
			err = &snapshotStageError{"empty_snapshot", "no_detail", err}
		}
		return nil, finishOpenedNoteSnapshotAttempt(ctx, counter, page, err)
	}
	if err := finishOpenedNoteSnapshotAttempt(ctx, counter, page, nil); err != nil {
		if counter != nil && counter.stageDiagnostics {
			return nil, &snapshotStageError{"attempt_finalize", classifySnapshotError(err), err}
		}
		return nil, err
	}
	return &OpenedNoteSnapshot{Note: content, Comments: comments}, nil
}

func finishOpenedNoteSnapshotAttempt(ctx context.Context, counter *evalTimeoutCounter, page *hrod.Page, err error) error {
	if counter == nil {
		return err
	}
	return counter.add(ctx, err, func() error {
		return confirmRendererAlive(ctx, page, counter)
	})
}

func extractOpenedNoteFieldsFromDOM(ctx context.Context, page *hrod.Page, feedID string) (OpenedNoteContent, error) {
	result, err := evalJSDirect(ctx, page, `(feedID, likeSel, collectSel) => {` + domCleanJS + domNoteHelpersJS + `
		const title = pickText(["#detail-title", ".note-content .title", ".title", "[class*='title']"]);
		const desc = pickText(["#detail-desc", ".note-content .desc", ".note-text", ".desc", "[class*='desc']"]);
		const author = pickText([".author .name", ".author-wrapper .name", ".user .name", ".nickname", "[class*='author'] [class*='name']"]);
		const avatar = pickAttr([".author img", ".user img", ".avatar img", "img.avatar"], "src");
		const images = Array.from(document.querySelectorAll(".swiper img, .note-content img, .media-container img"))
			.map((img) => ({ width: img.naturalWidth || 0, height: img.naturalHeight || 0, urlDefault: img.src || "", urlPre: img.src || "" }))
			.filter((img) => img.urlDefault);
		const interact = (() => {` + interactStateJS + `})();
		return JSON.stringify({
			note_id: feedID,
			title,
			desc,
			type: document.querySelector("video") ? "video" : "normal",
			user: { nickname: author, nickName: author, avatar },
			interactInfo: {
				liked: interact ? interact.liked : false,
				collected: interact ? interact.collected : false,
				likedCount: countNear([".interact-container .like-lottie", ".interact-container .like-wrapper", ".interact-container [class*='like']"]),
				commentCount: countNear([".comments-container .total", ".comment-wrapper", "[class*='comment']"]),
				collectedCount: countNear([".interact-container .collect-icon", ".interact-container .collect-wrapper", ".interact-container [class*='collect']"])
			},
			imageList: images
		});
	}`, feedID, SelectorLikeButton, SelectorCollectButton)
	if err != nil {
		kind := "异常"
		if isEvalTimeout(err) {
			kind = "超时"
		}
		return OpenedNoteContent{}, fmt.Errorf("提取打开笔记快照失败: DOM Eval%s: %w", kind, err)
	}
	if result == nil || strings.TrimSpace(result.Value.Str()) == "" {
		return OpenedNoteContent{}, fmt.Errorf("DOM快照返回为空: %w", errors.ErrNoFeedDetail)
	}

	var content OpenedNoteContent
	if err := json.Unmarshal([]byte(result.Value.Str()), &content); err != nil {
		return OpenedNoteContent{}, fmt.Errorf("提取打开笔记快照失败: JSON解析异常: %w", err)
	}
	return content, nil
}

func extractOpenedNoteCommentsFromDOM(ctx context.Context, page *hrod.Page, feedID string) ([]Comment, error) {
	result, err := evalJSDirect(ctx, page, `(feedID) => {` + domCleanJS + domCommentExtractorJS + `
		return JSON.stringify(extractComments(feedID));
	}`, feedID)
	if err != nil {
		kind := "异常"
		if isEvalTimeout(err) {
			kind = "超时"
		}
		return nil, fmt.Errorf("提取打开笔记快照失败: DOM Eval%s: %w", kind, err)
	}
	if result == nil || strings.TrimSpace(result.Value.Str()) == "" {
		return nil, fmt.Errorf("DOM快照返回为空: %w", errors.ErrNoFeedDetail)
	}

	var comments []Comment
	if err := json.Unmarshal([]byte(result.Value.Str()), &comments); err != nil {
		return nil, fmt.Errorf("提取打开笔记快照失败: JSON解析异常: %w", err)
	}
	return comments, nil
}

type SnapshotDiagnosticError struct {
	diagnostic string
	cause      error
}

func (e *SnapshotDiagnosticError) Error() string {
	if e == nil {
		return ""
	}
	if e.diagnostic == "" {
		if e.cause == nil {
			return ""
		}
		return e.cause.Error()
	}
	return "snapshot_diagnostic=" + e.diagnostic
}

func (e *SnapshotDiagnosticError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *SnapshotDiagnosticError) Diagnostic() string {
	if e == nil {
		return ""
	}
	return e.diagnostic
}

func probeOpenedNoteSnapshotStages(ctx context.Context, page *hrod.Page) string {
	stages := []struct {
		name string
		js   string
	}{
		{"snapshot_shell", `() => JSON.stringify(document.querySelectorAll("#detail-title, #detail-desc, .note-content").length)`},
		{"note_fields", `() => JSON.stringify(["#detail-title", "#detail-desc", ".author .name", ".author-wrapper .name"].reduce((n, s) => n + document.querySelectorAll(s).length, 0))`},
		{"images", `() => JSON.stringify(document.querySelectorAll(".swiper img, .note-content img, .media-container img").length)`},
		{"comments_top", `() => JSON.stringify(document.querySelectorAll(".parent-comment").length)`},
		{"comments_nested", `() => JSON.stringify(document.querySelectorAll(".parent-comment .reply-container .comment-item").length)`},
		{"interactions", `() => JSON.stringify(document.querySelectorAll(".interact-container, [class*='like'], [class*='collect']").length)`},
	}
	parts := make([]string, 0, len(stages))
	for _, stage := range stages {
		started := time.Now()
		probeCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		result, err := evalJSDirect(probeCtx, page, stage.js)
		cancel()
		terminal := "ok"
		countBucket := "0"
		if err != nil {
			switch {
			case stderrors.Is(err, context.Canceled):
				terminal = "context_canceled"
			case stderrors.Is(err, context.DeadlineExceeded), isEvalTimeout(err):
				terminal = "eval_timeout"
			default:
				terminal = "other"
			}
		} else if result == nil {
			terminal = "other"
		} else {
			var count int
			if json.Unmarshal([]byte(result.Value.Str()), &count) != nil {
				terminal = "other"
			} else {
				switch {
				case count == 0:
					countBucket = "0"
				case count == 1:
					countBucket = "1"
				case count <= 20:
					countBucket = "2_20"
				case count <= 100:
					countBucket = "21_100"
				default:
					countBucket = "gt100"
				}
			}
		}
		elapsed := time.Since(started)
		elapsedBucket := "lt100ms"
		switch {
		case elapsed >= 250*time.Millisecond:
			elapsedBucket = "timeout"
		case elapsed >= 100*time.Millisecond:
			elapsedBucket = "100_249ms"
		}
		if terminal != "ok" {
			countBucket = "0"
		}
		parts = append(parts, stage.name+":"+terminal+":"+elapsedBucket+":"+countBucket)
	}
	return strings.Join(parts, ",")
}

// ExtractSearchFeedsFromDOM 从渲染后的搜索/首页卡片提取笔记信息。
func ExtractSearchFeedsFromDOM(page *hrod.Page) ([]Feed, error) {
	sources, err := extractSearchFeedSources(context.Background(), page, nil, false)
	if err != nil {
		return nil, err
	}
	if len(sources.DOM) == 0 {
		return nil, errors.ErrNoFeeds
	}
	return sources.DOM, nil
}

type searchFeedSources struct {
	DOM   []Feed `json:"dom"`
	State []Feed `json:"state"`
}

func extractSearchFeedSources(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter, includeState bool) (searchFeedSources, error) {
	result, err := evalJS(ctx, counter, page, `(selector, includeState) => {
		const clean = (value) => (value || "").replace(/\s+/g, " ").trim();
		const pickText = (root, selectors) => {
			for (const selector of selectors) {
				const el = root.querySelector(selector);
				const text = clean(el?.innerText || el?.textContent);
				if (text) return text;
			}
			return "";
		};
		const pickAttr = (root, selectors, attr) => {
			for (const selector of selectors) {
				const el = root.querySelector(selector);
				const value = el?.getAttribute(attr) || "";
				if (value) return value;
			}
			return "";
		};
		const noteIDFromHref = (href) => {
			const match = String(href || "").match(/\/(?:explore|discovery\/item)\/([^/?#]+)/);
			return match ? decodeURIComponent(match[1]) : "";
		};
		const xsecTokenFromHref = (href) => {
			try { return new URL(href, location.href).searchParams.get("xsec_token") || ""; }
			catch (_) { return ""; }
		};
		const countAfter = (text, labels) => {
			for (const label of labels) {
				const match = text.match(new RegExp(label + "\\s*([\\d.万wWkK]+)"));
				if (match) return match[1];
			}
			return "";
		};
		const pickImageURL = (img) => {
			if (!img) return "";
			for (const value of [
				img.currentSrc,
				img.getAttribute("src"),
				img.getAttribute("data-src"),
				img.getAttribute("data-original"),
				img.getAttribute("data-lazy-src"),
			]) {
				if (value && !value.startsWith("data:image")) return value;
			}
			const srcset = img.getAttribute("srcset") || img.getAttribute("data-srcset") || "";
			return srcset.split(",")[0]?.trim().split(/\s+/)[0] || "";
		};
		const userIDFromHref = (href) => {
			const match = String(href || "").match(/\/user\/profile\/([^/?#]+)/);
			return match ? decodeURIComponent(match[1]) : "";
		};

		const cards = Array.from(document.querySelectorAll(selector));
		const dom = cards.map((card, index) => {
			const links = Array.from(card.querySelectorAll("a[href]"));
			const noteLink = links.find((a) => /\/(?:explore|discovery\/item)\//.test(a.href)) || links[0];
			const href = noteLink?.href || "";
			const text = clean(card.innerText || card.textContent);
			const img = card.querySelector("img");
			const authorLink = links.find((a) => /\/user\/profile\//.test(a.href));
			const coverURL = pickImageURL(img);
			const title = pickText(card, [
				".title", ".note-title", ".footer .title", ".content .title",
				"[class*='title']", "a[title]",
			]) || clean(noteLink?.getAttribute("title")) || clean(noteLink?.textContent);
			const author = pickText(card, [
				".author .name", ".user .name", ".name", ".nickname",
				"[class*='author'] [class*='name']", "[class*='user'] [class*='name']",
			]);
			const avatar = pickAttr(card, [
				".author img", ".user img", ".avatar img", "img.avatar", "[class*='avatar'] img",
			], "src");
			const likedCount = pickText(card, [".like-wrapper .count", ".like-wrapper", "[class*='like'] [class*='count']"])
				|| countAfter(text, ["赞", "点赞"]);
			const commentCount = pickText(card, ["[class*='comment'] [class*='count']"]) || countAfter(text, ["评论"]);
			const collectedCount = pickText(card, ["[class*='collect'] [class*='count']"]) || countAfter(text, ["收藏"]);

			return {
				id: card.dataset?.noteId || card.getAttribute("data-note-id") || card.dataset?.id || noteIDFromHref(href),
				xsecToken: card.dataset?.xsecToken || card.getAttribute("data-xsec-token") || xsecTokenFromHref(href),
				modelType: card.dataset?.modelType || "",
				index,
				noteCard: {
					type: card.dataset?.noteType || (card.querySelector("video, [class*='video']") ? "video" : "normal"),
					displayTitle: title,
					user: {
						userId: card.dataset?.userId || userIDFromHref(authorLink?.href),
						nickname: author,
						nickName: author,
						avatar,
					},
					interactInfo: {
						likedCount,
						commentCount,
						collectedCount,
					},
					cover: {
						width: img?.naturalWidth || img?.width || 0,
						height: img?.naturalHeight || img?.height || 0,
						url: coverURL,
						urlDefault: coverURL,
						urlPre: coverURL,
					},
				},
			};
		}).filter((feed) => feed.id || feed.noteCard.displayTitle);
		const feeds = includeState ? window.__INITIAL_STATE__?.search?.feeds : null;
		const state = feeds?.value !== undefined ? feeds.value : (feeds?._value !== undefined ? feeds._value : feeds?._rawValue);
		return JSON.stringify({ dom, state: Array.isArray(state) ? state : [] });
	}`, SelectorFeedCard, includeState)
	if err != nil {
		return searchFeedSources{}, err
	}
	if result == nil || strings.TrimSpace(result.Value.Str()) == "" {
		return searchFeedSources{}, errors.ErrNoFeeds
	}

	var sources searchFeedSources
	if err := json.Unmarshal([]byte(result.Value.Str()), &sources); err != nil {
		return searchFeedSources{}, fmt.Errorf("解析搜索结果失败: %w", err)
	}
	return sources, nil
}

// ExtractFeedDetailFromDOM 从当前详情页可见 DOM 提取笔记、作者、评论和互动状态。
func ExtractFeedDetailFromDOM(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter, feedID string) (*FeedDetailResponse, error) {
	result, err := evalJS(ctx, counter, page, `(feedID, likeSel, collectSel) => {` + domCleanJS + domNoteHelpersJS + domCommentExtractorJS + `
		const interact = (() => {` + interactStateJS + `})();
		if (!interact) return "";

		const title = pickText(["#detail-title", ".note-content .title", ".title", "[class*='title']"]);
		const desc = pickText(["#detail-desc", ".note-content .desc", ".note-text", ".desc", "[class*='desc']"]);
		const author = pickText([".author .name", ".author-wrapper .name", ".user .name", ".nickname", "[class*='author'] [class*='name']"]);
		const avatar = pickAttr([".author img", ".user img", ".avatar img", "img.avatar"], "src");
		const images = Array.from(document.querySelectorAll(".swiper img, .note-content img, .media-container img"))
			.map((img) => ({ width: img.naturalWidth || 0, height: img.naturalHeight || 0, urlDefault: img.src || "", urlPre: img.src || "" }))
			.filter((img) => img.urlDefault);
		const comments = extractComments(feedID);

		const detail = {
			note: {
				noteId: feedID,
				xsecToken: (() => { try { return new URL(location.href).searchParams.get("xsec_token") || ""; } catch (_) { return ""; } })(),
				title,
				desc,
				type: document.querySelector("video") ? "video" : "normal",
				user: { nickname: author, nickName: author, avatar },
				interactInfo: {
					liked: interact.liked,
					collected: interact.collected,
					likedCount: countNear([".interact-container .like-lottie", ".interact-container .like-wrapper", ".interact-container [class*='like']"]),
					commentCount: countNear([".comments-container .total", ".comment-wrapper", "[class*='comment']"]),
					collectedCount: countNear([".interact-container .collect-icon", ".interact-container .collect-wrapper", ".interact-container [class*='collect']"])
				},
				imageList: images
			},
			comments: { list: comments, cursor: "", hasMore: false }
		};
		if (!title && !desc && comments.length === 0) return "";
		return JSON.stringify(detail);
	}`, feedID, SelectorLikeButton, SelectorCollectButton)
	if err != nil {
		return nil, fmt.Errorf("提取 DOM Feed 详情失败: %w", err)
	}
	if result == nil || strings.TrimSpace(result.Value.Str()) == "" {
		return nil, errors.ErrNoFeedDetail
	}

	var response FeedDetailResponse
	if err := json.Unmarshal([]byte(result.Value.Str()), &response); err != nil {
		return nil, fmt.Errorf("解析 DOM Feed 详情失败: %w", err)
	}
	return &response, nil
}

func ExtractCommentsFromDOM(ctx context.Context, page *hrod.Page, feedID string) ([]Comment, error) {
	snapshot, err := extractCommentsWithProgressFromDOM(ctx, page, feedID)
	if err != nil {
		return nil, err
	}
	return snapshot.Comments, nil
}

type commentsDOMSnapshot struct {
	Comments []Comment       `json:"comments"`
	Progress commentProgress `json:"progress"`
}

func extractCommentsWithProgressFromDOM(ctx context.Context, page *hrod.Page, feedID string) (commentsDOMSnapshot, error) {
	result, err := evalJSNoCounterRetryOnce(ctx, page, `(feedID) => {` + domCleanJS + domCommentExtractorJS + `
		const comments = extractComments(feedID);
		const totalText = (document.querySelector(".comments-container .total") ||
			document.querySelector(".comment-total") || document.querySelector(".total"))?.innerText || "";
		const totalMatch = totalText.match(/共\s*(\d+)\s*条评论/);
		const endText = document.querySelector(".end-container")?.textContent || "";
		const noCommentsText = document.querySelector(".no-comments-text")?.textContent || "";
		return JSON.stringify({
			comments,
			progress: {
				total: totalMatch ? Number(totalMatch[1]) : 0,
				atEnd: /THE\s*END/i.test(endText),
				noComments: noCommentsText.includes("这是一片荒地"),
			},
		});
	}`, feedID)
	if err != nil {
		return commentsDOMSnapshot{}, fmt.Errorf("提取 DOM 评论失败: %w", err)
	}
	if result == nil || strings.TrimSpace(result.Value.Str()) == "" {
		return commentsDOMSnapshot{}, errors.ErrNoFeedDetail
	}

	var snapshot commentsDOMSnapshot
	if err := json.Unmarshal([]byte(result.Value.Str()), &snapshot); err != nil {
		return commentsDOMSnapshot{}, fmt.Errorf("解析 DOM 评论失败: %w", err)
	}
	return snapshot, nil
}

// ExtractInteractStateFromDOM 复用唯一 href 状态片段读取点赞/收藏状态。
func ExtractInteractStateFromDOM(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter, feedID string) (bool, bool, error) {
	result, err := evalJS(ctx, counter, page, `(likeSel, collectSel) => {
		const interact = (() => {` + interactStateJS + `})();
		if (!interact) return "";
		return JSON.stringify(interact);
	}`, SelectorLikeButton, SelectorCollectButton)
	if err != nil {
		return false, false, err
	}
	if result == nil || strings.TrimSpace(result.Value.Str()) == "" {
		return false, false, errors.ErrNoFeedDetail
	}
	var state struct {
		Liked     bool `json:"liked"`
		Collected bool `json:"collected"`
	}
	if err := json.Unmarshal([]byte(result.Value.Str()), &state); err != nil {
		return false, false, fmt.Errorf("解析 DOM 互动状态失败: %w", err)
	}
	return state.Liked, state.Collected, nil
}

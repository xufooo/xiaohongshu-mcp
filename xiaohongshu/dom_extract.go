package xiaohongshu

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xpzouying/xiaohongshu-mcp/errors"
	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

// ExtractSearchFeedsFromDOM 从渲染后的搜索/首页卡片提取笔记信息。
func ExtractSearchFeedsFromDOM(page *hrod.Page) ([]Feed, error) {
	result, err := page.Eval(`(selector) => {
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

		const cards = Array.from(document.querySelectorAll(selector));
		return JSON.stringify(cards.map((card, index) => {
			const links = Array.from(card.querySelectorAll("a[href]"));
			const noteLink = links.find((a) => /\/(?:explore|discovery\/item)\//.test(a.href)) || links[0];
			const href = noteLink?.href || "";
			const text = clean(card.innerText || card.textContent);
			const img = card.querySelector("img");
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

			return {
				id: card.dataset?.noteId || card.dataset?.id || noteIDFromHref(href),
				xsecToken: xsecTokenFromHref(href),
				modelType: "",
				index,
				noteCard: {
					type: card.querySelector("video") ? "video" : "normal",
					displayTitle: title,
					user: { nickname: author, nickName: author, avatar },
					interactInfo: {
						likedCount: countAfter(text, ["赞", "点赞"]),
						commentCount: countAfter(text, ["评论"]),
						collectedCount: countAfter(text, ["收藏"])
					},
					cover: { url: img?.src || "", urlDefault: img?.src || "", urlPre: img?.src || "" }
				}
			};
		}).filter((feed) => feed.id || feed.noteCard.displayTitle));
	}`, SelectorFeedCard)
	if err != nil {
		return nil, err
	}
	if result == nil || strings.TrimSpace(result.Value.Str()) == "" {
		return nil, errors.ErrNoFeeds
	}

	var feeds []Feed
	if err := json.Unmarshal([]byte(result.Value.Str()), &feeds); err != nil {
		return nil, fmt.Errorf("解析 DOM 搜索结果失败: %w", err)
	}
	if len(feeds) == 0 {
		return nil, errors.ErrNoFeeds
	}
	return feeds, nil
}

// ExtractFeedDetailFromDOM 从当前详情页可见 DOM 提取笔记、作者、评论和互动状态。
func ExtractFeedDetailFromDOM(page *hrod.Page, feedID string) (*FeedDetailResponse, error) {
	result, err := page.Eval(`(feedID) => {
		const clean = (value) => (value || "").replace(/\s+/g, " ").trim();
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
		const isActive = (selectors) => selectors.some((selector) => {
			const el = document.querySelector(selector);
			if (!el) return false;
			const value = [
				el.getAttribute("aria-pressed"),
				el.getAttribute("aria-checked"),
				el.getAttribute("data-active"),
				el.className,
				el.getAttribute("class"),
				el.getAttribute("style"),
			].join(" ").toLowerCase();
			return /\btrue\b|active|selected|liked|collected|--active|fill|color:\s*rgb/.test(value);
		});
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

		const title = pickText(["#detail-title", ".note-content .title", ".title", "[class*='title']"]);
		const desc = pickText(["#detail-desc", ".note-content .desc", ".note-text", ".desc", "[class*='desc']"]);
		const author = pickText([".author .name", ".author-wrapper .name", ".user .name", ".nickname", "[class*='author'] [class*='name']"]);
		const avatar = pickAttr([".author img", ".user img", ".avatar img", "img.avatar"], "src");
		const images = Array.from(document.querySelectorAll(".swiper img, .note-content img, .media-container img"))
			.map((img) => ({ width: img.naturalWidth || 0, height: img.naturalHeight || 0, urlDefault: img.src || "", urlPre: img.src || "" }))
			.filter((img) => img.urlDefault);
		const comments = Array.from(document.querySelectorAll(".parent-comment")).map((parent) => {
			const top = parent.querySelector(":scope > .comment-item") || parent;
			const content = clean(top.querySelector(".content, .note-text, [class*='content']")?.innerText || top.innerText);
			const user = clean(top.querySelector(".author-wrapper .name, .name, .nickname, [class*='name']")?.innerText);
			const likeText = clean(top.querySelector(".interactions .like, .like, [class*='like']")?.innerText);
			const subComments = Array.from(parent.querySelectorAll(":scope > .children-comments > .comment-item-sub, :scope > .reply-container > .list-container > .comment-item")).map((sub) => {
				const subContent = clean(sub.querySelector(".content, .note-text, [class*='content']")?.innerText || sub.innerText);
				const subUser = clean(sub.querySelector(".author-wrapper .name, .name, .nickname, [class*='name']")?.innerText);
				const subLikeText = clean(sub.querySelector(".interactions .like, .like, [class*='like']")?.innerText);
				return {
					id: sub.dataset?.id || sub.getAttribute("data-comment-id") || "",
					noteId: feedID,
					content: subContent,
					likeCount: (subLikeText.match(/([\d.万wWkK]+)/) || ["", ""])[1],
					userInfo: { nickname: subUser, nickName: subUser },
					subComments: [],
					showTags: []
				};
			}).filter((subComment) => subComment.content);
			return {
				id: parent.dataset?.id || parent.getAttribute("data-comment-id") || top.dataset?.id || top.getAttribute("data-comment-id") || "",
				noteId: feedID,
				content,
				likeCount: (likeText.match(/([\d.万wWkK]+)/) || ["", ""])[1],
				userInfo: { nickname: user, nickName: user },
				subCommentCount: subComments.length ? String(subComments.length) : "",
				subComments,
				showTags: []
			};
		}).filter((comment) => comment.content);

		const liked = isActive([".interact-container .like-lottie", ".interact-container .like-wrapper", ".interact-container [class*='like']"]);
		const collected = isActive([".interact-container .collect-icon", ".interact-container .collect-wrapper", ".interact-container [class*='collect']"]);
		const detail = {
			note: {
				noteId: feedID,
				xsecToken: (() => { try { return new URL(location.href).searchParams.get("xsec_token") || ""; } catch (_) { return ""; } })(),
				title,
				desc,
				type: document.querySelector("video") ? "video" : "normal",
				user: { nickname: author, nickName: author, avatar },
				interactInfo: {
					liked,
					collected,
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
	}`, feedID)
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

// ExtractInteractStateFromDOM 从详情页可见按钮状态读取点赞/收藏状态。
func ExtractInteractStateFromDOM(page *hrod.Page, feedID string) (bool, bool, error) {
	result, err := page.Eval(`() => {
		const find = (selectors) => {
			for (const selector of selectors) {
				const el = document.querySelector(selector);
				if (el) return el;
			}
			return null;
		};
		const active = (el) => {
			if (!el) return false;
			const value = [
				el.getAttribute("aria-pressed"),
				el.getAttribute("aria-checked"),
				el.getAttribute("data-active"),
				el.className,
				el.getAttribute("class"),
				el.getAttribute("style"),
			].join(" ").toLowerCase();
			return /\btrue\b|active|selected|liked|collected|--active|fill|color:\s*rgb/.test(value);
		};
		const like = find([".interact-container .like-lottie", ".interact-container .like-wrapper", ".interact-container [class*='like']"]);
		const collect = find([".interact-container .collect-icon", ".interact-container .collect-wrapper", ".interact-container [class*='collect']"]);
		if (!like && !collect) return "";
		return JSON.stringify({ liked: active(like), collected: active(collect) });
	}`)
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

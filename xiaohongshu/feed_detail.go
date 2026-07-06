package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/avast/retry-go/v4"
	"github.com/go-rod/rod"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/errors"
	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

// ========== 配置常量 ==========
const (
	commentPollInterval    = 100 * time.Millisecond
	// The note is available before the asynchronously populated comment ref on
	// some versions of the web client. Keep this short: it is only used when
	// the note reports comments but the state snapshot has none.
	initialCommentStateTimeout = 5 * time.Second
)

const (
	feedDetailPageTimeout = 10 * time.Minute
	commentLoadTimeout    = 9 * time.Minute
)

// ========== 数据结构 ==========

type CommentLoadConfig struct {
	ClickMoreReplies    bool
	MaxRepliesThreshold int
	MaxCommentItems     int
	ScrollSpeed         string
}

func DefaultCommentLoadConfig() CommentLoadConfig {
	return CommentLoadConfig{
		ClickMoreReplies:    false,
		MaxRepliesThreshold: 10,
		MaxCommentItems:     0,
		ScrollSpeed:         "fast",
	}
}

type FeedDetailAction struct {
	page  *hrod.Page
	state *ActionStateStore
}

func NewFeedDetailAction(page *hrod.Page) *FeedDetailAction {
	return &FeedDetailAction{page: page}
}

func NewFeedDetailActionWithState(page *hrod.Page, state *ActionStateStore) *FeedDetailAction {
	return &FeedDetailAction{page: page, state: state}
}

// ========== 主要业务逻辑 ==========

func (f *FeedDetailAction) GetFeedDetail(ctx context.Context, feedID, xsecToken string, loadAllComments bool, config CommentLoadConfig) (*FeedDetailResponse, error) {
	return f.GetFeedDetailWithConfig(ctx, feedID, xsecToken, loadAllComments, config)
}

func (f *FeedDetailAction) GetFeedDetailWithConfig(ctx context.Context, feedID, xsecToken string, loadAllComments bool, config CommentLoadConfig) (*FeedDetailResponse, error) {
	if err := validateFeedAccessArgs(feedID, xsecToken); err != nil {
		return nil, err
	}

	config = normalizeCommentLoadConfig(config)
	page := f.page.Context(ctx).Timeout(feedDetailPageTimeout)
	url := makeFeedDetailURL(feedID, xsecToken)

	logrus.Infof("打开 feed 详情页: %s", url)
	logrus.Infof("配置: 点击更多=%v, 回复阈值=%d, 最大评论数=%d, 滚动速度=%s",
		config.ClickMoreReplies, config.MaxRepliesThreshold, config.MaxCommentItems, config.ScrollSpeed)

	opener := NewNoteOpenActionWithState(page, f.state)
	if err := opener.OpenFromCards(ctx, feedID, xsecToken, ""); err != nil {
		logrus.Warnf("从卡片打开笔记失败，使用详情 URL 兜底: %v", err)
		// XHS continuously mutates the document after navigation. Waiting for DOM
		// stability can therefore consume the full request deadline even though the
		// note state is already available.
		err := retry.Do(
			func() error {
				return opener.OpenByURLFallback(ctx, feedID, xsecToken)
			},
			retry.Attempts(3),
			retry.Delay(500*time.Millisecond),
			retry.MaxJitter(1000*time.Millisecond),
			retry.OnRetry(func(n uint, err error) {
				logrus.Debugf("页面导航重试 #%d: %v", n, err)
			}),
		)
		if err != nil {
			logrus.Errorf("页面导航失败: %v", err)
			return nil, err
		}
	}
	if err := sleepRandom(page, 1000, 1000); err != nil {
		return nil, err
	}

	if err := checkPageAccessible(page); err != nil {
		return nil, err
	}
	readDuration := 20 * time.Second
	if loadAllComments {
		readDuration = 5 * time.Second
	}
	reader := NewReadStageAction(page, f.state)
	if err := reader.Read(ctx, feedID, readDuration); err != nil {
		return nil, fmt.Errorf("阅读阶段失败: %w", err)
	}
	if !loadAllComments {
		return f.extractFeedDetail(page, feedID)
	}

	// Keep the server-provided first page before interacting with the comment
	// area. Scrolling changes the client-side store on some site versions; if
	// that later snapshot is incomplete, this remains a valid partial result.
	initialDetail, initialDetailErr := f.extractFeedDetail(page, feedID)
	if initialDetailErr != nil {
		logrus.Debugf("加载评论前提取详情失败: %v", initialDetailErr)
	}

	// Bound the scroll loop independently so comment loading failures return a
	// scoped error instead of consuming the main page context.
	commentPage := page.Timeout(commentLoadTimeout)
	commentStart := time.Now()
	commentLoadErr := loadCommentsByJS(commentPage, config)
	reader.RecordCommentDwell(feedID, time.Since(commentStart), true)
	if commentLoadErr != nil {
		if strings.Contains(commentLoadErr.Error(), "context deadline exceeded") ||
			strings.Contains(commentLoadErr.Error(), "timeout") {
			logrus.Warnf("评论加载超时(%s)，返回已加载数据: %v", time.Since(commentStart).Round(time.Second), commentLoadErr)
		} else {
			logrus.Warnf("加载全部评论失败，返回已加载数据: %v", commentLoadErr)
		}
	}

	detail, err := f.extractFeedDetail(page, feedID)
	if err != nil {
		if commentLoadErr != nil {
			return nil, fmt.Errorf("加载全部评论失败: %v；提取详情失败: %w", commentLoadErr, err)
		}
		if initialDetail != nil {
			logrus.Warnf("评论加载后提取详情失败，返回加载前快照: %v", err)
			return initialDetail, nil
		}
		return nil, err
	}
	if shouldUseInitialCommentSnapshot(initialDetail, detail) {
		logrus.Warn("评论加载后的状态快照为空，返回加载前的评论列表")
		return initialDetail, nil
	}
	return detail, nil
}

func normalizeCommentLoadConfig(config CommentLoadConfig) CommentLoadConfig {
	switch config.ScrollSpeed {
	case "slow", "normal", "fast":
	default:
		config.ScrollSpeed = DefaultCommentLoadConfig().ScrollSpeed
	}
	return config
}

func sessionCommentPageLoadConfig(progress commentProgress, progressErr error) CommentLoadConfig {
	config := DefaultCommentLoadConfig()
	if progressErr == nil {
		config.MaxCommentItems = progress.Count + rand.Intn(6) + 5
		if progress.Total > 0 && config.MaxCommentItems > progress.Total {
			config.MaxCommentItems = progress.Total
		}
	} else {
		config.MaxCommentItems = 10
	}
	return config
}

// ========== 评论加载器 ==========

// commentProgress is collected in one browser evaluation. Keeping the check in
// the browser avoids several round trips per scroll on slower devices.
type commentProgress struct {
	Count      int  `json:"count"`
	Total      int  `json:"total"`
	AtEnd      bool `json:"atEnd"`
	NoComments bool `json:"noComments"`
}

func loadCommentsByJS(page *hrod.Page, config CommentLoadConfig) error {
	logrus.Info("开始加载评论(JS)...")
	result, err := page.Eval(loadCommentsScript(), config.MaxCommentItems, config.ScrollSpeed)
	if err != nil {
		return fmt.Errorf("JS加载失败: %w", err)
	}
	if result == nil {
		return fmt.Errorf("JS加载无返回")
	}
	logrus.Infof("JS结果: %s", result.Value.Str())
	return nil
}

func loadCommentsScript() string {
	return `(maxItems, speed) => {
		const delay={slow:1200,normal:700,fast:400}[speed]||700;
		const MAX=200, slp=ms=>new Promise(r=>setTimeout(r,ms));
		// Find scrollable ancestor of the comments area
		function findScrollContainer(){
			const cc=document.querySelector(".comments-container")||document.querySelector(".interaction-container");
			if(!cc) return document.documentElement;
			cc.scrollIntoView({block:"center"});
			let el=cc;
			for(let i=0;i<10;i++){
				const p=el.parentElement;
				if(!p) break;
				if(p.scrollHeight>p.clientHeight+5) return p;
				el=p;
			}
			return cc;
		}
		const ct=findScrollContainer();
		// Scroll last comment into view to trigger lazy load
		function scrollLast(){
			const all=document.querySelectorAll(".parent-comment");
			if(all.length>0){
				const last=all[all.length-1];
				last.scrollIntoView({block:"nearest",behavior:"instant"});
			}else{
				ct.scrollBy(0,300);
			}
		}
		return (async()=>{
			await slp(800);
			for(let i=0;i<MAX;i++){
				const n=document.querySelectorAll(".parent-comment").length;
				const e=document.querySelector(".end-container");
				const end=e&&/THE\\s*END/i.test(e.textContent||"");
				if((maxItems>0&&n>=maxItems)||end) return JSON.stringify({count:n,reachedEnd:end,rounds:i+1,status:"ok"});
				scrollLast();
				await slp(delay);
			}
			return JSON.stringify({count:document.querySelectorAll(".parent-comment").length,reachedEnd:false,rounds:MAX,status:"max_rounds"});
		})();
	}`
}

func sleepRandom(page *hrod.Page, minMs, maxMs int) error {
	return page.SleepRandom(time.Duration(minMs)*time.Millisecond, time.Duration(maxMs)*time.Millisecond)
}

// scrollToCommentsArea 使用JS定位到评论区（comment_feed.go 引用）
func scrollToCommentsArea(page *hrod.Page) error {
	_, err := page.Eval(`() => {
		const cc = document.querySelector('.comments-container');
		if (cc) { cc.scrollIntoView({block:'center'}); }
	}`)
	return err
}

// ========== DOM 查询 ==========

func getCommentProgress(page *hrod.Page) (commentProgress, error) {
	var progress commentProgress

	result, err := page.Eval(`() => {
		const totalEl = document.querySelector(".comments-container .total") ||
			document.querySelector(".comment-total") ||
			document.querySelector(".total");
		const totalText = totalEl?.innerText || "";
		const totalMatch = totalText.match(/共\s*(\d+)\s*条评论/);
		const endText = document.querySelector(".end-container")?.textContent || "";
		const noCommentsText = document.querySelector(".no-comments-text")?.textContent || "";

		return JSON.stringify({
			count: document.querySelectorAll(".parent-comment").length,
			total: totalMatch ? Number(totalMatch[1]) : 0,
			atEnd: /THE\s*END/i.test(endText),
			noComments: noCommentsText.includes("这是一片荒地"),
		});
	}`)
	if err != nil {
		return progress, err
	}
	if result == nil {
		return progress, fmt.Errorf("读取评论加载状态未返回结果")
	}
	if err := json.Unmarshal([]byte(result.Value.Str()), &progress); err != nil {
		return progress, fmt.Errorf("解析评论加载状态: %w", err)
	}
	return progress, nil
}

func getScrollTop(page *hrod.Page) int {
	var result int

	// 使用retry-go来处理可能的DOM查询失败
	err := retry.Do(
		func() error {
			evalResult, err := page.Eval(`() => {
				return window.pageYOffset || document.documentElement.scrollTop || document.body.scrollTop || 0;
			}`)
			if err != nil {
				return err
			}
			if evalResult == nil {
				return fmt.Errorf("读取滚动位置未返回结果")
			}

			result = evalResult.Value.Int()
			return nil
		},
		retry.Attempts(3),
		retry.Delay(100*time.Millisecond),
		retry.MaxJitter(200*time.Millisecond),
		retry.OnRetry(func(n uint, err error) {
			logrus.Debugf("获取滚动位置重试 #%d: %v", n, err)
		}),
	)

	if err != nil {
		logrus.Warnf("获取滚动位置失败: %v", err)
		return 0 // 失败时返回0
	}

	return result
}

func getCommentCount(page *hrod.Page) int {
	var result int

	// 使用retry-go来处理可能的DOM查询失败
	err := retry.Do(
		func() error {
			// 使用 Go 获取评论元素
			elements, err := page.Timeout(2 * time.Second).Elements(".parent-comment")
			if err != nil {
				return err
			}
			result = len(elements)
			return nil
		},
		retry.Attempts(3),
		retry.Delay(100*time.Millisecond),
		retry.MaxJitter(200*time.Millisecond),
		retry.OnRetry(func(n uint, err error) {
			logrus.Debugf("获取评论计数重试 #%d: %v", n, err)
		}),
	)

	if err != nil {
		logrus.Warnf("获取评论计数失败: %v", err)
		return 0 // 失败时返回0
	}

	return result
}

func getTotalCommentCount(page *hrod.Page) int {
	var result int

	// 使用retry-go来处理可能的DOM查询失败
	err := retry.Do(
		func() error {
			// 使用 Go 获取总评论数元素，多选择器备用
			totalEl, err := page.Timeout(2 * time.Second).Element(".comments-container .total")
			if err != nil {
				// 备用选择器
				totalEl, err = page.Timeout(1 * time.Second).Element(".comment-total")
				if err != nil {
					totalEl, err = page.Timeout(1 * time.Second).Element(".total")
				}
			}
			if err != nil {
				return err
			}

			// 获取文本内容
			text, err := totalEl.Text()
			if err != nil {
				return err
			}

			// 使用正则提取数字
			re := regexp.MustCompile(`共(\d+)条评论`)
			matches := re.FindStringSubmatch(text)
			if len(matches) > 1 {
				count, err := strconv.Atoi(matches[1])
				if err != nil {
					return err
				}
				result = count
			} else {
				result = 0
			}

			return nil
		},
		retry.Attempts(3),
		retry.Delay(100*time.Millisecond),
		retry.MaxJitter(200*time.Millisecond),
		retry.OnRetry(func(n uint, err error) {
			logrus.Debugf("获取总评论计数重试 #%d: %v", n, err)
		}),
	)

	if err != nil {
		logrus.Warnf("获取总评论计数失败: %v", err)
		return 0 // 失败时返回0
	}

	return result
}

func checkNoCommentsArea(page *hrod.Page) bool {
	// 查找无评论区域
	noCommentsEl, err := page.Timeout(2 * time.Second).Element(".no-comments-text")
	if err != nil {
		// 未找到无评论元素，说明有评论或评论区正常
		return false
	}

	// 获取文本内容
	text, err := noCommentsEl.Text()
	if err != nil {
		return false
	}

	// 检查是否包含"这是一片荒地"等关键词
	text = strings.TrimSpace(text)
	return strings.Contains(text, "这是一片荒地")
}

func checkEndContainer(page *hrod.Page) bool {
	var result bool

	// 使用retry-go来处理可能的DOM查询失败
	err := retry.Do(
		func() error {
			// 使用 Go 查找结束容器
			endEl, err := page.Timeout(2 * time.Second).Element(".end-container")
			if err != nil {
				// 未找到元素，说明未到底部
				result = false
				return nil
			}

			// 获取文本内容
			text, err := endEl.Text()
			if err != nil {
				result = false
				return nil
			}

			// 转换为大写并检查
			textUpper := strings.ToUpper(strings.TrimSpace(text))
			result = strings.Contains(textUpper, "THE END") || strings.Contains(textUpper, "THEEND")
			return nil
		},
		retry.Attempts(3),
		retry.Delay(100*time.Millisecond),
		retry.MaxJitter(200*time.Millisecond),
		retry.OnRetry(func(n uint, err error) {
			logrus.Debugf("检查结束容器重试 #%d: %v", n, err)
		}),
	)

	if err != nil {
		logrus.Warnf("检查结束容器失败: %v", err)
		return false // 失败时返回false
	}

	return result
}

// ========== 页面检查 ==========

func checkPageAccessible(page *hrod.Page) error {
	if err := page.Sleep(500 * time.Millisecond); err != nil {
		return err
	}

	// 查找错误提示容器
	wrapperEl, err := page.Timeout(2 * time.Second).Element(".access-wrapper, .error-wrapper, .not-found-wrapper, .blocked-wrapper")
	if err != nil {
		// 未找到错误容器，说明页面可访问
		return nil
	}

	// 获取文本内容
	text, err := wrapperEl.Text()
	if err != nil {
		// 无法获取文本，假设页面可访问
		return nil
	}

	// 检查关键词
	keywords := []string{
		"当前笔记暂时无法浏览",
		"该内容因违规已被删除",
		"该笔记已被删除",
		"内容不存在",
		"笔记不存在",
		"已失效",
		"私密笔记",
		"仅作者可见",
		"因用户设置，你无法查看",
		"因违规无法查看",
	}

	for _, kw := range keywords {
		if strings.Contains(text, kw) {
			logrus.Warnf("笔记不可访问: %s", kw)
			return fmt.Errorf("笔记不可访问: %s", kw)
		}
	}

	// 如果有文本但不匹配关键词，返回未知错误
	trimmedText := strings.TrimSpace(text)
	if trimmedText != "" {
		logrus.Warnf("笔记不可访问（未知原因）: %s", trimmedText)
		return fmt.Errorf("笔记不可访问: %s", trimmedText)
	}

	return nil
}

// ========== 数据提取 ==========

func (f *FeedDetailAction) extractFeedDetail(page *hrod.Page, feedID string) (*FeedDetailResponse, error) {
	if err := page.Wait(rod.Eval(`(id, selector, deadline) => {
		const s = window.__INITIAL_STATE__;
		const hasState = s?.note?.noteDetailMap?.[id] != null;
		const hasDOM = document.querySelector(selector) !== null;
		return hasDOM || hasState || Date.now() >= deadline;
	}`, feedID, SelectorFeedDetailReady, time.Now().Add(10*time.Second).UnixMilli())); err != nil {
		return nil, fmt.Errorf("等待笔记详情加载失败: %w", err)
	}

	deadline := time.Now().Add(initialCommentStateTimeout)
	var lastErr error

	for {
		response, err := ExtractFeedDetailFromDOM(page, feedID)
		if err != nil {
			lastErr = err
			// DOM 结构变更或虚拟列表未渲染时，降级读取 __INITIAL_STATE__。
			response, err = readFeedDetailState(page, feedID)
			if err != nil {
				lastErr = err
			}
		}

		// A non-zero displayed count with an empty list is a transient state while
		// the web client hydrates its comments ref. Do not return that incomplete
		// snapshot as a successful result. A genuinely empty or unavailable list
		// still returns after the short bounded wait.
		if response != nil && (!shouldWaitForInitialComments(response) || time.Now().After(deadline)) {
			return response, nil
		}
		if time.Now().After(deadline) && lastErr != nil {
			return nil, lastErr
		}

		if response != nil {
			logrus.Debugf("评论 DOM 尚未就绪: note=%s, reported=%s", feedID, response.Note.InteractInfo.CommentCount)
		}
		if err := page.Sleep(commentPollInterval); err != nil {
			return nil, err
		}
	}
}

// readFeedDetailState normalizes Vue refs before serializing the state. The
// site has used both direct values and ref wrappers (value/_value) for
// noteDetailMap and comments. json.Unmarshal silently turns a wrapped comments
// value into an empty CommentList, so unwrapping must happen in the page.
func readFeedDetailState(page *hrod.Page, feedID string) (*FeedDetailResponse, error) {
	var response *FeedDetailResponse
	err := retry.Do(
		func() error {
			var err error
			response, err = readFeedDetailStateOnce(page, feedID)
			return err
		},
		retry.Attempts(3),
		retry.Delay(200*time.Millisecond),
		retry.MaxJitter(300*time.Millisecond),
		retry.OnRetry(func(n uint, err error) {
			logrus.Debugf("提取Feed详情重试 #%d: %v", n, err)
		}),
	)
	if err != nil {
		return nil, err
	}
	return response, nil
}

func readFeedDetailStateOnce(page *hrod.Page, feedID string) (*FeedDetailResponse, error) {
	result, err := page.Eval(`(feedID) => {
		const hasOwn = (value, key) => Object.prototype.hasOwnProperty.call(value, key);
		const isObject = (value) => value !== null && typeof value === "object";

		const unwrapRef = (value) => {
			const seen = new Set();
			let current = value;
			while (isObject(current) && !seen.has(current)) {
				seen.add(current);
				if ((current.__v_isReactive || current.__v_isReadonly) &&
					isObject(current.__v_raw) && current.__v_raw !== current) {
					current = current.__v_raw;
					continue;
				}
				if (current.__v_isRef === true) {
					const next = current.value;
					if (next === current) break;
					current = next;
					continue;
				}
				if (hasOwn(current, "_value")) {
					const next = current._value;
					if (next === current) break;
					current = next;
					continue;
				}
				if (hasOwn(current, "value")) {
					const next = current.value;
					if (next === current) break;
					current = next;
					continue;
				}
				break;
			}
			return current;
		};

		// JSON.stringify invokes getters and proxy traps. Its replacer also sees
		// nested refs which are not covered by unwrapping just note/comments.
		// Parsing the JSON result makes the evaluated value a plain, deep snapshot
		// before it crosses the Go/CDP boundary.
		const snapshot = (value) => {
			const json = JSON.stringify(unwrapRef(value), (_key, nested) => unwrapRef(nested));
			return json === undefined ? undefined : JSON.parse(json);
		};

		const state = window.__INITIAL_STATE__;
		const noteState = unwrapRef(state?.note);
		const noteDetailMap = unwrapRef(noteState?.noteDetailMap);
		const detail = unwrapRef(noteDetailMap?.[feedID]);
		if (!detail) return "";

		return JSON.stringify(snapshot({
			note: detail.note,
			comments: detail.comments,
		}));
	}`, feedID)
	if err != nil {
		return nil, fmt.Errorf("提取Feed详情失败: %w", err)
	}
	if result == nil || result.Value.Str() == "" {
		return nil, errors.ErrNoFeedDetail
	}

	var noteDetail struct {
		Note     FeedDetail  `json:"note"`
		Comments CommentList `json:"comments"`
	}
	if err := json.Unmarshal([]byte(result.Value.Str()), &noteDetail); err != nil {
		return nil, fmt.Errorf("failed to unmarshal feed detail: %w", err)
	}

	return &FeedDetailResponse{Note: noteDetail.Note, Comments: noteDetail.Comments}, nil
}

func shouldWaitForInitialComments(response *FeedDetailResponse) bool {
	if len(response.Comments.List) != 0 {
		return false
	}
	commentCount, err := strconv.Atoi(strings.TrimSpace(response.Note.InteractInfo.CommentCount))
	return err == nil && commentCount > 0
}

func shouldUseInitialCommentSnapshot(initial, current *FeedDetailResponse) bool {
	return initial != nil && current != nil &&
		len(initial.Comments.List) > 0 && len(current.Comments.List) == 0
}

func makeFeedDetailURL(feedID, xsecToken string) string {
	return fmt.Sprintf("https://www.xiaohongshu.com/explore/%s?xsec_token=%s&xsec_source=pc_feed", feedID, xsecToken)
}

func validateFeedAccessArgs(feedID, xsecToken string) error {
	if strings.TrimSpace(feedID) == "" {
		return fmt.Errorf("缺少feed_id参数")
	}
	if strings.TrimSpace(xsecToken) == "" {
		return fmt.Errorf("缺少xsec_token参数")
	}
	return nil
}

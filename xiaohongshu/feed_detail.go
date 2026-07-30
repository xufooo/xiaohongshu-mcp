package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/avast/retry-go/v4"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/errors"
	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

// ========== 配置常量 ==========
const (
	commentPollInterval       = 100 * time.Millisecond
	replyExpansionRetryDelay  = time.Second
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

type CommentCursor struct {
	FeedID      string    `json:"feed_id"`
	Round       int       `json:"round"`
	ReturnedIDs []string  `json:"returned_ids"`
	ExpandRound int       `json:"expand_round"`
	CreatedAt   time.Time `json:"created_at"`
	TotalKnown  bool      `json:"total_known,omitempty"`
	Total       int       `json:"total,omitempty"`
}

func DefaultCommentLoadConfig() CommentLoadConfig {
	return CommentLoadConfig{
		ClickMoreReplies:    false,
		MaxRepliesThreshold: 10,
		MaxCommentItems:     0,
		ScrollSpeed:         "fast",
	}
}

type commentBatchDOMState struct {
	RootFound         bool      `json:"rootFound"`
	RootAmbiguous     bool      `json:"rootAmbiguous"`
	ScrollportFound   bool      `json:"scrollportFound"`
	RootKey           string    `json:"rootKey"`
	Moved             bool      `json:"moved"`
	ScrollTop         float64   `json:"scrollTop"`
	ScrollHeight      float64   `json:"scrollHeight"`
	ClientHeight      float64   `json:"clientHeight"`
	AtBottom          bool      `json:"atBottom"`
	TotalKnown        bool      `json:"totalKnown"`
	Total             int       `json:"total"`
	EndVisible        bool      `json:"endVisible"`
	NoCommentsVisible bool      `json:"noCommentsVisible"`
	MissingIDCount    int       `json:"missingIdCount"`
	Comments          []Comment `json:"comments"`
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

	logrus.Infof("从卡片打开 feed 详情页: %s", feedID)
	logrus.Infof("配置: 点击更多=%v, 回复阈值=%d, 最大评论数=%d, 滚动速度=%s",
		config.ClickMoreReplies, config.MaxRepliesThreshold, config.MaxCommentItems, config.ScrollSpeed)

	opener := NewNoteOpenActionWithState(page, f.state)
	if err := opener.OpenFromCards(ctx, feedID, xsecToken, ""); err != nil {
		return nil, fmt.Errorf("从卡片打开笔记失败，请重新搜索或滚动后重试: %w", err)
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

func (f *FeedDetailAction) GetFeedDetailCommentsBatch(ctx context.Context, feedID, xsecToken string, cursor *CommentCursor, maxItems int, config CommentLoadConfig) (*FeedDetailResponse, *CommentCursor, bool, error) {
	if err := validateFeedAccessArgs(feedID, xsecToken); err != nil {
		return nil, nil, false, err
	}

	config = normalizeCommentLoadConfig(config)
	page := f.page.Context(ctx).Timeout(feedDetailPageTimeout)

	logrus.Infof("从卡片打开 feed 详情页(评论分批): %s", feedID)
	opener := NewNoteOpenActionWithState(page, f.state)
	if err := opener.OpenFromCards(ctx, feedID, xsecToken, ""); err != nil {
		return nil, nil, false, fmt.Errorf("从卡片打开笔记失败，请重新搜索或滚动后重试: %w", err)
	}
	if err := sleepRandom(page, 1000, 1000); err != nil {
		return nil, nil, false, err
	}
	if err := checkPageAccessible(page); err != nil {
		return nil, nil, false, err
	}

	reader := NewReadStageAction(page, f.state)
	if err := reader.Read(ctx, feedID, 5*time.Second); err != nil {
		return nil, nil, false, fmt.Errorf("阅读阶段失败: %w", err)
	}

	detail, err := f.extractFeedDetail(page, feedID)
	if err != nil {
		return nil, nil, false, err
	}

	commentPage := page.Timeout(commentLoadTimeout)
	commentStart := time.Now()
	comments, nextCursor, hasMore, err := LoadCommentsBatch(ctx, commentPage, config, cursor, maxItems)
	reader.RecordCommentDwell(feedID, time.Since(commentStart), true)
	if err != nil {
		return nil, nil, false, err
	}
	if nextCursor != nil && nextCursor.FeedID == "" {
		nextCursor.FeedID = feedID
	}

	detail.Comments = CommentList{
		List:    comments,
		HasMore: hasMore,
	}
	if nextCursor != nil && nextCursor.TotalKnown && nextCursor.Total > 0 {
		detail.Comments.TotalItems = nextCursor.Total
	} else if totalItems := knownCommentTotal(commentPage); totalItems > 0 {
		detail.Comments.TotalItems = totalItems
	}
	return detail, nextCursor, hasMore, nil
}

func knownCommentTotal(page *hrod.Page) int {
	progress, err := getCommentProgress(page)
	if err != nil || progress.Total <= 0 {
		return 0
	}
	return progress.Total
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
	config.ClickMoreReplies = true
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
	logrus.Info("开始加载评论(note-scroller JS scrollBy)...")
	logrus.Infof("配置: maxItems=%d, speed=%s", config.MaxCommentItems, config.ScrollSpeed)

	await, scrollDelta := commentScrollSettings(config.ScrollSpeed)
	maxRounds := 500
	commentDeadline := time.Now().Add(commentLoadTimeout)
	remainingDeadline := func() time.Duration {
		return time.Until(commentDeadline)
	}

	// 先确保评论区可见
	if err := scrollToCommentsArea(page); err != nil {
		logrus.Warnf("定位评论区失败: %v", err)
	}
	if err := page.Sleep(time.Second); err != nil {
		return err
	}

	lastCount := -1
	staleChecks := 0
	const maxStaleChecks = 20 // 连续20次无增长即退出

	progressFunc := func() (int, bool, bool) {
		result, err := page.Timeout(2*time.Second).Eval(commentProgressScript())
		if err != nil {
			logrus.Warnf("评论进度检查失败: %v", err)
			return lastCount, false, false
		}
		if result == nil {
			return lastCount, false, false
		}
		var p struct {
			Count      int  `json:"count"`
			AtEnd      bool `json:"atEnd"`
			NoComments bool `json:"noComments"`
		}
		if err := json.Unmarshal([]byte(result.Value.Str()), &p); err != nil {
			return lastCount, false, false
		}
		return p.Count, p.AtEnd, p.NoComments
	}

	for i := 0; i < maxRounds; i++ {
		if remaining := remainingDeadline(); remaining < 30*time.Second {
			logrus.Warnf("评论加载剩余时间不足(%s)，停止新滚动", remaining.Round(time.Second))
			break
		}
		moved, scrollErr := scrollNoteScrollerMoved(page, scrollDelta)
		if scrollErr != nil {
			return fmt.Errorf("评论容器滚动失败: %w", scrollErr)
		}
		if err := page.Sleep(await); err != nil {
			return err
		}
		if !moved {
			count, atEnd, noComments := progressFunc()
			if noComments || atEnd {
				logrus.Infof("评论容器到达末尾: count=%d", count)
				break
			}
			return fmt.Errorf("评论容器未推进且尚未出现 end 标识")
		}

		// 每5轮检查一次进度（非致命，超时继续）
		if i%5 == 0 {
			if config.ClickMoreReplies {
				button, err := nextVisibleShowMoreButton(page, config.MaxRepliesThreshold)
				if err != nil {
					logrus.Warnf("检查可见子评论展开按钮失败: %v", err)
				} else if button != nil {
					logrus.Infof("滚动中点击展开子评论: %s", button.Text)
					if err := dispatchMouseClick(page, button.X, button.Y); err != nil {
						logrus.Warnf("滚动中展开子评论失败: %v", err)
					}
				}
			}

			count, atEnd, noComments := progressFunc()
			if noComments {
				logrus.Info("✓ 笔记无评论（荒地），跳过加载")
				return nil
			}
			if atEnd {
				logrus.Infof("✓ 检测到评论已到底: count=%d", count)
				break
			}
			logrus.Debugf("评论进度: %d/%d, atEnd=%v", count, config.MaxCommentItems, atEnd)

			if config.MaxCommentItems > 0 && count >= config.MaxCommentItems {
				logrus.Infof("✓ 已达到目标评论数: %d", count)
				break
			}

			if count > lastCount {
				lastCount = count
				staleChecks = 0
			} else {
				staleChecks++
				if staleChecks >= maxStaleChecks {
					logrus.Infof("✓ 评论数连续%d轮无增长(%d)，停止", staleChecks*5, count)
					break
				}
			}
		}
	}

	if config.ClickMoreReplies {
		if remaining := remainingDeadline(); remaining < 15*time.Second {
			logrus.Warnf("评论加载剩余时间不足(%s)，跳过末尾子评论展开", remaining.Round(time.Second))
			logrus.Info("✓ 评论加载流程结束")
			return nil
		}
		if err := clickMoreReplies(page, config.MaxRepliesThreshold, remainingDeadline); err != nil {
			return err
		}
	}

	logrus.Info("✓ 评论加载流程结束")
	return nil
}

func commentBatchSample(page *hrod.Page, feedID string, align bool, delta float64) (*commentBatchDOMState, error) {
	js := `(fd,al,dl)=>{var V=function(e){if(!e||!e.isConnected)return false;var c=e;while(c){var s=getComputedStyle(c);if(s.display==='none'||s.visibility==='hidden')return false;c=c.parentElement}var r=e.getBoundingClientRect();return r.width>0&&r.height>0};
var I=function(e){var r=e.getBoundingClientRect();return r.left<window.innerWidth&&r.right>0&&r.top<window.innerHeight&&r.bottom>0};
var C=function(v){return(v||'').replace(/\s+/g,' ').trim()};
var aR=Array.from(document.querySelectorAll('.comments-container,.comments-el'));var va=[];
for(var ri=0;ri<aR.length;ri++){var rt=aR[ri];if(!V(rt))continue;var dt=rt.closest('.note-detail-mask,.note-container');if(!dt||!V(dt)||!I(dt))continue;va.push({r:rt,d:dt})}
if(va.length===0)return JSON.stringify({rf:false,ra:false,sf:false,rk:'',mv:false,st:0,sh:0,ch:0,ab:false,tk:false,tl:0,ev:false,nv:false,mc:0,co:[]});
var gr=new Map();for(var vi=0;vi<va.length;vi++){var v=va[vi];if(!gr.has(v.d))gr.set(v.d,[]);gr.get(v.d).push(v.r)}
var aD=Array.from(document.querySelectorAll('.note-detail-mask,.note-container'));
var sc=Array.from(gr.entries()).map(function(e){var d=e[0],rs=e[1];var z=parseInt(getComputedStyle(d).zIndex,10);if(isNaN(z))z=0;var o=aD.indexOf(d);var dp=0,p=d.parentElement;while(p&&p!==document.body){dp++;p=p.parentElement}return{d:d,rs:rs,z:z,o:o,dp:dp}});
sc.sort(function(a,b){if(a.z!==b.z)return b.z-a.z;if(a.o!==b.o)return b.o-a.o;return b.dp-a.dp});
var dA=sc.length>1&&sc[0].z===sc[1].z&&sc[0].o===sc[1].o;
var bst=sc[0];var dRs=bst.rs;
var sR=dRs.map(function(rt){var rect=rt.getBoundingClientRect();var ar=Math.max(0,Math.min(rect.right,window.innerWidth)-Math.max(rect.left,0))*Math.max(0,Math.min(rect.bottom,window.innerHeight)-Math.max(rect.top,0));
var hp=rt.querySelector(':scope>.parent-comment')!==null;var tt=(rt.querySelector('.total'))?.innerText||'';var ht=/共\s*\d+\s*条评论/.test(tt);
var ne=rt.querySelector('.no-comments-text');var nc=ne?.textContent||'';var hn=!!ne&&V(ne)&&I(ne)&&nc.includes('这是一片荒地');var ee=rt.querySelector('.end-container');var et=ee?.textContent||'';var he=!!ee&&V(ee)&&I(ee)&&/THE\s*END/i.test(et);
var dp=0,p=rt.parentElement;while(p&&p!==bst.d){dp++;p=p.parentElement}return{r:rt,ar:ar,dp:dp,sg:(hp?1:0)+(ht?1:0)+(hn?1:0)+(he?1:0)}});
sR.sort(function(a,b){if(a.sg!==b.sg)return b.sg-a.sg;if(a.ar!==b.ar)return b.ar-a.ar;return b.dp-a.dp});
var tR=sR[0];var rA=dA||sR.length>1&&sR[0].sg===sR[1].sg&&sR[0].ar===sR[1].ar&&sR[0].dp===sR[1].dp;
if(!tR)return JSON.stringify({rf:false,ra:false,sf:false,rk:'',mv:false,st:0,sh:0,ch:0,ab:false,tk:false,tl:0,ev:false,nv:false,mc:0,co:[]});
var sl=tR.r;var dI=aD.indexOf(bst.d);var rI=dRs.indexOf(sl);var rk=dI+':'+rI+':'+sl.tagName+':'+Array.from(sl.classList).sort().join('.');
var te=(sl.querySelector('.total'))?.innerText||'';var tm=te.match(/共\s*(\d+)\s*条评论/);var tk=!!tm;var tl=tm?Number(tm[1]):0;
var ee2=sl.querySelector('.end-container');var et2=ee2?.textContent||'';var ev=!!ee2&&V(ee2)&&I(ee2)&&/THE\s*END/i.test(et2);var ne2=sl.querySelector('.no-comments-text');var nc2=ne2?.textContent||'';var nv=!!ne2&&V(ne2)&&I(ne2)&&nc2.includes('这是一片荒地');
var EC=function(el,isSub){var tp=isSub?el:(el.querySelector(':scope>.comment-item')||el);var ct=C(tp.querySelector('.content,.note-text,[class*="content"]')?.innerText||tp.innerText);var us=C(tp.querySelector('.author-wrapper .name,.name,.nickname,[class*="name"]')?.innerText);var lt=C(tp.querySelector('.interactions .like,.like,[class*="like"]')?.innerText);return{id:tp.getAttribute('id')||el.dataset?.id||el.getAttribute('data-comment-id')||tp.dataset?.id||tp.getAttribute('data-comment-id')||'',noteId:fd,content:ct,likeCount:(lt.match(/([\d.万wWkK]+)/)||['',''])[1],userInfo:{nickname:us,nickName:us},subComments:[],showTags:[]}};
var pcs=Array.from(sl.querySelectorAll(':scope>.parent-comment'));var mc=0;var co=[];
for(var pi=0;pi<pcs.length;pi++){var pc=pcs[pi];var rc=EC(pc,false);if(rc.content){if(!rc.id)mc++;co.push(rc);var ses=Array.from(pc.querySelectorAll(':scope>.reply-container>.list-container>.comment-item,:scope>.children-comments>.comment-item-sub'));for(var si=0;si<ses.length;si++){var stc=EC(ses[si],true);if(stc.content){if(!stc.id)mc++;co.push(stc)}}}}
var sp=null,cu=sl.parentElement;while(cu&&cu!==document.body){var sy=getComputedStyle(cu);var ov=sy.overflowY;if((ov==='auto'||ov==='scroll'||ov==='overlay')&&cu.clientHeight>0){sp=cu;break}if(cu===bst.d)break;cu=cu.parentElement}
if(!sp&&(ev||nv))return JSON.stringify({rf:true,ra:rA,sf:false,rk:rk,mv:false,st:0,sh:0,ch:0,ab:false,tk:tk,tl:tl,ev:ev,nv:nv,mc:mc,co:co});
if(!sp)return JSON.stringify({rf:true,ra:rA,sf:false,rk:rk,mv:false,st:0,sh:0,ch:0,ab:false,tk:tk,tl:tl,ev:ev,nv:nv,mc:mc,co:co});
if(al&&dl>0){var t=sl.getBoundingClientRect().top-sp.getBoundingClientRect().top+sp.scrollTop;sp.scrollTo(0,Math.max(0,t-80))}
var bf=sp.scrollTop;if(dl!==0)sp.scrollBy(0,dl);var mv=sp.scrollTop>bf;
var st=sp.scrollTop,sh=sp.scrollHeight,ch=sp.clientHeight,ab=(sh-ch-st)<=2;
return JSON.stringify({rf:true,ra:rA,sf:true,rk:rk,mv:mv,st:st,sh:sh,ch:ch,ab:ab,tk:tk,tl:tl,ev:ev,nv:nv,mc:mc,co:co});}`
	result, err := page.Eval(js, feedID, align, delta)
	if err != nil {
		return nil, err
	}
	if result == nil || result.Value.Str() == "" {
		return nil, fmt.Errorf("评论采样未返回结果")
	}
	var raw struct {
		RootFound         bool        `json:"rf"`
		RootAmbiguous     bool        `json:"ra"`
		ScrollportFound   bool        `json:"sf"`
		RootKey           string      `json:"rk"`
		Moved             bool        `json:"mv"`
		ScrollTop         float64     `json:"st"`
		ScrollHeight      float64     `json:"sh"`
		ClientHeight      float64     `json:"ch"`
		AtBottom          bool        `json:"ab"`
		TotalKnown        bool        `json:"tk"`
		Total             int         `json:"tl"`
		EndVisible        bool        `json:"ev"`
		NoCommentsVisible bool        `json:"nv"`
		MissingIDCount    int         `json:"mc"`
		Comments          []Comment   `json:"co"`
	}
	if err := json.Unmarshal([]byte(result.Value.Str()), &raw); err != nil {
		return nil, fmt.Errorf("解析评论采样状态: %w", err)
	}
	if !raw.RootFound {
		return nil, fmt.Errorf("未找到评论 root")
	}
	if raw.RootAmbiguous {
		return nil, fmt.Errorf("评论 root 模糊")
	}
	if !raw.ScrollportFound && !raw.NoCommentsVisible && !raw.EndVisible {
		return nil, fmt.Errorf("未找到 scrollport")
	}
	return &commentBatchDOMState{
		RootFound:         raw.RootFound,
		RootAmbiguous:     raw.RootAmbiguous,
		ScrollportFound:   raw.ScrollportFound,
		RootKey:           raw.RootKey,
		Moved:             raw.Moved,
		ScrollTop:         raw.ScrollTop,
		ScrollHeight:      raw.ScrollHeight,
		ClientHeight:      raw.ClientHeight,
		AtBottom:          raw.AtBottom,
		TotalKnown:        raw.TotalKnown,
		Total:             raw.Total,
		EndVisible:        raw.EndVisible,
		NoCommentsVisible: raw.NoCommentsVisible,
		MissingIDCount:    raw.MissingIDCount,
		Comments:          raw.Comments,
	}, nil
}

func appendCommentBatch(state *commentBatchDOMState, batch *[]Comment, batchCursor *CommentCursor, returned map[string]struct{}, maxItems int) (hasUnseenDOMIDs bool) {
	for _, c := range state.Comments {
		id := strings.TrimSpace(c.ID)
		if id == "" {
			continue
		}
		if _, ok := returned[id]; ok {
			continue
		}
		if len(*batch) < maxItems {
			*batch = append(*batch, c)
			returned[id] = struct{}{}
			batchCursor.ReturnedIDs = append(batchCursor.ReturnedIDs, id)
		} else {
			hasUnseenDOMIDs = true
		}
	}
	return
}

func updateBatchTotal(state *commentBatchDOMState, cursor *CommentCursor) {
	if !state.TotalKnown {
		return
	}
	if cursor.TotalKnown {
		if state.Total > cursor.Total {
			cursor.Total = state.Total
		}
	} else {
		cursor.TotalKnown = true
		cursor.Total = state.Total
	}
}

func commentBatchEndCandidate(state *commentBatchDOMState, cursor *CommentCursor, hasUnseenDOMIDs bool) bool {
	if !state.RootFound || state.RootAmbiguous {
		return false
	}
	if !state.ScrollportFound && !state.EndVisible && !state.NoCommentsVisible {
		return false
	}
	if state.ScrollportFound && !state.AtBottom {
		return false
	}
	if hasUnseenDOMIDs {
		return false
	}
	if state.MissingIDCount > 0 {
		return false
	}
	if cursor.TotalKnown && len(cursor.ReturnedIDs) < cursor.Total {
		return false
	}
	if state.NoCommentsVisible {
		for _, c := range state.Comments {
			if strings.TrimSpace(c.ID) != "" {
				return false
			}
		}
		return true
	}
	if !state.EndVisible {
		return false
	}
	return true
}

func commentBatchStableEnd(page *hrod.Page, feedID string, firstState *commentBatchDOMState, await time.Duration, remaining func() time.Duration) (bool, *commentBatchDOMState, error) {
	if remaining() < await+2*time.Second {
		return false, nil, fmt.Errorf("到底确认预算不足")
	}
	if err := page.Sleep(await); err != nil {
		return false, nil, err
	}
	secondState, err := commentBatchSample(page, feedID, false, 0)
	if err != nil {
		return false, nil, fmt.Errorf("稳定复采失败: %w", err)
	}
	if secondState.RootKey != firstState.RootKey {
		return false, secondState, nil
	}
	if secondState.ScrollportFound != firstState.ScrollportFound ||
		secondState.EndVisible != firstState.EndVisible ||
		secondState.NoCommentsVisible != firstState.NoCommentsVisible {
		return false, secondState, nil
	}
	if secondState.ScrollportFound && !secondState.AtBottom {
		return false, secondState, nil
	}
	if !secondState.EndVisible && !secondState.NoCommentsVisible {
		return false, secondState, nil
	}
	if secondState.MissingIDCount > 0 {
		return false, secondState, nil
	}
	if secondState.ScrollportFound &&
		(absDiff(secondState.ScrollTop, firstState.ScrollTop) > 1 ||
			absDiff(secondState.ScrollHeight, firstState.ScrollHeight) > 1 ||
			absDiff(secondState.ClientHeight, firstState.ClientHeight) > 1) {
		return false, secondState, nil
	}
	if secondState.TotalKnown != firstState.TotalKnown || secondState.Total != firstState.Total {
		return false, secondState, nil
	}
	firstIDs := sortedCommentIDs(firstState.Comments)
	secondIDs := sortedCommentIDs(secondState.Comments)
	if !stringSlicesEqual(firstIDs, secondIDs) {
		return false, secondState, nil
	}
	return true, secondState, nil
}

func absDiff(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}

func sortedCommentIDs(comments []Comment) []string {
	ids := make([]string, 0, len(comments))
	for _, c := range comments {
		id := strings.TrimSpace(c.ID)
		if id != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func LoadCommentsBatch(ctx context.Context, page *hrod.Page, config CommentLoadConfig, cursor *CommentCursor, maxItems int) ([]Comment, *CommentCursor, bool, error) {
	config = normalizeCommentLoadConfig(config)
	if maxItems <= 0 {
		maxItems = 20
	}

	await, scrollDelta := commentScrollSettings(config.ScrollSpeed)
	deadline := commentLoadDeadline(ctx)
	remaining := func() time.Duration { return time.Until(deadline) }

	feedID := ""
	batchCursor := &CommentCursor{CreatedAt: time.Now()}
	returned := make(map[string]struct{})
	if cursor != nil {
		feedID = cursor.FeedID
		batchCursor.FeedID = cursor.FeedID
		batchCursor.Round = cursor.Round
		batchCursor.ExpandRound = cursor.ExpandRound
		batchCursor.CreatedAt = cursor.CreatedAt
		batchCursor.TotalKnown = cursor.TotalKnown
		batchCursor.Total = cursor.Total
		if batchCursor.CreatedAt.IsZero() {
			batchCursor.CreatedAt = time.Now()
		}
		for _, id := range cursor.ReturnedIDs {
			id = strings.TrimSpace(id)
			if id == "" || strings.HasPrefix(id, "idx_") {
				continue
			}
			if _, ok := returned[id]; ok {
				continue
			}
			returned[id] = struct{}{}
			batchCursor.ReturnedIDs = append(batchCursor.ReturnedIDs, id)
		}
	}
	if feedID == "" {
		if id, err := currentFeedIDFromPage(page); err == nil {
			feedID = id
			batchCursor.FeedID = id
		}
	}

	inputBase := len(batchCursor.ReturnedIDs)
	replyLimit := -1
	if config.ClickMoreReplies {
		replyLimit = config.MaxRepliesThreshold
		if replyLimit < 0 {
			replyLimit = 0
		}
	}

	var batch []Comment

	if batchCursor.Round == 0 {
		state, err := commentBatchSample(page, feedID, true, 160)
		if err != nil {
			return nil, nil, false, fmt.Errorf("初始评论采样失败: %w", err)
		}
		if state.Moved {
			batchCursor.Round++
		}
		if err := page.Sleep(await); err != nil {
			return nil, nil, false, err
		}
		if err := page.Sleep(time.Second); err != nil {
			return nil, nil, false, err
		}
	}

	state, err := commentBatchSample(page, feedID, false, 0)
	if err != nil {
		if len(batchCursor.ReturnedIDs) > inputBase && len(batch) > 0 && ctx.Err() == nil {
			return batch, batchCursor, true, nil
		}
		return nil, nil, false, fmt.Errorf("评论采样失败: %w", err)
	}

	hasUnseen := appendCommentBatch(state, &batch, batchCursor, returned, maxItems)
	updateBatchTotal(state, batchCursor)

	if len(batch) >= maxItems {
		if commentBatchEndCandidate(state, batchCursor, hasUnseen) {
			if stable, secondState, sErr := commentBatchStableEnd(page, feedID, state, await, remaining); sErr != nil {
				if len(batchCursor.ReturnedIDs) > inputBase && len(batch) > 0 && ctx.Err() == nil {
					return batch, batchCursor, true, nil
				}
				return nil, nil, false, sErr
			} else if stable {
				return batch, batchCursor, false, nil
			} else if secondState != nil {
				appendCommentBatch(secondState, &batch, batchCursor, returned, maxItems)
				updateBatchTotal(secondState, batchCursor)
			}
		}
		return batch, batchCursor, true, nil
	}

	if commentBatchEndCandidate(state, batchCursor, hasUnseen) {
		if stable, secondState, sErr := commentBatchStableEnd(page, feedID, state, await, remaining); sErr != nil {
			if len(batchCursor.ReturnedIDs) > inputBase && len(batch) > 0 && ctx.Err() == nil {
				return batch, batchCursor, true, nil
			}
			return nil, nil, false, sErr
		} else if stable {
			if len(batch) > 0 {
				return batch, batchCursor, false, nil
			}
			return []Comment{}, batchCursor, false, nil
		} else if secondState != nil {
			appendCommentBatch(secondState, &batch, batchCursor, returned, maxItems)
			updateBatchTotal(secondState, batchCursor)
		}
	}

	replyClicksTotal := 0
	replyStall := false
	clickedParents := make(map[string]struct{})

	for i := 0; i < 500; i++ {
		if rem := remaining(); rem < 15*time.Second {
			break
		}

		scrollState, sErr := commentBatchSample(page, feedID, false, scrollDelta)
		if sErr != nil {
			if len(batchCursor.ReturnedIDs) > inputBase && len(batch) > 0 && ctx.Err() == nil {
				return batch, batchCursor, true, nil
			}
			return nil, nil, false, fmt.Errorf("评论滚动采样失败: %w", sErr)
		}
		if scrollState.Moved {
			batchCursor.Round++
		}
		if err := page.Sleep(await); err != nil {
			if len(batchCursor.ReturnedIDs) > inputBase && len(batch) > 0 && ctx.Err() == nil {
				return batch, batchCursor, true, nil
			}
			return nil, nil, false, err
		}

		if config.ClickMoreReplies && !replyStall && scrollState.Moved {
			roundClicks := 0
			for roundClicks < 2 && replyClicksTotal < 8 && !replyStall {
				button, bErr := nextVisibleShowMoreButton(page, replyLimit)
				if bErr != nil {
					if len(batchCursor.ReturnedIDs) > inputBase && len(batch) > 0 && ctx.Err() == nil {
						return batch, batchCursor, true, nil
					}
					return nil, nil, false, fmt.Errorf("查询展开按钮失败: %w", bErr)
				}
				if button == nil {
					break
				}
				pid, pErr := commentParentID(page, button.ParentIndex)
				if pErr != nil || pid == "" {
					if len(batchCursor.ReturnedIDs) > inputBase && len(batch) > 0 && ctx.Err() == nil {
						return batch, batchCursor, true, nil
					}
					if pErr != nil {
						return nil, nil, false, fmt.Errorf("读取回复父评论ID失败: %w", pErr)
					}
					return nil, nil, false, fmt.Errorf("回复父评论缺少真实ID")
				}
				if _, clicked := clickedParents[pid]; clicked {
					break
				}
				before, cErr := countReplyItems(page, button.ParentIndex)
				if cErr != nil {
					if len(batchCursor.ReturnedIDs) > inputBase && len(batch) > 0 && ctx.Err() == nil {
						return batch, batchCursor, true, nil
					}
					return nil, nil, false, fmt.Errorf("回复计数失败: %w", cErr)
				}
				if err := dispatchMouseClick(page, button.X, button.Y); err != nil {
					if len(batchCursor.ReturnedIDs) > inputBase && len(batch) > 0 && ctx.Err() == nil {
						return batch, batchCursor, true, nil
					}
					return nil, nil, false, fmt.Errorf("回复展开点击失败: %w", err)
				}
				replyClicksTotal++
				roundClicks++
				batchCursor.ExpandRound++
				clickedParents[pid] = struct{}{}
				if err := waitReplyItemsChanged(page, button.ParentIndex, before, 3*time.Second); err != nil {
					replyStall = true
				}
			}
		}

		state, err = commentBatchSample(page, feedID, false, 0)
		if err != nil {
			if len(batchCursor.ReturnedIDs) > inputBase && len(batch) > 0 && ctx.Err() == nil {
				return batch, batchCursor, true, nil
			}
			return nil, nil, false, fmt.Errorf("评论采样失败: %w", err)
		}

		beforeAppend := len(batchCursor.ReturnedIDs)
		hasUnseen = appendCommentBatch(state, &batch, batchCursor, returned, maxItems)
		updateBatchTotal(state, batchCursor)
		roundNewIDs := len(batchCursor.ReturnedIDs) > beforeAppend

		if len(batch) >= maxItems {
			if commentBatchEndCandidate(state, batchCursor, hasUnseen) {
				if stable, secondState, sErr := commentBatchStableEnd(page, feedID, state, await, remaining); sErr != nil {
					if len(batchCursor.ReturnedIDs) > inputBase && len(batch) > 0 && ctx.Err() == nil {
						return batch, batchCursor, true, nil
					}
					return nil, nil, false, sErr
				} else if stable {
					return batch, batchCursor, false, nil
				} else if secondState != nil {
					appendCommentBatch(secondState, &batch, batchCursor, returned, maxItems)
					updateBatchTotal(secondState, batchCursor)
				}
			}
			return batch, batchCursor, true, nil
		}

		if commentBatchEndCandidate(state, batchCursor, hasUnseen) {
			if stable, secondState, sErr := commentBatchStableEnd(page, feedID, state, await, remaining); sErr != nil {
				if len(batchCursor.ReturnedIDs) > inputBase && len(batch) > 0 && ctx.Err() == nil {
					return batch, batchCursor, true, nil
				}
				return nil, nil, false, sErr
			} else if stable {
				if len(batch) > 0 {
					return batch, batchCursor, false, nil
				}
				return []Comment{}, batchCursor, false, nil
			} else if secondState != nil {
				appendCommentBatch(secondState, &batch, batchCursor, returned, maxItems)
				updateBatchTotal(secondState, batchCursor)
			}
		}

		if !scrollState.Moved && !roundNewIDs {
			if len(batchCursor.ReturnedIDs) > inputBase && len(batch) > 0 && ctx.Err() == nil {
				return batch, batchCursor, true, nil
			}
			return nil, nil, false, fmt.Errorf("评论滚动无进展，请重试")
		}
	}

	if ctx.Err() != nil {
		return nil, nil, false, ctx.Err()
	}

	state, err = commentBatchSample(page, feedID, false, 0)
	if err != nil {
		if len(batchCursor.ReturnedIDs) > inputBase && len(batch) > 0 && ctx.Err() == nil {
			return batch, batchCursor, true, nil
		}
		return nil, nil, false, err
	}

	hasUnseen = appendCommentBatch(state, &batch, batchCursor, returned, maxItems)
	updateBatchTotal(state, batchCursor)
	idsGrew := len(batchCursor.ReturnedIDs) > inputBase

	if commentBatchEndCandidate(state, batchCursor, hasUnseen) {
		if stable, secondState, sErr := commentBatchStableEnd(page, feedID, state, await, remaining); sErr != nil {
			if len(batchCursor.ReturnedIDs) > inputBase && len(batch) > 0 && ctx.Err() == nil {
				return batch, batchCursor, true, nil
			}
			return nil, nil, false, sErr
		} else if stable {
			if len(batch) > 0 {
				return batch, batchCursor, false, nil
			}
			return []Comment{}, batchCursor, false, nil
		} else if secondState != nil {
			appendCommentBatch(secondState, &batch, batchCursor, returned, maxItems)
			updateBatchTotal(secondState, batchCursor)
			idsGrew = len(batchCursor.ReturnedIDs) > inputBase
		}
	}

	if idsGrew {
		return batch, batchCursor, true, nil
	}
	return nil, nil, false, fmt.Errorf("评论滚动无进展，请重试")
}

func commentBatchKey(_ int, comment Comment) string {
	if comment.ID != "" {
		return comment.ID
	}
	return ""
}

func flattenComments(comments []Comment) []Comment {
	flat := make([]Comment, 0, len(comments))
	for _, comment := range comments {
		subComments := comment.SubComments
		comment.SubComments = nil
		flat = append(flat, comment)
		flat = append(flat, subComments...)
	}
	return flat
}

func currentFeedIDFromPage(page *hrod.Page) (string, error) {
	result, err := page.Timeout(2*time.Second).Eval(`() => {
		const fromPath = String(location.pathname || "").match(/\/(?:explore|discovery\/item)\/([^/?#]+)/);
		if (fromPath?.[1]) return decodeURIComponent(fromPath[1]);
		return "";
	}`)
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", nil
	}
	return strings.TrimSpace(result.Value.Str()), nil
}

func commentScrollSettings(speed string) (time.Duration, float64) {
	await := map[string]time.Duration{"slow": 1200 * time.Millisecond, "normal": time.Second, "fast": time.Second}[speed]
	scrollDelta := map[string]float64{"slow": 100, "normal": 150, "fast": 150}[speed]
	if await < time.Second {
		await = time.Second
	}
	if scrollDelta == 0 {
		scrollDelta = 100
	}
	return await, scrollDelta
}

func scrollNoteScroller(page *hrod.Page, delta float64) error {
	_, err := scrollNoteScrollerMoved(page, delta)
	return err
}

func scrollNoteScrollerMoved(page *hrod.Page, delta float64) (bool, error) {
	result, err := page.Timeout(2*time.Second).Eval(`(delta) => {
		const content = document.querySelector(".note-scroller");
		let scroller = content;
		while (scroller) {
			const style = getComputedStyle(scroller);
			const canScroll = scroller.scrollHeight > scroller.clientHeight &&
				(style.overflowY === "auto" || style.overflowY === "scroll");
			if (canScroll) break;
			scroller = scroller.parentElement;
		}
		if (!scroller) return JSON.stringify({found: false, moved: false});
		const before = scroller.scrollTop;
		scroller.scrollBy(0, delta);
		return JSON.stringify({found: true, moved: scroller.scrollTop > before});
	}`, delta)
	if err != nil {
		return false, err
	}
	if result == nil {
		return false, fmt.Errorf("评论容器滚动未返回结果")
	}
	var state struct {
		Found bool `json:"found"`
		Moved bool `json:"moved"`
	}
	if err := json.Unmarshal([]byte(result.Value.Str()), &state); err != nil {
		return false, fmt.Errorf("解析评论容器滚动状态: %w", err)
	}
	if !state.Found {
		return false, fmt.Errorf("评论容器不存在")
	}
	return state.Moved, nil
}

func commentProgressScript() string {
	return `() => {
		const endEl = document.querySelector(".end-container");
		const endText = endEl?.textContent || "";
		const noCommentsText = document.querySelector(".no-comments-text")?.textContent || "";
		const parentCount = document.querySelectorAll(".parent-comment").length;
		const subCount = document.querySelectorAll(".parent-comment > .children-comments > .comment-item-sub, .parent-comment > .reply-container > .list-container > .comment-item").length;
		return JSON.stringify({
			count: parentCount + subCount,
			atEnd: /THE\s*END/i.test(endText),
			noComments: noCommentsText.includes("这是一片荒地"),
		});
	}`
}

func countReplyItems(page *hrod.Page, parentIndex int) (int, error) {
	val, err := page.Timeout(2*time.Second).Eval(`(parentIndex) => {
		const parent = document.querySelectorAll(".parent-comment")[parentIndex];
		if (!parent) return -1;
		return parent.querySelectorAll(":scope > .children-comments > .comment-item-sub, :scope > .reply-container > .list-container > .comment-item").length;
	}`, parentIndex)
	if err != nil {
		return 0, err
	}
	count := int(val.Value.Int())
	if count < 0 {
		return 0, fmt.Errorf("父评论不存在: index=%d", parentIndex)
	}
	return count, nil
}

func waitReplyItemsChanged(page *hrod.Page, parentIndex, before int, timeout time.Duration) error {
	return retry.Do(
		func() error {
			cur, err := countReplyItems(page, parentIndex)
			if err != nil {
				return err
			}
			if cur > before {
				return nil
			}
			return fmt.Errorf("子评论数量未增长: before=%d cur=%d", before, cur)
		},
		retry.Delay(replyExpansionRetryDelay),
		retry.Attempts(uint(timeout / replyExpansionRetryDelay)),
		retry.LastErrorOnly(true),
	)
}

type showMoreButtonSnapshot struct {
	Text        string  `json:"text"`
	X           float64 `json:"x"`
	Y           float64 `json:"y"`
	Count       int     `json:"count"`
	ParentIndex int     `json:"parentIndex"`
}

func clickMoreReplies(page *hrod.Page, maxRepliesThreshold int, remainingDeadline func() time.Duration) error {
	const maxRounds = 20
	for i := 0; i < maxRounds; i++ {
		if remainingDeadline != nil {
			if remaining := remainingDeadline(); remaining < 15*time.Second {
				logrus.Warnf("评论加载剩余时间不足(%s)，停止展开子评论", remaining.Round(time.Second))
				break
			}
		}
		button, err := nextShowMoreButton(page, maxRepliesThreshold)
		if err != nil {
			if isEvalTimeout(err) {
				logrus.Warnf("检查子评论展开按钮超时，跳过本轮: %v", err)
				continue
			}
			return err
		}
		if button == nil {
			return nil
		}
		logrus.Infof("点击展开子评论: %s", button.Text)
		// Scope the growth wait to the clicked parent. This assumes the parent
		// comment DOM order remains stable between the button snapshot and retry.
		before, err := countReplyItems(page, button.ParentIndex)
		if err != nil {
			logrus.Warnf("获取展开前子评论数量失败: %v", err)
			before = 0
		}
		if err := dispatchMouseClick(page, button.X, button.Y); err != nil {
			return err
		}
		if err := waitReplyItemsChanged(page, button.ParentIndex, before, 7*time.Second); err != nil {
			logrus.Debugf("等待子评论增长超时，继续下一轮: %v", err)
		}
		if err := page.Sleep(4 * time.Second); err != nil {
			return err
		}
	}
	logrus.Infof("展开子评论达到最大轮数(%d)，停止", maxRounds)
	return nil
}

func nextShowMoreButton(page *hrod.Page, maxRepliesThreshold int) (*showMoreButtonSnapshot, error) {
	result, err := page.Timeout(2*time.Second).Eval(`(maxRepliesThreshold) => {
		const clean = (value) => (value || "").replace(/\s+/g, " ").trim();
		const scroller = document.querySelector(".note-scroller");
		const parents = Array.from(document.querySelectorAll(".parent-comment"));
		const buttons = parents
			.flatMap((parent) => Array.from(parent.querySelectorAll(":scope > .children-comments .show-more, :scope > .reply-container .show-more")));
		for (const btn of buttons) {
			const text = clean(btn.innerText || btn.textContent);
			if (!text) continue;
			if (!text.includes("展开") || text.includes("收起")) continue;
			const parent = btn.closest(".parent-comment");
			const parentIndex = parents.indexOf(parent);
			if (parentIndex < 0) continue;
			let rect = btn.getBoundingClientRect();
			if (rect.width <= 0 || rect.height <= 0) continue;
			const match = text.match(/(\d+(?:\.\d+)?)\s*([万千])?/);
			let count = match ? Number(match[1]) : 0;
			if (match?.[2] === "万") count *= 10000;
			if (match?.[2] === "千") count *= 1000;
			count = Math.floor(count);
			if (maxRepliesThreshold > 0 && count > maxRepliesThreshold) continue;
			btn.scrollIntoView({ block: "center", inline: "nearest" });
			rect = btn.getBoundingClientRect();
			if (scroller) {
				const sRect = scroller.getBoundingClientRect();
				const visibleTop = Math.max(0, sRect.top);
				const visibleBottom = Math.min(window.innerHeight, sRect.bottom);
				if (rect.top < visibleTop || rect.bottom > visibleBottom) {
					scroller.scrollBy(0, rect.top - sRect.top - sRect.height / 2 + rect.height / 2);
					rect = btn.getBoundingClientRect();
				}
			}
			if (rect.width <= 0 || rect.height <= 0) continue;
			return JSON.stringify({
				text,
				x: rect.left + rect.width / 2,
				y: rect.top + rect.height / 2,
				count,
				parentIndex,
			});
		}
		return "";
	}`, maxRepliesThreshold)
	if err != nil {
		return nil, err
	}
	if result == nil || strings.TrimSpace(result.Value.Str()) == "" {
		return nil, nil
	}
	var button showMoreButtonSnapshot
	if err := json.Unmarshal([]byte(result.Value.Str()), &button); err != nil {
		return nil, fmt.Errorf("解析展开按钮位置失败: %w", err)
	}
	return &button, nil
}

func nextVisibleShowMoreButton(page *hrod.Page, maxRepliesThreshold int) (*showMoreButtonSnapshot, error) {
	result, err := page.Timeout(2*time.Second).Eval(`(maxRepliesThreshold) => {
		const clean = (value) => (value || "").replace(/\s+/g, " ").trim();
		const scroller = document.querySelector(".note-scroller");
		const sRect = scroller?.getBoundingClientRect();
		const visibleTop = sRect ? Math.max(0, sRect.top) : 0;
		const visibleBottom = sRect ? Math.min(window.innerHeight, sRect.bottom) : window.innerHeight;
		const parents = Array.from(document.querySelectorAll(".parent-comment"));
		const buttons = parents
			.flatMap((parent) => Array.from(parent.querySelectorAll(":scope > .children-comments .show-more, :scope > .reply-container .show-more")));
		for (const btn of buttons) {
			const text = clean(btn.innerText || btn.textContent);
			if (!text) continue;
			if (!text.includes("展开") || text.includes("收起")) continue;
			const parent = btn.closest(".parent-comment");
			const parentIndex = parents.indexOf(parent);
			if (parentIndex < 0) continue;
			const rect = btn.getBoundingClientRect();
			if (rect.width <= 0 || rect.height <= 0) continue;
			if (rect.top < visibleTop || rect.bottom > visibleBottom) continue;
			const match = text.match(/(\d+(?:\.\d+)?)\s*([万千])?/);
			let count = match ? Number(match[1]) : 0;
			if (match?.[2] === "万") count *= 10000;
			if (match?.[2] === "千") count *= 1000;
			count = Math.floor(count);
			if (maxRepliesThreshold > 0 && count > maxRepliesThreshold) continue;
			return JSON.stringify({
				text,
				x: rect.left + rect.width / 2,
				y: rect.top + rect.height / 2,
				count,
				parentIndex,
			});
		}
		return "";
	}`, maxRepliesThreshold)
	if err != nil {
		return nil, err
	}
	if result == nil || strings.TrimSpace(result.Value.Str()) == "" {
		return nil, nil
	}
	var button showMoreButtonSnapshot
	if err := json.Unmarshal([]byte(result.Value.Str()), &button); err != nil {
		return nil, fmt.Errorf("解析可见展开按钮位置失败: %w", err)
	}
	return &button, nil
}

func commentParentID(page *hrod.Page, parentIndex int) (string, error) {
	result, err := page.Eval(`(parentIndex) => {
		const parent = document.querySelectorAll(".parent-comment")[parentIndex];
		if (!parent) return "";
		return parent.getAttribute("id") || parent.dataset?.id || parent.getAttribute("data-comment-id") || "";
	}`, parentIndex)
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", nil
	}
	return strings.TrimSpace(result.Value.Str()), nil
}

func dispatchMouseClick(page *hrod.Page, x, y float64) error {
	return page.ClickPoint(proto.Point{X: x, Y: y})
}

func sleepRandom(page *hrod.Page, minMs, maxMs int) error {
	return page.SleepRandom(time.Duration(minMs)*time.Millisecond, time.Duration(maxMs)*time.Millisecond)
}

// scrollToCommentsArea 使用JS定位到评论区（comment_feed.go 引用）
func scrollToCommentsArea(page *hrod.Page) error {
	_, err := page.Timeout(2*time.Second).Eval(`() => {
		const cc = document.querySelector('.comments-container, .comments-el');
		let scroller = cc;
		while (scroller) {
			const style = getComputedStyle(scroller);
			const canScroll = scroller.scrollHeight > scroller.clientHeight &&
				(style.overflowY === 'auto' || style.overflowY === 'scroll');
			if (canScroll) break;
			scroller = scroller.parentElement;
		}
		if (cc && scroller) {
			const top = cc.getBoundingClientRect().top - scroller.getBoundingClientRect().top + scroller.scrollTop;
			scroller.scrollTo(0, Math.max(0, top - 80));
			return;
		}
		if (cc) { cc.scrollIntoView({block:'center'}); }
	}`)
	if err != nil && isEvalTimeout(err) {
		logrus.Warnf("定位评论区 Eval 超时: %v", err)
		return nil
	}
	return err
}

func isEvalTimeout(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded")
}

// ========== DOM 查询 ==========

func getCommentProgress(page *hrod.Page) (commentProgress, error) {
	var progress commentProgress

	result, err := page.Timeout(2*time.Second).Eval(`() => {
		const totalEl = document.querySelector(".comments-container .total") ||
			document.querySelector(".comment-total") ||
			document.querySelector(".total");
		const totalText = totalEl?.innerText || "";
		const totalMatch = totalText.match(/共\s*(\d+)\s*条评论/);
		const endText = document.querySelector(".end-container")?.textContent || "";
		const noCommentsText = document.querySelector(".no-comments-text")?.textContent || "";
		const parentCount = document.querySelectorAll(".parent-comment").length;
		const subCount = document.querySelectorAll(".parent-comment > .children-comments > .comment-item-sub, .parent-comment > .reply-container > .list-container > .comment-item").length;

		return JSON.stringify({
			count: parentCount + subCount,
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
			evalResult, err := page.Timeout(2*time.Second).Eval(`() => {
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

			// 检测到底标记（页面显示"THE END"）
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
	result, err := page.Timeout(2*time.Second).Eval(`(feedID) => {
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

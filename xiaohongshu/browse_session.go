package xiaohongshu

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	xerrors "github.com/xpzouying/xiaohongshu-mcp/errors"
	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
)

const (
	DefaultBrowseSessionTimeout     = 10 * time.Minute
	browseSessionRefreshTimeout     = 2 * time.Second
	maxBrowseSessionTimelineEntries = 10
	maxBrowseSessionResults         = 500
	browseSessionBackTimeout        = 15 * time.Second
	healthCheckTimeout              = 5 * time.Second
	sessionSearchTimeout            = 180 * time.Second
	openedNoteStateImageWait        = 2 * time.Second
	openedNoteStateImageInterval    = 250 * time.Millisecond
)

type BrowseSessionStatus string

const (
	SessionReady     BrowseSessionStatus = "ready"
	SessionBusy      BrowseSessionStatus = "busy"
	SessionNotReady  BrowseSessionStatus = "not_ready"
	SessionExpired   BrowseSessionStatus = "expired"
	SessionUnhealthy BrowseSessionStatus = "unhealthy"
)

type BrowseSessionInfo struct {
	ID            string          `json:"id"`
	CurrentURL    string          `json:"current_url,omitempty"`
	SourceURL     string          `json:"source_url,omitempty"`
	ScrollY       int             `json:"scroll_y,omitempty"`
	CurrentFeedID string          `json:"current_feed_id,omitempty"`
	Opened        bool            `json:"opened"`
	Read          bool            `json:"read"`
	SeenNotes     map[string]bool `json:"seen_notes,omitempty"`
	ExpiresAt     time.Time       `json:"expires_at"`
}

// SessionOpenNoteResponse 在打开笔记后直接返回首屏正文、作者、互动数据、首屏评论及笔记图片 URL。
type SessionOpenNoteResponse struct {
	BrowseSessionInfo
	Note     OpenedNoteContent `json:"note"`
	Comments []Comment         `json:"comments"`
}

type CreateBrowseSessionResult struct {
	Outcome           string                  `json:"outcome"`
	Session           *BrowseSessionInfo      `json:"session,omitempty"`
	Status            BrowseSessionStatusInfo `json:"status"`
	RecommendedAction string                  `json:"recommended_action"`

	// 临时探针字段：验收冷启动耗时后删除（2026-08-09）。
	DebugStartupMS int64 `json:"debug_startup_ms"`
	DebugReadyMS   int64 `json:"debug_ready_ms"`
	DebugTotalMS   int64 `json:"debug_total_ms"`
	DebugReused    bool  `json:"debug_reused"`
}

type BrowseSessionStatusInfo struct {
	Status          BrowseSessionStatus `json:"status"`
	Session         *BrowseSessionInfo  `json:"session,omitempty"`
	LastError       string              `json:"last_error,omitempty"`
	HealthCheckedAt time.Time           `json:"health_checked_at,omitempty"`
	Ready           bool                `json:"ready"`
	Risk            string              `json:"risk,omitempty"`
}

type ReuseCheck struct {
	Status          BrowseSessionStatus
	LastError       string
	HealthCheckedAt time.Time
	Ready           bool
	Risk            string
}

type reusePageState struct {
	URL        string `json:"url"`
	ReadyState string `json:"readyState"`
}

type BrowseSessionPageState struct {
	Session           BrowseSessionInfo       `json:"session"`
	Summary           string                  `json:"summary,omitempty"`
	Kind              XHSReadyKind            `json:"kind"`
	Ready             bool                    `json:"ready"`
	Risk              RiskSignal              `json:"risk"`
	Counts            BrowseSessionPageCounts `json:"counts"`
	Current           BrowseSessionCurrent    `json:"current"`
	Results           []BrowseSessionResult   `json:"results,omitempty"`
	Actions           []BrowseSessionAction   `json:"actions,omitempty"`
	RecommendedAction *BrowseSessionAction    `json:"recommended_action,omitempty"`
	Timeline          []BrowseSessionEvent    `json:"timeline,omitempty"`
	StateFragment     string                  `json:"state_fragment,omitempty"`
	ResultsCount      int                               `json:"results_count"`
	SeenCount         int                               `json:"seen_count"`
	AvailableActions  []string                          `json:"available_actions,omitempty"`
	Notification      *BrowseSessionNotificationSurface `json:"notification,omitempty"`
}

type BrowseSessionCurrent struct {
	Kind           XHSReadyKind `json:"kind"`
	URL            string       `json:"url,omitempty"`
	FeedID         string       `json:"feed_id,omitempty"`
	Opened         bool         `json:"opened"`
	Read           bool         `json:"read"`
	ScrollY        int          `json:"scroll_y,omitempty"`
	NextHint       string       `json:"next_hint,omitempty"`
	ResultsCount   int          `json:"results_count"`
	AvailableTools []string     `json:"available_tools,omitempty"`
}

type BrowseSessionResult struct {
	Ref    string `json:"ref"`
	FeedID string `json:"feed_id,omitempty"`
	Title  string `json:"title,omitempty"`
	Author string `json:"author,omitempty"`
	Seen   bool   `json:"seen"`
}

type BrowseSessionAction struct {
	Ref             string `json:"ref"`
	Tool            string `json:"tool"`
	Label           string `json:"label"`
	ResultRef       string `json:"result_ref,omitempty"`
	FeedID          string `json:"feed_id,omitempty"`
	Requires        string `json:"requires,omitempty"`
	Confirm         bool   `json:"confirm,omitempty"`
	NotificationRef string `json:"notification_ref,omitempty"`
}

type BrowseSessionEvent struct {
	Action string    `json:"action"`
	Target string    `json:"target,omitempty"`
	Status string    `json:"status"`
	At     time.Time `json:"at"`
	Note   string    `json:"note,omitempty"`
}

// BrowseSessionNotificationSurface 通知页面会话的对外状态。
type BrowseSessionNotificationSurface struct {
	Tab         NotificationTab     `json:"tab"`
	Generation  uint64              `json:"generation"`
	EnteredAt   time.Time           `json:"entered_at"`
	ScrollCount int                 `json:"scroll_count"`
	ResultCount int                 `json:"result_count"`
	Items       []NotificationItem  `json:"items"`
	Cursor      string              `json:"cursor,omitempty"`
	HasMore     bool                `json:"has_more"`
}

// browseNotificationState 通知 surface 的会话内部状态。
type browseNotificationState struct {
	active      bool
	tab         NotificationTab
	generation  uint64
	enteredAt   time.Time
	scrollCount int
	items       []NotificationItem
	targets     map[string]notificationTarget
	cursor      string
	returnedIDs map[string]bool
	hasMore     bool
}

type BrowseSessionPageCounts struct {
	AppCount           int `json:"app_count"`
	FeedCardCount      int `json:"feed_card_count"`
	SearchInputCount   int `json:"search_input_count"`
	SearchResultCount  int `json:"search_result_count"`
	HomeFeedCount      int `json:"home_feed_count"`
	SearchFeedCount    int `json:"search_feed_count"`
	DetailCount        int `json:"detail_count"`
	CommentBoxCount    int `json:"comment_box_count"`
	LikeButtonCount    int `json:"like_button_count"`
	PublishSignalCount int `json:"publish_signal_count"`
}

type BrowseSession struct {
	mu       sync.Mutex
	opToken  chan struct{}
	closedCh chan struct{}
	opCtx    context.Context
	activeOp context.CancelFunc
	evalJS   func(ctx context.Context, page *hrod.Page, script string) (*proto.RuntimeRemoteObject, error)
	id       string
	page     *hrod.Page
	state    *ActionStateStore
	timeout  time.Duration
	timer    *time.Timer
	onClose  func(*hrod.Page)
	onRemove func(*BrowseSession)

	touchOnFinish bool

	currentURL        string
	sourceURL         string
	scrollY           int
	seenNotes         map[string]bool
	results           map[string]Feed
	nextResultIndex   int
	currentFeedID     string
	currentXsecToken  string
	opened            bool
	read              bool
	closed            bool
	expiresAt         time.Time
	timeline          []BrowseSessionEvent
	initialCommentIDs []string
	notification      browseNotificationState
	openedNoteContent OpenedNoteContent
}

type BrowseSessionManager struct {
	mu       sync.Mutex
	timeout  time.Duration
	sessions map[string]*BrowseSession
}

func NewBrowseSessionManager(timeout time.Duration) *BrowseSessionManager {
	if timeout <= 0 {
		timeout = DefaultBrowseSessionTimeout
	}
	return &BrowseSessionManager{
		timeout:  timeout,
		sessions: make(map[string]*BrowseSession),
	}
}

func (m *BrowseSessionManager) Create(page *hrod.Page, state *ActionStateStore, onClose func(*hrod.Page)) *BrowseSession {
	return m.create(page, state, onClose, func(session *BrowseSession) {
		session.evalJS = func(ctx context.Context, p *hrod.Page, script string) (*proto.RuntimeRemoteObject, error) {
			return p.Context(ctx).Eval(script)
		}
	})
}

func (m *BrowseSessionManager) create(page *hrod.Page, state *ActionStateStore, onClose func(*hrod.Page), configure func(*BrowseSession)) *BrowseSession {
	session := &BrowseSession{
		id:        newBrowseSessionID(),
		opToken:   make(chan struct{}, 1),
		closedCh:  make(chan struct{}),
		page:      page,
		state:     state,
		timeout:   m.timeout,
		onClose:   onClose,
		onRemove:  m.remove,
		seenNotes: make(map[string]bool),
		results:   make(map[string]Feed),
		notification: browseNotificationState{
			targets:     make(map[string]notificationTarget),
			returnedIDs: make(map[string]bool),
		},
	}
	session.opToken <- struct{}{}

	if configure != nil {
		configure(session)
	}

	m.mu.Lock()
	m.sessions[session.id] = session
	session.mu.Lock()
	session.touchLocked()
	session.mu.Unlock()
	m.mu.Unlock()

	opCtx, err := session.beginLockedOperation(context.Background(), false)
	if err == nil {
		session.refreshPageState(opCtx)
		session.releaseOperation()
	}
	return session
}

func (m *BrowseSessionManager) Get(id string) (*BrowseSession, error) {
	m.mu.Lock()
	session := m.sessions[id]
	m.mu.Unlock()
	if session == nil {
		return nil, fmt.Errorf("browse session 不存在或已过期: %s", id)
	}
	if session.isExpired() {
		_ = m.Close(id)
		return nil, fmt.Errorf("browse session 已过期: %s", id)
	}
	return session, nil
}

func (m *BrowseSessionManager) ActiveInfo() (BrowseSessionInfo, bool) {
	m.mu.Lock()
	var session *BrowseSession
	for _, current := range m.sessions {
		session = current
		break
	}
	m.mu.Unlock()

	if session == nil {
		return BrowseSessionInfo{}, false
	}
	if session.isExpired() {
		_ = m.Close(session.ID())
		return BrowseSessionInfo{}, false
	}
	return session.Info(), true
}

func (m *BrowseSessionManager) Close(id string) error {
	m.mu.Lock()
	session := m.sessions[id]
	delete(m.sessions, id)
	m.mu.Unlock()
	if session == nil {
		return fmt.Errorf("browse session 不存在: %s", id)
	}
	session.Close()
	return nil
}

func (m *BrowseSessionManager) remove(session *BrowseSession) {
	if session == nil {
		return
	}
	m.mu.Lock()
	if m.sessions[session.id] == session {
		delete(m.sessions, session.id)
	}
	m.mu.Unlock()
}

func (m *BrowseSessionManager) CloseAll() {
	m.mu.Lock()
	sessions := make([]*BrowseSession, 0, len(m.sessions))
	for id, session := range m.sessions {
		sessions = append(sessions, session)
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	for _, session := range sessions {
		session.Close()
	}
}

func (s *BrowseSession) ID() string {
	return s.id
}

func (s *BrowseSession) Info() BrowseSessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.infoLocked()
}

func (s *BrowseSession) GetInitialCommentIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.initialCommentIDs...)
}

func (s *BrowseSession) Renew() BrowseSessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed && !time.Now().After(s.expiresAt) {
		s.touchLocked()
	}
	return s.infoLocked()
}

func (s *BrowseSession) PageState(ctx context.Context) (state *BrowseSessionPageState, err error) {
	opCtx, err := s.beginLockedOperation(ctx, true)
	if err != nil {
		return nil, err
	}
	defer func() { s.finishOperation(err) }()

	s.mu.Lock()
	page := s.page
	feedID := s.currentFeedID
	opened := s.opened
	s.mu.Unlock()
	if page == nil {
		return nil, fmt.Errorf("browse session 页面不存在: %s", s.id)
	}

	page = page.Context(opCtx)
	probe, err := probeXHSReadyFull(page, feedID)
	if err != nil {
		return nil, err
	}
	risk := riskSignalFromReadyProbe(probe)

	liveDetail := probe.VisibleDetailCount > 0
	liveKind := inferLivePageKind(probe, liveDetail)
	mismatch := opened && feedID != "" && !liveDetail

	s.mu.Lock()
	if probe.URL != "" {
		s.currentURL = probe.URL
	}
	s.scrollY = probe.ScrollY
	info := s.infoLocked()
	resultsCount := s.uniqueResultCountLocked()
	seenCount := len(s.seenNotes)
	timeline := s.timelineLocked()
	notification := s.notificationSurfaceLocked()
	results := s.semanticResultsLocked()

	kind := liveKind
	var ready bool
	var availableActions []string
	var actions []BrowseSessionAction
	var recommendedAction *BrowseSessionAction
	var current BrowseSessionCurrent
	var summary string
	if mismatch {
		ready = false
		availableActions, actions = s.mismatchActionsLocked(results)
		recommendedAction = &BrowseSessionAction{Ref: "search_feeds", Tool: "search_feeds", Label: "搜索笔记"}
		if len(results) > 0 {
			recommendedAction = &BrowseSessionAction{Ref: "open_note:" + results[0].Ref, Tool: "open_note", Label: "打开搜索结果 " + results[0].Ref, ResultRef: results[0].Ref, FeedID: results[0].FeedID}
		}
		current = BrowseSessionCurrent{
			Kind:           kind,
			URL:            redactSensitiveURL(s.currentURL),
			FeedID:         "",
			Opened:         false,
			Read:           false,
			ScrollY:        s.scrollY,
			NextHint:       "页面状态不一致：session 声明已打开笔记，但当前页面无可见详情；详情工具已禁用",
			ResultsCount:   resultsCount,
			AvailableTools: append([]string(nil), availableActions...),
		}
		summary = fmt.Sprintf("state_mismatch expected_detail=%s live_kind=%s detail_tools_disabled", feedID, kind) + "\n" + browseSessionSummary(kind, ready, resultsCount, seenCount, current, recommendedAction)
	} else {
		ready = isXHSReady(probe, kind, feedID, true)
		if ready {
			probeWatchdogSelectors(page, XHSReadyOptions{Kind: kind, FeedID: feedID})
		}
		availableActions = s.availableActionsLocked(resultsCount)
		actions = s.semanticActionsLocked(resultsCount)
		recommendedAction = s.recommendedActionLocked(ready, results)
		current = s.currentStateLocked(kind, resultsCount, availableActions)
		summary = browseSessionSummary(kind, ready, resultsCount, seenCount, current, recommendedAction)
	}
	s.mu.Unlock()

	return &BrowseSessionPageState{
		Session: info,
		Summary: summary,
		Kind:    kind,
		Ready:   ready,
		Risk:    risk,
		Counts: BrowseSessionPageCounts{
			AppCount:           probe.AppCount,
			FeedCardCount:      probe.FeedCardCount,
			SearchInputCount:   probe.SearchInputCount,
			SearchResultCount:  probe.SearchResultCount,
			HomeFeedCount:      probe.HomeFeedCount,
			SearchFeedCount:    probe.SearchFeedCount,
			DetailCount:        probe.DetailCount,
			CommentBoxCount:    probe.CommentBoxCount,
			LikeButtonCount:    probe.LikeButtonCount,
			PublishSignalCount: probe.PublishSignalCount,
		},
		Current:           current,
		Results:           results,
		Actions:           actions,
		RecommendedAction: recommendedAction,
		Timeline:          timeline,
		StateFragment:     probe.StateFragment,
		ResultsCount:      resultsCount,
		SeenCount:         seenCount,
		AvailableActions:  availableActions,
		Notification:      notification,
	}, nil
}

func (s *BrowseSession) Search(ctx context.Context, keyword string, filters ...FilterOption) ([]Feed, error) {
	feeds, _, _, err := s.SearchBatch(ctx, keyword, filters, nil, 35)
	return feeds, err
}

// ListFeedsBatch 在 session 浏览器中分页获取首页 Feeds。
// cursor 为空时执行首页导航/ready；非空时从当前页继续滚动。
func (s *BrowseSession) ListFeedsBatch(ctx context.Context, cursor *FeedCursor, maxItems int) (feeds []Feed, nextCursor *FeedCursor, hasMore bool, err error) {
	opCtx, err := s.beginLockedOperation(ctx, true)
	if err != nil {
		return nil, nil, false, err
	}
	defer func() { s.finishOperation(err) }()

	maxItems = normalizeFeedPageSize(maxItems)

	s.mu.Lock()
	page := s.page
	s.mu.Unlock()

	if page == nil {
		return nil, nil, false, fmt.Errorf("browse session 页面不存在: %s", s.id)
	}

	isFirstPage := cursor == nil

	if cursor == nil {
		action, err := NewFeedsListAction(page.Context(opCtx))
		if err != nil {
			return nil, nil, false, err
		}
		allFeeds, err := action.GetFeedsList(opCtx)
		if err != nil {
			return nil, nil, false, err
		}
		cursor = &FeedCursor{Kind: FeedPageHome, CreatedAt: time.Now(), ReturnedIDs: make([]string, 0)}
		feeds = takeNewFeeds(allFeeds, cursor, maxItems)
		nextCursor = cursor
		hasMore = len(allFeeds) > len(feeds) || !hasEndSignal(page.Context(opCtx))
	} else {
		feeds, nextCursor, hasMore, err = LoadFeedBatch(opCtx, page.Context(opCtx), FeedPageHome, cursor, maxItems, func() ([]Feed, error) {
			return collectHomeFeeds(page.Context(opCtx))
		})
		if err != nil {
			return nil, nil, false, err
		}
	}

	s.mu.Lock()
	if isFirstPage {
		s.sourceURL = ""
		s.currentFeedID = ""
		s.currentXsecToken = ""
		s.opened = false
		s.read = false
		s.initialCommentIDs = nil
		s.results, s.nextResultIndex = replaceSessionResults(feeds)
		s.resetNotificationSurfaceLocked()
	} else {
		originalCount := len(feeds)
		s.results, s.nextResultIndex, feeds = appendSessionResults(s.results, s.nextResultIndex, feeds)
		if len(feeds) < originalCount {
			hasMore = false
			trimFeedCursorTail(nextCursor, originalCount-len(feeds))
		}
	}
	s.recordTimelineLocked("list_feeds", "", "ok", time.Now(), fmt.Sprintf("results=%d", len(feeds)))
	s.mu.Unlock()
	return feeds, nextCursor, hasMore, nil
}

// ListFeeds 是 ListFeedsBatch 的兼容 wrapper，不传游标且默认 35 条。
func (s *BrowseSession) ListFeeds(ctx context.Context) ([]Feed, error) {
	feeds, _, _, err := s.ListFeedsBatch(ctx, nil, 35)
	return feeds, err
}

// SearchBatch 在 session 浏览器中分页搜索。
// cursor 为空时执行真实 UI 搜索和筛选；非空时从当前页继续滚动。
func (s *BrowseSession) SearchBatch(ctx context.Context, keyword string, filters []FilterOption, cursor *FeedCursor, maxItems int) ([]Feed, *FeedCursor, bool, error) {
	result, nextCursor, hasMore, err := s.searchBatch(ctx, keyword, filters, cursor, maxItems, false)
	return result.Feeds, nextCursor, hasMore, err
}

// SearchBatchWithAI 在首次搜索时额外返回 AI 回复，续页不重复读取。
func (s *BrowseSession) SearchBatchWithAI(ctx context.Context, keyword string, filters []FilterOption, cursor *FeedCursor, maxItems int) (SearchPageResult, *FeedCursor, bool, error) {
	return s.searchBatch(ctx, keyword, filters, cursor, maxItems, true)
}

func (s *BrowseSession) searchBatch(ctx context.Context, keyword string, filters []FilterOption, cursor *FeedCursor, maxItems int, includeAI bool) (result SearchPageResult, nextCursor *FeedCursor, hasMore bool, err error) {
	searchCtx, cancel := context.WithTimeout(ctx, sessionSearchTimeout)
	defer cancel()

	rec := newDebugSearchRecorder()
	rec.beginStage("precheck")
	searchCtx = withDebugSearchRecorder(searchCtx, rec)

	opCtx, err := s.beginLockedOperation(searchCtx, true)
	if err != nil {
		rec.finish()
		return SearchPageResult{}, nil, false, fmt.Errorf("%w; debug_search=%s", err, rec.marshalSummary())
	}
	defer func() {
		if err != nil && searchCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
			err = fmt.Errorf("搜索操作超过内部时限 %s: %w", sessionSearchTimeout, err)
		}
		rec.finish()
		if err != nil {
			err = fmt.Errorf("%w; debug_search=%s", err, rec.marshalSummary())
		} else {
			rec.fillResult(&result)
		}
		s.reconcileAfterFailedSearch(err)
		s.finishOperation(err)
	}()

	maxItems = normalizeFeedPageSize(maxItems)

	s.mu.Lock()
	page := s.page
	s.mu.Unlock()

	if page == nil {
		return SearchPageResult{}, nil, false, fmt.Errorf("browse session 页面不存在: %s", s.id)
	}

	var feeds []Feed
	var aiChat *AIChatReply

	if cursor == nil {
		action := NewSearchAction(page.Context(opCtx))
		var allFeeds []Feed
		if includeAI {
			searchResult, searchErr := action.Search(opCtx, keyword, filters...)
			if searchErr != nil {
				return SearchPageResult{}, nil, false, searchErr
			}
			allFeeds = searchResult.Feeds
			aiChat = searchResult.AIChat
		} else {
			var searchErr error
			allFeeds, searchErr = action.SearchFeedsOnly(opCtx, keyword, filters...)
			if searchErr != nil {
				return SearchPageResult{}, nil, false, searchErr
			}
		}
		cursor = &FeedCursor{
			Kind:        FeedPageSearch,
			Keyword:     keyword,
			FilterKey:   filterKeyFromFilters(filters),
			CreatedAt:   time.Now(),
			ReturnedIDs: make([]string, 0),
		}
		feeds = takeNewFeeds(allFeeds, cursor, maxItems)
		hasMore = len(allFeeds) > len(feeds) || !hasEndSignal(page.Context(opCtx))
		s.mu.Lock()
		s.sourceURL = ""
		s.currentFeedID = ""
		s.currentXsecToken = ""
		s.opened = false
		s.read = false
		s.results, s.nextResultIndex = replaceSessionResults(feeds)
		s.resetNotificationSurfaceLocked()
		s.recordTimelineLocked("search", keyword, "ok", time.Now(), fmt.Sprintf("results=%d", len(feeds)))
		s.mu.Unlock()
		s.probeWatchdogSelectorsForKind(opCtx, XHSReadySearch, "")
		result.Feeds = feeds
		result.AIChat = aiChat
		return result, cursor, hasMore, nil
	}

	counter := &evalTimeoutCounter{}
	feeds, nextCursor, hasMore, err = LoadFeedBatch(opCtx, page.Context(opCtx), FeedPageSearch, cursor, maxItems, func() ([]Feed, error) {
		return collectSearchFeeds(opCtx, page.Context(opCtx), counter)
	})
	if err != nil {
		return SearchPageResult{}, nil, false, err
	}

	s.mu.Lock()
	originalCount := len(feeds)
	s.results, s.nextResultIndex, feeds = appendSessionResults(s.results, s.nextResultIndex, feeds)
	if len(feeds) < originalCount {
		hasMore = false
		trimFeedCursorTail(nextCursor, originalCount-len(feeds))
	}
	s.mu.Unlock()
	return SearchPageResult{Feeds: feeds}, nextCursor, hasMore, nil
}

func (s *BrowseSession) OpenNote(ctx context.Context, resultRef, shareURL, xsecToken string) (*SessionOpenNoteResponse, error) {
	opCtx, err := s.beginLockedOperation(ctx, true)
	if err != nil {
		return nil, err
	}
	var operationErr error
	defer func() { s.finishOperation(operationErr) }()
	fail := func(err error) (*SessionOpenNoteResponse, error) {
		operationErr = err
		return nil, err
	}

	hasResultRef := resultRef != ""
	hasShareURL := shareURL != ""
	if !hasResultRef && !hasShareURL {
		return fail(fmt.Errorf("result_ref与share_url必须且只能提供一个"))
	}
	if hasResultRef && hasShareURL {
		return fail(fmt.Errorf("result_ref与share_url必须且只能提供一个"))
	}
	if hasShareURL && xsecToken != "" {
		return fail(fmt.Errorf("share_url不能与xsec_token同时使用"))
	}

	var feed Feed
	var sourceURL string
	var resultRefForTimeline string
	var finalURL string

	if hasShareURL {
		parsed, parseErr := parseAndValidateShareURL(shareURL)
		if parseErr != nil {
			return fail(parseErr)
		}
		s.mu.Lock()
		page := s.page
		s.mu.Unlock()
		if page == nil {
			return fail(fmt.Errorf("browse session 页面不存在: %s", s.id))
		}
		counter := &evalTimeoutCounter{}
		sourceURLCandidate, urlErr := s.currentPageURL(opCtx, counter)
		if urlErr != nil {
			return fail(fmt.Errorf("读取当前页面URL: %w", urlErr))
		}
		sourceURL = sourceURLCandidate
		if navErr := page.Context(opCtx).Navigate(parsed.NormalizedURL); navErr != nil {
			var navigationErr *rod.NavigationError
			if !parsed.IsShortLink || !errors.As(navErr, &navigationErr) {
				return fail(fmt.Errorf("导航到share_url失败"))
			}
			if _, _, sourceErr := validateFinalNoteURL(sourceURL); sourceErr == nil {
				return fail(fmt.Errorf("导航到share_url失败"))
			}
		}
		pollResult, pollErr := waitForNoteURLStable(opCtx, 60*time.Second, func(readCtx context.Context) (string, error) {
			result, infoErr := proto.TargetGetTargetInfo{TargetID: page.Rod.TargetID}.Call(page.Rod.Browser().Context(readCtx))
			if infoErr != nil {
				return "", infoErr
			}
			if result == nil || result.TargetInfo == nil {
				return "", fmt.Errorf("页面信息为空")
			}
			return result.TargetInfo.URL, nil
		})
		if pollErr != nil {
			return fail(pollErr)
		}
		finalURL = pollResult.URL
		finalNoteID := pollResult.NoteID
		finalToken := ""
		if u, uErr := strictValidateHTTPSURL(finalURL); uErr == nil {
			_, finalToken, _ = parseOfficialNoteURL(u)
		}
		if !parsed.IsShortLink && parsed.ExpectedID != finalNoteID {
			return fail(fmt.Errorf("最终note ID与预期不一致"))
		}
		feed = Feed{ID: finalNoteID, XsecToken: shareURLToken(finalToken, parsed.XsecToken)}
		resultRefForTimeline = "share_url"
		if err := waitFeedDetailVisible(opCtx, page, counter, feed.ID); err != nil {
			return fail(err)
		}
	} else {
		feed, err = s.resolveResult(resultRef)
		if err != nil {
			return fail(err)
		}
		if xsecToken != "" {
			feed.XsecToken = xsecToken
		}
		if err := validateFeedAccessArgs(feed.ID, feed.XsecToken); err != nil {
			return fail(fmt.Errorf("搜索结果参数无效: %w", err))
		}
		s.mu.Lock()
		page := s.page
		s.mu.Unlock()
		if page == nil {
			return fail(fmt.Errorf("browse session 页面不存在: %s", s.id))
		}
		counter := &evalTimeoutCounter{}
		resultRefForTimeline = resultRef
		probe, probeErr := probeCurrentFeedDetail(opCtx, page, counter, feed.ID)
		if probeErr != nil && IsFatalRendererError(probeErr) {
			return fail(probeErr)
		}
		if probeErr != nil && !isTransientCurrentDetailProbeError(probeErr) {
			return fail(fmt.Errorf("探测当前笔记详情失败: %w", probeErr))
		}
		alreadyOpen := probeErr == nil && currentFeedDetailMatched(probe, feed.ID)
		if !alreadyOpen {
			sourceURL, err = s.currentPageURL(opCtx, counter)
			if err != nil {
				return fail(fmt.Errorf("读取当前页面 URL: %w", err))
			}
			opener := NewNoteOpenActionWithState(page.Context(opCtx), s.state)
			if err := opener.OpenFromCards(opCtx, counter, feed.ID, feed.XsecToken); err != nil {
				return fail(fmt.Errorf("从卡片打开笔记失败，请重新搜索或滚动后重试: %w", err))
			}
		}
	}

	s.mu.Lock()
	page := s.page
	s.mu.Unlock()
	if page == nil {
		return fail(fmt.Errorf("browse session 页面不存在: %s", s.id))
	}
	counter := &evalTimeoutCounter{}

	snapshot, err := pollOpenedNoteSnapshot(opCtx, 15*time.Second, 250*time.Millisecond, func() (*OpenedNoteSnapshot, error) {
		return ExtractOpenedNoteSnapshotFromDOM(opCtx, page, counter, feed.ID)
	})
	if err != nil {
		if opCtx.Err() == nil && isEvalTimeout(err) && !IsFatalRendererError(err) {
			diagnostic := probeOpenedNoteSnapshotStages(opCtx, page)
			if opCtx.Err() == nil {
				err = &SnapshotDiagnosticError{diagnostic: diagnostic, cause: err}
			}
		}
		if errors.Is(err, xerrors.ErrNoFeedDetail) {
			return fail(fmt.Errorf("笔记已打开但内容未就绪: %w", err))
		}
		return fail(fmt.Errorf("首屏内容读取阶段: %w", err))
	}
	content := snapshot.Note
	if hasShareURL {
		if strings.TrimSpace(content.Title) == "" {
			return fail(fmt.Errorf("笔记标题为空"))
		}
		if strings.TrimSpace(content.User.Nickname) == "" {
			return fail(fmt.Errorf("笔记作者为空"))
		}
	} else {
		mergeOpenedNoteUserFromSearchResult(&content, feed)
	}
	comments := snapshot.Comments
	content.ImageList, err = preferOpenedNoteImages(opCtx, page, counter, feed.ID, content.ImageList)
	if err != nil {
		return fail(fmt.Errorf("图片状态读取阶段: %w", err))
	}

	initialCommentIDs := make([]string, 0)
	for i, c := range comments {
		if key := commentBatchKey(i, c); key != "" {
			initialCommentIDs = append(initialCommentIDs, key)
		}
		for j, sub := range c.SubComments {
			if key := commentBatchKey(j, sub); key != "" {
				initialCommentIDs = append(initialCommentIDs, key)
			}
		}
	}
	if !hasShareURL && s.state != nil {
		_ = s.state.RecordOpen(feed.ID, OpenSourceSearch)
		_ = s.state.RecordRead(feed.ID, 0)
	}
	info := s.commitOpenedNote(feed, redactSensitiveURL(sourceURL), resultRefForTimeline, initialCommentIDs, content)
	if hasShareURL && finalURL != "" {
		info.CurrentURL = redactSensitiveURL(finalURL)
	}
	s.probeWatchdogSelectorsForKind(opCtx, XHSReadyDetail, feed.ID)
	return &SessionOpenNoteResponse{
		BrowseSessionInfo: info,
		Note:              content,
		Comments:          comments,
	}, nil
}

func cloneOpenedNoteContent(src OpenedNoteContent) OpenedNoteContent {
	clone := src
	if len(src.ImageList) > 0 {
		clone.ImageList = make([]DetailImageInfo, len(src.ImageList))
		copy(clone.ImageList, src.ImageList)
	}
	return clone
}

func (s *BrowseSession) openedNoteFeedDetail(feedID string) FeedDetail {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.opened || s.currentFeedID != feedID || s.openedNoteContent.NoteID != feedID {
		return FeedDetail{}
	}
	content := cloneOpenedNoteContent(s.openedNoteContent)
	return FeedDetail{
		NoteID:       content.NoteID,
		XsecToken:    s.currentXsecToken,
		Title:        content.Title,
		Desc:         content.Desc,
		Type:         content.Type,
		User:         content.User,
		InteractInfo: content.InteractInfo,
		ImageList:    append([]DetailImageInfo(nil), content.ImageList...),
	}
}

func (s *BrowseSession) commitOpenedNote(feed Feed, sourceURL, resultRef string, initialCommentIDs []string, content OpenedNoteContent) BrowseSessionInfo {
	s.mu.Lock()
	if sourceURL != "" {
		s.sourceURL = sourceURL
	}
	s.currentFeedID = feed.ID
	s.currentXsecToken = feed.XsecToken
	s.opened = true
	s.read = true
	s.seenNotes[feed.ID] = true
	s.initialCommentIDs = append([]string(nil), initialCommentIDs...)
	s.openedNoteContent = cloneOpenedNoteContent(content)
	s.resetNotificationSurfaceLocked()
	openDetail := "opened from search result " + resultRef
	readDetail := "content read from search result " + resultRef
	if resultRef == "share_url" {
		openDetail = "opened from share_url"
		readDetail = "content read from share_url"
	}
	s.recordTimelineLocked("open_note", feed.ID, "ok", time.Now(), openDetail)
	s.recordTimelineLocked("read_note", feed.ID, "ok", time.Now(), readDetail)
	info := s.infoLocked()
	s.mu.Unlock()
	return info
}

func pollOpenedNoteSnapshot(ctx context.Context, timeout, interval time.Duration, attempt func() (*OpenedNoteSnapshot, error)) (*OpenedNoteSnapshot, error) {
	deadline := time.Now().Add(timeout)
	for {
		snapshot, err := attempt()
		if err == nil {
			return snapshot, nil
		}
		if IsFatalRendererError(err) {
			return nil, err
		}
		if !errors.Is(err, xerrors.ErrNoFeedDetail) && !isEvalTimeout(err) {
			return nil, err
		}
		if !time.Now().Before(deadline) {
			return nil, err
		}
		timer := time.NewTimer(min(interval, time.Until(deadline)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func mergeOpenedNoteUserFromSearchResult(content *OpenedNoteContent, feed Feed) {
	if content == nil {
		return
	}
	searchUser := feed.NoteCard.User
	if content.User.UserID == "" {
		content.User.UserID = searchUser.UserID
	}
	if content.User.XsecToken == "" {
		content.User.XsecToken = searchUser.XsecToken
	}
}

func preferOpenedNoteImages(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter, feedID string, domImages []DetailImageInfo) ([]DetailImageInfo, error) {
	stateImages, err := readOpenedNoteStateImageList(ctx, page, counter, feedID)
	if err != nil {
		return nil, err
	}
	return mergePreferredImageLists(stateImages, domImages), nil
}

func readOpenedNoteStateImageList(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter, feedID string) ([]DetailImageInfo, error) {
	imageCtx, cancel := context.WithTimeout(ctx, openedNoteStateImageWait)
	defer cancel()
	for {
		detail, err := readFeedDetailStateOnce(imageCtx, page, counter, feedID)
		if err == nil && len(detail.Note.ImageList) > 0 {
			return detail.Note.ImageList, nil
		}
		if err != nil && IsFatalRendererError(err) {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := imageCtx.Err(); err != nil {
			return nil, nil
		}
		if err := page.Sleep(openedNoteStateImageInterval); err != nil {
			return nil, err
		}
	}
}

func mergePreferredImageLists(stateImages, domImages []DetailImageInfo) []DetailImageInfo {
	if len(stateImages) > 0 {
		return stateImages
	}
	return filterStubContentImages(domImages)
}

func filterStubContentImages(images []DetailImageInfo) []DetailImageInfo {
	filtered := make([]DetailImageInfo, 0, len(images))
	for _, img := range images {
		if strings.TrimSpace(img.URLDefault) == "" {
			continue
		}
		if img.Width == 48 && img.Height == 48 {
			continue
		}
		filtered = append(filtered, img)
	}
	return filtered
}

func (s *BrowseSession) Detail(ctx context.Context, _ bool, _ int) (detail *SessionDetailResponse, err error) {
	opCtx, err := s.beginLockedOperation(ctx, true)
	if err != nil {
		return nil, err
	}
	defer func() { s.finishOperation(err) }()

	feedID, err := s.currentOpenedFeedID()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	page := s.page
	s.mu.Unlock()
	if page == nil {
		return nil, fmt.Errorf("browse session 页面不存在: %s", s.id)
	}
	if err := WaitForXHSReady(page.Context(opCtx), XHSReadyOptions{Kind: XHSReadyDetail, FeedID: feedID}); err != nil {
		return nil, err
	}
	comments, err := ExtractCommentsFromDOM(opCtx, page, feedID)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.recordTimelineLocked("detail", feedID, "ok", time.Now(), fmt.Sprintf("visible_comments=%d", len(comments)))
	s.mu.Unlock()
	s.probeWatchdogSelectorsForKind(opCtx, XHSReadyDetail, feedID)
	return &SessionDetailResponse{
		NoteID:   feedID,
		Comments: comments,
	}, nil
}

func (s *BrowseSession) DetailCommentsBatch(ctx context.Context, expectedFeedID string, cursor *CommentCursor, maxItems int, config CommentLoadConfig) (*FeedDetailResponse, *CommentCursor, bool, error) {
	return s.detailCommentsBatchLifecycle(
		ctx, expectedFeedID, maxItems, cursor, config,
		func(opCtx context.Context, page *hrod.Page, counter *evalTimeoutCounter, feedID string) error {
			if cursor != nil && cursor.Round > 0 {
				return s.recoverCommentBatchSession(opCtx, page, counter, feedID)
			}
			return WaitForXHSReady(page.Context(opCtx), XHSReadyOptions{Kind: XHSReadyDetail, FeedID: feedID})
		},
		func(loadCtx context.Context, page *hrod.Page, counter *evalTimeoutCounter) ([]Comment, *CommentCursor, bool, error) {
			return loadCommentsBatch(loadCtx, page.Context(loadCtx), config, cursor, maxItems)
		},
	)
}

func (s *BrowseSession) detailCommentsBatchLifecycle(
	ctx context.Context,
	expectedFeedID string,
	maxItems int,
	cursor *CommentCursor,
	config CommentLoadConfig,
	pretask func(context.Context, *hrod.Page, *evalTimeoutCounter, string) error,
	loader func(context.Context, *hrod.Page, *evalTimeoutCounter) ([]Comment, *CommentCursor, bool, error),
) (detail *FeedDetailResponse, nextCursor *CommentCursor, hasMore bool, err error) {
	opCtx, err := s.beginLockedOperation(ctx, true)
	if err != nil {
		return nil, nil, false, err
	}
	defer func() { s.finishOperation(err) }()

	feedID, err := s.currentOpenedFeedIDFor(expectedFeedID)
	if err != nil {
		return nil, nil, false, err
	}
	s.mu.Lock()
	page := s.page
	s.mu.Unlock()
	if page == nil {
		return nil, nil, false, fmt.Errorf("browse session 页面不存在: %s", s.id)
	}
	counter := &evalTimeoutCounter{}

	if pretask != nil {
		if err := pretask(opCtx, page, counter, feedID); err != nil {
			return nil, nil, false, err
		}
	}

	// 在 loader 前记录开始时间
	var dwellStart time.Time
	if s.state != nil {
		dwellStart = time.Now()
	}
	comments, nextCursor, hasMore, err := runDetailCommentsBatch(opCtx, func(loadCtx context.Context) ([]Comment, *CommentCursor, bool, error) {
		return loader(loadCtx, page, counter)
	})
	if err != nil {
		return nil, nil, false, err
	}
	// loader 成功后记录本次真实停留时间与是否发生确认过的物理滚动；
	// error 路径不记录未完成停留。
	if s.state != nil {
		inputRound := 0
		if cursor != nil {
			inputRound = cursor.Round
		}
		scrolled := nextCursor != nil && nextCursor.Round > inputRound
		_ = s.state.RecordCommentDwell(feedID, time.Since(dwellStart), scrolled)
	}
	return s.completeDetailCommentsBatch(opCtx, page, counter, feedID, cursor, maxItems, config, comments, nextCursor, hasMore)
}

func commentLoadDeadline(ctx context.Context) time.Time {
	deadline := time.Now().Add(commentLoadTimeout)
	if dl, ok := ctx.Deadline(); ok {
		reserve := 45 * time.Second
		adjusted := dl.Add(-reserve)
		if adjusted.Before(deadline) {
			deadline = adjusted
		}
	}
	return deadline
}

func runDetailCommentsBatch(opCtx context.Context, loader func(context.Context) ([]Comment, *CommentCursor, bool, error)) ([]Comment, *CommentCursor, bool, error) {
	loadCtx, cancel := context.WithTimeout(opCtx, commentLoadTimeout)
	defer cancel()
	comments, nextCursor, hasMore, err := loader(loadCtx)
	if err != nil {
		return nil, nil, false, err
	}
	if len(comments) == 0 && !hasMore && loadCtx.Err() != nil {
		return nil, nil, false, loadCtx.Err()
	}
	return comments, nextCursor, hasMore, nil
}

func detailBatchResponse(note FeedDetail, comments []Comment, hasMore bool) *FeedDetailResponse {
	return &FeedDetailResponse{
		Note: note,
		Comments: CommentList{
			List:    comments,
			HasMore: hasMore,
		},
	}
}

func (s *BrowseSession) completeDetailCommentsBatch(
	opCtx context.Context,
	page *hrod.Page,
	counter *evalTimeoutCounter,
	feedID string,
	inputCursor *CommentCursor,
	maxItems int,
	config CommentLoadConfig,
	comments []Comment,
	nextCursor *CommentCursor,
	hasMore bool,
) (*FeedDetailResponse, *CommentCursor, bool, error) {
	if opCtx.Err() != nil {
		return nil, nil, false, opCtx.Err()
	}
	if nextCursor != nil && nextCursor.FeedID == "" {
		nextCursor.FeedID = feedID
	}

	note := s.openedNoteFeedDetail(feedID)
	resp := detailBatchResponse(note, comments, hasMore)
	seenCount := len(comments)
	if nextCursor != nil {
		seenCount = len(nextCursor.ReturnedIDs)
	}
	progress, progressErr := getCommentProgress(opCtx, page)
	if progressErr != nil {
		if !IsFatalRendererError(progressErr) && hasMore && len(comments) > 0 {
			resp.Comments.SeenCount = seenCount
			resp.Comments.Complete = false
			resp.Comments.IncompleteReason = "progress_unavailable"
			return resp, nextCursor, true, nil
		}
		return nil, nil, false, progressErr
	}
	if progress.Total > 0 {
		resp.Comments.TotalItems = progress.Total
	}
	if opCtx.Err() != nil {
		return nil, nil, false, opCtx.Err()
	}
	s.probeWatchdogSelectorsForKind(opCtx, XHSReadyDetail, feedID)
	if opCtx.Err() != nil {
		return nil, nil, false, opCtx.Err()
	}
	resp.Comments.SeenCount = seenCount
	complete, reason, forceHasMore := decideCommentCompletion(progress, config, resp.Comments.TotalItems, seenCount, hasMore)
	if forceHasMore {
		inputLen := 0
		if inputCursor != nil {
			inputLen = len(inputCursor.ReturnedIDs)
		}
		if nextCursor == nil || len(nextCursor.ReturnedIDs) <= inputLen {
			return nil, nil, false, fmt.Errorf("评论总数未收敛，请重试")
		}
		hasMore = true
	}
	resp.Comments.Complete = complete
	resp.Comments.IncompleteReason = reason
	resp.Comments.HasMore = hasMore
	return resp, nextCursor, hasMore, nil
}

func (s *BrowseSession) recoverCommentBatchSession(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter, expectedFeedID string) error {
	p := page.Context(ctx)
	feedID, err := currentFeedIDFromPage(ctx, p)
	if err != nil || feedID == "" {
		if IsFatalRendererError(err) {
			return err
		}
		if err := p.Sleep(time.Second); err != nil {
			return err
		}
		feedID, err = currentFeedIDFromPage(ctx, p)
	}
	if err != nil || feedID == "" {
		if IsFatalRendererError(err) {
			return err
		}
		return fmt.Errorf("session 当前页面无法确认 feed: expected=%s, err=%v", expectedFeedID, err)
	}
	if feedID != expectedFeedID {
		return fmt.Errorf("session 当前页面与目标笔记不匹配: expected=%s, actual=%s", expectedFeedID, feedID)
	}
	return p.Sleep(1500 * time.Millisecond)
}

func (s *BrowseSession) Like(ctx context.Context, unlike bool) error {
	return s.like(ctx, "", unlike)
}

func (s *BrowseSession) LikeForFeed(ctx context.Context, expectedFeedID string, unlike bool) error {
	return s.like(ctx, expectedFeedID, unlike)
}

func (s *BrowseSession) like(ctx context.Context, expectedFeedID string, unlike bool) (err error) {
	opCtx, err := s.beginLockedOperation(ctx, true)
	if err != nil {
		return err
	}
	defer func() { s.finishOperation(err) }()

	feedID, xsecToken, err := s.currentFeedFor(expectedFeedID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	page := s.page
	s.mu.Unlock()
	if page == nil {
		return fmt.Errorf("browse session 页面不存在: %s", s.id)
	}
	action := NewLikeActionWithState(page.Context(opCtx), s.state)
	if unlike {
		if err := action.Unlike(opCtx, feedID, xsecToken); err != nil {
			return err
		}
	} else {
		if err := action.Like(opCtx, feedID, xsecToken); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.read = true
	if unlike {
		s.recordTimelineLocked("unlike", feedID, "ok", time.Now(), "")
	} else {
		s.recordTimelineLocked("like", feedID, "ok", time.Now(), "")
	}
	s.mu.Unlock()
	s.probeWatchdogSelectorsForKind(opCtx, XHSReadyDetail, feedID)
	return nil
}

func (s *BrowseSession) Favorite(ctx context.Context, unfavorite bool) error {
	return s.favorite(ctx, "", unfavorite)
}

func (s *BrowseSession) FavoriteForFeed(ctx context.Context, expectedFeedID string, unfavorite bool) error {
	return s.favorite(ctx, expectedFeedID, unfavorite)
}

func (s *BrowseSession) favorite(ctx context.Context, expectedFeedID string, unfavorite bool) (err error) {
	opCtx, err := s.beginLockedOperation(ctx, true)
	if err != nil {
		return err
	}
	defer func() { s.finishOperation(err) }()

	feedID, xsecToken, err := s.currentFeedFor(expectedFeedID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	page := s.page
	s.mu.Unlock()
	if page == nil {
		return fmt.Errorf("browse session 页面不存在: %s", s.id)
	}
	action := NewFavoriteActionWithState(page.Context(opCtx), s.state)
	if unfavorite {
		if err := action.Unfavorite(opCtx, feedID, xsecToken); err != nil {
			return err
		}
	} else {
		if err := action.Favorite(opCtx, feedID, xsecToken); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.read = true
	if unfavorite {
		s.recordTimelineLocked("unfavorite", feedID, "ok", time.Now(), "")
	} else {
		s.recordTimelineLocked("favorite", feedID, "ok", time.Now(), "")
	}
	s.mu.Unlock()
	s.probeWatchdogSelectorsForKind(opCtx, XHSReadyDetail, feedID)
	return nil
}

func (s *BrowseSession) Comment(ctx context.Context, content string) error {
	return s.comment(ctx, "", content)
}

func (s *BrowseSession) CommentForFeed(ctx context.Context, expectedFeedID, content string) error {
	return s.comment(ctx, expectedFeedID, content)
}

func (s *BrowseSession) comment(ctx context.Context, expectedFeedID, content string) (err error) {
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("评论内容不能为空")
	}
	opCtx, err := s.beginLockedOperation(ctx, true)
	if err != nil {
		return err
	}
	defer func() { s.finishOperation(err) }()

	feedID, xsecToken, err := s.currentFeedFor(expectedFeedID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	page := s.page
	s.mu.Unlock()
	if page == nil {
		return fmt.Errorf("browse session 页面不存在: %s", s.id)
	}
	action := NewCommentFeedActionWithState(page.Context(opCtx), s.state)
	if err := action.PostComment(opCtx, feedID, xsecToken, content); err != nil {
		return err
	}
	s.mu.Lock()
	s.read = true
	s.recordTimelineLocked("comment", feedID, "ok", time.Now(), compactTimelineNote(content))
	s.mu.Unlock()
	s.probeWatchdogSelectorsForKind(opCtx, XHSReadyDetail, feedID)
	return nil
}

func (s *BrowseSession) Reply(ctx context.Context, commentID, userID, content string) error {
	return s.reply(ctx, "", commentID, userID, content)
}

func (s *BrowseSession) ReplyForFeed(ctx context.Context, expectedFeedID, commentID, userID, content string) error {
	return s.reply(ctx, expectedFeedID, commentID, userID, content)
}

func (s *BrowseSession) reply(ctx context.Context, expectedFeedID, commentID, userID, content string) (err error) {
	opCtx, err := s.beginLockedOperation(ctx, true)
	if err != nil {
		return err
	}
	defer func() { s.finishOperation(err) }()

	feedID, xsecToken, err := s.currentFeedFor(expectedFeedID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	page := s.page
	s.mu.Unlock()
	if page == nil {
		return fmt.Errorf("browse session 页面不存在: %s", s.id)
	}
	action := NewCommentFeedActionWithState(page.Context(opCtx), s.state)
	if err := action.ReplyToComment(opCtx, feedID, xsecToken, commentID, userID, content); err != nil {
		return err
	}
	s.mu.Lock()
	s.read = true
	s.recordTimelineLocked("reply", feedID, "ok", time.Now(), compactTimelineNote(content))
	s.mu.Unlock()
	s.probeWatchdogSelectorsForKind(opCtx, XHSReadyDetail, feedID)
	return nil
}

func historyTargetReady(probe xhsReadyProbe, fromURL string, kind XHSReadyKind, fromDetail bool) bool {
	targetReady := isXHSReady(probe, kind, "", false)
	if fromDetail {
		return probe.VisibleDetailCount == 0 && targetReady
	}
	return probe.URL != "" && probe.URL != fromURL && targetReady
}

func waitForHistoryTargetReady(ctx context.Context, page *hrod.Page, fromURL, sourceURL string, fromDetail bool) (xhsReadyProbe, error) {
	deadline := time.Now().Add(browseSessionBackTimeout)
	var last xhsReadyProbe
	var lastErr error
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return last, err
		}
		probe, err := probeXHSReadyFull(page.Context(ctx), "")
		if err != nil {
			lastErr = err
		} else {
			last = probe
			kindURL := probe.URL
			if sourceURL != "" {
				kindURL = sourceURL
			}
			kind := inferXHSReadyKindFromURL(kindURL)
			if probe.RiskText != "" {
				return last, fmt.Errorf("返回页出现风险信号: %s", probe.RiskText)
			}
			if historyTargetReady(probe, fromURL, kind, fromDetail) {
				return probe, nil
			}
		}
		if err := page.Context(ctx).SleepRandom(300*time.Millisecond, 500*time.Millisecond); err != nil {
			return last, err
		}
	}
	if lastErr != nil {
		return last, fmt.Errorf("等待 history.back 目标页超时: %w; %s", lastErr, formatXHSReadyProbe(last))
	}
	return last, fmt.Errorf("等待 history.back 目标页超时: %s", formatXHSReadyProbe(last))
}

func (s *BrowseSession) Back(ctx context.Context) (err error) {
	opCtx, err := s.beginLockedOperation(ctx, true)
	if err != nil {
		return err
	}
	defer func() { s.finishOperation(err) }()

	s.mu.Lock()
	page := s.page
	feedID := s.currentFeedID
	sourceURL := s.sourceURL
	opened := s.opened
	s.mu.Unlock()

	if page == nil {
		return fmt.Errorf("browse session 页面不存在: %s", s.id)
	}

	counter := &evalTimeoutCounter{}
	fromURL, err := s.currentPageURL(opCtx, counter)
	if err != nil {
		return fmt.Errorf("读取后退前 URL: %w", err)
	}

	probe, err := probeXHSReadyFull(page.Context(opCtx), feedID)
	if err != nil {
		return fmt.Errorf("后退前页面探测失败: %w", err)
	}
	if opened && feedID != "" && probe.VisibleDetailCount == 0 {
		return fmt.Errorf("页面状态不一致：session 声明已打开笔记(%s)，但当前页面无可见详情，详情工具已禁用，请先 get_page_state 查看状态", feedID)
	}
	fromDetail := probe.VisibleDetailCount > 0

	// 通用后退：从任意页面返回上一步
	if _, err := evalJS(opCtx, counter, page, `() => window.history.back()`); err != nil {
		return fmt.Errorf("history.back 失败: %w", err)
	}

	probe, err = waitForHistoryTargetReady(opCtx, page, fromURL, sourceURL, fromDetail)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.currentURL = probe.URL
	s.scrollY = probe.ScrollY
	s.sourceURL = ""
	s.currentFeedID = ""
	s.currentXsecToken = ""
	s.opened = false
	s.read = false
	s.resetNotificationSurfaceLocked()
	s.recordTimelineLocked("back", feedID, "ok", time.Now(), "history.back()")
	s.mu.Unlock()
	return nil
}

// UnreadNotificationCount 从当前保留页面读取未读数，不点击入口，不清未读。
func (s *BrowseSession) UnreadNotificationCount(ctx context.Context) (count *NotificationCount, err error) {
	opCtx, err := s.beginLockedOperation(ctx, true)
	if err != nil {
		return nil, err
	}
	defer func() { s.finishOperation(err) }()

	s.mu.Lock()
	page := s.page
	s.mu.Unlock()
	if page == nil {
		return nil, fmt.Errorf("browse session 页面不存在: %s", s.id)
	}
	counter := &evalTimeoutCounter{}
	return readNotificationCount(opCtx, page.Context(opCtx), counter)
}

// ListNotifications 通过侧栏真实点击进入通知页/切换 tab 并分页读取。
// 传空 cursor：进入通知页/切 tab/fresh 重新读取首批并建立新 generation；
// 传有效 cursor：同 tab 续页；过期/无效 cursor 明确拒绝，不做静默 fresh list。
func (s *BrowseSession) ListNotifications(ctx context.Context, rawTab string, cursor string, maxItems int) (list *NotificationList, err error) {
	tab, err := ParseNotificationTab(rawTab)
	if err != nil {
		return nil, err
	}
	maxItems = normalizeNotificationPageSize(maxItems)

	opCtx, err := s.beginLockedOperation(ctx, true)
	if err != nil {
		return nil, err
	}
	defer func() { s.finishOperation(err) }()

	s.mu.Lock()
	page := s.page
	s.mu.Unlock()
	if page == nil {
		return nil, fmt.Errorf("browse session 页面不存在: %s", s.id)
	}
	page = page.Context(opCtx)
	counter := &evalTimeoutCounter{}

	s.mu.Lock()
	notif := s.notification
	s.mu.Unlock()

	switch {
	case cursor == "":
		list, err = s.startNotificationListLocked(opCtx, page, counter, tab, maxItems)
	case notif.active && notif.tab == tab && notif.cursor == cursor:
		list, err = s.continueNotificationListLocked(opCtx, page, counter, tab, maxItems)
	default:
		return nil, fmt.Errorf("cursor 已过期或无效（tab/generation 已变化），请传空 cursor 重新 list_notifications")
	}
	if err != nil {
		return nil, err
	}
	return list, nil
}

// startNotificationListLocked 建立/刷新通知 surface generation，并读取首批。
// 首次真实点击通知入口；跨 tab 真实切换 tab；同 tab fresh list 视为新 generation。
func (s *BrowseSession) startNotificationListLocked(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter, tab NotificationTab, maxItems int) (*NotificationList, error) {
	s.mu.Lock()
	notif := s.notification
	s.mu.Unlock()
	if !notif.active {
		if err := enterNotificationPage(ctx, page, counter); err != nil {
			return nil, err
		}
	} else if notif.tab != tab {
		if err := switchNotificationTab(ctx, page, counter, tab); err != nil {
			return nil, err
		}
	}

	s.mu.Lock()
	s.beginNotificationSurfaceLocked(tab)
	s.mu.Unlock()
	return s.refreshNotificationListLocked(ctx, page, counter, tab, maxItems)
}

// continueNotificationListLocked 同 tab 续页：逐轮拟人滚动并累计各轮 fresh 到本次最终结果（事务式）：
// 每轮只把条目收集到局部 batch，任何中途错误都不提交 returned/items/targets（可原样重试）；
// 只有最终响应组装成功后，才一次性提交并返回。提前停止条件照旧：已达 maxItems、hasMore=false 或连续两轮无新增。
func (s *BrowseSession) continueNotificationListLocked(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter, tab NotificationTab, maxItems int) (*NotificationList, error) {
	batch := make([]NotificationItem, 0, maxItems)
	totalFiltered := 0
	consecutiveNoNew := 0
	lastHasMore := true
	seen := make(map[string]bool)
	emptySeen := 0
	for round := 0; round < 6; round++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := page.SleepRandom(1500*time.Millisecond, 3500*time.Millisecond); err != nil {
			return nil, err
		}
		delta := 350 + rand.Intn(351)
		if err := page.Actor().Mouse.Scroll(0, float64(delta)); err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.notification.scrollCount++
		s.mu.Unlock()

		budget := maxItems
		if maxItems > 0 {
			budget = maxItems - len(batch)
		}
		fresh, filteredNew, hasMore, err := s.collectNotificationItems(ctx, page, counter, tab, budget, seen, &emptySeen)
		if err != nil {
			return nil, err
		}
		batch = append(batch, fresh...)
		totalFiltered += filteredNew
		lastHasMore = hasMore

		if maxItems > 0 && len(batch) >= maxItems {
			break
		}
		if !lastHasMore {
			break
		}
		if len(fresh) == 0 {
			consecutiveNoNew++
			if consecutiveNoNew >= 2 {
				break
			}
			continue
		}
		consecutiveNoNew = 0
	}
	// 组装成功，事务式提交：只在此处一次性写入 returned/items/targets。
	return s.commitNotificationListLocked(ctx, tab, batch, totalFiltered, lastHasMore)
}

// commitNotificationListLocked 通用的最终响应组装：事务式提交 batch 后更新 cursor/hasMore 并记录时间线。
// fresh（refreshNotificationListLocked）与续页（continueNotificationListLocked）共用。
func (s *BrowseSession) commitNotificationListLocked(_ context.Context, tab NotificationTab, batch []NotificationItem, filtered int, hasMore bool) (*NotificationList, error) {
	s.mu.Lock()
	s.commitNotificationSelectionsLocked(tab, batch)
	cursor := notificationOpaqueToken("nc")
	s.notification.cursor = cursor
	s.notification.hasMore = hasMore
	s.recordTimelineLocked("list_notifications", string(tab), "ok", time.Now(),
		fmt.Sprintf("items=%d filtered=%d has_more=%t", len(batch), filtered, hasMore))
	s.mu.Unlock()

	return &NotificationList{
		Tab:          tab,
		Items:        batch,
		Filtered:     filtered,
		Cursor:       cursor,
		HasMore:      hasMore,
		ClearsUnread: true,
		ResultCount:  len(batch),
	}, nil
}

// collectNotificationItems 依据当前 state+DOM，在已返回集合基础上挑选一批新条目（事务式，不写 session 状态）：
// 候选集 = 会话 returnedIDs ∪ 本次调用已见集合（seen）。返回本批条目、本批新过滤数、hasMore；
// 不标记 returned/不更新 items/targets，由调用方在最终响应组装成功后统一提交，中途错误可原样重试。
// 超过正预算（budget>0）的不标记，留待下次续页（budget<=0 视为不设上限）。
func (s *BrowseSession) collectNotificationItems(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter, tab NotificationTab, budget int, seen map[string]bool, emptySeen *int) (fresh []NotificationItem, filteredNew int, hasMore bool, err error) {
	state, err := readNotificationTabState(ctx, page, counter, tab)
	if err != nil {
		return nil, 0, false, err
	}
	dom, err := readNotificationDOMSnapshot(ctx, page, counter)
	if err != nil {
		return nil, 0, false, err
	}

	s.mu.Lock()
	refByID := make(map[string]string)
	for ref, t := range s.notification.targets {
		if t.Item.ID != "" {
			refByID[t.Item.ID] = ref
		}
	}
	returned := s.notification.returnedIDs
	s.mu.Unlock()

	items, _ := convertNotifications(state.MessageList, dom, tab, func(entry rawNotification, _ int) string {
		if entry.ID != "" {
			if saved, ok := refByID[entry.ID]; ok {
				return saved
			}
		}
		return notificationOpaqueToken("nr")
	})

	// 只统计本批「新过滤」条目：本次调用首次见到的不可见条目，以及超出已见数的空 ID 条目。
	emptyInRound := 0
	for _, r := range state.MessageList {
		if r.ID == "" {
			emptyInRound++
			continue
		}
		if seen[r.ID] {
			continue
		}
		if !r.visible() {
			seen[r.ID] = true
			filteredNew++
		}
	}
	if emptyInRound > *emptySeen {
		filteredNew += emptyInRound - *emptySeen
		*emptySeen = emptyInRound
	}

	// 当前 state 中尚未返回且本次调用未挑选的条目数（预算截断后仍留在本地的缓冲）。
	unreturned := 0
	for _, it := range items {
		if it.ID != "" && !returned[it.ID] && !seen[it.ID] {
			unreturned++
		}
	}

	fresh = make([]NotificationItem, 0, len(items))
	for _, it := range items {
		if it.ID == "" {
			continue // 空 ID 无法去重/截断，fail-closed：不在结果中，已在 filteredNew 计入
		}
		if returned[it.ID] || seen[it.ID] {
			continue
		}
		if budget > 0 && len(fresh) >= budget {
			break // 达到预算上限，不选本批，留待下次续页返回
		}
		seen[it.ID] = true
		fresh = append(fresh, it)
	}
	// has_more = 服务端还有更多，或本地仍有未返回的缓冲条目（避免截断后不可达）。
	hasMore = state.HasMore || unreturned > len(fresh)
	return fresh, filteredNew, hasMore, nil
}

// commitNotificationSelectionsLocked 在最终响应组装成功后一次性提交本次调用选中的条目：
// 标记 returned、surface 只保存最近一次成功返回的 batch、重建 targets（保留本 generation 全部已签发 ref）。
// 中途任何错误都不得调用它，保证重试可重新收集。
func (s *BrowseSession) commitNotificationSelectionsLocked(tab NotificationTab, fresh []NotificationItem) {
	generation := s.notification.generation
	for _, it := range fresh {
		if it.ID != "" {
			s.notification.returnedIDs[it.ID] = true
		}
	}
	// surface 只保存最近一次成功返回的 batch，避免状态与语义动作无限膨胀；
	// 复制一份再保存，避免与返回响应共享底层 slice（并发写 liked 不影响响应）。
	s.notification.items = append([]NotificationItem(nil), fresh...)
	nextTargets := make(map[string]notificationTarget)
	for ref, t := range s.notification.targets {
		if t.Generation == generation {
			nextTargets[ref] = t
		}
	}
	for _, it := range fresh {
		if it.Actionable && it.NotificationRef != "" {
			nextTargets[it.NotificationRef] = notificationTarget{
				Ref:        it.NotificationRef,
				Tab:        tab,
				Generation: generation,
				Item:       it,
			}
		}
	}
	s.notification.targets = nextTargets
}

// refreshNotificationListLocked 单轮刷新（fresh 起始调用）：挑选首批，成功后再统一提交并组装响应。
func (s *BrowseSession) refreshNotificationListLocked(ctx context.Context, page *hrod.Page, counter *evalTimeoutCounter, tab NotificationTab, maxItems int) (*NotificationList, error) {
	seen := make(map[string]bool)
	emptySeen := 0
	fresh, filtered, hasMore, err := s.collectNotificationItems(ctx, page, counter, tab, maxItems, seen, &emptySeen)
	if err != nil {
		return nil, err
	}
	return s.commitNotificationListLocked(ctx, tab, fresh, filtered, hasMore)
}

// LikeNotification 点赞/取消点赞当前 notification surface 中的评论通知，幂等。
func (s *BrowseSession) LikeNotification(ctx context.Context, notificationRef string, unlike bool) (result *NotificationLikeResult, err error) {
	opCtx, err := s.beginLockedOperation(ctx, true)
	if err != nil {
		return nil, err
	}
	defer func() { s.finishOperation(err) }()

	page, target, err := s.resolveNotificationWriteTarget(opCtx, notificationRef)
	if err != nil {
		return nil, err
	}

	result, err = likeNotificationOnPage(ctx, page, target, unlike)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	if t, ok := s.notification.targets[target.Ref]; ok {
		t.Item.Liked = result.Liked
		s.notification.targets[target.Ref] = t
	}
	// 同步更新 items 中对应条目的点赞状态，保证后续 get_page_state 读到最新 liked。
	for i := range s.notification.items {
		if s.notification.items[i].NotificationRef == target.Ref {
			s.notification.items[i].Liked = result.Liked
		}
	}
	action := "like_notification"
	if unlike {
		action = "unlike_notification"
	}
	s.recordTimelineLocked(action, target.Ref, "ok", time.Now(), fmt.Sprintf("liked=%t skipped=%t", result.Liked, result.Skipped))
	s.mu.Unlock()
	if s.state != nil {
		_ = s.state.RecordInteraction("", "like_notification")
	}
	return result, nil
}

// ReplyNotification 回复当前 notification surface 中的评论通知。
func (s *BrowseSession) ReplyNotification(ctx context.Context, notificationRef, content string) (result *NotificationReplyResult, err error) {
	if normalizeNotificationText(content) == "" {
		return nil, fmt.Errorf("回复内容不能为空")
	}
	opCtx, err := s.beginLockedOperation(ctx, true)
	if err != nil {
		return nil, err
	}
	defer func() { s.finishOperation(err) }()

	page, target, err := s.resolveNotificationWriteTarget(opCtx, notificationRef)
	if err != nil {
		return nil, err
	}

	result, err = replyNotificationOnPage(ctx, page, target, content)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.recordTimelineLocked("reply_notification", target.Ref, "ok", time.Now(), compactTimelineNote(content))
	s.mu.Unlock()
	if s.state != nil {
		_ = s.state.RecordInteraction("", "reply_notification")
	}
	return result, nil
}

// resolveNotificationWriteTarget 在 operation 已取得后解析当前写 target 并校验 notification surface 与风控。
// beginLockedOperation/defer finishOperation 仍由公开方法管理，此 helper 不持有或释放 session operation。
func (s *BrowseSession) resolveNotificationWriteTarget(opCtx context.Context, notificationRef string) (*hrod.Page, notificationTarget, error) {
	s.mu.Lock()
	page := s.page
	active := s.notification.active
	generation := s.notification.generation
	target, ok := s.resolveNotificationTargetLocked(notificationRef)
	s.mu.Unlock()
	if !ok {
		return nil, notificationTarget{}, fmt.Errorf("notification_ref 引用已过期或不存在，请重新 list_notifications")
	}
	if !active || target.Tab != TabMentions || target.Generation != generation {
		return nil, notificationTarget{}, fmt.Errorf("notification_ref 已过期（tab/generation 已变化），请重新 list_notifications")
	}
	if page == nil {
		return nil, notificationTarget{}, fmt.Errorf("browse session 页面不存在: %s", s.id)
	}
	page = page.Context(opCtx)
	if err := s.checkNotificationWritePreLocked(opCtx); err != nil {
		return nil, notificationTarget{}, err
	}
	signal, err := ClassifyRisk(page)
	if err != nil {
		return nil, notificationTarget{}, err
	}
	if IsRisk(signal) {
		return nil, notificationTarget{}, fmt.Errorf("风控中止: %s", signal.Reason)
	}
	return page, target, nil
}

// checkNotificationWritePreLocked 写操作前检查 ActionStateStore 风控冷却。
func (s *BrowseSession) checkNotificationWritePreLocked(ctx context.Context) error {
	if s.state == nil {
		return nil
	}
	state, err := s.state.Load()
	if err != nil {
		return err
	}
	if state.RiskCooldownUntil.After(time.Now()) {
		return fmt.Errorf("账号处于风控冷却中，冷却至 %s：%s",
			state.RiskCooldownUntil.Format(time.RFC3339), state.LastRiskText)
	}
	return nil
}

// resolveNotificationTargetLocked 按 ref 查找当前 surface 中的写操作 target。
// 必须在持锁时调用。
func (s *BrowseSession) resolveNotificationTargetLocked(ref string) (notificationTarget, bool) {
	if ref == "" || !s.notification.active {
		return notificationTarget{}, false
	}
	t, ok := s.notification.targets[ref]
	return t, ok
}

// beginNotificationSurfaceLocked 开启新 generation 并清空旧 ref/cursor。必须在持锁时调用。
// 同时记录进入通知页前的 URL 到 sourceURL，并清除 feed 打开状态。
func (s *BrowseSession) beginNotificationSurfaceLocked(tab NotificationTab) {
	if !s.notification.active {
		if s.currentURL != "" {
			s.sourceURL = s.currentURL
		}
		s.currentFeedID = ""
		s.currentXsecToken = ""
		s.opened = false
		s.read = false
	}
	s.notification.active = true
	s.notification.generation++
	s.notification.tab = tab
	s.notification.enteredAt = time.Now()
	s.notification.scrollCount = 0
	s.notification.cursor = ""
	s.notification.items = nil
	s.notification.targets = make(map[string]notificationTarget)
	s.notification.returnedIDs = make(map[string]bool)
}

// resetNotificationSurfaceLocked 清空通知 surface（进入首页/搜索/打开笔记/后退等导航失效点）。
func (s *BrowseSession) resetNotificationSurfaceLocked() {
	s.notification = browseNotificationState{
		targets:     make(map[string]notificationTarget),
		returnedIDs: make(map[string]bool),
	}
}

// notificationSurfaceLocked 返回对外通知 surface 快照。必须在持锁时调用。
func (s *BrowseSession) notificationSurfaceLocked() *BrowseSessionNotificationSurface {
	if !s.notification.active {
		return nil
	}
	return &BrowseSessionNotificationSurface{
		Tab:         s.notification.tab,
		Generation:  s.notification.generation,
		EnteredAt:   s.notification.enteredAt,
		ScrollCount: s.notification.scrollCount,
		ResultCount: len(s.notification.items),
		Items:       append([]NotificationItem(nil), s.notification.items...),
		Cursor:      s.notification.cursor,
		HasMore:     s.notification.hasMore,
	}
}

func normalizeNotificationPageSize(maxItems int) int {
	if maxItems <= 0 {
		return 10
	}
	if maxItems > 20 {
		return 20
	}
	return maxItems
}

func notificationOpaqueToken(prefix string) string {
	var buf [8]byte
	if _, err := crand.Read(buf[:]); err == nil {
		return prefix + "_" + hex.EncodeToString(buf[:])
	}
	return prefix + "_" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

func (s *BrowseSession) CheckReusable(ctx context.Context) ReuseCheck {
	select {
	case <-s.opToken:
	default:
		return ReuseCheck{Status: SessionBusy, LastError: "session 正在执行操作"}
	}
	defer func() { s.opToken <- struct{}{} }()

	checkedAt := time.Now()

	s.mu.Lock()
	closed := s.closed
	expired := !closed && time.Now().After(s.expiresAt)
	page := s.page
	s.mu.Unlock()

	if closed {
		return ReuseCheck{Status: SessionNotReady, HealthCheckedAt: checkedAt, Ready: false, LastError: "session 已关闭"}
	}

	if expired {
		return ReuseCheck{Status: SessionExpired, LastError: "session 已过期", HealthCheckedAt: checkedAt, Ready: false}
	}

	if page == nil {
		return ReuseCheck{Status: SessionNotReady, LastError: "session 页面不存在", HealthCheckedAt: checkedAt, Ready: false}
	}

	probeScript := `() => JSON.stringify({url: location.href, readyState: document.readyState})`

	var errProbeTimeout = fmt.Errorf("健康检查超时")
	var errProbeCDP = fmt.Errorf("CDP 连接异常")
	var errProbeJS = fmt.Errorf("页面 JS 不可用")

	probe := func(budget time.Duration) (*reusePageState, error) {
		evalCtx, cancel := context.WithTimeout(ctx, budget)
		defer cancel()
		result, err := s.evalJS(evalCtx, page, probeScript)
		if err != nil || result == nil {
			if errors.Is(ctx.Err(), context.Canceled) {
				return nil, ctx.Err()
			}
			if errors.Is(evalCtx.Err(), context.DeadlineExceeded) {
				return nil, errProbeTimeout
			}
			return nil, errProbeCDP
		}
		var pageState reusePageState
		if err := json.Unmarshal([]byte(result.Value.Str()), &pageState); err != nil || pageState.URL == "" || pageState.ReadyState == "" {
			return nil, errProbeJS
		}
		return &pageState, nil
	}

	const firstProbeBudget = 2 * time.Second

	pageState, err := probe(firstProbeBudget)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return ReuseCheck{
				Status:          SessionNotReady,
				LastError:       "请求已取消",
				HealthCheckedAt: time.Now(),
				Ready:           false,
			}
		}
		if errors.Is(err, errProbeTimeout) {
			time.Sleep(150 * time.Millisecond)
			pageState, err = probe(healthCheckTimeout - firstProbeBudget - 150*time.Millisecond)
		}
		if err != nil {
			risk := "cdp_disconnected"
			if errors.Is(err, errProbeJS) {
				risk = "js_unavailable"
			}
			return ReuseCheck{
				Status:          SessionUnhealthy,
				LastError:       err.Error(),
				HealthCheckedAt: time.Now(),
				Ready:           false,
				Risk:            risk,
			}
		}
	}

	if strings.HasPrefix(pageState.URL, "about:") {
		return ReuseCheck{
			Status:          SessionNotReady,
			LastError:       "页面URL异常",
			HealthCheckedAt: time.Now(),
			Ready:           false,
		}
	}

	readyState := pageState.ReadyState

	if readyState != "complete" && readyState != "interactive" {
		return ReuseCheck{
			Status:          SessionNotReady,
			LastError:       fmt.Sprintf("页面未就绪: %s", readyState),
			HealthCheckedAt: time.Now(),
			Ready:           false,
		}
	}

	return ReuseCheck{
		Status:          SessionReady,
		HealthCheckedAt: time.Now(),
		Ready:           true,
	}
}

func (s *BrowseSession) Close() {
	s.close()
}

// TryCloseIdle 非阻塞尝试关闭空闲 session。
// 获取 opToken 成功说明当前无操作，原子关闭并释放页面，返回 true；
// 失败说明有操作正在执行，返回 false（调用方应返回 blocked 而非自动重建）。
func (s *BrowseSession) TryCloseIdle() bool {
	select {
	case <-s.opToken:
	default:
		return false
	}
	defer func() { s.opToken <- struct{}{} }()

	s.close()
	return true
}

func (s *BrowseSession) ClassifyRisk() (RiskSignal, error) {
	return s.ClassifyRiskContext(context.Background())
}

func (s *BrowseSession) ClassifyRiskContext(ctx context.Context) (signal RiskSignal, err error) {
	opCtx, err := s.beginLockedOperation(ctx, false)
	if err != nil {
		return RiskSignal{Kind: RiskNone, DetectedAt: time.Now()}, err
	}
	defer func() { s.finishOperation(err) }()

	s.mu.Lock()
	page := s.page
	s.mu.Unlock()
	if page == nil {
		return RiskSignal{Kind: RiskNone, DetectedAt: time.Now()}, nil
	}
	return ClassifyRisk(page.Context(opCtx))
}

func (s *BrowseSession) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.closedCh)
	if s.timer != nil {
		s.timer.Stop()
	}
	cancel := s.activeOp
	page := s.page
	onClose := s.onClose
	onRemove := s.onRemove
	s.page = nil
	s.onClose = nil
	s.mu.Unlock()

	if onRemove != nil {
		onRemove(s)
	}
	if cancel != nil {
		cancel()
	}
	if onClose != nil {
		onClose(page)
	}
}

func (s *BrowseSession) beginLockedOperation(ctx context.Context, touchTTL bool) (context.Context, error) {
	select {
	case <-s.opToken:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.closedCh:
		return nil, fmt.Errorf("browse session 已关闭: %s", s.id)
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.opToken <- struct{}{}
		return nil, fmt.Errorf("browse session 已关闭: %s", s.id)
	}
	if time.Now().After(s.expiresAt) {
		s.mu.Unlock()
		s.opToken <- struct{}{}
		return nil, fmt.Errorf("browse session 已过期: %s", s.id)
	}
	// token 就绪后、续 TTL 前，再次检查 ctx 取消或 session 关闭
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		s.opToken <- struct{}{}
		return nil, err
	}
	s.touchOnFinish = touchTTL
	if touchTTL {
		s.touchLocked()
	}
	s.opCtx, s.activeOp = context.WithCancel(ctx)
	s.mu.Unlock()
	return s.opCtx, nil
}

func (s *BrowseSession) finishOperation(operationErr error) {
	s.endOperation(operationErr)
	s.releaseOperation()
}

func (s *BrowseSession) endOperation(operationErr error) {
	s.mu.Lock()
	closed := s.closed
	opCtx := s.opCtx
	expired := !closed && time.Now().After(s.expiresAt)
	s.mu.Unlock()
	fatal := IsFatalRendererError(operationErr)

	if expired {
		s.close()
		return
	}

	if !closed && !fatal && opCtx != nil {
		s.refreshPageState(opCtx)
	}

	s.mu.Lock()
	if !s.closed && !fatal && s.touchOnFinish && opCtx != nil {
		if time.Now().After(s.expiresAt) {
			s.mu.Unlock()
			s.close()
			return
		}
		s.touchLocked()
	}
	s.mu.Unlock()
}

func (s *BrowseSession) releaseOperation() {
	s.mu.Lock()
	cancel := s.activeOp
	if cancel != nil {
		cancel()
	}
	s.opCtx = nil
	s.activeOp = nil
	s.mu.Unlock()

	s.opToken <- struct{}{}
}

func (s *BrowseSession) resolveResult(resultRef string) (Feed, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if resultRef == "" {
		return Feed{}, fmt.Errorf("搜索结果引用不能为空")
	}
	if index, err := strconv.Atoi(resultRef); err == nil {
		if index < 0 || index >= s.nextResultIndex {
			return Feed{}, fmt.Errorf("搜索结果引用索引越界: %s（有效范围 0-%d）", resultRef, max(0, s.nextResultIndex-1))
		}
	}
	feed, ok := s.results[resultRef]
	if !ok {
		return Feed{}, fmt.Errorf("未找到搜索结果引用: %s", resultRef)
	}
	return feed, nil
}

func (s *BrowseSession) currentOpenedFeedID() (string, error) {
	return s.currentOpenedFeedIDFor("")
}

func (s *BrowseSession) currentOpenedFeedIDFor(expectedFeedID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.opened || s.currentFeedID == "" {
		return "", fmt.Errorf("必须先打开笔记")
	}
	if expectedFeedID != "" && s.currentFeedID != expectedFeedID {
		return "", fmt.Errorf("session 当前笔记 %s 与目标笔记 %s 不一致", s.currentFeedID, expectedFeedID)
	}
	return s.currentFeedID, nil
}

func (s *BrowseSession) currentFeedFor(expectedFeedID string) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.opened || s.currentFeedID == "" {
		return "", "", fmt.Errorf("互动只能对已打开的笔记执行")
	}
	if !s.read {
		return "", "", fmt.Errorf("互动只能对已阅读的笔记执行")
	}
	if expectedFeedID != "" && s.currentFeedID != expectedFeedID {
		return "", "", fmt.Errorf("session 当前笔记 %s 与目标笔记 %s 不一致", s.currentFeedID, expectedFeedID)
	}
	return s.currentFeedID, s.currentXsecToken, nil
}

func (s *BrowseSession) isExpired() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed || time.Now().After(s.expiresAt)
}

func (s *BrowseSession) touchLocked() {
	s.expiresAt = time.Now().Add(s.timeout)
	if s.timer != nil {
		s.timer.Stop()
	}
	expiresAt := s.expiresAt
	s.timer = time.AfterFunc(s.timeout, func() {
		s.closeExpired(expiresAt)
	})
}

func (s *BrowseSession) closeExpired(expiresAt time.Time) {
	s.mu.Lock()
	expired := !s.closed && s.expiresAt.Equal(expiresAt) && !time.Now().Before(s.expiresAt)
	s.mu.Unlock()
	if !expired {
		return
	}
	s.close()
}

func (s *BrowseSession) reconcileAfterFailedSearch(operationErr error) {
	if operationErr == nil {
		return
	}
	s.mu.Lock()
	page := s.page
	closed := s.closed
	s.mu.Unlock()
	if closed || page == nil {
		return
	}
	info, err := page.Rod.Info()
	if err != nil {
		return
	}
	u, err := url.Parse(info.URL)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.opened && s.currentFeedID != "" && !strings.HasPrefix(u.Path, "/explore/"+s.currentFeedID) {
		s.sourceURL = ""
		s.currentFeedID = ""
		s.currentXsecToken = ""
		s.opened = false
		s.read = false
	}
}

func (s *BrowseSession) refreshPageState(ctx context.Context) {
	s.mu.Lock()
	page := s.page
	closed := s.closed
	s.mu.Unlock()
	if closed || page == nil {
		return
	}

	evalCtx, cancel := context.WithTimeout(ctx, browseSessionRefreshTimeout)
	defer cancel()

	// 一次 Eval 返回 {url, scroll_y}，解析一次后持锁提交；缺失字段只跳过该字段。
	var snapshot struct {
		URL     *string `json:"url"`
		ScrollY *int    `json:"scroll_y"`
	}
	got := false
	if s.evalJS != nil {
		if result, err := s.evalJS(evalCtx, page, `() => JSON.stringify({
			url: location.href,
			scroll_y: Math.round(window.scrollY || document.scrollingElement?.scrollTop || 0),
		})`); err == nil && result != nil {
			if err := json.Unmarshal([]byte(result.Value.Str()), &snapshot); err == nil {
				got = true
			}
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.page != page {
		return
	}
	if got {
		if snapshot.URL != nil && *snapshot.URL != "" {
			s.currentURL = *snapshot.URL
		}
		if snapshot.ScrollY != nil {
			s.scrollY = *snapshot.ScrollY
		}
	}
}

func (s *BrowseSession) probeWatchdogSelectorsForKind(ctx context.Context, kind XHSReadyKind, feedID string) {
	if ctx == nil {
		return
	}
	s.mu.Lock()
	page := s.page
	closed := s.closed
	s.mu.Unlock()
	if closed || page == nil {
		return
	}

	probeWatchdogSelectors(page.Context(ctx), XHSReadyOptions{Kind: kind, FeedID: feedID})
}

func (s *BrowseSession) currentPageURL(ctx context.Context, counter *evalTimeoutCounter) (string, error) {
	s.mu.Lock()
	page := s.page
	s.mu.Unlock()
	if page == nil {
		return "", nil
	}
	result, err := evalJS(ctx, counter, page, `() => location.href`)
	if err != nil || result == nil {
		return "", err
	}
	return result.Value.Str(), nil
}

func (s *BrowseSession) infoLocked() BrowseSessionInfo {
	seen := make(map[string]bool, len(s.seenNotes))
	for id, ok := range s.seenNotes {
		seen[id] = ok
	}
	return BrowseSessionInfo{
		ID:            s.id,
		CurrentURL:    redactSensitiveURL(s.currentURL),
		SourceURL:     redactSensitiveURL(s.sourceURL),
		ScrollY:       s.scrollY,
		CurrentFeedID: s.currentFeedID,
		Opened:        s.opened,
		Read:          s.read,
		SeenNotes:     seen,
		ExpiresAt:     s.expiresAt,
	}
}

func (s *BrowseSession) currentStateLocked(kind XHSReadyKind, resultsCount int, availableActions []string) BrowseSessionCurrent {
	return BrowseSessionCurrent{
		Kind:           kind,
		URL:            redactSensitiveURL(s.currentURL),
		FeedID:         s.currentFeedID,
		Opened:         s.opened,
		Read:           s.read,
		ScrollY:        s.scrollY,
		NextHint:       s.nextHintLocked(resultsCount),
		ResultsCount:   resultsCount,
		AvailableTools: append([]string(nil), availableActions...),
	}
}

func (s *BrowseSession) nextHintLocked(resultsCount int) string {
	switch {
	case s.read:
		return "笔记已打开：首屏标题/正文/作者/互动数据/首页评论/笔记图片 URL（data.note.imageList[].urlDefault/urlPre）已在 open_note 返回；如需理解图片内容，请将 data.note.imageList 中的 URL 交给 vision 工具。可继续操作：get_note_detail(分批加载更多评论)、like_feed、favorite_feed、comment_feed、reply_comment_in_feed(回复当前笔记中的评论)"
	case s.opened:
		return "笔记已打开，内容尚未读取完成；可 go_back 返回搜索结果"
	case s.notification.active:
		return "已进入通知页：可切换 tab 读取列表 (list_notifications)、对 mentions tab 条目点赞 (like_notification) 或回复 (reply_notification)，或 go_back 返回"
	case resultsCount > 0:
		return "可继续：搜索新关键词 (search_feeds)、打开其他笔记 (open_note)、或滚动浏览 feed"
	case resultsCount == 0:
		return "可搜索关键词 (search_feeds) 查找笔记"
	}
	return ""
}

func (s *BrowseSession) uniqueResultCountLocked() int {
	ids := make(map[string]bool, len(s.results))
	for _, feed := range s.results {
		if feed.ID != "" {
			ids[feed.ID] = true
		}
	}
	if len(ids) > 0 {
		return len(ids)
	}
	return len(s.results)
}

func (s *BrowseSession) semanticResultsLocked() []BrowseSessionResult {
	results := make([]BrowseSessionResult, 0, s.uniqueResultCountLocked())
	for index := 0; ; index++ {
		ref := strconv.Itoa(index)
		feed, ok := s.results[ref]
		if !ok {
			break
		}
		author := feed.NoteCard.User.Nickname
		if author == "" {
			author = feed.NoteCard.User.NickName
		}
		results = append(results, BrowseSessionResult{
			Ref:    ref,
			FeedID: feed.ID,
			Title:  feed.NoteCard.DisplayTitle,
			Author: author,
			Seen:   feed.ID != "" && s.seenNotes[feed.ID],
		})
	}
	return results
}

func (s *BrowseSession) availableActionsLocked(resultsCount int) []string {
	// 通知页只暴露通知相关动作，不混入 feed 动作。
	if s.notification.active {
		actions := []string{"get_page_state", "get_unread_count", "list_notifications", "go_back", "close_page"}
		if len(s.notification.targets) > 0 {
			actions = append(actions, "like_notification", "reply_notification")
		}
		return actions
	}
	actions := []string{"get_page_state", "search_feeds", "close_page", "get_unread_count", "list_notifications"}
	if resultsCount > 0 && !s.opened {
		actions = append(actions, "open_note")
	}
	if s.read {
		actions = append(actions, "get_note_detail", "like_feed", "favorite_feed", "comment_feed", "reply_comment_in_feed", "go_back")
	} else if s.opened {
		actions = append(actions, "go_back")
	}
	return actions
}

func (s *BrowseSession) mismatchActionsLocked(results []BrowseSessionResult) ([]string, []BrowseSessionAction) {
	available := []string{"get_page_state", "search_feeds", "close_page"}
	actions := []BrowseSessionAction{
		{Ref: "get_page_state", Tool: "get_page_state", Label: "查看当前页面会话状态"},
		{Ref: "search_feeds", Tool: "search_feeds", Label: "搜索笔记"},
	}
	if len(results) > 0 {
		available = append(available, "open_note")
	}
	for _, result := range results {
		actions = append(actions, BrowseSessionAction{
			Ref:       "open_note:" + result.Ref,
			Tool:      "open_note",
			Label:     "打开搜索结果 " + result.Ref,
			ResultRef: result.Ref,
			FeedID:    result.FeedID,
		})
	}
	actions = append(actions, BrowseSessionAction{Ref: "close_page", Tool: "close_page", Label: "关闭当前页面会话"})
	return available, actions
}

func (s *BrowseSession) semanticActionsLocked(resultsCount int) []BrowseSessionAction {
	actions := []BrowseSessionAction{
		{Ref: "get_page_state", Tool: "get_page_state", Label: "查看当前页面会话状态"},
	}
	// 通知页按通知条目生成语义动作。
	if s.notification.active {
		actions = append(actions, BrowseSessionAction{Ref: "list_notifications", Tool: "list_notifications", Label: "查看通知列表"})
		for _, item := range s.notification.items {
			if !item.Actionable || item.NotificationRef == "" {
				continue
			}
			nick := item.From.Nickname
			if nick == "" {
				nick = item.From.UserID
			}
			actions = append(actions,
				BrowseSessionAction{
					Ref:             "like_notification:" + item.NotificationRef,
					Tool:            "like_notification",
					Label:           "点赞通知评论 " + nick,
					NotificationRef: item.NotificationRef,
					Confirm:         true,
				},
				BrowseSessionAction{
					Ref:             "reply_notification:" + item.NotificationRef,
					Tool:            "reply_notification",
					Label:           "回复通知评论 " + nick,
					NotificationRef: item.NotificationRef,
					Confirm:         true,
				},
			)
		}
		actions = append(actions,
			BrowseSessionAction{Ref: "go_back", Tool: "go_back", Label: "后退到上一页（返回通知入口之前页面）"},
			BrowseSessionAction{Ref: "close_page", Tool: "close_page", Label: "关闭当前页面会话"},
		)
		return actions
	}
	actions = append(actions, BrowseSessionAction{Ref: "search_feeds", Tool: "search_feeds", Label: "搜索笔记"})
	if resultsCount > 0 && !s.opened {
		for index := 0; index < resultsCount; index++ {
			ref := strconv.Itoa(index)
			feed, ok := s.results[ref]
			if !ok {
				continue
			}
			actions = append(actions, BrowseSessionAction{
				Ref:       "open_note:" + ref,
				Tool:      "open_note",
				Label:     "打开搜索结果 " + ref,
				ResultRef: ref,
				FeedID:    feed.ID,
			})
		}
	}
	if s.read {
		actions = append(actions,
			BrowseSessionAction{
				Ref:      "get_note_detail",
				Tool:     "get_note_detail",
				Label:    "继续读取当前笔记评论",
				FeedID:   s.currentFeedID,
				Requires: "opened",
			},
			BrowseSessionAction{
				Ref:      "like_feed",
				Tool:     "like_feed",
				Label:    "点赞当前笔记",
				FeedID:   s.currentFeedID,
				Requires: "opened",
				Confirm:  true,
			},
			BrowseSessionAction{
				Ref:      "favorite_feed",
				Tool:     "favorite_feed",
				Label:    "收藏当前笔记",
				FeedID:   s.currentFeedID,
				Requires: "opened",
				Confirm:  true,
			},
			BrowseSessionAction{
				Ref:      "comment_feed",
				Tool:     "comment_feed",
				Label:    "评论当前笔记",
				FeedID:   s.currentFeedID,
				Requires: "opened",
				Confirm:  true,
			},
			BrowseSessionAction{
				Ref:      "reply_comment_in_feed",
				Tool:     "reply_comment_in_feed",
				Label:    "回复当前笔记中的评论",
				FeedID:   s.currentFeedID,
				Requires: "opened + comment_id/user_id",
				Confirm:  true,
			},
		)
	}
	if s.opened {
		actions = append(actions, BrowseSessionAction{
			Ref:    "go_back",
			Tool:   "go_back",
			Label:  "后退到上一页（关闭笔记/返回）",
			FeedID: s.currentFeedID,
		})
	}
	actions = append(actions, BrowseSessionAction{Ref: "close_page", Tool: "close_page", Label: "关闭当前页面会话"})
	return actions
}

func (s *BrowseSession) recommendedActionLocked(ready bool, results []BrowseSessionResult) *BrowseSessionAction {
	if !ready {
		return &BrowseSessionAction{
			Ref:   "get_page_state",
			Tool:  "get_page_state",
			Label: "重新读取页面会话状态",
		}
	}
	// 通知页按通知上下文推荐：有 actionable 条目先推荐操作，否则刷新列表。
	if s.notification.active {
		for _, item := range s.notification.items {
			if !item.Actionable || item.NotificationRef == "" {
				continue
			}
			nick := item.From.Nickname
			if nick == "" {
				nick = item.From.UserID
			}
			return &BrowseSessionAction{
				Ref:             "reply_notification:" + item.NotificationRef,
				Tool:            "reply_notification",
				Label:           "回复通知评论 " + nick,
				NotificationRef: item.NotificationRef,
				Confirm:         true,
			}
		}
		return &BrowseSessionAction{
			Ref:   "list_notifications",
			Tool:  "list_notifications",
			Label: "刷新通知列表",
		}
	}
	if s.opened {
		return &BrowseSessionAction{
			Ref:    "go_back",
			Tool:   "go_back",
			Label:  "后退到上一页",
			FeedID: s.currentFeedID,
		}
	}
	if !s.opened {
		for _, result := range results {
			if result.Seen {
				continue
			}
			return &BrowseSessionAction{
				Ref:       "open_note:" + result.Ref,
				Tool:      "open_note",
				Label:     "打开下一张未读笔记",
				ResultRef: result.Ref,
				FeedID:    result.FeedID,
			}
		}
		if len(results) > 0 {
			result := results[0]
			return &BrowseSessionAction{
				Ref:       "open_note:" + result.Ref,
				Tool:      "open_note",
				Label:     "打开搜索结果 " + result.Ref,
				ResultRef: result.Ref,
				FeedID:    result.FeedID,
			}
		}
	}
	return &BrowseSessionAction{
		Ref:   "search_feeds",
		Tool:  "search_feeds",
		Label: "搜索笔记",
	}
}

func browseSessionSummary(kind XHSReadyKind, ready bool, resultsCount, seenCount int, current BrowseSessionCurrent, recommendedAction *BrowseSessionAction) string {
	lines := []string{
		fmt.Sprintf("当前: %s ready=%t results=%d seen=%d", kind, ready, resultsCount, seenCount),
	}
	if current.FeedID != "" {
		lines[0] += fmt.Sprintf(" feed_id=%s opened=%t read=%t", current.FeedID, current.Opened, current.Read)
	}
	if current.NextHint != "" {
		lines = append(lines, "下一步: "+current.NextHint)
	}
	if recommendedAction != nil {
		lines = append(lines, "推荐: "+formatBrowseSessionRecommendedAction(*recommendedAction))
	}
	return strings.Join(lines, "\n")
}

func formatBrowseSessionRecommendedAction(action BrowseSessionAction) string {
	parts := []string{action.Tool}
	if action.ResultRef != "" {
		parts = append(parts, "result_ref="+action.ResultRef)
	}
	if action.FeedID != "" {
		parts = append(parts, "feed_id="+action.FeedID)
	}
	if action.ResultRef == "" && action.FeedID == "" && action.Ref != "" {
		parts = append(parts, "ref="+action.Ref)
	}
	return strings.Join(parts, " ")
}

func (s *BrowseSession) recordTimelineLocked(action, target, status string, at time.Time, note string) {
	s.timeline = append(s.timeline, BrowseSessionEvent{
		Action: action,
		Target: target,
		Status: status,
		At:     at,
		Note:   note,
	})
	if len(s.timeline) > maxBrowseSessionTimelineEntries {
		s.timeline = append([]BrowseSessionEvent(nil), s.timeline[len(s.timeline)-maxBrowseSessionTimelineEntries:]...)
	}
}

func (s *BrowseSession) timelineLocked() []BrowseSessionEvent {
	return append([]BrowseSessionEvent(nil), s.timeline...)
}

func compactTimelineNote(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= 40 {
		return value
	}
	return string(runes[:40]) + "..."
}

func filterKeyFromFilters(filters []FilterOption) string {
	data, _ := json.Marshal(filters)
	return string(data)
}

func normalizeFeedPageSize(maxItems int) int {
	if maxItems <= 0 {
		return 35
	}
	if maxItems > 50 {
		return 50
	}
	return maxItems
}

func appendSessionResults(results map[string]Feed, next int, feeds []Feed) (map[string]Feed, int, []Feed) {
	if results == nil {
		results = make(map[string]Feed, len(feeds)*2)
	}
	registered := min(len(feeds), max(0, maxBrowseSessionResults-next))
	for i := 0; i < registered; i++ {
		feeds[i].Index = next
		results[strconv.Itoa(next)] = feeds[i]
		if feeds[i].ID != "" {
			results[feeds[i].ID] = feeds[i]
		}
		next++
	}
	return results, next, feeds[:registered]
}

func trimFeedCursorTail(cursor *FeedCursor, count int) {
	if cursor == nil || count <= 0 {
		return
	}
	remaining := max(0, len(cursor.ReturnedIDs)-count)
	cursor.ReturnedIDs = cursor.ReturnedIDs[:remaining]
}

func replaceSessionResults(feeds []Feed) (map[string]Feed, int) {
	capacity := min(len(feeds), maxBrowseSessionResults)
	results := make(map[string]Feed, capacity*2)
	for i := 0; i < capacity; i++ {
		feeds[i].Index = i
		results[strconv.Itoa(i)] = feeds[i]
		if feeds[i].ID != "" {
			results[feeds[i].ID] = feeds[i]
		}
	}
	return results, capacity
}

func inferXHSReadyKindFromSessionURL(rawURL string) XHSReadyKind {
	if isDetailURL(rawURL) {
		return XHSReadyDetail
	}
	return inferXHSReadyKindFromURL(rawURL)
}

func inferLivePageKind(probe xhsReadyProbe, liveDetail bool) XHSReadyKind {
	if probe.NotificationPageCount > 0 || strings.Contains(probe.URL, "/notification") {
		return XHSReadyNotification
	}
	if liveDetail {
		return XHSReadyDetail
	}
	return inferXHSReadyKindFromSessionURL(probe.URL)
}

func newBrowseSessionID() string {
	var buf [8]byte
	if _, err := crand.Read(buf[:]); err == nil {
		return hex.EncodeToString(buf[:])
	}
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

type parsedShareURL struct {
	NormalizedURL string
	IsShortLink   bool
	ExpectedID    string
	XsecToken     string
}

func strictValidateHTTPSURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("URL解析失败")
	}
	if !u.IsAbs() {
		return nil, fmt.Errorf("URL必须是绝对URL")
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("URL必须使用HTTPS")
	}
	if u.User != nil {
		return nil, fmt.Errorf("URL不能包含userinfo")
	}
	if strings.TrimSpace(u.Fragment) != "" {
		return nil, fmt.Errorf("URL不能包含fragment")
	}
	if u.Port() != "" {
		return nil, fmt.Errorf("URL不能包含显式端口")
	}
	return u, nil
}

func isHex24(s string) bool {
	if len(s) != 24 {
		return false
	}
	for i := 0; i < 24; i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func parseOfficialNoteURL(u *url.URL) (noteID string, xsecToken string, ok bool) {
	host := strings.ToLower(u.Hostname())
	if host != "www.xiaohongshu.com" {
		return "", "", false
	}
	path := u.EscapedPath()
	var prefix string
	if strings.HasPrefix(path, "/explore/") {
		prefix = "/explore/"
	} else if strings.HasPrefix(path, "/discovery/item/") {
		prefix = "/discovery/item/"
	} else {
		return "", "", false
	}
	rest := strings.TrimPrefix(path, prefix)
	if rest == "" || rest == "/" {
		return "", "", false
	}
	decoded, err := url.PathUnescape(rest)
	if err != nil {
		return "", "", false
	}
	if decoded != rest {
		return "", "", false
	}
	if !isHex24(decoded) {
		return "", "", false
	}
	return decoded, u.Query().Get("xsec_token"), true
}

func shareURLToken(finalToken, inputToken string) string {
	if finalToken != "" {
		return finalToken
	}
	return inputToken
}

func parseAndValidateShareURL(raw string) (*parsedShareURL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("share_url不能为空")
	}
	u, err := strictValidateHTTPSURL(raw)
	if err != nil {
		return nil, err
	}
	host := strings.ToLower(u.Hostname())
	if host == "xhslink.com" || host == "www.xhslink.com" || host == "xhslink.cn" || host == "www.xhslink.cn" {
		path := u.EscapedPath()
		if path == "" || path == "/" {
			return nil, fmt.Errorf("短链path不能为空")
		}
		return &parsedShareURL{
			NormalizedURL: raw,
			IsShortLink:   true,
		}, nil
	}
	if strings.ToLower(u.Hostname()) != "www.xiaohongshu.com" {
		return nil, fmt.Errorf("share_url host必须是www.xiaohongshu.com或xhslink.com")
	}
	noteID, xsecToken, ok := parseOfficialNoteURL(u)
	if !ok {
		return nil, fmt.Errorf("share_url path不是有效的笔记页面")
	}
	return &parsedShareURL{
		NormalizedURL: raw,
		IsShortLink:   false,
		ExpectedID:    noteID,
		XsecToken:     xsecToken,
	}, nil
}

func validateFinalNoteURL(finalURL string) (noteID string, xsecToken string, err error) {
	u, err := strictValidateHTTPSURL(finalURL)
	if err != nil {
		return "", "", err
	}
	noteID, xsecToken, ok := parseOfficialNoteURL(u)
	if !ok {
		return "", "", fmt.Errorf("最终URL不是有效的笔记页面")
	}
	return noteID, xsecToken, nil
}

type noteURLPollResult struct {
	URL    string
	NoteID string
}

type NoteURLPollError struct {
	sample            string
	deadlineDiscarded string
	contextState      string
	cause             error
}

func (e *NoteURLPollError) Error() string {
	return "等待笔记URL稳定失败"
}

func (e *NoteURLPollError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *NoteURLPollError) Diagnostic() string {
	if e == nil || e.sample == "" || e.deadlineDiscarded == "" || e.contextState == "" {
		return ""
	}
	return fmt.Sprintf("sample=%s deadline_discarded=%s context=%s", e.sample, e.deadlineDiscarded, e.contextState)
}

func newNoteURLPollError(sample, deadlineDiscarded, contextState string, cause error) *NoteURLPollError {
	return &NoteURLPollError{
		sample:            sample,
		deadlineDiscarded: deadlineDiscarded,
		contextState:      contextState,
		cause:             cause,
	}
}

func noteURLContextState(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	default:
		return "error"
	}
}

func waitForNoteURLStable(ctx context.Context, wallClock time.Duration, readURL func(context.Context) (string, error)) (noteURLPollResult, error) {
	if wallClock <= 0 {
		wallClock = 60 * time.Second
	}
	deadline := time.Now().Add(wallClock)
	const pollInterval = 500 * time.Millisecond
	var lastErr error
	for {
		if ctx.Err() != nil {
			return noteURLPollResult{}, ctx.Err()
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		readCtx, cancel := context.WithTimeout(ctx, remaining)
		rawURL, err := readURL(readCtx)
		cancel()
		if ctx.Err() != nil {
			return noteURLPollResult{}, ctx.Err()
		}
		if time.Now().After(deadline) {
			if err != nil {
				if isTransientCurrentDetailProbeError(err) {
					lastErr = newNoteURLPollError("transient_error", "false", noteURLContextState(err), err)
				} else {
					lastErr = newNoteURLPollError("read_error", "false", "unknown", err)
				}
			} else {
				noteID, _, valErr := validateFinalNoteURL(rawURL)
				if valErr == nil && noteID != "" {
					lastErr = newNoteURLPollError("deadline_discarded", "true", "ok", nil)
				} else if strings.TrimSpace(rawURL) == "" {
					lastErr = newNoteURLPollError("empty_url", "false", "ok", nil)
				} else {
					lastErr = newNoteURLPollError("invalid_url", "false", "ok", nil)
				}
			}
			break
		}
		if err != nil {
			if isTransientCurrentDetailProbeError(err) {
				lastErr = newNoteURLPollError("transient_error", "false", noteURLContextState(err), err)
				sleepDur := min(pollInterval, time.Until(deadline))
				if sleepDur <= 0 {
					break
				}
				if sleepErr := pageSleep(ctx, sleepDur); sleepErr != nil {
					return noteURLPollResult{}, sleepErr
				}
				continue
			}
			if IsFatalRendererError(err) {
				return noteURLPollResult{}, err
			}
			return noteURLPollResult{}, newNoteURLPollError("read_error", "false", "unknown", err)
		}
		noteID, _, valErr := validateFinalNoteURL(rawURL)
		if valErr == nil && noteID != "" {
			return noteURLPollResult{URL: rawURL, NoteID: noteID}, nil
		}
		sample := "invalid_url"
		if strings.TrimSpace(rawURL) == "" {
			sample = "empty_url"
		}
		lastErr = newNoteURLPollError(sample, "false", "ok", nil)
		sleepDur := min(pollInterval, time.Until(deadline))
		if sleepDur <= 0 {
			break
		}
		if sleepErr := pageSleep(ctx, sleepDur); sleepErr != nil {
			return noteURLPollResult{}, sleepErr
		}
	}
	if lastErr != nil {
		return noteURLPollResult{}, fmt.Errorf("等待笔记URL稳定超时: %w", lastErr)
	}
	return noteURLPollResult{}, fmt.Errorf("等待笔记URL稳定超时")
}

func pageSleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

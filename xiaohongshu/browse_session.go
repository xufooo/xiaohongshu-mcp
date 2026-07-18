package xiaohongshu

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod/lib/proto"
	"github.com/sirupsen/logrus"
	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

const (
	DefaultBrowseSessionTimeout       = 10 * time.Minute
	browseSessionRefreshTimeout       = 2 * time.Second
	maxBrowseSessionTimelineEntries   = 10
)

type BrowseSessionInfo struct {
	ID            string         `json:"id"`
	CurrentURL    string         `json:"current_url,omitempty"`
	SourceURL     string         `json:"source_url,omitempty"`
	ScrollY       int            `json:"scroll_y,omitempty"`
	CurrentFeedID string         `json:"current_feed_id,omitempty"`
	Opened        bool           `json:"opened"`
	Read          bool           `json:"read"`
	SeenNotes     map[string]bool `json:"seen_notes,omitempty"`
	ExpiresAt     time.Time      `json:"expires_at"`
}

// SessionOpenNoteResponse 在打开笔记后直接返回首屏标题和正文。
type SessionOpenNoteResponse struct {
	BrowseSessionInfo
	Note     OpenedNoteContent              `json:"note"`
	Comments []Comment                      `json:"comments"`
	Media    SessionMediaReadStatus         `json:"media"`
}

type BrowseSessionPageState struct {
	Session           BrowseSessionInfo        `json:"session"`
	Summary           string                   `json:"summary,omitempty"`
	Kind              XHSReadyKind             `json:"kind"`
	Ready             bool                     `json:"ready"`
	Risk              RiskSignal               `json:"risk"`
	Counts            BrowseSessionPageCounts  `json:"counts"`
	Current           BrowseSessionCurrent     `json:"current"`
	Results           []BrowseSessionResult    `json:"results,omitempty"`
	Actions           []BrowseSessionAction    `json:"actions,omitempty"`
	RecommendedAction *BrowseSessionAction     `json:"recommended_action,omitempty"`
	Timeline          []BrowseSessionEvent     `json:"timeline,omitempty"`
	StateFragment     string                   `json:"state_fragment,omitempty"`
	ResultsCount      int                      `json:"results_count"`
	SeenCount         int                      `json:"seen_count"`
	AvailableActions  []string                 `json:"available_actions,omitempty"`
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
	AvailableTools []string    `json:"available_tools,omitempty"`
}

type BrowseSessionResult struct {
	Ref    string `json:"ref"`
	FeedID string `json:"feed_id,omitempty"`
	Title  string `json:"title,omitempty"`
	Author string `json:"author,omitempty"`
	Seen   bool   `json:"seen"`
}

type BrowseSessionAction struct {
	Ref       string `json:"ref"`
	Tool      string `json:"tool"`
	Label     string `json:"label"`
	ResultRef string `json:"result_ref,omitempty"`
	FeedID    string `json:"feed_id,omitempty"`
	Requires  string `json:"requires,omitempty"`
	Confirm   bool   `json:"confirm,omitempty"`
}

type BrowseSessionEvent struct {
	Action string    `json:"action"`
	Target string    `json:"target,omitempty"`
	Status string    `json:"status"`
	At     time.Time `json:"at"`
	Note   string    `json:"note,omitempty"`
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

	touchOnFinish  bool

	currentURL       string
	sourceURL        string
	scrollY          int
	seenNotes        map[string]bool
	results          map[string]Feed
	currentFeedID    string
	currentXsecToken string
	opened           bool
	read             bool
	closed           bool
	expiresAt          time.Time
	timeline           []BrowseSessionEvent
	initialCommentIDs  []string
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
	return s.initialCommentIDs
}

func (s *BrowseSession) Renew() BrowseSessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed && !time.Now().After(s.expiresAt) {
		s.touchLocked()
	}
	return s.infoLocked()
}

func (s *BrowseSession) PageState(ctx context.Context) (*BrowseSessionPageState, error) {
	opCtx, err := s.beginLockedOperation(ctx, true)
	if err != nil {
		return nil, err
	}
	defer s.finishOperation()

	s.mu.Lock()
	page := s.page
	feedID := s.currentFeedID
	opened := s.opened
	s.mu.Unlock()
	if page == nil {
		return nil, fmt.Errorf("browse session 页面不存在: %s", s.id)
	}

	page = page.Context(opCtx)
	probe, err := probeXHSReady(page, feedID)
	if err != nil {
		return nil, err
	}
	risk := riskSignalFromReadyProbe(probe)

	kind := inferXHSReadyKindFromSessionState(probe.URL, opened, feedID)
	ready := isXHSReady(probe, kind, feedID, true)
	if ready {
		probeWatchdogSelectors(page, XHSReadyOptions{Kind: kind, FeedID: feedID})
	}

	s.mu.Lock()
	if probe.URL != "" {
		s.currentURL = probe.URL
	}
	s.scrollY = probe.ScrollY
	info := s.infoLocked()
	resultsCount := s.uniqueResultCountLocked()
	seenCount := len(s.seenNotes)
	availableActions := s.availableActionsLocked(resultsCount)
	results := s.semanticResultsLocked()
	actions := s.semanticActionsLocked(resultsCount)
	recommendedAction := s.recommendedActionLocked(ready, results)
	current := s.currentStateLocked(kind, resultsCount, availableActions)
	summary := browseSessionSummary(kind, ready, resultsCount, seenCount, current, recommendedAction)
	timeline := s.timelineLocked()
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
	}, nil
}

func (s *BrowseSession) Search(ctx context.Context, keyword string, filters ...FilterOption) ([]Feed, error) {
	opCtx, err := s.beginLockedOperation(ctx, true)
	if err != nil {
		return nil, err
	}
	defer s.finishOperation()

	action := NewSearchActionWithState(s.page.Context(opCtx), s.state)
	feeds, err := action.Search(opCtx, keyword, filters...)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.sourceURL = ""
	s.currentFeedID = ""
	s.currentXsecToken = ""
	s.opened = false
	s.read = false
	s.results = make(map[string]Feed, len(feeds)*2)
	for i, feed := range feeds {
		feed.Index = i
		s.results[strconv.Itoa(i)] = feed
		if feed.ID != "" {
			s.results[feed.ID] = feed
		}
	}
	s.recordTimelineLocked("search", keyword, "ok", time.Now(), fmt.Sprintf("results=%d", len(feeds)))
	s.mu.Unlock()
	s.probeWatchdogSelectorsForKind(opCtx, XHSReadySearch, "")
	return feeds, nil
}

func (s *BrowseSession) OpenNote(ctx context.Context, resultRef, xsecToken string) (*SessionOpenNoteResponse, error) {
	opCtx, err := s.beginLockedOperation(ctx, true)
	if err != nil {
		return nil, err
	}
	defer s.finishOperation()

	feed, ok := s.resolveResult(resultRef)
	if !ok {
		return nil, fmt.Errorf("未找到搜索结果引用: %s", resultRef)
	}
	if xsecToken != "" {
		feed.XsecToken = xsecToken
	}
	if err := validateFeedAccessArgs(feed.ID, feed.XsecToken); err != nil {
		return nil, fmt.Errorf("搜索结果参数无效: %w", err)
	}

	sourceURL, err := s.currentPageURL(opCtx)
	if err != nil {
		return nil, fmt.Errorf("读取当前页面 URL: %w", err)
	}
	opener := NewNoteOpenActionWithState(s.page.Context(opCtx), s.state)
	if err := opener.OpenFromCards(opCtx, feed.ID, feed.XsecToken, OpenSourceSearch); err != nil {
		return nil, fmt.Errorf("从卡片打开笔记失败，请重新搜索或滚动后重试: %w", err)
	}

	content, err := ExtractOpenedNoteContentFromDOM(s.page.Context(opCtx), feed.ID)
	if err != nil {
		return nil, err
	}

	comments, commentErr := ExtractCommentsFromDOM(s.page.Context(opCtx), feed.ID)
	if commentErr != nil {
		comments = nil
	}

	s.mu.Lock()
	s.sourceURL = sourceURL
	s.currentFeedID = feed.ID
	s.currentXsecToken = feed.XsecToken
	s.opened = true
	s.read = true
	s.seenNotes[feed.ID] = true
	// 储存首屏评论 ID 作为初始 cursor，让 session_detail 从后续评论开始加载
	s.initialCommentIDs = s.initialCommentIDs[:0]
	for i, c := range comments {
		if key := commentBatchKey(i, c); key != "" {
			s.initialCommentIDs = append(s.initialCommentIDs, key)
		}
		for j, sub := range c.SubComments {
			if key := commentBatchKey(j, sub); key != "" {
				s.initialCommentIDs = append(s.initialCommentIDs, key)
			}
		}
	}
	s.recordTimelineLocked("open_note", feed.ID, "ok", time.Now(), "opened and content read from search result "+resultRef)
	info := s.infoLocked()
	s.mu.Unlock()
	s.probeWatchdogSelectorsForKind(opCtx, XHSReadyDetail, feed.ID)
	return &SessionOpenNoteResponse{
		BrowseSessionInfo: info,
		Note:              *content,
		Comments:          comments,
		Media:             SessionMediaReadStatus{Implemented: false, Message: "图片和视频阅读功能尚未实现，后续由 session_detail 支持"},
	}, nil
}

func (s *BrowseSession) Detail(ctx context.Context, _ bool, _ int) (*SessionDetailResponse, error) {
	opCtx, err := s.beginLockedOperation(ctx, true)
	if err != nil {
		return nil, err
	}
	defer s.finishOperation()

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
	comments, err := ExtractCommentsFromDOM(page.Context(opCtx), feedID)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.recordTimelineLocked("detail", feedID, "ok", time.Now(), fmt.Sprintf("visible_comments=%d", len(comments)))
	s.mu.Unlock()
	s.probeWatchdogSelectorsForKind(opCtx, XHSReadyDetail, feedID)
	unimplemented := SessionMediaReadStatus{Implemented: false, Message: "暂未实现"}
	return &SessionDetailResponse{
		NoteID:   feedID,
		Comments: comments,
		Images:   unimplemented,
		Video:    unimplemented,
	}, nil
}

func (s *BrowseSession) DetailForFeed(ctx context.Context, expectedFeedID string, loadComments bool, config CommentLoadConfig) (*FeedDetailResponse, error) {
	return s.detail(ctx, expectedFeedID, loadComments, 0, config, true)
}

func (s *BrowseSession) DetailCommentsBatch(ctx context.Context, expectedFeedID string, cursor *CommentCursor, maxItems int, config CommentLoadConfig) (*FeedDetailResponse, *CommentCursor, bool, error) {
	opCtx, err := s.beginLockedOperation(ctx, true)
	if err != nil {
		return nil, nil, false, err
	}
	defer s.finishOperation()

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
	if err := WaitForXHSReady(page.Context(opCtx), XHSReadyOptions{Kind: XHSReadyDetail, FeedID: feedID}); err != nil {
		return nil, nil, false, err
	}

	commentPage := page.Context(opCtx).Timeout(commentLoadTimeout)
	comments, nextCursor, hasMore, err := LoadCommentsBatch(commentPage, config, cursor, maxItems)
	if err != nil {
		return nil, nil, false, err
	}
	if nextCursor != nil && nextCursor.FeedID == "" {
		nextCursor.FeedID = feedID
	}

	resp := &FeedDetailResponse{
		Comments: CommentList{
			List:    comments,
			HasMore: hasMore,
		},
	}
	if totalItems := knownCommentTotal(commentPage); totalItems > 0 {
		resp.Comments.TotalItems = totalItems
	}

	s.mu.Lock()
	s.recordTimelineLocked("detail_comments_batch", feedID, "ok", time.Now(), fmt.Sprintf("maxItems=%d hasMore=%v", maxItems, hasMore))
	s.mu.Unlock()
	s.probeWatchdogSelectorsForKind(opCtx, XHSReadyDetail, feedID)
	return resp, nextCursor, hasMore, nil
}

func (s *BrowseSession) detail(ctx context.Context, expectedFeedID string, loadComments bool, pages int, config CommentLoadConfig, useConfig bool) (*FeedDetailResponse, error) {
	opCtx, err := s.beginLockedOperation(ctx, true)
	if err != nil {
		return nil, err
	}
	defer s.finishOperation()

	feedID, err := s.currentOpenedFeedIDFor(expectedFeedID)
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
	if loadComments {
		commentPage := page.Context(opCtx).Timeout(commentLoadTimeout)
		ops := sessionCommentLoadOps{
			getProgress: func() (commentProgress, error) {
				return getCommentProgress(commentPage)
			},
			load: func(config CommentLoadConfig) error {
				return loadCommentsByJS(commentPage, config)
			},
		}
		var loadErr error
		if useConfig {
			loadErr = loadSessionCommentsForDetailWithConfig(config, ops)
		} else {
			loadErr = loadSessionCommentsForDetail(pages, ops)
		}
		if loadErr != nil {
			if errors.Is(loadErr, context.Canceled) || errors.Is(loadErr, context.DeadlineExceeded) {
				return nil, loadErr
			}
			logrus.Warnf("session detail load comments failed: %v", loadErr)
		}
	}
	detail, err := ExtractFeedDetailFromDOM(page.Context(opCtx), feedID)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.recordTimelineLocked("detail", feedID, "ok", time.Now(), "")
	s.mu.Unlock()
	s.probeWatchdogSelectorsForKind(opCtx, XHSReadyDetail, feedID)
	return detail, nil
}

type sessionCommentLoadOps struct {
	getProgress func() (commentProgress, error)
	load        func(CommentLoadConfig) error
}

func loadSessionCommentsForDetail(pages int, ops sessionCommentLoadOps) error {
	progress, err := ops.getProgress()
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
	}
	config := sessionCommentPageLoadConfig(progress, err)
	if pages > 0 {
		config.MaxCommentItems = progress.Count + pages*20
	} else if pages < 0 {
		config.MaxCommentItems = 0
	}
	if err := ops.load(config); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		logrus.Warnf("session detail load comments failed: %v", err)
	}
	return nil
}

func loadSessionCommentsForDetailWithConfig(config CommentLoadConfig, ops sessionCommentLoadOps) error {
	config = normalizeCommentLoadConfig(config)
	if err := ops.load(config); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		logrus.Warnf("session detail load comments failed: %v", err)
	}
	return nil
}

func shouldStopSessionCommentPaging(progress commentProgress) bool {
	if progress.NoComments {
		return true
	}
	if progress.AtEnd {
		return true
	}
	return progress.Total > 0 && progress.Count >= progress.Total
}

func (s *BrowseSession) Like(ctx context.Context, unlike bool) error {
	return s.like(ctx, "", unlike)
}

func (s *BrowseSession) LikeForFeed(ctx context.Context, expectedFeedID string, unlike bool) error {
	return s.like(ctx, expectedFeedID, unlike)
}

func (s *BrowseSession) like(ctx context.Context, expectedFeedID string, unlike bool) error {
	opCtx, err := s.beginLockedOperation(ctx, true)
	if err != nil {
		return err
	}
	defer s.finishOperation()

	feedID, xsecToken, err := s.currentFeedFor(expectedFeedID)
	if err != nil {
		return err
	}
	action := NewLikeActionWithState(s.page.Context(opCtx), s.state)
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

func (s *BrowseSession) favorite(ctx context.Context, expectedFeedID string, unfavorite bool) error {
	opCtx, err := s.beginLockedOperation(ctx, true)
	if err != nil {
		return err
	}
	defer s.finishOperation()

	feedID, xsecToken, err := s.currentFeedFor(expectedFeedID)
	if err != nil {
		return err
	}
	action := NewFavoriteActionWithState(s.page.Context(opCtx), s.state)
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

func (s *BrowseSession) comment(ctx context.Context, expectedFeedID, content string) error {
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("评论内容不能为空")
	}
	opCtx, err := s.beginLockedOperation(ctx, true)
	if err != nil {
		return err
	}
	defer s.finishOperation()

	feedID, xsecToken, err := s.currentFeedFor(expectedFeedID)
	if err != nil {
		return err
	}
	action := NewCommentFeedActionWithState(s.page.Context(opCtx), s.state)
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

func (s *BrowseSession) reply(ctx context.Context, expectedFeedID, commentID, userID, content string) error {
	opCtx, err := s.beginLockedOperation(ctx, true)
	if err != nil {
		return err
	}
	defer s.finishOperation()

	feedID, xsecToken, err := s.currentFeedFor(expectedFeedID)
	if err != nil {
		return err
	}
	action := NewCommentFeedActionWithState(s.page.Context(opCtx), s.state)
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

func (s *BrowseSession) Back(ctx context.Context) error {
	opCtx, err := s.beginLockedOperation(ctx, true)
	if err != nil {
		return err
	}
	defer s.finishOperation()

	s.mu.Lock()
	page := s.page
	feedID := s.currentFeedID
	s.mu.Unlock()

	if page == nil {
		return fmt.Errorf("browse session 页面不存在: %s", s.id)
	}

	// 通用后退：从任意页面返回上一步
	if _, err := page.Eval(`() => window.history.back()`); err != nil {
		return fmt.Errorf("history.back 失败: %w", err)
	}
	page.Sleep(1000 * time.Millisecond) // 等待 SPA 渲染

	s.refreshPageState(opCtx)
	s.mu.Lock()
	s.currentFeedID = ""
	s.currentXsecToken = ""
	s.opened = false
	s.read = false
	s.recordTimelineLocked("back", feedID, "ok", time.Now(), "history.back()")
	s.mu.Unlock()
	return nil
}

func (s *BrowseSession) CloseNote(ctx context.Context) error {
	opCtx, err := s.beginLockedOperation(ctx, true)
	if err != nil {
		return err
	}
	defer s.finishOperation()

	s.mu.Lock()
	page := s.page
	sourceURL := s.sourceURL
	feedID := s.currentFeedID
	s.mu.Unlock()

	if page == nil {
		return fmt.Errorf("browse session 页面不存在: %s", s.id)
	}

	method, err := closeNoteOverlay(page.Context(opCtx), sourceURL)
	if err != nil {
		return fmt.Errorf("关闭笔记面板失败: %w", err)
	}
	logrus.Debugf("关闭笔记面板方式: %s", method)

	s.refreshPageState(opCtx)
	s.mu.Lock()
	s.currentFeedID = ""
	s.currentXsecToken = ""
	s.opened = false
	s.read = false
	s.recordTimelineLocked("close_note", feedID, "ok", time.Now(), method)
	s.mu.Unlock()
	return nil
}

func (s *BrowseSession) Close() {
	s.close()
}

func (s *BrowseSession) ClassifyRisk() (RiskSignal, error) {
	return s.ClassifyRiskContext(context.Background())
}

func (s *BrowseSession) ClassifyRiskContext(ctx context.Context) (RiskSignal, error) {
	opCtx, err := s.beginLockedOperation(ctx, false)
	if err != nil {
		return RiskSignal{Kind: RiskNone, DetectedAt: time.Now()}, err
	}
	defer s.finishOperation()

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
	hasActiveOp := s.activeOp != nil
	page := s.page
	onClose := s.onClose
	if !hasActiveOp {
		s.page = nil
		s.onClose = nil
	}
	onRemove := s.onRemove
	s.mu.Unlock()

	if onRemove != nil {
		onRemove(s)
	}
	if cancel != nil {
		cancel()
	}
	if !hasActiveOp && onClose != nil {
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

func (s *BrowseSession) finishOperation() {
	s.endOperation()
	s.releaseOperation()
}

func (s *BrowseSession) endOperation() {
	s.mu.Lock()
	closed := s.closed
	opCtx := s.opCtx
	expired := !closed && time.Now().After(s.expiresAt)
	s.mu.Unlock()

	if expired {
		s.close()
		return
	}

	if !closed && opCtx != nil && opCtx.Err() == nil {
		s.refreshPageState(opCtx)
	}

	s.mu.Lock()
	if !s.closed && s.touchOnFinish && opCtx != nil && opCtx.Err() == nil {
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
	shouldRelease := s.closed
	page := s.page
	onClose := s.onClose
	if cancel != nil {
		cancel()
	}
	s.opCtx = nil
	s.activeOp = nil
	if shouldRelease {
		s.page = nil
		s.onClose = nil
	}
	s.mu.Unlock()

	s.opToken <- struct{}{}

	if shouldRelease && onClose != nil {
		onClose(page)
	}
}

func (s *BrowseSession) resolveResult(resultRef string) (Feed, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if resultRef == "" {
		return Feed{}, false
	}
	feed, ok := s.results[resultRef]
	return feed, ok
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

func (s *BrowseSession) currentFeed() (string, string) {
	feedID, xsecToken, _ := s.currentFeedFor("")
	return feedID, xsecToken
}

func (s *BrowseSession) currentFeedFor(expectedFeedID string) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.opened || s.currentFeedID == "" {
		return "", "", fmt.Errorf("互动只能对已打开的笔记执行")
	}
	if expectedFeedID != "" && s.currentFeedID != expectedFeedID {
		return "", "", fmt.Errorf("session 当前笔记 %s 与目标笔记 %s 不一致", s.currentFeedID, expectedFeedID)
	}
	return s.currentFeedID, s.currentXsecToken, nil
}

func (s *BrowseSession) ensureReadableInteraction() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.opened || s.currentFeedID == "" {
		return fmt.Errorf("互动只能对已打开的笔记执行")
	}
	if !s.read {
		return fmt.Errorf("互动只能对已阅读的笔记执行")
	}
	return nil
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

	var currentURL string
	var scrollY int
	var hasURL, hasScrollY bool
	if s.evalJS != nil {
		if url, err := s.evalJS(evalCtx, page, `() => location.href`); err == nil && url != nil {
			currentURL = url.Value.Str()
			hasURL = true
		}
		if y, err := s.evalJS(evalCtx, page, `() => Math.round(window.scrollY || document.scrollingElement?.scrollTop || 0)`); err == nil && y != nil {
			scrollY = y.Value.Int()
			hasScrollY = true
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.page != page {
		return
	}
	if hasURL {
		s.currentURL = currentURL
	}
	if hasScrollY {
		s.scrollY = scrollY
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

func (s *BrowseSession) currentPageURL(ctx context.Context) (string, error) {
	if s.page == nil {
		return "", nil
	}
	evalCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	result, err := s.page.Context(evalCtx).Eval(`() => location.href`)
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
		CurrentURL:    s.currentURL,
		SourceURL:     s.sourceURL,
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
		URL:            s.currentURL,
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
	case s.opened:
		return "笔记已打开：首屏标题/正文/首页评论/作者/互动数据/图片列表已在 session_open_note 返回。可继续操作：session_detail(分批加载更多评论)、session_like、session_comment、user_profile(看作者主页)。图片和视频浏览功能尚未实现"
	case resultsCount > 0:
		return "可继续：搜索新关键词 (session_search)、打开其他笔记 (session_open_note)、或滚动浏览 feed"
	case !s.opened && resultsCount == 0:
		return "可搜索关键词 (session_search) 查找笔记"
	default:
		return "可搜索关键词 (session_search) 查找笔记"
	}
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
	actions := []string{"session_state", "session_search", "close_browse_session"}
	if resultsCount > 0 && !s.opened {
		actions = append(actions, "session_open_note")
	}
	if s.opened {
		actions = append(actions, "session_detail", "session_like", "session_comment", "session_close_note")
	}
	return actions
}

func (s *BrowseSession) semanticActionsLocked(resultsCount int) []BrowseSessionAction {
	actions := []BrowseSessionAction{
		{Ref: "session_state", Tool: "session_state", Label: "查看当前 session 状态"},
		{Ref: "session_search", Tool: "session_search", Label: "搜索笔记"},
	}
	if resultsCount > 0 && !s.opened {
		for index := 0; index < resultsCount; index++ {
			ref := strconv.Itoa(index)
			feed, ok := s.results[ref]
			if !ok {
				continue
			}
			actions = append(actions, BrowseSessionAction{
				Ref:       "open_note:" + ref,
				Tool:      "session_open_note",
				Label:     "打开搜索结果 " + ref,
				ResultRef: ref,
				FeedID:    feed.ID,
			})
		}
	}
	if s.opened {
		actions = append(actions,
			BrowseSessionAction{
				Ref:      "detail_current",
				Tool:     "session_detail",
				Label:    "继续读取当前笔记媒体或评论",
				FeedID:   s.currentFeedID,
				Requires: "opened",
			},
			BrowseSessionAction{
				Ref:      "like_current",
				Tool:     "session_like",
				Label:    "点赞当前笔记",
				FeedID:   s.currentFeedID,
				Requires: "opened",
				Confirm:  true,
			},
			BrowseSessionAction{
				Ref:      "comment_current",
				Tool:     "session_comment",
				Label:    "评论当前笔记",
				FeedID:   s.currentFeedID,
				Requires: "opened",
				Confirm:  true,
			},
		)
	}
	if s.opened {
		actions = append(actions, BrowseSessionAction{
			Ref:    "back_to_results",
			Tool:   "session_close_note",
			Label:  "关闭当前笔记并返回来源页",
			FeedID: s.currentFeedID,
		})
	}
	actions = append(actions, BrowseSessionAction{Ref: "close_session", Tool: "close_browse_session", Label: "关闭当前 session"})
	return actions
}

func (s *BrowseSession) recommendedActionLocked(ready bool, results []BrowseSessionResult) *BrowseSessionAction {
	if !ready {
		return &BrowseSessionAction{
			Ref:   "refresh_state",
			Tool:  "session_state",
			Label: "重新读取 session 状态",
		}
	}
	if s.opened {
		return &BrowseSessionAction{
			Ref:    "back_to_results",
			Tool:   "session_close_note",
			Label:  "关闭当前笔记并返回来源页",
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
				Tool:      "session_open_note",
				Label:     "打开下一张未读笔记",
				ResultRef: result.Ref,
				FeedID:    result.FeedID,
			}
		}
		if len(results) > 0 {
			result := results[0]
			return &BrowseSessionAction{
				Ref:       "open_note:" + result.Ref,
				Tool:      "session_open_note",
				Label:     "打开搜索结果 " + result.Ref,
				ResultRef: result.Ref,
				FeedID:    result.FeedID,
			}
		}
	}
	return &BrowseSessionAction{
		Ref:   "session_search",
		Tool:  "session_search",
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

func inferXHSReadyKindFromSessionURL(rawURL string) XHSReadyKind {
	if isDetailURL(rawURL) {
		return XHSReadyDetail
	}
	return inferXHSReadyKindFromURL(rawURL)
}

func inferXHSReadyKindFromSessionState(rawURL string, opened bool, feedID string) XHSReadyKind {
	if opened && feedID != "" {
		return XHSReadyDetail
	}
	return inferXHSReadyKindFromSessionURL(rawURL)
}

func newBrowseSessionID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err == nil {
		return hex.EncodeToString(buf[:])
	}
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

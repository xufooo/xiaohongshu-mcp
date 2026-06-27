package xiaohongshu

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

const DefaultBrowseSessionTimeout = 10 * time.Minute

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

type BrowseSessionPageState struct {
	Session          BrowseSessionInfo       `json:"session"`
	Kind             XHSReadyKind            `json:"kind"`
	Ready            bool                    `json:"ready"`
	Risk             RiskSignal              `json:"risk"`
	Counts           BrowseSessionPageCounts `json:"counts"`
	StateFragment    string                  `json:"state_fragment,omitempty"`
	ResultsCount     int                     `json:"results_count"`
	SeenCount        int                     `json:"seen_count"`
	AvailableActions []string                `json:"available_actions,omitempty"`
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
	mu      sync.Mutex
	opMu    sync.Mutex
	id      string
	page    *hrod.Page
	state   *ActionStateStore
	timeout time.Duration
	timer   *time.Timer
	onClose func(*hrod.Page)
	onRemove func(*BrowseSession)

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
	expiresAt        time.Time
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
	session := &BrowseSession{
		id:        newBrowseSessionID(),
		page:      page,
		state:     state,
		timeout:   m.timeout,
		onClose:   onClose,
		onRemove:  m.remove,
		seenNotes: make(map[string]bool),
		results:   make(map[string]Feed),
	}
	session.touchLocked()
	session.refreshPageState()

	m.mu.Lock()
	m.sessions[session.id] = session
	m.mu.Unlock()
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
	sessions := make([]*BrowseSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.mu.Unlock()

	for _, session := range sessions {
		if session.isExpired() {
			_ = m.Close(session.ID())
			continue
		}
		return session.Info(), true
	}
	return BrowseSessionInfo{}, false
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

func (s *BrowseSession) PageState(ctx context.Context) (*BrowseSessionPageState, error) {
	if err := s.beginLockedOperation(); err != nil {
		return nil, err
	}
	defer s.finishPageStateOperation()

	s.mu.Lock()
	page := s.page
	feedID := s.currentFeedID
	s.mu.Unlock()
	if page == nil {
		return nil, fmt.Errorf("browse session 页面不存在: %s", s.id)
	}

	page = page.Context(ctx)
	probe, err := probeXHSReady(page, feedID)
	if err != nil {
		return nil, err
	}
	risk := riskSignalFromReadyProbe(probe)

	kind := inferXHSReadyKindFromSessionURL(probe.URL)
	ready := isXHSReady(probe, kind, feedID, true)

	s.mu.Lock()
	if probe.URL != "" {
		s.currentURL = probe.URL
	}
	s.scrollY = probe.ScrollY
	info := s.infoLocked()
	resultsCount := s.uniqueResultCountLocked()
	seenCount := len(s.seenNotes)
	availableActions := s.availableActionsLocked(resultsCount)
	s.mu.Unlock()

	return &BrowseSessionPageState{
		Session: info,
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
		StateFragment:    probe.StateFragment,
		ResultsCount:     resultsCount,
		SeenCount:        seenCount,
		AvailableActions: availableActions,
	}, nil
}

func (s *BrowseSession) Search(ctx context.Context, keyword string, filters ...FilterOption) ([]Feed, error) {
	if err := s.beginLockedOperation(); err != nil {
		return nil, err
	}
	defer s.finishOperation()

	action := NewSearchActionWithState(s.page.Context(ctx), s.state)
	feeds, err := action.Search(ctx, keyword, filters...)
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
	s.mu.Unlock()
	return feeds, nil
}

func (s *BrowseSession) OpenNote(ctx context.Context, resultRef, xsecToken string) error {
	if err := s.beginLockedOperation(); err != nil {
		return err
	}
	defer s.finishOperation()

	feed, ok := s.resolveResult(resultRef)
	if !ok {
		return fmt.Errorf("未找到搜索结果引用: %s", resultRef)
	}
	if xsecToken != "" {
		feed.XsecToken = xsecToken
	}
	if feed.ID == "" {
		return fmt.Errorf("搜索结果缺少 feed_id")
	}

	sourceURL, err := s.currentPageURL()
	if err != nil {
		return fmt.Errorf("读取当前页面 URL: %w", err)
	}
	opener := NewNoteOpenActionWithState(s.page.Context(ctx), s.state)
	if err := opener.OpenFromCards(ctx, feed.ID, feed.XsecToken, OpenSourceSearch); err != nil {
		return err
	}

	s.mu.Lock()
	s.sourceURL = sourceURL
	s.currentFeedID = feed.ID
	s.currentXsecToken = feed.XsecToken
	s.opened = true
	s.read = false
	s.seenNotes[feed.ID] = true
	s.mu.Unlock()
	return nil
}

func (s *BrowseSession) Read(ctx context.Context, minDuration time.Duration) error {
	if err := s.beginLockedOperation(); err != nil {
		return err
	}
	defer s.finishOperation()

	feedID, err := s.currentOpenedFeedID()
	if err != nil {
		return err
	}
	if minDuration <= 0 {
		minDuration = 20 * time.Second
	}
	reader := NewReadStageAction(s.page.Context(ctx), s.state)
	if err := reader.Read(ctx, feedID, minDuration); err != nil {
		return err
	}

	s.mu.Lock()
	s.read = true
	s.seenNotes[feedID] = true
	s.mu.Unlock()
	return nil
}

func (s *BrowseSession) Like(ctx context.Context, unlike bool) error {
	if err := s.beginLockedOperation(); err != nil {
		return err
	}
	defer s.finishOperation()

	if err := s.ensureReadableInteraction(); err != nil {
		return err
	}
	feedID, xsecToken := s.currentFeed()
	action := NewLikeActionWithState(s.page.Context(ctx), s.state)
	if unlike {
		return action.Unlike(ctx, feedID, xsecToken)
	}
	return action.Like(ctx, feedID, xsecToken)
}

func (s *BrowseSession) Comment(ctx context.Context, content string) error {
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("评论内容不能为空")
	}
	if err := s.beginLockedOperation(); err != nil {
		return err
	}
	defer s.finishOperation()

	if err := s.ensureReadableInteraction(); err != nil {
		return err
	}
	feedID, xsecToken := s.currentFeed()
	action := NewCommentFeedActionWithState(s.page.Context(ctx), s.state)
	return action.PostComment(ctx, feedID, xsecToken, content)
}

func (s *BrowseSession) Back(ctx context.Context) error {
	if err := s.beginLockedOperation(); err != nil {
		return err
	}
	defer s.finishOperation()

	s.mu.Lock()
	sourceURL := s.sourceURL
	s.mu.Unlock()
	if sourceURL == "" {
		return fmt.Errorf("当前 session 没有来源 URL")
	}
	if err := s.page.Context(ctx).Navigate(sourceURL); err != nil {
		return err
	}
	if err := WaitForXHSReady(s.page.Context(ctx), XHSReadyOptions{Kind: inferXHSReadyKindFromURL(sourceURL)}); err != nil {
		return err
	}

	s.mu.Lock()
	s.currentFeedID = ""
	s.currentXsecToken = ""
	s.opened = false
	s.read = false
	s.mu.Unlock()
	return nil
}

func (s *BrowseSession) Close() {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.close()
}

func (s *BrowseSession) ClassifyRisk() (RiskSignal, error) {
	return s.ClassifyRiskContext(context.Background())
}

func (s *BrowseSession) ClassifyRiskContext(ctx context.Context) (RiskSignal, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.mu.Lock()
	closed := s.closed
	page := s.page
	s.mu.Unlock()
	if closed || page == nil {
		return RiskSignal{Kind: RiskNone, DetectedAt: time.Now()}, nil
	}
	return ClassifyRisk(page.Context(ctx))
}

func (s *BrowseSession) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	if s.timer != nil {
		s.timer.Stop()
	}
	page := s.page
	onClose := s.onClose
	onRemove := s.onRemove
	s.mu.Unlock()

	if onRemove != nil {
		onRemove(s)
	}
	if onClose != nil {
		onClose(page)
	}
}

func (s *BrowseSession) beginLockedOperation() error {
	s.opMu.Lock()
	if err := s.beginOperation(); err != nil {
		s.opMu.Unlock()
		return err
	}
	return nil
}

func (s *BrowseSession) finishOperation() {
	s.endOperation()
	s.opMu.Unlock()
}

func (s *BrowseSession) finishPageStateOperation() {
	s.mu.Lock()
	if !s.closed {
		s.touchLocked()
	}
	s.mu.Unlock()
	s.opMu.Unlock()
}

func (s *BrowseSession) beginOperation() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("browse session 已关闭: %s", s.id)
	}
	if time.Now().After(s.expiresAt) {
		return fmt.Errorf("browse session 已过期: %s", s.id)
	}
	s.touchLocked()
	return nil
}

func (s *BrowseSession) endOperation() {
	s.refreshPageState()

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.touchLocked()
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.opened || s.currentFeedID == "" {
		return "", fmt.Errorf("互动或阅读前必须先打开笔记")
	}
	return s.currentFeedID, nil
}

func (s *BrowseSession) currentFeed() (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentFeedID, s.currentXsecToken
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
	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.mu.Lock()
	expired := !s.closed && s.expiresAt.Equal(expiresAt) && !time.Now().Before(s.expiresAt)
	s.mu.Unlock()
	if !expired {
		return
	}
	s.close()
}

func (s *BrowseSession) refreshPageState() {
	s.mu.Lock()
	page := s.page
	closed := s.closed
	s.mu.Unlock()
	if closed || page == nil {
		return
	}

	var currentURL string
	var scrollY int
	var hasURL, hasScrollY bool
	if url, err := page.Eval(`() => location.href`); err == nil && url != nil {
		currentURL = url.Value.Str()
		hasURL = true
	}
	if y, err := page.Eval(`() => Math.round(window.scrollY || document.scrollingElement?.scrollTop || 0)`); err == nil && y != nil {
		scrollY = y.Value.Int()
		hasScrollY = true
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

func (s *BrowseSession) currentPageURL() (string, error) {
	if s.page == nil {
		return "", nil
	}
	result, err := s.page.Eval(`() => location.href`)
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

func (s *BrowseSession) availableActionsLocked(resultsCount int) []string {
	actions := []string{"session_state", "session_search", "close_browse_session"}
	if resultsCount > 0 && !s.opened {
		actions = append(actions, "session_open_note")
	}
	if s.opened && !s.read {
		actions = append(actions, "session_read")
	}
	if s.opened && s.read {
		actions = append(actions, "session_like", "session_comment")
	}
	if s.opened && s.sourceURL != "" {
		actions = append(actions, "session_back")
	}
	return actions
}

func inferXHSReadyKindFromSessionURL(rawURL string) XHSReadyKind {
	if isDetailURL(rawURL) {
		return XHSReadyDetail
	}
	return inferXHSReadyKindFromURL(rawURL)
}

func newBrowseSessionID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err == nil {
		return hex.EncodeToString(buf[:])
	}
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

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

type BrowseSession struct {
	mu      sync.Mutex
	id      string
	page    *hrod.Page
	state   *ActionStateStore
	timeout time.Duration
	timer   *time.Timer
	onClose func(*hrod.Page)

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
		seenNotes: make(map[string]bool),
		results:   make(map[string]Feed),
	}
	session.touchLocked()
	session.refreshPageStateLocked()

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

func (s *BrowseSession) Search(ctx context.Context, keyword string, filters ...FilterOption) ([]Feed, error) {
	if err := s.beginOperation(); err != nil {
		return nil, err
	}
	defer s.endOperation()

	action := NewSearchActionWithState(s.page.Context(ctx), s.state)
	feeds, err := action.Search(ctx, keyword, filters...)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
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
	s.refreshPageStateLocked()
	return feeds, nil
}

func (s *BrowseSession) OpenNote(ctx context.Context, resultRef, xsecToken string) error {
	if err := s.beginOperation(); err != nil {
		return err
	}
	defer s.endOperation()

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

	sourceURL := s.page.MustEval(`() => location.href`).String()
	opener := NewNoteOpenActionWithState(s.page.Context(ctx), s.state)
	if err := opener.OpenFromCards(ctx, feed.ID, feed.XsecToken, OpenSourceSearch); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sourceURL = sourceURL
	s.currentFeedID = feed.ID
	s.currentXsecToken = feed.XsecToken
	s.opened = true
	s.read = false
	s.seenNotes[feed.ID] = true
	s.refreshPageStateLocked()
	return nil
}

func (s *BrowseSession) Read(ctx context.Context, minDuration time.Duration) error {
	if err := s.beginOperation(); err != nil {
		return err
	}
	defer s.endOperation()

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
	defer s.mu.Unlock()
	s.read = true
	s.seenNotes[feedID] = true
	s.refreshPageStateLocked()
	return nil
}

func (s *BrowseSession) Like(ctx context.Context, unlike bool) error {
	if err := s.ensureReadableInteraction(); err != nil {
		return err
	}
	if err := s.beginOperation(); err != nil {
		return err
	}
	defer s.endOperation()

	feedID, xsecToken := s.currentFeed()
	action := NewLikeActionWithState(s.page.Context(ctx), s.state)
	if unlike {
		return action.Unlike(ctx, feedID, xsecToken)
	}
	return action.Like(ctx, feedID, xsecToken)
}

func (s *BrowseSession) Comment(ctx context.Context, content string) error {
	if err := s.ensureReadableInteraction(); err != nil {
		return err
	}
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("评论内容不能为空")
	}
	if err := s.beginOperation(); err != nil {
		return err
	}
	defer s.endOperation()

	feedID, xsecToken := s.currentFeed()
	action := NewCommentFeedActionWithState(s.page.Context(ctx), s.state)
	return action.PostComment(ctx, feedID, xsecToken, content)
}

func (s *BrowseSession) Back(ctx context.Context) error {
	if err := s.beginOperation(); err != nil {
		return err
	}
	defer s.endOperation()

	sourceURL := s.sourceURL
	if sourceURL == "" {
		return fmt.Errorf("当前 session 没有来源 URL")
	}
	if err := s.page.Context(ctx).Navigate(sourceURL); err != nil {
		return err
	}
	if err := s.page.Context(ctx).WaitLoad(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentFeedID = ""
	s.currentXsecToken = ""
	s.opened = false
	s.read = false
	s.refreshPageStateLocked()
	return nil
}

func (s *BrowseSession) Close() {
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
	s.mu.Unlock()

	if onClose != nil {
		onClose(page)
	}
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.refreshPageStateLocked()
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
	s.timer = time.AfterFunc(s.timeout, s.Close)
}

func (s *BrowseSession) refreshPageStateLocked() {
	if s.page == nil {
		return
	}
	if url, err := s.page.Eval(`() => location.href`); err == nil && url != nil {
		s.currentURL = url.Value.Str()
	}
	if y, err := s.page.Eval(`() => Math.round(window.scrollY || document.scrollingElement?.scrollTop || 0)`); err == nil && y != nil {
		s.scrollY = y.Value.Int()
	}
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

func newBrowseSessionID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err == nil {
		return hex.EncodeToString(buf[:])
	}
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

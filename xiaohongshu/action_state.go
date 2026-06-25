package xiaohongshu

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	OpenSourceHome              = "home"
	OpenSourceSearch            = "search"
	OpenSourceRecommend         = "recommend"
	OpenSourceDetailURLFallback = "detail_url_fallback"
)

type ActionState struct {
	LastAction         string        `json:"last_action,omitempty"`
	LastActionAt       time.Time     `json:"last_action_at,omitempty"`
	LastOpenedFeedID   string        `json:"last_opened_feed_id,omitempty"`
	LastOpenSource     string        `json:"last_open_source,omitempty"`
	LastOpenAt         time.Time     `json:"last_open_at,omitempty"`
	LastReadAt         time.Time     `json:"last_read_at,omitempty"`
	ReadDuration       time.Duration `json:"read_duration,omitempty"`
	FeedScrollCount    int           `json:"feed_scroll_count,omitempty"`
	CommentDwellTime   time.Duration `json:"comment_dwell_time,omitempty"`
	CommentScrollCount int           `json:"comment_scroll_count,omitempty"`
	InteractionsOnFeed int           `json:"interactions_on_feed,omitempty"`
	SessionActions     int           `json:"session_actions,omitempty"`

	ConsecutiveFailures int       `json:"consecutive_failures,omitempty"`
	RiskCooldownUntil   time.Time `json:"risk_cooldown_until,omitempty"`
	LastRiskText        string    `json:"last_risk_text,omitempty"`
}

type ActionStateStore struct {
	mu   sync.Mutex
	path string
}

func NewActionStateStore(root string, accountKey string) (*ActionStateStore, error) {
	if root == "" {
		root = defaultActionStateRoot()
	}
	if accountKey == "" {
		accountKey = "default"
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(accountKey))
	return &ActionStateStore{
		path: filepath.Join(root, hex.EncodeToString(sum[:])+".json"),
	}, nil
}

func DefaultActionStateStore(accountParts ...string) *ActionStateStore {
	key := strings.Join(accountParts, "|")
	store, err := NewActionStateStore("", key)
	if err != nil {
		return &ActionStateStore{}
	}
	return store
}

func defaultActionStateRoot() string {
	if v := strings.TrimSpace(os.Getenv("XHS_ACTION_STATE_STORE")); v != "" {
		return v
	}
	if dir, err := os.UserCacheDir(); err == nil && dir != "" {
		return filepath.Join(dir, "xiaohongshu-mcp", "action_state")
	}
	return filepath.Join(os.TempDir(), "xiaohongshu-mcp-action-state")
}

func (s *ActionStateStore) Load() (ActionState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *ActionStateStore) Save(state ActionState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(state)
}

func (s *ActionStateStore) RecordOpen(feedID, source string) error {
	return s.update(func(state *ActionState) {
		now := time.Now()
		state.ReadDuration = 0
		state.FeedScrollCount = 0
		state.CommentDwellTime = 0
		state.CommentScrollCount = 0
		state.InteractionsOnFeed = 0
		state.LastAction = "open_note"
		state.LastActionAt = now
		state.LastOpenedFeedID = feedID
		state.LastOpenSource = source
		state.LastOpenAt = now
		state.SessionActions++
	})
}

func (s *ActionStateStore) RecordRead(feedID string, duration time.Duration) error {
	return s.update(func(state *ActionState) {
		now := time.Now()
		if state.LastOpenedFeedID == feedID {
			state.ReadDuration += duration
			state.LastReadAt = now
		}
	})
}

func (s *ActionStateStore) RecordFeedScroll(feedID string, count int) error {
	if count <= 0 {
		count = 1
	}
	return s.update(func(state *ActionState) {
		if state.LastOpenedFeedID == feedID {
			state.FeedScrollCount += count
			state.LastReadAt = time.Now()
		}
	})
}

func (s *ActionStateStore) RecordCommentDwell(feedID string, duration time.Duration, scrolled bool) error {
	return s.update(func(state *ActionState) {
		if state.LastOpenedFeedID == feedID {
			state.CommentDwellTime += duration
			if scrolled {
				state.CommentScrollCount++
			}
		}
	})
}

func (s *ActionStateStore) RecordInteraction(feedID, action string) error {
	return s.update(func(state *ActionState) {
		now := time.Now()
		if state.LastOpenedFeedID == feedID {
			state.InteractionsOnFeed++
		}
		state.LastAction = action
		state.LastActionAt = now
		state.SessionActions++
		state.ConsecutiveFailures = 0
	})
}

func (s *ActionStateStore) RecordFailure(reason string) error {
	return s.update(func(state *ActionState) {
		state.ConsecutiveFailures++
		state.LastRiskText = reason
		if state.ConsecutiveFailures >= 3 {
			state.RiskCooldownUntil = time.Now().Add(60 * time.Minute)
		}
	})
}

func (s *ActionStateStore) RecordRisk(reason string, cooldown time.Duration) error {
	if cooldown <= 0 {
		cooldown = 6 * time.Hour
	}
	return s.update(func(state *ActionState) {
		state.ConsecutiveFailures++
		state.LastRiskText = reason
		state.RiskCooldownUntil = time.Now().Add(cooldown)
	})
}

func (s *ActionStateStore) ValidateInteraction(feedID, action string) error {
	state, err := s.Load()
	if err != nil {
		return err
	}
	now := time.Now()
	if state.RiskCooldownUntil.After(now) {
		return fmt.Errorf("账号处于风控冷却中，冷却至 %s：%s", state.RiskCooldownUntil.Format(time.RFC3339), state.LastRiskText)
	}
	if state.LastOpenedFeedID == "" || state.LastOpenAt.IsZero() {
		return fmt.Errorf("互动前必须先从列表或搜索结果打开笔记")
	}
	if state.LastOpenedFeedID != feedID {
		return fmt.Errorf("互动目标 %s 与最近打开笔记 %s 不一致", feedID, state.LastOpenedFeedID)
	}
	if now.Sub(state.LastOpenAt) > 30*time.Minute {
		return fmt.Errorf("最近打开笔记已超过 30 分钟，需要重新打开并阅读")
	}
	if state.LastOpenSource == OpenSourceDetailURLFallback && (state.ReadDuration < 45*time.Second || state.FeedScrollCount < 1) {
		return fmt.Errorf("直接 URL 兜底打开的笔记需要补充阅读和滚动后才能互动")
	}
	if state.InteractionsOnFeed > 0 && state.LastActionAt.After(state.LastReadAt) {
		return fmt.Errorf("同一篇笔记连续互动前必须再次阅读或滚动")
	}

	switch action {
	case "like", "favorite":
		if state.ReadDuration < 20*time.Second {
			return fmt.Errorf("点赞或收藏前阅读时长至少需要 20 秒")
		}
	case "comment":
		if state.ReadDuration < 45*time.Second || state.FeedScrollCount < 1 {
			return fmt.Errorf("评论前阅读至少 45 秒且需要正文或图片区域滚动")
		}
	case "reply":
		if state.CommentDwellTime < 60*time.Second || state.CommentScrollCount < 1 {
			return fmt.Errorf("回复前评论区停留至少 60 秒且需要滚动或定位目标评论")
		}
	}
	return nil
}

func (s *ActionStateStore) update(fn func(*ActionState)) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.loadLocked()
	if err != nil {
		return err
	}
	fn(&state)
	return s.saveLocked(state)
}

func (s *ActionStateStore) loadLocked() (ActionState, error) {
	var state ActionState
	if s.path == "" {
		return state, nil
	}
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	return state, nil
}

func (s *ActionStateStore) saveLocked(state ActionState) error {
	if s.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

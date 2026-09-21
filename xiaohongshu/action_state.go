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

	"github.com/sirupsen/logrus"
)

const (
	OpenSourceHome      = "home"
	OpenSourceSearch    = "search"
	OpenSourceRecommend = "recommend"
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

	Identity *IdentityMetadata `json:"identity,omitempty"`
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
	resolved, err := ensureWritableStateDir(root)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(accountKey))
	return &ActionStateStore{
		path: filepath.Join(resolved, hex.EncodeToString(sum[:])+".json"),
	}, nil
}

// ensureWritableStateDir 解析可写的状态目录。首选目录不可用时回退到临时目录：
// 树莓派上常见只读 rootfs / 受限 HOME（systemd ProtectHome），
// 不该因为一个缓存目录就让整个服务起不来（与 ratelimit 的降级策略一致）。
func ensureWritableStateDir(root string) (string, error) {
	err := os.MkdirAll(root, 0755)
	if err == nil && dirWritable(root) {
		return root, nil
	}

	fallback := filepath.Join(os.TempDir(), "xiaohongshu-mcp-action-state")
	if fallback == root {
		return "", fmt.Errorf("状态目录不可写: %s: %w", root, err)
	}
	if fallbackErr := os.MkdirAll(fallback, 0755); fallbackErr != nil || !dirWritable(fallback) {
		return "", fmt.Errorf("状态目录不可写: %s: %v（回退目录 %s 同样不可用: %v）", root, err, fallback, fallbackErr)
	}
	logrus.Warnf("action state dir %s 不可写（%v），回退到 %s", root, err, fallback)
	return fallback, nil
}

// dirWritable 用一次临时文件创建探测目录是否真的可写（MkdirAll 成功不代表可写）。
func dirWritable(dir string) bool {
	file, err := os.CreateTemp(dir, ".probe-*")
	if err != nil {
		return false
	}
	name := file.Name()
	_ = file.Close()
	_ = os.Remove(name)
	return true
}

func DefaultActionStateStore(accountParts ...string) (*ActionStateStore, error) {
	key := strings.Join(accountParts, "|")
	return NewActionStateStore("", key)
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

// RecordReadStage 单次 update 同时更新阅读时长、正文滚动次数与 LastReadAt，用于 Read 结束时一次性落盘。
func (s *ActionStateStore) RecordReadStage(feedID string, duration time.Duration, scrollCount int) error {
	if scrollCount <= 0 {
		scrollCount = 1
	}
	return s.update(func(state *ActionState) {
		if state.LastOpenedFeedID == feedID {
			now := time.Now()
			state.ReadDuration += duration
			state.FeedScrollCount += scrollCount
			state.LastReadAt = now
		}
	})
}

func (s *ActionStateStore) RecordCommentDwell(feedID string, duration time.Duration, scrolled bool) error {
	return s.update(func(state *ActionState) {
		if state.LastOpenedFeedID == feedID {
			now := time.Now()
			state.CommentDwellTime += duration
			if scrolled {
				state.CommentScrollCount++
			}
			// 有有效停留或滚动时同步更新 LastReadAt，使评论区阅读可解除"连续互动前需重新阅读"。
			if duration > 0 || scrolled {
				state.LastReadAt = now
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
	return s.update(func(state *ActionState) {
		state.ConsecutiveFailures++
		state.LastRiskText = reason
		if cooldown > 0 {
			state.RiskCooldownUntil = time.Now().Add(cooldown)
		}
	})
}

func (s *ActionStateStore) ClearRisk() error {
	return s.update(func(state *ActionState) {
		state.ConsecutiveFailures = 0
		state.RiskCooldownUntil = time.Time{}
		state.LastRiskText = ""
	})
}

func (s *ActionStateStore) ClearIdentity() error {
	return s.update(func(state *ActionState) {
		state.Identity = nil
	})
}

// validateInteractionState 校验目标与基础风控：冷却、已打开 feed、feed 匹配、30 分钟有效期。
// Target 与完整 ValidateInteraction 共用，避免重复规则。
func validateInteractionState(state ActionState, feedID string) error {
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
	return nil
}

// ValidateInteractionTarget 只校验冷却、已打开 feed、feed 匹配和 30 分钟有效期，
// 不校验阅读/滚动阈值，供回复定位前确认页面和基础风控状态。
func (s *ActionStateStore) ValidateInteractionTarget(feedID string) error {
	state, err := s.Load()
	if err != nil {
		return err
	}
	return validateInteractionState(state, feedID)
}

func (s *ActionStateStore) ValidateInteraction(feedID, action string) error {
	state, err := s.Load()
	if err != nil {
		return err
	}
	if err := validateInteractionState(state, feedID); err != nil {
		return err
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
		if state.ReadDuration < 20*time.Second || state.FeedScrollCount < 1 {
			return fmt.Errorf("评论前阅读至少 20 秒且需要正文或图片区域滚动")
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
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

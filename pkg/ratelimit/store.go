package ratelimit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Store interface {
	Load() (*State, error)
	Save(*State) error
}

type State struct {
	Actions map[Action][]int64 `json:"actions"`

	All         []int64 `json:"all"`
	Interaction []int64 `json:"interaction"`
	Write       []int64 `json:"write"`
	Publish     []int64 `json:"publish"`

	RiskCooldownUntil time.Time `json:"risk_cooldown_until,omitempty"`
	LastRiskText      string    `json:"last_risk_text,omitempty"`
	LastAction        string    `json:"last_action,omitempty"`
	LastActionAt      time.Time `json:"last_action_at,omitempty"`

	ConsecutiveFailures int `json:"consecutive_failures,omitempty"`
}

func (s *State) ensure() {
	if s.Actions == nil {
		s.Actions = make(map[Action][]int64)
	}
}

// prune 丢弃 24 小时窗口外的事件；返回是否有内容被裁剪，
// 供调用方跳过"没有变化也写盘"的无效写入（SD 卡设备上写入放大显著）。
func (s *State) prune(now time.Time) bool {
	s.ensure()
	cutoff := now.Add(-24 * time.Hour).Unix()
	changed := false
	for action, events := range s.Actions {
		pruned := pruneEvents(events, cutoff)
		if len(pruned) != len(events) {
			changed = true
		}
		s.Actions[action] = pruned
	}
	if pruned := pruneEvents(s.All, cutoff); len(pruned) != len(s.All) {
		s.All = pruned
		changed = true
	}
	if pruned := pruneEvents(s.Interaction, cutoff); len(pruned) != len(s.Interaction) {
		s.Interaction = pruned
		changed = true
	}
	if pruned := pruneEvents(s.Write, cutoff); len(pruned) != len(s.Write) {
		s.Write = pruned
		changed = true
	}
	if pruned := pruneEvents(s.Publish, cutoff); len(pruned) != len(s.Publish) {
		s.Publish = pruned
		changed = true
	}
	return changed
}

func pruneEvents(events []int64, cutoff int64) []int64 {
	if len(events) == 0 {
		return events
	}
	first := 0
	for first < len(events) && events[first] < cutoff {
		first++
	}
	if first == 0 {
		return events
	}
	if first == len(events) {
		return nil
	}
	return append([]int64(nil), events[first:]...)
}

type MemoryStore struct {
	mu    sync.Mutex
	state *State
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{state: &State{Actions: make(map[Action][]int64)}}
}

func (s *MemoryStore) Load() (*State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneState(s.state), nil
}

func (s *MemoryStore) Save(state *State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = cloneState(state)
	return nil
}

type FileStore struct {
	path string
}

func NewFileStore(root string, account AccountKey) (*FileStore, error) {
	if root == "" {
		root = DefaultStorePath()
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, err
	}

	key := stableAccountKey(account)
	sum := sha256.Sum256([]byte(key))
	filename := hex.EncodeToString(sum[:]) + ".json"
	return &FileStore{path: filepath.Join(root, filename)}, nil
}

func DefaultStorePath() string {
	if v := strings.TrimSpace(os.Getenv("XHS_RATELIMIT_STORE")); v != "" {
		return v
	}
	if dir, err := os.UserCacheDir(); err == nil && dir != "" {
		return filepath.Join(dir, "xiaohongshu-mcp", "ratelimit")
	}
	return filepath.Join(os.TempDir(), "xiaohongshu-mcp-ratelimit")
}

func (s *FileStore) Load() (*State, error) {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return &State{Actions: make(map[Action][]int64)}, nil
	}
	if err != nil {
		return nil, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	state.ensure()
	return &state, nil
}

func (s *FileStore) Save(state *State) error {
	state.ensure()
	// 用紧凑 JSON：内容等价，但写入量与 CPU 更小（树莓派 SD 卡上的写入放大）。
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func stableAccountKey(account AccountKey) string {
	if strings.TrimSpace(account.AccountID) != "" {
		return "account:" + strings.TrimSpace(account.AccountID)
	}

	parts := []string{
		"profile:" + strings.TrimSpace(account.ProfileDir),
		"cookies:" + strings.TrimSpace(account.CookiesPath),
	}
	if account.CookiesPath != "" {
		if data, err := os.ReadFile(account.CookiesPath); err == nil {
			sum := sha256.Sum256(data)
			parts = append(parts, "cookies_sha256:"+hex.EncodeToString(sum[:]))
		}
	}
	return strings.Join(parts, "|")
}

func cloneState(state *State) *State {
	if state == nil {
		return &State{Actions: make(map[Action][]int64)}
	}
	clone := *state
	clone.Actions = make(map[Action][]int64, len(state.Actions))
	for action, events := range state.Actions {
		clone.Actions[action] = append([]int64(nil), events...)
	}
	clone.All = append([]int64(nil), state.All...)
	clone.Interaction = append([]int64(nil), state.Interaction...)
	clone.Write = append([]int64(nil), state.Write...)
	clone.Publish = append([]int64(nil), state.Publish...)
	return &clone
}

package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

const writeConfirmationTTL = 5 * time.Minute

type WriteConfirmationGate struct {
	mu      sync.Mutex
	enabled bool
	ttl     time.Duration
	pending map[string]pendingWriteConfirmation
}

type pendingWriteConfirmation struct {
	Token     string
	Key       string
	Action    string
	Summary   string
	ExpiresAt time.Time
}

type writeConfirmationChallenge struct {
	RequiresConfirmation bool      `json:"requires_confirmation"`
	ConfirmToken         string    `json:"confirm_token"`
	ExpiresAt            time.Time `json:"expires_at"`
	Action               string    `json:"action"`
	Summary              string    `json:"summary"`
	Message              string    `json:"message"`
}

func NewWriteConfirmationGate(enabled bool) *WriteConfirmationGate {
	return &WriteConfirmationGate{
		enabled: enabled,
		ttl:     writeConfirmationTTL,
		pending: make(map[string]pendingWriteConfirmation),
	}
}

func (g *WriteConfirmationGate) Enabled() bool {
	return g != nil && g.enabled
}

func (g *WriteConfirmationGate) Confirm(action, key, summary, token string) (*writeConfirmationChallenge, error) {
	if !g.Enabled() {
		return nil, nil
	}
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pruneLocked(now)

	if token != "" {
		pending, ok := g.pending[token]
		if !ok {
			return nil, fmt.Errorf("确认令牌不存在或已过期")
		}
		if pending.Key != key || pending.Action != action {
			return nil, fmt.Errorf("确认令牌与当前写操作不匹配")
		}
		delete(g.pending, token)
		return nil, nil
	}

	newToken, err := newWriteConfirmToken()
	if err != nil {
		return nil, err
	}
	challenge := pendingWriteConfirmation{
		Token:     newToken,
		Key:       key,
		Action:    action,
		Summary:   summary,
		ExpiresAt: now.Add(g.ttl),
	}
	g.pending[newToken] = challenge
	return &writeConfirmationChallenge{
		RequiresConfirmation: true,
		ConfirmToken:         newToken,
		ExpiresAt:            challenge.ExpiresAt,
		Action:               action,
		Summary:              summary,
		Message:              "写操作需要确认。请用相同参数再次调用，并传入 confirm_token。",
	}, nil
}

func (g *WriteConfirmationGate) pruneLocked(now time.Time) {
	for token, pending := range g.pending {
		if !now.Before(pending.ExpiresAt) {
			delete(g.pending, token)
		}
	}
}

func newWriteConfirmToken() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err == nil {
		return hex.EncodeToString(buf[:]), nil
	} else {
		return "", fmt.Errorf("生成确认令牌失败: %w", err)
	}
}

func writeConfirmationKey(parts ...any) string {
	data, _ := json.Marshal(parts)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

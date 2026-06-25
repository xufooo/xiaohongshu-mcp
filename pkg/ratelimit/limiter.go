// Package ratelimit 提供账号维度的防封号速率限制。
package ratelimit

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"time"
)

type Action string

const (
	ActionBrowse   Action = "browse"
	ActionSearch   Action = "search"
	ActionOpenNote Action = "open_note"
	ActionLike     Action = "like"
	ActionFavorite Action = "favorite"
	ActionComment  Action = "comment"
	ActionReply    Action = "reply"
	ActionPublish  Action = "publish"
)

type DelayConfig struct {
	Min time.Duration
	P50 time.Duration
	P95 time.Duration
	Max time.Duration
}

type BudgetConfig struct {
	Per10Min int
	PerHour  int
	PerDay   int
}

type ActionLimitConfig struct {
	Delay  DelayConfig
	Budget BudgetConfig
}

type AccountKey struct {
	AccountID  string
	ProfileDir string
	CookiesPath string
}

// Config 速率限制配置。
type Config struct {
	// 旧配置字段保留给已有调用方；未提供动作配置时会按 MaxPerHour 走兼容模式。
	MaxPerHour    int
	CooldownBase  time.Duration
	WarnThreshold float64
	SlowThreshold float64

	Account     AccountKey
	StorePath   string
	AutoWaitMax time.Duration
	Limits      map[Action]ActionLimitConfig
	Global      GlobalBudgetConfig
}

type GlobalBudgetConfig struct {
	All         BudgetConfig
	Interaction BudgetConfig
	Write       BudgetConfig
	Publish     BudgetConfig
}

// Info 速率限制状态。
type Info struct {
	Used          int    `json:"used"`
	Limit         int    `json:"limit"`
	Remaining     int    `json:"remaining"`
	WindowSeconds int    `json:"window_seconds"`
	ResetUnix     int64  `json:"reset_unix"`
	Warning       string `json:"warning,omitempty"`
	RetryAfter    *int   `json:"retry_after,omitempty"`
	Action        string `json:"action,omitempty"`
	Scope         string `json:"scope,omitempty"`
}

// Limiter 速率限制器（goroutine-safe）。
type Limiter struct {
	mu     sync.Mutex
	cfg    Config
	store  Store
	legacy bool
}

func DefaultConfig() Config {
	return Config{
		MaxPerHour:    30,
		CooldownBase:  3 * time.Second,
		WarnThreshold: 0.8,
		SlowThreshold: 0.9,
		AutoWaitMax:   2 * time.Minute,
		Limits:        DefaultActionLimits(),
		Global: GlobalBudgetConfig{
			All:         BudgetConfig{Per10Min: 60, PerHour: 250, PerDay: 1200},
			Interaction: BudgetConfig{Per10Min: 10, PerHour: 40, PerDay: 160},
			Write:       BudgetConfig{Per10Min: 3, PerHour: 12, PerDay: 45},
			Publish:     BudgetConfig{Per10Min: 1, PerHour: 2, PerDay: 5},
		},
	}
}

func DefaultActionLimits() map[Action]ActionLimitConfig {
	return map[Action]ActionLimitConfig{
		ActionBrowse: {
			Delay:  DelayConfig{Min: 6 * time.Second, P50: 12 * time.Second, P95: 25 * time.Second, Max: 45 * time.Second},
			Budget: BudgetConfig{Per10Min: 35, PerHour: 150, PerDay: 800},
		},
		ActionSearch: {
			Delay:  DelayConfig{Min: 12 * time.Second, P50: 22 * time.Second, P95: 45 * time.Second, Max: 90 * time.Second},
			Budget: BudgetConfig{Per10Min: 6, PerHour: 20, PerDay: 80},
		},
		ActionOpenNote: {
			Delay:  DelayConfig{Min: 10 * time.Second, P50: 25 * time.Second, P95: 70 * time.Second, Max: 150 * time.Second},
			Budget: BudgetConfig{Per10Min: 18, PerHour: 80, PerDay: 400},
		},
		ActionLike: {
			Delay:  DelayConfig{Min: 25 * time.Second, P50: 60 * time.Second, P95: 180 * time.Second, Max: 300 * time.Second},
			Budget: BudgetConfig{Per10Min: 6, PerHour: 25, PerDay: 120},
		},
		ActionFavorite: {
			Delay:  DelayConfig{Min: 35 * time.Second, P50: 90 * time.Second, P95: 240 * time.Second, Max: 480 * time.Second},
			Budget: BudgetConfig{Per10Min: 4, PerHour: 15, PerDay: 60},
		},
		ActionComment: {
			Delay:  DelayConfig{Min: 90 * time.Second, P50: 180 * time.Second, P95: 480 * time.Second, Max: 900 * time.Second},
			Budget: BudgetConfig{Per10Min: 2, PerHour: 8, PerDay: 30},
		},
		ActionReply: {
			Delay:  DelayConfig{Min: 120 * time.Second, P50: 240 * time.Second, P95: 600 * time.Second, Max: 1200 * time.Second},
			Budget: BudgetConfig{Per10Min: 2, PerHour: 6, PerDay: 25},
		},
		ActionPublish: {
			Delay:  DelayConfig{Min: 180 * time.Second, P50: 480 * time.Second, P95: 1200 * time.Second, Max: 1800 * time.Second},
			Budget: BudgetConfig{Per10Min: 1, PerHour: 2, PerDay: 5},
		},
	}
}

// New 创建速率限制器。
func New(cfg Config) *Limiter {
	legacy := cfg.Limits == nil && cfg.Global == (GlobalBudgetConfig{})
	if legacy {
		cfg = normalizeLegacyConfig(cfg)
		return &Limiter{cfg: cfg, store: NewMemoryStore(), legacy: true}
	}

	cfg = normalizeConfig(cfg)
	store, err := NewFileStore(cfg.StorePath, cfg.Account)
	if err != nil {
		// 持久化不可用时降级内存存储，避免服务启动失败。
		store = NewMemoryStore()
	}
	return &Limiter{cfg: cfg, store: store}
}

func normalizeLegacyConfig(cfg Config) Config {
	if cfg.MaxPerHour <= 0 {
		cfg.MaxPerHour = 30
	}
	if cfg.CooldownBase <= 0 {
		cfg.CooldownBase = 3 * time.Second
	}
	if cfg.WarnThreshold <= 0 {
		cfg.WarnThreshold = 0.8
	}
	if cfg.SlowThreshold <= 0 {
		cfg.SlowThreshold = 0.9
	}
	cfg.AutoWaitMax = 2 * time.Minute
	cfg.Limits = map[Action]ActionLimitConfig{
		ActionBrowse: {
			Delay: DelayConfig{
				Min: cfg.CooldownBase,
				P50: cfg.CooldownBase,
				P95: cfg.CooldownBase * 2,
				Max: cfg.CooldownBase * 3,
			},
			Budget: BudgetConfig{PerHour: cfg.MaxPerHour},
		},
	}
	return cfg
}

func normalizeConfig(cfg Config) Config {
	def := DefaultConfig()
	if cfg.MaxPerHour <= 0 {
		cfg.MaxPerHour = def.MaxPerHour
	}
	if cfg.CooldownBase <= 0 {
		cfg.CooldownBase = def.CooldownBase
	}
	if cfg.WarnThreshold <= 0 {
		cfg.WarnThreshold = def.WarnThreshold
	}
	if cfg.SlowThreshold <= 0 {
		cfg.SlowThreshold = def.SlowThreshold
	}
	if cfg.AutoWaitMax <= 0 {
		cfg.AutoWaitMax = def.AutoWaitMax
	}
	if cfg.Limits == nil {
		cfg.Limits = def.Limits
	}
	if cfg.Global == (GlobalBudgetConfig{}) {
		cfg.Global = def.Global
	}
	if cfg.StorePath == "" {
		cfg.StorePath = DefaultStorePath()
	}
	return cfg
}

// Check 检查默认浏览操作是否允许。
func (l *Limiter) Check() (Info, bool, error) {
	return l.CheckAction(ActionBrowse)
}

func (l *Limiter) CheckAction(action Action) (Info, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	state, err := l.loadState(now)
	if err != nil {
		return Info{}, false, err
	}

	info, canProceed := l.evaluate(state, action, now)
	return info, canProceed, nil
}

// Reserve 原子地检查并预占默认浏览操作额度。
func (l *Limiter) Reserve(ctx context.Context) (Info, time.Duration, bool, error) {
	return l.ReserveAction(ctx, ActionBrowse)
}

// ReserveAction 原子地检查并预占一次指定操作额度。
func (l *Limiter) ReserveAction(ctx context.Context, action Action) (Info, time.Duration, bool, error) {
	select {
	case <-ctx.Done():
		return Info{}, 0, false, ctx.Err()
	default:
	}

	l.mu.Lock()

	now := time.Now()
	state, err := l.loadState(now)
	if err != nil {
		l.mu.Unlock()
		return Info{}, 0, false, err
	}

	info, canProceed := l.evaluate(state, action, now)
	if !canProceed {
		l.mu.Unlock()
		return info, 0, false, nil
	}

	wait := l.waitBeforeAction(state, action, now)
	if wait > l.cfg.AutoWaitMax {
		info.Warning = fmt.Sprintf("操作节奏过快，建议 %d 秒后重试", int(wait.Seconds()))
		retryAfter := int(wait.Seconds())
		info.RetryAfter = &retryAfter
		l.mu.Unlock()
		return info, 0, false, nil
	}

	l.recordLocked(state, action, now)
	info, _ = l.evaluate(state, action, now)
	l.mu.Unlock()

	return info, wait, true, nil
}

// Record 记录一次默认浏览操作。
func (l *Limiter) Record() {
	l.RecordAction(ActionBrowse)
}

func (l *Limiter) RecordAction(action Action) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	state, err := l.loadState(now)
	if err != nil {
		return
	}
	l.recordLocked(state, action, now)
}

// WaitDuration 计算操作前需要等待的时间（含偏态随机延迟）。
func (l *Limiter) WaitDuration(info Info) time.Duration {
	action := Action(info.Action)
	if action == "" {
		action = ActionBrowse
	}
	cfg := l.actionConfig(action)
	return sampleDelay(cfg.Delay)
}

func (l *Limiter) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = l.store.Save(&State{})
}

// RecordSuccess 清空连续失败计数。
func (l *Limiter) RecordSuccess() {
	l.mu.Lock()
	defer l.mu.Unlock()

	state, err := l.loadState(time.Now())
	if err != nil {
		return
	}
	state.ConsecutiveFailures = 0
	_ = l.store.Save(state)
}

// RecordFailure 记录普通失败；连续 3 次失败后进入短熔断。
func (l *Limiter) RecordFailure(reason string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	state, err := l.loadState(now)
	if err != nil {
		return
	}
	state.ConsecutiveFailures++
	state.LastRiskText = reason
	if state.ConsecutiveFailures >= 3 {
		cooldown := time.Duration(30+rand.Intn(91)) * time.Minute
		state.RiskCooldownUntil = now.Add(cooldown)
	}
	_ = l.store.Save(state)
}

// RecordRisk 记录验证码、登录异常或风控提示，并立即进入账号级熔断。
func (l *Limiter) RecordRisk(reason string, cooldown time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if cooldown <= 0 {
		cooldown = time.Duration(6+rand.Intn(19)) * time.Hour
	}
	now := time.Now()
	state, err := l.loadState(now)
	if err != nil {
		return
	}
	state.ConsecutiveFailures++
	state.LastRiskText = reason
	state.RiskCooldownUntil = now.Add(cooldown)
	_ = l.store.Save(state)
}

func (l *Limiter) String() string {
	info, _, _ := l.Check()
	if info.Limit <= 0 {
		return "ratelimit[unlimited]"
	}
	ratio := math.Round(float64(info.Used)/float64(info.Limit)*100) / 100
	resetAt := time.Unix(info.ResetUnix, 0)
	return fmt.Sprintf("ratelimit[action=%s used=%d/%d (%.0f%%), reset=%s]",
		info.Action, info.Used, info.Limit, ratio*100, resetAt.Format("15:04"))
}

func (l *Limiter) loadState(now time.Time) (*State, error) {
	state, err := l.store.Load()
	if err != nil {
		return nil, err
	}
	state.ensure()
	state.prune(now)
	if err := l.store.Save(state); err != nil {
		return nil, err
	}
	return state, nil
}

func (l *Limiter) evaluate(state *State, action Action, now time.Time) (Info, bool) {
	if state.RiskCooldownUntil.After(now) {
		retryAfter := int(state.RiskCooldownUntil.Sub(now).Seconds())
		if retryAfter < 0 {
			retryAfter = 0
		}
		return Info{
			Used:          0,
			Limit:         0,
			Remaining:     0,
			WindowSeconds: int(time.Until(state.RiskCooldownUntil).Seconds()),
			ResetUnix:     state.RiskCooldownUntil.Unix(),
			Warning:       fmt.Sprintf("账号处于风控冷却中，建议 %d 秒后重试：%s", retryAfter, state.LastRiskText),
			RetryAfter:    &retryAfter,
			Action:        string(action),
			Scope:         "risk",
		}, false
	}

	checks := []limitCheck{
		l.checkBudget("action", state.Actions[action], l.actionConfig(action).Budget, now),
		l.checkBudget("all", state.All, l.cfg.Global.All, now),
	}
	if isInteraction(action) {
		checks = append(checks, l.checkBudget("interaction", state.Interaction, l.cfg.Global.Interaction, now))
	}
	if isWrite(action) {
		checks = append(checks, l.checkBudget("write", state.Write, l.cfg.Global.Write, now))
	}
	if action == ActionPublish {
		checks = append(checks, l.checkBudget("publish", state.Publish, l.cfg.Global.Publish, now))
	}

	info := checks[0].info
	canProceed := true
	for _, check := range checks {
		if check.info.Limit > 0 && (info.Limit <= 0 || check.ratio() > float64(info.Used)/float64(info.Limit)) {
			info = check.info
		}
		if !check.allowed {
			info = check.info
			canProceed = false
			break
		}
	}
	info.Action = string(action)
	return info, canProceed
}

type limitCheck struct {
	info    Info
	allowed bool
}

func (c limitCheck) ratio() float64 {
	if c.info.Limit <= 0 {
		return 0
	}
	return float64(c.info.Used) / float64(c.info.Limit)
}

func (l *Limiter) checkBudget(scope string, events []int64, budget BudgetConfig, now time.Time) limitCheck {
	windows := []struct {
		name     string
		seconds  int
		duration time.Duration
		limit    int
	}{
		{name: "10min", seconds: 600, duration: 10 * time.Minute, limit: budget.Per10Min},
		{name: "hour", seconds: 3600, duration: time.Hour, limit: budget.PerHour},
		{name: "day", seconds: 86400, duration: 24 * time.Hour, limit: budget.PerDay},
	}

	var best Info
	bestRatio := -1.0
	for _, w := range windows {
		if w.limit <= 0 {
			continue
		}
		used := countSince(events, now.Add(-w.duration))
		remaining := w.limit - used
		if remaining < 0 {
			remaining = 0
		}
		reset := resetUnix(events, now, w.duration)
		info := Info{
			Used:          used,
			Limit:         w.limit,
			Remaining:     remaining,
			WindowSeconds: w.seconds,
			ResetUnix:     reset,
			Scope:         scope + ":" + w.name,
		}

		ratio := float64(used) / float64(w.limit)
		if ratio >= 1 {
			retryAfter := int(time.Unix(reset, 0).Sub(now).Seconds())
			if retryAfter < 0 {
				retryAfter = 0
			}
			info.RetryAfter = &retryAfter
			info.Warning = fmt.Sprintf("已达%s窗口 %d 次上限（防封保护），建议 %d 秒后重试", info.Scope, w.limit, retryAfter)
			return limitCheck{info: info, allowed: false}
		}
		if ratio > bestRatio {
			best = info
			bestRatio = ratio
		}
	}

	if best.Limit <= 0 {
		best = Info{Limit: 0, Remaining: math.MaxInt, Scope: scope}
	}
	ratio := float64(best.Used) / float64(best.Limit)
	if best.Limit > 0 && ratio >= l.cfg.SlowThreshold {
		best.Warning = fmt.Sprintf("已使用 %d/%d 次额度（%.0f%%），操作将自动减速", best.Used, best.Limit, ratio*100)
	} else if best.Limit > 0 && ratio >= l.cfg.WarnThreshold {
		best.Warning = fmt.Sprintf("已使用 %d/%d 次额度（%.0f%%），请注意操作频率", best.Used, best.Limit, ratio*100)
	}
	return limitCheck{info: best, allowed: true}
}

func (l *Limiter) recordLocked(state *State, action Action, now time.Time) {
	ts := now.Unix()
	state.Actions[action] = append(state.Actions[action], ts)
	state.All = append(state.All, ts)
	if isInteraction(action) {
		state.Interaction = append(state.Interaction, ts)
	}
	if isWrite(action) {
		state.Write = append(state.Write, ts)
	}
	if action == ActionPublish {
		state.Publish = append(state.Publish, ts)
	}
	state.LastAction = string(action)
	state.LastActionAt = now
	_ = l.store.Save(state)
}

func (l *Limiter) waitBeforeAction(state *State, action Action, now time.Time) time.Duration {
	if state.LastActionAt.IsZero() {
		return 0
	}
	delay := sampleDelay(l.actionConfig(action).Delay)
	elapsed := now.Sub(state.LastActionAt)
	if elapsed >= delay {
		return 0
	}
	return delay - elapsed
}

func (l *Limiter) actionConfig(action Action) ActionLimitConfig {
	cfg, ok := l.cfg.Limits[action]
	if ok {
		return cfg
	}
	return l.cfg.Limits[ActionBrowse]
}

func countSince(events []int64, cutoff time.Time) int {
	count := 0
	cutoffUnix := cutoff.Unix()
	for _, ts := range events {
		if ts >= cutoffUnix {
			count++
		}
	}
	return count
}

func resetUnix(events []int64, now time.Time, window time.Duration) int64 {
	cutoff := now.Add(-window).Unix()
	var oldest int64
	for _, ts := range events {
		if ts >= cutoff && (oldest == 0 || ts < oldest) {
			oldest = ts
		}
	}
	if oldest == 0 {
		return now.Add(window).Unix()
	}
	return time.Unix(oldest, 0).Add(window).Unix()
}

func sampleDelay(cfg DelayConfig) time.Duration {
	if cfg.P50 <= 0 {
		return 0
	}
	if cfg.Min <= 0 {
		cfg.Min = cfg.P50 / 2
	}
	if cfg.P95 <= cfg.P50 {
		cfg.P95 = cfg.P50 * 2
	}
	if cfg.Max <= cfg.P95 {
		cfg.Max = cfg.P95
	}

	mu := math.Log(float64(cfg.P50))
	sigma := (math.Log(float64(cfg.P95)) - math.Log(float64(cfg.P50))) / 1.64485
	if sigma <= 0 || math.IsNaN(sigma) || math.IsInf(sigma, 0) {
		sigma = 0.4
	}

	for i := 0; i < 8; i++ {
		d := time.Duration(math.Exp(mu + sigma*rand.NormFloat64()))
		if d >= cfg.Min && d <= cfg.Max {
			return d
		}
	}

	// 截断后仍避免精准卡 Min，给安全下限留一点随机缓冲。
	if rand.Float64() < 0.75 {
		extra := time.Duration(float64(cfg.Min) * (0.1 + rand.Float64()*0.3))
		if cfg.Min+extra < cfg.Max {
			return cfg.Min + extra
		}
	}
	return cfg.Max
}

func isInteraction(action Action) bool {
	return action == ActionLike || action == ActionFavorite || isWrite(action)
}

func isWrite(action Action) bool {
	return action == ActionComment || action == ActionReply || action == ActionPublish
}

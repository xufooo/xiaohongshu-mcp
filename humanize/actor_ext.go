// Package humanize provides human-like browser interactions for go-rod.
//
// Unlike a single smooth Bezier curve, this package:
//   - splits movements into multiple segments with different curve families
//   - adds jitter, pauses, overshoots, and random scroll events
//   - types with variable speed, bursts, and occasional typos + corrections
//   - exposes slow/normal/fast speed profiles based on real human timing
package humanize

import (
	"context"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
)

// Actor groups humanized mouse and keyboard actions.
type Actor struct {
	Mouse    *Mouse
	Keyboard *Keyboard
	cfg      Config
	ctx      context.Context
	state    *inputState
}

// inputState 保存 Actor 层共享的输入状态（当前位置、按下的鼠标按钮、已按键）。
// Mouse/Keyboard 不再依赖 rod.Page 内部状态，避免 CDP 调用未绑定 ctx 时卡死。
type inputState struct {
	mu         sync.Mutex
	dispatchMu sync.Mutex
	pos        Point
	buttons    map[proto.InputMouseButton]int
	pressed    map[input.Key]bool
}

func newInputState() *inputState {
	return &inputState{
		buttons: make(map[proto.InputMouseButton]int),
		pressed: make(map[input.Key]bool),
	}
}

// mouseSnapshot 锁内一次性取得 pos/buttons/modifiers，供单个 dispatch 使用。
type mouseSnapshot struct {
	pos       Point
	buttons   int
	modifiers int
}

func (s *inputState) mouseSnapshot() mouseSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	flag := 0
	for btn := range s.buttons {
		flag |= input.MouseKeys[btn]
	}
	return mouseSnapshot{pos: s.pos, buttons: flag, modifiers: s.modifiersLocked()}
}

// modifiers 返回当前按下的修饰键位掩码（供 Input.dispatchMouseEvent 使用）。
func (s *inputState) modifiers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.modifiersLocked()
}

// modifiersLocked 返回修饰键位掩码，调用方必须持有 s.mu。
func (s *inputState) modifiersLocked() int {
	var mods int
	if s.pressed[input.ControlLeft] || s.pressed[input.ControlRight] {
		mods |= input.ModifierControl
	}
	if s.pressed[input.AltLeft] || s.pressed[input.AltRight] {
		mods |= input.ModifierAlt
	}
	if s.pressed[input.ShiftLeft] || s.pressed[input.ShiftRight] {
		mods |= input.ModifierShift
	}
	if s.pressed[input.MetaLeft] || s.pressed[input.MetaRight] {
		mods |= input.ModifierMeta
	}
	return mods
}

// New creates a humanized actor for the given page.
func New(page *rod.Page, cfg Config) *Actor {
	return NewWithContext(page, cfg, context.Background())
}

// NewWithContext creates a humanized actor for the given page and context.
func NewWithContext(page *rod.Page, cfg Config, ctx context.Context) *Actor {
	state := newInputState()
	mouse := newMouseWithState(page, cfg, state)
	actor := &Actor{
		Mouse:    mouse,
		Keyboard: NewKeyboard(page, cfg, mouse),
		cfg:      cfg,
		state:    state,
	}
	actor.SetContext(ctx)
	return actor
}

// Config returns the actor's configuration.
func (a *Actor) Config() Config {
	return a.cfg
}

// SetContext updates the context used by humanized delays and all CDP calls.
// 注意：真实 CDP 调用（Mouse/Keyboard dispatch）也会使用该 ctx，不再仅用于延迟。
func (a *Actor) SetContext(ctx context.Context) {
	a.ctx = ctx
	a.Mouse.setContext(ctx)
	a.Keyboard.setContext(ctx)
}

// Ctx returns the actor's context.
func (a *Actor) Ctx() context.Context {
	return a.ctx
}

// Sleep waits for d, or returns immediately when the actor's context is cancelled.
func (a *Actor) Sleep(d time.Duration) error {
	return sleepWithContext(a.ctx, d)
}

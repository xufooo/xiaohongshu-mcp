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
	"time"

	"github.com/go-rod/rod"
)

// Actor groups humanized mouse and keyboard actions.
type Actor struct {
	Mouse    *Mouse
	Keyboard *Keyboard
	cfg      Config
	ctx      context.Context
}

// New creates a humanized actor for the given page.
func New(page *rod.Page, cfg Config) *Actor {
	return NewWithContext(page, cfg, context.Background())
}

// NewWithContext creates a humanized actor for the given page and context.
func NewWithContext(page *rod.Page, cfg Config, ctx context.Context) *Actor {
	mouse := NewMouse(page, cfg)
	actor := &Actor{
		Mouse:    mouse,
		Keyboard: NewKeyboard(page, cfg, mouse),
		cfg:      cfg,
	}
	actor.SetContext(ctx)
	return actor
}

// Config returns the actor's configuration.
func (a *Actor) Config() Config {
	return a.cfg
}

// SetContext updates the context used by humanized delays.
func (a *Actor) SetContext(ctx context.Context) {
	a.ctx = ctx
	a.Mouse.setContext(ctx)
	a.Keyboard.setContext(ctx)
}

// Sleep waits for d, or returns immediately when the actor's context is cancelled.
func (a *Actor) Sleep(d time.Duration) error {
	return sleepWithContext(a.ctx, d)
}

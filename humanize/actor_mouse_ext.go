package humanize

import (
	"context"
	"errors"
	"math"
	"math/rand"
	"os"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/ysmood/gson"
)

var debugMouse bool

func init() {
	v := os.Getenv("HUMANIZE_DEBUG")
	debugMouse = v == "1" || v == "true" || v == "yes"
}

// Mouse provides human-like mouse operations.
type Mouse struct {
	page        *rod.Page
	cfg         Config
	ctx         context.Context
	state       *inputState
	initialized bool
}

// NewMouse creates a new humanized mouse wrapper.
func NewMouse(page *rod.Page, cfg Config) *Mouse {
	return newMouseWithState(page, cfg, newInputState())
}

func newMouseWithState(page *rod.Page, cfg Config, state *inputState) *Mouse {
	return &Mouse{page: page, cfg: cfg, ctx: context.Background(), state: state}
}

func (m *Mouse) setContext(ctx context.Context) {
	m.ctx = ctx
}

// boundPage 返回绑定当前 ctx 的 page clone。真实 CDP 调用必须走它，
// 否则 renderer 卡死时 rod.Mouse 内部未绑定 ctx 的调用不响应取消。
func (m *Mouse) boundPage() *rod.Page {
	return m.page.Context(m.ctx)
}

// dispatchMouseMove 发送 Input.dispatchMouseEvent(mouseMoved) 到绑定 ctx 的页面。
func (m *Mouse) dispatchMouseMove(bound *rod.Page, p Point) error {
	m.state.dispatchMu.Lock()
	defer m.state.dispatchMu.Unlock()
	snap := m.state.mouseSnapshot()
	if err := proto.InputDispatchMouseEvent{
		Type:      proto.InputDispatchMouseEventTypeMouseMoved,
		X:         p.X,
		Y:         p.Y,
		Button:    proto.InputMouseButtonNone,
		Buttons:   gson.Int(snap.buttons),
		Modifiers: snap.modifiers,
	}.Call(bound); err != nil {
		return err
	}
	m.state.mu.Lock()
	m.state.pos = p
	m.state.mu.Unlock()
	return nil
}

// posSnapshot 返回当前鼠标位置（锁内快照）。
func (m *Mouse) posSnapshot() Point {
	m.state.mu.Lock()
	defer m.state.mu.Unlock()
	return m.state.pos
}

// dispatchMouseButton 发送按下/释放事件。全程持 dispatchMu 串行化，
// CDP 成功后才提交按钮状态，失败时不提交本地候选状态。
func (m *Mouse) dispatchMouseButton(typ proto.InputDispatchMouseEventType, button proto.InputMouseButton, clickCount int) error {
	m.state.dispatchMu.Lock()
	defer m.state.dispatchMu.Unlock()
	bound := m.boundPage()

	m.state.mu.Lock()
	nextButtons := make(map[proto.InputMouseButton]int, len(m.state.buttons)+1)
	for btn, cnt := range m.state.buttons {
		nextButtons[btn] = cnt
	}
	if typ == proto.InputDispatchMouseEventTypeMousePressed {
		nextButtons[button] = clickCount
	} else {
		delete(nextButtons, button)
	}
	flag := 0
	for btn := range nextButtons {
		flag |= input.MouseKeys[btn]
	}
	snap := mouseSnapshot{pos: m.state.pos, buttons: flag, modifiers: m.state.modifiersLocked()}
	m.state.mu.Unlock()

	if err := proto.InputDispatchMouseEvent{
		Type:       typ,
		X:          snap.pos.X,
		Y:          snap.pos.Y,
		Button:     button,
		Buttons:    gson.Int(snap.buttons),
		ClickCount: clickCount,
		Modifiers:  snap.modifiers,
	}.Call(bound); err != nil {
		return err
	}

	m.state.mu.Lock()
	m.state.buttons = nextButtons
	m.state.mu.Unlock()
	return nil
}

// dispatchMouseScroll 发送滚轮事件。全程持 dispatchMu 串行化。
func (m *Mouse) dispatchMouseScroll(deltaX, deltaY float64) error {
	m.state.dispatchMu.Lock()
	defer m.state.dispatchMu.Unlock()
	bound := m.boundPage()
	snap := m.state.mouseSnapshot()
	if err := proto.InputDispatchMouseEvent{
		Type:      proto.InputDispatchMouseEventTypeMouseWheel,
		X:         snap.pos.X,
		Y:         snap.pos.Y,
		DeltaX:    deltaX,
		DeltaY:    deltaY,
		Buttons:   gson.Int(snap.buttons),
		Modifiers: snap.modifiers,
	}.Call(bound); err != nil {
		return err
	}
	return nil
}

// initPosition moves the cursor from the rod default (0,0) to a plausible
// starting point inside the viewport. This is done once per Mouse instance so
// subsequent movements do not look like long flights from the screen corner.
// The movement itself is humanized so the cursor does not teleport.
func (m *Mouse) initPosition() error {
	if m.initialized {
		return nil
	}
	vp, err := m.viewport()
	if err != nil {
		return err
	}
	center := Point{
		X: vp.width/2 + (rand.Float64()*2-1)*vp.width*0.15,
		Y: vp.height/2 + (rand.Float64()*2-1)*vp.height*0.15,
	}

	// Mark initialized before calling moveTo to avoid recursion.
	m.initialized = true
	if err := m.moveTo(center, false); err != nil {
		m.initialized = false
		return err
	}
	return nil
}

// InitPosition eagerly moves the cursor from the rod default (0,0) to a
// plausible starting point. Call this right after a page is created so the
// first real interaction does not start from the detectable (0,0) origin.
func (m *Mouse) InitPosition() error {
	return m.initPosition()
}

// Move moves the cursor to target with a realistic, non-deterministic path.
// If the target lies outside the current viewport, the page is scrolled first
// so that the destination is rendered before the cursor moves there.
func (m *Mouse) Move(target Point) error {
	// target is in page-absolute coordinates. Scroll it into view if it is
	// outside the current viewport, then convert to viewport-relative
	// coordinates before moving the cursor (rod.Mouse.MoveTo expects
	// viewport-relative coordinates).
	if err := m.scrollToVisible(target); err != nil {
		return err
	}
	vp, err := m.viewport()
	if err != nil {
		return err
	}
	return m.moveTo(Point{
		X: target.X - vp.scrollX,
		Y: target.Y - vp.scrollY,
	}, true)
}

// MovePoint moves to a viewport-relative point.
func (m *Mouse) MovePoint(target Point) error {
	return m.moveTo(target, true)
}

// moveTo performs the actual cursor movement without any extra scrolling.
func (m *Mouse) moveTo(target Point, scrollingAllowed bool) error {
	if debugMouse {
		m.ensureDebugOverlay()
	}

	bound := m.boundPage()

	// Start from a plausible position instead of rod's default (0,0).
	if err := m.initPosition(); err != nil {
		return err
	}

	start := m.posSnapshot()
	if start == (Point{}) {
		start = m.page.Mouse.Position()
		m.state.mu.Lock()
		m.state.pos = start
		m.state.mu.Unlock()
	}
	straightDist := math.Hypot(target.X-start.X, target.Y-start.Y)

	// Derive step count from distance so short moves finish quickly and long
	// moves still have enough points to look natural.
	desiredSteps := int(straightDist / m.cfg.Mouse.StepDistance)
	if desiredSteps < m.cfg.Mouse.MinSteps {
		desiredSteps = m.cfg.Mouse.MinSteps
	}
	if desiredSteps > m.cfg.Mouse.MaxSteps {
		desiredSteps = m.cfg.Mouse.MaxSteps
	}

	path := GeneratePath(start, target, desiredSteps, desiredSteps, m.cfg.Mouse.OvershootRatio)

	// Base speed with variance.
	speed := m.cfg.Mouse.MoveSpeedPxPerSec * (1 + (rand.Float64()*2-1)*m.cfg.Mouse.SpeedVariance)

	// Total distance for velocity profile normalization.
	totalDist := 0.0
	prev := start
	for _, p := range path {
		totalDist += math.Hypot(p.X-prev.X, p.Y-prev.Y)
		prev = p
	}

	// Accelerate-then-fine-tune velocity profile: slow at the start, fast in
	// the middle, and slow again near the target. The profile is a sine hump
	// scaled so its average over [0,1] is 1.0, keeping the overall move time
	// comparable to the constant-speed baseline.
	const velocityFloor = 0.3
	velocityAmp := (1.0 - velocityFloor) * math.Pi / 2

	cumulativeDist := 0.0
	last := start
	for i, p := range path {
		// Inject jitter.
		if rand.Float64() < m.cfg.Mouse.JitterProbability {
			p = jitter(p, m.cfg.Mouse.JitterRadius)
		}

		// Distance-based step duration with ease-in-out acceleration.
		dist := math.Hypot(p.X-last.X, p.Y-last.Y)
		cumulativeDist += dist

		var stepDuration time.Duration
		if totalDist > 0 {
			t := cumulativeDist / totalDist
			// Use the midpoint of the step for smoother transitions.
			tMid := t - dist/(2*totalDist)
			if tMid < 0 {
				tMid = 0
			}
			velocity := velocityFloor + velocityAmp*math.Sin(math.Pi*tMid)
			effectiveSpeed := speed * velocity
			stepDuration = time.Duration(float64(time.Second) * dist / effectiveSpeed)
		} else {
			stepDuration = time.Duration(float64(time.Second) * dist / speed)
		}
		if stepDuration < 1*time.Millisecond {
			stepDuration = 1 * time.Millisecond
		}

		// Keep the event density high enough to look like a real mouse
		// (typical browser refresh rate is 60-120Hz). If the planned step is
		// too long, subdivide it into smaller micro-steps.
		const maxStepDuration = 16 * time.Millisecond
		subSteps := 1
		if stepDuration > maxStepDuration {
			subSteps = int(math.Ceil(float64(stepDuration) / float64(maxStepDuration)))
		}

		for j := 0; j < subSteps; j++ {
			ratio := float64(j+1) / float64(subSteps)
			subP := Point{
				X: last.X + (p.X-last.X)*ratio,
				Y: last.Y + (p.Y-last.Y)*ratio,
			}

			if err := m.dispatchMouseMove(bound, subP); err != nil {
				return err
			}

			if debugMouse {
				_ = m.tracePoint(subP.X, subP.Y, i == 0 && j == 0)
			}

			if err := sleepWithContext(m.ctx, stepDuration/time.Duration(subSteps)); err != nil {
				return err
			}
		}

		if scrollingAllowed && straightDist > 250 && rand.Float64() < m.cfg.Mouse.ScrollDuringMoveProbability {
			_ = m.scrollRandom()
			if err := sleepWithContext(m.ctx, randDuration(80*time.Millisecond, 180*time.Millisecond)); err != nil {
				return err
			}
		}
		if rand.Float64() < m.cfg.Mouse.PauseProbability {
			if err := sleepWithContext(m.ctx, randDuration(m.cfg.Mouse.PauseMin, m.cfg.Mouse.PauseMax)); err != nil {
				return err
			}
		}

		last = p
	}
	return nil
}

// Click scrolls the element into view, moves to its center with random offset, and clicks.
func (m *Mouse) Click(el *rod.Element) error {
	return m.ClickWithOptions(el, proto.InputMouseButtonLeft, 1)
}

// ClickWithOptions scrolls the element into view, moves to its center with a
// random offset, and clicks it with the requested button and click count.
func (m *Mouse) ClickWithOptions(el *rod.Element, button proto.InputMouseButton, clickCount int) error {
	if clickCount < 1 {
		clickCount = 1
	}

	// Scroll the target element into view first; its on-screen position may
	// change after scrolling (fixed/sticky elements or layout shifts).
	if err := m.ScrollIntoView(el); err != nil {
		return err
	}
	// Re-calculate the target after scrolling, because fixed/sticky elements
	// move with the viewport and the old page-absolute coordinates are stale.
	target, err := elementTarget(el.Context(m.ctx))
	if err != nil {
		return err
	}
	if err := m.moveTo(target, false); err != nil {
		return err
	}

	// Human pause before clicking.
	if err := sleepWithContext(m.ctx, randDuration(80*time.Millisecond, 350*time.Millisecond)); err != nil {
		return err
	}

	if err := m.dispatchMouseButton(proto.InputDispatchMouseEventTypeMousePressed, button, clickCount); err != nil {
		return err
	}
	if err := sleepWithContext(m.ctx, randDuration(40*time.Millisecond, 120*time.Millisecond)); err != nil {
		return err
	}
	if err := m.dispatchMouseButton(proto.InputDispatchMouseEventTypeMouseReleased, button, clickCount); err != nil {
		return err
	}
	return nil
}

// ClickNoScroll performs a human-like click without scrolling the element into
// view first. Use it when the target is already known to be visible (e.g.
// sticky/fixed elements) to avoid the overhead or infinite loops caused by
// ScrollIntoView.
func (m *Mouse) ClickNoScroll(el *rod.Element) error {
	target, err := elementTarget(el.Context(m.ctx))
	if err != nil {
		return err
	}
	if err := m.moveTo(target, false); err != nil {
		return err
	}

	// Human pause before clicking.
	if err := sleepWithContext(m.ctx, randDuration(80*time.Millisecond, 350*time.Millisecond)); err != nil {
		return err
	}

	if err := m.dispatchMouseButton(proto.InputDispatchMouseEventTypeMousePressed, proto.InputMouseButtonLeft, 1); err != nil {
		return err
	}
	if err := sleepWithContext(m.ctx, randDuration(40*time.Millisecond, 120*time.Millisecond)); err != nil {
		return err
	}
	if err := m.dispatchMouseButton(proto.InputDispatchMouseEventTypeMouseReleased, proto.InputMouseButtonLeft, 1); err != nil {
		return err
	}
	return nil
}

// ClickPoint moves to a viewport-relative point and clicks there.
func (m *Mouse) ClickPoint(target Point) error {
	if err := m.moveTo(target, false); err != nil {
		return err
	}
	if err := sleepWithContext(m.ctx, randDuration(80*time.Millisecond, 350*time.Millisecond)); err != nil {
		return err
	}
	if err := m.dispatchMouseButton(proto.InputDispatchMouseEventTypeMousePressed, proto.InputMouseButtonLeft, 1); err != nil {
		return err
	}
	if err := sleepWithContext(m.ctx, randDuration(40*time.Millisecond, 120*time.Millisecond)); err != nil {
		return err
	}
	if err := m.dispatchMouseButton(proto.InputDispatchMouseEventTypeMouseReleased, proto.InputMouseButtonLeft, 1); err != nil {
		return err
	}
	return nil
}

// Scroll scrolls by deltaY (and optionally deltaX) in human-like increments.
func (m *Mouse) Scroll(deltaX, deltaY float64) error {
	if deltaY == 0 && deltaX == 0 {
		return nil
	}

	steps := int(math.Abs(deltaY)/80) + int(math.Abs(deltaX)/80) + 3
	steps += rand.Intn(5)
	stepX := deltaX / float64(steps)
	stepY := deltaY / float64(steps)

	for i := 0; i < steps; i++ {
		if err := m.dispatchMouseScroll(stepX, stepY); err != nil {
			return err
		}
		// Variable scroll speed: faster at start, slower near end.
		base := 30 + float64(i)*5
		if err := sleepWithContext(m.ctx, time.Duration(base+rand.Float64()*40)*time.Millisecond); err != nil {
			return err
		}
	}
	return sleepWithContext(m.ctx, randDuration(200*time.Millisecond, 600*time.Millisecond))
}

// ScrollIntoView scrolls the page just enough to bring the element into the
// visible viewport using humanized wheel events. It avoids JS scrollIntoView
// which can be detected by pages observing synchronous scroll/layout changes.
// The element only needs to be visible (with a small margin); it is not forced
// to the center, so sticky/fixed elements do not cause infinite scrolling.
func (m *Mouse) ScrollIntoView(el *rod.Element) error {
	const maxAttempts = 12
	const margin = 80
	const boundaryTolerance = 1.0
	type rect struct {
		left, top, right, bottom float64
	}
	type probe struct {
		found                         bool
		target, visible               rect
		centerHit                     bool
		hitRect                       rect
		scrollTop, scrollHeight       float64
		clientHeight                  float64
		viewportWidth, viewportHeight float64
	}
	boundEl := el.Context(m.ctx)
	readProbe := func() (probe, error) {
		obj, err := boundEl.Eval(`() => {
			const r = this.getBoundingClientRect();
			const target = {left: r.left, top: r.top, right: r.right, bottom: r.bottom};
			const centerX = (r.left + r.right) / 2;
			const centerY = (r.top + r.bottom) / 2;
			const hit = document.elementFromPoint(centerX, centerY);
			const centerHit = !!hit && (hit === this || this.contains(hit));
			const hitRect = hit && !centerHit ? hit.getBoundingClientRect() : null;
			let parent = this.parentElement;
			while (parent) {
				const style = getComputedStyle(parent);
				if (/^(auto|scroll|overlay)$/.test(style.overflowY) && parent.scrollHeight > parent.clientHeight + 1) {
					const pr = parent.getBoundingClientRect();
					const rawLeft = pr.left + parent.clientLeft;
					const rawTop = pr.top + parent.clientTop;
					const rawRight = rawLeft + parent.clientWidth;
					const rawBottom = rawTop + parent.clientHeight;
					const left = Math.max(0, rawLeft);
					const top = Math.max(0, rawTop);
					const right = Math.min(window.innerWidth, rawRight);
					const bottom = Math.min(window.innerHeight, rawBottom);
					return {found: true, target, visible: {left, top, right, bottom}, centerHit, hitRect, scrollTop: parent.scrollTop, scrollHeight: parent.scrollHeight, clientHeight: parent.clientHeight, viewportWidth: window.innerWidth, viewportHeight: window.innerHeight};
				}
				parent = parent.parentElement;
			}
			return {found: false, target, centerHit, hitRect, viewportWidth: window.innerWidth, viewportHeight: window.innerHeight};
		}`)
		if err != nil {
			return probe{}, err
		}
		value, err := m.boundPage().ObjectToJSON(obj)
		if err != nil {
			return probe{}, err
		}
		readRect := func(value gson.JSON) rect {
			return rect{left: value.Get("left").Num(), top: value.Get("top").Num(), right: value.Get("right").Num(), bottom: value.Get("bottom").Num()}
		}
		result := probe{found: value.Get("found").Bool(), target: readRect(value.Get("target")), centerHit: value.Get("centerHit").Bool(), viewportWidth: value.Get("viewportWidth").Num(), viewportHeight: value.Get("viewportHeight").Num()}
		result.hitRect = readRect(value.Get("hitRect"))
		if result.found {
			result.visible = readRect(value.Get("visible"))
			result.scrollTop = value.Get("scrollTop").Num()
			result.scrollHeight = value.Get("scrollHeight").Num()
			result.clientHeight = value.Get("clientHeight").Num()
		}
		return result, nil
	}
	windowScroll := func() error {
		for i := 0; i < maxAttempts; i++ {
			shape, err := boundEl.Shape()
			if err != nil {
				return err
			}
			if len(shape.Quads) == 0 {
				return errors.New("element has no content quads")
			}
			q := shape.Quads[0]
			var minX, maxX, minY, maxY float64
			for j := 0; j < q.Len(); j++ {
				x, y := q[j*2], q[j*2+1]
				if j == 0 || x < minX { minX = x }
				if j == 0 || x > maxX { maxX = x }
				if j == 0 || y < minY { minY = y }
				if j == 0 || y > maxY { maxY = y }
			}
			vp, err := m.viewport()
			if err != nil { return err }
			centerX, centerY := (minX+maxX)/2, (minY+maxY)/2
			if centerX >= 0 && centerX <= vp.width && centerY >= 0 && centerY <= vp.height { return nil }
			if centerX < 0 || centerX > vp.width { return errors.New("element center is outside viewport horizontally") }
			var deltaX, deltaY float64
			if maxX < margin { deltaX = maxX - margin } else if minX > vp.width-margin { deltaX = minX - vp.width + margin }
			if maxY < margin { deltaY = maxY - margin } else if minY > vp.height-margin { deltaY = minY - vp.height + margin }
			if deltaX == 0 && deltaY == 0 { return errors.New("element center is outside viewport") }
			if err := m.Scroll(deltaX, deltaY); err != nil { return err }
			if err := sleepWithContext(m.ctx, randDuration(80*time.Millisecond, 200*time.Millisecond)); err != nil { return err }
			afterVP, err := m.viewport()
			if err != nil { return err }
			if math.Abs(afterVP.scrollX-vp.scrollX) < 1 && math.Abs(afterVP.scrollY-vp.scrollY) < 1 { return errors.New("window scroll made no progress") }
		}
		return errors.New("element did not become visible after maximum window scroll attempts")
	}
	for i := 0; i < maxAttempts; i++ {
		before, err := readProbe()
		if err != nil { return err }
		if !before.found { return windowScroll() }
		if before.visible.right-before.visible.left <= 1 || before.visible.bottom-before.visible.top <= 1 { return errors.New("scroll container has no visible area") }
		effectiveMargin := math.Min(margin, (before.visible.bottom-before.visible.top)/4)
		centerX := (before.target.left + before.target.right) / 2
		centerY := (before.target.top + before.target.bottom) / 2
		centerVisible := centerX >= before.visible.left && centerX <= before.visible.right && centerY >= before.visible.top && centerY <= before.visible.bottom
		if centerVisible && before.centerHit { return nil }
		if centerX < before.visible.left || centerX > before.visible.right { return errors.New("element center is outside scroll container horizontally") }
		var deltaY float64
		if centerVisible && !before.centerHit {
			invalidHitRect := before.hitRect.right-before.hitRect.left <= 0 || before.hitRect.bottom-before.hitRect.top <= 0
			fullHeightHit := before.hitRect.top <= before.visible.top+boundaryTolerance && before.hitRect.bottom >= before.visible.bottom-boundaryTolerance
			if invalidHitRect || fullHeightHit {
				if err := sleepWithContext(m.ctx, randDuration(100*time.Millisecond, 300*time.Millisecond)); err != nil {
					return err
				}
				deltaY = centerY - (before.visible.top+before.visible.bottom)/2
			} else if before.hitRect.bottom >= before.visible.bottom-boundaryTolerance {
				deltaY = centerY - before.hitRect.top + boundaryTolerance
			} else {
				deltaY = centerY - before.hitRect.bottom - boundaryTolerance
				if deltaY >= 0 { deltaY = -effectiveMargin }
			}
		} else if centerY < before.visible.top+effectiveMargin {
			deltaY = centerY - before.visible.top - effectiveMargin
		} else if centerY > before.visible.bottom-effectiveMargin {
			deltaY = centerY - before.visible.bottom + effectiveMargin
		} else {
			return nil
		}
		if before.scrollTop <= 0 && deltaY < 0 || before.scrollTop >= before.scrollHeight-before.clientHeight-1 && deltaY > 0 { return errors.New("scroll container reached its boundary") }
		wheelPoint := Point{X: (before.visible.left + before.visible.right) / 2, Y: (before.visible.top + before.visible.bottom) / 2}
		if err := m.moveTo(wheelPoint, false); err != nil { return err }
		if err := m.Scroll(0, deltaY); err != nil { return err }
		if err := sleepWithContext(m.ctx, randDuration(80*time.Millisecond, 200*time.Millisecond)); err != nil { return err }
		after, err := readProbe()
		if err != nil { return err }
		afterCenterX := (after.target.left + after.target.right) / 2
		afterCenterY := (after.target.top + after.target.bottom) / 2
		if after.found && afterCenterX >= after.visible.left && afterCenterX <= after.visible.right && afterCenterY >= after.visible.top && afterCenterY <= after.visible.bottom && after.centerHit { return nil }
		if math.Abs(after.target.top-before.target.top) < 1 && math.Abs(after.scrollTop-before.scrollTop) < 1 { return errors.New("scroll made no progress") }
	}
	return errors.New("element did not become visible after maximum scroll attempts")
}

// Hover scrolls the element into view, moves to it, and pauses briefly.
func (m *Mouse) Hover(el *rod.Element) error {
	if err := m.ScrollIntoView(el); err != nil {
		return err
	}
	target, err := elementTarget(el.Context(m.ctx))
	if err != nil {
		return err
	}
	if err := m.moveTo(target, false); err != nil {
		return err
	}
	return sleepWithContext(m.ctx, randDuration(150*time.Millisecond, 500*time.Millisecond))
}

func (m *Mouse) scrollRandom() error {
	deltaY := randomSign() * (m.cfg.Mouse.ScrollMin + rand.Float64()*(m.cfg.Mouse.ScrollMax-m.cfg.Mouse.ScrollMin))
	return m.dispatchMouseScroll(0, deltaY)
}

type viewport struct {
	scrollX, scrollY float64
	width, height    float64
}

func (m *Mouse) viewport() (viewport, error) {
	obj, err := m.boundPage().Eval(`() => ({
		scrollX: window.scrollX,
		scrollY: window.scrollY,
		innerWidth: window.innerWidth,
		innerHeight: window.innerHeight
	})`)
	if err != nil {
		return viewport{}, err
	}
	res, err := m.boundPage().ObjectToJSON(obj)
	if err != nil {
		return viewport{}, err
	}
	return viewport{
		scrollX: res.Get("scrollX").Num(),
		scrollY: res.Get("scrollY").Num(),
		width:   res.Get("innerWidth").Num(),
		height:  res.Get("innerHeight").Num(),
	}, nil
}

// scrollToVisible scrolls the page so that target is rendered inside the
// viewport with a comfortable margin. It is a no-op if target is already visible.
func (m *Mouse) scrollToVisible(target Point) error {
	vp, err := m.viewport()
	if err != nil {
		return err
	}

	const margin = 80
	var deltaX, deltaY float64

	if target.X < vp.scrollX+margin {
		deltaX = target.X - vp.scrollX - vp.width/2
	} else if target.X > vp.scrollX+vp.width-margin {
		deltaX = target.X - vp.scrollX - vp.width/2
	}

	if target.Y < vp.scrollY+margin {
		deltaY = target.Y - vp.scrollY - vp.height/2
	} else if target.Y > vp.scrollY+vp.height-margin {
		deltaY = target.Y - vp.scrollY - vp.height/2
	}

	if deltaX == 0 && deltaY == 0 {
		return nil
	}

	// Add a small random offset so the target does not always land at the exact
	// center of the viewport.
	deltaX += (rand.Float64()*2 - 1) * 30
	deltaY += (rand.Float64()*2 - 1) * 30

	return m.Scroll(deltaX, deltaY)
}

// ensureDebugOverlay injects a canvas to visualize mouse movement.
// It is only called when HUMANIZE_DEBUG=1.
func (m *Mouse) ensureDebugOverlay() {
	_, _ = m.boundPage().Eval(`() => {
		if (window.__humanizeCanvas) return;
		const canvas = document.createElement('canvas');
		canvas.id = '__humanize_mouse_trace';
		canvas.width = window.innerWidth;
		canvas.height = window.innerHeight;
		canvas.style.cssText = 'position:fixed;top:0;left:0;pointer-events:none;z-index:2147483647;';
		document.body.appendChild(canvas);
		window.__humanizeCanvas = canvas;
		window.__humanizeCtx = canvas.getContext('2d');
		window.addEventListener('resize', () => {
			canvas.width = window.innerWidth;
			canvas.height = window.innerHeight;
		});
	}`)
}

// tracePoint draws a dot on the debug overlay at (x, y).
func (m *Mouse) tracePoint(x, y float64, first bool) error {
	_, err := m.boundPage().Eval(`(x, y, first) => {
		const ctx = window.__humanizeCtx;
		if (!ctx) return;
		ctx.fillStyle = first ? 'rgba(0, 255, 0, 0.8)' : 'rgba(255, 0, 0, 0.5)';
		ctx.beginPath();
		ctx.arc(x, y, first ? 5 : 3, 0, Math.PI * 2);
		ctx.fill();
	}`, x, y, first)
	return err
}

func elementTarget(el *rod.Element) (Point, error) {
	shape, err := el.Shape()
	if err != nil {
		return Point{}, err
	}
	if len(shape.Quads) == 0 {
		return Point{}, errors.New("element has no content quads")
	}
	q := shape.Quads[0]

	// Compute the bounding box of the quad to handle arbitrary vertex order.
	var minX, maxX, minY, maxY float64
	for i := 0; i < q.Len(); i++ {
		x := q[i*2]
		y := q[i*2+1]
		if i == 0 || x < minX {
			minX = x
		}
		if i == 0 || x > maxX {
			maxX = x
		}
		if i == 0 || y < minY {
			minY = y
		}
		if i == 0 || y > maxY {
			maxY = y
		}
	}
	center := Point{
		X: (minX + maxX) / 2,
		Y: (minY + maxY) / 2,
	}
	width := maxX - minX
	height := maxY - minY

	// CDP DOM.getContentQuads returns coordinates relative to the viewport, and
	// rod.Mouse.MoveTo also expects viewport-relative coordinates, so no scroll
	// offset conversion is needed.
	// Random offset within central 60% of element.
	return Point{
		X: center.X + width*(rand.Float64()*0.3-0.15),
		Y: center.Y + height*(rand.Float64()*0.3-0.15),
	}, nil
}

func jitter(p Point, radius float64) Point {
	angle := rand.Float64() * 2 * math.Pi
	d := rand.Float64() * radius
	return Point{
		X: p.X + math.Cos(angle)*d,
		Y: p.Y + math.Sin(angle)*d,
	}
}

func randomSign() float64 {
	if rand.Intn(2) == 0 {
		return -1
	}
	return 1
}

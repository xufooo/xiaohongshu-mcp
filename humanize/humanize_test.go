package humanize

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/stretchr/testify/assert"
)

func TestLogNormal_sample(t *testing.T) {
	d := LogNormal{Mu: 0, Sigma: 0.5, Min: 100 * time.Millisecond, Max: 10 * time.Second}

	assert.Equal(t, time.Second, d.sample(0))

	assert.Less(t, d.sample(-1), d.sample(0))
	assert.Less(t, d.sample(0), d.sample(1))
}

func TestLogNormal_clamp(t *testing.T) {
	d := LogNormal{Mu: 0, Sigma: 1, Min: 500 * time.Millisecond, Max: 2 * time.Second}

	assert.Equal(t, d.Min, d.sample(-100))
	assert.Equal(t, d.Max, d.sample(100))
}

func TestLogNormal_noMax(t *testing.T) {
	d := LogNormal{Mu: 0, Sigma: 1, Min: 0, Max: 0}
	assert.Greater(t, d.sample(3), 15*time.Second)
}

func TestDefaultProvider_Timing(t *testing.T) {
	tp := DefaultProvider{}.Timing()

	for _, action := range []Action{AfterClick, AfterType, AfterNavigate, BetweenScroll, BeforeSubmit, BeforeClick, Reading, Keystroke, ClickHold, AfterInteract, PointerSettle} {
		dist, ok := tp[action]
		assert.True(t, ok, "缺少动作 %s 的时延分布", action)
		assert.Greater(t, dist.Max, dist.Min, "%s: Max 应大于 Min", action)

		mid := dist.sample(0)
		assert.GreaterOrEqual(t, mid, dist.Min, "%s: 中位样本不应小于 Min", action)
		assert.LessOrEqual(t, mid, dist.Max, "%s: 中位样本不应大于 Max", action)
	}
}

func TestDelay_RespectsContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	Delay(ctx, AfterNavigate)
	assert.Less(t, time.Since(start), 100*time.Millisecond, "已取消的 ctx 应让 Delay 立即返回")
}

func TestActorMouseKeyboardShareState(t *testing.T) {
	actor := New(&rod.Page{}, Config{})
	if actor.Mouse.state != actor.Keyboard.state {
		t.Fatal("Mouse 与 Keyboard 应共享同一 inputState")
	}
}

func TestKeyboardOwnsStateWhenNoMouse(t *testing.T) {
	kb := NewKeyboard(&rod.Page{}, Config{}, nil)
	if kb.state == nil {
		t.Fatal("无 Mouse 的 Keyboard 应有自己的 inputState")
	}
	kb.state.mu.Lock()
	kb.state.pressed[input.ShiftLeft] = true
	kb.state.mu.Unlock()
	if kb.state.modifiers()&input.ModifierShift == 0 {
		t.Fatal("无 Mouse 的 Keyboard modifiers 应正常工作")
	}
}

func TestInputStateModifiers(t *testing.T) {
	state := newInputState()
	if state.modifiers() != 0 {
		t.Fatalf("无按键时 modifiers 应为 0，得到 %d", state.modifiers())
	}
	state.mu.Lock()
	state.pressed[input.ShiftLeft] = true
	state.mu.Unlock()
	if state.modifiers()&input.ModifierShift == 0 {
		t.Fatalf("Shift 按下后 modifiers 应含 Shift 位")
	}
}

func TestBoundPageInheritsCanceledContext(t *testing.T) {
	m := NewMouse(&rod.Page{}, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.setContext(ctx)

	if m.ctx != ctx {
		t.Fatal("setContext 应保存调用方 ctx")
	}
	if err := m.ctx.Err(); err == nil {
		t.Fatal("已取消的 ctx 应立即可见")
	}
}

func TestCubicBezier_Endpoints(t *testing.T) {
	p0 := proto.Point{X: 0, Y: 0}
	p1 := proto.Point{X: 10, Y: 50}
	p2 := proto.Point{X: 90, Y: 50}
	p3 := proto.Point{X: 100, Y: 0}

	assert.Equal(t, p0, cubicBezier(p0, p1, p2, p3, 0))
	assert.Equal(t, p3, cubicBezier(p0, p1, p2, p3, 1))

	mid := cubicBezier(p0, p1, p2, p3, 0.5)
	assert.Greater(t, mid.X, 0.0)
	assert.Less(t, mid.X, 100.0)
}

func TestEaseInOut(t *testing.T) {
	assert.Equal(t, 0.0, easeInOut(0))
	assert.Equal(t, 1.0, easeInOut(1))
	assert.InDelta(t, 0.5, easeInOut(0.5), 1e-9)
	assert.Less(t, easeInOut(0.25), easeInOut(0.75))
}

func TestJitterOffset_Bounds(t *testing.T) {
	cases := []struct {
		size  float64
		limit float64
		desc  string
	}{
		{size: 20, limit: 3, desc: "小元素按比例取"},
		{size: 600, limit: 8, desc: "宽元素封顶"},
		{size: -40, limit: 6, desc: "负边长按绝对值处理"},
		{size: 0, limit: 0, desc: "零边长不偏移"},
	}

	for _, c := range cases {
		for i := 0; i < 200; i++ {
			got := jitterOffset(c.size)
			assert.LessOrEqual(t, math.Abs(got), c.limit, "%s: 偏移 %v 超出上限 %v", c.desc, got, c.limit)
		}
	}
}

func TestJitterInQuad_StaysInside(t *testing.T) {
	q := proto.DOMQuad{10, 20, 110, 20, 110, 60, 10, 60}
	center := proto.Point{X: 60, Y: 40}

	for i := 0; i < 500; i++ {
		p := jitterInQuad(center, q)
		assert.GreaterOrEqual(t, p.X, 10.0)
		assert.LessOrEqual(t, p.X, 110.0)
		assert.GreaterOrEqual(t, p.Y, 20.0)
		assert.LessOrEqual(t, p.Y, 60.0)
	}
}

func TestDelay_UnknownActionFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.NotPanics(t, func() {
		Delay(ctx, Action("nonexistent"))
	})
}

func TestPointerSettle_HasSufficientFloor(t *testing.T) {
	dist := DefaultProvider{}.Timing()[PointerSettle]

	assert.GreaterOrEqual(t, dist.Min, 200*time.Millisecond,
		"PointerSettle 的下限不应低于 200ms")

	assert.GreaterOrEqual(t, dist.sample(-5), 200*time.Millisecond,
		"极端偏小的采样也应被 clamp 到 200ms 以上")
}

// TestCoverDismissJSShape 钉住"关掉盖住目标的浮层"的判据：
// 只挑**视口内可见**的关闭控件（实测引导浮层的"我知道了"落在视口外 y=509/视口高 459），
// 且优先用 aria-label 明确写着"关闭"的那个。
func TestCoverDismissJSShape(t *testing.T) {
	for _, want := range []string{
		`[aria-label*="关闭"]`,
		"inViewport",
		"b.bottom <= innerHeight",
		`removeAttribute("data-xhs-mcp-cover-close")`,
		`setAttribute("data-xhs-mcp-cover-close", "1")`,
	} {
		if !strings.Contains(coverDismissJS, want) {
			t.Fatalf("关闭浮层的探针缺少 %q", want)
		}
	}
	// aria-label 必须排在泛化的 [class*=close] 之前，否则会点到无关的隐藏 ×。
	if strings.Index(coverDismissJS, "aria-label") > strings.Index(coverDismissJS, `[class*="close"]`) {
		t.Fatal("关闭控件优先级不对：应先按 aria-label 找")
	}
}

type canceledPressClient struct {
	events      chan *cdp.Event
	cancel      context.CancelFunc
	presses     int
	releases    int
	failRelease bool
	releaseCtx  context.Context
}

func (c *canceledPressClient) Event() <-chan *cdp.Event { return c.events }

func (c *canceledPressClient) Call(ctx context.Context, _, method string, params interface{}) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if method == "Target.attachToTarget" {
		return []byte(`{"sessionId":"mouse-session"}`), nil
	}
	if method == "Input.dispatchMouseEvent" {
		request := params.(proto.InputDispatchMouseEvent)
		if request.Type == proto.InputDispatchMouseEventTypeMousePressed {
			c.presses++
			c.cancel()
		}
		if request.Type == proto.InputDispatchMouseEventTypeMouseReleased {
			c.releases++
			c.releaseCtx = ctx
			if c.failRelease {
				return nil, errors.New("release disconnected")
			}
		}
	}
	return []byte(`{}`), nil
}

func TestClickCancellationReleasesOrDisablesSharedInput(t *testing.T) {
	for _, failRelease := range []bool{false, true} {
		name := "release_confirmed"
		if failRelease {
			name = "release_failed"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := &canceledPressClient{events: make(chan *cdp.Event), cancel: cancel, failRelease: failRelease}
			browserCtx, stop := context.WithCancel(context.Background())
			defer stop()
			browser := rod.New().Context(browserCtx).NoDefaultDevice().ControlURL("").Client(client)
			if err := browser.Connect(); err != nil {
				t.Fatal(err)
			}
			browserEvents := browser.Event()
			defer func() {
				close(client.events)
				select {
				case <-browserEvents:
				case <-time.After(time.Second):
					t.Error("mouse fixture event goroutine did not exit")
				}
			}()
			page, err := browser.PageFromTarget("mouse-target")
			if err != nil {
				t.Fatal(err)
			}
			actor := NewWithContext(page, Config{}, ctx)
			err = actor.Mouse.clickButton(proto.InputMouseButtonLeft, 1)
			if !errors.Is(err, context.Canceled) || client.presses != 1 || client.releases != 1 {
				t.Fatalf("click cancellation: presses=%d releases=%d err=%v", client.presses, client.releases, err)
			}
			if deadline, ok := client.releaseCtx.Deadline(); !ok || time.Until(deadline) > 2*time.Second {
				t.Fatal("release cleanup has no bounded context")
			}
			if failRelease {
				actor.SetContext(context.Background())
				if err := actor.Mouse.dispatchMouseScroll(0, 1); err == nil {
					t.Fatal("uncertain button state allowed scrolling")
				}
				if err := actor.Keyboard.Press(input.Key('a')); err == nil {
					t.Fatal("uncertain button state allowed keyboard input")
				}
			} else if len(actor.state.buttons) != 0 {
				t.Fatal("successful release retained pressed button")
			}
		})
	}
}

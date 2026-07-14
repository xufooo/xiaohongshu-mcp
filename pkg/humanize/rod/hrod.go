// Package hrod provides a thin humanized wrapper around go-rod.
//
// It keeps the go-rod API familiar while transparently adding human-like
// mouse movements, scrolls, and keystrokes to the most common automation
// methods (Click, Input, MustClick, MustInput, etc.).
//
// The wrapper uses explicit composition instead of embedding *rod.Page and
// *rod.Element, so child lookup methods can keep the natural Element/MustElement
// names and always return humanized *hrod.Element values.
//
// For methods that are not explicitly wrapped, access the underlying rod value
// via the exported Rod field: page.Rod.XXX() or el.Rod.XXX().
package hrod

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/xpzouying/headless_browser"
	"github.com/xpzouying/xiaohongshu-mcp/pkg/humanize"
	"github.com/ysmood/gson"
)

// Browser wraps a *headless_browser.Browser and humanizes the pages it creates.
type Browser struct {
	hb  *headless_browser.Browser
	cfg humanize.Config
}

const browserCloseTimeout = 10 * time.Second

// NewBrowser wraps an existing *headless_browser.Browser.
func NewBrowser(hb *headless_browser.Browser, cfg humanize.Config) *Browser {
	return &Browser{hb: hb, cfg: cfg}
}

// Rod returns the underlying *headless_browser.Browser.
func (b *Browser) Rod() *headless_browser.Browser {
	return b.hb
}

// Close closes the underlying browser.
func (b *Browser) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), browserCloseTimeout)
	defer cancel()
	return b.hb.CloseContext(ctx)
}

// Health 检查底层浏览器的 CDP 连接。
func (b *Browser) Health(ctx context.Context) error {
	return b.hb.Health(ctx)
}

// MustClose is the humanized version of MustClose.
func (b *Browser) MustClose() *Browser {
	_ = b.Close()
	return b
}

// Page creates a humanized page.
func (b *Browser) Page() (*Page, error) {
	page, err := b.hb.Page()
	if err != nil {
		return nil, err
	}
	return b.wrapPage(page), nil
}

// NewPage creates a humanized page.
func (b *Browser) NewPage() *Page {
	page, err := b.Page()
	if err != nil {
		panic(err)
	}
	return page
}

// MustPage opens a humanized page.
func (b *Browser) MustPage(url string) *Page {
	p := b.hb.NewPage()
	if url != "" {
		p.MustNavigate(url)
	}
	return b.wrapPage(p)
}

func (b *Browser) wrapPage(p *rod.Page) *Page {
	if p == nil {
		return nil
	}
	page := &Page{
		Rod:      p,
		Mouse:    p.Mouse,
		Keyboard: p.Keyboard,
		actor:    humanize.New(p, b.cfg),
		browser:  b,
		cfg:      b.cfg,
		ctx:      context.Background(),
	}
	return page
}

// Page wraps a *rod.Page and adds humanized versions of common actions.
// Access the underlying *rod.Page through the exported Rod field when a method
// is not explicitly wrapped.
type Page struct {
	Rod      *rod.Page
	Mouse    *rod.Mouse
	Keyboard *rod.Keyboard
	actor    *humanize.Actor
	browser  *Browser
	cfg      humanize.Config
	ctx      context.Context
}

// Actor exposes the underlying humanize actor for advanced use.
func (p *Page) Actor() *humanize.Actor {
	return p.actor
}

// Browser returns the wrapping humanized browser.
func (p *Page) Browser() *Browser {
	return p.browser
}

func (p *Page) wrapPage(rp *rod.Page) *Page {
	if rp == nil {
		return nil
	}
	return &Page{
		Rod:      rp,
		Mouse:    rp.Mouse,
		Keyboard: rp.Keyboard,
		actor:    p.actor,
		browser:  p.browser,
		cfg:      p.cfg,
		ctx:      p.ctx,
	}
}

// Close closes the page.
func (p *Page) Close() error {
	return p.Rod.Close()
}

// Context returns a humanized clone with the specified context.
func (p *Page) Context(ctx context.Context) *Page {
	page := p.wrapPage(p.Rod.Context(ctx))
	page.ctx = ctx
	page.actor.SetContext(ctx)
	return page
}

// Timeout returns a humanized clone with the specified timeout.
func (p *Page) Timeout(d time.Duration) *Page {
	page := p.wrapPage(p.Rod.Timeout(d))
	page.ctx, _ = context.WithTimeout(p.ctx, d)
	page.actor.SetContext(page.ctx)
	return page
}

// CancelTimeout returns a humanized clone with the timeout cancelled.
func (p *Page) CancelTimeout() *Page {
	return p.wrapPage(p.Rod.CancelTimeout())
}

// Sleep waits for d, or returns immediately when this page's context is cancelled.
func (p *Page) Sleep(d time.Duration) error {
	return humanize.SleepContext(p.ctx, d, d)
}

// SleepRandom waits for a random duration in [min, max], or returns when cancelled.
func (p *Page) SleepRandom(min, max time.Duration) error {
	return humanize.SleepContext(p.ctx, min, max)
}

// Err returns nil if the page's context is still active, or the context error if cancelled.
func (p *Page) Err() error {
	return p.ctx.Err()
}

// Navigate navigates to the URL.
func (p *Page) Navigate(url string) error {
	return p.Rod.Navigate(url)
}

// MustNavigate is the humanized MustNavigate.
func (p *Page) MustNavigate(url string) *Page {
	p.Rod.MustNavigate(url)
	return p
}

// Has checks if an element exists.
func (p *Page) Has(selector string) (bool, *Element, error) {
	found, el, err := p.Rod.Has(selector)
	if err != nil || !found {
		return found, nil, err
	}
	return true, p.wrapElement(el), nil
}

// Element finds an element and returns a humanized wrapper.
func (p *Page) Element(selector string) (*Element, error) {
	el, err := p.Rod.Element(selector)
	if err != nil {
		return nil, err
	}
	return p.wrapElement(el), nil
}

// MustElement finds an element and returns a humanized wrapper.
func (p *Page) MustElement(selector string) *Element {
	return p.wrapElement(p.Rod.MustElement(selector))
}

// ElementR finds an element by regex and returns a humanized wrapper.
func (p *Page) ElementR(selector, regex string) (*Element, error) {
	el, err := p.Rod.ElementR(selector, regex)
	if err != nil {
		return nil, err
	}
	return p.wrapElement(el), nil
}

// MustElementR finds an element by regex and returns a humanized wrapper.
func (p *Page) MustElementR(selector, regex string) *Element {
	return p.wrapElement(p.Rod.MustElementR(selector, regex))
}

// ElementX finds an element by XPath and returns a humanized wrapper.
func (p *Page) ElementX(xpath string) (*Element, error) {
	el, err := p.Rod.ElementX(xpath)
	if err != nil {
		return nil, err
	}
	return p.wrapElement(el), nil
}

// MustElementX finds an element by XPath and returns a humanized wrapper.
func (p *Page) MustElementX(xpath string) *Element {
	return p.wrapElement(p.Rod.MustElementX(xpath))
}

// Elements returns humanized elements.
func (p *Page) Elements(selector string) ([]*Element, error) {
	els, err := p.Rod.Elements(selector)
	if err != nil {
		return nil, err
	}
	result := make([]*Element, len(els))
	for i, el := range els {
		result[i] = p.wrapElement(el)
	}
	return result, nil
}

// MustElements returns humanized elements.
func (p *Page) MustElements(selector string) []*Element {
	els := p.Rod.MustElements(selector)
	result := make([]*Element, len(els))
	for i, el := range els {
		result[i] = p.wrapElement(el)
	}
	return result
}

// Eval evaluates JS on the page.
func (p *Page) Eval(js string, params ...interface{}) (*proto.RuntimeRemoteObject, error) {
	return p.Rod.Eval(js, params...)
}

// MustEval is the humanized MustEval.
func (p *Page) MustEval(js string, params ...interface{}) gson.JSON {
	return p.Rod.MustEval(js, params...)
}

// Wait waits until the JS expression returns true.
func (p *Page) Wait(opts *rod.EvalOptions) error {
	return p.Rod.Wait(opts)
}

// MustWait is the humanized MustWait.
func (p *Page) MustWait(js string, params ...interface{}) *Page {
	p.Rod.MustWait(js, params...)
	return p
}

// WaitLoad waits for the page load event.
func (p *Page) WaitLoad() error {
	return p.Rod.WaitLoad()
}

// MustWaitLoad is the humanized MustWaitLoad.
func (p *Page) MustWaitLoad() *Page {
	p.Rod.MustWaitLoad()
	return p
}

// WaitStable waits for the page to be stable.
func (p *Page) WaitStable(d time.Duration) error {
	return p.Rod.WaitStable(d)
}

// MustWaitStable is the humanized MustWaitStable.
func (p *Page) MustWaitStable() *Page {
	p.Rod.MustWaitStable()
	return p
}

// WaitDOMStable waits for the DOM to be stable.
func (p *Page) WaitDOMStable(d time.Duration, diff float64) error {
	return p.Rod.WaitDOMStable(d, diff)
}

// MustWaitDOMStable is the humanized MustWaitDOMStable.
func (p *Page) MustWaitDOMStable() *Page {
	p.Rod.MustWaitDOMStable()
	return p
}

// MustClick clicks the element matched by selector with human-like movement.
// It panics if the element cannot be found or clicked.
func (p *Page) MustClick(selector string) *Page {
	el, err := p.Element(selector)
	if err != nil {
		panic(err)
	}
	if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
		panic(err)
	}
	return p
}

// MustInput focuses the element and types text with human-like timing.
// It panics if the element cannot be found or the text cannot be entered.
func (p *Page) MustInput(selector string, text string) *Page {
	el, err := p.Element(selector)
	if err != nil {
		panic(err)
	}
	if err := el.Input(text); err != nil {
		panic(err)
	}
	return p
}

// MustScroll scrolls vertically by the given amount in a human-like way.
// It panics if the scroll cannot be performed.
func (p *Page) MustScroll(vertical float64) *Page {
	if err := p.actor.Mouse.Scroll(0, vertical); err != nil {
		panic(err)
	}
	return p
}

func (p *Page) wrapElement(el *rod.Element) *Element {
	if el == nil {
		return nil
	}
	return newElement(el, p.actor, p.browser)
}

// NewElement creates a humanized element from a raw *rod.Element.
func NewElement(el *rod.Element, actor *humanize.Actor) *Element {
	return newElement(el, actor, nil)
}

func newElement(el *rod.Element, actor *humanize.Actor, browser *Browser) *Element {
	if el == nil {
		return nil
	}
	return &Element{Rod: el, actor: actor, browser: browser}
}

// Element wraps a *rod.Element and adds humanized Click/Input methods.
// Access the underlying *rod.Element through the exported Rod field when a method
// is not explicitly wrapped.
type Element struct {
	Rod     *rod.Element
	actor   *humanize.Actor
	browser *Browser
}

// Actor exposes the underlying humanize actor for advanced use.
func (el *Element) Actor() *humanize.Actor {
	return el.actor
}

// Sleep waits for d, or returns immediately when the element's actor context is cancelled.
func (el *Element) Sleep(d time.Duration) error {
	return el.actor.Sleep(d)
}

// Page returns the wrapping humanized page.
func (el *Element) Page() *Page {
	rp := el.Rod.Page()
	return &Page{
		Rod:      rp,
		Mouse:    rp.Mouse,
		Keyboard: rp.Keyboard,
		actor:    el.actor,
		browser:  el.browser,
		cfg:      el.actor.Config(),
		ctx:      el.actor.Ctx(),
	}
}

// Click performs a human-like click.
func (el *Element) Click(button proto.InputMouseButton, clickCount int) error {
	if err := el.waitInteractable(5*time.Second, true); err != nil {
		return err
	}
	return el.actor.Mouse.ClickWithOptions(el.Rod, button, clickCount)
}

// ClickNoScroll performs a human-like click without scrolling the element into
// view first. Useful for sticky/fixed elements that are already visible.
func (el *Element) ClickNoScroll() error {
	if err := el.waitInteractable(5*time.Second, false); err != nil {
		return err
	}
	return el.actor.Mouse.ClickNoScroll(el.Rod)
}

// ClickPoint moves to a viewport-relative point and clicks there.
func (p *Page) ClickPoint(point proto.Point) error {
	return p.actor.Mouse.ClickPoint(point)
}

// MovePoint moves to a viewport-relative point.
func (p *Page) MovePoint(point proto.Point) error {
	return p.actor.Mouse.MovePoint(point)
}

// MustClick is the humanized MustClick.
// It panics if the element cannot be clicked.
func (el *Element) MustClick() *Element {
	if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
		panic(err)
	}
	return el
}

// Input performs human-like typing.
func (el *Element) Input(text string) error {
	return el.actor.Keyboard.Type(el.Rod, text)
}

// MustInput is the humanized MustInput.
// It panics if the text cannot be entered.
func (el *Element) MustInput(text string) *Element {
	if err := el.Input(text); err != nil {
		panic(err)
	}
	return el
}

// Hover moves the cursor over the element in a human-like way.
func (el *Element) Hover() error {
	return el.actor.Mouse.Hover(el.Rod)
}

// MustHover is the humanized MustHover.
// It panics if the cursor cannot be moved over the element.
func (el *Element) MustHover() *Element {
	if err := el.Hover(); err != nil {
		panic(err)
	}
	return el
}

// ScrollIntoView scrolls the element into view in a human-like way by
// dispatching wheel events until the element is centered in the viewport.
func (el *Element) ScrollIntoView() error {
	return el.actor.Mouse.ScrollIntoView(el.Rod)
}

// MustScrollIntoView is the humanized MustScrollIntoView.
// It panics if the element cannot be scrolled into view.
func (el *Element) MustScrollIntoView() *Element {
	if err := el.ScrollIntoView(); err != nil {
		panic(err)
	}
	return el
}

type interactableSnapshot struct {
	Connected bool    `json:"connected"`
	Visible   bool    `json:"visible"`
	Clickable bool    `json:"clickable"`
	Left      float64 `json:"left"`
	Top       float64 `json:"top"`
	Width     float64 `json:"width"`
	Height    float64 `json:"height"`
	Reason    string  `json:"reason"`

	// 现场证据（obscured 时填充，单次 Eval 收集）
	TargetTag         string  `json:"targetTag,omitempty"`
	TargetID          string  `json:"targetId,omitempty"`
	TargetClass       string  `json:"targetClass,omitempty"`
	TargetPlaceholder string  `json:"targetPlaceholder,omitempty"`
	TargetCX          float64 `json:"targetCx,omitempty"`
	TargetCY          float64 `json:"targetCy,omitempty"`
	HitTag            string  `json:"hitTag,omitempty"`
	HitID             string  `json:"hitId,omitempty"`
	HitClass          string  `json:"hitClass,omitempty"`
	HitText           string  `json:"hitText,omitempty"`
	HitLeft           float64 `json:"hitLeft,omitempty"`
	HitTop            float64 `json:"hitTop,omitempty"`
	HitWidth          float64 `json:"hitWidth,omitempty"`
	HitHeight         float64 `json:"hitHeight,omitempty"`
	TargetContainsHit bool    `json:"targetContainsHit"`
	URL               string  `json:"url,omitempty"`
	Title             string  `json:"title,omitempty"`
}

// WaitInteractable 等待元素进入可点击状态。页面频繁重排时会重新测量，避免点到旧位置。
func (el *Element) WaitInteractable(timeout time.Duration) error {
	return el.waitInteractable(timeout, true)
}

func (el *Element) waitInteractable(timeout time.Duration, scroll bool) error {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)
	var last interactableSnapshot
	var lastErr error

	for attempt := 0; time.Now().Before(deadline); attempt++ {
		if scroll {
			if err := el.ScrollIntoView(); err != nil {
				lastErr = err
				_ = el.actor.Sleep(120 * time.Millisecond)
				continue
			}
		}

		first, err := el.interactableSnapshot()
		if err != nil {
			lastErr = err
			_ = el.actor.Sleep(120 * time.Millisecond)
			continue
		}
		last = first
		if !first.Connected || !first.Visible || !first.Clickable {
			lastErr = fmt.Errorf("元素不可点击: %s%s", first.Reason, first.evidenceString())
			_ = el.actor.Sleep(120 * time.Millisecond)
			continue
		}

		// DOM 变化会导致位置漂移。短暂停顿后重新测量，确认中心点稳定。
		if err := el.actor.Sleep(160 * time.Millisecond); err != nil {
			return err
		}
		second, err := el.interactableSnapshot()
		if err != nil {
			lastErr = err
			continue
		}
		last = second
		if second.Connected && second.Visible && second.Clickable && snapshotStable(first, second) {
			return nil
		}
		lastErr = fmt.Errorf("元素尚未稳定: %s%s", second.Reason, second.evidenceString())

		if attempt%2 == 1 {
			_ = el.actor.Sleep(220 * time.Millisecond)
		}
	}

	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("等待元素可点击超时: visible=%v clickable=%v stable=false reason=%s%s", last.Visible, last.Clickable, last.Reason, last.evidenceString())
}

func (el *Element) interactableSnapshot() (interactableSnapshot, error) {
	result, err := el.Eval(`() => {
		const rect = this.getBoundingClientRect();
		const style = window.getComputedStyle(this);
		const connected = this.isConnected;
		const visible = connected &&
			style.visibility !== "hidden" &&
			style.display !== "none" &&
			Number(style.opacity || "1") > 0 &&
			rect.width > 1 && rect.height > 1 &&
			rect.bottom > 0 && rect.right > 0 &&
			rect.top < window.innerHeight && rect.left < window.innerWidth;
		const x = Math.min(Math.max(rect.left + rect.width / 2, 1), window.innerWidth - 1);
		const y = Math.min(Math.max(rect.top + rect.height / 2, 1), window.innerHeight - 1);
		const hit = visible ? document.elementFromPoint(x, y) : null;
		const clickable = visible && hit && (hit === this || this.contains(hit));
		let reason = "";
		if (!connected) reason = "detached";
		else if (!visible) reason = "not_visible";
		else if (!clickable) reason = "obscured";
		const r = { connected, visible, clickable, left: rect.left, top: rect.top, width: rect.width, height: rect.height, reason };
		if (reason === "obscured") {
			const hitRect = hit ? hit.getBoundingClientRect() : null;
			const sanitize = (s, n) => {
				s = String(s || "");
				s = s.replace(/[\x00-\x1F\x7F\u0080-\u009F\u061C\u200B-\u200F\u2028-\u202F\u2066-\u2069\uFEFF]/g, "");
				return s.slice(0, n);
			};
			const cls = (s) => {
				if (s && typeof s === "object" && "baseVal" in s) s = s.baseVal;
				return sanitize(String(s || "").replace(/\s+/g, " ").trim(), 80);
			};
			r.targetTag = this.tagName || "";
			r.targetId = sanitize(this.id, 40);
			r.targetClass = cls(this.className);
			r.targetPlaceholder = sanitize(this.getAttribute("placeholder"), 40);
			r.targetCx = x;
			r.targetCy = y;
			r.hitTag = hit ? hit.tagName || "" : "";
			r.hitId = hit ? sanitize(hit.id, 40) : "";
			r.hitClass = hit ? cls(hit.className) : "";
			r.hitText = hit ? sanitize((hit.innerText || hit.textContent || "").replace(/\s+/g, " ").trim(), 60) : "";
			r.hitLeft = hitRect ? hitRect.left : 0;
			r.hitTop = hitRect ? hitRect.top : 0;
			r.hitWidth = hitRect ? hitRect.width : 0;
			r.hitHeight = hitRect ? hitRect.height : 0;
			r.targetContainsHit = hit ? (hit === this || this.contains(hit)) : false;
			r.url = location.origin + location.pathname;
			r.title = sanitize(document.title || "", 120);
		}
		return JSON.stringify(r);
	}`)
	if err != nil {
		return interactableSnapshot{}, err
	}
	if result == nil {
		return interactableSnapshot{}, fmt.Errorf("元素可点击性检查无结果")
	}
	var snapshot interactableSnapshot
	if err := json.Unmarshal([]byte(result.Value.Str()), &snapshot); err != nil {
		return interactableSnapshot{}, err
	}
	return snapshot, nil
}

func snapshotStable(a, b interactableSnapshot) bool {
	const tolerance = 1.5
	return absFloat(a.Left-b.Left) <= tolerance &&
		absFloat(a.Top-b.Top) <= tolerance &&
		absFloat(a.Width-b.Width) <= tolerance &&
		absFloat(a.Height-b.Height) <= tolerance
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

const maxErrorBytes = 2048
const prefixReserve = 120
const maxEvidenceBytes = maxErrorBytes - prefixReserve

func truncate(s string, n int) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if !isDiscardRune(r) {
			b.WriteRune(r)
		}
	}
	s = b.String()
	if len(s) > n {
		s = s[:n]
	}
	return s
}

func isDiscardRune(r rune) bool {
	return r < 32 || r == 127 ||
		(r >= 0x80 && r <= 0x9F) ||
		r == 0x061C ||
		(r >= 0x200B && r <= 0x200F) ||
		(r >= 0x2028 && r <= 0x202F) ||
		(r >= 0x2066 && r <= 0x2069) ||
		r == 0xFEFF
}

func sanitizeURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.RawQuery = ""
	u.Fragment = ""
	u.User = nil
	return u.Scheme + "://" + u.Host + u.EscapedPath()
}

// evidenceString 返回 obscured 时紧凑 JSON 现场证据，非 obscured 返回空串。
// 输出严格 <= maxEvidenceBytes 字节，清理全部控制字符，URL 剥离 query/fragment/userinfo。
func (s interactableSnapshot) evidenceString() string {
	if s.Reason != "obscured" {
		return ""
	}
	ev := map[string]interface{}{
		"placeholder":  truncate(s.TargetPlaceholder, 40),
		"targetTag":    truncate(s.TargetTag, 20),
		"targetId":     truncate(s.TargetID, 40),
		"targetClass":  truncate(s.TargetClass, 80),
		"targetCx":     s.TargetCX,
		"targetCy":     s.TargetCY,
		"targetLeft":   s.Left,
		"targetTop":    s.Top,
		"targetWidth":  s.Width,
		"targetHeight": s.Height,
		"hitTag":       truncate(s.HitTag, 20),
		"hitId":        truncate(s.HitID, 40),
		"hitClass":     truncate(s.HitClass, 80),
		"hitText":      truncate(s.HitText, 60),
		"hitLeft":      s.HitLeft,
		"hitTop":       s.HitTop,
		"hitWidth":     s.HitWidth,
		"hitHeight":    s.HitHeight,
		"contains":     s.TargetContainsHit,
		"url":          truncate(sanitizeURL(s.URL), 200),
		"title":        truncate(s.Title, 120),
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return ""
	}
	if len(b) <= maxEvidenceBytes {
		return string(b)
	}
	// 超出 2048 时使用最小 fallback，仍包含 target/hit/contains/center 与双方 rect
	fallback := map[string]interface{}{
		"targetTag":    truncate(s.TargetTag, 10),
		"hitTag":       truncate(s.HitTag, 10),
		"url":          truncate(sanitizeURL(s.URL), 100),
		"title":        truncate(s.Title, 50),
		"contains":     s.TargetContainsHit,
		"targetCx":     s.TargetCX,
		"targetCy":     s.TargetCY,
		"targetLeft":   s.Left,
		"targetTop":    s.Top,
		"targetWidth":  s.Width,
		"targetHeight": s.Height,
		"hitLeft":      s.HitLeft,
		"hitTop":       s.HitTop,
		"hitWidth":     s.HitWidth,
		"hitHeight":    s.HitHeight,
	}
	b, _ = json.Marshal(fallback)
	return string(b)
}

// Element finds a child element and returns a humanized wrapper.
func (el *Element) Element(selector string) (*Element, error) {
	child, err := el.Rod.Element(selector)
	if err != nil {
		return nil, err
	}
	return newElement(child, el.actor, el.browser), nil
}

// MustElement finds a child element and returns a humanized wrapper.
func (el *Element) MustElement(selector string) *Element {
	return newElement(el.Rod.MustElement(selector), el.actor, el.browser)
}

// ElementR finds a child element by regex and returns a humanized wrapper.
func (el *Element) ElementR(selector, regex string) (*Element, error) {
	child, err := el.Rod.ElementR(selector, regex)
	if err != nil {
		return nil, err
	}
	return newElement(child, el.actor, el.browser), nil
}

// MustElementR finds a child element by regex and returns a humanized wrapper.
func (el *Element) MustElementR(selector, regex string) *Element {
	return newElement(el.Rod.MustElementR(selector, regex), el.actor, el.browser)
}

// ElementX finds a child element by XPath and returns a humanized wrapper.
func (el *Element) ElementX(xpath string) (*Element, error) {
	child, err := el.Rod.ElementX(xpath)
	if err != nil {
		return nil, err
	}
	return newElement(child, el.actor, el.browser), nil
}

// MustElementX finds a child element by XPath and returns a humanized wrapper.
func (el *Element) MustElementX(xpath string) *Element {
	return newElement(el.Rod.MustElementX(xpath), el.actor, el.browser)
}

// Elements returns humanized child elements.
func (el *Element) Elements(selector string) ([]*Element, error) {
	children, err := el.Rod.Elements(selector)
	if err != nil {
		return nil, err
	}
	result := make([]*Element, len(children))
	for i, child := range children {
		result[i] = newElement(child, el.actor, el.browser)
	}
	return result, nil
}

// MustElements returns humanized child elements.
func (el *Element) MustElements(selector string) []*Element {
	children := el.Rod.MustElements(selector)
	result := make([]*Element, len(children))
	for i, child := range children {
		result[i] = newElement(child, el.actor, el.browser)
	}
	return result
}

// Parent returns the humanized parent element.
func (el *Element) Parent() (*Element, error) {
	p, err := el.Rod.Parent()
	if err != nil {
		return nil, err
	}
	return newElement(p, el.actor, el.browser), nil
}

// Next returns the humanized next sibling element.
func (el *Element) Next() (*Element, error) {
	next, err := el.Rod.Next()
	if err != nil {
		return nil, err
	}
	return newElement(next, el.actor, el.browser), nil
}

// Previous returns the humanized previous sibling element.
func (el *Element) Previous() (*Element, error) {
	prev, err := el.Rod.Previous()
	if err != nil {
		return nil, err
	}
	return newElement(prev, el.actor, el.browser), nil
}

// Attribute returns the value of an attribute.
func (el *Element) Attribute(name string) (*string, error) {
	return el.Rod.Attribute(name)
}

// MustAttribute is the humanized MustAttribute.
func (el *Element) MustAttribute(name string) *string {
	return el.Rod.MustAttribute(name)
}

// Text returns the element text.
func (el *Element) Text() (string, error) {
	return el.Rod.Text()
}

// MustText is the humanized MustText.
func (el *Element) MustText() string {
	return el.Rod.MustText()
}

// Visible returns whether the element is visible.
func (el *Element) Visible() (bool, error) {
	return el.Rod.Visible()
}

// MustVisible is the humanized MustVisible.
func (el *Element) MustVisible() bool {
	return el.Rod.MustVisible()
}

// WaitVisible waits for the element to become visible.
func (el *Element) WaitVisible() error {
	return el.Rod.WaitVisible()
}

// MustWaitVisible is the humanized MustWaitVisible.
func (el *Element) MustWaitVisible() *Element {
	el.Rod.MustWaitVisible()
	return el
}

// Eval evaluates JS on the element.
func (el *Element) Eval(js string, params ...interface{}) (*proto.RuntimeRemoteObject, error) {
	return el.Rod.Eval(js, params...)
}

// MustEval is the humanized MustEval.
func (el *Element) MustEval(js string, params ...interface{}) gson.JSON {
	return el.Rod.MustEval(js, params...)
}

// Shape returns the element shape.
func (el *Element) Shape() (*proto.DOMGetContentQuadsResult, error) {
	return el.Rod.Shape()
}

// KeyActions returns key actions for the element.
func (el *Element) KeyActions() (*rod.KeyActions, error) {
	return el.Rod.KeyActions()
}

// SelectAllText selects all text in the element.
func (el *Element) SelectAllText() error {
	return el.Rod.SelectAllText()
}

// SetFiles sets files for a file input element.
func (el *Element) SetFiles(paths []string) error {
	return el.Rod.SetFiles(paths)
}

// MustSetFiles is the humanized MustSetFiles.
func (el *Element) MustSetFiles(paths ...string) *Element {
	el.Rod.MustSetFiles(paths...)
	return el
}

// Remove removes the element from the DOM.
func (el *Element) Remove() error {
	return el.Rod.Remove()
}

// MustRemove is the humanized MustRemove.
func (el *Element) MustRemove() *Element {
	el.Rod.MustRemove()
	return el
}

// Timeout returns a humanized clone with the specified timeout.
func (el *Element) Timeout(d time.Duration) *Element {
	return newElement(el.Rod.Timeout(d), el.actor, el.browser)
}

// Context returns a humanized clone with the specified context.
func (el *Element) Context(ctx context.Context) *Element {
	return newElement(el.Rod.Context(ctx), el.actor, el.browser)
}

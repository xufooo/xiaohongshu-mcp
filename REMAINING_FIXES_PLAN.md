Reading prompt from stdin...
OpenAI Codex v0.141.0
--------
workdir: /tmp/xiaohongshu-mcp-fork
model: gpt-5.5
provider: openai
approval: never
sandbox: read-only
reasoning effort: high
reasoning summaries: none
session id: 019eee13-da6f-7372-9a15-8aee6b1fa9af
--------
user
# 遗留问题修复方案

fixup 分支还有两个 P2 问题需要写修复方案。请阅读代码后，为每个问题输出具体的代码级修复方案（Markdown 格式）。

## 问题 1：xiaohongshu/ 下大量 time.Sleep 未响应 context 取消

请阅读 xiaohongshu/ 目录下的所有 .go 文件（publish.go, comment_feed.go, publish_video.go, feed_detail.go, like_favorite.go, login.go, feeds.go），分析：
- 哪些函数已经有 ctx context.Context 参数
- time.Sleep 的分布
- 可复用的封装模式（已经有一个 sleepRandom() 函数）

同时阅读 pkg/humanize/util.go（已有 sleepWithContext）和 service.go（page.Context(ctx) 调用模式）。

输出：最小侵入的替换方案，不改函数签名。

## 问题 2：hrod Page.wrapPage() 重建 actor 丢失鼠标/键盘状态

请阅读 pkg/humanize/rod/hrod.go 中：
- Page.wrapPage()（L110-123）：每次都 humanize.NewWithContext 创建新 actor
- Page.Context()（L131-136）：调用 wrapPage 后额外设置了 ctx 和 actor.SetContext
- Page.Timeout()（L139-141）：只调 wrapPage
- Browser.wrapPage()（L68-79）：做了 InitPosition()

同时阅读 pkg/humanize/humanize.go（Actor 结构体）
pkg/humanize/mouse.go（initialized 标志）
pkg/humanize/keyboard.go（lastEl 字段）

输出：复用 p.actor 而非重建的方案。

## 输出格式

请直接输出完整的 Markdown 内容，包含：
- 每个问题的根因分析
- 需要修改的文件、行号、代码片段
- 风险评估
- 优先级建议
warning: Codex could not find bubblewrap on PATH. Install bubblewrap with your OS package manager. See the sandbox prerequisites: https://developers.openai.com/codex/concepts/sandboxing#prerequisites. Codex will use the bundled bubblewrap in the meantime.
codex
我会只做只读审查，定位实际调用点与现有封装，再给出最小侵入的代码级方案。
exec
/bin/bash -lc 'nl -ba pkg/humanize/util.go && nl -ba service.go && nl -ba pkg/humanize/rod/hrod.go && nl -ba pkg/humanize/humanize.go && nl -ba pkg/humanize/mouse.go && nl -ba pkg/humanize/keyboard.go' in /tmp/xiaohongshu-mcp-fork
exec
/bin/bash -lc "rg -n -C 3 'time\\.Sleep|sleepRandom|sleepWithContext|context\\.Context|\\.Context\\(' xiaohongshu pkg/humanize service.go" in /tmp/xiaohongshu-mcp-fork
exec
/bin/bash -lc "sed -n '1,260p' xiaohongshu/publish.go && sed -n '1,260p' xiaohongshu/comment_feed.go && sed -n '1,260p' xiaohongshu/publish_video.go && sed -n '1,260p' xiaohongshu/feed_detail.go" in /tmp/xiaohongshu-mcp-fork
exec
/bin/bash -lc "sed -n '1,260p' xiaohongshu/like_favorite.go && sed -n '1,320p' xiaohongshu/login.go && sed -n '1,320p' xiaohongshu/feeds.go" in /tmp/xiaohongshu-mcp-fork
 succeeded in 102ms:
 succeeded in 110ms:
package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
	"github.com/xpzouying/xiaohongshu-mcp/errors"
)

type FeedsListAction struct {
	page *hrod.Page
}

func NewFeedsListAction(page *hrod.Page) *FeedsListAction {
	pp := page.Timeout(60 * time.Second)

	pp.MustNavigate("https://www.xiaohongshu.com")
	pp.MustWaitDOMStable()

	return &FeedsListAction{page: pp}
}

// GetFeedsList 获取页面的 Feed 列表数据
func (f *FeedsListAction) GetFeedsList(ctx context.Context) ([]Feed, error) {
	page := f.page.Context(ctx)

	time.Sleep(1 * time.Second)

	result := page.MustEval(`() => {
		if (window.__INITIAL_STATE__ &&
		    window.__INITIAL_STATE__.feed &&
		    window.__INITIAL_STATE__.feed.feeds) {
			const feeds = window.__INITIAL_STATE__.feed.feeds;
			const feedsData = feeds.value !== undefined ? feeds.value : feeds._value;
			if (feedsData) {
				return JSON.stringify(feedsData);
			}
		}
		return "";
	}`).String()

	if result == "" {
		return nil, errors.ErrNoFeeds
	}

	var feeds []Feed
	if err := json.Unmarshal([]byte(result), &feeds); err != nil {
		return nil, fmt.Errorf("failed to unmarshal feeds: %w", err)
	}

	return feeds, nil
}

 succeeded in 124ms:
     1	// Package hrod provides a thin humanized wrapper around go-rod.
     2	//
     3	// It keeps the go-rod API familiar while transparently adding human-like
     4	// mouse movements, scrolls, and keystrokes to the most common automation
     5	// methods (Click, Input, MustClick, MustInput, etc.).
     6	//
     7	// The wrapper uses explicit composition instead of embedding *rod.Page and
     8	// *rod.Element, so child lookup methods can keep the natural Element/MustElement
     9	// names and always return humanized *hrod.Element values.
    10	//
    11	// For methods that are not explicitly wrapped, access the underlying rod value
    12	// via the exported Rod field: page.Rod.XXX() or el.Rod.XXX().
    13	package hrod
    14	
    15	import (
    16		"context"
    17		"time"
    18	
    19		"github.com/go-rod/rod"
    20		"github.com/go-rod/rod/lib/proto"
    21		"github.com/ysmood/gson"
    22		"github.com/xpzouying/headless_browser"
    23		"github.com/xpzouying/xiaohongshu-mcp/pkg/humanize"
    24	)
    25	
    26	// Browser wraps a *headless_browser.Browser and humanizes the pages it creates.
    27	type Browser struct {
    28		hb  *headless_browser.Browser
    29		cfg humanize.Config
    30	}
    31	
    32	// NewBrowser wraps an existing *headless_browser.Browser.
    33	func NewBrowser(hb *headless_browser.Browser, cfg humanize.Config) *Browser {
    34		return &Browser{hb: hb, cfg: cfg}
    35	}
    36	
    37	// Rod returns the underlying *headless_browser.Browser.
    38	func (b *Browser) Rod() *headless_browser.Browser {
    39		return b.hb
    40	}
    41	
    42	// Close closes the underlying browser.
    43	func (b *Browser) Close() error {
    44		b.hb.Close()
    45		return nil
    46	}
    47	
    48	// MustClose is the humanized version of MustClose.
    49	func (b *Browser) MustClose() *Browser {
    50		b.hb.Close()
    51		return b
    52	}
    53	
    54	// NewPage creates a humanized page.
    55	func (b *Browser) NewPage() *Page {
    56		return b.wrapPage(b.hb.NewPage())
    57	}
    58	
    59	// MustPage opens a humanized page.
    60	func (b *Browser) MustPage(url string) *Page {
    61		p := b.hb.NewPage()
    62		if url != "" {
    63			p.MustNavigate(url)
    64		}
    65		return b.wrapPage(p)
    66	}
    67	
    68	func (b *Browser) wrapPage(p *rod.Page) *Page {
    69		if p == nil {
    70			return nil
    71		}
    72		page := &Page{
    73			Rod:      p,
    74			Mouse:    p.Mouse,
    75			Keyboard: p.Keyboard,
    76			actor:    humanize.New(p, b.cfg),
    77			browser:  b,
    78			cfg:      b.cfg,
    79			ctx:      context.Background(),
    80		}
    81		// Eagerly initialize the cursor position so the first interaction does not
    82		// start from rod's default (0,0), which is an obvious automation signature.
    83		_ = page.actor.Mouse.InitPosition()
    84		return page
    85	}
    86	
    87	// Page wraps a *rod.Page and adds humanized versions of common actions.
    88	// Access the underlying *rod.Page through the exported Rod field when a method
    89	// is not explicitly wrapped.
    90	type Page struct {
    91		Rod      *rod.Page
    92		Mouse    *rod.Mouse
    93		Keyboard *rod.Keyboard
    94		actor    *humanize.Actor
    95		browser  *Browser
    96		cfg      humanize.Config
    97		ctx      context.Context
    98	}
    99	
   100	// Actor exposes the underlying humanize actor for advanced use.
   101	func (p *Page) Actor() *humanize.Actor {
   102		return p.actor
   103	}
   104	
   105	// Browser returns the wrapping humanized browser.
   106	func (p *Page) Browser() *Browser {
   107		return p.browser
   108	}
   109	
   110	func (p *Page) wrapPage(rp *rod.Page) *Page {
   111		if rp == nil {
   112			return nil
   113		}
   114		return &Page{
   115			Rod:      rp,
   116			Mouse:    rp.Mouse,
   117			Keyboard: rp.Keyboard,
   118			actor:    humanize.NewWithContext(rp, p.cfg, p.ctx),
   119			browser:  p.browser,
   120			cfg:      p.cfg,
   121			ctx:      p.ctx,
   122		}
   123	}
   124	
   125	// Close closes the page.
   126	func (p *Page) Close() error {
   127		return p.Rod.Close()
   128	}
   129	
   130	// Context returns a humanized clone with the specified context.
   131	func (p *Page) Context(ctx context.Context) *Page {
   132		page := p.wrapPage(p.Rod.Context(ctx))
   133		page.ctx = ctx
   134		page.actor.SetContext(ctx)
   135		return page
   136	}
   137	
   138	// Timeout returns a humanized clone with the specified timeout.
   139	func (p *Page) Timeout(d time.Duration) *Page {
   140		return p.wrapPage(p.Rod.Timeout(d))
   141	}
   142	
   143	// CancelTimeout returns a humanized clone with the timeout cancelled.
   144	func (p *Page) CancelTimeout() *Page {
   145		return p.wrapPage(p.Rod.CancelTimeout())
   146	}
   147	
   148	// Navigate navigates to the URL.
   149	func (p *Page) Navigate(url string) error {
   150		return p.Rod.Navigate(url)
   151	}
   152	
   153	// MustNavigate is the humanized MustNavigate.
   154	func (p *Page) MustNavigate(url string) *Page {
   155		p.Rod.MustNavigate(url)
   156		return p
   157	}
   158	
   159	// Has checks if an element exists.
   160	func (p *Page) Has(selector string) (bool, *Element, error) {
   161		found, el, err := p.Rod.Has(selector)
   162		if err != nil || !found {
   163			return found, nil, err
   164		}
   165		return true, p.wrapElement(el), nil
   166	}
   167	
   168	// Element finds an element and returns a humanized wrapper.
   169	func (p *Page) Element(selector string) (*Element, error) {
   170		el, err := p.Rod.Element(selector)
   171		if err != nil {
   172			return nil, err
   173		}
   174		return p.wrapElement(el), nil
   175	}
   176	
   177	// MustElement finds an element and returns a humanized wrapper.
   178	func (p *Page) MustElement(selector string) *Element {
   179		return p.wrapElement(p.Rod.MustElement(selector))
   180	}
   181	
   182	// ElementR finds an element by regex and returns a humanized wrapper.
   183	func (p *Page) ElementR(selector, regex string) (*Element, error) {
   184		el, err := p.Rod.ElementR(selector, regex)
   185		if err != nil {
   186			return nil, err
   187		}
   188		return p.wrapElement(el), nil
   189	}
   190	
   191	// MustElementR finds an element by regex and returns a humanized wrapper.
   192	func (p *Page) MustElementR(selector, regex string) *Element {
   193		return p.wrapElement(p.Rod.MustElementR(selector, regex))
   194	}
   195	
   196	// ElementX finds an element by XPath and returns a humanized wrapper.
   197	func (p *Page) ElementX(xpath string) (*Element, error) {
   198		el, err := p.Rod.ElementX(xpath)
   199		if err != nil {
   200			return nil, err
   201		}
   202		return p.wrapElement(el), nil
   203	}
   204	
   205	// MustElementX finds an element by XPath and returns a humanized wrapper.
   206	func (p *Page) MustElementX(xpath string) *Element {
   207		return p.wrapElement(p.Rod.MustElementX(xpath))
   208	}
   209	
   210	// Elements returns humanized elements.
   211	func (p *Page) Elements(selector string) ([]*Element, error) {
   212		els, err := p.Rod.Elements(selector)
   213		if err != nil {
   214			return nil, err
   215		}
   216		result := make([]*Element, len(els))
   217		for i, el := range els {
   218			result[i] = p.wrapElement(el)
   219		}
   220		return result, nil
   221	}
   222	
   223	// MustElements returns humanized elements.
   224	func (p *Page) MustElements(selector string) []*Element {
   225		els := p.Rod.MustElements(selector)
   226		result := make([]*Element, len(els))
   227		for i, el := range els {
   228			result[i] = p.wrapElement(el)
   229		}
   230		return result
   231	}
   232	
   233	// Eval evaluates JS on the page.
   234	func (p *Page) Eval(js string, params ...interface{}) (*proto.RuntimeRemoteObject, error) {
   235		return p.Rod.Eval(js, params...)
   236	}
   237	
   238	// MustEval is the humanized MustEval.
   239	func (p *Page) MustEval(js string, params ...interface{}) gson.JSON {
   240		return p.Rod.MustEval(js, params...)
   241	}
   242	
   243	// Wait waits until the JS expression returns true.
   244	func (p *Page) Wait(opts *rod.EvalOptions) error {
   245		return p.Rod.Wait(opts)
   246	}
   247	
   248	// MustWait is the humanized MustWait.
   249	func (p *Page) MustWait(js string, params ...interface{}) *Page {
   250		p.Rod.MustWait(js, params...)
   251		return p
   252	}
   253	
   254	// WaitLoad waits for the page load event.
   255	func (p *Page) WaitLoad() error {
   256		return p.Rod.WaitLoad()
   257	}
   258	
   259	// MustWaitLoad is the humanized MustWaitLoad.
   260	func (p *Page) MustWaitLoad() *Page {
   261		p.Rod.MustWaitLoad()
   262		return p
   263	}
   264	
   265	// WaitStable waits for the page to be stable.
   266	func (p *Page) WaitStable(d time.Duration) error {
   267		return p.Rod.WaitStable(d)
   268	}
   269	
   270	// MustWaitStable is the humanized MustWaitStable.
   271	func (p *Page) MustWaitStable() *Page {
   272		p.Rod.MustWaitStable()
   273		return p
   274	}
   275	
   276	// WaitDOMStable waits for the DOM to be stable.
   277	func (p *Page) WaitDOMStable(d time.Duration, diff float64) error {
   278		return p.Rod.WaitDOMStable(d, diff)
   279	}
   280	
   281	// MustWaitDOMStable is the humanized MustWaitDOMStable.
   282	func (p *Page) MustWaitDOMStable() *Page {
   283		p.Rod.MustWaitDOMStable()
   284		return p
   285	}
   286	
   287	// MustClick clicks the element matched by selector with human-like movement.
   288	// It panics if the element cannot be found or clicked.
   289	func (p *Page) MustClick(selector string) *Page {
   290		el, err := p.Element(selector)
   291		if err != nil {
   292			panic(err)
   293		}
   294		if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
   295			panic(err)
   296		}
   297		return p
   298	}
   299	
   300	// MustInput focuses the element and types text with human-like timing.
   301	// It panics if the element cannot be found or the text cannot be entered.
   302	func (p *Page) MustInput(selector string, text string) *Page {
   303		el, err := p.Element(selector)
   304		if err != nil {
   305			panic(err)
   306		}
   307		if err := el.Input(text); err != nil {
   308			panic(err)
   309		}
   310		return p
   311	}
   312	
   313	// MustScroll scrolls vertically by the given amount in a human-like way.
   314	// It panics if the scroll cannot be performed.
   315	func (p *Page) MustScroll(vertical float64) *Page {
   316		if err := p.actor.Mouse.Scroll(0, vertical); err != nil {
   317			panic(err)
   318		}
   319		return p
   320	}
   321	
   322	func (p *Page) wrapElement(el *rod.Element) *Element {
   323		if el == nil {
   324			return nil
   325		}
   326		return newElement(el, p.actor, p.browser)
   327	}
   328	
   329	// NewElement creates a humanized element from a raw *rod.Element.
   330	func NewElement(el *rod.Element, actor *humanize.Actor) *Element {
   331		return newElement(el, actor, nil)
   332	}
   333	
   334	func newElement(el *rod.Element, actor *humanize.Actor, browser *Browser) *Element {
   335		if el == nil {
   336			return nil
   337		}
   338		return &Element{Rod: el, actor: actor, browser: browser}
   339	}
   340	
   341	// Element wraps a *rod.Element and adds humanized Click/Input methods.
   342	// Access the underlying *rod.Element through the exported Rod field when a method
   343	// is not explicitly wrapped.
   344	type Element struct {
   345		Rod     *rod.Element
   346		actor   *humanize.Actor
   347		browser *Browser
   348	}
   349	
   350	// Actor exposes the underlying humanize actor for advanced use.
   351	func (el *Element) Actor() *humanize.Actor {
   352		return el.actor
   353	}
   354	
   355	// Page returns the wrapping humanized page.
   356	func (el *Element) Page() *Page {
   357		rp := el.Rod.Page()
   358		return &Page{
   359			Rod:      rp,
   360			Mouse:    rp.Mouse,
   361			Keyboard: rp.Keyboard,
   362			actor:    el.actor,
   363			browser:  el.browser,
   364			cfg:      el.actor.Config(),
   365		}
   366	}
   367	
   368	// Click performs a human-like click.
   369	func (el *Element) Click(button proto.InputMouseButton, clickCount int) error {
   370		return el.actor.Mouse.ClickWithOptions(el.Rod, button, clickCount)
   371	}
   372	
   373	// ClickNoScroll performs a human-like click without scrolling the element into
   374	// view first. Useful for sticky/fixed elements that are already visible.
   375	func (el *Element) ClickNoScroll() error {
   376		return el.actor.Mouse.ClickNoScroll(el.Rod)
   377	}
   378	
   379	// ClickPoint moves to a viewport-relative point and clicks there.
   380	func (p *Page) ClickPoint(point proto.Point) error {
   381		return p.actor.Mouse.ClickPoint(point)
   382	}
   383	
   384	// MovePoint moves to a viewport-relative point.
   385	func (p *Page) MovePoint(point proto.Point) error {
   386		return p.actor.Mouse.MovePoint(point)
   387	}
   388	
   389	// MustClick is the humanized MustClick.
   390	// It panics if the element cannot be clicked.
   391	func (el *Element) MustClick() *Element {
   392		if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
   393			panic(err)
   394		}
   395		return el
   396	}
   397	
   398	// Input performs human-like typing.
   399	func (el *Element) Input(text string) error {
   400		return el.actor.Keyboard.Type(el.Rod, text)
   401	}
   402	
   403	// MustInput is the humanized MustInput.
   404	// It panics if the text cannot be entered.
   405	func (el *Element) MustInput(text string) *Element {
   406		if err := el.Input(text); err != nil {
   407			panic(err)
   408		}
   409		return el
   410	}
   411	
   412	// Hover moves the cursor over the element in a human-like way.
   413	func (el *Element) Hover() error {
   414		return el.actor.Mouse.Hover(el.Rod)
   415	}
   416	
   417	// MustHover is the humanized MustHover.
   418	// It panics if the cursor cannot be moved over the element.
   419	func (el *Element) MustHover() *Element {
   420		if err := el.Hover(); err != nil {
   421			panic(err)
   422		}
   423		return el
   424	}
   425	
   426	// ScrollIntoView scrolls the element into view in a human-like way by
   427	// dispatching wheel events until the element is centered in the viewport.
   428	func (el *Element) ScrollIntoView() error {
   429		return el.actor.Mouse.ScrollIntoView(el.Rod)
   430	}
   431	
   432	// MustScrollIntoView is the humanized MustScrollIntoView.
   433	// It panics if the element cannot be scrolled into view.
   434	func (el *Element) MustScrollIntoView() *Element {
   435		if err := el.ScrollIntoView(); err != nil {
   436			panic(err)
   437		}
   438		return el
   439	}
   440	
   441	// Element finds a child element and returns a humanized wrapper.
   442	func (el *Element) Element(selector string) (*Element, error) {
   443		child, err := el.Rod.Element(selector)
   444		if err != nil {
   445			return nil, err
   446		}
   447		return newElement(child, el.actor, el.browser), nil
   448	}
   449	
   450	// MustElement finds a child element and returns a humanized wrapper.
   451	func (el *Element) MustElement(selector string) *Element {
   452		return newElement(el.Rod.MustElement(selector), el.actor, el.browser)
   453	}
   454	
   455	// ElementR finds a child element by regex and returns a humanized wrapper.
   456	func (el *Element) ElementR(selector, regex string) (*Element, error) {
   457		child, err := el.Rod.ElementR(selector, regex)
   458		if err != nil {
   459			return nil, err
   460		}
   461		return newElement(child, el.actor, el.browser), nil
   462	}
   463	
   464	// MustElementR finds a child element by regex and returns a humanized wrapper.
   465	func (el *Element) MustElementR(selector, regex string) *Element {
   466		return newElement(el.Rod.MustElementR(selector, regex), el.actor, el.browser)
   467	}
   468	
   469	// ElementX finds a child element by XPath and returns a humanized wrapper.
   470	func (el *Element) ElementX(xpath string) (*Element, error) {
   471		child, err := el.Rod.ElementX(xpath)
   472		if err != nil {
   473			return nil, err
   474		}
   475		return newElement(child, el.actor, el.browser), nil
   476	}
   477	
   478	// MustElementX finds a child element by XPath and returns a humanized wrapper.
   479	func (el *Element) MustElementX(xpath string) *Element {
   480		return newElement(el.Rod.MustElementX(xpath), el.actor, el.browser)
   481	}
   482	
   483	// Elements returns humanized child elements.
   484	func (el *Element) Elements(selector string) ([]*Element, error) {
   485		children, err := el.Rod.Elements(selector)
   486		if err != nil {
   487			return nil, err
   488		}
   489		result := make([]*Element, len(children))
   490		for i, child := range children {
   491			result[i] = newElement(child, el.actor, el.browser)
   492		}
   493		return result, nil
   494	}
   495	
   496	// MustElements returns humanized child elements.
   497	func (el *Element) MustElements(selector string) []*Element {
   498		children := el.Rod.MustElements(selector)
   499		result := make([]*Element, len(children))
   500		for i, child := range children {
   501			result[i] = newElement(child, el.actor, el.browser)
   502		}
   503		return result
   504	}
   505	
   506	// Parent returns the humanized parent element.
   507	func (el *Element) Parent() (*Element, error) {
   508		p, err := el.Rod.Parent()
   509		if err != nil {
   510			return nil, err
   511		}
   512		return newElement(p, el.actor, el.browser), nil
   513	}
   514	
   515	// Next returns the humanized next sibling element.
   516	func (el *Element) Next() (*Element, error) {
   517		next, err := el.Rod.Next()
   518		if err != nil {
   519			return nil, err
   520		}
   521		return newElement(next, el.actor, el.browser), nil
   522	}
   523	
   524	// Previous returns the humanized previous sibling element.
   525	func (el *Element) Previous() (*Element, error) {
   526		prev, err := el.Rod.Previous()
   527		if err != nil {
   528			return nil, err
   529		}
   530		return newElement(prev, el.actor, el.browser), nil
   531	}
   532	
   533	// Attribute returns the value of an attribute.
   534	func (el *Element) Attribute(name string) (*string, error) {
   535		return el.Rod.Attribute(name)
   536	}
   537	
   538	// MustAttribute is the humanized MustAttribute.
   539	func (el *Element) MustAttribute(name string) *string {
   540		return el.Rod.MustAttribute(name)
   541	}
   542	
   543	// Text returns the element text.
   544	func (el *Element) Text() (string, error) {
   545		return el.Rod.Text()
   546	}
   547	
   548	// MustText is the humanized MustText.
   549	func (el *Element) MustText() string {
   550		return el.Rod.MustText()
   551	}
   552	
   553	// Visible returns whether the element is visible.
   554	func (el *Element) Visible() (bool, error) {
   555		return el.Rod.Visible()
   556	}
   557	
   558	// MustVisible is the humanized MustVisible.
   559	func (el *Element) MustVisible() bool {
   560		return el.Rod.MustVisible()
   561	}
   562	
   563	// WaitVisible waits for the element to become visible.
   564	func (el *Element) WaitVisible() error {
   565		return el.Rod.WaitVisible()
   566	}
   567	
   568	// MustWaitVisible is the humanized MustWaitVisible.
   569	func (el *Element) MustWaitVisible() *Element {
   570		el.Rod.MustWaitVisible()
   571		return el
   572	}
   573	
   574	// Eval evaluates JS on the element.
   575	func (el *Element) Eval(js string, params ...interface{}) (*proto.RuntimeRemoteObject, error) {
   576		return el.Rod.Eval(js, params...)
   577	}
   578	
   579	// MustEval is the humanized MustEval.
   580	func (el *Element) MustEval(js string, params ...interface{}) gson.JSON {
   581		return el.Rod.MustEval(js, params...)
   582	}
   583	
   584	// Shape returns the element shape.
   585	func (el *Element) Shape() (*proto.DOMGetContentQuadsResult, error) {
   586		return el.Rod.Shape()
   587	}
   588	
   589	// KeyActions returns key actions for the element.
   590	func (el *Element) KeyActions() (*rod.KeyActions, error) {
   591		return el.Rod.KeyActions()
   592	}
   593	
   594	// SelectAllText selects all text in the element.
   595	func (el *Element) SelectAllText() error {
   596		return el.Rod.SelectAllText()
   597	}
   598	
   599	// SetFiles sets files for a file input element.
   600	func (el *Element) SetFiles(paths []string) error {
   601		return el.Rod.SetFiles(paths)
   602	}
   603	
   604	// MustSetFiles is the humanized MustSetFiles.
   605	func (el *Element) MustSetFiles(paths ...string) *Element {
   606		el.Rod.MustSetFiles(paths...)
   607		return el
   608	}
   609	
   610	// Remove removes the element from the DOM.
   611	func (el *Element) Remove() error {
   612		return el.Rod.Remove()
   613	}
   614	
   615	// MustRemove is the humanized MustRemove.
   616	func (el *Element) MustRemove() *Element {
   617		el.Rod.MustRemove()
   618		return el
   619	}
   620	
   621	// Timeout returns a humanized clone with the specified timeout.
   622	func (el *Element) Timeout(d time.Duration) *Element {
   623		return newElement(el.Rod.Timeout(d), el.actor, el.browser)
   624	}
   625	
   626	// Context returns a humanized clone with the specified context.
   627	func (el *Element) Context(ctx context.Context) *Element {
   628		return newElement(el.Rod.Context(ctx), el.actor, el.browser)
   629	}
     1	// Package humanize provides human-like browser interactions for go-rod.
     2	//
     3	// Unlike a single smooth Bezier curve, this package:
     4	//   - splits movements into multiple segments with different curve families
     5	//   - adds jitter, pauses, overshoots, and random scroll events
     6	//   - types with variable speed, bursts, and occasional typos + corrections
     7	//   - exposes slow/normal/fast speed profiles based on real human timing
     8	package humanize
     9	
    10	import (
    11		"context"
    12	
    13		"github.com/go-rod/rod"
    14	)
    15	
    16	// Actor groups humanized mouse and keyboard actions.
    17	type Actor struct {
    18		Mouse    *Mouse
    19		Keyboard *Keyboard
    20		cfg      Config
    21	}
    22	
    23	// New creates a humanized actor for the given page.
    24	func New(page *rod.Page, cfg Config) *Actor {
    25		return NewWithContext(page, cfg, context.Background())
    26	}
    27	
    28	// NewWithContext creates a humanized actor for the given page and context.
    29	func NewWithContext(page *rod.Page, cfg Config, ctx context.Context) *Actor {
    30		mouse := NewMouse(page, cfg)
    31		actor := &Actor{
    32			Mouse:    mouse,
    33			Keyboard: NewKeyboard(page, cfg, mouse),
    34			cfg:      cfg,
    35		}
    36		actor.SetContext(ctx)
    37		return actor
    38	}
    39	
    40	// Config returns the actor's configuration.
    41	func (a *Actor) Config() Config {
    42		return a.cfg
    43	}
    44	
    45	// SetContext updates the context used by humanized delays.
    46	func (a *Actor) SetContext(ctx context.Context) {
    47		a.Mouse.setContext(ctx)
    48		a.Keyboard.setContext(ctx)
    49	}
     1	package humanize
     2	
     3	import (
     4		"context"
     5		"errors"
     6		"math"
     7		"math/rand"
     8		"os"
     9		"time"
    10	
    11		"github.com/go-rod/rod"
    12		"github.com/go-rod/rod/lib/proto"
    13	)
    14	
    15	var debugMouse bool
    16	
    17	func init() {
    18		v := os.Getenv("HUMANIZE_DEBUG")
    19		debugMouse = v == "1" || v == "true" || v == "yes"
    20	}
    21	
    22	// Mouse provides human-like mouse operations.
    23	type Mouse struct {
    24		page        *rod.Page
    25		cfg         Config
    26		ctx         context.Context
    27		initialized bool
    28	}
    29	
    30	// NewMouse creates a new humanized mouse wrapper.
    31	func NewMouse(page *rod.Page, cfg Config) *Mouse {
    32		return &Mouse{page: page, cfg: cfg, ctx: context.Background()}
    33	}
    34	
    35	func (m *Mouse) setContext(ctx context.Context) {
    36		m.ctx = ctx
    37	}
    38	
    39	// initPosition moves the cursor from the rod default (0,0) to a plausible
    40	// starting point inside the viewport. This is done once per Mouse instance so
    41	// subsequent movements do not look like long flights from the screen corner.
    42	// The movement itself is humanized so the cursor does not teleport.
    43	func (m *Mouse) initPosition() error {
    44		if m.initialized {
    45			return nil
    46		}
    47		vp, err := m.viewport()
    48		if err != nil {
    49			return err
    50		}
    51		center := Point{
    52			X: vp.width/2 + (rand.Float64()*2-1)*vp.width*0.15,
    53			Y: vp.height/2 + (rand.Float64()*2-1)*vp.height*0.15,
    54		}
    55	
    56		// Mark initialized before calling moveTo to avoid recursion.
    57		m.initialized = true
    58		if err := m.moveTo(center, false); err != nil {
    59			m.initialized = false
    60			return err
    61		}
    62		return nil
    63	}
    64	
    65	// InitPosition eagerly moves the cursor from the rod default (0,0) to a
    66	// plausible starting point. Call this right after a page is created so the
    67	// first real interaction does not start from the detectable (0,0) origin.
    68	func (m *Mouse) InitPosition() error {
    69		return m.initPosition()
    70	}
    71	
    72	// Move moves the cursor to target with a realistic, non-deterministic path.
    73	// If the target lies outside the current viewport, the page is scrolled first
    74	// so that the destination is rendered before the cursor moves there.
    75	func (m *Mouse) Move(target Point) error {
    76		// target is in page-absolute coordinates. Scroll it into view if it is
    77		// outside the current viewport, then convert to viewport-relative
    78		// coordinates before moving the cursor (rod.Mouse.MoveTo expects
    79		// viewport-relative coordinates).
    80		if err := m.scrollToVisible(target); err != nil {
    81			return err
    82		}
    83		vp, err := m.viewport()
    84		if err != nil {
    85			return err
    86		}
    87		return m.moveTo(Point{
    88			X: target.X - vp.scrollX,
    89			Y: target.Y - vp.scrollY,
    90		}, true)
    91	}
    92	
    93	// MovePoint moves to a viewport-relative point.
    94	func (m *Mouse) MovePoint(target Point) error {
    95		return m.moveTo(target, true)
    96	}
    97	
    98	// moveTo performs the actual cursor movement without any extra scrolling.
    99	func (m *Mouse) moveTo(target Point, scrollingAllowed bool) error {
   100		if debugMouse {
   101			m.ensureDebugOverlay()
   102		}
   103	
   104		// Start from a plausible position instead of rod's default (0,0).
   105		if err := m.initPosition(); err != nil {
   106			return err
   107		}
   108	
   109		start := m.page.Mouse.Position()
   110		straightDist := math.Hypot(target.X-start.X, target.Y-start.Y)
   111	
   112		// Derive step count from distance so short moves finish quickly and long
   113		// moves still have enough points to look natural.
   114		desiredSteps := int(straightDist / m.cfg.Mouse.StepDistance)
   115		if desiredSteps < m.cfg.Mouse.MinSteps {
   116			desiredSteps = m.cfg.Mouse.MinSteps
   117		}
   118		if desiredSteps > m.cfg.Mouse.MaxSteps {
   119			desiredSteps = m.cfg.Mouse.MaxSteps
   120		}
   121	
   122		path := GeneratePath(start, target, desiredSteps, desiredSteps, m.cfg.Mouse.OvershootRatio)
   123	
   124		// Base speed with variance.
   125		speed := m.cfg.Mouse.MoveSpeedPxPerSec * (1 + (rand.Float64()*2-1)*m.cfg.Mouse.SpeedVariance)
   126	
   127		// Total distance for velocity profile normalization.
   128		totalDist := 0.0
   129		prev := start
   130		for _, p := range path {
   131			totalDist += math.Hypot(p.X-prev.X, p.Y-prev.Y)
   132			prev = p
   133		}
   134	
   135		// Accelerate-then-fine-tune velocity profile: slow at the start, fast in
   136		// the middle, and slow again near the target. The profile is a sine hump
   137		// scaled so its average over [0,1] is 1.0, keeping the overall move time
   138		// comparable to the constant-speed baseline.
   139		const velocityFloor = 0.3
   140		velocityAmp := (1.0 - velocityFloor) * math.Pi / 2
   141	
   142		cumulativeDist := 0.0
   143		last := start
   144		for i, p := range path {
   145			// Inject jitter.
   146			if rand.Float64() < m.cfg.Mouse.JitterProbability {
   147				p = jitter(p, m.cfg.Mouse.JitterRadius)
   148			}
   149	
   150			// Distance-based step duration with ease-in-out acceleration.
   151			dist := math.Hypot(p.X-last.X, p.Y-last.Y)
   152			cumulativeDist += dist
   153	
   154			var stepDuration time.Duration
   155			if totalDist > 0 {
   156				t := cumulativeDist / totalDist
   157				// Use the midpoint of the step for smoother transitions.
   158				tMid := t - dist/(2*totalDist)
   159				if tMid < 0 {
   160					tMid = 0
   161				}
   162				velocity := velocityFloor + velocityAmp*math.Sin(math.Pi*tMid)
   163				effectiveSpeed := speed * velocity
   164				stepDuration = time.Duration(float64(time.Second) * dist / effectiveSpeed)
   165			} else {
   166				stepDuration = time.Duration(float64(time.Second) * dist / speed)
   167			}
   168			if stepDuration < 1*time.Millisecond {
   169				stepDuration = 1 * time.Millisecond
   170			}
   171	
   172			// Keep the event density high enough to look like a real mouse
   173			// (typical browser refresh rate is 60-120Hz). If the planned step is
   174			// too long, subdivide it into smaller micro-steps.
   175			const maxStepDuration = 16 * time.Millisecond
   176			subSteps := 1
   177			if stepDuration > maxStepDuration {
   178				subSteps = int(math.Ceil(float64(stepDuration) / float64(maxStepDuration)))
   179			}
   180	
   181			for j := 0; j < subSteps; j++ {
   182				ratio := float64(j+1) / float64(subSteps)
   183				subP := Point{
   184					X: last.X + (p.X-last.X)*ratio,
   185					Y: last.Y + (p.Y-last.Y)*ratio,
   186				}
   187	
   188				if err := m.page.Mouse.MoveTo(subP); err != nil {
   189					return err
   190				}
   191	
   192				if debugMouse {
   193					_ = m.tracePoint(subP.X, subP.Y, i == 0 && j == 0)
   194				}
   195	
   196				if err := sleepWithContext(m.ctx, stepDuration/time.Duration(subSteps)); err != nil {
   197					return err
   198				}
   199			}
   200	
   201			if scrollingAllowed && straightDist > 250 && rand.Float64() < m.cfg.Mouse.ScrollDuringMoveProbability {
   202				_ = m.scrollRandom()
   203				if err := sleepWithContext(m.ctx, randDuration(80*time.Millisecond, 180*time.Millisecond)); err != nil {
   204					return err
   205				}
   206			}
   207			if rand.Float64() < m.cfg.Mouse.PauseProbability {
   208				if err := sleepWithContext(m.ctx, randDuration(m.cfg.Mouse.PauseMin, m.cfg.Mouse.PauseMax)); err != nil {
   209					return err
   210				}
   211			}
   212	
   213			last = p
   214		}
   215		return nil
   216	}
   217	
   218	// Click scrolls the element into view, moves to its center with random offset, and clicks.
   219	func (m *Mouse) Click(el *rod.Element) error {
   220		return m.ClickWithOptions(el, proto.InputMouseButtonLeft, 1)
   221	}
   222	
   223	// ClickWithOptions scrolls the element into view, moves to its center with a
   224	// random offset, and clicks it with the requested button and click count.
   225	func (m *Mouse) ClickWithOptions(el *rod.Element, button proto.InputMouseButton, clickCount int) error {
   226		if clickCount < 1 {
   227			clickCount = 1
   228		}
   229	
   230		// Scroll the target element into view first; its on-screen position may
   231		// change after scrolling (fixed/sticky elements or layout shifts).
   232		if err := m.ScrollIntoView(el); err != nil {
   233			return err
   234		}
   235		// Re-calculate the target after scrolling, because fixed/sticky elements
   236		// move with the viewport and the old page-absolute coordinates are stale.
   237		target, err := elementTarget(el)
   238		if err != nil {
   239			return err
   240		}
   241		if err := m.moveTo(target, false); err != nil {
   242			return err
   243		}
   244	
   245		// Human pause before clicking.
   246		if err := sleepWithContext(m.ctx, randDuration(80*time.Millisecond, 350*time.Millisecond)); err != nil {
   247			return err
   248		}
   249	
   250		if err := m.page.Mouse.Down(button, clickCount); err != nil {
   251			return err
   252		}
   253		if err := sleepWithContext(m.ctx, randDuration(40*time.Millisecond, 120*time.Millisecond)); err != nil {
   254			return err
   255		}
   256		if err := m.page.Mouse.Up(button, clickCount); err != nil {
   257			return err
   258		}
   259		return nil
   260	}
   261	
   262	// ClickNoScroll performs a human-like click without scrolling the element into
   263	// view first. Use it when the target is already known to be visible (e.g.
   264	// sticky/fixed elements) to avoid the overhead or infinite loops caused by
   265	// ScrollIntoView.
   266	func (m *Mouse) ClickNoScroll(el *rod.Element) error {
   267		target, err := elementTarget(el)
   268		if err != nil {
   269			return err
   270		}
   271		if err := m.moveTo(target, false); err != nil {
   272			return err
   273		}
   274	
   275		// Human pause before clicking.
   276		if err := sleepWithContext(m.ctx, randDuration(80*time.Millisecond, 350*time.Millisecond)); err != nil {
   277			return err
   278		}
   279	
   280		if err := m.page.Mouse.Down(proto.InputMouseButtonLeft, 1); err != nil {
   281			return err
   282		}
   283		if err := sleepWithContext(m.ctx, randDuration(40*time.Millisecond, 120*time.Millisecond)); err != nil {
   284			return err
   285		}
   286		if err := m.page.Mouse.Up(proto.InputMouseButtonLeft, 1); err != nil {
   287			return err
   288		}
   289		return nil
   290	}
   291	
   292	// ClickPoint moves to a viewport-relative point and clicks there.
   293	func (m *Mouse) ClickPoint(target Point) error {
   294		if err := m.moveTo(target, false); err != nil {
   295			return err
   296		}
   297		if err := sleepWithContext(m.ctx, randDuration(80*time.Millisecond, 350*time.Millisecond)); err != nil {
   298			return err
   299		}
   300		if err := m.page.Mouse.Down(proto.InputMouseButtonLeft, 1); err != nil {
   301			return err
   302		}
   303		if err := sleepWithContext(m.ctx, randDuration(40*time.Millisecond, 120*time.Millisecond)); err != nil {
   304			return err
   305		}
   306		if err := m.page.Mouse.Up(proto.InputMouseButtonLeft, 1); err != nil {
   307			return err
   308		}
   309		return nil
   310	}
   311	
   312	// Scroll scrolls by deltaY (and optionally deltaX) in human-like increments.
   313	func (m *Mouse) Scroll(deltaX, deltaY float64) error {
   314		if deltaY == 0 && deltaX == 0 {
   315			return nil
   316		}
   317	
   318		steps := int(math.Abs(deltaY)/80) + int(math.Abs(deltaX)/80) + 3
   319		steps += rand.Intn(5)
   320		stepX := deltaX / float64(steps)
   321		stepY := deltaY / float64(steps)
   322	
   323		for i := 0; i < steps; i++ {
   324			if err := m.page.Mouse.Scroll(stepX, stepY, 1); err != nil {
   325				return err
   326			}
   327			// Variable scroll speed: faster at start, slower near end.
   328			base := 30 + float64(i)*5
   329			if err := sleepWithContext(m.ctx, time.Duration(base+rand.Float64()*40)*time.Millisecond); err != nil {
   330				return err
   331			}
   332		}
   333		return sleepWithContext(m.ctx, randDuration(200*time.Millisecond, 600*time.Millisecond))
   334	}
   335	
   336	// ScrollIntoView scrolls the page just enough to bring the element into the
   337	// visible viewport using humanized wheel events. It avoids JS scrollIntoView
   338	// which can be detected by pages observing synchronous scroll/layout changes.
   339	// The element only needs to be visible (with a small margin); it is not forced
   340	// to the center, so sticky/fixed elements do not cause infinite scrolling.
   341	func (m *Mouse) ScrollIntoView(el *rod.Element) error {
   342		const maxAttempts = 12
   343		const margin = 80
   344		for i := 0; i < maxAttempts; i++ {
   345			shape, err := el.Shape()
   346			if err != nil {
   347				return err
   348			}
   349			if len(shape.Quads) == 0 {
   350				return errors.New("element has no content quads")
   351			}
   352	
   353			// CDP quads are viewport-relative, so no scroll offset is needed.
   354			q := shape.Quads[0]
   355			var minX, maxX, minY, maxY float64
   356			for j := 0; j < q.Len(); j++ {
   357				x := q[j*2]
   358				y := q[j*2+1]
   359				if j == 0 || x < minX {
   360					minX = x
   361				}
   362				if j == 0 || x > maxX {
   363					maxX = x
   364				}
   365				if j == 0 || y < minY {
   366					minY = y
   367				}
   368				if j == 0 || y > maxY {
   369					maxY = y
   370				}
   371			}
   372	
   373			vp, err := m.viewport()
   374			if err != nil {
   375				return err
   376			}
   377	
   378			var deltaX, deltaY float64
   379			// CDP wheel: positive delta scrolls down/right, negative scrolls up/left.
   380			// Element above/left of viewport → scroll up/left (negative delta).
   381			// Element below/right of viewport → scroll down/right (positive delta).
   382			if maxX < margin {
   383				deltaX = maxX - margin
   384			} else if minX > vp.width-margin {
   385				deltaX = minX - vp.width + margin
   386			}
   387			if maxY < margin {
   388				deltaY = maxY - margin
   389			} else if minY > vp.height-margin {
   390				deltaY = minY - vp.height + margin
   391			}
   392	
   393			if deltaX == 0 && deltaY == 0 {
   394				return nil
   395			}
   396	
   397			if err := m.Scroll(deltaX, deltaY); err != nil {
   398				return err
   399			}
   400			if err := sleepWithContext(m.ctx, randDuration(80*time.Millisecond, 200*time.Millisecond)); err != nil {
   401				return err
   402			}
   403		}
   404		return nil
   405	}
   406	
   407	// Hover scrolls the element into view, moves to it, and pauses briefly.
   408	func (m *Mouse) Hover(el *rod.Element) error {
   409		if err := m.ScrollIntoView(el); err != nil {
   410			return err
   411		}
   412		target, err := elementTarget(el)
   413		if err != nil {
   414			return err
   415		}
   416		if err := m.moveTo(target, false); err != nil {
   417			return err
   418		}
   419		return sleepWithContext(m.ctx, randDuration(150*time.Millisecond, 500*time.Millisecond))
   420	}
   421	
   422	func (m *Mouse) scrollRandom() error {
   423		deltaY := randomSign() * (m.cfg.Mouse.ScrollMin + rand.Float64()*(m.cfg.Mouse.ScrollMax-m.cfg.Mouse.ScrollMin))
   424		return m.page.Mouse.Scroll(0, deltaY, 1)
   425	}
   426	
   427	type viewport struct {
   428		scrollX, scrollY float64
   429		width, height    float64
   430	}
   431	
   432	func (m *Mouse) viewport() (viewport, error) {
   433		obj, err := m.page.Eval(`() => ({
   434			scrollX: window.scrollX,
   435			scrollY: window.scrollY,
   436			innerWidth: window.innerWidth,
   437			innerHeight: window.innerHeight
   438		})`)
   439		if err != nil {
   440			return viewport{}, err
   441		}
   442		res, err := m.page.ObjectToJSON(obj)
   443		if err != nil {
   444			return viewport{}, err
   445		}
   446		return viewport{
   447			scrollX: res.Get("scrollX").Num(),
   448			scrollY: res.Get("scrollY").Num(),
   449			width:   res.Get("innerWidth").Num(),
   450			height:  res.Get("innerHeight").Num(),
   451		}, nil
   452	}
   453	
   454	// scrollToVisible scrolls the page so that target is rendered inside the
   455	// viewport with a comfortable margin. It is a no-op if target is already visible.
   456	func (m *Mouse) scrollToVisible(target Point) error {
   457		vp, err := m.viewport()
   458		if err != nil {
   459			return err
   460		}
   461	
   462		const margin = 80
   463		var deltaX, deltaY float64
   464	
   465		if target.X < vp.scrollX+margin {
   466			deltaX = target.X - vp.scrollX - vp.width/2
   467		} else if target.X > vp.scrollX+vp.width-margin {
   468			deltaX = target.X - vp.scrollX - vp.width/2
   469		}
   470	
   471		if target.Y < vp.scrollY+margin {
   472			deltaY = target.Y - vp.scrollY - vp.height/2
   473		} else if target.Y > vp.scrollY+vp.height-margin {
   474			deltaY = target.Y - vp.scrollY - vp.height/2
   475		}
   476	
   477		if deltaX == 0 && deltaY == 0 {
   478			return nil
   479		}
   480	
   481		// Add a small random offset so the target does not always land at the exact
   482		// center of the viewport.
   483		deltaX += (rand.Float64()*2 - 1) * 30
   484		deltaY += (rand.Float64()*2 - 1) * 30
   485	
   486		return m.Scroll(deltaX, deltaY)
   487	}
   488	
   489	// ensureDebugOverlay injects a canvas to visualize mouse movement.
   490	// It is only called when HUMANIZE_DEBUG=1.
   491	func (m *Mouse) ensureDebugOverlay() {
   492		_, _ = m.page.Eval(`() => {
   493			if (window.__humanizeCanvas) return;
   494			const canvas = document.createElement('canvas');
   495			canvas.id = '__humanize_mouse_trace';
   496			canvas.width = window.innerWidth;
   497			canvas.height = window.innerHeight;
   498			canvas.style.cssText = 'position:fixed;top:0;left:0;pointer-events:none;z-index:2147483647;';
   499			document.body.appendChild(canvas);
   500			window.__humanizeCanvas = canvas;
   501			window.__humanizeCtx = canvas.getContext('2d');
   502			window.addEventListener('resize', () => {
   503				canvas.width = window.innerWidth;
   504				canvas.height = window.innerHeight;
   505			});
   506		}`)
   507	}
   508	
   509	// tracePoint draws a dot on the debug overlay at (x, y).
   510	func (m *Mouse) tracePoint(x, y float64, first bool) error {
   511		_, err := m.page.Eval(`(x, y, first) => {
   512			const ctx = window.__humanizeCtx;
   513			if (!ctx) return;
   514			ctx.fillStyle = first ? 'rgba(0, 255, 0, 0.8)' : 'rgba(255, 0, 0, 0.5)';
   515			ctx.beginPath();
   516			ctx.arc(x, y, first ? 5 : 3, 0, Math.PI * 2);
   517			ctx.fill();
   518		}`, x, y, first)
   519		return err
   520	}
   521	
   522	func elementTarget(el *rod.Element) (Point, error) {
   523		shape, err := el.Shape()
   524		if err != nil {
   525			return Point{}, err
   526		}
   527		if len(shape.Quads) == 0 {
   528			return Point{}, errors.New("element has no content quads")
   529		}
   530		q := shape.Quads[0]
   531	
   532		// Compute the bounding box of the quad to handle arbitrary vertex order.
   533		var minX, maxX, minY, maxY float64
   534		for i := 0; i < q.Len(); i++ {
   535			x := q[i*2]
   536			y := q[i*2+1]
   537			if i == 0 || x < minX {
   538				minX = x
   539			}
   540			if i == 0 || x > maxX {
   541				maxX = x
   542			}
   543			if i == 0 || y < minY {
   544				minY = y
   545			}
   546			if i == 0 || y > maxY {
   547				maxY = y
   548			}
   549		}
   550		center := Point{
   551			X: (minX + maxX) / 2,
   552			Y: (minY + maxY) / 2,
   553		}
   554		width := maxX - minX
   555		height := maxY - minY
   556	
   557		// CDP DOM.getContentQuads returns coordinates relative to the viewport, and
   558		// rod.Mouse.MoveTo also expects viewport-relative coordinates, so no scroll
   559		// offset conversion is needed.
   560		// Random offset within central 60% of element.
   561		return Point{
   562			X: center.X + width*(rand.Float64()*0.3-0.15),
   563			Y: center.Y + height*(rand.Float64()*0.3-0.15),
   564		}, nil
   565	}
   566	
   567	func jitter(p Point, radius float64) Point {
   568		angle := rand.Float64() * 2 * math.Pi
   569		d := rand.Float64() * radius
   570		return Point{
   571			X: p.X + math.Cos(angle)*d,
   572			Y: p.Y + math.Sin(angle)*d,
   573		}
   574	}
   575	
   576	func randomSign() float64 {
   577		if rand.Intn(2) == 0 {
   578			return -1
   579		}
   580		return 1
   581	}
     1	package humanize
     2	
     3	import (
     4		"context"
     5		"math"
     6		"math/rand"
     7		"time"
     8	
     9		"github.com/go-rod/rod"
    10		"github.com/go-rod/rod/lib/input"
    11		"github.com/go-rod/rod/lib/proto"
    12	)
    13	
    14	// Keyboard provides human-like keyboard input.
    15	type Keyboard struct {
    16		page     *rod.Page
    17		cfg      Config
    18		ctx      context.Context
    19		mouse    *Mouse
    20		lastEl   *rod.Element
    21	}
    22	
    23	// NewKeyboard creates a new humanized keyboard wrapper.
    24	func NewKeyboard(page *rod.Page, cfg Config, mouse *Mouse) *Keyboard {
    25		return &Keyboard{page: page, cfg: cfg, ctx: context.Background(), mouse: mouse}
    26	}
    27	
    28	func (k *Keyboard) setContext(ctx context.Context) {
    29		k.ctx = ctx
    30	}
    31	
    32	// Type types text into el with realistic timing, occasional typos, and corrections.
    33	// ASCII characters are typed key-by-key; CJK and other non-keyboard characters
    34	// are inserted via simulated voice/IME composition events.
    35	func (k *Keyboard) Type(el *rod.Element, text string) error {
    36		// Ensure the element is rendered before typing, so the cursor lands on a
    37		// visible input area even when the page is long.
    38		if k.mouse == nil {
    39			if err := el.ScrollIntoView(); err != nil {
    40				return err
    41			}
    42		} else if k.lastEl == el {
    43			if err := k.mouse.ScrollIntoView(el); err != nil {
    44				return err
    45			}
    46		}
    47	
    48		// Move the cursor onto the element and click it, just like a human would
    49		// before typing. Skip the click if we just typed into the same element to
    50		// avoid repeated cursor jumps during continuous input (e.g. typing tags
    51		// char by char). This also keeps the mouse position continuous between
    52		// actions without querying DOM state that a page could detect.
    53		if k.mouse != nil && k.lastEl != el {
    54			if err := k.mouse.Click(el); err != nil {
    55				return err
    56			}
    57		}
    58		k.lastEl = el
    59	
    60		if err := el.Focus(); err != nil {
    61			return err
    62		}
    63	
    64		cfg := k.cfg.Keyboard
    65		if cfg.TypoChars == nil {
    66			cfg.TypoChars = []rune("qwertyuiopasdfghjklzxcvbnm1234567890")
    67		}
    68		if cfg.BurstLength <= 0 {
    69			cfg.BurstLength = 1
    70		}
    71	
    72		cpm := cfg.CPM * (1 + (rand.Float64()*2-1)*cfg.CPMVariance)
    73		msPerChar := 60000.0 / cpm
    74		// ASCII is typed roughly 2x faster; CJK voice/IME composition is slower.
    75		asciiMsPerChar := msPerChar / 2
    76		cjkMsPerChar := msPerChar * 3
    77	
    78		tokens := tokenizeText(text)
    79		typed := 0
    80		lastScrollCheck := 0
    81	
    82		for _, token := range tokens {
    83			if token.isASCII {
    84				for _, r := range token.text {
    85					// Occasional typo for ASCII keys.
    86					if rand.Float64() < cfg.TypoProbability {
    87						typo := randomTypo(r, cfg.TypoChars)
    88						if err := k.press(input.Key(typo)); err != nil {
    89							return err
    90						}
    91						if err := sleepWithContext(k.ctx, cfg.PauseAfterTypo+time.Duration(rand.Float64()*200)*time.Millisecond); err != nil {
    92							return err
    93						}
    94						if err := k.pressBackspace(); err != nil {
    95							return err
    96						}
    97						if err := sleepWithContext(k.ctx, randDuration(50*time.Millisecond, 150*time.Millisecond)); err != nil {
    98							return err
    99						}
   100					}
   101	
   102					if err := k.press(input.Key(r)); err != nil {
   103						return err
   104					}
   105					typed++
   106	
   107					delay := time.Duration(asciiMsPerChar * (0.6 + rand.Float64()*0.8) * float64(time.Millisecond))
   108					if delay < 10*time.Millisecond {
   109						delay = 10 * time.Millisecond
   110					}
   111					if err := sleepWithContext(k.ctx, delay); err != nil {
   112						return err
   113					}
   114	
   115					if typed%cfg.BurstLength == 0 {
   116						if err := sleepWithContext(k.ctx, randDuration(cfg.BurstPause, cfg.BurstPause+80*time.Millisecond)); err != nil {
   117							return err
   118						}
   119					}
   120	
   121					if typed-lastScrollCheck >= 30 {
   122						_ = k.scrollToCursor(el)
   123						lastScrollCheck = typed
   124					}
   125				}
   126			} else {
   127				// CJK / emoji / special chars: simulate voice/IME composition.
   128				segments := segmentCJK(token.text)
   129				for _, seg := range segments {
   130					if err := k.insertCompositionText(el, seg); err != nil {
   131						return err
   132					}
   133					segRunes := []rune(seg)
   134					// Pause between voice/IME chunks scales with segment length and
   135					// the slower CJK speed.
   136					pause := time.Duration(cjkMsPerChar * float64(len(segRunes)) * (0.8 + rand.Float64()*0.6))
   137					if pause < 150*time.Millisecond {
   138						pause = 150 * time.Millisecond
   139					}
   140					if err := sleepWithContext(k.ctx, pause); err != nil {
   141						return err
   142					}
   143					typed += len(segRunes)
   144	
   145					if typed-lastScrollCheck >= 30 {
   146						_ = k.scrollToCursor(el)
   147						lastScrollCheck = typed
   148					}
   149				}
   150			}
   151		}
   152	
   153		return nil
   154	}
   155	
   156	// Press presses a single key with human-like delay.
   157	func (k *Keyboard) Press(key input.Key) error {
   158		if err := k.press(key); err != nil {
   159			return err
   160		}
   161		return sleepWithContext(k.ctx, randDuration(50*time.Millisecond, 150*time.Millisecond))
   162	}
   163	
   164	func (k *Keyboard) press(key input.Key) error {
   165		return k.page.Keyboard.Press(key)
   166	}
   167	
   168	// pressBackspace sends a Backspace key via CDP directly.
   169	func (k *Keyboard) pressBackspace() error {
   170		return proto.InputDispatchKeyEvent{
   171			Type:                  proto.InputDispatchKeyEventTypeKeyDown,
   172			Key:                   "Backspace",
   173			Code:                  "Backspace",
   174			WindowsVirtualKeyCode: 8,
   175		}.Call(k.page)
   176	}
   177	
   178	// scrollToCursor scrolls the page so the text cursor remains visible while
   179	// typing long content. It is best-effort and ignores errors to avoid breaking
   180	// the typing flow.
   181	func (k *Keyboard) scrollToCursor(el *rod.Element) error {
   182		obj, err := el.Eval(`() => {
   183			const sel = window.getSelection();
   184			if (!sel || sel.rangeCount === 0) return null;
   185			const range = sel.getRangeAt(0);
   186			const rect = range.getBoundingClientRect();
   187			if (rect.width === 0 && rect.height === 0) return null;
   188			return {
   189				cursorTop: rect.top + window.scrollY,
   190				cursorBottom: rect.bottom + window.scrollY,
   191				cursorLeft: rect.left + window.scrollX,
   192				cursorRight: rect.right + window.scrollX,
   193			};
   194		}`)
   195		if err != nil {
   196			return err
   197		}
   198		if obj == nil {
   199			return nil
   200		}
   201		val, err := k.page.ObjectToJSON(obj)
   202		if err != nil {
   203			return err
   204		}
   205	
   206		cursorTop := val.Get("cursorTop").Num()
   207		cursorBottom := val.Get("cursorBottom").Num()
   208	
   209		vp, err := k.viewport()
   210		if err != nil {
   211			return err
   212		}
   213	
   214		const margin = 100
   215		var deltaY float64
   216		if cursorBottom > vp.scrollY+vp.height-margin {
   217			deltaY = cursorBottom - (vp.scrollY + vp.height) + margin + 50
   218		} else if cursorTop < vp.scrollY+margin {
   219			deltaY = cursorTop - vp.scrollY - margin - 50
   220		}
   221	
   222		if deltaY != 0 {
   223			return k.page.Mouse.Scroll(0, deltaY, 1)
   224		}
   225		return nil
   226	}
   227	
   228	func (k *Keyboard) viewport() (struct {
   229		scrollX, scrollY float64
   230		width, height    float64
   231	}, error) {
   232		var vp struct {
   233			scrollX, scrollY float64
   234			width, height    float64
   235		}
   236		obj, err := k.page.Eval(`() => ({
   237			scrollX: window.scrollX,
   238			scrollY: window.scrollY,
   239			innerWidth: window.innerWidth,
   240			innerHeight: window.innerHeight,
   241		})`)
   242		if err != nil {
   243			return vp, err
   244		}
   245		res, err := k.page.ObjectToJSON(obj)
   246		if err != nil {
   247			return vp, err
   248		}
   249		vp.scrollX = res.Get("scrollX").Num()
   250		vp.scrollY = res.Get("scrollY").Num()
   251		vp.width = res.Get("innerWidth").Num()
   252		vp.height = res.Get("innerHeight").Num()
   253		return vp, nil
   254	}
   255	
   256	// insertText inserts text directly via CDP Input.insertText.
   257	func (k *Keyboard) insertText(text string) error {
   258		return proto.InputInsertText{Text: text}.Call(k.page)
   259	}
   260	
   261	// insertCompositionText inserts CJK and other IME text through CDP so the
   262	// browser dispatches the native editing events expected by controlled inputs.
   263	func (k *Keyboard) insertCompositionText(_ *rod.Element, text string) error {
   264		return k.insertText(text)
   265	}
   266	
   267	// isRodKeySupported reports whether r can be sent via rod.Keyboard.Press.
   268	// Rod only supports a subset of printable ASCII keys; CJK characters panic.
   269	func isRodKeySupported(r rune) bool {
   270		return r >= 32 && r <= 126
   271	}
   272	
   273	// textToken represents a contiguous ASCII or non-ASCII run of text.
   274	type textToken struct {
   275		text   string
   276		isASCII bool
   277	}
   278	
   279	// tokenizeText splits text into alternating ASCII and non-ASCII tokens.
   280	func tokenizeText(text string) []textToken {
   281		var tokens []textToken
   282		var current []rune
   283		var currentASCII bool
   284		var hasCurrent bool
   285	
   286		for _, r := range text {
   287			ascii := isRodKeySupported(r)
   288			if !hasCurrent {
   289				current = append(current, r)
   290				currentASCII = ascii
   291				hasCurrent = true
   292				continue
   293			}
   294			if ascii == currentASCII {
   295				current = append(current, r)
   296			} else {
   297				tokens = append(tokens, textToken{text: string(current), isASCII: currentASCII})
   298				current = []rune{r}
   299				currentASCII = ascii
   300			}
   301		}
   302		if hasCurrent {
   303			tokens = append(tokens, textToken{text: string(current), isASCII: currentASCII})
   304		}
   305		return tokens
   306	}
   307	
   308	// segmentCJK splits a CJK string into small segments that mimic voice/IME
   309	// recognition chunks. It prefers splitting at punctuation and keeps segments
   310	// within 2-6 characters.
   311	func segmentCJK(text string) []string {
   312		runes := []rune(text)
   313		if len(runes) == 0 {
   314			return nil
   315		}
   316	
   317		// Sentence-ending or phrase-breaking punctuation.
   318		breakers := map[rune]bool{
   319			'。': true, '，': true, '、': true, '；': true, '：': true,
   320			'？': true, '！': true, '…': true, '“': true, '”': true,
   321			'（': true, '）': true, '【': true, '】': true, '《': true, '》': true,
   322			'.': true, ',': true, '!': true, '?': true, ';': true, ':': true,
   323		}
   324	
   325		var segments []string
   326		var start int
   327		minSeg := 2
   328		maxSeg := 6
   329	
   330		for i := 0; i < len(runes); i++ {
   331			length := i - start + 1
   332			isBreaker := breakers[runes[i]]
   333	
   334			// Break at punctuation or when segment reaches preferred size.
   335			if isBreaker || length >= maxSeg {
   336				if length >= minSeg || isBreaker {
   337					segments = append(segments, string(runes[start:i+1]))
   338					start = i + 1
   339					continue
   340				}
   341			}
   342	
   343			// If we're approaching max size and next char isn't a breaker, cut here.
   344			if length >= maxSeg-1 && i+1 < len(runes) && !breakers[runes[i+1]] {
   345				segments = append(segments, string(runes[start:i+1]))
   346				start = i + 1
   347			}
   348		}
   349	
   350		if start < len(runes) {
   351			segments = append(segments, string(runes[start:]))
   352		}
   353	
   354		return segments
   355	}
   356	
   357	// randomTypo returns a character near the intended one or a random typo char.
   358	func randomTypo(intended rune, pool []rune) rune {
   359		// Try to pick a visually/adjacently similar key from QWERTY rows.
   360		neighbors := map[rune][]rune{
   361			'q': {'w', 'a', 's'},
   362			'w': {'q', 'e', 's'},
   363			'e': {'w', 'r', 'd'},
   364			'r': {'e', 't', 'f'},
   365			't': {'r', 'y', 'g'},
   366			'y': {'t', 'u', 'h'},
   367			'u': {'y', 'i', 'j'},
   368			'i': {'u', 'o', 'k'},
   369			'o': {'i', 'p', 'l'},
   370			'p': {'o', 'l'},
   371			'a': {'q', 'w', 's'},
   372			's': {'a', 'w', 'd'},
   373			'd': {'s', 'e', 'f'},
   374			'f': {'d', 'r', 'g'},
   375			'g': {'f', 't', 'h'},
   376			'h': {'g', 'y', 'j'},
   377			'j': {'h', 'u', 'k'},
   378			'k': {'j', 'i', 'l'},
   379			'l': {'k', 'o', 'p'},
   380			'z': {'a', 's', 'x'},
   381			'x': {'z', 's', 'd'},
   382			'c': {'x', 'd', 'f'},
   383			'v': {'c', 'f', 'g'},
   384			'b': {'v', 'g', 'h'},
   385			'n': {'b', 'h', 'j'},
   386			'm': {'n', 'j', 'k'},
   387		}
   388	
   389		lower := intended
   390		if intended >= 'A' && intended <= 'Z' {
   391			lower = intended - 'A' + 'a'
   392		}
   393	
   394		if nbs, ok := neighbors[lower]; ok && len(nbs) > 0 && rand.Float64() < 0.7 {
   395			return nbs[rand.Intn(len(nbs))]
   396		}
   397	
   398		return pool[rand.Intn(len(pool))]
   399	}
   400	
   401	// randomUniform returns a random float in [min, max].
   402	func randomUniform(min, max float64) float64 {
   403		return min + rand.Float64()*(max-min)
   404	}
   405	
   406	// randomNormal returns a normally distributed value with mean and stddev.
   407	func randomNormal(mean, stddev float64) float64 {
   408		return mean + stddev*rand.NormFloat64()
   409	}
   410	
   411	// clamp ensures value is within [min, max].
   412	func clamp(value, min, max float64) float64 {
   413		if value < min {
   414			return min
   415		}
   416		if value > max {
   417			return max
   418		}
   419		return value
   420	}
   421	
   422	// unused helpers kept for future use.
   423	var _ = randomUniform
   424	var _ = randomNormal
   425	var _ = clamp
   426	var _ = math.Pi

 succeeded in 136ms:
--
pkg/humanize/humanize.go-26-}
pkg/humanize/humanize.go-27-
pkg/humanize/humanize.go-28-// NewWithContext creates a humanized actor for the given page and context.
pkg/humanize/humanize.go:29:func NewWithContext(page *rod.Page, cfg Config, ctx context.Context) *Actor {
pkg/humanize/humanize.go-30-	mouse := NewMouse(page, cfg)
pkg/humanize/humanize.go-31-	actor := &Actor{
pkg/humanize/humanize.go-32-		Mouse:    mouse,
--
pkg/humanize/humanize.go-43-}
pkg/humanize/humanize.go-44-
pkg/humanize/humanize.go-45-// SetContext updates the context used by humanized delays.
pkg/humanize/humanize.go:46:func (a *Actor) SetContext(ctx context.Context) {
pkg/humanize/humanize.go-47-	a.Mouse.setContext(ctx)
pkg/humanize/humanize.go-48-	a.Keyboard.setContext(ctx)
pkg/humanize/humanize.go-49-}
--
pkg/humanize/rod/hrod.go-94-	actor    *humanize.Actor
pkg/humanize/rod/hrod.go-95-	browser  *Browser
pkg/humanize/rod/hrod.go-96-	cfg      humanize.Config
pkg/humanize/rod/hrod.go:97:	ctx      context.Context
pkg/humanize/rod/hrod.go-98-}
pkg/humanize/rod/hrod.go-99-
pkg/humanize/rod/hrod.go-100-// Actor exposes the underlying humanize actor for advanced use.
--
pkg/humanize/rod/hrod.go-128-}
pkg/humanize/rod/hrod.go-129-
pkg/humanize/rod/hrod.go-130-// Context returns a humanized clone with the specified context.
pkg/humanize/rod/hrod.go:131:func (p *Page) Context(ctx context.Context) *Page {
pkg/humanize/rod/hrod.go:132:	page := p.wrapPage(p.Rod.Context(ctx))
pkg/humanize/rod/hrod.go-133-	page.ctx = ctx
pkg/humanize/rod/hrod.go-134-	page.actor.SetContext(ctx)
pkg/humanize/rod/hrod.go-135-	return page
--
pkg/humanize/rod/hrod.go-624-}
pkg/humanize/rod/hrod.go-625-
pkg/humanize/rod/hrod.go-626-// Context returns a humanized clone with the specified context.
pkg/humanize/rod/hrod.go:627:func (el *Element) Context(ctx context.Context) *Element {
pkg/humanize/rod/hrod.go:628:	return newElement(el.Rod.Context(ctx), el.actor, el.browser)
pkg/humanize/rod/hrod.go-629-}
--
pkg/humanize/keyboard.go-15-type Keyboard struct {
pkg/humanize/keyboard.go-16-	page     *rod.Page
pkg/humanize/keyboard.go-17-	cfg      Config
pkg/humanize/keyboard.go:18:	ctx      context.Context
pkg/humanize/keyboard.go-19-	mouse    *Mouse
pkg/humanize/keyboard.go-20-	lastEl   *rod.Element
pkg/humanize/keyboard.go-21-}
--
pkg/humanize/keyboard.go-25-	return &Keyboard{page: page, cfg: cfg, ctx: context.Background(), mouse: mouse}
pkg/humanize/keyboard.go-26-}
pkg/humanize/keyboard.go-27-
pkg/humanize/keyboard.go:28:func (k *Keyboard) setContext(ctx context.Context) {
pkg/humanize/keyboard.go-29-	k.ctx = ctx
pkg/humanize/keyboard.go-30-}
pkg/humanize/keyboard.go-31-
--
pkg/humanize/keyboard.go-88-					if err := k.press(input.Key(typo)); err != nil {
pkg/humanize/keyboard.go-89-						return err
pkg/humanize/keyboard.go-90-					}
pkg/humanize/keyboard.go:91:					if err := sleepWithContext(k.ctx, cfg.PauseAfterTypo+time.Duration(rand.Float64()*200)*time.Millisecond); err != nil {
pkg/humanize/keyboard.go-92-						return err
pkg/humanize/keyboard.go-93-					}
pkg/humanize/keyboard.go-94-					if err := k.pressBackspace(); err != nil {
pkg/humanize/keyboard.go-95-						return err
pkg/humanize/keyboard.go-96-					}
pkg/humanize/keyboard.go:97:					if err := sleepWithContext(k.ctx, randDuration(50*time.Millisecond, 150*time.Millisecond)); err != nil {
pkg/humanize/keyboard.go-98-						return err
pkg/humanize/keyboard.go-99-					}
pkg/humanize/keyboard.go-100-				}
--
pkg/humanize/keyboard.go-108-				if delay < 10*time.Millisecond {
pkg/humanize/keyboard.go-109-					delay = 10 * time.Millisecond
pkg/humanize/keyboard.go-110-				}
pkg/humanize/keyboard.go:111:				if err := sleepWithContext(k.ctx, delay); err != nil {
pkg/humanize/keyboard.go-112-					return err
pkg/humanize/keyboard.go-113-				}
pkg/humanize/keyboard.go-114-
pkg/humanize/keyboard.go-115-				if typed%cfg.BurstLength == 0 {
pkg/humanize/keyboard.go:116:					if err := sleepWithContext(k.ctx, randDuration(cfg.BurstPause, cfg.BurstPause+80*time.Millisecond)); err != nil {
pkg/humanize/keyboard.go-117-						return err
pkg/humanize/keyboard.go-118-					}
pkg/humanize/keyboard.go-119-				}
--
pkg/humanize/keyboard.go-137-				if pause < 150*time.Millisecond {
pkg/humanize/keyboard.go-138-					pause = 150 * time.Millisecond
pkg/humanize/keyboard.go-139-				}
pkg/humanize/keyboard.go:140:				if err := sleepWithContext(k.ctx, pause); err != nil {
pkg/humanize/keyboard.go-141-					return err
pkg/humanize/keyboard.go-142-				}
pkg/humanize/keyboard.go-143-				typed += len(segRunes)
--
pkg/humanize/keyboard.go-158-	if err := k.press(key); err != nil {
pkg/humanize/keyboard.go-159-		return err
pkg/humanize/keyboard.go-160-	}
pkg/humanize/keyboard.go:161:	return sleepWithContext(k.ctx, randDuration(50*time.Millisecond, 150*time.Millisecond))
pkg/humanize/keyboard.go-162-}
pkg/humanize/keyboard.go-163-
pkg/humanize/keyboard.go-164-func (k *Keyboard) press(key input.Key) error {
--
pkg/humanize/mouse.go-23-type Mouse struct {
pkg/humanize/mouse.go-24-	page        *rod.Page
pkg/humanize/mouse.go-25-	cfg         Config
pkg/humanize/mouse.go:26:	ctx         context.Context
pkg/humanize/mouse.go-27-	initialized bool
pkg/humanize/mouse.go-28-}
pkg/humanize/mouse.go-29-
--
pkg/humanize/mouse.go-32-	return &Mouse{page: page, cfg: cfg, ctx: context.Background()}
pkg/humanize/mouse.go-33-}
pkg/humanize/mouse.go-34-
pkg/humanize/mouse.go:35:func (m *Mouse) setContext(ctx context.Context) {
pkg/humanize/mouse.go-36-	m.ctx = ctx
pkg/humanize/mouse.go-37-}
pkg/humanize/mouse.go-38-
--
pkg/humanize/mouse.go-193-				_ = m.tracePoint(subP.X, subP.Y, i == 0 && j == 0)
pkg/humanize/mouse.go-194-			}
pkg/humanize/mouse.go-195-
pkg/humanize/mouse.go:196:			if err := sleepWithContext(m.ctx, stepDuration/time.Duration(subSteps)); err != nil {
pkg/humanize/mouse.go-197-				return err
pkg/humanize/mouse.go-198-			}
pkg/humanize/mouse.go-199-		}
pkg/humanize/mouse.go-200-
pkg/humanize/mouse.go-201-		if scrollingAllowed && straightDist > 250 && rand.Float64() < m.cfg.Mouse.ScrollDuringMoveProbability {
pkg/humanize/mouse.go-202-			_ = m.scrollRandom()
pkg/humanize/mouse.go:203:			if err := sleepWithContext(m.ctx, randDuration(80*time.Millisecond, 180*time.Millisecond)); err != nil {
pkg/humanize/mouse.go-204-				return err
pkg/humanize/mouse.go-205-			}
pkg/humanize/mouse.go-206-		}
pkg/humanize/mouse.go-207-		if rand.Float64() < m.cfg.Mouse.PauseProbability {
pkg/humanize/mouse.go:208:			if err := sleepWithContext(m.ctx, randDuration(m.cfg.Mouse.PauseMin, m.cfg.Mouse.PauseMax)); err != nil {
pkg/humanize/mouse.go-209-				return err
pkg/humanize/mouse.go-210-			}
pkg/humanize/mouse.go-211-		}
--
pkg/humanize/mouse.go-243-	}
pkg/humanize/mouse.go-244-
pkg/humanize/mouse.go-245-	// Human pause before clicking.
pkg/humanize/mouse.go:246:	if err := sleepWithContext(m.ctx, randDuration(80*time.Millisecond, 350*time.Millisecond)); err != nil {
pkg/humanize/mouse.go-247-		return err
pkg/humanize/mouse.go-248-	}
pkg/humanize/mouse.go-249-
pkg/humanize/mouse.go-250-	if err := m.page.Mouse.Down(button, clickCount); err != nil {
pkg/humanize/mouse.go-251-		return err
pkg/humanize/mouse.go-252-	}
pkg/humanize/mouse.go:253:	if err := sleepWithContext(m.ctx, randDuration(40*time.Millisecond, 120*time.Millisecond)); err != nil {
pkg/humanize/mouse.go-254-		return err
pkg/humanize/mouse.go-255-	}
pkg/humanize/mouse.go-256-	if err := m.page.Mouse.Up(button, clickCount); err != nil {
--
pkg/humanize/mouse.go-273-	}
pkg/humanize/mouse.go-274-
pkg/humanize/mouse.go-275-	// Human pause before clicking.
pkg/humanize/mouse.go:276:	if err := sleepWithContext(m.ctx, randDuration(80*time.Millisecond, 350*time.Millisecond)); err != nil {
pkg/humanize/mouse.go-277-		return err
pkg/humanize/mouse.go-278-	}
pkg/humanize/mouse.go-279-
pkg/humanize/mouse.go-280-	if err := m.page.Mouse.Down(proto.InputMouseButtonLeft, 1); err != nil {
pkg/humanize/mouse.go-281-		return err
pkg/humanize/mouse.go-282-	}
pkg/humanize/mouse.go:283:	if err := sleepWithContext(m.ctx, randDuration(40*time.Millisecond, 120*time.Millisecond)); err != nil {
pkg/humanize/mouse.go-284-		return err
pkg/humanize/mouse.go-285-	}
pkg/humanize/mouse.go-286-	if err := m.page.Mouse.Up(proto.InputMouseButtonLeft, 1); err != nil {
--
pkg/humanize/mouse.go-294-	if err := m.moveTo(target, false); err != nil {
pkg/humanize/mouse.go-295-		return err
pkg/humanize/mouse.go-296-	}
pkg/humanize/mouse.go:297:	if err := sleepWithContext(m.ctx, randDuration(80*time.Millisecond, 350*time.Millisecond)); err != nil {
pkg/humanize/mouse.go-298-		return err
pkg/humanize/mouse.go-299-	}
pkg/humanize/mouse.go-300-	if err := m.page.Mouse.Down(proto.InputMouseButtonLeft, 1); err != nil {
pkg/humanize/mouse.go-301-		return err
pkg/humanize/mouse.go-302-	}
pkg/humanize/mouse.go:303:	if err := sleepWithContext(m.ctx, randDuration(40*time.Millisecond, 120*time.Millisecond)); err != nil {
pkg/humanize/mouse.go-304-		return err
pkg/humanize/mouse.go-305-	}
pkg/humanize/mouse.go-306-	if err := m.page.Mouse.Up(proto.InputMouseButtonLeft, 1); err != nil {
--
pkg/humanize/mouse.go-326-		}
pkg/humanize/mouse.go-327-		// Variable scroll speed: faster at start, slower near end.
pkg/humanize/mouse.go-328-		base := 30 + float64(i)*5
pkg/humanize/mouse.go:329:		if err := sleepWithContext(m.ctx, time.Duration(base+rand.Float64()*40)*time.Millisecond); err != nil {
pkg/humanize/mouse.go-330-			return err
pkg/humanize/mouse.go-331-		}
pkg/humanize/mouse.go-332-	}
pkg/humanize/mouse.go:333:	return sleepWithContext(m.ctx, randDuration(200*time.Millisecond, 600*time.Millisecond))
pkg/humanize/mouse.go-334-}
pkg/humanize/mouse.go-335-
pkg/humanize/mouse.go-336-// ScrollIntoView scrolls the page just enough to bring the element into the
--
pkg/humanize/mouse.go-397-		if err := m.Scroll(deltaX, deltaY); err != nil {
pkg/humanize/mouse.go-398-			return err
pkg/humanize/mouse.go-399-		}
pkg/humanize/mouse.go:400:		if err := sleepWithContext(m.ctx, randDuration(80*time.Millisecond, 200*time.Millisecond)); err != nil {
pkg/humanize/mouse.go-401-			return err
pkg/humanize/mouse.go-402-		}
pkg/humanize/mouse.go-403-	}
--
pkg/humanize/mouse.go-416-	if err := m.moveTo(target, false); err != nil {
pkg/humanize/mouse.go-417-		return err
pkg/humanize/mouse.go-418-	}
pkg/humanize/mouse.go:419:	return sleepWithContext(m.ctx, randDuration(150*time.Millisecond, 500*time.Millisecond))
pkg/humanize/mouse.go-420-}
pkg/humanize/mouse.go-421-
pkg/humanize/mouse.go-422-func (m *Mouse) scrollRandom() error {
--
pkg/humanize/util.go-20-}
pkg/humanize/util.go-21-
pkg/humanize/util.go-22-// SleepContext pauses for a random short duration unless ctx is cancelled.
pkg/humanize/util.go:23:func SleepContext(ctx context.Context, min, max time.Duration) error {
pkg/humanize/util.go:24:	return sleepWithContext(ctx, randDuration(min, max))
pkg/humanize/util.go-25-}
pkg/humanize/util.go-26-
pkg/humanize/util.go:27:func sleepWithContext(ctx context.Context, d time.Duration) error {
pkg/humanize/util.go-28-	timer := time.NewTimer(d)
pkg/humanize/util.go-29-	defer timer.Stop()
pkg/humanize/util.go-30-
--
xiaohongshu/comment_feed.go-21-}
xiaohongshu/comment_feed.go-22-
xiaohongshu/comment_feed.go-23-// PostComment 发表评论到 Feed
xiaohongshu/comment_feed.go:24:func (f *CommentFeedAction) PostComment(ctx context.Context, feedID, xsecToken, content string) error {
xiaohongshu/comment_feed.go-25-	// 不使用 Context(ctx)，避免继承外部 context 的超时
xiaohongshu/comment_feed.go-26-	page := f.page.Timeout(60 * time.Second)
xiaohongshu/comment_feed.go-27-
--
xiaohongshu/comment_feed.go-31-	// 导航到详情页
xiaohongshu/comment_feed.go-32-	page.MustNavigate(url)
xiaohongshu/comment_feed.go-33-	page.MustWaitDOMStable()
xiaohongshu/comment_feed.go:34:	time.Sleep(1 * time.Second)
xiaohongshu/comment_feed.go-35-
xiaohongshu/comment_feed.go-36-	// 检测页面是否可访问
xiaohongshu/comment_feed.go-37-	if err := checkPageAccessible(page); err != nil {
--
xiaohongshu/comment_feed.go-60-		return fmt.Errorf("无法输入评论内容: %w", err)
xiaohongshu/comment_feed.go-61-	}
xiaohongshu/comment_feed.go-62-
xiaohongshu/comment_feed.go:63:	time.Sleep(1 * time.Second)
xiaohongshu/comment_feed.go-64-
xiaohongshu/comment_feed.go-65-	submitButton, err := page.Element("div.bottom button.submit")
xiaohongshu/comment_feed.go-66-	if err != nil {
--
xiaohongshu/comment_feed.go-73-		return fmt.Errorf("无法点击提交按钮: %w", err)
xiaohongshu/comment_feed.go-74-	}
xiaohongshu/comment_feed.go-75-
xiaohongshu/comment_feed.go:76:	time.Sleep(1 * time.Second)
xiaohongshu/comment_feed.go-77-
xiaohongshu/comment_feed.go-78-	logrus.Infof("Comment posted successfully to feed: %s", feedID)
xiaohongshu/comment_feed.go-79-	return nil
xiaohongshu/comment_feed.go-80-}
xiaohongshu/comment_feed.go-81-
xiaohongshu/comment_feed.go-82-// ReplyToComment 回复指定评论
xiaohongshu/comment_feed.go:83:func (f *CommentFeedAction) ReplyToComment(ctx context.Context, feedID, xsecToken, commentID, userID, content string) error {
xiaohongshu/comment_feed.go-84-	// 增加超时时间，因为需要滚动查找评论
xiaohongshu/comment_feed.go-85-	// 注意：不使用 Context(ctx)，避免继承外部 context 的超时
xiaohongshu/comment_feed.go-86-	page := f.page.Timeout(5 * time.Minute)
--
xiaohongshu/comment_feed.go-90-	// 导航到详情页
xiaohongshu/comment_feed.go-91-	page.MustNavigate(url)
xiaohongshu/comment_feed.go-92-	page.MustWaitDOMStable()
xiaohongshu/comment_feed.go:93:	time.Sleep(1 * time.Second)
xiaohongshu/comment_feed.go-94-
xiaohongshu/comment_feed.go-95-	// 检测页面是否可访问
xiaohongshu/comment_feed.go-96-	if err := checkPageAccessible(page); err != nil {
--
xiaohongshu/comment_feed.go-98-	}
xiaohongshu/comment_feed.go-99-
xiaohongshu/comment_feed.go-100-	// 等待评论容器加载
xiaohongshu/comment_feed.go:101:	time.Sleep(2 * time.Second)
xiaohongshu/comment_feed.go-102-
xiaohongshu/comment_feed.go-103-	// 使用 Go 实现的查找逻辑
xiaohongshu/comment_feed.go-104-	commentEl, err := findCommentElement(page, commentID, userID)
--
xiaohongshu/comment_feed.go-109-	// 滚动到评论位置
xiaohongshu/comment_feed.go-110-	logrus.Info("滚动到评论位置...")
xiaohongshu/comment_feed.go-111-	commentEl.MustScrollIntoView()
xiaohongshu/comment_feed.go:112:	time.Sleep(1 * time.Second)
xiaohongshu/comment_feed.go-113-
xiaohongshu/comment_feed.go-114-	logrus.Info("准备点击回复按钮")
xiaohongshu/comment_feed.go-115-
--
xiaohongshu/comment_feed.go-123-		return fmt.Errorf("点击回复按钮失败: %w", err)
xiaohongshu/comment_feed.go-124-	}
xiaohongshu/comment_feed.go-125-
xiaohongshu/comment_feed.go:126:	time.Sleep(1 * time.Second)
xiaohongshu/comment_feed.go-127-
xiaohongshu/comment_feed.go-128-	// 查找回复输入框
xiaohongshu/comment_feed.go-129-	inputEl, err := page.Element("div.input-box div.content-edit p.content-input")
--
xiaohongshu/comment_feed.go-136-		return fmt.Errorf("输入回复内容失败: %w", err)
xiaohongshu/comment_feed.go-137-	}
xiaohongshu/comment_feed.go-138-
xiaohongshu/comment_feed.go:139:	time.Sleep(500 * time.Millisecond)
xiaohongshu/comment_feed.go-140-
xiaohongshu/comment_feed.go-141-	// 查找并点击提交按钮
xiaohongshu/comment_feed.go-142-	submitBtn, err := page.Element("div.bottom button.submit")
--
xiaohongshu/comment_feed.go-148-		return fmt.Errorf("点击提交按钮失败: %w", err)
xiaohongshu/comment_feed.go-149-	}
xiaohongshu/comment_feed.go-150-
xiaohongshu/comment_feed.go:151:	time.Sleep(2 * time.Second)
xiaohongshu/comment_feed.go-152-	logrus.Infof("回复评论成功")
xiaohongshu/comment_feed.go-153-	return nil
xiaohongshu/comment_feed.go-154-}
--
xiaohongshu/comment_feed.go-162-
xiaohongshu/comment_feed.go-163-	// 先滚动到评论区
xiaohongshu/comment_feed.go-164-	scrollToCommentsArea(page)
xiaohongshu/comment_feed.go:165:	time.Sleep(1 * time.Second)
xiaohongshu/comment_feed.go-166-
xiaohongshu/comment_feed.go-167-	var lastCommentCount = 0
xiaohongshu/comment_feed.go-168-	stagnantChecks := 0
--
xiaohongshu/comment_feed.go-215-			} else {
xiaohongshu/comment_feed.go-216-				logrus.Warnf("未找到评论元素: %v", err)
xiaohongshu/comment_feed.go-217-			}
xiaohongshu/comment_feed.go:218:			time.Sleep(300 * time.Millisecond)
xiaohongshu/comment_feed.go-219-		}
xiaohongshu/comment_feed.go-220-
xiaohongshu/comment_feed.go-221-		// === 5. 继续向下滚动 ===
--
xiaohongshu/comment_feed.go-224-		if err := page.Actor().Mouse.Scroll(0, float64(viewportHeight)*0.8); err != nil {
xiaohongshu/comment_feed.go-225-			logrus.Warnf("滚动失败: %v", err)
xiaohongshu/comment_feed.go-226-		}
xiaohongshu/comment_feed.go:227:		time.Sleep(500 * time.Millisecond)
xiaohongshu/comment_feed.go-228-
xiaohongshu/comment_feed.go-229-		// === 6. 滚动后立即查找（边滚动边查找）===
xiaohongshu/comment_feed.go-230-		// 优先通过 commentID 查找（使用 Timeout 避免长时间等待）
--
xiaohongshu/comment_feed.go-266-		logrus.Infof("本次尝试未找到目标评论，继续下一轮...")
xiaohongshu/comment_feed.go-267-
xiaohongshu/comment_feed.go-268-		// === 7. 等待内容加载 ===
xiaohongshu/comment_feed.go:269:		time.Sleep(scrollInterval)
xiaohongshu/comment_feed.go-270-	}
xiaohongshu/comment_feed.go-271-
xiaohongshu/comment_feed.go-272-	return nil, fmt.Errorf("未找到评论 (commentID: %s, userID: %s), 尝试次数: %d", commentID, userID, maxAttempts)
--
xiaohongshu/navigate.go-14-	return &NavigateAction{page: page}
xiaohongshu/navigate.go-15-}
xiaohongshu/navigate.go-16-
xiaohongshu/navigate.go:17:func (n *NavigateAction) ToExplorePage(ctx context.Context) error {
xiaohongshu/navigate.go:18:	page := n.page.Context(ctx)
xiaohongshu/navigate.go-19-
xiaohongshu/navigate.go-20-	page.MustNavigate("https://www.xiaohongshu.com/explore").
xiaohongshu/navigate.go-21-		MustWaitLoad().
--
xiaohongshu/navigate.go-24-	return nil
xiaohongshu/navigate.go-25-}
xiaohongshu/navigate.go-26-
xiaohongshu/navigate.go:27:func (n *NavigateAction) ToProfilePage(ctx context.Context) error {
xiaohongshu/navigate.go:28:	page := n.page.Context(ctx)
xiaohongshu/navigate.go-29-
xiaohongshu/navigate.go-30-	// First navigate to explore page
xiaohongshu/navigate.go-31-	if err := n.ToExplorePage(ctx); err != nil {
--
xiaohongshu/like_favorite.go-43-	return &interactAction{page: page}
xiaohongshu/like_favorite.go-44-}
xiaohongshu/like_favorite.go-45-
xiaohongshu/like_favorite.go:46:func (a *interactAction) preparePage(ctx context.Context, actionType interactActionType, feedID, xsecToken string) *hrod.Page {
xiaohongshu/like_favorite.go:47:	page := a.page.Context(ctx).Timeout(60 * time.Second)
xiaohongshu/like_favorite.go-48-	url := makeFeedDetailURL(feedID, xsecToken)
xiaohongshu/like_favorite.go-49-	logrus.Infof("Opening feed detail page for %s: %s", actionType, url)
xiaohongshu/like_favorite.go-50-
xiaohongshu/like_favorite.go-51-	page.MustNavigate(url)
xiaohongshu/like_favorite.go-52-	page.MustWaitDOMStable()
xiaohongshu/like_favorite.go:53:	time.Sleep(1 * time.Second)
xiaohongshu/like_favorite.go-54-
xiaohongshu/like_favorite.go-55-	return page
xiaohongshu/like_favorite.go-56-}
--
xiaohongshu/like_favorite.go-70-}
xiaohongshu/like_favorite.go-71-
xiaohongshu/like_favorite.go-72-// Like 点赞指定笔记，如果已点赞则直接返回
xiaohongshu/like_favorite.go:73:func (a *LikeAction) Like(ctx context.Context, feedID, xsecToken string) error {
xiaohongshu/like_favorite.go-74-	return a.perform(ctx, feedID, xsecToken, true)
xiaohongshu/like_favorite.go-75-}
xiaohongshu/like_favorite.go-76-
xiaohongshu/like_favorite.go-77-// Unlike 取消点赞指定笔记，如果未点赞则直接返回
xiaohongshu/like_favorite.go:78:func (a *LikeAction) Unlike(ctx context.Context, feedID, xsecToken string) error {
xiaohongshu/like_favorite.go-79-	return a.perform(ctx, feedID, xsecToken, false)
xiaohongshu/like_favorite.go-80-}
xiaohongshu/like_favorite.go-81-
xiaohongshu/like_favorite.go:82:func (a *LikeAction) perform(ctx context.Context, feedID, xsecToken string, targetLiked bool) error {
xiaohongshu/like_favorite.go-83-	actionType := actionLike
xiaohongshu/like_favorite.go-84-	if !targetLiked {
xiaohongshu/like_favorite.go-85-		actionType = actionUnlike
--
xiaohongshu/like_favorite.go-107-
xiaohongshu/like_favorite.go-108-func (a *LikeAction) toggleLike(page *hrod.Page, feedID string, targetLiked bool, actionType interactActionType) error {
xiaohongshu/like_favorite.go-109-	a.performClick(page, SelectorLikeButton)
xiaohongshu/like_favorite.go:110:	time.Sleep(3 * time.Second)
xiaohongshu/like_favorite.go-111-
xiaohongshu/like_favorite.go-112-	liked, _, err := a.getInteractState(page, feedID)
xiaohongshu/like_favorite.go-113-	if err != nil {
--
xiaohongshu/like_favorite.go-121-
xiaohongshu/like_favorite.go-122-	logrus.Warnf("feed %s %s可能未成功，状态未变化，尝试再次点击", feedID, actionType)
xiaohongshu/like_favorite.go-123-	a.performClick(page, SelectorLikeButton)
xiaohongshu/like_favorite.go:124:	time.Sleep(2 * time.Second)
xiaohongshu/like_favorite.go-125-
xiaohongshu/like_favorite.go-126-	liked, _, err = a.getInteractState(page, feedID)
xiaohongshu/like_favorite.go-127-	if err != nil {
--
xiaohongshu/like_favorite.go-146-}
xiaohongshu/like_favorite.go-147-
xiaohongshu/like_favorite.go-148-// Favorite 收藏指定笔记，如果已收藏则直接返回
xiaohongshu/like_favorite.go:149:func (a *FavoriteAction) Favorite(ctx context.Context, feedID, xsecToken string) error {
xiaohongshu/like_favorite.go-150-	return a.perform(ctx, feedID, xsecToken, true)
xiaohongshu/like_favorite.go-151-}
xiaohongshu/like_favorite.go-152-
xiaohongshu/like_favorite.go-153-// Unfavorite 取消收藏指定笔记，如果未收藏则直接返回
xiaohongshu/like_favorite.go:154:func (a *FavoriteAction) Unfavorite(ctx context.Context, feedID, xsecToken string) error {
xiaohongshu/like_favorite.go-155-	return a.perform(ctx, feedID, xsecToken, false)
xiaohongshu/like_favorite.go-156-}
xiaohongshu/like_favorite.go-157-
xiaohongshu/like_favorite.go:158:func (a *FavoriteAction) perform(ctx context.Context, feedID, xsecToken string, targetCollected bool) error {
xiaohongshu/like_favorite.go-159-	actionType := actionFavorite
xiaohongshu/like_favorite.go-160-	if !targetCollected {
xiaohongshu/like_favorite.go-161-		actionType = actionUnfavorite
--
xiaohongshu/like_favorite.go-183-
xiaohongshu/like_favorite.go-184-func (a *FavoriteAction) toggleFavorite(page *hrod.Page, feedID string, targetCollected bool, actionType interactActionType) error {
xiaohongshu/like_favorite.go-185-	a.performClick(page, SelectorCollectButton)
xiaohongshu/like_favorite.go:186:	time.Sleep(3 * time.Second)
xiaohongshu/like_favorite.go-187-
xiaohongshu/like_favorite.go-188-	_, collected, err := a.getInteractState(page, feedID)
xiaohongshu/like_favorite.go-189-	if err != nil {
--
xiaohongshu/like_favorite.go-197-
xiaohongshu/like_favorite.go-198-	logrus.Warnf("feed %s %s可能未成功，状态未变化，尝试再次点击", feedID, actionType)
xiaohongshu/like_favorite.go-199-	a.performClick(page, SelectorCollectButton)
xiaohongshu/like_favorite.go:200:	time.Sleep(2 * time.Second)
xiaohongshu/like_favorite.go-201-
xiaohongshu/like_favorite.go-202-	_, collected, err = a.getInteractState(page, feedID)
xiaohongshu/like_favorite.go-203-	if err != nil {
--
xiaohongshu/login.go-16-	return &LoginAction{page: page}
xiaohongshu/login.go-17-}
xiaohongshu/login.go-18-
xiaohongshu/login.go:19:func (a *LoginAction) CheckLoginStatus(ctx context.Context) (bool, error) {
xiaohongshu/login.go:20:	pp := a.page.Context(ctx)
xiaohongshu/login.go-21-	pp.MustNavigate("https://www.xiaohongshu.com/explore").MustWaitLoad()
xiaohongshu/login.go-22-
xiaohongshu/login.go:23:	time.Sleep(1 * time.Second)
xiaohongshu/login.go-24-
xiaohongshu/login.go-25-	exists, _, err := pp.Has(`.main-container .user .link-wrapper .channel`)
xiaohongshu/login.go-26-	if err != nil {
--
xiaohongshu/login.go-34-	return true, nil
xiaohongshu/login.go-35-}
xiaohongshu/login.go-36-
xiaohongshu/login.go:37:func (a *LoginAction) Login(ctx context.Context) error {
xiaohongshu/login.go:38:	pp := a.page.Context(ctx)
xiaohongshu/login.go-39-
xiaohongshu/login.go-40-	// 导航到小红书首页，这会触发二维码弹窗
xiaohongshu/login.go-41-	pp.MustNavigate("https://www.xiaohongshu.com/explore").MustWaitLoad()
xiaohongshu/login.go-42-
xiaohongshu/login.go-43-	// 等待一小段时间让页面完全加载
xiaohongshu/login.go:44:	time.Sleep(2 * time.Second)
xiaohongshu/login.go-45-
xiaohongshu/login.go-46-	// 检查是否已经登录
xiaohongshu/login.go-47-	if exists, _, _ := pp.Has(".main-container .user .link-wrapper .channel"); exists {
--
xiaohongshu/login.go-56-	return nil
xiaohongshu/login.go-57-}
xiaohongshu/login.go-58-
xiaohongshu/login.go:59:func (a *LoginAction) FetchQrcodeImage(ctx context.Context) (string, bool, error) {
xiaohongshu/login.go:60:	pp := a.page.Context(ctx)
xiaohongshu/login.go-61-
xiaohongshu/login.go-62-	// 导航到小红书首页，这会触发二维码弹窗
xiaohongshu/login.go-63-	pp.MustNavigate("https://www.xiaohongshu.com/explore").MustWaitLoad()
xiaohongshu/login.go-64-
xiaohongshu/login.go-65-	// 等待一小段时间让页面完全加载
xiaohongshu/login.go:66:	time.Sleep(2 * time.Second)
xiaohongshu/login.go-67-
xiaohongshu/login.go-68-	// 检查是否已经登录
xiaohongshu/login.go-69-	if exists, _, _ := pp.Has(".main-container .user .link-wrapper .channel"); exists {
--
xiaohongshu/login.go-82-	return *src, false, nil
xiaohongshu/login.go-83-}
xiaohongshu/login.go-84-
xiaohongshu/login.go:85:func (a *LoginAction) WaitForLogin(ctx context.Context) bool {
xiaohongshu/login.go:86:	pp := a.page.Context(ctx)
xiaohongshu/login.go-87-	ticker := time.NewTicker(500 * time.Millisecond)
xiaohongshu/login.go-88-	defer ticker.Stop()
xiaohongshu/login.go-89-
--
xiaohongshu/publish.go-50-	if err := pp.WaitLoad(); err != nil {
xiaohongshu/publish.go-51-		logrus.Warnf("等待页面加载出现问题: %v，继续尝试", err)
xiaohongshu/publish.go-52-	}
xiaohongshu/publish.go:53:	time.Sleep(2 * time.Second)
xiaohongshu/publish.go-54-
xiaohongshu/publish.go-55-	// 等待页面稳定
xiaohongshu/publish.go-56-	if err := pp.WaitDOMStable(time.Second, 0.1); err != nil {
xiaohongshu/publish.go-57-		logrus.Warnf("等待 DOM 稳定出现问题: %v，继续尝试", err)
xiaohongshu/publish.go-58-	}
xiaohongshu/publish.go:59:	time.Sleep(1 * time.Second)
xiaohongshu/publish.go-60-
xiaohongshu/publish.go-61-	if err := mustClickPublishTab(pp, "上传图文"); err != nil {
xiaohongshu/publish.go-62-		logrus.Errorf("点击上传图文 TAB 失败: %v", err)
xiaohongshu/publish.go-63-		return nil, err
xiaohongshu/publish.go-64-	}
xiaohongshu/publish.go-65-
xiaohongshu/publish.go:66:	time.Sleep(1 * time.Second)
xiaohongshu/publish.go-67-
xiaohongshu/publish.go-68-	return &PublishAction{
xiaohongshu/publish.go-69-		page: pp,
xiaohongshu/publish.go-70-	}, nil
xiaohongshu/publish.go-71-}
xiaohongshu/publish.go-72-
xiaohongshu/publish.go:73:func (p *PublishAction) Publish(ctx context.Context, content PublishImageContent) error {
xiaohongshu/publish.go-74-	if len(content.ImagePaths) == 0 {
xiaohongshu/publish.go-75-		return errors.New("图片不能为空")
xiaohongshu/publish.go-76-	}
xiaohongshu/publish.go-77-
xiaohongshu/publish.go:78:	page := p.page.Context(ctx)
xiaohongshu/publish.go-79-
xiaohongshu/publish.go-80-	if err := uploadImages(page, content.ImagePaths); err != nil {
xiaohongshu/publish.go-81-		return errors.Wrap(err, "小红书上传图片失败")
--
xiaohongshu/publish.go-127-		tab, blocked, err := getTabElement(page, tabname)
xiaohongshu/publish.go-128-		if err != nil {
xiaohongshu/publish.go-129-			logrus.Warnf("获取发布 TAB 元素失败: %v", err)
xiaohongshu/publish.go:130:			time.Sleep(200 * time.Millisecond)
xiaohongshu/publish.go-131-			continue
xiaohongshu/publish.go-132-		}
xiaohongshu/publish.go-133-
xiaohongshu/publish.go-134-		if tab == nil {
xiaohongshu/publish.go:135:			time.Sleep(200 * time.Millisecond)
xiaohongshu/publish.go-136-			continue
xiaohongshu/publish.go-137-		}
xiaohongshu/publish.go-138-
xiaohongshu/publish.go-139-		if blocked {
xiaohongshu/publish.go-140-			logrus.Info("发布 TAB 被遮挡，尝试移除遮挡")
xiaohongshu/publish.go-141-			removePopCover(page)
xiaohongshu/publish.go:142:			time.Sleep(200 * time.Millisecond)
xiaohongshu/publish.go-143-			continue
xiaohongshu/publish.go-144-		}
xiaohongshu/publish.go-145-
xiaohongshu/publish.go-146-		if err := tab.Click(proto.InputMouseButtonLeft, 1); err != nil {
xiaohongshu/publish.go-147-			logrus.Warnf("点击发布 TAB 失败: %v", err)
xiaohongshu/publish.go:148:			time.Sleep(200 * time.Millisecond)
xiaohongshu/publish.go-149-			continue
xiaohongshu/publish.go-150-		}
xiaohongshu/publish.go-151-
--
xiaohongshu/publish.go-238-		if err := waitForUploadComplete(page, i+1); err != nil {
xiaohongshu/publish.go-239-			return errors.Wrapf(err, "第%d张图片上传超时", i+1)
xiaohongshu/publish.go-240-		}
xiaohongshu/publish.go:241:		time.Sleep(1 * time.Second)
xiaohongshu/publish.go-242-	}
xiaohongshu/publish.go-243-
xiaohongshu/publish.go-244-	return nil
--
xiaohongshu/publish.go-254-	for time.Since(start) < maxWaitTime {
xiaohongshu/publish.go-255-		uploadedImages, err := page.Elements(".img-preview-area .pr")
xiaohongshu/publish.go-256-		if err != nil {
xiaohongshu/publish.go:257:			time.Sleep(checkInterval)
xiaohongshu/publish.go-258-			continue
xiaohongshu/publish.go-259-		}
xiaohongshu/publish.go-260-
--
xiaohongshu/publish.go-269-			return nil
xiaohongshu/publish.go-270-		}
xiaohongshu/publish.go-271-
xiaohongshu/publish.go:272:		time.Sleep(checkInterval)
xiaohongshu/publish.go-273-	}
xiaohongshu/publish.go-274-
xiaohongshu/publish.go-275-	return errors.Errorf("第%d张图片上传超时(60s)，请检查网络连接和图片大小", expectedCount)
--
xiaohongshu/publish.go-285-	}
xiaohongshu/publish.go-286-
xiaohongshu/publish.go-287-	// 检查标题长度
xiaohongshu/publish.go:288:	time.Sleep(500 * time.Millisecond)
xiaohongshu/publish.go-289-	if err := checkTitleMaxLength(page); err != nil {
xiaohongshu/publish.go-290-		return err
xiaohongshu/publish.go-291-	}
xiaohongshu/publish.go-292-	slog.Info("检查标题长度：通过")
xiaohongshu/publish.go-293-
xiaohongshu/publish.go:294:	time.Sleep(1 * time.Second)
xiaohongshu/publish.go-295-
xiaohongshu/publish.go-296-	contentElem, ok := getContentElement(page)
xiaohongshu/publish.go-297-	if !ok {
--
xiaohongshu/publish.go-307-		return err
xiaohongshu/publish.go-308-	}
xiaohongshu/publish.go-309-
xiaohongshu/publish.go:310:	time.Sleep(1 * time.Second)
xiaohongshu/publish.go-311-
xiaohongshu/publish.go-312-	// 检查正文长度
xiaohongshu/publish.go-313-	if err := checkContentMaxLength(page); err != nil {
--
xiaohongshu/publish.go-346-		return err
xiaohongshu/publish.go-347-	}
xiaohongshu/publish.go-348-
xiaohongshu/publish.go:349:	time.Sleep(3 * time.Second)
xiaohongshu/publish.go-350-	return nil
xiaohongshu/publish.go-351-}
xiaohongshu/publish.go-352-
--
xiaohongshu/publish.go-383-		btn, disabledReason, err := findPublishButton(page)
xiaohongshu/publish.go-384-		if err != nil {
xiaohongshu/publish.go-385-			slog.Warn("查找发布按钮失败，继续等待", "error", err)
xiaohongshu/publish.go:386:			time.Sleep(interval)
xiaohongshu/publish.go-387-			continue
xiaohongshu/publish.go-388-		}
xiaohongshu/publish.go-389-		if btn != nil && disabledReason == "" {
--
xiaohongshu/publish.go-392-		if disabledReason != "" {
xiaohongshu/publish.go-393-			lastDisabledReason = disabledReason
xiaohongshu/publish.go-394-		}
xiaohongshu/publish.go:395:		time.Sleep(interval)
xiaohongshu/publish.go-396-	}
xiaohongshu/publish.go-397-
xiaohongshu/publish.go-398-	if lastDisabledReason != "" {
--
xiaohongshu/publish.go-486-// waitAndClickTitleInput 在填写正文后等待 1 秒并回点标题输入框，增强后续交互稳定性
xiaohongshu/publish.go-487-func waitAndClickTitleInput(titleElem *hrod.Element) error {
xiaohongshu/publish.go-488-	slog.Info("正文填写完成，准备等待后回点标题输入框")
xiaohongshu/publish.go:489:	time.Sleep(1 * time.Second)
xiaohongshu/publish.go-490-	if err := titleElem.Click(proto.InputMouseButtonLeft, 1); err != nil {
xiaohongshu/publish.go-491-		return errors.Wrap(err, "回点标题输入框失败")
xiaohongshu/publish.go-492-	}
--
xiaohongshu/publish.go-577-		return nil
xiaohongshu/publish.go-578-	}
xiaohongshu/publish.go-579-
xiaohongshu/publish.go:580:	time.Sleep(1 * time.Second)
xiaohongshu/publish.go-581-
xiaohongshu/publish.go-582-	for i := 0; i < 20; i++ {
xiaohongshu/publish.go-583-		ka, err := contentElem.KeyActions()
--
xiaohongshu/publish.go-587-		if err := ka.Type(input.ArrowDown).Do(); err != nil {
xiaohongshu/publish.go-588-			return errors.Wrap(err, "按下方向键失败")
xiaohongshu/publish.go-589-		}
xiaohongshu/publish.go:590:		time.Sleep(10 * time.Millisecond)
xiaohongshu/publish.go-591-	}
xiaohongshu/publish.go-592-
xiaohongshu/publish.go-593-	ka, err := contentElem.KeyActions()
--
xiaohongshu/publish.go-598-		return errors.Wrap(err, "按下回车键失败")
xiaohongshu/publish.go-599-	}
xiaohongshu/publish.go-600-
xiaohongshu/publish.go:601:	time.Sleep(1 * time.Second)
xiaohongshu/publish.go-602-
xiaohongshu/publish.go-603-	for _, tag := range tags {
xiaohongshu/publish.go-604-		tag = strings.TrimLeft(tag, "#")
--
xiaohongshu/publish.go-613-	if err := contentElem.Input("#"); err != nil {
xiaohongshu/publish.go-614-		return errors.Wrap(err, "输入#失败")
xiaohongshu/publish.go-615-	}
xiaohongshu/publish.go:616:	time.Sleep(200 * time.Millisecond)
xiaohongshu/publish.go-617-
xiaohongshu/publish.go-618-	for _, char := range tag {
xiaohongshu/publish.go-619-		if err := contentElem.Input(string(char)); err != nil {
xiaohongshu/publish.go-620-			return errors.Wrapf(err, "输入字符[%c]失败", char)
xiaohongshu/publish.go-621-		}
xiaohongshu/publish.go:622:		time.Sleep(50 * time.Millisecond)
xiaohongshu/publish.go-623-	}
xiaohongshu/publish.go-624-
xiaohongshu/publish.go:625:	time.Sleep(1 * time.Second)
xiaohongshu/publish.go-626-
xiaohongshu/publish.go-627-	page := contentElem.Page()
xiaohongshu/publish.go-628-	topicContainer, err := page.Element("#creator-editor-topic-container")
--
xiaohongshu/publish.go-641-		return errors.Wrap(err, "点击标签联想选项失败")
xiaohongshu/publish.go-642-	}
xiaohongshu/publish.go-643-	slog.Info("成功点击标签联想选项", "tag", tag)
xiaohongshu/publish.go:644:	time.Sleep(200 * time.Millisecond)
xiaohongshu/publish.go-645-
xiaohongshu/publish.go:646:	time.Sleep(500 * time.Millisecond) // 等待标签处理完成
xiaohongshu/publish.go-647-	return nil
xiaohongshu/publish.go-648-}
xiaohongshu/publish.go-649-
--
xiaohongshu/publish.go-787-	if err := dropdown.Click(proto.InputMouseButtonLeft, 1); err != nil {
xiaohongshu/publish.go-788-		return errors.Wrap(err, "点击可见范围下拉框失败")
xiaohongshu/publish.go-789-	}
xiaohongshu/publish.go:790:	time.Sleep(500 * time.Millisecond)
xiaohongshu/publish.go-791-
xiaohongshu/publish.go-792-	// 在弹窗中查找并点击目标选项
xiaohongshu/publish.go-793-	opts, err := page.Elements("div.d-options-wrapper div.d-grid-item div.custom-option")
--
xiaohongshu/publish.go-804-				return errors.Wrap(err, "选择可见范围失败")
xiaohongshu/publish.go-805-			}
xiaohongshu/publish.go-806-			slog.Info("已设置可见范围", "visibility", visibility)
xiaohongshu/publish.go:807:			time.Sleep(200 * time.Millisecond)
xiaohongshu/publish.go-808-			return nil
xiaohongshu/publish.go-809-		}
xiaohongshu/publish.go-810-	}
--
xiaohongshu/publish.go-817-	if err := clickScheduleSwitch(page); err != nil {
xiaohongshu/publish.go-818-		return err
xiaohongshu/publish.go-819-	}
xiaohongshu/publish.go:820:	time.Sleep(800 * time.Millisecond)
xiaohongshu/publish.go-821-
xiaohongshu/publish.go-822-	// 2. 设置日期时间
xiaohongshu/publish.go-823-	if err := setDateTime(page, t); err != nil {
xiaohongshu/publish.go-824-		return err
xiaohongshu/publish.go-825-	}
xiaohongshu/publish.go:826:	time.Sleep(500 * time.Millisecond)
xiaohongshu/publish.go-827-
xiaohongshu/publish.go-828-	return nil
xiaohongshu/publish.go-829-}
--
xiaohongshu/publish.go-911-			return errors.Wrap(err, "点击原创声明开关失败")
xiaohongshu/publish.go-912-		}
xiaohongshu/publish.go-913-
xiaohongshu/publish.go:914:		time.Sleep(500 * time.Millisecond)
xiaohongshu/publish.go-915-
xiaohongshu/publish.go-916-		// 处理原创声明确认弹窗
xiaohongshu/publish.go-917-		if err := confirmOriginalDeclaration(page); err != nil {
--
xiaohongshu/publish.go-928-// confirmOriginalDeclaration 处理原创声明确认弹窗
xiaohongshu/publish.go-929-func confirmOriginalDeclaration(page *hrod.Page) error {
xiaohongshu/publish.go-930-	// 等待确认弹窗出现
xiaohongshu/publish.go:931:	time.Sleep(800 * time.Millisecond)
xiaohongshu/publish.go-932-
xiaohongshu/publish.go-933-	// 查找包含"原创声明须知"的 footer 并勾选 checkbox
xiaohongshu/publish.go-934-	if noticeFooter, err := page.ElementR("div.footer", "原创声明须知"); err == nil {
--
xiaohongshu/publish.go-946-		slog.Warn("未找到原创声明须知弹窗", "error", err)
xiaohongshu/publish.go-947-	}
xiaohongshu/publish.go-948-
xiaohongshu/publish.go:949:	time.Sleep(500 * time.Millisecond)
xiaohongshu/publish.go-950-
xiaohongshu/publish.go-951-	// 查找包含"声明原创"的 footer
xiaohongshu/publish.go-952-	confirmFooter, err := page.ElementR("div.footer", "声明原创")
--
xiaohongshu/publish.go-975-				_ = checkbox.Click(proto.InputMouseButtonLeft, 1)
xiaohongshu/publish.go-976-			}
xiaohongshu/publish.go-977-		}
xiaohongshu/publish.go:978:		time.Sleep(300 * time.Millisecond)
xiaohongshu/publish.go-979-		if isDisabled(btn) {
xiaohongshu/publish.go-980-			return errors.New("声明原创按钮仍处于禁用状态")
xiaohongshu/publish.go-981-		}
--
xiaohongshu/publish.go-986-	}
xiaohongshu/publish.go-987-
xiaohongshu/publish.go-988-	slog.Info("已成功点击声明原创按钮")
xiaohongshu/publish.go:989:	time.Sleep(300 * time.Millisecond)
xiaohongshu/publish.go-990-
xiaohongshu/publish.go-991-	return nil
xiaohongshu/publish.go-992-}
--
xiaohongshu/publish.go-1003-	if err := clickAddProductButton(page); err != nil {
xiaohongshu/publish.go-1004-		return errors.Wrap(err, "点击添加商品按钮失败")
xiaohongshu/publish.go-1005-	}
xiaohongshu/publish.go:1006:	time.Sleep(1 * time.Second)
xiaohongshu/publish.go-1007-
xiaohongshu/publish.go-1008-	// 等待商品选择弹窗出现
xiaohongshu/publish.go-1009-	modal, err := waitForProductModal(page)
--
xiaohongshu/publish.go-1019-			slog.Warn("搜索选择商品失败", "keyword", keyword, "error", err)
xiaohongshu/publish.go-1020-			failedProducts = append(failedProducts, keyword)
xiaohongshu/publish.go-1021-		}
xiaohongshu/publish.go:1022:		time.Sleep(500 * time.Millisecond)
xiaohongshu/publish.go-1023-	}
xiaohongshu/publish.go-1024-
xiaohongshu/publish.go-1025-	// 点击保存按钮
--
xiaohongshu/publish.go-1041-	}
xiaohongshu/publish.go-1042-
xiaohongshu/publish.go-1043-	slog.Info("商品绑定完成", "total", len(products))
xiaohongshu/publish.go:1044:	time.Sleep(1000 * time.Millisecond)
xiaohongshu/publish.go-1045-	return nil
xiaohongshu/publish.go-1046-}
xiaohongshu/publish.go-1047-
--
xiaohongshu/publish.go-1083-						return errors.Wrap(err, "点击添加商品按钮失败")
xiaohongshu/publish.go-1084-					}
xiaohongshu/publish.go-1085-					slog.Info("已点击添加商品按钮")
xiaohongshu/publish.go:1086:					time.Sleep(300 * time.Millisecond) // 确保弹窗动画开始
xiaohongshu/publish.go-1087-					return nil
xiaohongshu/publish.go-1088-				}
xiaohongshu/publish.go-1089-
--
xiaohongshu/publish.go-1093-						return errors.Wrap(err, "点击添加商品按钮失败")
xiaohongshu/publish.go-1094-					}
xiaohongshu/publish.go-1095-					slog.Info("已点击添加商品按钮")
xiaohongshu/publish.go:1096:					time.Sleep(300 * time.Millisecond) // 确保弹窗动画开始
xiaohongshu/publish.go-1097-					return nil
xiaohongshu/publish.go-1098-				}
xiaohongshu/publish.go-1099-			}
--
xiaohongshu/publish.go-1116-				return modal, nil
xiaohongshu/publish.go-1117-			}
xiaohongshu/publish.go-1118-		}
xiaohongshu/publish.go:1119:		time.Sleep(100 * time.Millisecond) // 缩短轮询间隔，更快响应
xiaohongshu/publish.go-1120-	}
xiaohongshu/publish.go-1121-
xiaohongshu/publish.go-1122-	return nil, errors.New("等待商品选择弹窗超时")
--
xiaohongshu/publish.go-1136-	if err := searchInput.SelectAllText(); err != nil {
xiaohongshu/publish.go-1137-		slog.Warn("选择搜索框文本失败", "error", err)
xiaohongshu/publish.go-1138-	}
xiaohongshu/publish.go:1139:	time.Sleep(100 * time.Millisecond)
xiaohongshu/publish.go-1140-
xiaohongshu/publish.go-1141-	// 使用 rod Input 输入关键词
xiaohongshu/publish.go-1142-	if err := searchInput.Input(keyword); err != nil {
xiaohongshu/publish.go-1143-		return errors.Wrap(err, "输入搜索关键词失败")
xiaohongshu/publish.go-1144-	}
xiaohongshu/publish.go:1145:	time.Sleep(300 * time.Millisecond)
xiaohongshu/publish.go-1146-
xiaohongshu/publish.go-1147-	// 3. 触发搜索（模拟键盘 Enter）
xiaohongshu/publish.go-1148-	if err := page.Actor().Keyboard.Press(input.Enter); err != nil {
--
xiaohongshu/publish.go-1150-	}
xiaohongshu/publish.go-1151-
xiaohongshu/publish.go-1152-	// 4. 等待搜索结果加载
xiaohongshu/publish.go:1153:	time.Sleep(1 * time.Second)
xiaohongshu/publish.go-1154-
xiaohongshu/publish.go-1155-	// 等待 loading 消失（使用与工作代码相同的选择器）
xiaohongshu/publish.go-1156-	deadline := time.Now().Add(10 * time.Second)
--
xiaohongshu/publish.go-1163-		if !visible {
xiaohongshu/publish.go-1164-			break
xiaohongshu/publish.go-1165-		}
xiaohongshu/publish.go:1166:		time.Sleep(100 * time.Millisecond)
xiaohongshu/publish.go-1167-	}
xiaohongshu/publish.go-1168-
xiaohongshu/publish.go-1169-	// 等待商品列表渲染完成（使用与工作代码相同的选择器）
--
xiaohongshu/publish.go-1172-		if err == nil && productList != nil {
xiaohongshu/publish.go-1173-			break
xiaohongshu/publish.go-1174-		}
xiaohongshu/publish.go:1175:		time.Sleep(100 * time.Millisecond)
xiaohongshu/publish.go-1176-	}
xiaohongshu/publish.go:1177:	time.Sleep(500 * time.Millisecond) // 额外等待确保渲染完成
xiaohongshu/publish.go-1178-
xiaohongshu/publish.go-1179-	// 5. 点击第一个商品的 checkbox（使用与工作代码相同的选择器）
xiaohongshu/publish.go-1180-	checkbox, err := modal.Element(".goods-list-normal .good-card-container .d-checkbox")
--
xiaohongshu/publish.go-1198-
xiaohongshu/publish.go-1199-	// 6. 随机延迟模拟人为操作（800-1500ms）
xiaohongshu/publish.go-1200-	randomDelay := 800 + rand.Intn(700)
xiaohongshu/publish.go:1201:	time.Sleep(time.Duration(randomDelay) * time.Millisecond)
xiaohongshu/publish.go-1202-
xiaohongshu/publish.go-1203-	slog.Info("已选择商品", "keyword", keyword)
xiaohongshu/publish.go-1204-	return nil
--
xiaohongshu/publish.go-1244-			slog.Info("弹窗已关闭")
xiaohongshu/publish.go-1245-			return nil
xiaohongshu/publish.go-1246-		}
xiaohongshu/publish.go:1247:		time.Sleep(200 * time.Millisecond)
xiaohongshu/publish.go-1248-	}
xiaohongshu/publish.go-1249-
xiaohongshu/publish.go-1250-	return errors.New("等待弹窗关闭超时")
--
xiaohongshu/feeds.go-24-}
xiaohongshu/feeds.go-25-
xiaohongshu/feeds.go-26-// GetFeedsList 获取页面的 Feed 列表数据
xiaohongshu/feeds.go:27:func (f *FeedsListAction) GetFeedsList(ctx context.Context) ([]Feed, error) {
xiaohongshu/feeds.go:28:	page := f.page.Context(ctx)
xiaohongshu/feeds.go-29-
xiaohongshu/feeds.go:30:	time.Sleep(1 * time.Second)
xiaohongshu/feeds.go-31-
xiaohongshu/feeds.go-32-	result := page.MustEval(`() => {
xiaohongshu/feeds.go-33-		if (window.__INITIAL_STATE__ &&
--
xiaohongshu/publish_video.go-34-	if err := pp.WaitLoad(); err != nil {
xiaohongshu/publish_video.go-35-		logrus.Warnf("等待页面加载出现问题: %v，继续尝试", err)
xiaohongshu/publish_video.go-36-	}
xiaohongshu/publish_video.go:37:	time.Sleep(2 * time.Second)
xiaohongshu/publish_video.go-38-
xiaohongshu/publish_video.go-39-	if err := pp.WaitDOMStable(time.Second, 0.1); err != nil {
xiaohongshu/publish_video.go-40-		logrus.Warnf("等待 DOM 稳定出现问题: %v，继续尝试", err)
xiaohongshu/publish_video.go-41-	}
xiaohongshu/publish_video.go:42:	time.Sleep(1 * time.Second)
xiaohongshu/publish_video.go-43-
xiaohongshu/publish_video.go-44-	if err := mustClickPublishTab(pp, "上传视频"); err != nil {
xiaohongshu/publish_video.go-45-		return nil, errors.Wrap(err, "切换到上传视频失败")
xiaohongshu/publish_video.go-46-	}
xiaohongshu/publish_video.go-47-
xiaohongshu/publish_video.go:48:	time.Sleep(1 * time.Second)
xiaohongshu/publish_video.go-49-
xiaohongshu/publish_video.go-50-	return &PublishAction{page: pp}, nil
xiaohongshu/publish_video.go-51-}
xiaohongshu/publish_video.go-52-
xiaohongshu/publish_video.go-53-// PublishVideo 上传视频并提交
xiaohongshu/publish_video.go:54:func (p *PublishAction) PublishVideo(ctx context.Context, content PublishVideoContent) error {
xiaohongshu/publish_video.go-55-	if content.VideoPath == "" {
xiaohongshu/publish_video.go-56-		return errors.New("视频不能为空")
xiaohongshu/publish_video.go-57-	}
xiaohongshu/publish_video.go-58-
xiaohongshu/publish_video.go:59:	page := p.page.Context(ctx)
xiaohongshu/publish_video.go-60-
xiaohongshu/publish_video.go-61-	if err := uploadVideo(page, content.VideoPath); err != nil {
xiaohongshu/publish_video.go-62-		return errors.Wrap(err, "小红书上传视频失败")
--
xiaohongshu/publish_video.go-108-	if err := titleElem.Input(title); err != nil {
xiaohongshu/publish_video.go-109-		return errors.Wrap(err, "输入标题失败")
xiaohongshu/publish_video.go-110-	}
xiaohongshu/publish_video.go:111:	time.Sleep(1 * time.Second)
xiaohongshu/publish_video.go-112-
xiaohongshu/publish_video.go-113-	// 正文 + 标签
xiaohongshu/publish_video.go-114-	contentElem, ok := getContentElement(page)
--
xiaohongshu/publish_video.go-125-		return err
xiaohongshu/publish_video.go-126-	}
xiaohongshu/publish_video.go-127-
xiaohongshu/publish_video.go:128:	time.Sleep(1 * time.Second)
xiaohongshu/publish_video.go-129-
xiaohongshu/publish_video.go-130-	// 处理定时发布
xiaohongshu/publish_video.go-131-	if scheduleTime != nil {
--
xiaohongshu/publish_video.go-149-		return err
xiaohongshu/publish_video.go-150-	}
xiaohongshu/publish_video.go-151-
xiaohongshu/publish_video.go:152:	time.Sleep(3 * time.Second)
xiaohongshu/publish_video.go-153-	return nil
xiaohongshu/publish_video.go-154-}
--
xiaohongshu/user_profile.go-19-}
xiaohongshu/user_profile.go-20-
xiaohongshu/user_profile.go-21-// UserProfile 获取用户基本信息及帖子
xiaohongshu/user_profile.go:22:func (u *UserProfileAction) UserProfile(ctx context.Context, userID, xsecToken string) (*UserProfileResponse, error) {
xiaohongshu/user_profile.go:23:	page := u.page.Context(ctx)
xiaohongshu/user_profile.go-24-
xiaohongshu/user_profile.go-25-	searchURL := makeUserProfileURL(userID, xsecToken)
xiaohongshu/user_profile.go-26-	page.MustNavigate(searchURL)
--
xiaohongshu/user_profile.go-104-	return fmt.Sprintf("https://www.xiaohongshu.com/user/profile/%s?xsec_token=%s&xsec_source=pc_note", userID, xsecToken)
xiaohongshu/user_profile.go-105-}
xiaohongshu/user_profile.go-106-
xiaohongshu/user_profile.go:107:func (u *UserProfileAction) GetMyProfileViaSidebar(ctx context.Context) (*UserProfileResponse, error) {
xiaohongshu/user_profile.go:108:	page := u.page.Context(ctx)
xiaohongshu/user_profile.go-109-
xiaohongshu/user_profile.go-110-	// 创建导航动作
xiaohongshu/user_profile.go-111-	navigate := NewNavigate(page)
--
xiaohongshu/search.go-166-	return &SearchAction{page: pp}
xiaohongshu/search.go-167-}
xiaohongshu/search.go-168-
xiaohongshu/search.go:169:func (s *SearchAction) Search(ctx context.Context, keyword string, filters ...FilterOption) ([]Feed, error) {
xiaohongshu/search.go:170:	page := s.page.Context(ctx)
xiaohongshu/search.go-171-
xiaohongshu/search.go-172-	searchURL := makeSearchURL(keyword)
xiaohongshu/search.go-173-	page.MustNavigate(searchURL)
--
xiaohongshu/feed_detail.go-72-
xiaohongshu/feed_detail.go-73-// ========== 主要业务逻辑 ==========
xiaohongshu/feed_detail.go-74-
xiaohongshu/feed_detail.go:75:func (f *FeedDetailAction) GetFeedDetail(ctx context.Context, feedID, xsecToken string, loadAllComments bool, config CommentLoadConfig) (*FeedDetailResponse, error) {
xiaohongshu/feed_detail.go-76-	return f.GetFeedDetailWithConfig(ctx, feedID, xsecToken, loadAllComments, config)
xiaohongshu/feed_detail.go-77-}
xiaohongshu/feed_detail.go-78-
xiaohongshu/feed_detail.go:79:func (f *FeedDetailAction) GetFeedDetailWithConfig(ctx context.Context, feedID, xsecToken string, loadAllComments bool, config CommentLoadConfig) (*FeedDetailResponse, error) {
xiaohongshu/feed_detail.go:80:	page := f.page.Context(ctx).Timeout(10 * time.Minute)
xiaohongshu/feed_detail.go-81-	url := makeFeedDetailURL(feedID, xsecToken)
xiaohongshu/feed_detail.go-82-
xiaohongshu/feed_detail.go-83-	logrus.Infof("打开 feed 详情页: %s", url)
--
xiaohongshu/feed_detail.go-102-		logrus.Errorf("页面导航失败: %v", err)
xiaohongshu/feed_detail.go-103-		return nil, err
xiaohongshu/feed_detail.go-104-	}
xiaohongshu/feed_detail.go:105:	sleepRandom(1000, 1000)
xiaohongshu/feed_detail.go-106-
xiaohongshu/feed_detail.go-107-	if err := checkPageAccessible(page); err != nil {
xiaohongshu/feed_detail.go-108-		return nil, err
--
xiaohongshu/feed_detail.go-155-
xiaohongshu/feed_detail.go-156-	logrus.Info("开始加载评论...")
xiaohongshu/feed_detail.go-157-	scrollToCommentsArea(cl.page)
xiaohongshu/feed_detail.go:158:	sleepRandom(humanDelayRange.min, humanDelayRange.max)
xiaohongshu/feed_detail.go-159-
xiaohongshu/feed_detail.go-160-	// 检查是否没有评论
xiaohongshu/feed_detail.go-161-	if cl.checkNoComments() {
--
xiaohongshu/feed_detail.go-183-		cl.performScroll()
xiaohongshu/feed_detail.go-184-		cl.handleStagnation()
xiaohongshu/feed_detail.go-185-
xiaohongshu/feed_detail.go:186:		time.Sleep(scrollInterval)
xiaohongshu/feed_detail.go-187-	}
xiaohongshu/feed_detail.go-188-
xiaohongshu/feed_detail.go-189-	cl.performFinalSprint()
--
xiaohongshu/feed_detail.go-209-	if checkEndContainer(cl.page) {
xiaohongshu/feed_detail.go-210-		currentCount := getCommentCount(cl.page)
xiaohongshu/feed_detail.go-211-		logrus.Infof("✓ 检测到 'THE END' 元素，已滑动到底部")
xiaohongshu/feed_detail.go:212:		sleepRandom(humanDelayRange.min, humanDelayRange.max)
xiaohongshu/feed_detail.go-213-		logrus.Infof("✓ 加载完成: %d 条评论, 尝试次数: %d, 点击: %d, 跳过: %d",
xiaohongshu/feed_detail.go-214-			currentCount, cl.stats.attempts+1, cl.stats.totalClicked, cl.stats.totalSkipped)
xiaohongshu/feed_detail.go-215-		return true
--
xiaohongshu/feed_detail.go-229-		logrus.Infof("点击'更多': %d 个, 跳过: %d 个, 累计点击: %d, 累计跳过: %d",
xiaohongshu/feed_detail.go-230-			clicked, skipped, cl.stats.totalClicked, cl.stats.totalSkipped)
xiaohongshu/feed_detail.go-231-
xiaohongshu/feed_detail.go:232:		sleepRandom(readTimeRange.min, readTimeRange.max)
xiaohongshu/feed_detail.go-233-
xiaohongshu/feed_detail.go-234-		// 重试一轮
xiaohongshu/feed_detail.go-235-		clicked2, skipped2 := clickShowMoreButtonsSmart(cl.page, cl.config.MaxRepliesThreshold)
--
xiaohongshu/feed_detail.go-237-			cl.stats.totalClicked += clicked2
xiaohongshu/feed_detail.go-238-			cl.stats.totalSkipped += skipped2
xiaohongshu/feed_detail.go-239-			logrus.Infof("第 2 轮: 点击 %d, 跳过 %d", clicked2, skipped2)
xiaohongshu/feed_detail.go:240:			sleepRandom(shortReadRange.min, shortReadRange.max)
xiaohongshu/feed_detail.go-241-		}
xiaohongshu/feed_detail.go-242-	}
xiaohongshu/feed_detail.go-243-}
--
xiaohongshu/feed_detail.go-279-	currentCount := getCommentCount(cl.page)
xiaohongshu/feed_detail.go-280-	if currentCount > 0 {
xiaohongshu/feed_detail.go-281-		scrollToLastComment(cl.page)
xiaohongshu/feed_detail.go:282:		sleepRandom(postScrollRange.min, postScrollRange.max)
xiaohongshu/feed_detail.go-283-	}
xiaohongshu/feed_detail.go-284-
xiaohongshu/feed_detail.go-285-	largeMode := cl.state.stagnantChecks >= largeScrollTrigger
--
xiaohongshu/feed_detail.go-326-
xiaohongshu/feed_detail.go-327-// ========== 工具函数 ==========
xiaohongshu/feed_detail.go-328-
xiaohongshu/feed_detail.go:329:func sleepRandom(minMs, maxMs int) {
xiaohongshu/feed_detail.go-330-	if maxMs <= minMs {
xiaohongshu/feed_detail.go:331:		time.Sleep(time.Duration(minMs) * time.Millisecond)
xiaohongshu/feed_detail.go-332-		return
xiaohongshu/feed_detail.go-333-	}
xiaohongshu/feed_detail.go-334-	delay := time.Duration(minMs+rand.Intn(maxMs-minMs)) * time.Millisecond
xiaohongshu/feed_detail.go:335:	time.Sleep(delay)
xiaohongshu/feed_detail.go-336-}
xiaohongshu/feed_detail.go-337-
xiaohongshu/feed_detail.go-338-func getScrollInterval(speed string) time.Duration {
--
xiaohongshu/feed_detail.go-420-			// 滚动到元素
xiaohongshu/feed_detail.go-421-			el.MustScrollIntoView()
xiaohongshu/feed_detail.go-422-
xiaohongshu/feed_detail.go:423:			sleepRandom(reactionTimeRange.min, reactionTimeRange.max)
xiaohongshu/feed_detail.go-424-
xiaohongshu/feed_detail.go-425-			// 鼠标悬停
xiaohongshu/feed_detail.go-426-			if box, err := el.Shape(); err == nil && len(box.Quads) > 0 {
--
xiaohongshu/feed_detail.go-429-				if err := page.MovePoint(proto.Point{X: x, Y: y}); err != nil {
xiaohongshu/feed_detail.go-430-					return err
xiaohongshu/feed_detail.go-431-				}
xiaohongshu/feed_detail.go:432:				sleepRandom(hoverTimeRange.min, hoverTimeRange.max)
xiaohongshu/feed_detail.go-433-			}
xiaohongshu/feed_detail.go-434-
xiaohongshu/feed_detail.go-435-			// 点击
--
xiaohongshu/feed_detail.go-438-			}
xiaohongshu/feed_detail.go-439-
xiaohongshu/feed_detail.go-440-			// 模拟人类阅读时间
xiaohongshu/feed_detail.go:441:			sleepRandom(readTimeRange.min, readTimeRange.max)
xiaohongshu/feed_detail.go-442-			clickSuccess = true
xiaohongshu/feed_detail.go-443-			return nil
xiaohongshu/feed_detail.go-444-		},
--
xiaohongshu/feed_detail.go-483-			logrus.Warnf("人化滚动失败: %v", err)
xiaohongshu/feed_detail.go-484-		}
xiaohongshu/feed_detail.go-485-
xiaohongshu/feed_detail.go:486:		sleepRandom(scrollWaitRange.min, scrollWaitRange.max)
xiaohongshu/feed_detail.go-487-
xiaohongshu/feed_detail.go-488-		currentScrollTop = getScrollTop(page)
xiaohongshu/feed_detail.go-489-		deltaThisTime := currentScrollTop - beforeTop
--
xiaohongshu/feed_detail.go-496-		beforeTop = currentScrollTop
xiaohongshu/feed_detail.go-497-
xiaohongshu/feed_detail.go-498-		if i < pushCount-1 {
xiaohongshu/feed_detail.go:499:			sleepRandom(humanDelayRange.min, humanDelayRange.max)
xiaohongshu/feed_detail.go-500-		}
xiaohongshu/feed_detail.go-501-	}
xiaohongshu/feed_detail.go-502-
--
xiaohongshu/feed_detail.go-506-		if err := page.Actor().Mouse.Scroll(0, float64(scrollHeight-currentScrollTop)); err != nil {
xiaohongshu/feed_detail.go-507-			logrus.Warnf("滚动到底部失败: %v", err)
xiaohongshu/feed_detail.go-508-		}
xiaohongshu/feed_detail.go:509:		sleepRandom(postScrollRange.min, postScrollRange.max)
xiaohongshu/feed_detail.go-510-		currentScrollTop = getScrollTop(page)
xiaohongshu/feed_detail.go-511-		actualDelta = currentScrollTop - beforeTop + actualDelta
xiaohongshu/feed_detail.go-512-		scrolled = actualDelta > 5
--
xiaohongshu/feed_detail.go-547-		el.MustScrollIntoView()
xiaohongshu/feed_detail.go-548-	}
xiaohongshu/feed_detail.go-549-	// 等待滚动完成
xiaohongshu/feed_detail.go:550:	time.Sleep(500 * time.Millisecond)
xiaohongshu/feed_detail.go-551-
xiaohongshu/feed_detail.go-552-	// 触发一次小滚动，激活懒加载机制
xiaohongshu/feed_detail.go-553-	smartScroll(page, 100)
--
xiaohongshu/feed_detail.go-745-// ========== 页面检查 ==========
xiaohongshu/feed_detail.go-746-
xiaohongshu/feed_detail.go-747-func checkPageAccessible(page *hrod.Page) error {
xiaohongshu/feed_detail.go:748:	time.Sleep(500 * time.Millisecond)
xiaohongshu/feed_detail.go-749-
xiaohongshu/feed_detail.go-750-	// 查找错误提示容器
xiaohongshu/feed_detail.go-751-	wrapperEl, err := page.Timeout(2 * time.Second).Element(".access-wrapper, .error-wrapper, .not-found-wrapper, .blocked-wrapper")

exec
/bin/bash -lc "nl -ba pkg/humanize/util.go; nl -ba pkg/humanize/rod/hrod.go | sed -n '1,190p'; nl -ba pkg/humanize/humanize.go | sed -n '1,210p'; nl -ba pkg/humanize/mouse.go | sed -n '1,180p'; nl -ba pkg/humanize/keyboard.go | sed -n '1,180p'" in /tmp/xiaohongshu-mcp-fork
exec
/bin/bash -lc "rg -n -C 4 'page\\.Context\\(ctx\\)|\\.Context\\(ctx\\)' service.go xiaohongshu; rg -n 'func \\(.*\\) Context|func \\(.*\\) Timeout|func \\(.*\\) wrapPage|func .*sleepWithContext' -S ." in /tmp/xiaohongshu-mcp-fork
exec
/bin/bash -lc "nl -ba service.go | sed -n '1,260p'; git status --short; git branch --show-current; git log -1 --oneline" in /tmp/xiaohongshu-mcp-fork
exec
/bin/bash -lc 'for f in xiaohongshu/{publish.go,comment_feed.go,publish_video.go,feed_detail.go,like_favorite.go,login.go,feeds.go}; do echo "### $f"; rg -n '"'"'^func .*context'"\\.Context|time\\.Sleep|sleepRandom' \""'$f"; done' in /tmp/xiaohongshu-mcp-fork
 succeeded in 129ms:
fixup
9ba72f9 fix: correct ScrollIntoView delta direction for above/left elements

 succeeded in 146ms:
     1	// Package humanize provides human-like browser interactions for go-rod.
     2	//
     3	// Unlike a single smooth Bezier curve, this package:
     4	//   - splits movements into multiple segments with different curve families
     5	//   - adds jitter, pauses, overshoots, and random scroll events
     6	//   - types with variable speed, bursts, and occasional typos + corrections
     7	//   - exposes slow/normal/fast speed profiles based on real human timing
     8	package humanize
     9	
    10	import (
    11		"context"
    12	
    13		"github.com/go-rod/rod"
    14	)
    15	
    16	// Actor groups humanized mouse and keyboard actions.
    17	type Actor struct {
    18		Mouse    *Mouse
    19		Keyboard *Keyboard
    20		cfg      Config
    21	}
    22	
    23	// New creates a humanized actor for the given page.
    24	func New(page *rod.Page, cfg Config) *Actor {
    25		return NewWithContext(page, cfg, context.Background())
    26	}
    27	
    28	// NewWithContext creates a humanized actor for the given page and context.
    29	func NewWithContext(page *rod.Page, cfg Config, ctx context.Context) *Actor {
    30		mouse := NewMouse(page, cfg)
    31		actor := &Actor{
    32			Mouse:    mouse,
    33			Keyboard: NewKeyboard(page, cfg, mouse),
    34			cfg:      cfg,
    35		}
    36		actor.SetContext(ctx)
    37		return actor
    38	}
    39	
    40	// Config returns the actor's configuration.
    41	func (a *Actor) Config() Config {
    42		return a.cfg
    43	}
    44	
    45	// SetContext updates the context used by humanized delays.
    46	func (a *Actor) SetContext(ctx context.Context) {
    47		a.Mouse.setContext(ctx)
    48		a.Keyboard.setContext(ctx)
    49	}
     1	package humanize
     2	
     3	import (
     4		"context"
     5		"errors"
     6		"math"
     7		"math/rand"
     8		"os"
     9		"time"
    10	
    11		"github.com/go-rod/rod"
    12		"github.com/go-rod/rod/lib/proto"
    13	)
    14	
    15	var debugMouse bool
    16	
    17	func init() {
    18		v := os.Getenv("HUMANIZE_DEBUG")
    19		debugMouse = v == "1" || v == "true" || v == "yes"
    20	}
    21	
    22	// Mouse provides human-like mouse operations.
    23	type Mouse struct {
    24		page        *rod.Page
    25		cfg         Config
    26		ctx         context.Context
    27		initialized bool
    28	}
    29	
    30	// NewMouse creates a new humanized mouse wrapper.
    31	func NewMouse(page *rod.Page, cfg Config) *Mouse {
    32		return &Mouse{page: page, cfg: cfg, ctx: context.Background()}
    33	}
    34	
    35	func (m *Mouse) setContext(ctx context.Context) {
    36		m.ctx = ctx
    37	}
    38	
    39	// initPosition moves the cursor from the rod default (0,0) to a plausible
    40	// starting point inside the viewport. This is done once per Mouse instance so
    41	// subsequent movements do not look like long flights from the screen corner.
    42	// The movement itself is humanized so the cursor does not teleport.
    43	func (m *Mouse) initPosition() error {
    44		if m.initialized {
    45			return nil
    46		}
    47		vp, err := m.viewport()
    48		if err != nil {
    49			return err
    50		}
    51		center := Point{
    52			X: vp.width/2 + (rand.Float64()*2-1)*vp.width*0.15,
    53			Y: vp.height/2 + (rand.Float64()*2-1)*vp.height*0.15,
    54		}
    55	
    56		// Mark initialized before calling moveTo to avoid recursion.
    57		m.initialized = true
    58		if err := m.moveTo(center, false); err != nil {
    59			m.initialized = false
    60			return err
    61		}
    62		return nil
    63	}
    64	
    65	// InitPosition eagerly moves the cursor from the rod default (0,0) to a
    66	// plausible starting point. Call this right after a page is created so the
    67	// first real interaction does not start from the detectable (0,0) origin.
    68	func (m *Mouse) InitPosition() error {
    69		return m.initPosition()
    70	}
    71	
    72	// Move moves the cursor to target with a realistic, non-deterministic path.
    73	// If the target lies outside the current viewport, the page is scrolled first
    74	// so that the destination is rendered before the cursor moves there.
    75	func (m *Mouse) Move(target Point) error {
    76		// target is in page-absolute coordinates. Scroll it into view if it is
    77		// outside the current viewport, then convert to viewport-relative
    78		// coordinates before moving the cursor (rod.Mouse.MoveTo expects
    79		// viewport-relative coordinates).
    80		if err := m.scrollToVisible(target); err != nil {
    81			return err
    82		}
    83		vp, err := m.viewport()
    84		if err != nil {
    85			return err
    86		}
    87		return m.moveTo(Point{
    88			X: target.X - vp.scrollX,
    89			Y: target.Y - vp.scrollY,
    90		}, true)
    91	}
    92	
    93	// MovePoint moves to a viewport-relative point.
    94	func (m *Mouse) MovePoint(target Point) error {
    95		return m.moveTo(target, true)
    96	}
    97	
    98	// moveTo performs the actual cursor movement without any extra scrolling.
    99	func (m *Mouse) moveTo(target Point, scrollingAllowed bool) error {
   100		if debugMouse {
   101			m.ensureDebugOverlay()
   102		}
   103	
   104		// Start from a plausible position instead of rod's default (0,0).
   105		if err := m.initPosition(); err != nil {
   106			return err
   107		}
   108	
   109		start := m.page.Mouse.Position()
   110		straightDist := math.Hypot(target.X-start.X, target.Y-start.Y)
   111	
   112		// Derive step count from distance so short moves finish quickly and long
   113		// moves still have enough points to look natural.
   114		desiredSteps := int(straightDist / m.cfg.Mouse.StepDistance)
   115		if desiredSteps < m.cfg.Mouse.MinSteps {
   116			desiredSteps = m.cfg.Mouse.MinSteps
   117		}
   118		if desiredSteps > m.cfg.Mouse.MaxSteps {
   119			desiredSteps = m.cfg.Mouse.MaxSteps
   120		}
   121	
   122		path := GeneratePath(start, target, desiredSteps, desiredSteps, m.cfg.Mouse.OvershootRatio)
   123	
   124		// Base speed with variance.
   125		speed := m.cfg.Mouse.MoveSpeedPxPerSec * (1 + (rand.Float64()*2-1)*m.cfg.Mouse.SpeedVariance)
   126	
   127		// Total distance for velocity profile normalization.
   128		totalDist := 0.0
   129		prev := start
   130		for _, p := range path {
   131			totalDist += math.Hypot(p.X-prev.X, p.Y-prev.Y)
   132			prev = p
   133		}
   134	
   135		// Accelerate-then-fine-tune velocity profile: slow at the start, fast in
   136		// the middle, and slow again near the target. The profile is a sine hump
   137		// scaled so its average over [0,1] is 1.0, keeping the overall move time
   138		// comparable to the constant-speed baseline.
   139		const velocityFloor = 0.3
   140		velocityAmp := (1.0 - velocityFloor) * math.Pi / 2
   141	
   142		cumulativeDist := 0.0
   143		last := start
   144		for i, p := range path {
   145			// Inject jitter.
   146			if rand.Float64() < m.cfg.Mouse.JitterProbability {
   147				p = jitter(p, m.cfg.Mouse.JitterRadius)
   148			}
   149	
   150			// Distance-based step duration with ease-in-out acceleration.
   151			dist := math.Hypot(p.X-last.X, p.Y-last.Y)
   152			cumulativeDist += dist
   153	
   154			var stepDuration time.Duration
   155			if totalDist > 0 {
   156				t := cumulativeDist / totalDist
   157				// Use the midpoint of the step for smoother transitions.
   158				tMid := t - dist/(2*totalDist)
   159				if tMid < 0 {
   160					tMid = 0
   161				}
   162				velocity := velocityFloor + velocityAmp*math.Sin(math.Pi*tMid)
   163				effectiveSpeed := speed * velocity
   164				stepDuration = time.Duration(float64(time.Second) * dist / effectiveSpeed)
   165			} else {
   166				stepDuration = time.Duration(float64(time.Second) * dist / speed)
   167			}
   168			if stepDuration < 1*time.Millisecond {
   169				stepDuration = 1 * time.Millisecond
   170			}
   171	
   172			// Keep the event density high enough to look like a real mouse
   173			// (typical browser refresh rate is 60-120Hz). If the planned step is
   174			// too long, subdivide it into smaller micro-steps.
   175			const maxStepDuration = 16 * time.Millisecond
   176			subSteps := 1
   177			if stepDuration > maxStepDuration {
   178				subSteps = int(math.Ceil(float64(stepDuration) / float64(maxStepDuration)))
   179			}
   180	
     1	package humanize
     2	
     3	import (
     4		"context"
     5		"math"
     6		"math/rand"
     7		"time"
     8	
     9		"github.com/go-rod/rod"
    10		"github.com/go-rod/rod/lib/input"
    11		"github.com/go-rod/rod/lib/proto"
    12	)
    13	
    14	// Keyboard provides human-like keyboard input.
    15	type Keyboard struct {
    16		page     *rod.Page
    17		cfg      Config
    18		ctx      context.Context
    19		mouse    *Mouse
    20		lastEl   *rod.Element
    21	}
    22	
    23	// NewKeyboard creates a new humanized keyboard wrapper.
    24	func NewKeyboard(page *rod.Page, cfg Config, mouse *Mouse) *Keyboard {
    25		return &Keyboard{page: page, cfg: cfg, ctx: context.Background(), mouse: mouse}
    26	}
    27	
    28	func (k *Keyboard) setContext(ctx context.Context) {
    29		k.ctx = ctx
    30	}
    31	
    32	// Type types text into el with realistic timing, occasional typos, and corrections.
    33	// ASCII characters are typed key-by-key; CJK and other non-keyboard characters
    34	// are inserted via simulated voice/IME composition events.
    35	func (k *Keyboard) Type(el *rod.Element, text string) error {
    36		// Ensure the element is rendered before typing, so the cursor lands on a
    37		// visible input area even when the page is long.
    38		if k.mouse == nil {
    39			if err := el.ScrollIntoView(); err != nil {
    40				return err
    41			}
    42		} else if k.lastEl == el {
    43			if err := k.mouse.ScrollIntoView(el); err != nil {
    44				return err
    45			}
    46		}
    47	
    48		// Move the cursor onto the element and click it, just like a human would
    49		// before typing. Skip the click if we just typed into the same element to
    50		// avoid repeated cursor jumps during continuous input (e.g. typing tags
    51		// char by char). This also keeps the mouse position continuous between
    52		// actions without querying DOM state that a page could detect.
    53		if k.mouse != nil && k.lastEl != el {
    54			if err := k.mouse.Click(el); err != nil {
    55				return err
    56			}
    57		}
    58		k.lastEl = el
    59	
    60		if err := el.Focus(); err != nil {
    61			return err
    62		}
    63	
    64		cfg := k.cfg.Keyboard
    65		if cfg.TypoChars == nil {
    66			cfg.TypoChars = []rune("qwertyuiopasdfghjklzxcvbnm1234567890")
    67		}
    68		if cfg.BurstLength <= 0 {
    69			cfg.BurstLength = 1
    70		}
    71	
    72		cpm := cfg.CPM * (1 + (rand.Float64()*2-1)*cfg.CPMVariance)
    73		msPerChar := 60000.0 / cpm
    74		// ASCII is typed roughly 2x faster; CJK voice/IME composition is slower.
    75		asciiMsPerChar := msPerChar / 2
    76		cjkMsPerChar := msPerChar * 3
    77	
    78		tokens := tokenizeText(text)
    79		typed := 0
    80		lastScrollCheck := 0
    81	
    82		for _, token := range tokens {
    83			if token.isASCII {
    84				for _, r := range token.text {
    85					// Occasional typo for ASCII keys.
    86					if rand.Float64() < cfg.TypoProbability {
    87						typo := randomTypo(r, cfg.TypoChars)
    88						if err := k.press(input.Key(typo)); err != nil {
    89							return err
    90						}
    91						if err := sleepWithContext(k.ctx, cfg.PauseAfterTypo+time.Duration(rand.Float64()*200)*time.Millisecond); err != nil {
    92							return err
    93						}
    94						if err := k.pressBackspace(); err != nil {
    95							return err
    96						}
    97						if err := sleepWithContext(k.ctx, randDuration(50*time.Millisecond, 150*time.Millisecond)); err != nil {
    98							return err
    99						}
   100					}
   101	
   102					if err := k.press(input.Key(r)); err != nil {
   103						return err
   104					}
   105					typed++
   106	
   107					delay := time.Duration(asciiMsPerChar * (0.6 + rand.Float64()*0.8) * float64(time.Millisecond))
   108					if delay < 10*time.Millisecond {
   109						delay = 10 * time.Millisecond
   110					}
   111					if err := sleepWithContext(k.ctx, delay); err != nil {
   112						return err
   113					}
   114	
   115					if typed%cfg.BurstLength == 0 {
   116						if err := sleepWithContext(k.ctx, randDuration(cfg.BurstPause, cfg.BurstPause+80*time.Millisecond)); err != nil {
   117							return err
   118						}
   119					}
   120	
   121					if typed-lastScrollCheck >= 30 {
   122						_ = k.scrollToCursor(el)
   123						lastScrollCheck = typed
   124					}
   125				}
   126			} else {
   127				// CJK / emoji / special chars: simulate voice/IME composition.
   128				segments := segmentCJK(token.text)
   129				for _, seg := range segments {
   130					if err := k.insertCompositionText(el, seg); err != nil {
   131						return err
   132					}
   133					segRunes := []rune(seg)
   134					// Pause between voice/IME chunks scales with segment length and
   135					// the slower CJK speed.
   136					pause := time.Duration(cjkMsPerChar * float64(len(segRunes)) * (0.8 + rand.Float64()*0.6))
   137					if pause < 150*time.Millisecond {
   138						pause = 150 * time.Millisecond
   139					}
   140					if err := sleepWithContext(k.ctx, pause); err != nil {
   141						return err
   142					}
   143					typed += len(segRunes)
   144	
   145					if typed-lastScrollCheck >= 30 {
   146						_ = k.scrollToCursor(el)
   147						lastScrollCheck = typed
   148					}
   149				}
   150			}
   151		}
   152	
   153		return nil
   154	}
   155	
   156	// Press presses a single key with human-like delay.
   157	func (k *Keyboard) Press(key input.Key) error {
   158		if err := k.press(key); err != nil {
   159			return err
   160		}
   161		return sleepWithContext(k.ctx, randDuration(50*time.Millisecond, 150*time.Millisecond))
   162	}
   163	
   164	func (k *Keyboard) press(key input.Key) error {
   165		return k.page.Keyboard.Press(key)
   166	}
   167	
   168	// pressBackspace sends a Backspace key via CDP directly.
   169	func (k *Keyboard) pressBackspace() error {
   170		return proto.InputDispatchKeyEvent{
   171			Type:                  proto.InputDispatchKeyEventTypeKeyDown,
   172			Key:                   "Backspace",
   173			Code:                  "Backspace",
   174			WindowsVirtualKeyCode: 8,
   175		}.Call(k.page)
   176	}
   177	
   178	// scrollToCursor scrolls the page so the text cursor remains visible while
   179	// typing long content. It is best-effort and ignores errors to avoid breaking
   180	// the typing flow.

 succeeded in 269ms:
53:	time.Sleep(2 * time.Second)
59:	time.Sleep(1 * time.Second)
66:	time.Sleep(1 * time.Second)
73:func (p *PublishAction) Publish(ctx context.Context, content PublishImageContent) error {
130:			time.Sleep(200 * time.Millisecond)
135:			time.Sleep(200 * time.Millisecond)
142:			time.Sleep(200 * time.Millisecond)
148:			time.Sleep(200 * time.Millisecond)
241:		time.Sleep(1 * time.Second)
257:			time.Sleep(checkInterval)
272:		time.Sleep(checkInterval)
288:	time.Sleep(500 * time.Millisecond)
294:	time.Sleep(1 * time.Second)
310:	time.Sleep(1 * time.Second)
349:	time.Sleep(3 * time.Second)
386:			time.Sleep(interval)
395:		time.Sleep(interval)
489:	time.Sleep(1 * time.Second)
580:	time.Sleep(1 * time.Second)
590:		time.Sleep(10 * time.Millisecond)
601:	time.Sleep(1 * time.Second)
616:	time.Sleep(200 * time.Millisecond)
622:		time.Sleep(50 * time.Millisecond)
625:	time.Sleep(1 * time.Second)
644:	time.Sleep(200 * time.Millisecond)
646:	time.Sleep(500 * time.Millisecond) // 等待标签处理完成
790:	time.Sleep(500 * time.Millisecond)
807:			time.Sleep(200 * time.Millisecond)
820:	time.Sleep(800 * time.Millisecond)
826:	time.Sleep(500 * time.Millisecond)
914:		time.Sleep(500 * time.Millisecond)
931:	time.Sleep(800 * time.Millisecond)
949:	time.Sleep(500 * time.Millisecond)
978:		time.Sleep(300 * time.Millisecond)
989:	time.Sleep(300 * time.Millisecond)
1006:	time.Sleep(1 * time.Second)
1022:		time.Sleep(500 * time.Millisecond)
1044:	time.Sleep(1000 * time.Millisecond)
1086:					time.Sleep(300 * time.Millisecond) // 确保弹窗动画开始
1096:					time.Sleep(300 * time.Millisecond) // 确保弹窗动画开始
1119:		time.Sleep(100 * time.Millisecond) // 缩短轮询间隔，更快响应
1139:	time.Sleep(100 * time.Millisecond)
1145:	time.Sleep(300 * time.Millisecond)
1153:	time.Sleep(1 * time.Second)
1166:		time.Sleep(100 * time.Millisecond)
1175:		time.Sleep(100 * time.Millisecond)
1177:	time.Sleep(500 * time.Millisecond) // 额外等待确保渲染完成
1201:	time.Sleep(time.Duration(randomDelay) * time.Millisecond)
1247:		time.Sleep(200 * time.Millisecond)
### xiaohongshu/comment_feed.go
24:func (f *CommentFeedAction) PostComment(ctx context.Context, feedID, xsecToken, content string) error {
34:	time.Sleep(1 * time.Second)
63:	time.Sleep(1 * time.Second)
76:	time.Sleep(1 * time.Second)
83:func (f *CommentFeedAction) ReplyToComment(ctx context.Context, feedID, xsecToken, commentID, userID, content string) error {
93:	time.Sleep(1 * time.Second)
101:	time.Sleep(2 * time.Second)
112:	time.Sleep(1 * time.Second)
126:	time.Sleep(1 * time.Second)
139:	time.Sleep(500 * time.Millisecond)
151:	time.Sleep(2 * time.Second)
165:	time.Sleep(1 * time.Second)
218:			time.Sleep(300 * time.Millisecond)
227:		time.Sleep(500 * time.Millisecond)
269:		time.Sleep(scrollInterval)
### xiaohongshu/publish_video.go
37:	time.Sleep(2 * time.Second)
42:	time.Sleep(1 * time.Second)
48:	time.Sleep(1 * time.Second)
54:func (p *PublishAction) PublishVideo(ctx context.Context, content PublishVideoContent) error {
111:	time.Sleep(1 * time.Second)
128:	time.Sleep(1 * time.Second)
152:	time.Sleep(3 * time.Second)
### xiaohongshu/feed_detail.go
75:func (f *FeedDetailAction) GetFeedDetail(ctx context.Context, feedID, xsecToken string, loadAllComments bool, config CommentLoadConfig) (*FeedDetailResponse, error) {
79:func (f *FeedDetailAction) GetFeedDetailWithConfig(ctx context.Context, feedID, xsecToken string, loadAllComments bool, config CommentLoadConfig) (*FeedDetailResponse, error) {
105:	sleepRandom(1000, 1000)
158:	sleepRandom(humanDelayRange.min, humanDelayRange.max)
186:		time.Sleep(scrollInterval)
212:		sleepRandom(humanDelayRange.min, humanDelayRange.max)
232:		sleepRandom(readTimeRange.min, readTimeRange.max)
240:			sleepRandom(shortReadRange.min, shortReadRange.max)
282:		sleepRandom(postScrollRange.min, postScrollRange.max)
329:func sleepRandom(minMs, maxMs int) {
331:		time.Sleep(time.Duration(minMs) * time.Millisecond)
335:	time.Sleep(delay)
423:			sleepRandom(reactionTimeRange.min, reactionTimeRange.max)
432:				sleepRandom(hoverTimeRange.min, hoverTimeRange.max)
441:			sleepRandom(readTimeRange.min, readTimeRange.max)
486:		sleepRandom(scrollWaitRange.min, scrollWaitRange.max)
499:			sleepRandom(humanDelayRange.min, humanDelayRange.max)
509:		sleepRandom(postScrollRange.min, postScrollRange.max)
550:	time.Sleep(500 * time.Millisecond)
748:	time.Sleep(500 * time.Millisecond)
### xiaohongshu/like_favorite.go
46:func (a *interactAction) preparePage(ctx context.Context, actionType interactActionType, feedID, xsecToken string) *hrod.Page {
53:	time.Sleep(1 * time.Second)
73:func (a *LikeAction) Like(ctx context.Context, feedID, xsecToken string) error {
78:func (a *LikeAction) Unlike(ctx context.Context, feedID, xsecToken string) error {
82:func (a *LikeAction) perform(ctx context.Context, feedID, xsecToken string, targetLiked bool) error {
110:	time.Sleep(3 * time.Second)
124:	time.Sleep(2 * time.Second)
149:func (a *FavoriteAction) Favorite(ctx context.Context, feedID, xsecToken string) error {
154:func (a *FavoriteAction) Unfavorite(ctx context.Context, feedID, xsecToken string) error {
158:func (a *FavoriteAction) perform(ctx context.Context, feedID, xsecToken string, targetCollected bool) error {
186:	time.Sleep(3 * time.Second)
200:	time.Sleep(2 * time.Second)
### xiaohongshu/login.go
19:func (a *LoginAction) CheckLoginStatus(ctx context.Context) (bool, error) {
23:	time.Sleep(1 * time.Second)
37:func (a *LoginAction) Login(ctx context.Context) error {
44:	time.Sleep(2 * time.Second)
59:func (a *LoginAction) FetchQrcodeImage(ctx context.Context) (string, bool, error) {
66:	time.Sleep(2 * time.Second)
85:func (a *LoginAction) WaitForLogin(ctx context.Context) bool {
### xiaohongshu/feeds.go
27:func (f *FeedsListAction) GetFeedsList(ctx context.Context) ([]Feed, error) {
30:	time.Sleep(1 * time.Second)

 succeeded in 559ms:
./REMAINING_FIXES_PLAN.md:187:    68	func (b *Browser) wrapPage(p *rod.Page) *Page {
./REMAINING_FIXES_PLAN.md:229:   110	func (p *Page) wrapPage(rp *rod.Page) *Page {
./REMAINING_FIXES_PLAN.md:250:   131	func (p *Page) Context(ctx context.Context) *Page {
./REMAINING_FIXES_PLAN.md:258:   139	func (p *Page) Timeout(d time.Duration) *Page {
./REMAINING_FIXES_PLAN.md:741:   622	func (el *Element) Timeout(d time.Duration) *Element {
./REMAINING_FIXES_PLAN.md:746:   627	func (el *Element) Context(ctx context.Context) *Element {
./REMAINING_FIXES_PLAN.md:1835:pkg/humanize/rod/hrod.go:131:func (p *Page) Context(ctx context.Context) *Page {
./REMAINING_FIXES_PLAN.md:1844:pkg/humanize/rod/hrod.go:627:func (el *Element) Context(ctx context.Context) *Element {
./REMAINING_FIXES_PLAN.md:2022:pkg/humanize/util.go:27:func sleepWithContext(ctx context.Context, d time.Duration) error {
./REMAINING_FIXES_PLAN.md:2899:/bin/bash -lc "rg -n -C 4 'page\\.Context\\(ctx\\)|\\.Context\\(ctx\\)' service.go xiaohongshu; rg -n 'func \\(.*\\) Context|func \\(.*\\) Timeout|func \\(.*\\) wrapPage|func .*sleepWithContext' -S ." in /tmp/xiaohongshu-mcp-fork
./pkg/humanize/rod/hrod.go:68:func (b *Browser) wrapPage(p *rod.Page) *Page {
./pkg/humanize/rod/hrod.go:110:func (p *Page) wrapPage(rp *rod.Page) *Page {
./pkg/humanize/rod/hrod.go:131:func (p *Page) Context(ctx context.Context) *Page {
./pkg/humanize/rod/hrod.go:139:func (p *Page) Timeout(d time.Duration) *Page {
./pkg/humanize/rod/hrod.go:622:func (el *Element) Timeout(d time.Duration) *Element {
./pkg/humanize/rod/hrod.go:627:func (el *Element) Context(ctx context.Context) *Element {
./pkg/humanize/util.go:27:func sleepWithContext(ctx context.Context, d time.Duration) error {
./handlers_api.go:126:func sleepWithContext(ctx context.Context, d time.Duration) error {

exec
/bin/bash -lc "nl -ba xiaohongshu/publish.go | sed -n '1,430p'; nl -ba xiaohongshu/publish.go | sed -n '450,840p'" in /tmp/xiaohongshu-mcp-fork
 succeeded in 0ms:
     1	package xiaohongshu
     2	
     3	import (
     4		"context"
     5		"log/slog"
     6		"math/rand"
     7		"os"
     8		"regexp"
     9		"strings"
    10		"time"
    11	
    12		"github.com/go-rod/rod"
    13		"github.com/go-rod/rod/lib/input"
    14		"github.com/go-rod/rod/lib/proto"
    15		"github.com/pkg/errors"
    16		"github.com/sirupsen/logrus"
    17		hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
    18	)
    19	
    20	// PublishImageContent 发布图文内容
    21	type PublishImageContent struct {
    22		Title        string
    23		Content      string
    24		Tags         []string
    25		ImagePaths   []string
    26		ScheduleTime *time.Time // 定时发布时间，nil 表示立即发布
    27		IsOriginal   bool       // 是否声明原创
    28		Visibility   string     // 可见范围: "公开可见"(默认), "仅自己可见", "仅互关好友可见"
    29		Products     []string   // 商品关键词列表，用于绑定带货商品
    30	}
    31	
    32	type PublishAction struct {
    33		page *hrod.Page
    34	}
    35	
    36	const (
    37		urlOfPublic = `https://creator.xiaohongshu.com/publish/publish?source=official`
    38	)
    39	
    40	func NewPublishImageAction(page *hrod.Page) (*PublishAction, error) {
    41	
    42		pp := page.Timeout(300 * time.Second)
    43	
    44		// 使用更稳健的导航和等待策略
    45		if err := pp.Navigate(urlOfPublic); err != nil {
    46			return nil, errors.Wrap(err, "导航到发布页面失败")
    47		}
    48	
    49		// 等待页面加载，使用 WaitLoad 代替 WaitIdle（更宽松）
    50		if err := pp.WaitLoad(); err != nil {
    51			logrus.Warnf("等待页面加载出现问题: %v，继续尝试", err)
    52		}
    53		time.Sleep(2 * time.Second)
    54	
    55		// 等待页面稳定
    56		if err := pp.WaitDOMStable(time.Second, 0.1); err != nil {
    57			logrus.Warnf("等待 DOM 稳定出现问题: %v，继续尝试", err)
    58		}
    59		time.Sleep(1 * time.Second)
    60	
    61		if err := mustClickPublishTab(pp, "上传图文"); err != nil {
    62			logrus.Errorf("点击上传图文 TAB 失败: %v", err)
    63			return nil, err
    64		}
    65	
    66		time.Sleep(1 * time.Second)
    67	
    68		return &PublishAction{
    69			page: pp,
    70		}, nil
    71	}
    72	
    73	func (p *PublishAction) Publish(ctx context.Context, content PublishImageContent) error {
    74		if len(content.ImagePaths) == 0 {
    75			return errors.New("图片不能为空")
    76		}
    77	
    78		page := p.page.Context(ctx)
    79	
    80		if err := uploadImages(page, content.ImagePaths); err != nil {
    81			return errors.Wrap(err, "小红书上传图片失败")
    82		}
    83	
    84		tags := content.Tags
    85		if len(tags) >= 10 {
    86			logrus.Warnf("标签数量超过10，截取前10个标签")
    87			tags = tags[:10]
    88		}
    89	
    90		logrus.Infof("发布内容: title=%s, images=%v, tags=%v, schedule=%v, original=%v, visibility=%s, products=%v", content.Title, len(content.ImagePaths), tags, content.ScheduleTime, content.IsOriginal, content.Visibility, content.Products)
    91	
    92		if err := submitPublish(page, content.Title, content.Content, tags, content.ScheduleTime, content.IsOriginal, content.Visibility, content.Products); err != nil {
    93			return errors.Wrap(err, "小红书发布失败")
    94		}
    95	
    96		return nil
    97	}
    98	
    99	func removePopCover(page *hrod.Page) {
   100	
   101		// 先移除弹窗封面
   102		has, elem, err := page.Has("div.d-popover")
   103		if err != nil {
   104			return
   105		}
   106		if has {
   107			elem.MustRemove()
   108		}
   109	
   110		// 兜底：点击一下空位置吧
   111		clickEmptyPosition(page)
   112	}
   113	
   114	func clickEmptyPosition(page *hrod.Page) {
   115		x := 380 + rand.Intn(100)
   116		y := 20 + rand.Intn(60)
   117		if err := page.ClickPoint(proto.Point{X: float64(x), Y: float64(y)}); err != nil {
   118			logrus.Warnf("点击空白位置失败: %v", err)
   119		}
   120	}
   121	
   122	func mustClickPublishTab(page *hrod.Page, tabname string) error {
   123		page.MustElement(`div.upload-content`).MustWaitVisible()
   124	
   125		deadline := time.Now().Add(15 * time.Second)
   126		for time.Now().Before(deadline) {
   127			tab, blocked, err := getTabElement(page, tabname)
   128			if err != nil {
   129				logrus.Warnf("获取发布 TAB 元素失败: %v", err)
   130				time.Sleep(200 * time.Millisecond)
   131				continue
   132			}
   133	
   134			if tab == nil {
   135				time.Sleep(200 * time.Millisecond)
   136				continue
   137			}
   138	
   139			if blocked {
   140				logrus.Info("发布 TAB 被遮挡，尝试移除遮挡")
   141				removePopCover(page)
   142				time.Sleep(200 * time.Millisecond)
   143				continue
   144			}
   145	
   146			if err := tab.Click(proto.InputMouseButtonLeft, 1); err != nil {
   147				logrus.Warnf("点击发布 TAB 失败: %v", err)
   148				time.Sleep(200 * time.Millisecond)
   149				continue
   150			}
   151	
   152			return nil
   153		}
   154	
   155		return errors.Errorf("没有找到发布 TAB - %s", tabname)
   156	}
   157	
   158	func getTabElement(page *hrod.Page, tabname string) (*hrod.Element, bool, error) {
   159		elems, err := page.Elements("div.creator-tab")
   160		if err != nil {
   161			return nil, false, err
   162		}
   163	
   164		for _, elem := range elems {
   165			if !isElementVisible(elem) {
   166				continue
   167			}
   168	
   169			text, err := elem.Text()
   170			if err != nil {
   171				logrus.Debugf("获取发布 TAB 文本失败: %v", err)
   172				continue
   173			}
   174	
   175			if strings.TrimSpace(text) != tabname {
   176				continue
   177			}
   178	
   179			blocked, err := isElementBlocked(elem)
   180			if err != nil {
   181				return nil, false, err
   182			}
   183	
   184			return elem, blocked, nil
   185		}
   186	
   187		return nil, false, nil
   188	}
   189	
   190	func isElementBlocked(elem *hrod.Element) (bool, error) {
   191		result, err := elem.Eval(`() => {
   192			const rect = this.getBoundingClientRect();
   193			if (rect.width === 0 || rect.height === 0) {
   194				return true;
   195			}
   196			const x = rect.left + rect.width / 2;
   197			const y = rect.top + rect.height / 2;
   198			const target = document.elementFromPoint(x, y);
   199			return !(target === this || this.contains(target));
   200		}`)
   201		if err != nil {
   202			return false, err
   203		}
   204	
   205		return result.Value.Bool(), nil
   206	}
   207	
   208	func uploadImages(page *hrod.Page, imagesPaths []string) error {
   209		// 验证文件路径有效性
   210		validPaths := make([]string, 0, len(imagesPaths))
   211		for _, path := range imagesPaths {
   212			if _, err := os.Stat(path); os.IsNotExist(err) {
   213				logrus.Warnf("图片文件不存在: %s", path)
   214				continue
   215			}
   216			validPaths = append(validPaths, path)
   217			logrus.Infof("获取有效图片：%s", path)
   218		}
   219	
   220		// 逐张上传：每张上传后等待预览出现，再上传下一张
   221		for i, path := range validPaths {
   222			selector := `input[type="file"]`
   223			if i == 0 {
   224				selector = ".upload-input"
   225			}
   226	
   227			uploadInput, err := page.Element(selector)
   228			if err != nil {
   229				return errors.Wrapf(err, "查找上传输入框失败(第%d张)", i+1)
   230			}
   231			if err := uploadInput.SetFiles([]string{path}); err != nil {
   232				return errors.Wrapf(err, "上传第%d张图片失败", i+1)
   233			}
   234	
   235			slog.Info("图片已提交上传", "index", i+1, "path", path)
   236	
   237			// 等待当前图片上传完成（预览元素数量达到 i+1），最多等 60 秒
   238			if err := waitForUploadComplete(page, i+1); err != nil {
   239				return errors.Wrapf(err, "第%d张图片上传超时", i+1)
   240			}
   241			time.Sleep(1 * time.Second)
   242		}
   243	
   244		return nil
   245	}
   246	
   247	// waitForUploadComplete 等待第 expectedCount 张图片上传完成，最多等 60 秒
   248	func waitForUploadComplete(page *hrod.Page, expectedCount int) error {
   249		maxWaitTime := 60 * time.Second
   250		checkInterval := 500 * time.Millisecond
   251		start := time.Now()
   252		lastLogCount := expectedCount - 1
   253	
   254		for time.Since(start) < maxWaitTime {
   255			uploadedImages, err := page.Elements(".img-preview-area .pr")
   256			if err != nil {
   257				time.Sleep(checkInterval)
   258				continue
   259			}
   260	
   261			currentCount := len(uploadedImages)
   262			// 数量变化时才打印，避免刷屏
   263			if currentCount != lastLogCount {
   264				slog.Info("等待图片上传", "current", currentCount, "expected", expectedCount)
   265				lastLogCount = currentCount
   266			}
   267			if currentCount >= expectedCount {
   268				slog.Info("图片上传完成", "count", currentCount)
   269				return nil
   270			}
   271	
   272			time.Sleep(checkInterval)
   273		}
   274	
   275		return errors.Errorf("第%d张图片上传超时(60s)，请检查网络连接和图片大小", expectedCount)
   276	}
   277	
   278	func submitPublish(page *hrod.Page, title, content string, tags []string, scheduleTime *time.Time, isOriginal bool, visibility string, products []string) error {
   279		titleElem, err := page.Element("div.d-input input")
   280		if err != nil {
   281			return errors.Wrap(err, "查找标题输入框失败")
   282		}
   283		if err := titleElem.Input(title); err != nil {
   284			return errors.Wrap(err, "输入标题失败")
   285		}
   286	
   287		// 检查标题长度
   288		time.Sleep(500 * time.Millisecond)
   289		if err := checkTitleMaxLength(page); err != nil {
   290			return err
   291		}
   292		slog.Info("检查标题长度：通过")
   293	
   294		time.Sleep(1 * time.Second)
   295	
   296		contentElem, ok := getContentElement(page)
   297		if !ok {
   298			return errors.New("没有找到内容输入框")
   299		}
   300		if err := contentElem.Input(content); err != nil {
   301			return errors.Wrap(err, "输入正文失败")
   302		}
   303		if err := waitAndClickTitleInput(titleElem); err != nil {
   304			return err
   305		}
   306		if err := inputTags(contentElem, tags); err != nil {
   307			return err
   308		}
   309	
   310		time.Sleep(1 * time.Second)
   311	
   312		// 检查正文长度
   313		if err := checkContentMaxLength(page); err != nil {
   314			return err
   315		}
   316		slog.Info("检查正文长度：通过")
   317	
   318		// 处理定时发布
   319		if scheduleTime != nil {
   320			if err := setSchedulePublish(page, *scheduleTime); err != nil {
   321				return errors.Wrap(err, "设置定时发布失败")
   322			}
   323			slog.Info("定时发布设置完成", "schedule_time", scheduleTime.Format("2006-01-02 15:04"))
   324		}
   325	
   326		// 设置可见范围
   327		if err := setVisibility(page, visibility); err != nil {
   328			return errors.Wrap(err, "设置可见范围失败")
   329		}
   330	
   331		// 处理原创声明
   332		if isOriginal {
   333			if err := setOriginal(page); err != nil {
   334				slog.Warn("设置原创声明失败，继续发布", "error", err)
   335			} else {
   336				slog.Info("已声明原创")
   337			}
   338		}
   339	
   340		// 绑定商品
   341		if err := bindProducts(page, products); err != nil {
   342			return errors.Wrap(err, "绑定商品失败")
   343		}
   344	
   345		if err := clickPublishButton(page); err != nil {
   346			return err
   347		}
   348	
   349		time.Sleep(3 * time.Second)
   350		return nil
   351	}
   352	
   353	type publishButton struct {
   354		elem     *hrod.Element
   355		isWidget bool
   356	}
   357	
   358	func clickPublishButton(page *hrod.Page) error {
   359		btn, err := waitForPublishButtonClickable(page, 15*time.Second)
   360		if err != nil {
   361			return err
   362		}
   363	
   364		if btn.isWidget {
   365			return clickPublishWidget(page, btn.elem)
   366		}
   367	
   368		if err := btn.elem.Click(proto.InputMouseButtonLeft, 1); err != nil {
   369			return errors.Wrap(err, "点击发布按钮失败")
   370		}
   371		return nil
   372	}
   373	
   374	// waitForPublishButtonClickable 等待新版 xhs-publish-btn 或旧版 button.bg-red 可点击。
   375	func waitForPublishButtonClickable(page *hrod.Page, maxWait time.Duration) (*publishButton, error) {
   376		interval := 300 * time.Millisecond
   377		start := time.Now()
   378		var lastDisabledReason string
   379	
   380		slog.Info("开始等待发布按钮可点击")
   381	
   382		for time.Since(start) < maxWait {
   383			btn, disabledReason, err := findPublishButton(page)
   384			if err != nil {
   385				slog.Warn("查找发布按钮失败，继续等待", "error", err)
   386				time.Sleep(interval)
   387				continue
   388			}
   389			if btn != nil && disabledReason == "" {
   390				return btn, nil
   391			}
   392			if disabledReason != "" {
   393				lastDisabledReason = disabledReason
   394			}
   395			time.Sleep(interval)
   396		}
   397	
   398		if lastDisabledReason != "" {
   399			return nil, errors.Errorf("等待发布按钮可点击超时: %s", lastDisabledReason)
   400		}
   401		return nil, errors.New("等待发布按钮可点击超时")
   402	}
   403	
   404	func findPublishButton(page *hrod.Page) (*publishButton, string, error) {
   405		widgets, err := page.Elements("xhs-publish-btn")
   406		if err != nil {
   407			return nil, "", errors.Wrap(err, "查找新版发布按钮失败")
   408		}
   409	
   410		for _, widget := range widgets {
   411			if !isElementVisible(widget) {
   412				continue
   413			}
   414	
   415			isPublish, err := widget.Attribute("is-publish")
   416			if err != nil {
   417				return nil, "", errors.Wrap(err, "读取新版发布按钮 is-publish 属性失败")
   418			}
   419			if isPublish != nil && *isPublish == "false" {
   420				continue
   421			}
   422	
   423			submitDisabled, err := widget.Attribute("submit-disabled")
   424			if err != nil {
   425				return nil, "", errors.Wrap(err, "读取新版发布按钮 submit-disabled 属性失败")
   426			}
   427			if submitDisabled != nil && *submitDisabled == "true" {
   428				return &publishButton{elem: widget, isWidget: true}, "新版发布按钮不可点击", nil
   429			}
   430	
   450			if ariaDisabled, err := oldButton.Attribute("aria-disabled"); err != nil {
   451				return nil, "", errors.Wrap(err, "读取旧版发布按钮 aria-disabled 属性失败")
   452			} else if ariaDisabled != nil && *ariaDisabled == "true" {
   453				return &publishButton{elem: oldButton}, "旧版发布按钮 aria-disabled=true", nil
   454			}
   455	
   456			if cls, err := oldButton.Attribute("class"); err != nil {
   457				return nil, "", errors.Wrap(err, "读取旧版发布按钮 class 属性失败")
   458			} else if cls != nil && hasExactClass(*cls, "disabled") {
   459				return &publishButton{elem: oldButton}, "旧版发布按钮包含 disabled class", nil
   460			}
   461	
   462			return &publishButton{elem: oldButton}, "", nil
   463		}
   464	
   465		return nil, "", nil
   466	}
   467	
   468	func clickPublishWidget(page *hrod.Page, widget *hrod.Element) error {
   469		shadowRoot, err := widget.Rod.ShadowRoot()
   470		if err != nil {
   471			return errors.Wrap(err, "获取发布按钮 shadow root 失败")
   472		}
   473		publishBtnRod, err := shadowRoot.ElementR("button", "发布")
   474		if err != nil {
   475			return errors.Wrap(err, "未找到新版发布按钮")
   476		}
   477		publishBtn := hrod.NewElement(publishBtnRod, page.Actor())
   478	
   479		// 发布按钮容器是 sticky bottom，已经在视口内，跳过 ScrollIntoView 避免 wheel 滚动死循环。
   480		if err := publishBtn.ClickNoScroll(); err != nil {
   481			return errors.Wrap(err, "点击发布按钮失败")
   482		}
   483		return nil
   484	}
   485	
   486	// waitAndClickTitleInput 在填写正文后等待 1 秒并回点标题输入框，增强后续交互稳定性
   487	func waitAndClickTitleInput(titleElem *hrod.Element) error {
   488		slog.Info("正文填写完成，准备等待后回点标题输入框")
   489		time.Sleep(1 * time.Second)
   490		if err := titleElem.Click(proto.InputMouseButtonLeft, 1); err != nil {
   491			return errors.Wrap(err, "回点标题输入框失败")
   492		}
   493		slog.Info("已回点标题输入框，继续后续发布流程")
   494		return nil
   495	}
   496	
   497	// 检查标题是否超过最大长度
   498	func checkTitleMaxLength(page *hrod.Page) error {
   499		has, elem, err := page.Has(`div.title-container div.max_suffix`)
   500		if err != nil {
   501			return errors.Wrap(err, "检查标题长度元素失败")
   502		}
   503	
   504		// 元素不存在，说明标题没超长
   505		if !has {
   506			return nil
   507		}
   508	
   509		// 元素存在，说明标题超长
   510		titleLength, err := elem.Text()
   511		if err != nil {
   512			return errors.Wrap(err, "获取标题长度文本失败")
   513		}
   514	
   515		return makeMaxLengthError(titleLength)
   516	}
   517	
   518	func checkContentMaxLength(page *hrod.Page) error {
   519		has, elem, err := page.Has(`div.edit-container div.length-error`)
   520		if err != nil {
   521			return errors.Wrap(err, "检查正文长度元素失败")
   522		}
   523	
   524		// 元素不存在，说明正文没超长
   525		if !has {
   526			return nil
   527		}
   528	
   529		// 元素存在，说明正文超长
   530		contentLength, err := elem.Text()
   531		if err != nil {
   532			return errors.Wrap(err, "获取正文长度文本失败")
   533		}
   534	
   535		return makeMaxLengthError(contentLength)
   536	}
   537	
   538	func makeMaxLengthError(elemText string) error {
   539		parts := strings.Split(elemText, "/")
   540		if len(parts) != 2 {
   541			return errors.Errorf("长度超过限制: %s", elemText)
   542		}
   543	
   544		currLen, maxLen := parts[0], parts[1]
   545	
   546		return errors.Errorf("当前输入长度为%s，最大长度为%s", currLen, maxLen)
   547	}
   548	
   549	// 查找内容输入框 - 使用Race方法处理两种样式
   550	func getContentElement(page *hrod.Page) (*hrod.Element, bool) {
   551		var foundElement *rod.Element
   552		var found bool
   553	
   554		page.Rod.Race().
   555			Element("div.ql-editor").MustHandle(func(e *rod.Element) {
   556			foundElement = e
   557			found = true
   558		}).
   559			ElementFunc(func(page *rod.Page) (*rod.Element, error) {
   560				return findTextboxByPlaceholder(page)
   561			}).MustHandle(func(e *rod.Element) {
   562			foundElement = e
   563			found = true
   564		}).
   565			MustDo()
   566	
   567		if found {
   568			return hrod.NewElement(foundElement, page.Actor()), true
   569		}
   570	
   571		slog.Warn("no content element found by any method")
   572		return nil, false
   573	}
   574	
   575	func inputTags(contentElem *hrod.Element, tags []string) error {
   576		if len(tags) == 0 {
   577			return nil
   578		}
   579	
   580		time.Sleep(1 * time.Second)
   581	
   582		for i := 0; i < 20; i++ {
   583			ka, err := contentElem.KeyActions()
   584			if err != nil {
   585				return errors.Wrap(err, "创建键盘操作失败")
   586			}
   587			if err := ka.Type(input.ArrowDown).Do(); err != nil {
   588				return errors.Wrap(err, "按下方向键失败")
   589			}
   590			time.Sleep(10 * time.Millisecond)
   591		}
   592	
   593		ka, err := contentElem.KeyActions()
   594		if err != nil {
   595			return errors.Wrap(err, "创建键盘操作失败")
   596		}
   597		if err := ka.Press(input.Enter).Press(input.Enter).Do(); err != nil {
   598			return errors.Wrap(err, "按下回车键失败")
   599		}
   600	
   601		time.Sleep(1 * time.Second)
   602	
   603		for _, tag := range tags {
   604			tag = strings.TrimLeft(tag, "#")
   605			if err := inputTag(contentElem, tag); err != nil {
   606				return errors.Wrapf(err, "输入标签[%s]失败", tag)
   607			}
   608		}
   609		return nil
   610	}
   611	
   612	func inputTag(contentElem *hrod.Element, tag string) error {
   613		if err := contentElem.Input("#"); err != nil {
   614			return errors.Wrap(err, "输入#失败")
   615		}
   616		time.Sleep(200 * time.Millisecond)
   617	
   618		for _, char := range tag {
   619			if err := contentElem.Input(string(char)); err != nil {
   620				return errors.Wrapf(err, "输入字符[%c]失败", char)
   621			}
   622			time.Sleep(50 * time.Millisecond)
   623		}
   624	
   625		time.Sleep(1 * time.Second)
   626	
   627		page := contentElem.Page()
   628		topicContainer, err := page.Element("#creator-editor-topic-container")
   629		if err != nil || topicContainer == nil {
   630			slog.Warn("未找到标签联想下拉框，直接输入空格", "tag", tag)
   631			return contentElem.Input(" ")
   632		}
   633	
   634		firstItem, err := topicContainer.Element(".item")
   635		if err != nil || firstItem == nil {
   636			slog.Warn("未找到标签联想选项，直接输入空格", "tag", tag)
   637			return contentElem.Input(" ")
   638		}
   639	
   640		if err := firstItem.Click(proto.InputMouseButtonLeft, 1); err != nil {
   641			return errors.Wrap(err, "点击标签联想选项失败")
   642		}
   643		slog.Info("成功点击标签联想选项", "tag", tag)
   644		time.Sleep(200 * time.Millisecond)
   645	
   646		time.Sleep(500 * time.Millisecond) // 等待标签处理完成
   647		return nil
   648	}
   649	
   650	func findTextboxByPlaceholder(page *rod.Page) (*rod.Element, error) {
   651		elements := page.MustElements("p")
   652		if elements == nil {
   653			return nil, errors.New("no p elements found")
   654		}
   655	
   656		// 查找包含指定placeholder的元素
   657		placeholderElem := findPlaceholderElement(elements, "输入正文描述")
   658		if placeholderElem == nil {
   659			return nil, errors.New("no placeholder element found")
   660		}
   661	
   662		// 向上查找textbox父元素
   663		textboxElem := findTextboxParent(placeholderElem)
   664		if textboxElem == nil {
   665			return nil, errors.New("no textbox parent found")
   666		}
   667	
   668		return textboxElem, nil
   669	}
   670	
   671	func findPlaceholderElement(elements rod.Elements, searchText string) *rod.Element {
   672		for _, elem := range elements {
   673			placeholder, err := elem.Attribute("data-placeholder")
   674			if err != nil || placeholder == nil {
   675				continue
   676			}
   677	
   678			if strings.Contains(*placeholder, searchText) {
   679				return elem
   680			}
   681		}
   682		return nil
   683	}
   684	
   685	func findTextboxParent(elem *rod.Element) *rod.Element {
   686		currentElem := elem
   687		for i := 0; i < 5; i++ {
   688			parent, err := currentElem.Parent()
   689			if err != nil {
   690				break
   691			}
   692	
   693			role, err := parent.Attribute("role")
   694			if err != nil || role == nil {
   695				currentElem = parent
   696				continue
   697			}
   698	
   699			if *role == "textbox" {
   700				return parent
   701			}
   702	
   703			currentElem = parent
   704		}
   705		return nil
   706	}
   707	
   708	// isElementVisible 检查元素是否可见
   709	func isElementVisible(elem *hrod.Element) bool {
   710	
   711		// 检查是否有隐藏样式
   712		style, err := elem.Attribute("style")
   713		if err == nil && style != nil {
   714			styleStr := *style
   715	
   716			if strings.Contains(styleStr, "left: -9999px") ||
   717				strings.Contains(styleStr, "top: -9999px") ||
   718				strings.Contains(styleStr, "position: absolute; left: -9999px") ||
   719				strings.Contains(styleStr, "display: none") ||
   720				strings.Contains(styleStr, "visibility: hidden") ||
   721				strings.Contains(styleStr, "opacity: 1e-05") {
   722				return false
   723			}
   724	
   725			// 精确匹配 opacity: 0（不匹配 0.5、0.1 等）
   726			if strings.Contains(styleStr, "opacity: 0") {
   727				// 确认是 opacity: 0 而非 opacity: 0.x
   728				if matched, _ := regexp.MatchString(`opacity:\s*0(\s|;|$)`, styleStr); matched {
   729					return false
   730				}
   731			}
   732		}
   733	
   734		// 检查 aria-hidden 属性
   735		ariaHidden, err := elem.Attribute("aria-hidden")
   736		if err == nil && ariaHidden != nil && *ariaHidden == "true" {
   737			return false
   738		}
   739	
   740		// 检查 tabindex 属性（-1 表示不可聚焦，通常也意味着不可见）
   741		tabindex, err := elem.Attribute("tabindex")
   742		if err == nil && tabindex != nil && *tabindex == "-1" {
   743			// 结合检查是否有 active class 来判断是否是真正的隐藏
   744			class, _ := elem.Attribute("class")
   745			// 使用单词边界检查，避免匹配 "inactive" 等
   746			if class == nil || !hasExactClass(*class, "active") {
   747				// 不是激活状态的 -1 tabindex 元素，可能是隐藏的叠加层
   748				return false
   749			}
   750		}
   751	
   752		visible, err := elem.Visible()
   753		if err != nil {
   754			slog.Warn("无法获取元素可见性", "error", err)
   755			return true
   756		}
   757	
   758		return visible
   759	}
   760	
   761	// hasExactClass 检查 class 字符串是否包含指定的完整类名（单词边界匹配）
   762	func hasExactClass(classStr, className string) bool {
   763		pattern := `\b` + regexp.QuoteMeta(className) + `\b`
   764		matched, _ := regexp.MatchString(pattern, classStr)
   765		return matched
   766	}
   767	
   768	// setVisibility 设置可见范围
   769	// 支持: "公开可见"(默认), "仅自己可见", "仅互关好友可见"
   770	func setVisibility(page *hrod.Page, visibility string) error {
   771		if visibility == "" || visibility == "公开可见" {
   772			slog.Info("可见范围使用默认：公开可见")
   773			return nil
   774		}
   775	
   776		// 支持的选项校验
   777		supported := map[string]bool{"仅自己可见": true, "仅互关好友可见": true}
   778		if !supported[visibility] {
   779			return errors.Errorf("不支持的可见范围: %s，支持: 公开可见、仅自己可见、仅互关好友可见", visibility)
   780		}
   781	
   782		// 点击可见范围下拉框
   783		dropdown, err := page.Element("div.permission-card-wrapper div.d-select-content")
   784		if err != nil {
   785			return errors.Wrap(err, "查找可见范围下拉框失败")
   786		}
   787		if err := dropdown.Click(proto.InputMouseButtonLeft, 1); err != nil {
   788			return errors.Wrap(err, "点击可见范围下拉框失败")
   789		}
   790		time.Sleep(500 * time.Millisecond)
   791	
   792		// 在弹窗中查找并点击目标选项
   793		opts, err := page.Elements("div.d-options-wrapper div.d-grid-item div.custom-option")
   794		if err != nil {
   795			return errors.Wrap(err, "查找可见范围选项失败")
   796		}
   797		for _, opt := range opts {
   798			text, err := opt.Text()
   799			if err != nil {
   800				continue
   801			}
   802			if strings.Contains(text, visibility) {
   803				if err := opt.Click(proto.InputMouseButtonLeft, 1); err != nil {
   804					return errors.Wrap(err, "选择可见范围失败")
   805				}
   806				slog.Info("已设置可见范围", "visibility", visibility)
   807				time.Sleep(200 * time.Millisecond)
   808				return nil
   809			}
   810		}
   811		return errors.Errorf("未找到可见范围选项: %s", visibility)
   812	}
   813	
   814	// setSchedulePublish 设置定时发布时间
   815	func setSchedulePublish(page *hrod.Page, t time.Time) error {
   816		// 1. 点击定时发布开关
   817		if err := clickScheduleSwitch(page); err != nil {
   818			return err
   819		}
   820		time.Sleep(800 * time.Millisecond)
   821	
   822		// 2. 设置日期时间
   823		if err := setDateTime(page, t); err != nil {
   824			return err
   825		}
   826		time.Sleep(500 * time.Millisecond)
   827	
   828		return nil
   829	}
   830	
   831	// clickScheduleSwitch 点击定时发布开关
   832	func clickScheduleSwitch(page *hrod.Page) error {
   833		switchElem, err := page.Element(".post-time-wrapper .d-switch")
   834		if err != nil {
   835			return errors.Wrap(err, "查找定时发布开关失败")
   836		}
   837	
   838		if err := switchElem.Click(proto.InputMouseButtonLeft, 1); err != nil {
   839			return errors.Wrap(err, "点击定时发布开关失败")
   840		}

exec
/bin/bash -lc "nl -ba xiaohongshu/comment_feed.go | sed -n '1,300p'; nl -ba xiaohongshu/like_favorite.go; nl -ba xiaohongshu/login.go; nl -ba xiaohongshu/feeds.go" in /tmp/xiaohongshu-mcp-fork
exec
/bin/bash -lc "nl -ba xiaohongshu/feed_detail.go | sed -n '1,370p'; nl -ba xiaohongshu/feed_detail.go | sed -n '400,570p'; nl -ba xiaohongshu/feed_detail.go | sed -n '720,770p'" in /tmp/xiaohongshu-mcp-fork
 succeeded in 0ms:
     1	package xiaohongshu
     2	
     3	import (
     4		"context"
     5		"fmt"
     6		"time"
     7	
     8		hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
     9		"github.com/go-rod/rod/lib/proto"
    10		"github.com/sirupsen/logrus"
    11	)
    12	
    13	// CommentFeedAction 表示 Feed 评论动作
    14	type CommentFeedAction struct {
    15		page *hrod.Page
    16	}
    17	
    18	// NewCommentFeedAction 创建 Feed 评论动作
    19	func NewCommentFeedAction(page *hrod.Page) *CommentFeedAction {
    20		return &CommentFeedAction{page: page}
    21	}
    22	
    23	// PostComment 发表评论到 Feed
    24	func (f *CommentFeedAction) PostComment(ctx context.Context, feedID, xsecToken, content string) error {
    25		// 不使用 Context(ctx)，避免继承外部 context 的超时
    26		page := f.page.Timeout(60 * time.Second)
    27	
    28		url := makeFeedDetailURL(feedID, xsecToken)
    29		logrus.Infof("打开 feed 详情页: %s", url)
    30	
    31		// 导航到详情页
    32		page.MustNavigate(url)
    33		page.MustWaitDOMStable()
    34		time.Sleep(1 * time.Second)
    35	
    36		// 检测页面是否可访问
    37		if err := checkPageAccessible(page); err != nil {
    38			return err
    39		}
    40	
    41		elem, err := page.Element("div.input-box div.content-edit span")
    42		if err != nil {
    43			logrus.Warnf("Failed to find comment input box: %v", err)
    44			return fmt.Errorf("未找到评论输入框，该帖子可能不支持评论或网页端不可访问: %w", err)
    45		}
    46	
    47		if err := elem.Click(proto.InputMouseButtonLeft, 1); err != nil {
    48			logrus.Warnf("Failed to click comment input box: %v", err)
    49			return fmt.Errorf("无法点击评论输入框: %w", err)
    50		}
    51	
    52		elem2, err := page.Element("div.input-box div.content-edit p.content-input")
    53		if err != nil {
    54			logrus.Warnf("Failed to find comment input field: %v", err)
    55			return fmt.Errorf("未找到评论输入区域: %w", err)
    56		}
    57	
    58		if err := elem2.Input(content); err != nil {
    59			logrus.Warnf("Failed to input comment content: %v", err)
    60			return fmt.Errorf("无法输入评论内容: %w", err)
    61		}
    62	
    63		time.Sleep(1 * time.Second)
    64	
    65		submitButton, err := page.Element("div.bottom button.submit")
    66		if err != nil {
    67			logrus.Warnf("Failed to find submit button: %v", err)
    68			return fmt.Errorf("未找到提交按钮: %w", err)
    69		}
    70	
    71		if err := submitButton.Click(proto.InputMouseButtonLeft, 1); err != nil {
    72			logrus.Warnf("Failed to click submit button: %v", err)
    73			return fmt.Errorf("无法点击提交按钮: %w", err)
    74		}
    75	
    76		time.Sleep(1 * time.Second)
    77	
    78		logrus.Infof("Comment posted successfully to feed: %s", feedID)
    79		return nil
    80	}
    81	
    82	// ReplyToComment 回复指定评论
    83	func (f *CommentFeedAction) ReplyToComment(ctx context.Context, feedID, xsecToken, commentID, userID, content string) error {
    84		// 增加超时时间，因为需要滚动查找评论
    85		// 注意：不使用 Context(ctx)，避免继承外部 context 的超时
    86		page := f.page.Timeout(5 * time.Minute)
    87		url := makeFeedDetailURL(feedID, xsecToken)
    88		logrus.Infof("打开 feed 详情页进行回复: %s", url)
    89	
    90		// 导航到详情页
    91		page.MustNavigate(url)
    92		page.MustWaitDOMStable()
    93		time.Sleep(1 * time.Second)
    94	
    95		// 检测页面是否可访问
    96		if err := checkPageAccessible(page); err != nil {
    97			return err
    98		}
    99	
   100		// 等待评论容器加载
   101		time.Sleep(2 * time.Second)
   102	
   103		// 使用 Go 实现的查找逻辑
   104		commentEl, err := findCommentElement(page, commentID, userID)
   105		if err != nil {
   106			return fmt.Errorf("无法找到评论: %w", err)
   107		}
   108	
   109		// 滚动到评论位置
   110		logrus.Info("滚动到评论位置...")
   111		commentEl.MustScrollIntoView()
   112		time.Sleep(1 * time.Second)
   113	
   114		logrus.Info("准备点击回复按钮")
   115	
   116		// 查找并点击回复按钮
   117		replyBtn, err := commentEl.Element(".right .interactions .reply")
   118		if err != nil {
   119			return fmt.Errorf("无法找到回复按钮: %w", err)
   120		}
   121	
   122		if err := replyBtn.Click(proto.InputMouseButtonLeft, 1); err != nil {
   123			return fmt.Errorf("点击回复按钮失败: %w", err)
   124		}
   125	
   126		time.Sleep(1 * time.Second)
   127	
   128		// 查找回复输入框
   129		inputEl, err := page.Element("div.input-box div.content-edit p.content-input")
   130		if err != nil {
   131			return fmt.Errorf("无法找到回复输入框: %w", err)
   132		}
   133	
   134		// 输入内容
   135		if err := inputEl.Input(content); err != nil {
   136			return fmt.Errorf("输入回复内容失败: %w", err)
   137		}
   138	
   139		time.Sleep(500 * time.Millisecond)
   140	
   141		// 查找并点击提交按钮
   142		submitBtn, err := page.Element("div.bottom button.submit")
   143		if err != nil {
   144			return fmt.Errorf("无法找到提交按钮: %w", err)
   145		}
   146	
   147		if err := submitBtn.Click(proto.InputMouseButtonLeft, 1); err != nil {
   148			return fmt.Errorf("点击提交按钮失败: %w", err)
   149		}
   150	
   151		time.Sleep(2 * time.Second)
   152		logrus.Infof("回复评论成功")
   153		return nil
   154	}
   155	
   156	// findCommentElement 查找指定评论元素（参考 feed_detail.go 的滚动逻辑）
   157	func findCommentElement(page *hrod.Page, commentID, userID string) (*hrod.Element, error) {
   158		logrus.Infof("开始查找评论 - commentID: %s, userID: %s", commentID, userID)
   159	
   160		const maxAttempts = 100
   161		const scrollInterval = 800 * time.Millisecond
   162	
   163		// 先滚动到评论区
   164		scrollToCommentsArea(page)
   165		time.Sleep(1 * time.Second)
   166	
   167		var lastCommentCount = 0
   168		stagnantChecks := 0
   169	
   170		logrus.Infof("开始循环查找，最大尝试次数: %d", maxAttempts)
   171	
   172		for attempt := 0; attempt < maxAttempts; attempt++ {
   173			logrus.Infof("=== 查找尝试 %d/%d ===", attempt+1, maxAttempts)
   174	
   175			// === 1. 检查是否到达底部 ===
   176			if checkEndContainer(page) {
   177				logrus.Info("已到达评论底部，未找到目标评论")
   178				break
   179			}
   180	
   181			// === 2. 获取当前评论数量 ===
   182			currentCount := getCommentCount(page)
   183			logrus.Infof("当前评论数: %d", currentCount)
   184			
   185			if currentCount != lastCommentCount {
   186				logrus.Infof("✓ 评论数增加: %d -> %d", lastCommentCount, currentCount)
   187				lastCommentCount = currentCount
   188				stagnantChecks = 0
   189			} else {
   190				stagnantChecks++
   191				if stagnantChecks%5 == 0 {
   192					logrus.Infof("评论数停滞 %d 次", stagnantChecks)
   193				}
   194			}
   195	
   196			// === 3. 停滞检测 ===
   197			if stagnantChecks >= 10 {
   198				logrus.Info("评论数量停滞超过10次，可能已加载完所有评论")
   199				break
   200			}
   201	
   202			// === 4. 先滚动到最后一个评论（触发懒加载）===
   203			if currentCount > 0 {
   204				logrus.Infof("滚动到最后一个评论（共 %d 条）", currentCount)
   205				
   206				// 使用 Go 获取所有评论元素
   207				elements, err := page.Timeout(2 * time.Second).Elements(".parent-comment, .comment-item, .comment")
   208				if err == nil && len(elements) > 0 {
   209					// 滚动到最后一个评论
   210					lastComment := elements[len(elements)-1]
   211					err := lastComment.ScrollIntoView()
   212					if err != nil {
   213						logrus.Warnf("滚动到最后一个评论失败: %v", err)
   214					}
   215				} else {
   216					logrus.Warnf("未找到评论元素: %v", err)
   217				}
   218				time.Sleep(300 * time.Millisecond)
   219			}
   220	
   221			// === 5. 继续向下滚动 ===
   222			logrus.Infof("继续向下滚动...")
   223			viewportHeight := page.MustEval(`() => window.innerHeight`).Int()
   224			if err := page.Actor().Mouse.Scroll(0, float64(viewportHeight)*0.8); err != nil {
   225				logrus.Warnf("滚动失败: %v", err)
   226			}
   227			time.Sleep(500 * time.Millisecond)
   228	
   229			// === 6. 滚动后立即查找（边滚动边查找）===
   230			// 优先通过 commentID 查找（使用 Timeout 避免长时间等待）
   231			if commentID != "" {
   232				selector := fmt.Sprintf("#comment-%s", commentID)
   233				logrus.Infof("尝试通过 commentID 查找: %s", selector)
   234				
   235				// 使用 Timeout 避免长时间等待
   236				el, err := page.Timeout(2 * time.Second).Element(selector)
   237				if err == nil && el != nil {
   238					logrus.Infof("✓ 通过 commentID 找到评论: %s (尝试 %d 次)", commentID, attempt+1)
   239					return el, nil
   240				}
   241				logrus.Infof("未找到 commentID (2秒超时)")
   242			}
   243	
   244			// 通过 userID 查找
   245			if userID != "" {
   246				logrus.Infof("尝试通过 userID 查找: %s", userID)
   247				
   248				// 使用 Timeout 避免长时间等待
   249				elements, err := page.Timeout(2 * time.Second).Elements(".comment-item, .comment, .parent-comment")
   250				if err == nil && len(elements) > 0 {
   251					logrus.Infof("找到 %d 个评论元素", len(elements))
   252					for i, el := range elements {
   253						// 快速检查，不等待
   254						userEl, err := el.Timeout(500 * time.Millisecond).Element(fmt.Sprintf(`[data-user-id="%s"]`, userID))
   255						if err == nil && userEl != nil {
   256							logrus.Infof("✓ 通过 userID 在第 %d 个元素中找到评论: %s (尝试 %d 次)", i+1, userID, attempt+1)
   257							return el, nil
   258						}
   259					}
   260					logrus.Infof("在 %d 个元素中未找到匹配的 userID", len(elements))
   261				} else {
   262					logrus.Infof("获取评论元素失败或超时: %v", err)
   263				}
   264			}
   265			
   266			logrus.Infof("本次尝试未找到目标评论，继续下一轮...")
   267	
   268			// === 7. 等待内容加载 ===
   269			time.Sleep(scrollInterval)
   270		}
   271	
   272		return nil, fmt.Errorf("未找到评论 (commentID: %s, userID: %s), 尝试次数: %d", commentID, userID, maxAttempts)
   273	}
     1	package xiaohongshu
     2	
     3	import (
     4		"context"
     5		"encoding/json"
     6		"fmt"
     7		"time"
     8	
     9		hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
    10		"github.com/pkg/errors"
    11		"github.com/sirupsen/logrus"
    12		myerrors "github.com/xpzouying/xiaohongshu-mcp/errors"
    13	)
    14	
    15	// ActionResult 通用动作响应（点赞/收藏等）
    16	type ActionResult struct {
    17		FeedID  string `json:"feed_id"`
    18		Success bool   `json:"success"`
    19		Message string `json:"message"`
    20	}
    21	
    22	// 选择器常量
    23	const (
    24		SelectorLikeButton    = ".interact-container .left .like-lottie"
    25		SelectorCollectButton = ".interact-container .left .reds-icon.collect-icon"
    26	)
    27	
    28	// interactActionType 交互动作类型
    29	type interactActionType string
    30	
    31	const (
    32		actionLike       interactActionType = "点赞"
    33		actionFavorite   interactActionType = "收藏"
    34		actionUnlike     interactActionType = "取消点赞"
    35		actionUnfavorite interactActionType = "取消收藏"
    36	)
    37	
    38	type interactAction struct {
    39		page *hrod.Page
    40	}
    41	
    42	func newInteractAction(page *hrod.Page) *interactAction {
    43		return &interactAction{page: page}
    44	}
    45	
    46	func (a *interactAction) preparePage(ctx context.Context, actionType interactActionType, feedID, xsecToken string) *hrod.Page {
    47		page := a.page.Context(ctx).Timeout(60 * time.Second)
    48		url := makeFeedDetailURL(feedID, xsecToken)
    49		logrus.Infof("Opening feed detail page for %s: %s", actionType, url)
    50	
    51		page.MustNavigate(url)
    52		page.MustWaitDOMStable()
    53		time.Sleep(1 * time.Second)
    54	
    55		return page
    56	}
    57	
    58	func (a *interactAction) performClick(page *hrod.Page, selector string) {
    59		element := page.MustElement(selector)
    60		element.MustClick()
    61	}
    62	
    63	// LikeAction 负责处理点赞相关交互
    64	type LikeAction struct {
    65		*interactAction
    66	}
    67	
    68	func NewLikeAction(page *hrod.Page) *LikeAction {
    69		return &LikeAction{interactAction: newInteractAction(page)}
    70	}
    71	
    72	// Like 点赞指定笔记，如果已点赞则直接返回
    73	func (a *LikeAction) Like(ctx context.Context, feedID, xsecToken string) error {
    74		return a.perform(ctx, feedID, xsecToken, true)
    75	}
    76	
    77	// Unlike 取消点赞指定笔记，如果未点赞则直接返回
    78	func (a *LikeAction) Unlike(ctx context.Context, feedID, xsecToken string) error {
    79		return a.perform(ctx, feedID, xsecToken, false)
    80	}
    81	
    82	func (a *LikeAction) perform(ctx context.Context, feedID, xsecToken string, targetLiked bool) error {
    83		actionType := actionLike
    84		if !targetLiked {
    85			actionType = actionUnlike
    86		}
    87	
    88		page := a.preparePage(ctx, actionType, feedID, xsecToken)
    89	
    90		liked, _, err := a.getInteractState(page, feedID)
    91		if err != nil {
    92			logrus.Warnf("failed to read interact state: %v (continue to try clicking)", err)
    93			return a.toggleLike(page, feedID, targetLiked, actionType)
    94		}
    95	
    96		if targetLiked && liked {
    97			logrus.Infof("feed %s already liked, skip clicking", feedID)
    98			return nil
    99		}
   100		if !targetLiked && !liked {
   101			logrus.Infof("feed %s not liked yet, skip clicking", feedID)
   102			return nil
   103		}
   104	
   105		return a.toggleLike(page, feedID, targetLiked, actionType)
   106	}
   107	
   108	func (a *LikeAction) toggleLike(page *hrod.Page, feedID string, targetLiked bool, actionType interactActionType) error {
   109		a.performClick(page, SelectorLikeButton)
   110		time.Sleep(3 * time.Second)
   111	
   112		liked, _, err := a.getInteractState(page, feedID)
   113		if err != nil {
   114			logrus.Warnf("验证%s状态失败: %v", actionType, err)
   115			return nil
   116		}
   117		if liked == targetLiked {
   118			logrus.Infof("feed %s %s成功", feedID, actionType)
   119			return nil
   120		}
   121	
   122		logrus.Warnf("feed %s %s可能未成功，状态未变化，尝试再次点击", feedID, actionType)
   123		a.performClick(page, SelectorLikeButton)
   124		time.Sleep(2 * time.Second)
   125	
   126		liked, _, err = a.getInteractState(page, feedID)
   127		if err != nil {
   128			logrus.Warnf("第二次验证%s状态失败: %v", actionType, err)
   129			return nil
   130		}
   131		if liked == targetLiked {
   132			logrus.Infof("feed %s 第二次点击%s成功", feedID, actionType)
   133			return nil
   134		}
   135	
   136		return nil
   137	}
   138	
   139	// FavoriteAction 负责处理收藏相关交互
   140	type FavoriteAction struct {
   141		*interactAction
   142	}
   143	
   144	func NewFavoriteAction(page *hrod.Page) *FavoriteAction {
   145		return &FavoriteAction{interactAction: newInteractAction(page)}
   146	}
   147	
   148	// Favorite 收藏指定笔记，如果已收藏则直接返回
   149	func (a *FavoriteAction) Favorite(ctx context.Context, feedID, xsecToken string) error {
   150		return a.perform(ctx, feedID, xsecToken, true)
   151	}
   152	
   153	// Unfavorite 取消收藏指定笔记，如果未收藏则直接返回
   154	func (a *FavoriteAction) Unfavorite(ctx context.Context, feedID, xsecToken string) error {
   155		return a.perform(ctx, feedID, xsecToken, false)
   156	}
   157	
   158	func (a *FavoriteAction) perform(ctx context.Context, feedID, xsecToken string, targetCollected bool) error {
   159		actionType := actionFavorite
   160		if !targetCollected {
   161			actionType = actionUnfavorite
   162		}
   163	
   164		page := a.preparePage(ctx, actionType, feedID, xsecToken)
   165	
   166		_, collected, err := a.getInteractState(page, feedID)
   167		if err != nil {
   168			logrus.Warnf("failed to read interact state: %v (continue to try clicking)", err)
   169			return a.toggleFavorite(page, feedID, targetCollected, actionType)
   170		}
   171	
   172		if targetCollected && collected {
   173			logrus.Infof("feed %s already favorited, skip clicking", feedID)
   174			return nil
   175		}
   176		if !targetCollected && !collected {
   177			logrus.Infof("feed %s not favorited yet, skip clicking", feedID)
   178			return nil
   179		}
   180	
   181		return a.toggleFavorite(page, feedID, targetCollected, actionType)
   182	}
   183	
   184	func (a *FavoriteAction) toggleFavorite(page *hrod.Page, feedID string, targetCollected bool, actionType interactActionType) error {
   185		a.performClick(page, SelectorCollectButton)
   186		time.Sleep(3 * time.Second)
   187	
   188		_, collected, err := a.getInteractState(page, feedID)
   189		if err != nil {
   190			logrus.Warnf("验证%s状态失败: %v", actionType, err)
   191			return nil
   192		}
   193		if collected == targetCollected {
   194			logrus.Infof("feed %s %s成功", feedID, actionType)
   195			return nil
   196		}
   197	
   198		logrus.Warnf("feed %s %s可能未成功，状态未变化，尝试再次点击", feedID, actionType)
   199		a.performClick(page, SelectorCollectButton)
   200		time.Sleep(2 * time.Second)
   201	
   202		_, collected, err = a.getInteractState(page, feedID)
   203		if err != nil {
   204			logrus.Warnf("第二次验证%s状态失败: %v", actionType, err)
   205			return nil
   206		}
   207		if collected == targetCollected {
   208			logrus.Infof("feed %s 第二次点击%s成功", feedID, actionType)
   209			return nil
   210		}
   211	
   212		return nil
   213	}
   214	
   215	// getInteractState 从 __INITIAL_STATE__ 读取笔记的点赞/收藏状态
   216	func (a *interactAction) getInteractState(page *hrod.Page, feedID string) (liked bool, collected bool, err error) {
   217	
   218		result := page.MustEval(`() => {
   219			if (window.__INITIAL_STATE__ &&
   220			    window.__INITIAL_STATE__.note &&
   221			    window.__INITIAL_STATE__.note.noteDetailMap) {
   222				return JSON.stringify(window.__INITIAL_STATE__.note.noteDetailMap);
   223			}
   224			return "";
   225		}`).String()
   226		if result == "" {
   227			return false, false, myerrors.ErrNoFeedDetail
   228		}
   229	
   230		// 直接解析为 noteDetailMap
   231		var noteDetailMap map[string]struct {
   232			Note struct {
   233				InteractInfo struct {
   234					Liked     bool `json:"liked"`
   235					Collected bool `json:"collected"`
   236				} `json:"interactInfo"`
   237			} `json:"note"`
   238		}
   239		if err := json.Unmarshal([]byte(result), &noteDetailMap); err != nil {
   240			return false, false, errors.Wrap(err, "unmarshal noteDetailMap failed")
   241		}
   242	
   243		detail, ok := noteDetailMap[feedID]
   244		if !ok {
   245			return false, false, fmt.Errorf("feed %s not in noteDetailMap", feedID)
   246		}
   247		return detail.Note.InteractInfo.Liked, detail.Note.InteractInfo.Collected, nil
   248	}
     1	package xiaohongshu
     2	
     3	import (
     4		"context"
     5		"time"
     6	
     7		hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
     8		"github.com/pkg/errors"
     9	)
    10	
    11	type LoginAction struct {
    12		page *hrod.Page
    13	}
    14	
    15	func NewLogin(page *hrod.Page) *LoginAction {
    16		return &LoginAction{page: page}
    17	}
    18	
    19	func (a *LoginAction) CheckLoginStatus(ctx context.Context) (bool, error) {
    20		pp := a.page.Context(ctx)
    21		pp.MustNavigate("https://www.xiaohongshu.com/explore").MustWaitLoad()
    22	
    23		time.Sleep(1 * time.Second)
    24	
    25		exists, _, err := pp.Has(`.main-container .user .link-wrapper .channel`)
    26		if err != nil {
    27			return false, errors.Wrap(err, "check login status failed")
    28		}
    29	
    30		if !exists {
    31			return false, errors.Wrap(err, "login status element not found")
    32		}
    33	
    34		return true, nil
    35	}
    36	
    37	func (a *LoginAction) Login(ctx context.Context) error {
    38		pp := a.page.Context(ctx)
    39	
    40		// 导航到小红书首页，这会触发二维码弹窗
    41		pp.MustNavigate("https://www.xiaohongshu.com/explore").MustWaitLoad()
    42	
    43		// 等待一小段时间让页面完全加载
    44		time.Sleep(2 * time.Second)
    45	
    46		// 检查是否已经登录
    47		if exists, _, _ := pp.Has(".main-container .user .link-wrapper .channel"); exists {
    48			// 已经登录，直接返回
    49			return nil
    50		}
    51	
    52		// 等待扫码成功提示或者登录完成
    53		// 这里我们等待登录成功的元素出现，这样更简单可靠
    54		pp.MustElement(".main-container .user .link-wrapper .channel")
    55	
    56		return nil
    57	}
    58	
    59	func (a *LoginAction) FetchQrcodeImage(ctx context.Context) (string, bool, error) {
    60		pp := a.page.Context(ctx)
    61	
    62		// 导航到小红书首页，这会触发二维码弹窗
    63		pp.MustNavigate("https://www.xiaohongshu.com/explore").MustWaitLoad()
    64	
    65		// 等待一小段时间让页面完全加载
    66		time.Sleep(2 * time.Second)
    67	
    68		// 检查是否已经登录
    69		if exists, _, _ := pp.Has(".main-container .user .link-wrapper .channel"); exists {
    70			return "", true, nil
    71		}
    72	
    73		// 获取二维码图片
    74		src, err := pp.MustElement(".login-container .qrcode-img").Attribute("src")
    75		if err != nil {
    76			return "", false, errors.Wrap(err, "get qrcode src failed")
    77		}
    78		if src == nil || len(*src) == 0 {
    79			return "", false, errors.New("qrcode src is empty")
    80		}
    81	
    82		return *src, false, nil
    83	}
    84	
    85	func (a *LoginAction) WaitForLogin(ctx context.Context) bool {
    86		pp := a.page.Context(ctx)
    87		ticker := time.NewTicker(500 * time.Millisecond)
    88		defer ticker.Stop()
    89	
    90		for {
    91			select {
    92			case <-ctx.Done():
    93				return false
    94			case <-ticker.C:
    95				el, err := pp.Element(".main-container .user .link-wrapper .channel")
    96				if err == nil && el != nil {
    97					return true
    98				}
    99			}
   100		}
   101	}
     1	package xiaohongshu
     2	
     3	import (
     4		"context"
     5		"encoding/json"
     6		"fmt"
     7		"time"
     8	
     9		hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
    10		"github.com/xpzouying/xiaohongshu-mcp/errors"
    11	)
    12	
    13	type FeedsListAction struct {
    14		page *hrod.Page
    15	}
    16	
    17	func NewFeedsListAction(page *hrod.Page) *FeedsListAction {
    18		pp := page.Timeout(60 * time.Second)
    19	
    20		pp.MustNavigate("https://www.xiaohongshu.com")
    21		pp.MustWaitDOMStable()
    22	
    23		return &FeedsListAction{page: pp}
    24	}
    25	
    26	// GetFeedsList 获取页面的 Feed 列表数据
    27	func (f *FeedsListAction) GetFeedsList(ctx context.Context) ([]Feed, error) {
    28		page := f.page.Context(ctx)
    29	
    30		time.Sleep(1 * time.Second)
    31	
    32		result := page.MustEval(`() => {
    33			if (window.__INITIAL_STATE__ &&
    34			    window.__INITIAL_STATE__.feed &&
    35			    window.__INITIAL_STATE__.feed.feeds) {
    36				const feeds = window.__INITIAL_STATE__.feed.feeds;
    37				const feedsData = feeds.value !== undefined ? feeds.value : feeds._value;
    38				if (feedsData) {
    39					return JSON.stringify(feedsData);
    40				}
    41			}
    42			return "";
    43		}`).String()
    44	
    45		if result == "" {
    46			return nil, errors.ErrNoFeeds
    47		}
    48	
    49		var feeds []Feed
    50		if err := json.Unmarshal([]byte(result), &feeds); err != nil {
    51			return nil, fmt.Errorf("failed to unmarshal feeds: %w", err)
    52		}
    53	
    54		return feeds, nil
    55	}

exec
/bin/bash -lc "nl -ba xiaohongshu/publish.go | sed -n '860,1270p'; nl -ba xiaohongshu/publish_video.go; nl -ba pkg/humanize/rod/hrod.go | sed -n '580,655p'; rg -n 'NewWithContext|SetContext|Actor\\(' --glob '*.go'" in /tmp/xiaohongshu-mcp-fork
 succeeded in 123ms:
   400		if threshold <= 0 {
   401			return false
   402		}
   403	
   404		matches := regex.FindStringSubmatch(text)
   405		if len(matches) > 1 {
   406			if replyCount, err := strconv.Atoi(matches[1]); err == nil && replyCount > threshold {
   407				logrus.Debugf("跳过'%s'（回复数 %d > 阈值 %d）", text, replyCount, threshold)
   408				return true
   409			}
   410		}
   411		return false
   412	}
   413	
   414	func clickElementWithHumanBehavior(page *hrod.Page, el *hrod.Element, text string) bool {
   415		var clickSuccess bool
   416	
   417		// 使用retry-go进行点击操作重试
   418		err := retry.Do(
   419			func() error {
   420				// 滚动到元素
   421				el.MustScrollIntoView()
   422	
   423				sleepRandom(reactionTimeRange.min, reactionTimeRange.max)
   424	
   425				// 鼠标悬停
   426				if box, err := el.Shape(); err == nil && len(box.Quads) > 0 {
   427					x := float64(box.Quads[0][0]+box.Quads[0][4]) / 2
   428					y := float64(box.Quads[0][1]+box.Quads[0][5]) / 2
   429					if err := page.MovePoint(proto.Point{X: x, Y: y}); err != nil {
   430						return err
   431					}
   432					sleepRandom(hoverTimeRange.min, hoverTimeRange.max)
   433				}
   434	
   435				// 点击
   436				if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
   437					return err // 返回错误以触发重试
   438				}
   439	
   440				// 模拟人类阅读时间
   441				sleepRandom(readTimeRange.min, readTimeRange.max)
   442				clickSuccess = true
   443				return nil
   444			},
   445			retry.Attempts(3),
   446			retry.Delay(100*time.Millisecond),
   447			retry.MaxJitter(200*time.Millisecond),
   448			retry.OnRetry(func(n uint, err error) {
   449				logrus.Debugf("点击重试 #%d: %s, 错误: %v", n, text, err)
   450			}),
   451		)
   452	
   453		if err != nil {
   454			logrus.Debugf("点击失败 '%s': %v", text, err)
   455			return false
   456		}
   457	
   458		if clickSuccess {
   459			logrus.Debugf("点击了'%s'", text)
   460		}
   461	
   462		return clickSuccess
   463	}
   464	
   465	// ========== 滚动相关 ==========
   466	
   467	func humanScroll(page *hrod.Page, speed string, largeMode bool, pushCount int) (bool, int, int) {
   468		beforeTop := getScrollTop(page)
   469		viewportHeight := page.MustEval(`() => window.innerHeight`).Int()
   470	
   471		baseRatio := getScrollRatio(speed)
   472		if largeMode {
   473			baseRatio *= 2.0
   474		}
   475	
   476		scrolled := false
   477		actualDelta := 0
   478		currentScrollTop := beforeTop
   479	
   480		for i := 0; i < max(1, pushCount); i++ {
   481			scrollDelta := calculateScrollDelta(viewportHeight, baseRatio)
   482			if err := page.Actor().Mouse.Scroll(0, scrollDelta); err != nil {
   483				logrus.Warnf("人化滚动失败: %v", err)
   484			}
   485	
   486			sleepRandom(scrollWaitRange.min, scrollWaitRange.max)
   487	
   488			currentScrollTop = getScrollTop(page)
   489			deltaThisTime := currentScrollTop - beforeTop
   490			actualDelta += deltaThisTime
   491	
   492			if deltaThisTime > 5 {
   493				scrolled = true
   494			}
   495	
   496			beforeTop = currentScrollTop
   497	
   498			if i < pushCount-1 {
   499				sleepRandom(humanDelayRange.min, humanDelayRange.max)
   500			}
   501		}
   502	
   503		if !scrolled && pushCount > 0 {
   504			scrollHeight := page.MustEval(`() => document.body.scrollHeight`).Int()
   505			currentScrollTop := getScrollTop(page)
   506			if err := page.Actor().Mouse.Scroll(0, float64(scrollHeight-currentScrollTop)); err != nil {
   507				logrus.Warnf("滚动到底部失败: %v", err)
   508			}
   509			sleepRandom(postScrollRange.min, postScrollRange.max)
   510			currentScrollTop = getScrollTop(page)
   511			actualDelta = currentScrollTop - beforeTop + actualDelta
   512			scrolled = actualDelta > 5
   513		}
   514	
   515		if scrolled {
   516			logrus.Debugf("滚动: %d -> %d (Δ%d, large=%v, push=%d)",
   517				beforeTop-actualDelta, currentScrollTop, actualDelta, largeMode, pushCount)
   518		}
   519	
   520		return scrolled, actualDelta, currentScrollTop
   521	}
   522	
   523	func getScrollRatio(speed string) float64 {
   524		switch speed {
   525		case "slow":
   526			return 0.5
   527		case "fast":
   528			return 0.9
   529		default: // normal
   530			return 0.7
   531		}
   532	}
   533	
   534	func calculateScrollDelta(viewportHeight int, baseRatio float64) float64 {
   535		scrollDelta := float64(viewportHeight) * (baseRatio + rand.Float64()*0.2)
   536		if scrollDelta < 400 {
   537			scrollDelta = 400
   538		}
   539		return scrollDelta + float64(rand.Intn(100)-50)
   540	}
   541	
   542	func scrollToCommentsArea(page *hrod.Page) {
   543		logrus.Info("滚动到评论区...")
   544	
   545		// 先定位到评论区
   546		if el, err := page.Timeout(2 * time.Second).Element(".comments-container"); err == nil {
   547			el.MustScrollIntoView()
   548		}
   549		// 等待滚动完成
   550		time.Sleep(500 * time.Millisecond)
   551	
   552		// 触发一次小滚动，激活懒加载机制
   553		smartScroll(page, 100)
   554	}
   555	
   556	// smartScroll 使用人化鼠标滚动触发懒加载。
   557	func smartScroll(page *hrod.Page, delta float64) {
   558		if err := page.Actor().Mouse.Scroll(0, delta); err != nil {
   559			logrus.Warnf("smartScroll 失败: %v", err)
   560		}
   561	}
   562	
   563	func scrollToLastComment(page *hrod.Page) {
   564		// 获取所有主评论元素
   565		elements, err := page.Timeout(2 * time.Second).Elements(".parent-comment")
   566		if err != nil || len(elements) == 0 {
   567			return
   568		}
   569		// 滚动到最后一个评论
   570		lastComment := elements[len(elements)-1]
   720					result = false
   721					return nil
   722				}
   723	
   724				// 转换为大写并检查
   725				textUpper := strings.ToUpper(strings.TrimSpace(text))
   726				result = strings.Contains(textUpper, "THE END") || strings.Contains(textUpper, "THEEND")
   727				return nil
   728			},
   729			retry.Attempts(3),
   730			retry.Delay(100*time.Millisecond),
   731			retry.MaxJitter(200*time.Millisecond),
   732			retry.OnRetry(func(n uint, err error) {
   733				logrus.Debugf("检查结束容器重试 #%d: %v", n, err)
   734			}),
   735		)
   736	
   737		if err != nil {
   738			logrus.Warnf("检查结束容器失败: %v", err)
   739			return false // 失败时返回false
   740		}
   741	
   742		return result
   743	}
   744	
   745	// ========== 页面检查 ==========
   746	
   747	func checkPageAccessible(page *hrod.Page) error {
   748		time.Sleep(500 * time.Millisecond)
   749	
   750		// 查找错误提示容器
   751		wrapperEl, err := page.Timeout(2 * time.Second).Element(".access-wrapper, .error-wrapper, .not-found-wrapper, .blocked-wrapper")
   752		if err != nil {
   753			// 未找到错误容器，说明页面可访问
   754			return nil
   755		}
   756	
   757		// 获取文本内容
   758		text, err := wrapperEl.Text()
   759		if err != nil {
   760			// 无法获取文本，假设页面可访问
   761			return nil
   762		}
   763	
   764		// 检查关键词
   765		keywords := []string{
   766			"当前笔记暂时无法浏览",
   767			"该内容因违规已被删除",
   768			"该笔记已被删除",
   769			"内容不存在",
   770			"笔记不存在",

 succeeded in 115ms:
xiaohongshu/comment_feed.go:224:		if err := page.Actor().Mouse.Scroll(0, float64(viewportHeight)*0.8); err != nil {
xiaohongshu/feed_detail.go:482:		if err := page.Actor().Mouse.Scroll(0, scrollDelta); err != nil {
xiaohongshu/feed_detail.go:506:		if err := page.Actor().Mouse.Scroll(0, float64(scrollHeight-currentScrollTop)); err != nil {
xiaohongshu/feed_detail.go:558:	if err := page.Actor().Mouse.Scroll(0, delta); err != nil {
xiaohongshu/publish.go:477:	publishBtn := hrod.NewElement(publishBtnRod, page.Actor())
xiaohongshu/publish.go:568:		return hrod.NewElement(foundElement, page.Actor()), true
xiaohongshu/publish.go:1148:	if err := page.Actor().Keyboard.Press(input.Enter); err != nil {
pkg/humanize/rod/hrod.go:101:func (p *Page) Actor() *humanize.Actor {
pkg/humanize/rod/hrod.go:118:		actor:    humanize.NewWithContext(rp, p.cfg, p.ctx),
pkg/humanize/rod/hrod.go:134:	page.actor.SetContext(ctx)
pkg/humanize/rod/hrod.go:351:func (el *Element) Actor() *humanize.Actor {
pkg/humanize/humanize.go:25:	return NewWithContext(page, cfg, context.Background())
pkg/humanize/humanize.go:28:// NewWithContext creates a humanized actor for the given page and context.
pkg/humanize/humanize.go:29:func NewWithContext(page *rod.Page, cfg Config, ctx context.Context) *Actor {
pkg/humanize/humanize.go:36:	actor.SetContext(ctx)
pkg/humanize/humanize.go:45:// SetContext updates the context used by humanized delays.
pkg/humanize/humanize.go:46:func (a *Actor) SetContext(ctx context.Context) {

exec
/bin/bash -lc "sed -n '1,380p' P2_FIX_PLAN.md; sed -n '1,260p' P2_FIXES_PROMPT.md" in /tmp/xiaohongshu-mcp-fork
 succeeded in 0ms:
# 遗留问题修复方案

fixup 分支还有两个 P2 问题需要写修复方案。请阅读代码后，为每个问题输出具体的代码级修复方案（Markdown 格式）。

## 问题 1：xiaohongshu/ 下大量 time.Sleep 未响应 context 取消

请阅读 xiaohongshu/ 目录下的所有 .go 文件（publish.go, comment_feed.go, publish_video.go, feed_detail.go, like_favorite.go, login.go, feeds.go），分析：
- 哪些函数已经有 ctx context.Context 参数
- time.Sleep 的分布
- 可复用的封装模式（已经有一个 sleepRandom() 函数）

同时阅读 pkg/humanize/util.go（已有 sleepWithContext）和 service.go（page.Context(ctx) 调用模式）。

输出：最小侵入的替换方案，不改函数签名。

## 问题 2：hrod Page.wrapPage() 重建 actor 丢失鼠标/键盘状态

请阅读 pkg/humanize/rod/hrod.go 中：
- Page.wrapPage()（L110-123）：每次都 humanize.NewWithContext 创建新 actor
- Page.Context()（L131-136）：调用 wrapPage 后额外设置了 ctx 和 actor.SetContext
- Page.Timeout()（L139-141）：只调 wrapPage
- Browser.wrapPage()（L68-79）：做了 InitPosition()

同时阅读 pkg/humanize/humanize.go（Actor 结构体）
pkg/humanize/mouse.go（initialized 标志）
pkg/humanize/keyboard.go（lastEl 字段）

输出：复用 p.actor 而非重建的方案。

## 输出格式

请直接输出完整的 Markdown 内容，包含：
- 每个问题的根因分析
- 需要修改的文件、行号、代码片段
- 风险评估
- 优先级建议
exec
/bin/bash -lc "nl -ba service.go | sed -n '245,620p'; nl -ba pkg/humanize/keyboard.go | sed -n '1,250p'; nl -ba pkg/humanize/mouse.go | sed -n '135,300p'" in /tmp/xiaohongshu-mcp-fork
 succeeded in 0ms:
   245		return processor.ProcessImages(images)
   246	}
   247	
   248	// publishContent 执行内容发布
   249	func (s *XiaohongshuService) publishContent(ctx context.Context, content xiaohongshu.PublishImageContent) error {
   250		page, err := s.bm.Acquire(ctx)
   251		if err != nil {
   252			return err
   253		}
   254		defer s.bm.Release()
   255	
   256		action, err := xiaohongshu.NewPublishImageAction(page.Context(ctx))
   257		if err != nil {
   258			return err
   259		}
   260	
   261		// 执行发布
   262		return action.Publish(ctx, content)
   263	}
   264	
   265	// PublishVideo 发布视频（本地文件）
   266	func (s *XiaohongshuService) PublishVideo(ctx context.Context, req *PublishVideoRequest) (*PublishVideoResponse, error) {
   267		// 标题长度校验（小红书限制：最大20个字）
   268		if xhsutil.CalcTitleLength(req.Title) > 20 {
   269			return nil, fmt.Errorf("标题长度超过限制")
   270		}
   271	
   272		// 本地视频文件校验
   273		if req.Video == "" {
   274			return nil, fmt.Errorf("必须提供本地视频文件")
   275		}
   276		if _, err := os.Stat(req.Video); err != nil {
   277			return nil, fmt.Errorf("视频文件不存在或不可访问: %v", err)
   278		}
   279	
   280		// 解析定时发布时间
   281		var scheduleTime *time.Time
   282		if req.ScheduleAt != "" {
   283			t, err := time.Parse(time.RFC3339, req.ScheduleAt)
   284			if err != nil {
   285				return nil, fmt.Errorf("定时发布时间格式错误，请使用 ISO8601 格式: %v", err)
   286			}
   287	
   288			// 校验定时发布时间范围：1小时至14天
   289			now := time.Now()
   290			minTime := now.Add(1 * time.Hour)
   291			maxTime := now.Add(14 * 24 * time.Hour)
   292	
   293			if t.Before(minTime) {
   294				return nil, fmt.Errorf("定时发布时间必须至少在1小时后，当前设置: %s，最早可选: %s",
   295					t.Format("2006-01-02 15:04"), minTime.Format("2006-01-02 15:04"))
   296			}
   297			if t.After(maxTime) {
   298				return nil, fmt.Errorf("定时发布时间不能超过14天，当前设置: %s，最晚可选: %s",
   299					t.Format("2006-01-02 15:04"), maxTime.Format("2006-01-02 15:04"))
   300			}
   301	
   302			scheduleTime = &t
   303			logrus.Infof("设置定时发布时间: %s", t.Format("2006-01-02 15:04"))
   304		}
   305	
   306		// 构建发布内容
   307		content := xiaohongshu.PublishVideoContent{
   308			Title:        req.Title,
   309			Content:      req.Content,
   310			Tags:         req.Tags,
   311			VideoPath:    req.Video,
   312			ScheduleTime: scheduleTime,
   313			Visibility:   req.Visibility,
   314			Products:     req.Products,
   315		}
   316	
   317		// 执行发布
   318		if err := s.publishVideo(ctx, content); err != nil {
   319			return nil, err
   320		}
   321	
   322		resp := &PublishVideoResponse{
   323			Title:   req.Title,
   324			Content: req.Content,
   325			Video:   req.Video,
   326			Status:  "发布完成",
   327		}
   328		return resp, nil
   329	}
   330	
   331	// publishVideo 执行视频发布
   332	func (s *XiaohongshuService) publishVideo(ctx context.Context, content xiaohongshu.PublishVideoContent) error {
   333		page, err := s.bm.Acquire(ctx)
   334		if err != nil {
   335			return err
   336		}
   337		defer s.bm.Release()
   338	
   339		action, err := xiaohongshu.NewPublishVideoAction(page.Context(ctx))
   340		if err != nil {
   341			return err
   342		}
   343	
   344		return action.PublishVideo(ctx, content)
   345	}
   346	
   347	// ListFeeds 获取Feeds列表
   348	func (s *XiaohongshuService) ListFeeds(ctx context.Context) (*FeedsListResponse, error) {
   349		page, err := s.bm.Acquire(ctx)
   350		if err != nil {
   351			return nil, err
   352		}
   353		defer s.bm.Release()
   354	
   355		// 创建 Feeds 列表 action
   356		action := xiaohongshu.NewFeedsListAction(page.Context(ctx))
   357	
   358		// 获取 Feeds 列表
   359		feeds, err := action.GetFeedsList(ctx)
   360		if err != nil {
   361			logrus.Errorf("获取 Feeds 列表失败: %v", err)
   362			return nil, err
   363		}
   364	
   365		response := &FeedsListResponse{
   366			Feeds: feeds,
   367			Count: len(feeds),
   368		}
   369	
   370		return response, nil
   371	}
   372	
   373	func (s *XiaohongshuService) SearchFeeds(ctx context.Context, keyword string, filters ...xiaohongshu.FilterOption) (*FeedsListResponse, error) {
   374		page, err := s.bm.Acquire(ctx)
   375		if err != nil {
   376			return nil, err
   377		}
   378		defer s.bm.Release()
   379	
   380		action := xiaohongshu.NewSearchAction(page.Context(ctx))
   381	
   382		feeds, err := action.Search(ctx, keyword, filters...)
   383		if err != nil {
   384			return nil, err
   385		}
   386	
   387		response := &FeedsListResponse{
   388			Feeds: feeds,
   389			Count: len(feeds),
   390		}
   391	
   392		return response, nil
   393	}
   394	
   395	// GetFeedDetail 获取Feed详情
   396	func (s *XiaohongshuService) GetFeedDetail(ctx context.Context, feedID, xsecToken string, loadAllComments bool) (*FeedDetailResponse, error) {
   397		return s.GetFeedDetailWithConfig(ctx, feedID, xsecToken, loadAllComments, xiaohongshu.DefaultCommentLoadConfig())
   398	}
   399	
   400	// GetFeedDetailWithConfig 使用配置获取Feed详情
   401	func (s *XiaohongshuService) GetFeedDetailWithConfig(ctx context.Context, feedID, xsecToken string, loadAllComments bool, config xiaohongshu.CommentLoadConfig) (*FeedDetailResponse, error) {
   402		page, err := s.bm.Acquire(ctx)
   403		if err != nil {
   404			return nil, err
   405		}
   406		defer s.bm.Release()
   407	
   408		// 创建 Feed 详情 action
   409		action := xiaohongshu.NewFeedDetailAction(page.Context(ctx))
   410	
   411		// 获取 Feed 详情
   412		result, err := action.GetFeedDetailWithConfig(ctx, feedID, xsecToken, loadAllComments, config)
   413		if err != nil {
   414			return nil, err
   415		}
   416	
   417		response := &FeedDetailResponse{
   418			FeedID: feedID,
   419			Data:   result,
   420		}
   421	
   422		return response, nil
   423	}
   424	
   425	// UserProfile 获取用户信息
   426	func (s *XiaohongshuService) UserProfile(ctx context.Context, userID, xsecToken string) (*UserProfileResponse, error) {
   427		page, err := s.bm.Acquire(ctx)
   428		if err != nil {
   429			return nil, err
   430		}
   431		defer s.bm.Release()
   432	
   433		action := xiaohongshu.NewUserProfileAction(page.Context(ctx))
   434	
   435		result, err := action.UserProfile(ctx, userID, xsecToken)
   436		if err != nil {
   437			return nil, err
   438		}
   439		response := &UserProfileResponse{
   440			UserBasicInfo: result.UserBasicInfo,
   441			Interactions:  result.Interactions,
   442			Feeds:         result.Feeds,
   443		}
   444	
   445		return response, nil
   446	
   447	}
   448	
   449	// PostCommentToFeed 发表评论到Feed
   450	func (s *XiaohongshuService) PostCommentToFeed(ctx context.Context, feedID, xsecToken, content string) (*PostCommentResponse, error) {
   451		page, err := s.bm.Acquire(ctx)
   452		if err != nil {
   453			return nil, err
   454		}
   455		defer s.bm.Release()
   456	
   457		action := xiaohongshu.NewCommentFeedAction(page.Context(ctx))
   458	
   459		if err := action.PostComment(ctx, feedID, xsecToken, content); err != nil {
   460			return nil, err
   461		}
   462	
   463		return &PostCommentResponse{FeedID: feedID, Success: true, Message: "评论发表成功"}, nil
   464	}
   465	
   466	// LikeFeed 点赞笔记
   467	func (s *XiaohongshuService) LikeFeed(ctx context.Context, feedID, xsecToken string) (*ActionResult, error) {
   468		page, err := s.bm.Acquire(ctx)
   469		if err != nil {
   470			return nil, err
   471		}
   472		defer s.bm.Release()
   473	
   474		action := xiaohongshu.NewLikeAction(page.Context(ctx))
   475		if err := action.Like(ctx, feedID, xsecToken); err != nil {
   476			return nil, err
   477		}
   478		return &ActionResult{FeedID: feedID, Success: true, Message: "点赞成功或已点赞"}, nil
   479	}
   480	
   481	// UnlikeFeed 取消点赞笔记
   482	func (s *XiaohongshuService) UnlikeFeed(ctx context.Context, feedID, xsecToken string) (*ActionResult, error) {
   483		page, err := s.bm.Acquire(ctx)
   484		if err != nil {
   485			return nil, err
   486		}
   487		defer s.bm.Release()
   488	
   489		action := xiaohongshu.NewLikeAction(page.Context(ctx))
   490		if err := action.Unlike(ctx, feedID, xsecToken); err != nil {
   491			return nil, err
   492		}
   493		return &ActionResult{FeedID: feedID, Success: true, Message: "取消点赞成功或未点赞"}, nil
   494	}
   495	
   496	// FavoriteFeed 收藏笔记
   497	func (s *XiaohongshuService) FavoriteFeed(ctx context.Context, feedID, xsecToken string) (*ActionResult, error) {
   498		page, err := s.bm.Acquire(ctx)
   499		if err != nil {
   500			return nil, err
   501		}
   502		defer s.bm.Release()
   503	
   504		action := xiaohongshu.NewFavoriteAction(page.Context(ctx))
   505		if err := action.Favorite(ctx, feedID, xsecToken); err != nil {
   506			return nil, err
   507		}
   508		return &ActionResult{FeedID: feedID, Success: true, Message: "收藏成功或已收藏"}, nil
   509	}
   510	
   511	// UnfavoriteFeed 取消收藏笔记
   512	func (s *XiaohongshuService) UnfavoriteFeed(ctx context.Context, feedID, xsecToken string) (*ActionResult, error) {
   513		page, err := s.bm.Acquire(ctx)
   514		if err != nil {
   515			return nil, err
   516		}
   517		defer s.bm.Release()
   518	
   519		action := xiaohongshu.NewFavoriteAction(page.Context(ctx))
   520		if err := action.Unfavorite(ctx, feedID, xsecToken); err != nil {
   521			return nil, err
   522		}
   523		return &ActionResult{FeedID: feedID, Success: true, Message: "取消收藏成功或未收藏"}, nil
   524	}
   525	
   526	// ReplyCommentToFeed 回复指定评论
   527	func (s *XiaohongshuService) ReplyCommentToFeed(ctx context.Context, feedID, xsecToken, commentID, userID, content string) (*ReplyCommentResponse, error) {
   528		page, err := s.bm.Acquire(ctx)
   529		if err != nil {
   530			return nil, err
   531		}
   532		defer s.bm.Release()
   533	
   534		action := xiaohongshu.NewCommentFeedAction(page.Context(ctx))
   535	
   536		if err := action.ReplyToComment(ctx, feedID, xsecToken, commentID, userID, content); err != nil {
   537			return nil, err
   538		}
   539	
   540		return &ReplyCommentResponse{
   541			FeedID:          feedID,
   542			TargetCommentID: commentID,
   543			TargetUserID:    userID,
   544			Success:         true,
   545			Message:         "评论回复成功",
   546		}, nil
   547	}
   548	
   549	func newBrowser() *hrod.Browser {
   550		return browser.NewBrowser(configs.IsHeadless(), browser.WithBinPath(configs.GetBinPath()))
   551	}
   552	
   553	func saveCookies(page *hrod.Page) error {
   554		cks, err := page.Rod.Browser().GetCookies()
   555		if err != nil {
   556			return err
   557		}
   558	
   559		data, err := json.Marshal(cks)
   560		if err != nil {
   561			return err
   562		}
   563	
   564		cookieLoader := cookies.NewLoadCookie(cookies.GetCookiesFilePath())
   565		return cookieLoader.SaveCookies(data)
   566	}
   567	
   568	// withBrowserPage 执行需要浏览器页面的操作的通用函数
   569	func (s *XiaohongshuService) withBrowserPage(ctx context.Context, fn func(*hrod.Page) error) error {
   570		page, err := s.bm.Acquire(ctx)
   571		if err != nil {
   572			return err
   573		}
   574		defer s.bm.Release()
   575	
   576		return fn(page)
   577	}
   578	
   579	// GetMyProfile 获取当前登录用户的个人信息
   580	func (s *XiaohongshuService) GetMyProfile(ctx context.Context) (*UserProfileResponse, error) {
   581		var result *xiaohongshu.UserProfileResponse
   582		var err error
   583	
   584		err = s.withBrowserPage(ctx, func(page *hrod.Page) error {
   585			action := xiaohongshu.NewUserProfileAction(page.Context(ctx))
   586			result, err = action.GetMyProfileViaSidebar(ctx)
   587			return err
   588		})
   589	
   590		if err != nil {
   591			return nil, err
   592		}
   593	
   594		response := &UserProfileResponse{
   595			UserBasicInfo: result.UserBasicInfo,
   596			Interactions:  result.Interactions,
   597			Feeds:         result.Feeds,
   598		}
   599	
   600		return response, nil
   601	}
     1	package humanize
     2	
     3	import (
     4		"context"
     5		"math"
     6		"math/rand"
     7		"time"
     8	
     9		"github.com/go-rod/rod"
    10		"github.com/go-rod/rod/lib/input"
    11		"github.com/go-rod/rod/lib/proto"
    12	)
    13	
    14	// Keyboard provides human-like keyboard input.
    15	type Keyboard struct {
    16		page     *rod.Page
    17		cfg      Config
    18		ctx      context.Context
    19		mouse    *Mouse
    20		lastEl   *rod.Element
    21	}
    22	
    23	// NewKeyboard creates a new humanized keyboard wrapper.
    24	func NewKeyboard(page *rod.Page, cfg Config, mouse *Mouse) *Keyboard {
    25		return &Keyboard{page: page, cfg: cfg, ctx: context.Background(), mouse: mouse}
    26	}
    27	
    28	func (k *Keyboard) setContext(ctx context.Context) {
    29		k.ctx = ctx
    30	}
    31	
    32	// Type types text into el with realistic timing, occasional typos, and corrections.
    33	// ASCII characters are typed key-by-key; CJK and other non-keyboard characters
    34	// are inserted via simulated voice/IME composition events.
    35	func (k *Keyboard) Type(el *rod.Element, text string) error {
    36		// Ensure the element is rendered before typing, so the cursor lands on a
    37		// visible input area even when the page is long.
    38		if k.mouse == nil {
    39			if err := el.ScrollIntoView(); err != nil {
    40				return err
    41			}
    42		} else if k.lastEl == el {
    43			if err := k.mouse.ScrollIntoView(el); err != nil {
    44				return err
    45			}
    46		}
    47	
    48		// Move the cursor onto the element and click it, just like a human would
    49		// before typing. Skip the click if we just typed into the same element to
    50		// avoid repeated cursor jumps during continuous input (e.g. typing tags
    51		// char by char). This also keeps the mouse position continuous between
    52		// actions without querying DOM state that a page could detect.
    53		if k.mouse != nil && k.lastEl != el {
    54			if err := k.mouse.Click(el); err != nil {
    55				return err
    56			}
    57		}
    58		k.lastEl = el
    59	
    60		if err := el.Focus(); err != nil {
    61			return err
    62		}
    63	
    64		cfg := k.cfg.Keyboard
    65		if cfg.TypoChars == nil {
    66			cfg.TypoChars = []rune("qwertyuiopasdfghjklzxcvbnm1234567890")
    67		}
    68		if cfg.BurstLength <= 0 {
    69			cfg.BurstLength = 1
    70		}
    71	
    72		cpm := cfg.CPM * (1 + (rand.Float64()*2-1)*cfg.CPMVariance)
    73		msPerChar := 60000.0 / cpm
    74		// ASCII is typed roughly 2x faster; CJK voice/IME composition is slower.
    75		asciiMsPerChar := msPerChar / 2
    76		cjkMsPerChar := msPerChar * 3
    77	
    78		tokens := tokenizeText(text)
    79		typed := 0
    80		lastScrollCheck := 0
    81	
    82		for _, token := range tokens {
    83			if token.isASCII {
    84				for _, r := range token.text {
    85					// Occasional typo for ASCII keys.
    86					if rand.Float64() < cfg.TypoProbability {
    87						typo := randomTypo(r, cfg.TypoChars)
    88						if err := k.press(input.Key(typo)); err != nil {
    89							return err
    90						}
    91						if err := sleepWithContext(k.ctx, cfg.PauseAfterTypo+time.Duration(rand.Float64()*200)*time.Millisecond); err != nil {
    92							return err
    93						}
    94						if err := k.pressBackspace(); err != nil {
    95							return err
    96						}
    97						if err := sleepWithContext(k.ctx, randDuration(50*time.Millisecond, 150*time.Millisecond)); err != nil {
    98							return err
    99						}
   100					}
   101	
   102					if err := k.press(input.Key(r)); err != nil {
   103						return err
   104					}
   105					typed++
   106	
   107					delay := time.Duration(asciiMsPerChar * (0.6 + rand.Float64()*0.8) * float64(time.Millisecond))
   108					if delay < 10*time.Millisecond {
   109						delay = 10 * time.Millisecond
   110					}
   111					if err := sleepWithContext(k.ctx, delay); err != nil {
   112						return err
   113					}
   114	
   115					if typed%cfg.BurstLength == 0 {
   116						if err := sleepWithContext(k.ctx, randDuration(cfg.BurstPause, cfg.BurstPause+80*time.Millisecond)); err != nil {
   117							return err
   118						}
   119					}
   120	
   121					if typed-lastScrollCheck >= 30 {
   122						_ = k.scrollToCursor(el)
   123						lastScrollCheck = typed
   124					}
   125				}
   126			} else {
   127				// CJK / emoji / special chars: simulate voice/IME composition.
   128				segments := segmentCJK(token.text)
   129				for _, seg := range segments {
   130					if err := k.insertCompositionText(el, seg); err != nil {
   131						return err
   132					}
   133					segRunes := []rune(seg)
   134					// Pause between voice/IME chunks scales with segment length and
   135					// the slower CJK speed.
   136					pause := time.Duration(cjkMsPerChar * float64(len(segRunes)) * (0.8 + rand.Float64()*0.6))
   137					if pause < 150*time.Millisecond {
   138						pause = 150 * time.Millisecond
   139					}
   140					if err := sleepWithContext(k.ctx, pause); err != nil {
   141						return err
   142					}
   143					typed += len(segRunes)
   144	
   145					if typed-lastScrollCheck >= 30 {
   146						_ = k.scrollToCursor(el)
   147						lastScrollCheck = typed
   148					}
   149				}
   150			}
   151		}
   152	
   153		return nil
   154	}
   155	
   156	// Press presses a single key with human-like delay.
   157	func (k *Keyboard) Press(key input.Key) error {
   158		if err := k.press(key); err != nil {
   159			return err
   160		}
   161		return sleepWithContext(k.ctx, randDuration(50*time.Millisecond, 150*time.Millisecond))
   162	}
   163	
   164	func (k *Keyboard) press(key input.Key) error {
   165		return k.page.Keyboard.Press(key)
   166	}
   167	
   168	// pressBackspace sends a Backspace key via CDP directly.
   169	func (k *Keyboard) pressBackspace() error {
   170		return proto.InputDispatchKeyEvent{
   171			Type:                  proto.InputDispatchKeyEventTypeKeyDown,
   172			Key:                   "Backspace",
   173			Code:                  "Backspace",
   174			WindowsVirtualKeyCode: 8,
   175		}.Call(k.page)
   176	}
   177	
   178	// scrollToCursor scrolls the page so the text cursor remains visible while
   179	// typing long content. It is best-effort and ignores errors to avoid breaking
   180	// the typing flow.
   181	func (k *Keyboard) scrollToCursor(el *rod.Element) error {
   182		obj, err := el.Eval(`() => {
   183			const sel = window.getSelection();
   184			if (!sel || sel.rangeCount === 0) return null;
   185			const range = sel.getRangeAt(0);
   186			const rect = range.getBoundingClientRect();
   187			if (rect.width === 0 && rect.height === 0) return null;
   188			return {
   189				cursorTop: rect.top + window.scrollY,
   190				cursorBottom: rect.bottom + window.scrollY,
   191				cursorLeft: rect.left + window.scrollX,
   192				cursorRight: rect.right + window.scrollX,
   193			};
   194		}`)
   195		if err != nil {
   196			return err
   197		}
   198		if obj == nil {
   199			return nil
   200		}
   201		val, err := k.page.ObjectToJSON(obj)
   202		if err != nil {
   203			return err
   204		}
   205	
   206		cursorTop := val.Get("cursorTop").Num()
   207		cursorBottom := val.Get("cursorBottom").Num()
   208	
   209		vp, err := k.viewport()
   210		if err != nil {
   211			return err
   212		}
   213	
   214		const margin = 100
   215		var deltaY float64
   216		if cursorBottom > vp.scrollY+vp.height-margin {
   217			deltaY = cursorBottom - (vp.scrollY + vp.height) + margin + 50
   218		} else if cursorTop < vp.scrollY+margin {
   219			deltaY = cursorTop - vp.scrollY - margin - 50
   220		}
   221	
   222		if deltaY != 0 {
   223			return k.page.Mouse.Scroll(0, deltaY, 1)
   224		}
   225		return nil
   226	}
   227	
   228	func (k *Keyboard) viewport() (struct {
   229		scrollX, scrollY float64
   230		width, height    float64
   231	}, error) {
   232		var vp struct {
   233			scrollX, scrollY float64
   234			width, height    float64
   235		}
   236		obj, err := k.page.Eval(`() => ({
   237			scrollX: window.scrollX,
   238			scrollY: window.scrollY,
   239			innerWidth: window.innerWidth,
   240			innerHeight: window.innerHeight,
   241		})`)
   242		if err != nil {
   243			return vp, err
   244		}
   245		res, err := k.page.ObjectToJSON(obj)
   246		if err != nil {
   247			return vp, err
   248		}
   249		vp.scrollX = res.Get("scrollX").Num()
   250		vp.scrollY = res.Get("scrollY").Num()
   135		// Accelerate-then-fine-tune velocity profile: slow at the start, fast in
   136		// the middle, and slow again near the target. The profile is a sine hump
   137		// scaled so its average over [0,1] is 1.0, keeping the overall move time
   138		// comparable to the constant-speed baseline.
   139		const velocityFloor = 0.3
   140		velocityAmp := (1.0 - velocityFloor) * math.Pi / 2
   141	
   142		cumulativeDist := 0.0
   143		last := start
   144		for i, p := range path {
   145			// Inject jitter.
   146			if rand.Float64() < m.cfg.Mouse.JitterProbability {
   147				p = jitter(p, m.cfg.Mouse.JitterRadius)
   148			}
   149	
   150			// Distance-based step duration with ease-in-out acceleration.
   151			dist := math.Hypot(p.X-last.X, p.Y-last.Y)
   152			cumulativeDist += dist
   153	
   154			var stepDuration time.Duration
   155			if totalDist > 0 {
   156				t := cumulativeDist / totalDist
   157				// Use the midpoint of the step for smoother transitions.
   158				tMid := t - dist/(2*totalDist)
   159				if tMid < 0 {
   160					tMid = 0
   161				}
   162				velocity := velocityFloor + velocityAmp*math.Sin(math.Pi*tMid)
   163				effectiveSpeed := speed * velocity
   164				stepDuration = time.Duration(float64(time.Second) * dist / effectiveSpeed)
   165			} else {
   166				stepDuration = time.Duration(float64(time.Second) * dist / speed)
   167			}
   168			if stepDuration < 1*time.Millisecond {
   169				stepDuration = 1 * time.Millisecond
   170			}
   171	
   172			// Keep the event density high enough to look like a real mouse
   173			// (typical browser refresh rate is 60-120Hz). If the planned step is
   174			// too long, subdivide it into smaller micro-steps.
   175			const maxStepDuration = 16 * time.Millisecond
   176			subSteps := 1
   177			if stepDuration > maxStepDuration {
   178				subSteps = int(math.Ceil(float64(stepDuration) / float64(maxStepDuration)))
   179			}
   180	
   181			for j := 0; j < subSteps; j++ {
   182				ratio := float64(j+1) / float64(subSteps)
   183				subP := Point{
   184					X: last.X + (p.X-last.X)*ratio,
   185					Y: last.Y + (p.Y-last.Y)*ratio,
   186				}
   187	
   188				if err := m.page.Mouse.MoveTo(subP); err != nil {
   189					return err
   190				}
   191	
   192				if debugMouse {
   193					_ = m.tracePoint(subP.X, subP.Y, i == 0 && j == 0)
   194				}
   195	
   196				if err := sleepWithContext(m.ctx, stepDuration/time.Duration(subSteps)); err != nil {
   197					return err
   198				}
   199			}
   200	
   201			if scrollingAllowed && straightDist > 250 && rand.Float64() < m.cfg.Mouse.ScrollDuringMoveProbability {
   202				_ = m.scrollRandom()
   203				if err := sleepWithContext(m.ctx, randDuration(80*time.Millisecond, 180*time.Millisecond)); err != nil {
   204					return err
   205				}
   206			}
   207			if rand.Float64() < m.cfg.Mouse.PauseProbability {
   208				if err := sleepWithContext(m.ctx, randDuration(m.cfg.Mouse.PauseMin, m.cfg.Mouse.PauseMax)); err != nil {
   209					return err
   210				}
   211			}
   212	
   213			last = p
   214		}
   215		return nil
   216	}
   217	
   218	// Click scrolls the element into view, moves to its center with random offset, and clicks.
   219	func (m *Mouse) Click(el *rod.Element) error {
   220		return m.ClickWithOptions(el, proto.InputMouseButtonLeft, 1)
   221	}
   222	
   223	// ClickWithOptions scrolls the element into view, moves to its center with a
   224	// random offset, and clicks it with the requested button and click count.
   225	func (m *Mouse) ClickWithOptions(el *rod.Element, button proto.InputMouseButton, clickCount int) error {
   226		if clickCount < 1 {
   227			clickCount = 1
   228		}
   229	
   230		// Scroll the target element into view first; its on-screen position may
   231		// change after scrolling (fixed/sticky elements or layout shifts).
   232		if err := m.ScrollIntoView(el); err != nil {
   233			return err
   234		}
   235		// Re-calculate the target after scrolling, because fixed/sticky elements
   236		// move with the viewport and the old page-absolute coordinates are stale.
   237		target, err := elementTarget(el)
   238		if err != nil {
   239			return err
   240		}
   241		if err := m.moveTo(target, false); err != nil {
   242			return err
   243		}
   244	
   245		// Human pause before clicking.
   246		if err := sleepWithContext(m.ctx, randDuration(80*time.Millisecond, 350*time.Millisecond)); err != nil {
   247			return err
   248		}
   249	
   250		if err := m.page.Mouse.Down(button, clickCount); err != nil {
   251			return err
   252		}
   253		if err := sleepWithContext(m.ctx, randDuration(40*time.Millisecond, 120*time.Millisecond)); err != nil {
   254			return err
   255		}
   256		if err := m.page.Mouse.Up(button, clickCount); err != nil {
   257			return err
   258		}
   259		return nil
   260	}
   261	
   262	// ClickNoScroll performs a human-like click without scrolling the element into
   263	// view first. Use it when the target is already known to be visible (e.g.
   264	// sticky/fixed elements) to avoid the overhead or infinite loops caused by
   265	// ScrollIntoView.
   266	func (m *Mouse) ClickNoScroll(el *rod.Element) error {
   267		target, err := elementTarget(el)
   268		if err != nil {
   269			return err
   270		}
   271		if err := m.moveTo(target, false); err != nil {
   272			return err
   273		}
   274	
   275		// Human pause before clicking.
   276		if err := sleepWithContext(m.ctx, randDuration(80*time.Millisecond, 350*time.Millisecond)); err != nil {
   277			return err
   278		}
   279	
   280		if err := m.page.Mouse.Down(proto.InputMouseButtonLeft, 1); err != nil {
   281			return err
   282		}
   283		if err := sleepWithContext(m.ctx, randDuration(40*time.Millisecond, 120*time.Millisecond)); err != nil {
   284			return err
   285		}
   286		if err := m.page.Mouse.Up(proto.InputMouseButtonLeft, 1); err != nil {
   287			return err
   288		}
   289		return nil
   290	}
   291	
   292	// ClickPoint moves to a viewport-relative point and clicks there.
   293	func (m *Mouse) ClickPoint(target Point) error {
   294		if err := m.moveTo(target, false); err != nil {
   295			return err
   296		}
   297		if err := sleepWithContext(m.ctx, randDuration(80*time.Millisecond, 350*time.Millisecond)); err != nil {
   298			return err
   299		}
   300		if err := m.page.Mouse.Down(proto.InputMouseButtonLeft, 1); err != nil {

exec
/bin/bash -lc "rg -n '"'^func '"' xiaohongshu/{publish.go,comment_feed.go,publish_video.go,feed_detail.go,like_favorite.go,login.go,feeds.go}; rg -n -C 2 'time\\.Sleep|sleepRandom' xiaohongshu/{publish.go,comment_feed.go,publish_video.go,feed_detail.go,like_favorite.go,login.go,feeds.go}" in /tmp/xiaohongshu-mcp-fork
 succeeded in 0ms:
xiaohongshu/publish_video.go:26:func NewPublishVideoAction(page *hrod.Page) (*PublishAction, error) {
xiaohongshu/publish_video.go:54:func (p *PublishAction) PublishVideo(ctx context.Context, content PublishVideoContent) error {
xiaohongshu/publish_video.go:72:func uploadVideo(page *hrod.Page, videoPath string) error {
xiaohongshu/publish_video.go:102:func submitPublishVideo(page *hrod.Page, title, content string, tags []string, scheduleTime *time.Time, visibility string, products []string) error {
xiaohongshu/feeds.go:17:func NewFeedsListAction(page *hrod.Page) *FeedsListAction {
xiaohongshu/feeds.go:27:func (f *FeedsListAction) GetFeedsList(ctx context.Context) ([]Feed, error) {
xiaohongshu/feed_detail.go:56:func DefaultCommentLoadConfig() CommentLoadConfig {
xiaohongshu/feed_detail.go:69:func NewFeedDetailAction(page *hrod.Page) *FeedDetailAction {
xiaohongshu/feed_detail.go:75:func (f *FeedDetailAction) GetFeedDetail(ctx context.Context, feedID, xsecToken string, loadAllComments bool, config CommentLoadConfig) (*FeedDetailResponse, error) {
xiaohongshu/feed_detail.go:79:func (f *FeedDetailAction) GetFeedDetailWithConfig(ctx context.Context, feedID, xsecToken string, loadAllComments bool, config CommentLoadConfig) (*FeedDetailResponse, error) {
xiaohongshu/feed_detail.go:141:func (f *FeedDetailAction) loadAllCommentsWithConfig(page *hrod.Page, config CommentLoadConfig) error {
xiaohongshu/feed_detail.go:152:func (cl *commentLoader) load() error {
xiaohongshu/feed_detail.go:193:func (cl *commentLoader) calculateMaxAttempts() int {
xiaohongshu/feed_detail.go:200:func (cl *commentLoader) checkNoComments() bool {
xiaohongshu/feed_detail.go:208:func (cl *commentLoader) checkComplete() bool {
xiaohongshu/feed_detail.go:220:func (cl *commentLoader) shouldClickButtons() bool {
xiaohongshu/feed_detail.go:224:func (cl *commentLoader) clickButtonsWithRetry() {
xiaohongshu/feed_detail.go:245:func (cl *commentLoader) updateState(currentCount int) {
xiaohongshu/feed_detail.go:262:func (cl *commentLoader) shouldStopAtTarget(currentCount int) bool {
xiaohongshu/feed_detail.go:278:func (cl *commentLoader) performScroll() {
xiaohongshu/feed_detail.go:304:func (cl *commentLoader) handleStagnation() {
xiaohongshu/feed_detail.go:317:func (cl *commentLoader) performFinalSprint() {
xiaohongshu/feed_detail.go:329:func sleepRandom(minMs, maxMs int) {
xiaohongshu/feed_detail.go:338:func getScrollInterval(speed string) time.Duration {
xiaohongshu/feed_detail.go:351:func clickShowMoreButtonsSmart(page *hrod.Page, maxRepliesThreshold int) (clicked, skipped int) {
xiaohongshu/feed_detail.go:389:func isElementClickable(el *hrod.Element) bool {
xiaohongshu/feed_detail.go:399:func shouldSkipButton(text string, threshold int, regex *regexp.Regexp) bool {
xiaohongshu/feed_detail.go:414:func clickElementWithHumanBehavior(page *hrod.Page, el *hrod.Element, text string) bool {
xiaohongshu/feed_detail.go:467:func humanScroll(page *hrod.Page, speed string, largeMode bool, pushCount int) (bool, int, int) {
xiaohongshu/feed_detail.go:523:func getScrollRatio(speed string) float64 {
xiaohongshu/feed_detail.go:534:func calculateScrollDelta(viewportHeight int, baseRatio float64) float64 {
xiaohongshu/feed_detail.go:542:func scrollToCommentsArea(page *hrod.Page) {
xiaohongshu/feed_detail.go:557:func smartScroll(page *hrod.Page, delta float64) {
xiaohongshu/feed_detail.go:563:func scrollToLastComment(page *hrod.Page) {
xiaohongshu/feed_detail.go:576:func getScrollTop(page *hrod.Page) int {
xiaohongshu/feed_detail.go:605:func getCommentCount(page *hrod.Page) int {
xiaohongshu/feed_detail.go:635:func getTotalCommentCount(page *hrod.Page) int {
xiaohongshu/feed_detail.go:684:func checkNoCommentsArea(page *hrod.Page) bool {
xiaohongshu/feed_detail.go:703:func checkEndContainer(page *hrod.Page) bool {
xiaohongshu/feed_detail.go:747:func checkPageAccessible(page *hrod.Page) error {
xiaohongshu/feed_detail.go:797:func (f *FeedDetailAction) extractFeedDetail(page *hrod.Page, feedID string) (*FeedDetailResponse, error) {
xiaohongshu/feed_detail.go:856:func makeFeedDetailURL(feedID, xsecToken string) string {
xiaohongshu/login.go:15:func NewLogin(page *hrod.Page) *LoginAction {
xiaohongshu/login.go:19:func (a *LoginAction) CheckLoginStatus(ctx context.Context) (bool, error) {
xiaohongshu/login.go:37:func (a *LoginAction) Login(ctx context.Context) error {
xiaohongshu/login.go:59:func (a *LoginAction) FetchQrcodeImage(ctx context.Context) (string, bool, error) {
xiaohongshu/login.go:85:func (a *LoginAction) WaitForLogin(ctx context.Context) bool {
xiaohongshu/like_favorite.go:42:func newInteractAction(page *hrod.Page) *interactAction {
xiaohongshu/like_favorite.go:46:func (a *interactAction) preparePage(ctx context.Context, actionType interactActionType, feedID, xsecToken string) *hrod.Page {
xiaohongshu/like_favorite.go:58:func (a *interactAction) performClick(page *hrod.Page, selector string) {
xiaohongshu/like_favorite.go:68:func NewLikeAction(page *hrod.Page) *LikeAction {
xiaohongshu/like_favorite.go:73:func (a *LikeAction) Like(ctx context.Context, feedID, xsecToken string) error {
xiaohongshu/like_favorite.go:78:func (a *LikeAction) Unlike(ctx context.Context, feedID, xsecToken string) error {
xiaohongshu/like_favorite.go:82:func (a *LikeAction) perform(ctx context.Context, feedID, xsecToken string, targetLiked bool) error {
xiaohongshu/like_favorite.go:108:func (a *LikeAction) toggleLike(page *hrod.Page, feedID string, targetLiked bool, actionType interactActionType) error {
xiaohongshu/like_favorite.go:144:func NewFavoriteAction(page *hrod.Page) *FavoriteAction {
xiaohongshu/like_favorite.go:149:func (a *FavoriteAction) Favorite(ctx context.Context, feedID, xsecToken string) error {
xiaohongshu/like_favorite.go:154:func (a *FavoriteAction) Unfavorite(ctx context.Context, feedID, xsecToken string) error {
xiaohongshu/like_favorite.go:158:func (a *FavoriteAction) perform(ctx context.Context, feedID, xsecToken string, targetCollected bool) error {
xiaohongshu/like_favorite.go:184:func (a *FavoriteAction) toggleFavorite(page *hrod.Page, feedID string, targetCollected bool, actionType interactActionType) error {
xiaohongshu/like_favorite.go:216:func (a *interactAction) getInteractState(page *hrod.Page, feedID string) (liked bool, collected bool, err error) {
xiaohongshu/comment_feed.go:19:func NewCommentFeedAction(page *hrod.Page) *CommentFeedAction {
xiaohongshu/comment_feed.go:24:func (f *CommentFeedAction) PostComment(ctx context.Context, feedID, xsecToken, content string) error {
xiaohongshu/comment_feed.go:83:func (f *CommentFeedAction) ReplyToComment(ctx context.Context, feedID, xsecToken, commentID, userID, content string) error {
xiaohongshu/comment_feed.go:157:func findCommentElement(page *hrod.Page, commentID, userID string) (*hrod.Element, error) {
xiaohongshu/publish.go:40:func NewPublishImageAction(page *hrod.Page) (*PublishAction, error) {
xiaohongshu/publish.go:73:func (p *PublishAction) Publish(ctx context.Context, content PublishImageContent) error {
xiaohongshu/publish.go:99:func removePopCover(page *hrod.Page) {
xiaohongshu/publish.go:114:func clickEmptyPosition(page *hrod.Page) {
xiaohongshu/publish.go:122:func mustClickPublishTab(page *hrod.Page, tabname string) error {
xiaohongshu/publish.go:158:func getTabElement(page *hrod.Page, tabname string) (*hrod.Element, bool, error) {
xiaohongshu/publish.go:190:func isElementBlocked(elem *hrod.Element) (bool, error) {
xiaohongshu/publish.go:208:func uploadImages(page *hrod.Page, imagesPaths []string) error {
xiaohongshu/publish.go:248:func waitForUploadComplete(page *hrod.Page, expectedCount int) error {
xiaohongshu/publish.go:278:func submitPublish(page *hrod.Page, title, content string, tags []string, scheduleTime *time.Time, isOriginal bool, visibility string, products []string) error {
xiaohongshu/publish.go:358:func clickPublishButton(page *hrod.Page) error {
xiaohongshu/publish.go:375:func waitForPublishButtonClickable(page *hrod.Page, maxWait time.Duration) (*publishButton, error) {
xiaohongshu/publish.go:404:func findPublishButton(page *hrod.Page) (*publishButton, string, error) {
xiaohongshu/publish.go:468:func clickPublishWidget(page *hrod.Page, widget *hrod.Element) error {
xiaohongshu/publish.go:487:func waitAndClickTitleInput(titleElem *hrod.Element) error {
xiaohongshu/publish.go:498:func checkTitleMaxLength(page *hrod.Page) error {
xiaohongshu/publish.go:518:func checkContentMaxLength(page *hrod.Page) error {
xiaohongshu/publish.go:538:func makeMaxLengthError(elemText string) error {
xiaohongshu/publish.go:550:func getContentElement(page *hrod.Page) (*hrod.Element, bool) {
xiaohongshu/publish.go:575:func inputTags(contentElem *hrod.Element, tags []string) error {
xiaohongshu/publish.go:612:func inputTag(contentElem *hrod.Element, tag string) error {
xiaohongshu/publish.go:650:func findTextboxByPlaceholder(page *rod.Page) (*rod.Element, error) {
xiaohongshu/publish.go:671:func findPlaceholderElement(elements rod.Elements, searchText string) *rod.Element {
xiaohongshu/publish.go:685:func findTextboxParent(elem *rod.Element) *rod.Element {
xiaohongshu/publish.go:709:func isElementVisible(elem *hrod.Element) bool {
xiaohongshu/publish.go:762:func hasExactClass(classStr, className string) bool {
xiaohongshu/publish.go:770:func setVisibility(page *hrod.Page, visibility string) error {
xiaohongshu/publish.go:815:func setSchedulePublish(page *hrod.Page, t time.Time) error {
xiaohongshu/publish.go:832:func clickScheduleSwitch(page *hrod.Page) error {
xiaohongshu/publish.go:846:func setDateTime(page *hrod.Page, t time.Time) error {
xiaohongshu/publish.go:866:func setOriginal(page *hrod.Page) error {
xiaohongshu/publish.go:929:func confirmOriginalDeclaration(page *hrod.Page) error {
xiaohongshu/publish.go:995:func bindProducts(page *hrod.Page, products []string) error {
xiaohongshu/publish.go:1049:func clickAddProductButton(page *hrod.Page) error {
xiaohongshu/publish.go:1107:func waitForProductModal(page *hrod.Page) (*hrod.Element, error) {
xiaohongshu/publish.go:1126:func searchAndSelectProduct(page *hrod.Page, modal *hrod.Element, keyword string) error {
xiaohongshu/publish.go:1208:func clickModalSaveButton(page *hrod.Page, modal *hrod.Element) error {
xiaohongshu/publish.go:1236:func waitForModalClose(page *hrod.Page) error {
xiaohongshu/publish_video.go-35-		logrus.Warnf("等待页面加载出现问题: %v，继续尝试", err)
xiaohongshu/publish_video.go-36-	}
xiaohongshu/publish_video.go:37:	time.Sleep(2 * time.Second)
xiaohongshu/publish_video.go-38-
xiaohongshu/publish_video.go-39-	if err := pp.WaitDOMStable(time.Second, 0.1); err != nil {
xiaohongshu/publish_video.go-40-		logrus.Warnf("等待 DOM 稳定出现问题: %v，继续尝试", err)
xiaohongshu/publish_video.go-41-	}
xiaohongshu/publish_video.go:42:	time.Sleep(1 * time.Second)
xiaohongshu/publish_video.go-43-
xiaohongshu/publish_video.go-44-	if err := mustClickPublishTab(pp, "上传视频"); err != nil {
--
xiaohongshu/publish_video.go-46-	}
xiaohongshu/publish_video.go-47-
xiaohongshu/publish_video.go:48:	time.Sleep(1 * time.Second)
xiaohongshu/publish_video.go-49-
xiaohongshu/publish_video.go-50-	return &PublishAction{page: pp}, nil
--
xiaohongshu/publish_video.go-109-		return errors.Wrap(err, "输入标题失败")
xiaohongshu/publish_video.go-110-	}
xiaohongshu/publish_video.go:111:	time.Sleep(1 * time.Second)
xiaohongshu/publish_video.go-112-
xiaohongshu/publish_video.go-113-	// 正文 + 标签
--
xiaohongshu/publish_video.go-126-	}
xiaohongshu/publish_video.go-127-
xiaohongshu/publish_video.go:128:	time.Sleep(1 * time.Second)
xiaohongshu/publish_video.go-129-
xiaohongshu/publish_video.go-130-	// 处理定时发布
--
xiaohongshu/publish_video.go-150-	}
xiaohongshu/publish_video.go-151-
xiaohongshu/publish_video.go:152:	time.Sleep(3 * time.Second)
xiaohongshu/publish_video.go-153-	return nil
xiaohongshu/publish_video.go-154-}
--
xiaohongshu/feeds.go-28-	page := f.page.Context(ctx)
xiaohongshu/feeds.go-29-
xiaohongshu/feeds.go:30:	time.Sleep(1 * time.Second)
xiaohongshu/feeds.go-31-
xiaohongshu/feeds.go-32-	result := page.MustEval(`() => {
--
xiaohongshu/login.go-21-	pp.MustNavigate("https://www.xiaohongshu.com/explore").MustWaitLoad()
xiaohongshu/login.go-22-
xiaohongshu/login.go:23:	time.Sleep(1 * time.Second)
xiaohongshu/login.go-24-
xiaohongshu/login.go-25-	exists, _, err := pp.Has(`.main-container .user .link-wrapper .channel`)
--
xiaohongshu/login.go-42-
xiaohongshu/login.go-43-	// 等待一小段时间让页面完全加载
xiaohongshu/login.go:44:	time.Sleep(2 * time.Second)
xiaohongshu/login.go-45-
xiaohongshu/login.go-46-	// 检查是否已经登录
--
xiaohongshu/login.go-64-
xiaohongshu/login.go-65-	// 等待一小段时间让页面完全加载
xiaohongshu/login.go:66:	time.Sleep(2 * time.Second)
xiaohongshu/login.go-67-
xiaohongshu/login.go-68-	// 检查是否已经登录
--
xiaohongshu/comment_feed.go-32-	page.MustNavigate(url)
xiaohongshu/comment_feed.go-33-	page.MustWaitDOMStable()
xiaohongshu/comment_feed.go:34:	time.Sleep(1 * time.Second)
xiaohongshu/comment_feed.go-35-
xiaohongshu/comment_feed.go-36-	// 检测页面是否可访问
--
xiaohongshu/comment_feed.go-61-	}
xiaohongshu/comment_feed.go-62-
xiaohongshu/comment_feed.go:63:	time.Sleep(1 * time.Second)
xiaohongshu/comment_feed.go-64-
xiaohongshu/comment_feed.go-65-	submitButton, err := page.Element("div.bottom button.submit")
--
xiaohongshu/comment_feed.go-74-	}
xiaohongshu/comment_feed.go-75-
xiaohongshu/comment_feed.go:76:	time.Sleep(1 * time.Second)
xiaohongshu/comment_feed.go-77-
xiaohongshu/comment_feed.go-78-	logrus.Infof("Comment posted successfully to feed: %s", feedID)
--
xiaohongshu/comment_feed.go-91-	page.MustNavigate(url)
xiaohongshu/comment_feed.go-92-	page.MustWaitDOMStable()
xiaohongshu/comment_feed.go:93:	time.Sleep(1 * time.Second)
xiaohongshu/comment_feed.go-94-
xiaohongshu/comment_feed.go-95-	// 检测页面是否可访问
--
xiaohongshu/comment_feed.go-99-
xiaohongshu/comment_feed.go-100-	// 等待评论容器加载
xiaohongshu/comment_feed.go:101:	time.Sleep(2 * time.Second)
xiaohongshu/comment_feed.go-102-
xiaohongshu/comment_feed.go-103-	// 使用 Go 实现的查找逻辑
--
xiaohongshu/comment_feed.go-110-	logrus.Info("滚动到评论位置...")
xiaohongshu/comment_feed.go-111-	commentEl.MustScrollIntoView()
xiaohongshu/comment_feed.go:112:	time.Sleep(1 * time.Second)
xiaohongshu/comment_feed.go-113-
xiaohongshu/comment_feed.go-114-	logrus.Info("准备点击回复按钮")
--
xiaohongshu/comment_feed.go-124-	}
xiaohongshu/comment_feed.go-125-
xiaohongshu/comment_feed.go:126:	time.Sleep(1 * time.Second)
xiaohongshu/comment_feed.go-127-
xiaohongshu/comment_feed.go-128-	// 查找回复输入框
--
xiaohongshu/comment_feed.go-137-	}
xiaohongshu/comment_feed.go-138-
xiaohongshu/comment_feed.go:139:	time.Sleep(500 * time.Millisecond)
xiaohongshu/comment_feed.go-140-
xiaohongshu/comment_feed.go-141-	// 查找并点击提交按钮
--
xiaohongshu/comment_feed.go-149-	}
xiaohongshu/comment_feed.go-150-
xiaohongshu/comment_feed.go:151:	time.Sleep(2 * time.Second)
xiaohongshu/comment_feed.go-152-	logrus.Infof("回复评论成功")
xiaohongshu/comment_feed.go-153-	return nil
--
xiaohongshu/comment_feed.go-163-	// 先滚动到评论区
xiaohongshu/comment_feed.go-164-	scrollToCommentsArea(page)
xiaohongshu/comment_feed.go:165:	time.Sleep(1 * time.Second)
xiaohongshu/comment_feed.go-166-
xiaohongshu/comment_feed.go-167-	var lastCommentCount = 0
--
xiaohongshu/comment_feed.go-216-				logrus.Warnf("未找到评论元素: %v", err)
xiaohongshu/comment_feed.go-217-			}
xiaohongshu/comment_feed.go:218:			time.Sleep(300 * time.Millisecond)
xiaohongshu/comment_feed.go-219-		}
xiaohongshu/comment_feed.go-220-
--
xiaohongshu/comment_feed.go-225-			logrus.Warnf("滚动失败: %v", err)
xiaohongshu/comment_feed.go-226-		}
xiaohongshu/comment_feed.go:227:		time.Sleep(500 * time.Millisecond)
xiaohongshu/comment_feed.go-228-
xiaohongshu/comment_feed.go-229-		// === 6. 滚动后立即查找（边滚动边查找）===
--
xiaohongshu/comment_feed.go-267-
xiaohongshu/comment_feed.go-268-		// === 7. 等待内容加载 ===
xiaohongshu/comment_feed.go:269:		time.Sleep(scrollInterval)
xiaohongshu/comment_feed.go-270-	}
xiaohongshu/comment_feed.go-271-
--
xiaohongshu/like_favorite.go-51-	page.MustNavigate(url)
xiaohongshu/like_favorite.go-52-	page.MustWaitDOMStable()
xiaohongshu/like_favorite.go:53:	time.Sleep(1 * time.Second)
xiaohongshu/like_favorite.go-54-
xiaohongshu/like_favorite.go-55-	return page
--
xiaohongshu/like_favorite.go-108-func (a *LikeAction) toggleLike(page *hrod.Page, feedID string, targetLiked bool, actionType interactActionType) error {
xiaohongshu/like_favorite.go-109-	a.performClick(page, SelectorLikeButton)
xiaohongshu/like_favorite.go:110:	time.Sleep(3 * time.Second)
xiaohongshu/like_favorite.go-111-
xiaohongshu/like_favorite.go-112-	liked, _, err := a.getInteractState(page, feedID)
--
xiaohongshu/like_favorite.go-122-	logrus.Warnf("feed %s %s可能未成功，状态未变化，尝试再次点击", feedID, actionType)
xiaohongshu/like_favorite.go-123-	a.performClick(page, SelectorLikeButton)
xiaohongshu/like_favorite.go:124:	time.Sleep(2 * time.Second)
xiaohongshu/like_favorite.go-125-
xiaohongshu/like_favorite.go-126-	liked, _, err = a.getInteractState(page, feedID)
--
xiaohongshu/like_favorite.go-184-func (a *FavoriteAction) toggleFavorite(page *hrod.Page, feedID string, targetCollected bool, actionType interactActionType) error {
xiaohongshu/like_favorite.go-185-	a.performClick(page, SelectorCollectButton)
xiaohongshu/like_favorite.go:186:	time.Sleep(3 * time.Second)
xiaohongshu/like_favorite.go-187-
xiaohongshu/like_favorite.go-188-	_, collected, err := a.getInteractState(page, feedID)
--
xiaohongshu/like_favorite.go-198-	logrus.Warnf("feed %s %s可能未成功，状态未变化，尝试再次点击", feedID, actionType)
xiaohongshu/like_favorite.go-199-	a.performClick(page, SelectorCollectButton)
xiaohongshu/like_favorite.go:200:	time.Sleep(2 * time.Second)
xiaohongshu/like_favorite.go-201-
xiaohongshu/like_favorite.go-202-	_, collected, err = a.getInteractState(page, feedID)
--
xiaohongshu/publish.go-51-		logrus.Warnf("等待页面加载出现问题: %v，继续尝试", err)
xiaohongshu/publish.go-52-	}
xiaohongshu/publish.go:53:	time.Sleep(2 * time.Second)
xiaohongshu/publish.go-54-
xiaohongshu/publish.go-55-	// 等待页面稳定
--
xiaohongshu/publish.go-57-		logrus.Warnf("等待 DOM 稳定出现问题: %v，继续尝试", err)
xiaohongshu/publish.go-58-	}
xiaohongshu/publish.go:59:	time.Sleep(1 * time.Second)
xiaohongshu/publish.go-60-
xiaohongshu/publish.go-61-	if err := mustClickPublishTab(pp, "上传图文"); err != nil {
--
xiaohongshu/publish.go-64-	}
xiaohongshu/publish.go-65-
xiaohongshu/publish.go:66:	time.Sleep(1 * time.Second)
xiaohongshu/publish.go-67-
xiaohongshu/publish.go-68-	return &PublishAction{
--
xiaohongshu/publish.go-128-		if err != nil {
xiaohongshu/publish.go-129-			logrus.Warnf("获取发布 TAB 元素失败: %v", err)
xiaohongshu/publish.go:130:			time.Sleep(200 * time.Millisecond)
xiaohongshu/publish.go-131-			continue
xiaohongshu/publish.go-132-		}
xiaohongshu/publish.go-133-
xiaohongshu/publish.go-134-		if tab == nil {
xiaohongshu/publish.go:135:			time.Sleep(200 * time.Millisecond)
xiaohongshu/publish.go-136-			continue
xiaohongshu/publish.go-137-		}
--
xiaohongshu/publish.go-140-			logrus.Info("发布 TAB 被遮挡，尝试移除遮挡")
xiaohongshu/publish.go-141-			removePopCover(page)
xiaohongshu/publish.go:142:			time.Sleep(200 * time.Millisecond)
xiaohongshu/publish.go-143-			continue
xiaohongshu/publish.go-144-		}
--
xiaohongshu/publish.go-146-		if err := tab.Click(proto.InputMouseButtonLeft, 1); err != nil {
xiaohongshu/publish.go-147-			logrus.Warnf("点击发布 TAB 失败: %v", err)
xiaohongshu/publish.go:148:			time.Sleep(200 * time.Millisecond)
xiaohongshu/publish.go-149-			continue
xiaohongshu/publish.go-150-		}
--
xiaohongshu/publish.go-239-			return errors.Wrapf(err, "第%d张图片上传超时", i+1)
xiaohongshu/publish.go-240-		}
xiaohongshu/publish.go:241:		time.Sleep(1 * time.Second)
xiaohongshu/publish.go-242-	}
xiaohongshu/publish.go-243-
--
xiaohongshu/publish.go-255-		uploadedImages, err := page.Elements(".img-preview-area .pr")
xiaohongshu/publish.go-256-		if err != nil {
xiaohongshu/publish.go:257:			time.Sleep(checkInterval)
xiaohongshu/publish.go-258-			continue
xiaohongshu/publish.go-259-		}
--
xiaohongshu/publish.go-270-		}
xiaohongshu/publish.go-271-
xiaohongshu/publish.go:272:		time.Sleep(checkInterval)
xiaohongshu/publish.go-273-	}
xiaohongshu/publish.go-274-
--
xiaohongshu/publish.go-286-
xiaohongshu/publish.go-287-	// 检查标题长度
xiaohongshu/publish.go:288:	time.Sleep(500 * time.Millisecond)
xiaohongshu/publish.go-289-	if err := checkTitleMaxLength(page); err != nil {
xiaohongshu/publish.go-290-		return err
--
xiaohongshu/publish.go-292-	slog.Info("检查标题长度：通过")
xiaohongshu/publish.go-293-
xiaohongshu/publish.go:294:	time.Sleep(1 * time.Second)
xiaohongshu/publish.go-295-
xiaohongshu/publish.go-296-	contentElem, ok := getContentElement(page)
--
xiaohongshu/publish.go-308-	}
xiaohongshu/publish.go-309-
xiaohongshu/publish.go:310:	time.Sleep(1 * time.Second)
xiaohongshu/publish.go-311-
xiaohongshu/publish.go-312-	// 检查正文长度
--
xiaohongshu/publish.go-347-	}
xiaohongshu/publish.go-348-
xiaohongshu/publish.go:349:	time.Sleep(3 * time.Second)
xiaohongshu/publish.go-350-	return nil
xiaohongshu/publish.go-351-}
--
xiaohongshu/publish.go-384-		if err != nil {
xiaohongshu/publish.go-385-			slog.Warn("查找发布按钮失败，继续等待", "error", err)
xiaohongshu/publish.go:386:			time.Sleep(interval)
xiaohongshu/publish.go-387-			continue
xiaohongshu/publish.go-388-		}
--
xiaohongshu/publish.go-393-			lastDisabledReason = disabledReason
xiaohongshu/publish.go-394-		}
xiaohongshu/publish.go:395:		time.Sleep(interval)
xiaohongshu/publish.go-396-	}
xiaohongshu/publish.go-397-
--
xiaohongshu/publish.go-487-func waitAndClickTitleInput(titleElem *hrod.Element) error {
xiaohongshu/publish.go-488-	slog.Info("正文填写完成，准备等待后回点标题输入框")
xiaohongshu/publish.go:489:	time.Sleep(1 * time.Second)
xiaohongshu/publish.go-490-	if err := titleElem.Click(proto.InputMouseButtonLeft, 1); err != nil {
xiaohongshu/publish.go-491-		return errors.Wrap(err, "回点标题输入框失败")
--
xiaohongshu/publish.go-578-	}
xiaohongshu/publish.go-579-
xiaohongshu/publish.go:580:	time.Sleep(1 * time.Second)
xiaohongshu/publish.go-581-
xiaohongshu/publish.go-582-	for i := 0; i < 20; i++ {
--
xiaohongshu/publish.go-588-			return errors.Wrap(err, "按下方向键失败")
xiaohongshu/publish.go-589-		}
xiaohongshu/publish.go:590:		time.Sleep(10 * time.Millisecond)
xiaohongshu/publish.go-591-	}
xiaohongshu/publish.go-592-
--
xiaohongshu/publish.go-599-	}
xiaohongshu/publish.go-600-
xiaohongshu/publish.go:601:	time.Sleep(1 * time.Second)
xiaohongshu/publish.go-602-
xiaohongshu/publish.go-603-	for _, tag := range tags {
--
xiaohongshu/publish.go-614-		return errors.Wrap(err, "输入#失败")
xiaohongshu/publish.go-615-	}
xiaohongshu/publish.go:616:	time.Sleep(200 * time.Millisecond)
xiaohongshu/publish.go-617-
xiaohongshu/publish.go-618-	for _, char := range tag {
--
xiaohongshu/publish.go-620-			return errors.Wrapf(err, "输入字符[%c]失败", char)
xiaohongshu/publish.go-621-		}
xiaohongshu/publish.go:622:		time.Sleep(50 * time.Millisecond)
xiaohongshu/publish.go-623-	}
xiaohongshu/publish.go-624-
xiaohongshu/publish.go:625:	time.Sleep(1 * time.Second)
xiaohongshu/publish.go-626-
xiaohongshu/publish.go-627-	page := contentElem.Page()
--
xiaohongshu/publish.go-642-	}
xiaohongshu/publish.go-643-	slog.Info("成功点击标签联想选项", "tag", tag)
xiaohongshu/publish.go:644:	time.Sleep(200 * time.Millisecond)
xiaohongshu/publish.go-645-
xiaohongshu/publish.go:646:	time.Sleep(500 * time.Millisecond) // 等待标签处理完成
xiaohongshu/publish.go-647-	return nil
xiaohongshu/publish.go-648-}
--
xiaohongshu/publish.go-788-		return errors.Wrap(err, "点击可见范围下拉框失败")
xiaohongshu/publish.go-789-	}
xiaohongshu/publish.go:790:	time.Sleep(500 * time.Millisecond)
xiaohongshu/publish.go-791-
xiaohongshu/publish.go-792-	// 在弹窗中查找并点击目标选项
--
xiaohongshu/publish.go-805-			}
xiaohongshu/publish.go-806-			slog.Info("已设置可见范围", "visibility", visibility)
xiaohongshu/publish.go:807:			time.Sleep(200 * time.Millisecond)
xiaohongshu/publish.go-808-			return nil
xiaohongshu/publish.go-809-		}
--
xiaohongshu/publish.go-818-		return err
xiaohongshu/publish.go-819-	}
xiaohongshu/publish.go:820:	time.Sleep(800 * time.Millisecond)
xiaohongshu/publish.go-821-
xiaohongshu/publish.go-822-	// 2. 设置日期时间
--
xiaohongshu/publish.go-824-		return err
xiaohongshu/publish.go-825-	}
xiaohongshu/publish.go:826:	time.Sleep(500 * time.Millisecond)
xiaohongshu/publish.go-827-
xiaohongshu/publish.go-828-	return nil
--
xiaohongshu/publish.go-912-		}
xiaohongshu/publish.go-913-
xiaohongshu/publish.go:914:		time.Sleep(500 * time.Millisecond)
xiaohongshu/publish.go-915-
xiaohongshu/publish.go-916-		// 处理原创声明确认弹窗
--
xiaohongshu/publish.go-929-func confirmOriginalDeclaration(page *hrod.Page) error {
xiaohongshu/publish.go-930-	// 等待确认弹窗出现
xiaohongshu/publish.go:931:	time.Sleep(800 * time.Millisecond)
xiaohongshu/publish.go-932-
xiaohongshu/publish.go-933-	// 查找包含"原创声明须知"的 footer 并勾选 checkbox
--
xiaohongshu/publish.go-947-	}
xiaohongshu/publish.go-948-
xiaohongshu/publish.go:949:	time.Sleep(500 * time.Millisecond)
xiaohongshu/publish.go-950-
xiaohongshu/publish.go-951-	// 查找包含"声明原创"的 footer
--
xiaohongshu/publish.go-976-			}
xiaohongshu/publish.go-977-		}
xiaohongshu/publish.go:978:		time.Sleep(300 * time.Millisecond)
xiaohongshu/publish.go-979-		if isDisabled(btn) {
xiaohongshu/publish.go-980-			return errors.New("声明原创按钮仍处于禁用状态")
--
xiaohongshu/publish.go-987-
xiaohongshu/publish.go-988-	slog.Info("已成功点击声明原创按钮")
xiaohongshu/publish.go:989:	time.Sleep(300 * time.Millisecond)
xiaohongshu/publish.go-990-
xiaohongshu/publish.go-991-	return nil
--
xiaohongshu/publish.go-1004-		return errors.Wrap(err, "点击添加商品按钮失败")
xiaohongshu/publish.go-1005-	}
xiaohongshu/publish.go:1006:	time.Sleep(1 * time.Second)
xiaohongshu/publish.go-1007-
xiaohongshu/publish.go-1008-	// 等待商品选择弹窗出现
--
xiaohongshu/publish.go-1020-			failedProducts = append(failedProducts, keyword)
xiaohongshu/publish.go-1021-		}
xiaohongshu/publish.go:1022:		time.Sleep(500 * time.Millisecond)
xiaohongshu/publish.go-1023-	}
xiaohongshu/publish.go-1024-
--
xiaohongshu/publish.go-1042-
xiaohongshu/publish.go-1043-	slog.Info("商品绑定完成", "total", len(products))
xiaohongshu/publish.go:1044:	time.Sleep(1000 * time.Millisecond)
xiaohongshu/publish.go-1045-	return nil
xiaohongshu/publish.go-1046-}
--
xiaohongshu/publish.go-1084-					}
xiaohongshu/publish.go-1085-					slog.Info("已点击添加商品按钮")
xiaohongshu/publish.go:1086:					time.Sleep(300 * time.Millisecond) // 确保弹窗动画开始
xiaohongshu/publish.go-1087-					return nil
xiaohongshu/publish.go-1088-				}
--
xiaohongshu/publish.go-1094-					}
xiaohongshu/publish.go-1095-					slog.Info("已点击添加商品按钮")
xiaohongshu/publish.go:1096:					time.Sleep(300 * time.Millisecond) // 确保弹窗动画开始
xiaohongshu/publish.go-1097-					return nil
xiaohongshu/publish.go-1098-				}
--
xiaohongshu/publish.go-1117-			}
xiaohongshu/publish.go-1118-		}
xiaohongshu/publish.go:1119:		time.Sleep(100 * time.Millisecond) // 缩短轮询间隔，更快响应
xiaohongshu/publish.go-1120-	}
xiaohongshu/publish.go-1121-
--
xiaohongshu/publish.go-1137-		slog.Warn("选择搜索框文本失败", "error", err)
xiaohongshu/publish.go-1138-	}
xiaohongshu/publish.go:1139:	time.Sleep(100 * time.Millisecond)
xiaohongshu/publish.go-1140-
xiaohongshu/publish.go-1141-	// 使用 rod Input 输入关键词
--
xiaohongshu/publish.go-1143-		return errors.Wrap(err, "输入搜索关键词失败")
xiaohongshu/publish.go-1144-	}
xiaohongshu/publish.go:1145:	time.Sleep(300 * time.Millisecond)
xiaohongshu/publish.go-1146-
xiaohongshu/publish.go-1147-	// 3. 触发搜索（模拟键盘 Enter）
--
xiaohongshu/publish.go-1151-
xiaohongshu/publish.go-1152-	// 4. 等待搜索结果加载
xiaohongshu/publish.go:1153:	time.Sleep(1 * time.Second)
xiaohongshu/publish.go-1154-
xiaohongshu/publish.go-1155-	// 等待 loading 消失（使用与工作代码相同的选择器）
--
xiaohongshu/publish.go-1164-			break
xiaohongshu/publish.go-1165-		}
xiaohongshu/publish.go:1166:		time.Sleep(100 * time.Millisecond)
xiaohongshu/publish.go-1167-	}
xiaohongshu/publish.go-1168-
--
xiaohongshu/publish.go-1173-			break
xiaohongshu/publish.go-1174-		}
xiaohongshu/publish.go:1175:		time.Sleep(100 * time.Millisecond)
xiaohongshu/publish.go-1176-	}
xiaohongshu/publish.go:1177:	time.Sleep(500 * time.Millisecond) // 额外等待确保渲染完成
xiaohongshu/publish.go-1178-
xiaohongshu/publish.go-1179-	// 5. 点击第一个商品的 checkbox（使用与工作代码相同的选择器）
--
xiaohongshu/publish.go-1199-	// 6. 随机延迟模拟人为操作（800-1500ms）
xiaohongshu/publish.go-1200-	randomDelay := 800 + rand.Intn(700)
xiaohongshu/publish.go:1201:	time.Sleep(time.Duration(randomDelay) * time.Millisecond)
xiaohongshu/publish.go-1202-
xiaohongshu/publish.go-1203-	slog.Info("已选择商品", "keyword", keyword)
--
xiaohongshu/publish.go-1245-			return nil
xiaohongshu/publish.go-1246-		}
xiaohongshu/publish.go:1247:		time.Sleep(200 * time.Millisecond)
xiaohongshu/publish.go-1248-	}
xiaohongshu/publish.go-1249-
--
xiaohongshu/feed_detail.go-103-		return nil, err
xiaohongshu/feed_detail.go-104-	}
xiaohongshu/feed_detail.go:105:	sleepRandom(1000, 1000)
xiaohongshu/feed_detail.go-106-
xiaohongshu/feed_detail.go-107-	if err := checkPageAccessible(page); err != nil {
--
xiaohongshu/feed_detail.go-156-	logrus.Info("开始加载评论...")
xiaohongshu/feed_detail.go-157-	scrollToCommentsArea(cl.page)
xiaohongshu/feed_detail.go:158:	sleepRandom(humanDelayRange.min, humanDelayRange.max)
xiaohongshu/feed_detail.go-159-
xiaohongshu/feed_detail.go-160-	// 检查是否没有评论
--
xiaohongshu/feed_detail.go-184-		cl.handleStagnation()
xiaohongshu/feed_detail.go-185-
xiaohongshu/feed_detail.go:186:		time.Sleep(scrollInterval)
xiaohongshu/feed_detail.go-187-	}
xiaohongshu/feed_detail.go-188-
--
xiaohongshu/feed_detail.go-210-		currentCount := getCommentCount(cl.page)
xiaohongshu/feed_detail.go-211-		logrus.Infof("✓ 检测到 'THE END' 元素，已滑动到底部")
xiaohongshu/feed_detail.go:212:		sleepRandom(humanDelayRange.min, humanDelayRange.max)
xiaohongshu/feed_detail.go-213-		logrus.Infof("✓ 加载完成: %d 条评论, 尝试次数: %d, 点击: %d, 跳过: %d",
xiaohongshu/feed_detail.go-214-			currentCount, cl.stats.attempts+1, cl.stats.totalClicked, cl.stats.totalSkipped)
--
xiaohongshu/feed_detail.go-230-			clicked, skipped, cl.stats.totalClicked, cl.stats.totalSkipped)
xiaohongshu/feed_detail.go-231-
xiaohongshu/feed_detail.go:232:		sleepRandom(readTimeRange.min, readTimeRange.max)
xiaohongshu/feed_detail.go-233-
xiaohongshu/feed_detail.go-234-		// 重试一轮
--
xiaohongshu/feed_detail.go-238-			cl.stats.totalSkipped += skipped2
xiaohongshu/feed_detail.go-239-			logrus.Infof("第 2 轮: 点击 %d, 跳过 %d", clicked2, skipped2)
xiaohongshu/feed_detail.go:240:			sleepRandom(shortReadRange.min, shortReadRange.max)
xiaohongshu/feed_detail.go-241-		}
xiaohongshu/feed_detail.go-242-	}
--
xiaohongshu/feed_detail.go-280-	if currentCount > 0 {
xiaohongshu/feed_detail.go-281-		scrollToLastComment(cl.page)
xiaohongshu/feed_detail.go:282:		sleepRandom(postScrollRange.min, postScrollRange.max)
xiaohongshu/feed_detail.go-283-	}
xiaohongshu/feed_detail.go-284-
--
xiaohongshu/feed_detail.go-327-// ========== 工具函数 ==========
xiaohongshu/feed_detail.go-328-
xiaohongshu/feed_detail.go:329:func sleepRandom(minMs, maxMs int) {
xiaohongshu/feed_detail.go-330-	if maxMs <= minMs {
xiaohongshu/feed_detail.go:331:		time.Sleep(time.Duration(minMs) * time.Millisecond)
xiaohongshu/feed_detail.go-332-		return
xiaohongshu/feed_detail.go-333-	}
xiaohongshu/feed_detail.go-334-	delay := time.Duration(minMs+rand.Intn(maxMs-minMs)) * time.Millisecond
xiaohongshu/feed_detail.go:335:	time.Sleep(delay)
xiaohongshu/feed_detail.go-336-}
xiaohongshu/feed_detail.go-337-
--
xiaohongshu/feed_detail.go-421-			el.MustScrollIntoView()
xiaohongshu/feed_detail.go-422-
xiaohongshu/feed_detail.go:423:			sleepRandom(reactionTimeRange.min, reactionTimeRange.max)
xiaohongshu/feed_detail.go-424-
xiaohongshu/feed_detail.go-425-			// 鼠标悬停
--
xiaohongshu/feed_detail.go-430-					return err
xiaohongshu/feed_detail.go-431-				}
xiaohongshu/feed_detail.go:432:				sleepRandom(hoverTimeRange.min, hoverTimeRange.max)
xiaohongshu/feed_detail.go-433-			}
xiaohongshu/feed_detail.go-434-
--
xiaohongshu/feed_detail.go-439-
xiaohongshu/feed_detail.go-440-			// 模拟人类阅读时间
xiaohongshu/feed_detail.go:441:			sleepRandom(readTimeRange.min, readTimeRange.max)
xiaohongshu/feed_detail.go-442-			clickSuccess = true
xiaohongshu/feed_detail.go-443-			return nil
--
xiaohongshu/feed_detail.go-484-		}
xiaohongshu/feed_detail.go-485-
xiaohongshu/feed_detail.go:486:		sleepRandom(scrollWaitRange.min, scrollWaitRange.max)
xiaohongshu/feed_detail.go-487-
xiaohongshu/feed_detail.go-488-		currentScrollTop = getScrollTop(page)
--
xiaohongshu/feed_detail.go-497-
xiaohongshu/feed_detail.go-498-		if i < pushCount-1 {
xiaohongshu/feed_detail.go:499:			sleepRandom(humanDelayRange.min, humanDelayRange.max)
xiaohongshu/feed_detail.go-500-		}
xiaohongshu/feed_detail.go-501-	}
--
xiaohongshu/feed_detail.go-507-			logrus.Warnf("滚动到底部失败: %v", err)
xiaohongshu/feed_detail.go-508-		}
xiaohongshu/feed_detail.go:509:		sleepRandom(postScrollRange.min, postScrollRange.max)
xiaohongshu/feed_detail.go-510-		currentScrollTop = getScrollTop(page)
xiaohongshu/feed_detail.go-511-		actualDelta = currentScrollTop - beforeTop + actualDelta
--
xiaohongshu/feed_detail.go-548-	}
xiaohongshu/feed_detail.go-549-	// 等待滚动完成
xiaohongshu/feed_detail.go:550:	time.Sleep(500 * time.Millisecond)
xiaohongshu/feed_detail.go-551-
xiaohongshu/feed_detail.go-552-	// 触发一次小滚动，激活懒加载机制
--
xiaohongshu/feed_detail.go-746-
xiaohongshu/feed_detail.go-747-func checkPageAccessible(page *hrod.Page) error {
xiaohongshu/feed_detail.go:748:	time.Sleep(500 * time.Millisecond)
xiaohongshu/feed_detail.go-749-
xiaohongshu/feed_detail.go-750-	// 查找错误提示容器

exec
/bin/bash -lc "nl -ba xiaohongshu/like_favorite.go; nl -ba xiaohongshu/login.go; nl -ba xiaohongshu/feeds.go; nl -ba xiaohongshu/comment_feed.go | sed -n '145,285p'" in /tmp/xiaohongshu-mcp-fork
 succeeded in 0ms:
     1	package xiaohongshu
     2	
     3	import (
     4		"context"
     5		"encoding/json"
     6		"fmt"
     7		"time"
     8	
     9		hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
    10		"github.com/pkg/errors"
    11		"github.com/sirupsen/logrus"
    12		myerrors "github.com/xpzouying/xiaohongshu-mcp/errors"
    13	)
    14	
    15	// ActionResult 通用动作响应（点赞/收藏等）
    16	type ActionResult struct {
    17		FeedID  string `json:"feed_id"`
    18		Success bool   `json:"success"`
    19		Message string `json:"message"`
    20	}
    21	
    22	// 选择器常量
    23	const (
    24		SelectorLikeButton    = ".interact-container .left .like-lottie"
    25		SelectorCollectButton = ".interact-container .left .reds-icon.collect-icon"
    26	)
    27	
    28	// interactActionType 交互动作类型
    29	type interactActionType string
    30	
    31	const (
    32		actionLike       interactActionType = "点赞"
    33		actionFavorite   interactActionType = "收藏"
    34		actionUnlike     interactActionType = "取消点赞"
    35		actionUnfavorite interactActionType = "取消收藏"
    36	)
    37	
    38	type interactAction struct {
    39		page *hrod.Page
    40	}
    41	
    42	func newInteractAction(page *hrod.Page) *interactAction {
    43		return &interactAction{page: page}
    44	}
    45	
    46	func (a *interactAction) preparePage(ctx context.Context, actionType interactActionType, feedID, xsecToken string) *hrod.Page {
    47		page := a.page.Context(ctx).Timeout(60 * time.Second)
    48		url := makeFeedDetailURL(feedID, xsecToken)
    49		logrus.Infof("Opening feed detail page for %s: %s", actionType, url)
    50	
    51		page.MustNavigate(url)
    52		page.MustWaitDOMStable()
    53		time.Sleep(1 * time.Second)
    54	
    55		return page
    56	}
    57	
    58	func (a *interactAction) performClick(page *hrod.Page, selector string) {
    59		element := page.MustElement(selector)
    60		element.MustClick()
    61	}
    62	
    63	// LikeAction 负责处理点赞相关交互
    64	type LikeAction struct {
    65		*interactAction
    66	}
    67	
    68	func NewLikeAction(page *hrod.Page) *LikeAction {
    69		return &LikeAction{interactAction: newInteractAction(page)}
    70	}
    71	
    72	// Like 点赞指定笔记，如果已点赞则直接返回
    73	func (a *LikeAction) Like(ctx context.Context, feedID, xsecToken string) error {
    74		return a.perform(ctx, feedID, xsecToken, true)
    75	}
    76	
    77	// Unlike 取消点赞指定笔记，如果未点赞则直接返回
    78	func (a *LikeAction) Unlike(ctx context.Context, feedID, xsecToken string) error {
    79		return a.perform(ctx, feedID, xsecToken, false)
    80	}
    81	
    82	func (a *LikeAction) perform(ctx context.Context, feedID, xsecToken string, targetLiked bool) error {
    83		actionType := actionLike
    84		if !targetLiked {
    85			actionType = actionUnlike
    86		}
    87	
    88		page := a.preparePage(ctx, actionType, feedID, xsecToken)
    89	
    90		liked, _, err := a.getInteractState(page, feedID)
    91		if err != nil {
    92			logrus.Warnf("failed to read interact state: %v (continue to try clicking)", err)
    93			return a.toggleLike(page, feedID, targetLiked, actionType)
    94		}
    95	
    96		if targetLiked && liked {
    97			logrus.Infof("feed %s already liked, skip clicking", feedID)
    98			return nil
    99		}
   100		if !targetLiked && !liked {
   101			logrus.Infof("feed %s not liked yet, skip clicking", feedID)
   102			return nil
   103		}
   104	
   105		return a.toggleLike(page, feedID, targetLiked, actionType)
   106	}
   107	
   108	func (a *LikeAction) toggleLike(page *hrod.Page, feedID string, targetLiked bool, actionType interactActionType) error {
   109		a.performClick(page, SelectorLikeButton)
   110		time.Sleep(3 * time.Second)
   111	
   112		liked, _, err := a.getInteractState(page, feedID)
   113		if err != nil {
   114			logrus.Warnf("验证%s状态失败: %v", actionType, err)
   115			return nil
   116		}
   117		if liked == targetLiked {
   118			logrus.Infof("feed %s %s成功", feedID, actionType)
   119			return nil
   120		}
   121	
   122		logrus.Warnf("feed %s %s可能未成功，状态未变化，尝试再次点击", feedID, actionType)
   123		a.performClick(page, SelectorLikeButton)
   124		time.Sleep(2 * time.Second)
   125	
   126		liked, _, err = a.getInteractState(page, feedID)
   127		if err != nil {
   128			logrus.Warnf("第二次验证%s状态失败: %v", actionType, err)
   129			return nil
   130		}
   131		if liked == targetLiked {
   132			logrus.Infof("feed %s 第二次点击%s成功", feedID, actionType)
   133			return nil
   134		}
   135	
   136		return nil
   137	}
   138	
   139	// FavoriteAction 负责处理收藏相关交互
   140	type FavoriteAction struct {
   141		*interactAction
   142	}
   143	
   144	func NewFavoriteAction(page *hrod.Page) *FavoriteAction {
   145		return &FavoriteAction{interactAction: newInteractAction(page)}
   146	}
   147	
   148	// Favorite 收藏指定笔记，如果已收藏则直接返回
   149	func (a *FavoriteAction) Favorite(ctx context.Context, feedID, xsecToken string) error {
   150		return a.perform(ctx, feedID, xsecToken, true)
   151	}
   152	
   153	// Unfavorite 取消收藏指定笔记，如果未收藏则直接返回
   154	func (a *FavoriteAction) Unfavorite(ctx context.Context, feedID, xsecToken string) error {
   155		return a.perform(ctx, feedID, xsecToken, false)
   156	}
   157	
   158	func (a *FavoriteAction) perform(ctx context.Context, feedID, xsecToken string, targetCollected bool) error {
   159		actionType := actionFavorite
   160		if !targetCollected {
   161			actionType = actionUnfavorite
   162		}
   163	
   164		page := a.preparePage(ctx, actionType, feedID, xsecToken)
   165	
   166		_, collected, err := a.getInteractState(page, feedID)
   167		if err != nil {
   168			logrus.Warnf("failed to read interact state: %v (continue to try clicking)", err)
   169			return a.toggleFavorite(page, feedID, targetCollected, actionType)
   170		}
   171	
   172		if targetCollected && collected {
   173			logrus.Infof("feed %s already favorited, skip clicking", feedID)
   174			return nil
   175		}
   176		if !targetCollected && !collected {
   177			logrus.Infof("feed %s not favorited yet, skip clicking", feedID)
   178			return nil
   179		}
   180	
   181		return a.toggleFavorite(page, feedID, targetCollected, actionType)
   182	}
   183	
   184	func (a *FavoriteAction) toggleFavorite(page *hrod.Page, feedID string, targetCollected bool, actionType interactActionType) error {
   185		a.performClick(page, SelectorCollectButton)
   186		time.Sleep(3 * time.Second)
   187	
   188		_, collected, err := a.getInteractState(page, feedID)
   189		if err != nil {
   190			logrus.Warnf("验证%s状态失败: %v", actionType, err)
   191			return nil
   192		}
   193		if collected == targetCollected {
   194			logrus.Infof("feed %s %s成功", feedID, actionType)
   195			return nil
   196		}
   197	
   198		logrus.Warnf("feed %s %s可能未成功，状态未变化，尝试再次点击", feedID, actionType)
   199		a.performClick(page, SelectorCollectButton)
   200		time.Sleep(2 * time.Second)
   201	
   202		_, collected, err = a.getInteractState(page, feedID)
   203		if err != nil {
   204			logrus.Warnf("第二次验证%s状态失败: %v", actionType, err)
   205			return nil
   206		}
   207		if collected == targetCollected {
   208			logrus.Infof("feed %s 第二次点击%s成功", feedID, actionType)
   209			return nil
   210		}
   211	
   212		return nil
   213	}
   214	
   215	// getInteractState 从 __INITIAL_STATE__ 读取笔记的点赞/收藏状态
   216	func (a *interactAction) getInteractState(page *hrod.Page, feedID string) (liked bool, collected bool, err error) {
   217	
   218		result := page.MustEval(`() => {
   219			if (window.__INITIAL_STATE__ &&
   220			    window.__INITIAL_STATE__.note &&
   221			    window.__INITIAL_STATE__.note.noteDetailMap) {
   222				return JSON.stringify(window.__INITIAL_STATE__.note.noteDetailMap);
   223			}
   224			return "";
   225		}`).String()
   226		if result == "" {
   227			return false, false, myerrors.ErrNoFeedDetail
   228		}
   229	
   230		// 直接解析为 noteDetailMap
   231		var noteDetailMap map[string]struct {
   232			Note struct {
   233				InteractInfo struct {
   234					Liked     bool `json:"liked"`
   235					Collected bool `json:"collected"`
   236				} `json:"interactInfo"`
   237			} `json:"note"`
   238		}
   239		if err := json.Unmarshal([]byte(result), &noteDetailMap); err != nil {
   240			return false, false, errors.Wrap(err, "unmarshal noteDetailMap failed")
   241		}
   242	
   243		detail, ok := noteDetailMap[feedID]
   244		if !ok {
   245			return false, false, fmt.Errorf("feed %s not in noteDetailMap", feedID)
   246		}
   247		return detail.Note.InteractInfo.Liked, detail.Note.InteractInfo.Collected, nil
   248	}
     1	package xiaohongshu
     2	
     3	import (
     4		"context"
     5		"time"
     6	
     7		hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
     8		"github.com/pkg/errors"
     9	)
    10	
    11	type LoginAction struct {
    12		page *hrod.Page
    13	}
    14	
    15	func NewLogin(page *hrod.Page) *LoginAction {
    16		return &LoginAction{page: page}
    17	}
    18	
    19	func (a *LoginAction) CheckLoginStatus(ctx context.Context) (bool, error) {
    20		pp := a.page.Context(ctx)
    21		pp.MustNavigate("https://www.xiaohongshu.com/explore").MustWaitLoad()
    22	
    23		time.Sleep(1 * time.Second)
    24	
    25		exists, _, err := pp.Has(`.main-container .user .link-wrapper .channel`)
    26		if err != nil {
    27			return false, errors.Wrap(err, "check login status failed")
    28		}
    29	
    30		if !exists {
    31			return false, errors.Wrap(err, "login status element not found")
    32		}
    33	
    34		return true, nil
    35	}
    36	
    37	func (a *LoginAction) Login(ctx context.Context) error {
    38		pp := a.page.Context(ctx)
    39	
    40		// 导航到小红书首页，这会触发二维码弹窗
    41		pp.MustNavigate("https://www.xiaohongshu.com/explore").MustWaitLoad()
    42	
    43		// 等待一小段时间让页面完全加载
    44		time.Sleep(2 * time.Second)
    45	
    46		// 检查是否已经登录
    47		if exists, _, _ := pp.Has(".main-container .user .link-wrapper .channel"); exists {
    48			// 已经登录，直接返回
    49			return nil
    50		}
    51	
    52		// 等待扫码成功提示或者登录完成
    53		// 这里我们等待登录成功的元素出现，这样更简单可靠
    54		pp.MustElement(".main-container .user .link-wrapper .channel")
    55	
    56		return nil
    57	}
    58	
    59	func (a *LoginAction) FetchQrcodeImage(ctx context.Context) (string, bool, error) {
    60		pp := a.page.Context(ctx)
    61	
    62		// 导航到小红书首页，这会触发二维码弹窗
    63		pp.MustNavigate("https://www.xiaohongshu.com/explore").MustWaitLoad()
    64	
    65		// 等待一小段时间让页面完全加载
    66		time.Sleep(2 * time.Second)
    67	
    68		// 检查是否已经登录
    69		if exists, _, _ := pp.Has(".main-container .user .link-wrapper .channel"); exists {
    70			return "", true, nil
    71		}
    72	
    73		// 获取二维码图片
    74		src, err := pp.MustElement(".login-container .qrcode-img").Attribute("src")
    75		if err != nil {
    76			return "", false, errors.Wrap(err, "get qrcode src failed")
    77		}
    78		if src == nil || len(*src) == 0 {
    79			return "", false, errors.New("qrcode src is empty")
    80		}
    81	
    82		return *src, false, nil
    83	}
    84	
    85	func (a *LoginAction) WaitForLogin(ctx context.Context) bool {
    86		pp := a.page.Context(ctx)
    87		ticker := time.NewTicker(500 * time.Millisecond)
    88		defer ticker.Stop()
    89	
    90		for {
    91			select {
    92			case <-ctx.Done():
    93				return false
    94			case <-ticker.C:
    95				el, err := pp.Element(".main-container .user .link-wrapper .channel")
    96				if err == nil && el != nil {
    97					return true
    98				}
    99			}
   100		}
   101	}
     1	package xiaohongshu
     2	
     3	import (
     4		"context"
     5		"encoding/json"
     6		"fmt"
     7		"time"
     8	
     9		hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
    10		"github.com/xpzouying/xiaohongshu-mcp/errors"
    11	)
    12	
    13	type FeedsListAction struct {
    14		page *hrod.Page
    15	}
    16	
    17	func NewFeedsListAction(page *hrod.Page) *FeedsListAction {
    18		pp := page.Timeout(60 * time.Second)
    19	
    20		pp.MustNavigate("https://www.xiaohongshu.com")
    21		pp.MustWaitDOMStable()
    22	
    23		return &FeedsListAction{page: pp}
    24	}
    25	
    26	// GetFeedsList 获取页面的 Feed 列表数据
    27	func (f *FeedsListAction) GetFeedsList(ctx context.Context) ([]Feed, error) {
    28		page := f.page.Context(ctx)
    29	
    30		time.Sleep(1 * time.Second)
    31	
    32		result := page.MustEval(`() => {
    33			if (window.__INITIAL_STATE__ &&
    34			    window.__INITIAL_STATE__.feed &&
    35			    window.__INITIAL_STATE__.feed.feeds) {
    36				const feeds = window.__INITIAL_STATE__.feed.feeds;
    37				const feedsData = feeds.value !== undefined ? feeds.value : feeds._value;
    38				if (feedsData) {
    39					return JSON.stringify(feedsData);
    40				}
    41			}
    42			return "";
    43		}`).String()
    44	
    45		if result == "" {
    46			return nil, errors.ErrNoFeeds
    47		}
    48	
    49		var feeds []Feed
    50		if err := json.Unmarshal([]byte(result), &feeds); err != nil {
    51			return nil, fmt.Errorf("failed to unmarshal feeds: %w", err)
    52		}
    53	
    54		return feeds, nil
    55	}
   145		}
   146	
   147		if err := submitBtn.Click(proto.InputMouseButtonLeft, 1); err != nil {
   148			return fmt.Errorf("点击提交按钮失败: %w", err)
   149		}
   150	
   151		time.Sleep(2 * time.Second)
   152		logrus.Infof("回复评论成功")
   153		return nil
   154	}
   155	
   156	// findCommentElement 查找指定评论元素（参考 feed_detail.go 的滚动逻辑）
   157	func findCommentElement(page *hrod.Page, commentID, userID string) (*hrod.Element, error) {
   158		logrus.Infof("开始查找评论 - commentID: %s, userID: %s", commentID, userID)
   159	
   160		const maxAttempts = 100
   161		const scrollInterval = 800 * time.Millisecond
   162	
   163		// 先滚动到评论区
   164		scrollToCommentsArea(page)
   165		time.Sleep(1 * time.Second)
   166	
   167		var lastCommentCount = 0
   168		stagnantChecks := 0
   169	
   170		logrus.Infof("开始循环查找，最大尝试次数: %d", maxAttempts)
   171	
   172		for attempt := 0; attempt < maxAttempts; attempt++ {
   173			logrus.Infof("=== 查找尝试 %d/%d ===", attempt+1, maxAttempts)
   174	
   175			// === 1. 检查是否到达底部 ===
   176			if checkEndContainer(page) {
   177				logrus.Info("已到达评论底部，未找到目标评论")
   178				break
   179			}
   180	
   181			// === 2. 获取当前评论数量 ===
   182			currentCount := getCommentCount(page)
   183			logrus.Infof("当前评论数: %d", currentCount)
   184			
   185			if currentCount != lastCommentCount {
   186				logrus.Infof("✓ 评论数增加: %d -> %d", lastCommentCount, currentCount)
   187				lastCommentCount = currentCount
   188				stagnantChecks = 0
   189			} else {
   190				stagnantChecks++
   191				if stagnantChecks%5 == 0 {
   192					logrus.Infof("评论数停滞 %d 次", stagnantChecks)
   193				}
   194			}
   195	
   196			// === 3. 停滞检测 ===
   197			if stagnantChecks >= 10 {
   198				logrus.Info("评论数量停滞超过10次，可能已加载完所有评论")
   199				break
   200			}
   201	
   202			// === 4. 先滚动到最后一个评论（触发懒加载）===
   203			if currentCount > 0 {
   204				logrus.Infof("滚动到最后一个评论（共 %d 条）", currentCount)
   205				
   206				// 使用 Go 获取所有评论元素
   207				elements, err := page.Timeout(2 * time.Second).Elements(".parent-comment, .comment-item, .comment")
   208				if err == nil && len(elements) > 0 {
   209					// 滚动到最后一个评论
   210					lastComment := elements[len(elements)-1]
   211					err := lastComment.ScrollIntoView()
   212					if err != nil {
   213						logrus.Warnf("滚动到最后一个评论失败: %v", err)
   214					}
   215				} else {
   216					logrus.Warnf("未找到评论元素: %v", err)
   217				}
   218				time.Sleep(300 * time.Millisecond)
   219			}
   220	
   221			// === 5. 继续向下滚动 ===
   222			logrus.Infof("继续向下滚动...")
   223			viewportHeight := page.MustEval(`() => window.innerHeight`).Int()
   224			if err := page.Actor().Mouse.Scroll(0, float64(viewportHeight)*0.8); err != nil {
   225				logrus.Warnf("滚动失败: %v", err)
   226			}
   227			time.Sleep(500 * time.Millisecond)
   228	
   229			// === 6. 滚动后立即查找（边滚动边查找）===
   230			// 优先通过 commentID 查找（使用 Timeout 避免长时间等待）
   231			if commentID != "" {
   232				selector := fmt.Sprintf("#comment-%s", commentID)
   233				logrus.Infof("尝试通过 commentID 查找: %s", selector)
   234				
   235				// 使用 Timeout 避免长时间等待
   236				el, err := page.Timeout(2 * time.Second).Element(selector)
   237				if err == nil && el != nil {
   238					logrus.Infof("✓ 通过 commentID 找到评论: %s (尝试 %d 次)", commentID, attempt+1)
   239					return el, nil
   240				}
   241				logrus.Infof("未找到 commentID (2秒超时)")
   242			}
   243	
   244			// 通过 userID 查找
   245			if userID != "" {
   246				logrus.Infof("尝试通过 userID 查找: %s", userID)
   247				
   248				// 使用 Timeout 避免长时间等待
   249				elements, err := page.Timeout(2 * time.Second).Elements(".comment-item, .comment, .parent-comment")
   250				if err == nil && len(elements) > 0 {
   251					logrus.Infof("找到 %d 个评论元素", len(elements))
   252					for i, el := range elements {
   253						// 快速检查，不等待
   254						userEl, err := el.Timeout(500 * time.Millisecond).Element(fmt.Sprintf(`[data-user-id="%s"]`, userID))
   255						if err == nil && userEl != nil {
   256							logrus.Infof("✓ 通过 userID 在第 %d 个元素中找到评论: %s (尝试 %d 次)", i+1, userID, attempt+1)
   257							return el, nil
   258						}
   259					}
   260					logrus.Infof("在 %d 个元素中未找到匹配的 userID", len(elements))
   261				} else {
   262					logrus.Infof("获取评论元素失败或超时: %v", err)
   263				}
   264			}
   265			
   266			logrus.Infof("本次尝试未找到目标评论，继续下一轮...")
   267	
   268			// === 7. 等待内容加载 ===
   269			time.Sleep(scrollInterval)
   270		}
   271	
   272		return nil, fmt.Errorf("未找到评论 (commentID: %s, userID: %s), 尝试次数: %d", commentID, userID, maxAttempts)
   273	}

exec
/bin/bash -lc "rg -n 'func \\(p \\*Page\\).*\\{|func \\(p \\*Page\\) wrapPage|func \\(p \\*Page\\) Context|func \\(p \\*Page\\) Timeout|func \\(b \\*Browser\\) wrapPage' pkg/humanize/rod/*_test.go pkg/humanize/rod/hrod.go; rg -n 'NewBrowser|NewWithContext|InitPosition|Context\\(' pkg/humanize/rod --glob '*_test.go'; rg -n 'func sleepWithContext' -S --glob '*.go'" in /tmp/xiaohongshu-mcp-fork
exec
/bin/bash -lc "rg -n -C 8 'NewPublish(Image|Video)Action|NewFeedDetailAction|NewFeedsListAction|NewCommentFeedAction' service.go; rg -n -C 5 'time\\.Sleep|sleepRandom' xiaohongshu/publish.go" in /tmp/xiaohongshu-mcp-fork
 succeeded in 0ms:
248-// publishContent 执行内容发布
249-func (s *XiaohongshuService) publishContent(ctx context.Context, content xiaohongshu.PublishImageContent) error {
250-	page, err := s.bm.Acquire(ctx)
251-	if err != nil {
252-		return err
253-	}
254-	defer s.bm.Release()
255-
256:	action, err := xiaohongshu.NewPublishImageAction(page.Context(ctx))
257-	if err != nil {
258-		return err
259-	}
260-
261-	// 执行发布
262-	return action.Publish(ctx, content)
263-}
264-
--
331-// publishVideo 执行视频发布
332-func (s *XiaohongshuService) publishVideo(ctx context.Context, content xiaohongshu.PublishVideoContent) error {
333-	page, err := s.bm.Acquire(ctx)
334-	if err != nil {
335-		return err
336-	}
337-	defer s.bm.Release()
338-
339:	action, err := xiaohongshu.NewPublishVideoAction(page.Context(ctx))
340-	if err != nil {
341-		return err
342-	}
343-
344-	return action.PublishVideo(ctx, content)
345-}
346-
347-// ListFeeds 获取Feeds列表
348-func (s *XiaohongshuService) ListFeeds(ctx context.Context) (*FeedsListResponse, error) {
349-	page, err := s.bm.Acquire(ctx)
350-	if err != nil {
351-		return nil, err
352-	}
353-	defer s.bm.Release()
354-
355-	// 创建 Feeds 列表 action
356:	action := xiaohongshu.NewFeedsListAction(page.Context(ctx))
357-
358-	// 获取 Feeds 列表
359-	feeds, err := action.GetFeedsList(ctx)
360-	if err != nil {
361-		logrus.Errorf("获取 Feeds 列表失败: %v", err)
362-		return nil, err
363-	}
364-
--
401-func (s *XiaohongshuService) GetFeedDetailWithConfig(ctx context.Context, feedID, xsecToken string, loadAllComments bool, config xiaohongshu.CommentLoadConfig) (*FeedDetailResponse, error) {
402-	page, err := s.bm.Acquire(ctx)
403-	if err != nil {
404-		return nil, err
405-	}
406-	defer s.bm.Release()
407-
408-	// 创建 Feed 详情 action
409:	action := xiaohongshu.NewFeedDetailAction(page.Context(ctx))
410-
411-	// 获取 Feed 详情
412-	result, err := action.GetFeedDetailWithConfig(ctx, feedID, xsecToken, loadAllComments, config)
413-	if err != nil {
414-		return nil, err
415-	}
416-
417-	response := &FeedDetailResponse{
--
449-// PostCommentToFeed 发表评论到Feed
450-func (s *XiaohongshuService) PostCommentToFeed(ctx context.Context, feedID, xsecToken, content string) (*PostCommentResponse, error) {
451-	page, err := s.bm.Acquire(ctx)
452-	if err != nil {
453-		return nil, err
454-	}
455-	defer s.bm.Release()
456-
457:	action := xiaohongshu.NewCommentFeedAction(page.Context(ctx))
458-
459-	if err := action.PostComment(ctx, feedID, xsecToken, content); err != nil {
460-		return nil, err
461-	}
462-
463-	return &PostCommentResponse{FeedID: feedID, Success: true, Message: "评论发表成功"}, nil
464-}
465-
--
526-// ReplyCommentToFeed 回复指定评论
527-func (s *XiaohongshuService) ReplyCommentToFeed(ctx context.Context, feedID, xsecToken, commentID, userID, content string) (*ReplyCommentResponse, error) {
528-	page, err := s.bm.Acquire(ctx)
529-	if err != nil {
530-		return nil, err
531-	}
532-	defer s.bm.Release()
533-
534:	action := xiaohongshu.NewCommentFeedAction(page.Context(ctx))
535-
536-	if err := action.ReplyToComment(ctx, feedID, xsecToken, commentID, userID, content); err != nil {
537-		return nil, err
538-	}
539-
540-	return &ReplyCommentResponse{
541-		FeedID:          feedID,
542-		TargetCommentID: commentID,
48-
49-	// 等待页面加载，使用 WaitLoad 代替 WaitIdle（更宽松）
50-	if err := pp.WaitLoad(); err != nil {
51-		logrus.Warnf("等待页面加载出现问题: %v，继续尝试", err)
52-	}
53:	time.Sleep(2 * time.Second)
54-
55-	// 等待页面稳定
56-	if err := pp.WaitDOMStable(time.Second, 0.1); err != nil {
57-		logrus.Warnf("等待 DOM 稳定出现问题: %v，继续尝试", err)
58-	}
59:	time.Sleep(1 * time.Second)
60-
61-	if err := mustClickPublishTab(pp, "上传图文"); err != nil {
62-		logrus.Errorf("点击上传图文 TAB 失败: %v", err)
63-		return nil, err
64-	}
65-
66:	time.Sleep(1 * time.Second)
67-
68-	return &PublishAction{
69-		page: pp,
70-	}, nil
71-}
--
125-	deadline := time.Now().Add(15 * time.Second)
126-	for time.Now().Before(deadline) {
127-		tab, blocked, err := getTabElement(page, tabname)
128-		if err != nil {
129-			logrus.Warnf("获取发布 TAB 元素失败: %v", err)
130:			time.Sleep(200 * time.Millisecond)
131-			continue
132-		}
133-
134-		if tab == nil {
135:			time.Sleep(200 * time.Millisecond)
136-			continue
137-		}
138-
139-		if blocked {
140-			logrus.Info("发布 TAB 被遮挡，尝试移除遮挡")
141-			removePopCover(page)
142:			time.Sleep(200 * time.Millisecond)
143-			continue
144-		}
145-
146-		if err := tab.Click(proto.InputMouseButtonLeft, 1); err != nil {
147-			logrus.Warnf("点击发布 TAB 失败: %v", err)
148:			time.Sleep(200 * time.Millisecond)
149-			continue
150-		}
151-
152-		return nil
153-	}
--
236-
237-		// 等待当前图片上传完成（预览元素数量达到 i+1），最多等 60 秒
238-		if err := waitForUploadComplete(page, i+1); err != nil {
239-			return errors.Wrapf(err, "第%d张图片上传超时", i+1)
240-		}
241:		time.Sleep(1 * time.Second)
242-	}
243-
244-	return nil
245-}
246-
--
252-	lastLogCount := expectedCount - 1
253-
254-	for time.Since(start) < maxWaitTime {
255-		uploadedImages, err := page.Elements(".img-preview-area .pr")
256-		if err != nil {
257:			time.Sleep(checkInterval)
258-			continue
259-		}
260-
261-		currentCount := len(uploadedImages)
262-		// 数量变化时才打印，避免刷屏
--
267-		if currentCount >= expectedCount {
268-			slog.Info("图片上传完成", "count", currentCount)
269-			return nil
270-		}
271-
272:		time.Sleep(checkInterval)
273-	}
274-
275-	return errors.Errorf("第%d张图片上传超时(60s)，请检查网络连接和图片大小", expectedCount)
276-}
277-
--
283-	if err := titleElem.Input(title); err != nil {
284-		return errors.Wrap(err, "输入标题失败")
285-	}
286-
287-	// 检查标题长度
288:	time.Sleep(500 * time.Millisecond)
289-	if err := checkTitleMaxLength(page); err != nil {
290-		return err
291-	}
292-	slog.Info("检查标题长度：通过")
293-
294:	time.Sleep(1 * time.Second)
295-
296-	contentElem, ok := getContentElement(page)
297-	if !ok {
298-		return errors.New("没有找到内容输入框")
299-	}
--
305-	}
306-	if err := inputTags(contentElem, tags); err != nil {
307-		return err
308-	}
309-
310:	time.Sleep(1 * time.Second)
311-
312-	// 检查正文长度
313-	if err := checkContentMaxLength(page); err != nil {
314-		return err
315-	}
--
344-
345-	if err := clickPublishButton(page); err != nil {
346-		return err
347-	}
348-
349:	time.Sleep(3 * time.Second)
350-	return nil
351-}
352-
353-type publishButton struct {
354-	elem     *hrod.Element
--
381-
382-	for time.Since(start) < maxWait {
383-		btn, disabledReason, err := findPublishButton(page)
384-		if err != nil {
385-			slog.Warn("查找发布按钮失败，继续等待", "error", err)
386:			time.Sleep(interval)
387-			continue
388-		}
389-		if btn != nil && disabledReason == "" {
390-			return btn, nil
391-		}
392-		if disabledReason != "" {
393-			lastDisabledReason = disabledReason
394-		}
395:		time.Sleep(interval)
396-	}
397-
398-	if lastDisabledReason != "" {
399-		return nil, errors.Errorf("等待发布按钮可点击超时: %s", lastDisabledReason)
400-	}
--
484-}
485-
486-// waitAndClickTitleInput 在填写正文后等待 1 秒并回点标题输入框，增强后续交互稳定性
487-func waitAndClickTitleInput(titleElem *hrod.Element) error {
488-	slog.Info("正文填写完成，准备等待后回点标题输入框")
489:	time.Sleep(1 * time.Second)
490-	if err := titleElem.Click(proto.InputMouseButtonLeft, 1); err != nil {
491-		return errors.Wrap(err, "回点标题输入框失败")
492-	}
493-	slog.Info("已回点标题输入框，继续后续发布流程")
494-	return nil
--
575-func inputTags(contentElem *hrod.Element, tags []string) error {
576-	if len(tags) == 0 {
577-		return nil
578-	}
579-
580:	time.Sleep(1 * time.Second)
581-
582-	for i := 0; i < 20; i++ {
583-		ka, err := contentElem.KeyActions()
584-		if err != nil {
585-			return errors.Wrap(err, "创建键盘操作失败")
586-		}
587-		if err := ka.Type(input.ArrowDown).Do(); err != nil {
588-			return errors.Wrap(err, "按下方向键失败")
589-		}
590:		time.Sleep(10 * time.Millisecond)
591-	}
592-
593-	ka, err := contentElem.KeyActions()
594-	if err != nil {
595-		return errors.Wrap(err, "创建键盘操作失败")
596-	}
597-	if err := ka.Press(input.Enter).Press(input.Enter).Do(); err != nil {
598-		return errors.Wrap(err, "按下回车键失败")
599-	}
600-
601:	time.Sleep(1 * time.Second)
602-
603-	for _, tag := range tags {
604-		tag = strings.TrimLeft(tag, "#")
605-		if err := inputTag(contentElem, tag); err != nil {
606-			return errors.Wrapf(err, "输入标签[%s]失败", tag)
--
611-
612-func inputTag(contentElem *hrod.Element, tag string) error {
613-	if err := contentElem.Input("#"); err != nil {
614-		return errors.Wrap(err, "输入#失败")
615-	}
616:	time.Sleep(200 * time.Millisecond)
617-
618-	for _, char := range tag {
619-		if err := contentElem.Input(string(char)); err != nil {
620-			return errors.Wrapf(err, "输入字符[%c]失败", char)
621-		}
622:		time.Sleep(50 * time.Millisecond)
623-	}
624-
625:	time.Sleep(1 * time.Second)
626-
627-	page := contentElem.Page()
628-	topicContainer, err := page.Element("#creator-editor-topic-container")
629-	if err != nil || topicContainer == nil {
630-		slog.Warn("未找到标签联想下拉框，直接输入空格", "tag", tag)
--
639-
640-	if err := firstItem.Click(proto.InputMouseButtonLeft, 1); err != nil {
641-		return errors.Wrap(err, "点击标签联想选项失败")
642-	}
643-	slog.Info("成功点击标签联想选项", "tag", tag)
644:	time.Sleep(200 * time.Millisecond)
645-
646:	time.Sleep(500 * time.Millisecond) // 等待标签处理完成
647-	return nil
648-}
649-
650-func findTextboxByPlaceholder(page *rod.Page) (*rod.Element, error) {
651-	elements := page.MustElements("p")
--
785-		return errors.Wrap(err, "查找可见范围下拉框失败")
786-	}
787-	if err := dropdown.Click(proto.InputMouseButtonLeft, 1); err != nil {
788-		return errors.Wrap(err, "点击可见范围下拉框失败")
789-	}
790:	time.Sleep(500 * time.Millisecond)
791-
792-	// 在弹窗中查找并点击目标选项
793-	opts, err := page.Elements("div.d-options-wrapper div.d-grid-item div.custom-option")
794-	if err != nil {
795-		return errors.Wrap(err, "查找可见范围选项失败")
--
802-		if strings.Contains(text, visibility) {
803-			if err := opt.Click(proto.InputMouseButtonLeft, 1); err != nil {
804-				return errors.Wrap(err, "选择可见范围失败")
805-			}
806-			slog.Info("已设置可见范围", "visibility", visibility)
807:			time.Sleep(200 * time.Millisecond)
808-			return nil
809-		}
810-	}
811-	return errors.Errorf("未找到可见范围选项: %s", visibility)
812-}
--
815-func setSchedulePublish(page *hrod.Page, t time.Time) error {
816-	// 1. 点击定时发布开关
817-	if err := clickScheduleSwitch(page); err != nil {
818-		return err
819-	}
820:	time.Sleep(800 * time.Millisecond)
821-
822-	// 2. 设置日期时间
823-	if err := setDateTime(page, t); err != nil {
824-		return err
825-	}
826:	time.Sleep(500 * time.Millisecond)
827-
828-	return nil
829-}
830-
831-// clickScheduleSwitch 点击定时发布开关
--
909-		// 点击开关
910-		if err := switchElem.Click(proto.InputMouseButtonLeft, 1); err != nil {
911-			return errors.Wrap(err, "点击原创声明开关失败")
912-		}
913-
914:		time.Sleep(500 * time.Millisecond)
915-
916-		// 处理原创声明确认弹窗
917-		if err := confirmOriginalDeclaration(page); err != nil {
918-			return errors.Wrap(err, "确认原创声明失败")
919-		}
--
926-}
927-
928-// confirmOriginalDeclaration 处理原创声明确认弹窗
929-func confirmOriginalDeclaration(page *hrod.Page) error {
930-	// 等待确认弹窗出现
931:	time.Sleep(800 * time.Millisecond)
932-
933-	// 查找包含"原创声明须知"的 footer 并勾选 checkbox
934-	if noticeFooter, err := page.ElementR("div.footer", "原创声明须知"); err == nil {
935-		if checkbox, err := noticeFooter.Element(`div.d-checkbox input[type="checkbox"]`); err == nil {
936-			checked, _ := checkbox.Rod.Property("checked")
--
944-		}
945-	} else {
946-		slog.Warn("未找到原创声明须知弹窗", "error", err)
947-	}
948-
949:	time.Sleep(500 * time.Millisecond)
950-
951-	// 查找包含"声明原创"的 footer
952-	confirmFooter, err := page.ElementR("div.footer", "声明原创")
953-	if err != nil {
954-		return errors.Wrap(err, "未找到声明原创弹窗")
--
973-			checked, _ := checkbox.Rod.Property("checked")
974-			if !checked.Bool() {
975-				_ = checkbox.Click(proto.InputMouseButtonLeft, 1)
976-			}
977-		}
978:		time.Sleep(300 * time.Millisecond)
979-		if isDisabled(btn) {
980-			return errors.New("声明原创按钮仍处于禁用状态")
981-		}
982-	}
983-
984-	if err := btn.Click(proto.InputMouseButtonLeft, 1); err != nil {
985-		return errors.Wrap(err, "点击声明原创按钮失败")
986-	}
987-
988-	slog.Info("已成功点击声明原创按钮")
989:	time.Sleep(300 * time.Millisecond)
990-
991-	return nil
992-}
993-
994-// bindProducts 绑定商品到发布内容
--
1001-
1002-	// 点击"添加商品"按钮
1003-	if err := clickAddProductButton(page); err != nil {
1004-		return errors.Wrap(err, "点击添加商品按钮失败")
1005-	}
1006:	time.Sleep(1 * time.Second)
1007-
1008-	// 等待商品选择弹窗出现
1009-	modal, err := waitForProductModal(page)
1010-	if err != nil {
1011-		return errors.Wrap(err, "等待商品弹窗失败")
--
1017-	for _, keyword := range products {
1018-		if err := searchAndSelectProduct(page, modal, keyword); err != nil {
1019-			slog.Warn("搜索选择商品失败", "keyword", keyword, "error", err)
1020-			failedProducts = append(failedProducts, keyword)
1021-		}
1022:		time.Sleep(500 * time.Millisecond)
1023-	}
1024-
1025-	// 点击保存按钮
1026-	slog.Info("准备点击保存按钮")
1027-	if err := clickModalSaveButton(page, modal); err != nil {
--
1039-	if len(failedProducts) > 0 {
1040-		return errors.Errorf("部分商品未找到: %v", failedProducts)
1041-	}
1042-
1043-	slog.Info("商品绑定完成", "total", len(products))
1044:	time.Sleep(1000 * time.Millisecond)
1045-	return nil
1046-}
1047-
1048-// clickAddProductButton 点击"添加商品"按钮
1049-func clickAddProductButton(page *hrod.Page) error {
--
1081-				if tag == "button" {
1082-					if err := parent.Click(proto.InputMouseButtonLeft, 1); err != nil {
1083-						return errors.Wrap(err, "点击添加商品按钮失败")
1084-					}
1085-					slog.Info("已点击添加商品按钮")
1086:					time.Sleep(300 * time.Millisecond) // 确保弹窗动画开始
1087-					return nil
1088-				}
1089-
1090-				cls, _ := parent.Attribute("class")
1091-				if cls != nil && strings.Contains(*cls, "d-button") {
1092-					if err := parent.Click(proto.InputMouseButtonLeft, 1); err != nil {
1093-						return errors.Wrap(err, "点击添加商品按钮失败")
1094-					}
1095-					slog.Info("已点击添加商品按钮")
1096:					time.Sleep(300 * time.Millisecond) // 确保弹窗动画开始
1097-					return nil
1098-				}
1099-			}
1100-		}
1101-	}
--
1114-			if visible {
1115-				slog.Info("商品选择弹窗已出现")
1116-				return modal, nil
1117-			}
1118-		}
1119:		time.Sleep(100 * time.Millisecond) // 缩短轮询间隔，更快响应
1120-	}
1121-
1122-	return nil, errors.New("等待商品选择弹窗超时")
1123-}
1124-
--
1134-
1135-	// 2. 清空并输入关键词（使用原生 JS setter + 完整事件）
1136-	if err := searchInput.SelectAllText(); err != nil {
1137-		slog.Warn("选择搜索框文本失败", "error", err)
1138-	}
1139:	time.Sleep(100 * time.Millisecond)
1140-
1141-	// 使用 rod Input 输入关键词
1142-	if err := searchInput.Input(keyword); err != nil {
1143-		return errors.Wrap(err, "输入搜索关键词失败")
1144-	}
1145:	time.Sleep(300 * time.Millisecond)
1146-
1147-	// 3. 触发搜索（模拟键盘 Enter）
1148-	if err := page.Actor().Keyboard.Press(input.Enter); err != nil {
1149-		return errors.Wrap(err, "触发搜索失败")
1150-	}
1151-
1152-	// 4. 等待搜索结果加载
1153:	time.Sleep(1 * time.Second)
1154-
1155-	// 等待 loading 消失（使用与工作代码相同的选择器）
1156-	deadline := time.Now().Add(10 * time.Second)
1157-	for time.Now().Before(deadline) {
1158-		loading, err := modal.Element(".goods-list-loading")
--
1161-		}
1162-		visible, _ := loading.Visible()
1163-		if !visible {
1164-			break
1165-		}
1166:		time.Sleep(100 * time.Millisecond)
1167-	}
1168-
1169-	// 等待商品列表渲染完成（使用与工作代码相同的选择器）
1170-	for time.Now().Before(deadline) {
1171-		productList, err := modal.Element(".goods-list-normal .good-card-container")
1172-		if err == nil && productList != nil {
1173-			break
1174-		}
1175:		time.Sleep(100 * time.Millisecond)
1176-	}
1177:	time.Sleep(500 * time.Millisecond) // 额外等待确保渲染完成
1178-
1179-	// 5. 点击第一个商品的 checkbox（使用与工作代码相同的选择器）
1180-	checkbox, err := modal.Element(".goods-list-normal .good-card-container .d-checkbox")
1181-	if err != nil {
1182-		return errors.Wrap(err, "未找到商品选择框")
--
1196-		return errors.Wrap(err, "点击商品选择框失败")
1197-	}
1198-
1199-	// 6. 随机延迟模拟人为操作（800-1500ms）
1200-	randomDelay := 800 + rand.Intn(700)
1201:	time.Sleep(time.Duration(randomDelay) * time.Millisecond)
1202-
1203-	slog.Info("已选择商品", "keyword", keyword)
1204-	return nil
1205-}
1206-
--
1242-		has, _, err := page.Has(".multi-goods-selector-modal")
1243-		if err != nil || !has {
1244-			slog.Info("弹窗已关闭")
1245-			return nil
1246-		}
1247:		time.Sleep(200 * time.Millisecond)
1248-	}
1249-
1250-	return errors.New("等待弹窗关闭超时")
1251-}

 succeeded in 134ms:
pkg/humanize/util.go:27:func sleepWithContext(ctx context.Context, d time.Duration) error {
handlers_api.go:126:func sleepWithContext(ctx context.Context, d time.Duration) error {

exec
/bin/bash -lc "nl -ba pkg/humanize/rod/hrod.go | sed -n '64,150p'; nl -ba pkg/humanize/humanize.go | sed -n '16,55p'; nl -ba pkg/humanize/mouse.go | sed -n '22,72p'; nl -ba pkg/humanize/keyboard.go | sed -n '14,62p'; nl -ba pkg/humanize/util.go | sed -n '17,39p'" in /tmp/xiaohongshu-mcp-fork
 succeeded in 0ms:
    64		}
    65		return b.wrapPage(p)
    66	}
    67	
    68	func (b *Browser) wrapPage(p *rod.Page) *Page {
    69		if p == nil {
    70			return nil
    71		}
    72		page := &Page{
    73			Rod:      p,
    74			Mouse:    p.Mouse,
    75			Keyboard: p.Keyboard,
    76			actor:    humanize.New(p, b.cfg),
    77			browser:  b,
    78			cfg:      b.cfg,
    79			ctx:      context.Background(),
    80		}
    81		// Eagerly initialize the cursor position so the first interaction does not
    82		// start from rod's default (0,0), which is an obvious automation signature.
    83		_ = page.actor.Mouse.InitPosition()
    84		return page
    85	}
    86	
    87	// Page wraps a *rod.Page and adds humanized versions of common actions.
    88	// Access the underlying *rod.Page through the exported Rod field when a method
    89	// is not explicitly wrapped.
    90	type Page struct {
    91		Rod      *rod.Page
    92		Mouse    *rod.Mouse
    93		Keyboard *rod.Keyboard
    94		actor    *humanize.Actor
    95		browser  *Browser
    96		cfg      humanize.Config
    97		ctx      context.Context
    98	}
    99	
   100	// Actor exposes the underlying humanize actor for advanced use.
   101	func (p *Page) Actor() *humanize.Actor {
   102		return p.actor
   103	}
   104	
   105	// Browser returns the wrapping humanized browser.
   106	func (p *Page) Browser() *Browser {
   107		return p.browser
   108	}
   109	
   110	func (p *Page) wrapPage(rp *rod.Page) *Page {
   111		if rp == nil {
   112			return nil
   113		}
   114		return &Page{
   115			Rod:      rp,
   116			Mouse:    rp.Mouse,
   117			Keyboard: rp.Keyboard,
   118			actor:    humanize.NewWithContext(rp, p.cfg, p.ctx),
   119			browser:  p.browser,
   120			cfg:      p.cfg,
   121			ctx:      p.ctx,
   122		}
   123	}
   124	
   125	// Close closes the page.
   126	func (p *Page) Close() error {
   127		return p.Rod.Close()
   128	}
   129	
   130	// Context returns a humanized clone with the specified context.
   131	func (p *Page) Context(ctx context.Context) *Page {
   132		page := p.wrapPage(p.Rod.Context(ctx))
   133		page.ctx = ctx
   134		page.actor.SetContext(ctx)
   135		return page
   136	}
   137	
   138	// Timeout returns a humanized clone with the specified timeout.
   139	func (p *Page) Timeout(d time.Duration) *Page {
   140		return p.wrapPage(p.Rod.Timeout(d))
   141	}
   142	
   143	// CancelTimeout returns a humanized clone with the timeout cancelled.
   144	func (p *Page) CancelTimeout() *Page {
   145		return p.wrapPage(p.Rod.CancelTimeout())
   146	}
   147	
   148	// Navigate navigates to the URL.
   149	func (p *Page) Navigate(url string) error {
   150		return p.Rod.Navigate(url)
    16	// Actor groups humanized mouse and keyboard actions.
    17	type Actor struct {
    18		Mouse    *Mouse
    19		Keyboard *Keyboard
    20		cfg      Config
    21	}
    22	
    23	// New creates a humanized actor for the given page.
    24	func New(page *rod.Page, cfg Config) *Actor {
    25		return NewWithContext(page, cfg, context.Background())
    26	}
    27	
    28	// NewWithContext creates a humanized actor for the given page and context.
    29	func NewWithContext(page *rod.Page, cfg Config, ctx context.Context) *Actor {
    30		mouse := NewMouse(page, cfg)
    31		actor := &Actor{
    32			Mouse:    mouse,
    33			Keyboard: NewKeyboard(page, cfg, mouse),
    34			cfg:      cfg,
    35		}
    36		actor.SetContext(ctx)
    37		return actor
    38	}
    39	
    40	// Config returns the actor's configuration.
    41	func (a *Actor) Config() Config {
    42		return a.cfg
    43	}
    44	
    45	// SetContext updates the context used by humanized delays.
    46	func (a *Actor) SetContext(ctx context.Context) {
    47		a.Mouse.setContext(ctx)
    48		a.Keyboard.setContext(ctx)
    49	}
    22	// Mouse provides human-like mouse operations.
    23	type Mouse struct {
    24		page        *rod.Page
    25		cfg         Config
    26		ctx         context.Context
    27		initialized bool
    28	}
    29	
    30	// NewMouse creates a new humanized mouse wrapper.
    31	func NewMouse(page *rod.Page, cfg Config) *Mouse {
    32		return &Mouse{page: page, cfg: cfg, ctx: context.Background()}
    33	}
    34	
    35	func (m *Mouse) setContext(ctx context.Context) {
    36		m.ctx = ctx
    37	}
    38	
    39	// initPosition moves the cursor from the rod default (0,0) to a plausible
    40	// starting point inside the viewport. This is done once per Mouse instance so
    41	// subsequent movements do not look like long flights from the screen corner.
    42	// The movement itself is humanized so the cursor does not teleport.
    43	func (m *Mouse) initPosition() error {
    44		if m.initialized {
    45			return nil
    46		}
    47		vp, err := m.viewport()
    48		if err != nil {
    49			return err
    50		}
    51		center := Point{
    52			X: vp.width/2 + (rand.Float64()*2-1)*vp.width*0.15,
    53			Y: vp.height/2 + (rand.Float64()*2-1)*vp.height*0.15,
    54		}
    55	
    56		// Mark initialized before calling moveTo to avoid recursion.
    57		m.initialized = true
    58		if err := m.moveTo(center, false); err != nil {
    59			m.initialized = false
    60			return err
    61		}
    62		return nil
    63	}
    64	
    65	// InitPosition eagerly moves the cursor from the rod default (0,0) to a
    66	// plausible starting point. Call this right after a page is created so the
    67	// first real interaction does not start from the detectable (0,0) origin.
    68	func (m *Mouse) InitPosition() error {
    69		return m.initPosition()
    70	}
    71	
    72	// Move moves the cursor to target with a realistic, non-deterministic path.
    14	// Keyboard provides human-like keyboard input.
    15	type Keyboard struct {
    16		page     *rod.Page
    17		cfg      Config
    18		ctx      context.Context
    19		mouse    *Mouse
    20		lastEl   *rod.Element
    21	}
    22	
    23	// NewKeyboard creates a new humanized keyboard wrapper.
    24	func NewKeyboard(page *rod.Page, cfg Config, mouse *Mouse) *Keyboard {
    25		return &Keyboard{page: page, cfg: cfg, ctx: context.Background(), mouse: mouse}
    26	}
    27	
    28	func (k *Keyboard) setContext(ctx context.Context) {
    29		k.ctx = ctx
    30	}
    31	
    32	// Type types text into el with realistic timing, occasional typos, and corrections.
    33	// ASCII characters are typed key-by-key; CJK and other non-keyboard characters
    34	// are inserted via simulated voice/IME composition events.
    35	func (k *Keyboard) Type(el *rod.Element, text string) error {
    36		// Ensure the element is rendered before typing, so the cursor lands on a
    37		// visible input area even when the page is long.
    38		if k.mouse == nil {
    39			if err := el.ScrollIntoView(); err != nil {
    40				return err
    41			}
    42		} else if k.lastEl == el {
    43			if err := k.mouse.ScrollIntoView(el); err != nil {
    44				return err
    45			}
    46		}
    47	
    48		// Move the cursor onto the element and click it, just like a human would
    49		// before typing. Skip the click if we just typed into the same element to
    50		// avoid repeated cursor jumps during continuous input (e.g. typing tags
    51		// char by char). This also keeps the mouse position continuous between
    52		// actions without querying DOM state that a page could detect.
    53		if k.mouse != nil && k.lastEl != el {
    54			if err := k.mouse.Click(el); err != nil {
    55				return err
    56			}
    57		}
    58		k.lastEl = el
    59	
    60		if err := el.Focus(); err != nil {
    61			return err
    62		}
    17	// Sleep pauses for a random short duration, useful between operations.
    18	func Sleep(min, max time.Duration) {
    19		_ = SleepContext(context.Background(), min, max)
    20	}
    21	
    22	// SleepContext pauses for a random short duration unless ctx is cancelled.
    23	func SleepContext(ctx context.Context, min, max time.Duration) error {
    24		return sleepWithContext(ctx, randDuration(min, max))
    25	}
    26	
    27	func sleepWithContext(ctx context.Context, d time.Duration) error {
    28		timer := time.NewTimer(d)
    29		defer timer.Stop()
    30	
    31		select {
    32		case <-ctx.Done():
    33			return ctx.Err()
    34		case <-timer.C:
    35			return nil
    36		}
    37	}

exec
/bin/bash -lc "for f in xiaohongshu/{publish.go,comment_feed.go,publish_video.go,feed_detail.go,like_favorite.go,login.go,feeds.go}; do printf '%s ' \""'$f"; rg -c '"'time\\.Sleep' \""'$f"; done; rg -n '"'time\\.Sleep' xiaohongshu/{publish.go,comment_feed.go,publish_video.go,feed_detail.go,like_favorite.go,login.go,feeds.go} | wc -l; rg -n 'sleepRandom\\(' xiaohongshu/feed_detail.go | wc -l; rg -n 'time\\.Sleep' xiaohongshu/feed_detail.go" in /tmp/xiaohongshu-mcp-fork
 succeeded in 184ms:
5
xiaohongshu/login.go 3
xiaohongshu/feeds.go 1
81
13
186:		time.Sleep(scrollInterval)
331:		time.Sleep(time.Duration(minMs) * time.Millisecond)
335:	time.Sleep(delay)
550:	time.Sleep(500 * time.Millisecond)
748:	time.Sleep(500 * time.Millisecond)

exec
/bin/bash -lc "rg -n 'time\\.Sleep' xiaohongshu --glob '*.go'; rg -n '"'^func .*context'"\\.Context' xiaohongshu --glob '*.go'; rg -n 'sleepRandom\\(' xiaohongshu --glob '*.go'" in /tmp/xiaohongshu-mcp-fork
exec
/bin/bash -lc "nl -ba pkg/humanize/rod/hrod.go | sed -n '320,390p'; rg -n 'func \\(el \\*Element\\) Page|func NewElement|func newElement' pkg/humanize/rod/hrod.go; nl -ba pkg/humanize/rod/hrod.go | sed -n '340,590p'" in /tmp/xiaohongshu-mcp-fork
 succeeded in 0ms:
   320	}
   321	
   322	func (p *Page) wrapElement(el *rod.Element) *Element {
   323		if el == nil {
   324			return nil
   325		}
   326		return newElement(el, p.actor, p.browser)
   327	}
   328	
   329	// NewElement creates a humanized element from a raw *rod.Element.
   330	func NewElement(el *rod.Element, actor *humanize.Actor) *Element {
   331		return newElement(el, actor, nil)
   332	}
   333	
   334	func newElement(el *rod.Element, actor *humanize.Actor, browser *Browser) *Element {
   335		if el == nil {
   336			return nil
   337		}
   338		return &Element{Rod: el, actor: actor, browser: browser}
   339	}
   340	
   341	// Element wraps a *rod.Element and adds humanized Click/Input methods.
   342	// Access the underlying *rod.Element through the exported Rod field when a method
   343	// is not explicitly wrapped.
   344	type Element struct {
   345		Rod     *rod.Element
   346		actor   *humanize.Actor
   347		browser *Browser
   348	}
   349	
   350	// Actor exposes the underlying humanize actor for advanced use.
   351	func (el *Element) Actor() *humanize.Actor {
   352		return el.actor
   353	}
   354	
   355	// Page returns the wrapping humanized page.
   356	func (el *Element) Page() *Page {
   357		rp := el.Rod.Page()
   358		return &Page{
   359			Rod:      rp,
   360			Mouse:    rp.Mouse,
   361			Keyboard: rp.Keyboard,
   362			actor:    el.actor,
   363			browser:  el.browser,
   364			cfg:      el.actor.Config(),
   365		}
   366	}
   367	
   368	// Click performs a human-like click.
   369	func (el *Element) Click(button proto.InputMouseButton, clickCount int) error {
   370		return el.actor.Mouse.ClickWithOptions(el.Rod, button, clickCount)
   371	}
   372	
   373	// ClickNoScroll performs a human-like click without scrolling the element into
   374	// view first. Useful for sticky/fixed elements that are already visible.
   375	func (el *Element) ClickNoScroll() error {
   376		return el.actor.Mouse.ClickNoScroll(el.Rod)
   377	}
   378	
   379	// ClickPoint moves to a viewport-relative point and clicks there.
   380	func (p *Page) ClickPoint(point proto.Point) error {
   381		return p.actor.Mouse.ClickPoint(point)
   382	}
   383	
   384	// MovePoint moves to a viewport-relative point.
   385	func (p *Page) MovePoint(point proto.Point) error {
   386		return p.actor.Mouse.MovePoint(point)
   387	}
   388	
   389	// MustClick is the humanized MustClick.
   390	// It panics if the element cannot be clicked.
330:func NewElement(el *rod.Element, actor *humanize.Actor) *Element {
334:func newElement(el *rod.Element, actor *humanize.Actor, browser *Browser) *Element {
356:func (el *Element) Page() *Page {
   340	
   341	// Element wraps a *rod.Element and adds humanized Click/Input methods.
   342	// Access the underlying *rod.Element through the exported Rod field when a method
   343	// is not explicitly wrapped.
   344	type Element struct {
   345		Rod     *rod.Element
   346		actor   *humanize.Actor
   347		browser *Browser
   348	}
   349	
   350	// Actor exposes the underlying humanize actor for advanced use.
   351	func (el *Element) Actor() *humanize.Actor {
   352		return el.actor
   353	}
   354	
   355	// Page returns the wrapping humanized page.
   356	func (el *Element) Page() *Page {
   357		rp := el.Rod.Page()
   358		return &Page{
   359			Rod:      rp,
   360			Mouse:    rp.Mouse,
   361			Keyboard: rp.Keyboard,
   362			actor:    el.actor,
   363			browser:  el.browser,
   364			cfg:      el.actor.Config(),
   365		}
   366	}
   367	
   368	// Click performs a human-like click.
   369	func (el *Element) Click(button proto.InputMouseButton, clickCount int) error {
   370		return el.actor.Mouse.ClickWithOptions(el.Rod, button, clickCount)
   371	}
   372	
   373	// ClickNoScroll performs a human-like click without scrolling the element into
   374	// view first. Useful for sticky/fixed elements that are already visible.
   375	func (el *Element) ClickNoScroll() error {
   376		return el.actor.Mouse.ClickNoScroll(el.Rod)
   377	}
   378	
   379	// ClickPoint moves to a viewport-relative point and clicks there.
   380	func (p *Page) ClickPoint(point proto.Point) error {
   381		return p.actor.Mouse.ClickPoint(point)
   382	}
   383	
   384	// MovePoint moves to a viewport-relative point.
   385	func (p *Page) MovePoint(point proto.Point) error {
   386		return p.actor.Mouse.MovePoint(point)
   387	}
   388	
   389	// MustClick is the humanized MustClick.
   390	// It panics if the element cannot be clicked.
   391	func (el *Element) MustClick() *Element {
   392		if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
   393			panic(err)
   394		}
   395		return el
   396	}
   397	
   398	// Input performs human-like typing.
   399	func (el *Element) Input(text string) error {
   400		return el.actor.Keyboard.Type(el.Rod, text)
   401	}
   402	
   403	// MustInput is the humanized MustInput.
   404	// It panics if the text cannot be entered.
   405	func (el *Element) MustInput(text string) *Element {
   406		if err := el.Input(text); err != nil {
   407			panic(err)
   408		}
   409		return el
   410	}
   411	
   412	// Hover moves the cursor over the element in a human-like way.
   413	func (el *Element) Hover() error {
   414		return el.actor.Mouse.Hover(el.Rod)
   415	}
   416	
   417	// MustHover is the humanized MustHover.
   418	// It panics if the cursor cannot be moved over the element.
   419	func (el *Element) MustHover() *Element {
   420		if err := el.Hover(); err != nil {
   421			panic(err)
   422		}
   423		return el
   424	}
   425	
   426	// ScrollIntoView scrolls the element into view in a human-like way by
   427	// dispatching wheel events until the element is centered in the viewport.
   428	func (el *Element) ScrollIntoView() error {
   429		return el.actor.Mouse.ScrollIntoView(el.Rod)
   430	}
   431	
   432	// MustScrollIntoView is the humanized MustScrollIntoView.
   433	// It panics if the element cannot be scrolled into view.
   434	func (el *Element) MustScrollIntoView() *Element {
   435		if err := el.ScrollIntoView(); err != nil {
   436			panic(err)
   437		}
   438		return el
   439	}
   440	
   441	// Element finds a child element and returns a humanized wrapper.
   442	func (el *Element) Element(selector string) (*Element, error) {
   443		child, err := el.Rod.Element(selector)
   444		if err != nil {
   445			return nil, err
   446		}
   447		return newElement(child, el.actor, el.browser), nil
   448	}
   449	
   450	// MustElement finds a child element and returns a humanized wrapper.
   451	func (el *Element) MustElement(selector string) *Element {
   452		return newElement(el.Rod.MustElement(selector), el.actor, el.browser)
   453	}
   454	
   455	// ElementR finds a child element by regex and returns a humanized wrapper.
   456	func (el *Element) ElementR(selector, regex string) (*Element, error) {
   457		child, err := el.Rod.ElementR(selector, regex)
   458		if err != nil {
   459			return nil, err
   460		}
   461		return newElement(child, el.actor, el.browser), nil
   462	}
   463	
   464	// MustElementR finds a child element by regex and returns a humanized wrapper.
   465	func (el *Element) MustElementR(selector, regex string) *Element {
   466		return newElement(el.Rod.MustElementR(selector, regex), el.actor, el.browser)
   467	}
   468	
   469	// ElementX finds a child element by XPath and returns a humanized wrapper.
   470	func (el *Element) ElementX(xpath string) (*Element, error) {
   471		child, err := el.Rod.ElementX(xpath)
   472		if err != nil {
   473			return nil, err
   474		}
   475		return newElement(child, el.actor, el.browser), nil
   476	}
   477	
   478	// MustElementX finds a child element by XPath and returns a humanized wrapper.
   479	func (el *Element) MustElementX(xpath string) *Element {
   480		return newElement(el.Rod.MustElementX(xpath), el.actor, el.browser)
   481	}
   482	
   483	// Elements returns humanized child elements.
   484	func (el *Element) Elements(selector string) ([]*Element, error) {
   485		children, err := el.Rod.Elements(selector)
   486		if err != nil {
   487			return nil, err
   488		}
   489		result := make([]*Element, len(children))
   490		for i, child := range children {
   491			result[i] = newElement(child, el.actor, el.browser)
   492		}
   493		return result, nil
   494	}
   495	
   496	// MustElements returns humanized child elements.
   497	func (el *Element) MustElements(selector string) []*Element {
   498		children := el.Rod.MustElements(selector)
   499		result := make([]*Element, len(children))
   500		for i, child := range children {
   501			result[i] = newElement(child, el.actor, el.browser)
   502		}
   503		return result
   504	}
   505	
   506	// Parent returns the humanized parent element.
   507	func (el *Element) Parent() (*Element, error) {
   508		p, err := el.Rod.Parent()
   509		if err != nil {
   510			return nil, err
   511		}
   512		return newElement(p, el.actor, el.browser), nil
   513	}
   514	
   515	// Next returns the humanized next sibling element.
   516	func (el *Element) Next() (*Element, error) {
   517		next, err := el.Rod.Next()
   518		if err != nil {
   519			return nil, err
   520		}
   521		return newElement(next, el.actor, el.browser), nil
   522	}
   523	
   524	// Previous returns the humanized previous sibling element.
   525	func (el *Element) Previous() (*Element, error) {
   526		prev, err := el.Rod.Previous()
   527		if err != nil {
   528			return nil, err
   529		}
   530		return newElement(prev, el.actor, el.browser), nil
   531	}
   532	
   533	// Attribute returns the value of an attribute.
   534	func (el *Element) Attribute(name string) (*string, error) {
   535		return el.Rod.Attribute(name)
   536	}
   537	
   538	// MustAttribute is the humanized MustAttribute.
   539	func (el *Element) MustAttribute(name string) *string {
   540		return el.Rod.MustAttribute(name)
   541	}
   542	
   543	// Text returns the element text.
   544	func (el *Element) Text() (string, error) {
   545		return el.Rod.Text()
   546	}
   547	
   548	// MustText is the humanized MustText.
   549	func (el *Element) MustText() string {
   550		return el.Rod.MustText()
   551	}
   552	
   553	// Visible returns whether the element is visible.
   554	func (el *Element) Visible() (bool, error) {
   555		return el.Rod.Visible()
   556	}
   557	
   558	// MustVisible is the humanized MustVisible.
   559	func (el *Element) MustVisible() bool {
   560		return el.Rod.MustVisible()
   561	}
   562	
   563	// WaitVisible waits for the element to become visible.
   564	func (el *Element) WaitVisible() error {
   565		return el.Rod.WaitVisible()
   566	}
   567	
   568	// MustWaitVisible is the humanized MustWaitVisible.
   569	func (el *Element) MustWaitVisible() *Element {
   570		el.Rod.MustWaitVisible()
   571		return el
   572	}
   573	
   574	// Eval evaluates JS on the element.
   575	func (el *Element) Eval(js string, params ...interface{}) (*proto.RuntimeRemoteObject, error) {
   576		return el.Rod.Eval(js, params...)
   577	}
   578	
   579	// MustEval is the humanized MustEval.
   580	func (el *Element) MustEval(js string, params ...interface{}) gson.JSON {
   581		return el.Rod.MustEval(js, params...)
   582	}
   583	
   584	// Shape returns the element shape.
   585	func (el *Element) Shape() (*proto.DOMGetContentQuadsResult, error) {
   586		return el.Rod.Shape()
   587	}
   588	
   589	// KeyActions returns key actions for the element.
   590	func (el *Element) KeyActions() (*rod.KeyActions, error) {

 succeeded in 102ms:
codex
# 遗留 P2 修复方案

## 问题 1：`time.Sleep` 不响应 `context` 取消

### 根因分析

`xiaohongshu/` 的请求入口普遍已接收 `ctx context.Context`，且 `service.go` 在创建 action 时已将请求上下文绑定到页面，例如：

```go
action := xiaohongshu.NewCommentFeedAction(page.Context(ctx))
```

但业务代码仍直接调用 `time.Sleep`。`time.Sleep` 只能等待固定时长；请求取消后，goroutine 会继续阻塞至等待结束，随后还可能继续执行下一步浏览器操作。

已存在的复用基础：

- [`pkg/humanize/util.go:22`](/tmp/xiaohongshu-mcp-fork/pkg/humanize/util.go:22) 的 `SleepContext` 最终使用私有 `sleepWithContext`，通过 `timer + select { case <-ctx.Done() }` 立即响应取消。
- [`xiaohongshu/feed_detail.go:329`](/tmp/xiaohongshu-mcp-fork/xiaohongshu/feed_detail.go:329) 有 `sleepRandom()`，但内部仍是 `time.Sleep`。
- [`pkg/humanize/rod/hrod.go:97`](/tmp/xiaohongshu-mcp-fork/pkg/humanize/rod/hrod.go:97) 的 `Page` 已保存 `ctx`，适合作为无需向各私有 helper 透传 `ctx` 的载体。

### 现状盘点

已拥有 `ctx context.Context` 的函数（限定题目所列七个文件）：

| 文件 | 函数 |
|---|---|
| `publish.go` | `(*PublishAction).Publish` |
| `comment_feed.go` | `(*CommentFeedAction).PostComment`、`ReplyToComment` |
| `publish_video.go` | `(*PublishAction).PublishVideo` |
| `feed_detail.go` | `(*FeedDetailAction).GetFeedDetail`、`GetFeedDetailWithConfig` |
| `like_favorite.go` | `preparePage`、`Like`、`Unlike`、两处 `perform`、`Favorite`、`Unfavorite` |
| `login.go` | `CheckLoginStatus`、`Login`、`FetchQrcodeImage`、`WaitForLogin` |
| `feeds.go` | `GetFeedsList` |

`time.Sleep` 分布：

| 文件 | 直接 `time.Sleep` 数量 | 主要场景 |
|---|---:|---|
| `publish.go` | 48 | 发布页稳定、图片上传、标签输入、发布按钮轮询、商品弹窗轮询 |
| `comment_feed.go` | 13 | 评论/回复后的等待、评论滚动查找 |
| `publish_video.go` | 6 | 发布页切换、视频发布表单 |
| `feed_detail.go` | 5 | 评论加载循环、滚动稳定、可访问性检查；另有 12 处 `sleepRandom()` 调用 |
| `like_favorite.go` | 5 | 页面加载、点赞/收藏状态确认 |
| `login.go` | 3 | 页面与二维码加载 |
| `feeds.go` | 1 | 首页状态等待 |
| **合计** | **81** | — |

`WaitForLogin` 使用 `ticker + select(ctx.Done())`，本身已正确响应取消，无需改动。

### 最小侵入方案

不修改现有 action、业务函数和私有 helper 的签名；将“从页面取得上下文并等待”的能力放到 `hrod.Page`，调用者已有 `page` 时直接使用它。

#### 1. 在 `hrod.Page` 增加上下文等待 API

修改 [`pkg/humanize/rod/hrod.go`](/tmp/xiaohongshu-mcp-fork/pkg/humanize/rod/hrod.go:139)，紧邻 `Timeout`/`CancelTimeout`：

```go
// Sleep waits for d, or returns immediately when this page's context is cancelled.
func (p *Page) Sleep(d time.Duration) error {
	return humanize.SleepContext(p.ctx, d, d)
}

// SleepRandom waits for a random duration in [min, max], or returns when cancelled.
func (p *Page) SleepRandom(min, max time.Duration) error {
	return humanize.SleepContext(p.ctx, min, max)
}
```

`humanize.SleepContext` 已公开；无需导出或复制 [`sleepWithContext`](/tmp/xiaohongshu-mcp-fork/pkg/humanize/util.go:27)。

#### 2. 为仅持有 `*hrod.Element` 的 helper 补一个等待入口

`publish.go` 的 `waitAndClickTitleInput`、`inputTags`、`inputTag` 只接收 `*hrod.Element`。不要通过 `Element.Page()` 取上下文：其当前构造结果未复制 `Page.ctx`。

修改 [`pkg/humanize/humanize.go:17`](/tmp/xiaohongshu-mcp-fork/pkg/humanize/humanize.go:17)，为 `Actor` 保存上下文并提供等待方法：

```go
type Actor struct {
	Mouse    *Mouse
	Keyboard *Keyboard
	cfg      Config
	ctx      context.Context
}

func (a *Actor) SetContext(ctx context.Context) {
	a.ctx = ctx
	a.Mouse.setContext(ctx)
	a.Keyboard.setContext(ctx)
}

func (a *Actor) Sleep(d time.Duration) error {
	return sleepWithContext(a.ctx, d)
}
```

修改 [`pkg/humanize/rod/hrod.go:351`](/tmp/xiaohongshu-mcp-fork/pkg/humanize/rod/hrod.go:351)：

```go
func (el *Element) Sleep(d time.Duration) error {
	return el.actor.Sleep(d)
}
```

这样仍复用现有 `sleepWithContext` 的实现，不需要给 `inputTag` 等函数增加 `ctx` 参数。

#### 3. 机械替换全部业务等待点，并传播取消错误

普通的固定延迟：

```go
// 修改前
time.Sleep(1 * time.Second)

// 修改后：当前函数可返回 error
if err := page.Sleep(time.Second); err != nil {
	return err
}
```

有业务错误包装的入口可保留语义：

```go
if err := page.Sleep(time.Second); err != nil {
	return errors.Wrap(err, "等待发布页稳定时请求已取消")
}
```

仅有 `Element` 的函数：

```go
if err := contentElem.Sleep(50 * time.Millisecond); err != nil {
	return errors.Wrap(err, "输入标签时请求已取消")
}
```

轮询循环必须在每次等待处返回取消错误，而不是 `continue`：

```go
// 修改前
time.Sleep(interval)
continue

// 修改后
if err := page.Sleep(interval); err != nil {
	return nil, err
}
continue
```

涉及文件及主要修改位置：

- [`xiaohongshu/publish.go:53`](/tmp/xiaohongshu-mcp-fork/xiaohongshu/publish.go:53) 至 [`1247`](/tmp/xiaohongshu-mcp-fork/xiaohongshu/publish.go:1247)：替换全部 48 处；尤其覆盖上传轮询、发布按钮轮询、商品弹窗轮询。
- [`xiaohongshu/comment_feed.go:34`](/tmp/xiaohongshu-mcp-fork/xiaohongshu/comment_feed.go:34) 至 [`269`](/tmp/xiaohongshu-mcp-fork/xiaohongshu/comment_feed.go:269)：替换 13 处；`findCommentElement` 可直接返回 `(nil, err)`。
- [`xiaohongshu/publish_video.go:37`](/tmp/xiaohongshu-mcp-fork/xiaohongshu/publish_video.go:37) 至 [`152`](/tmp/xiaohongshu-mcp-fork/xiaohongshu/publish_video.go:152)：替换 6 处。
- [`xiaohongshu/feed_detail.go:105`](/tmp/xiaohongshu-mcp-fork/xiaohongshu/feed_detail.go:105) 至 [`748`](/tmp/xiaohongshu-mcp-fork/xiaohongshu/feed_detail.go:748)：替换固定等待，并将 12 处 `sleepRandom()` 改为 `page.SleepRandom(...)` 或新增的私有 `sleepRandomContext(page, minMs, maxMs)`。
- [`xiaohongshu/like_favorite.go:53`](/tmp/xiaohongshu-mcp-fork/xiaohongshu/like_favorite.go:53) 至 [`200`](/tmp/xiaohongshu-mcp-fork/xiaohongshu/like_favorite.go:200)：替换 5 处。
- [`xiaohongshu/login.go:23`](/tmp/xiaohongshu-mcp-fork/xiaohongshu/login.go:23)、[`44`](/tmp/xiaohongshu-mcp-fork/xiaohongshu/login.go:44)、[`66`](/tmp/xiaohongshu-mcp-fork/xiaohongshu/login.go:66)：替换 3 处。
- [`xiaohongshu/feeds.go:30`](/tmp/xiaohongshu-mcp-fork/xiaohongshu/feeds.go:30)：替换 1 处。

对于返回 `bool` 或无返回值的内部 helper，不修改签名：

```go
if err := page.Sleep(...); err != nil {
	return
}
```

其外层的 `error` 返回函数在下一处可中断等待或 Rod 操作处返回 `ctx.Err()`。评论加载的 `commentLoader.load()` 则应在调用这类无返回值 helper 后立即检查并返回页面上下文错误，避免把取消误报为正常完成。

```go
if err := cl.page.Sleep(0); err != nil {
	return err
}
```

更清晰的实现是为 `Page` 增加 `Err() error { return p.ctx.Err() }`，然后写为 `if err := cl.page.Err(); err != nil { return err }`。

### 风险评估

- 低风险：等待时间与随机范围保持不变，只改变取消时的退出时机。
- 需要完整替换：漏掉任意轮询中的 `time.Sleep`，该路径仍会延迟取消；提交前应以 `rg 'time\.Sleep' xiaohongshu/...` 验证范围内归零。
- `Must*` 系列在 Rod 因取消返回错误时可能 panic；本项优先解决 sleep 阻塞，不应在同一个 P2 中顺带将所有 `Must*` 改为错误返回 API。
- `CommentFeedAction` 当前注释称不继承外部超时，但 action 创建时已在 `service.go` 传入 `page.Context(ctx)`。本方案保留其 60 秒 Rod 超时策略，同时允许调用方取消请求。

### 优先级建议

**P2，建议优先处理。** 单请求可因 `publish.go` 的 3 秒等待、视频发布或多轮评论加载而在取消后持续占用浏览器页面与 goroutine；改动机械、可集中验证。

---

## 问题 2：`Page.wrapPage()` 重建 `Actor`，丢失鼠标和键盘状态

### 根因分析

[`Browser.wrapPage()`](/tmp/xiaohongshu-mcp-fork/pkg/humanize/rod/hrod.go:68) 仅在真实新页面创建时构造 `Actor`，并调用一次：

```go
_ = page.actor.Mouse.InitPosition()
```

这是正确的初始化边界。

但 [`Page.wrapPage()`](/tmp/xiaohongshu-mcp-fork/pkg/humanize/rod/hrod.go:110) 被 `Context()`、`Timeout()`、`CancelTimeout()` 用于构造同一底层页面的 Rod 派生视图时，却每次调用：

```go
actor: humanize.NewWithContext(rp, p.cfg, p.ctx),
```

因此每次派生页面都会生成新的：

- `Mouse`：[`initialized`](/tmp/xiaohongshu-mcp-fork/pkg/humanize/mouse.go:27) 回到 `false`，下一次移动可能再次执行初始定位；
- `Keyboard`：[`lastEl`](/tmp/xiaohongshu-mcp-fork/pkg/humanize/keyboard.go:20) 被清空，连续输入被误认为新的输入目标，可能触发额外点击和鼠标移动；
- `Actor` 级上下文绑定也被不必要地反复重建。

`Context()` 已经在 [`hrod.go:131`](/tmp/xiaohongshu-mcp-fork/pkg/humanize/rod/hrod.go:131) 明确调用 `actor.SetContext(ctx)`，说明正确的模型应是“复用 actor，仅更新 context”，而不是“新建 actor”。

### 修复方案

修改 [`pkg/humanize/rod/hrod.go:110`](/tmp/xiaohongshu-mcp-fork/pkg/humanize/rod/hrod.go:110)，使派生页面共享父页面的 `Actor`：

```go
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
```

保持 [`Page.Context()`](/tmp/xiaohongshu-mcp-fork/pkg/humanize/rod/hrod.go:131) 的显式上下文更新；仅增加 nil 防御即可：

```go
func (p *Page) Context(ctx context.Context) *Page {
	page := p.wrapPage(p.Rod.Context(ctx))
	if page == nil {
		return nil
	}
	page.ctx = ctx
	page.actor.SetContext(ctx)
	return page
}
```

`Timeout()` 和 `CancelTimeout()` 保持派生上下文 `p.ctx`，不创建新 actor：

```go
func (p *Page) Timeout(d time.Duration) *Page {
	return p.wrapPage(p.Rod.Timeout(d))
}

func (p *Page) CancelTimeout() *Page {
	return p.wrapPage(p.Rod.CancelTimeout())
}
```

结果关系：

```text
Browser.NewPage()
  └─ Browser.wrapPage(): 创建 Actor，InitPosition 一次

Page.Context / Timeout / CancelTimeout
  └─ Page.wrapPage(): 复用同一 Actor
       ├─ Mouse.initialized 保留
       ├─ Keyboard.lastEl 保留
       └─ Context() 仅调用 Actor.SetContext(ctx)
```

### 验证建议

新增 `pkg/humanize/rod` 单元测试，至少覆盖：

```go
func TestPageWrapPageReusesActor(t *testing.T) {
	parent := newTestPage(...)
	child := parent.wrapPage(parent.Rod.Timeout(time.Second))

	require.Same(t, parent.actor, child.actor)
	require.Same(t, parent.Actor().Mouse, child.Actor().Mouse)
	require.Same(t, parent.Actor().Keyboard, child.Actor().Keyboard)
}
```

再增加行为级测试：

1. 初始化页面后记录 `Mouse` 实例；
2. 连续调用 `Context(ctx).Timeout(...)`；
3. 断言仍是同一 `Mouse`/`Keyboard`；
4. 调用 `Context(cancelledCtx)` 后，断言人化延迟返回 `context.Canceled`。

### 风险评估

- 低风险：`Context`、`Timeout`、`CancelTimeout` 派生的是同一浏览器页面，本就应共享输入设备状态。
- 需要明确并发约束：派生 `Page` 共享可变 `Actor`，同一底层页面不能被多个 goroutine 以不同 context 并发操作。浏览器页面当前也不应被并发复用；应在 `Page` 注释或 browser manager 的使用约束中明确这一点。
- 不应在 `Page.wrapPage()` 中重复调用 `InitPosition()`；首次真实页面创建时的 [`Browser.wrapPage():83`](/tmp/xiaohongshu-mcp-fork/pkg/humanize/rod/hrod.go:83) 已是唯一正确位置。

### 优先级建议

**P2，建议与问题 1 同批提交，且先完成本项。** 问题 1 依赖 `Page.Context(ctx)` 传递页面级取消语义；先保证派生页面保持同一 actor 状态，可避免修复等待取消后又引入鼠标重定位和键盘状态丢失。
tokens used
108,619
# 遗留 P2 修复方案

## 问题 1：`time.Sleep` 不响应 `context` 取消

### 根因分析

`xiaohongshu/` 的请求入口普遍已接收 `ctx context.Context`，且 `service.go` 在创建 action 时已将请求上下文绑定到页面，例如：

```go
action := xiaohongshu.NewCommentFeedAction(page.Context(ctx))
```

但业务代码仍直接调用 `time.Sleep`。`time.Sleep` 只能等待固定时长；请求取消后，goroutine 会继续阻塞至等待结束，随后还可能继续执行下一步浏览器操作。

已存在的复用基础：

- [`pkg/humanize/util.go:22`](/tmp/xiaohongshu-mcp-fork/pkg/humanize/util.go:22) 的 `SleepContext` 最终使用私有 `sleepWithContext`，通过 `timer + select { case <-ctx.Done() }` 立即响应取消。
- [`xiaohongshu/feed_detail.go:329`](/tmp/xiaohongshu-mcp-fork/xiaohongshu/feed_detail.go:329) 有 `sleepRandom()`，但内部仍是 `time.Sleep`。
- [`pkg/humanize/rod/hrod.go:97`](/tmp/xiaohongshu-mcp-fork/pkg/humanize/rod/hrod.go:97) 的 `Page` 已保存 `ctx`，适合作为无需向各私有 helper 透传 `ctx` 的载体。

### 现状盘点

已拥有 `ctx context.Context` 的函数（限定题目所列七个文件）：

| 文件 | 函数 |
|---|---|
| `publish.go` | `(*PublishAction).Publish` |
| `comment_feed.go` | `(*CommentFeedAction).PostComment`、`ReplyToComment` |
| `publish_video.go` | `(*PublishAction).PublishVideo` |
| `feed_detail.go` | `(*FeedDetailAction).GetFeedDetail`、`GetFeedDetailWithConfig` |
| `like_favorite.go` | `preparePage`、`Like`、`Unlike`、两处 `perform`、`Favorite`、`Unfavorite` |
| `login.go` | `CheckLoginStatus`、`Login`、`FetchQrcodeImage`、`WaitForLogin` |
| `feeds.go` | `GetFeedsList` |

`time.Sleep` 分布：

| 文件 | 直接 `time.Sleep` 数量 | 主要场景 |
|---|---:|---|
| `publish.go` | 48 | 发布页稳定、图片上传、标签输入、发布按钮轮询、商品弹窗轮询 |
| `comment_feed.go` | 13 | 评论/回复后的等待、评论滚动查找 |
| `publish_video.go` | 6 | 发布页切换、视频发布表单 |
| `feed_detail.go` | 5 | 评论加载循环、滚动稳定、可访问性检查；另有 12 处 `sleepRandom()` 调用 |
| `like_favorite.go` | 5 | 页面加载、点赞/收藏状态确认 |
| `login.go` | 3 | 页面与二维码加载 |
| `feeds.go` | 1 | 首页状态等待 |
| **合计** | **81** | — |

`WaitForLogin` 使用 `ticker + select(ctx.Done())`，本身已正确响应取消，无需改动。

### 最小侵入方案

不修改现有 action、业务函数和私有 helper 的签名；将“从页面取得上下文并等待”的能力放到 `hrod.Page`，调用者已有 `page` 时直接使用它。

#### 1. 在 `hrod.Page` 增加上下文等待 API

修改 [`pkg/humanize/rod/hrod.go`](/tmp/xiaohongshu-mcp-fork/pkg/humanize/rod/hrod.go:139)，紧邻 `Timeout`/`CancelTimeout`：

```go
// Sleep waits for d, or returns immediately when this page's context is cancelled.
func (p *Page) Sleep(d time.Duration) error {
	return humanize.SleepContext(p.ctx, d, d)
}

// SleepRandom waits for a random duration in [min, max], or returns when cancelled.
func (p *Page) SleepRandom(min, max time.Duration) error {
	return humanize.SleepContext(p.ctx, min, max)
}
```

`humanize.SleepContext` 已公开；无需导出或复制 [`sleepWithContext`](/tmp/xiaohongshu-mcp-fork/pkg/humanize/util.go:27)。

#### 2. 为仅持有 `*hrod.Element` 的 helper 补一个等待入口

`publish.go` 的 `waitAndClickTitleInput`、`inputTags`、`inputTag` 只接收 `*hrod.Element`。不要通过 `Element.Page()` 取上下文：其当前构造结果未复制 `Page.ctx`。

修改 [`pkg/humanize/humanize.go:17`](/tmp/xiaohongshu-mcp-fork/pkg/humanize/humanize.go:17)，为 `Actor` 保存上下文并提供等待方法：

```go
type Actor struct {
	Mouse    *Mouse
	Keyboard *Keyboard
	cfg      Config
	ctx      context.Context
}

func (a *Actor) SetContext(ctx context.Context) {
	a.ctx = ctx
	a.Mouse.setContext(ctx)
	a.Keyboard.setContext(ctx)
}

func (a *Actor) Sleep(d time.Duration) error {
	return sleepWithContext(a.ctx, d)
}
```

修改 [`pkg/humanize/rod/hrod.go:351`](/tmp/xiaohongshu-mcp-fork/pkg/humanize/rod/hrod.go:351)：

```go
func (el *Element) Sleep(d time.Duration) error {
	return el.actor.Sleep(d)
}
```

这样仍复用现有 `sleepWithContext` 的实现，不需要给 `inputTag` 等函数增加 `ctx` 参数。

#### 3. 机械替换全部业务等待点，并传播取消错误

普通的固定延迟：

```go
// 修改前
time.Sleep(1 * time.Second)

// 修改后：当前函数可返回 error
if err := page.Sleep(time.Second); err != nil {
	return err
}
```

有业务错误包装的入口可保留语义：

```go
if err := page.Sleep(time.Second); err != nil {
	return errors.Wrap(err, "等待发布页稳定时请求已取消")
}
```

仅有 `Element` 的函数：

```go
if err := contentElem.Sleep(50 * time.Millisecond); err != nil {
	return errors.Wrap(err, "输入标签时请求已取消")
}
```

轮询循环必须在每次等待处返回取消错误，而不是 `continue`：

```go
// 修改前
time.Sleep(interval)
continue

// 修改后
if err := page.Sleep(interval); err != nil {
	return nil, err
}
continue
```

涉及文件及主要修改位置：

- [`xiaohongshu/publish.go:53`](/tmp/xiaohongshu-mcp-fork/xiaohongshu/publish.go:53) 至 [`1247`](/tmp/xiaohongshu-mcp-fork/xiaohongshu/publish.go:1247)：替换全部 48 处；尤其覆盖上传轮询、发布按钮轮询、商品弹窗轮询。
- [`xiaohongshu/comment_feed.go:34`](/tmp/xiaohongshu-mcp-fork/xiaohongshu/comment_feed.go:34) 至 [`269`](/tmp/xiaohongshu-mcp-fork/xiaohongshu/comment_feed.go:269)：替换 13 处；`findCommentElement` 可直接返回 `(nil, err)`。
- [`xiaohongshu/publish_video.go:37`](/tmp/xiaohongshu-mcp-fork/xiaohongshu/publish_video.go:37) 至 [`152`](/tmp/xiaohongshu-mcp-fork/xiaohongshu/publish_video.go:152)：替换 6 处。
- [`xiaohongshu/feed_detail.go:105`](/tmp/xiaohongshu-mcp-fork/xiaohongshu/feed_detail.go:105) 至 [`748`](/tmp/xiaohongshu-mcp-fork/xiaohongshu/feed_detail.go:748)：替换固定等待，并将 12 处 `sleepRandom()` 改为 `page.SleepRandom(...)` 或新增的私有 `sleepRandomContext(page, minMs, maxMs)`。
- [`xiaohongshu/like_favorite.go:53`](/tmp/xiaohongshu-mcp-fork/xiaohongshu/like_favorite.go:53) 至 [`200`](/tmp/xiaohongshu-mcp-fork/xiaohongshu/like_favorite.go:200)：替换 5 处。
- [`xiaohongshu/login.go:23`](/tmp/xiaohongshu-mcp-fork/xiaohongshu/login.go:23)、[`44`](/tmp/xiaohongshu-mcp-fork/xiaohongshu/login.go:44)、[`66`](/tmp/xiaohongshu-mcp-fork/xiaohongshu/login.go:66)：替换 3 处。
- [`xiaohongshu/feeds.go:30`](/tmp/xiaohongshu-mcp-fork/xiaohongshu/feeds.go:30)：替换 1 处。

对于返回 `bool` 或无返回值的内部 helper，不修改签名：

```go
if err := page.Sleep(...); err != nil {
	return
}
```

其外层的 `error` 返回函数在下一处可中断等待或 Rod 操作处返回 `ctx.Err()`。评论加载的 `commentLoader.load()` 则应在调用这类无返回值 helper 后立即检查并返回页面上下文错误，避免把取消误报为正常完成。

```go
if err := cl.page.Sleep(0); err != nil {
	return err
}
```

更清晰的实现是为 `Page` 增加 `Err() error { return p.ctx.Err() }`，然后写为 `if err := cl.page.Err(); err != nil { return err }`。

### 风险评估

- 低风险：等待时间与随机范围保持不变，只改变取消时的退出时机。
- 需要完整替换：漏掉任意轮询中的 `time.Sleep`，该路径仍会延迟取消；提交前应以 `rg 'time\.Sleep' xiaohongshu/...` 验证范围内归零。
- `Must*` 系列在 Rod 因取消返回错误时可能 panic；本项优先解决 sleep 阻塞，不应在同一个 P2 中顺带将所有 `Must*` 改为错误返回 API。
- `CommentFeedAction` 当前注释称不继承外部超时，但 action 创建时已在 `service.go` 传入 `page.Context(ctx)`。本方案保留其 60 秒 Rod 超时策略，同时允许调用方取消请求。

### 优先级建议

**P2，建议优先处理。** 单请求可因 `publish.go` 的 3 秒等待、视频发布或多轮评论加载而在取消后持续占用浏览器页面与 goroutine；改动机械、可集中验证。

---

## 问题 2：`Page.wrapPage()` 重建 `Actor`，丢失鼠标和键盘状态

### 根因分析

[`Browser.wrapPage()`](/tmp/xiaohongshu-mcp-fork/pkg/humanize/rod/hrod.go:68) 仅在真实新页面创建时构造 `Actor`，并调用一次：

```go
_ = page.actor.Mouse.InitPosition()
```

这是正确的初始化边界。

但 [`Page.wrapPage()`](/tmp/xiaohongshu-mcp-fork/pkg/humanize/rod/hrod.go:110) 被 `Context()`、`Timeout()`、`CancelTimeout()` 用于构造同一底层页面的 Rod 派生视图时，却每次调用：

```go
actor: humanize.NewWithContext(rp, p.cfg, p.ctx),
```

因此每次派生页面都会生成新的：

- `Mouse`：[`initialized`](/tmp/xiaohongshu-mcp-fork/pkg/humanize/mouse.go:27) 回到 `false`，下一次移动可能再次执行初始定位；
- `Keyboard`：[`lastEl`](/tmp/xiaohongshu-mcp-fork/pkg/humanize/keyboard.go:20) 被清空，连续输入被误认为新的输入目标，可能触发额外点击和鼠标移动；
- `Actor` 级上下文绑定也被不必要地反复重建。

`Context()` 已经在 [`hrod.go:131`](/tmp/xiaohongshu-mcp-fork/pkg/humanize/rod/hrod.go:131) 明确调用 `actor.SetContext(ctx)`，说明正确的模型应是“复用 actor，仅更新 context”，而不是“新建 actor”。

### 修复方案

修改 [`pkg/humanize/rod/hrod.go:110`](/tmp/xiaohongshu-mcp-fork/pkg/humanize/rod/hrod.go:110)，使派生页面共享父页面的 `Actor`：

```go
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
```

保持 [`Page.Context()`](/tmp/xiaohongshu-mcp-fork/pkg/humanize/rod/hrod.go:131) 的显式上下文更新；仅增加 nil 防御即可：

```go
func (p *Page) Context(ctx context.Context) *Page {
	page := p.wrapPage(p.Rod.Context(ctx))
	if page == nil {
		return nil
	}
	page.ctx = ctx
	page.actor.SetContext(ctx)
	return page
}
```

`Timeout()` 和 `CancelTimeout()` 保持派生上下文 `p.ctx`，不创建新 actor：

```go
func (p *Page) Timeout(d time.Duration) *Page {
	return p.wrapPage(p.Rod.Timeout(d))
}

func (p *Page) CancelTimeout() *Page {
	return p.wrapPage(p.Rod.CancelTimeout())
}
```

结果关系：

```text
Browser.NewPage()
  └─ Browser.wrapPage(): 创建 Actor，InitPosition 一次

Page.Context / Timeout / CancelTimeout
  └─ Page.wrapPage(): 复用同一 Actor
       ├─ Mouse.initialized 保留
       ├─ Keyboard.lastEl 保留
       └─ Context() 仅调用 Actor.SetContext(ctx)
```

### 验证建议

新增 `pkg/humanize/rod` 单元测试，至少覆盖：

```go
func TestPageWrapPageReusesActor(t *testing.T) {
	parent := newTestPage(...)
	child := parent.wrapPage(parent.Rod.Timeout(time.Second))

	require.Same(t, parent.actor, child.actor)
	require.Same(t, parent.Actor().Mouse, child.Actor().Mouse)
	require.Same(t, parent.Actor().Keyboard, child.Actor().Keyboard)
}
```

再增加行为级测试：

1. 初始化页面后记录 `Mouse` 实例；
2. 连续调用 `Context(ctx).Timeout(...)`；
3. 断言仍是同一 `Mouse`/`Keyboard`；
4. 调用 `Context(cancelledCtx)` 后，断言人化延迟返回 `context.Canceled`。

### 风险评估

- 低风险：`Context`、`Timeout`、`CancelTimeout` 派生的是同一浏览器页面，本就应共享输入设备状态。
- 需要明确并发约束：派生 `Page` 共享可变 `Actor`，同一底层页面不能被多个 goroutine 以不同 context 并发操作。浏览器页面当前也不应被并发复用；应在 `Page` 注释或 browser manager 的使用约束中明确这一点。
- 不应在 `Page.wrapPage()` 中重复调用 `InitPosition()`；首次真实页面创建时的 [`Browser.wrapPage():83`](/tmp/xiaohongshu-mcp-fork/pkg/humanize/rod/hrod.go:83) 已是唯一正确位置。

### 优先级建议

**P2，建议与问题 1 同批提交，且先完成本项。** 问题 1 依赖 `Page.Context(ctx)` 传递页面级取消语义；先保证派生页面保持同一 actor 状态，可避免修复等待取消后又引入鼠标重定位和键盘状态丢失。

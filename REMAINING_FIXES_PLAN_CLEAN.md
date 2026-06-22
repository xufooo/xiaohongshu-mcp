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

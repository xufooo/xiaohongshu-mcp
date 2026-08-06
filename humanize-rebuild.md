## 1. 修改白名单与目录定稿

重制后结构：

```text
humanize/
├── delay.go                 # 上游原样
├── input.go                 # 上游原样
├── mouse.go                 # 上游原样
├── provider.go              # 上游原样
├── humanize_test.go         # 上游单元测试原样
├── actor_ext.go             # 原 pkg/humanize/humanize.go
├── config_ext.go            # 原 pkg/humanize/config.go
├── keyboard_ext.go          # 原 pkg/humanize/keyboard.go
├── actor_mouse_ext.go       # 原 pkg/humanize/mouse.go
├── path_ext.go              # 原 pkg/humanize/path.go
├── util_ext.go              # 原 pkg/humanize/util.go
└── rod/
    ├── hrod.go              # 原 pkg/humanize/rod/hrod.go
    └── hrod_test.go
```

删除迁移完成后的旧目录：

```text
pkg/humanize/
```

不保留旧 import path 的兼容 alias，避免长期维护两套入口。

### 上游文件

从 `upstream/main` 原样落入：

- `humanize/delay.go:1-21`
- `humanize/input.go:1-195`
- `humanize/mouse.go:1-69`
- `humanize/provider.go:1-77`
- `humanize/humanize_test.go:1-127`

验收时逐文件与 `git show upstream/main:humanize/<file>` 做字节级比较。以后同步上游也只覆盖这四个生产文件。

不直接复制 `input_integration_test.go`：它以 `package humanize` 导入 `browser`，而本分支 `browser` 又依赖 `humanize`，会形成测试 import cycle。真实浏览器验证放到 hrod 集成验收中，不改生产结构解决测试问题。

### 本地扩展文件

仅移动并改名，不改公开符号：

- `pkg/humanize/humanize.go:1-62` → `humanize/actor_ext.go`
- `pkg/humanize/config.go:1-149` → `humanize/config_ext.go`
- `pkg/humanize/keyboard.go:1-398` → `humanize/keyboard_ext.go`
- `pkg/humanize/mouse.go:1-663` → `humanize/actor_mouse_ext.go`
- `pkg/humanize/path.go:1-191` → `humanize/path_ext.go`
- `pkg/humanize/util.go:1-37` → `humanize/util_ext.go`
- `pkg/humanize/rod/hrod.go:1-984` → `humanize/rod/hrod.go`
- `pkg/humanize/rod/hrod_test.go:1-73` → `humanize/rod/hrod_test.go`

`*_ext.go` 明确标识本地扩展，后续拉取上游时不修改这些文件。

### 唯一符号冲突

上游和本地都定义了包内私有函数 `cubicBezier`，Go 不支持重载。

只在 `humanize/path_ext.go` 做：

- 原 `path.go:68`：调用改为 `actorCubicBezier(...)`
- 原 `path.go:153`：`cubicBezier` 改名为 `actorCubicBezier`
- 函数实现和参数顺序不变

上游 `humanize/mouse.go:cubicBezier` 保持原样。除此之外未发现顶层符号冲突。

## 2. API 统一策略

采用“共存但职责唯一”，不做行为合流。

### 上游 raw rod API

完整保留：

- `humanize.Delay`
- `humanize.Click`
- `humanize.ClickNoWait`
- `humanize.MoveTo`
- `humanize.Hover`
- `humanize.ClickAt`
- `humanize.Type`
- `humanize.Provider/SetProvider`

用途是保持上游兼容，方便后续同步新增能力。本期生产代码不改用这些函数。

### 本项目生产 API

继续唯一使用：

- `hrod.Page`
- `hrod.Element`
- `humanize.Actor`
- `humanize.Config`
- `Actor.Mouse/Keyboard`

特别保留：

- `Page/Element.Context`、`Timeout`
- `bindActorContext` 的 clone 上下文隔离
- `waitInteractable(5s)` fail-closed 检查
- `Sleep/SleepRandom`
- `Click/ClickNoScroll/ClickPoint/MovePoint`
- `Hover/ScrollIntoView`
- `Input`
- `Wait/WaitStable/WaitDOMStable`
- `CloseContext/Health`
- 本地多段鼠标路径、滚轮、输入节奏、错字修正

禁止把 `hrod.Element.Click/Input/Hover` 改为调用上游 `humanize.Click/Type/Hover`。两者的点击等待、鼠标路径、输入方式和上下文语义不同，替换会造成真实行为回归。

`Provider/Delay` 与本地 `Config/Actor` 本期不做桥接；桥接会改变延迟分布，超出“行为零变化”。

## 3. 调用点适配

### A. 机械替换

以下文件只改 import path，不改任何类型、函数调用、错误处理或时序。

根包：

- `browser/browser.go:11`  
  `.../pkg/humanize` → `.../humanize`
- `humanize/rod/hrod.go` 原 `hrod.go:26`
- `humanize/rod/hrod_test.go` 原 `hrod_test.go:9`

hrod 包：

- `browser/browser.go:12`
- `browser/browser_manager.go:12`
- `service.go:20`
- `xiaohongshu/browse_session.go:18`
- `xiaohongshu/comment_feed.go:10`
- `xiaohongshu/current_detail.go:12`
- `xiaohongshu/dom_extract.go:9`
- `xiaohongshu/feed_detail.go:18`
- `xiaohongshu/feed_pagination.go:9`
- `xiaohongshu/feeds.go:11`
- `xiaohongshu/identity.go:11`
- `xiaohongshu/like_favorite.go:10`
- `xiaohongshu/login.go:10`
- `xiaohongshu/navigate.go:8`
- `xiaohongshu/network_capture.go:11`
- `xiaohongshu/note_open.go:11`
- `xiaohongshu/notification.go:11`
- `xiaohongshu/notification_like.go:9`
- `xiaohongshu/notification_reply.go:11`
- `xiaohongshu/publish.go:17`
- `xiaohongshu/publish_video.go:9`
- `xiaohongshu/read_stage.go:10`
- `xiaohongshu/ready.go:10`
- `xiaohongshu/risk.go:9`
- `xiaohongshu/search.go:17`
- `xiaohongshu/selector_watchdog.go:8`
- `xiaohongshu/ui_selectors.go:7`
- `xiaohongshu/user_profile.go:10`

统一替换为：

```go
"github.com/xpzouying/xiaohongshu-mcp/humanize"
hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
```

### B. 需要逻辑级改动

只有一项：

- `humanize/path_ext.go`：私有 `cubicBezier` 重命名及内部调用同步。

### C. 必须原样保留

以下调用不做机械替换之外的任何改写：

- `browser/browser.go:181` 的 `hrod.NewBrowser(..., humanize.DefaultConfig())`
- 所有 `*hrod.Page`、`*hrod.Element` 函数签名
- 全仓 `.Click/.Input/.Hover/.ScrollIntoView/.SleepRandom/.Context/.Timeout`
- `Actor()`、`NewElement()` 公开签名
- session、通知、互动、发布的拟人节奏和状态机
- `mcp_server.go`、`mcp_handlers.go`、工具参数和注册逻辑

## 4. 分步实施顺序

### P0：目录与上游基线

1. 新建根目录 `humanize/`。
2. 原样导入上游四个生产文件及 `humanize_test.go`。
3. 移动六个本地扩展文件并使用 `*_ext.go` 命名。
4. 移动 `rod/hrod.go` 和测试。
5. 解决 `actorCubicBezier` 唯一冲突。
6. 删除空的 `pkg/humanize/`。

P0 验收：

- 上游四文件与 upstream/main 字节一致。
- `go test ./humanize` 通过。
- 包内不存在重复符号。
- 本地 `Actor/Config/Mouse/Keyboard` 导出符号完整。
- 旧目录不存在。

### P1：全仓 import 适配

1. 先改 `humanize/rod` 内部根包引用。
2. 改 `browser/browser.go`、`browser_manager.go`。
3. 改 `service.go`。
4. 批量修改 25 个 `xiaohongshu` 文件。
5. 运行 `gofmt`，仅允许 import 排序产生附带变化。

P1 验收：

```bash
rg 'xiaohongshu-mcp/pkg/humanize' --glob '*.go'
```

结果必须为零。

同时核对：

- 没有新增 `humanize.Click/Type/Hover/Delay` 生产调用。
- 所有原 `hrod` 方法调用文本保持不变。
- `mcp_server.go`、`mcp_handlers.go` 无 diff。
- `go.mod`、`go.sum` 无 diff。

### P2：验证

静态和单元验证：

```bash
gofmt -w humanize browser service.go xiaohongshu
git diff --check
go test ./humanize/...
go test ./browser/... ./xiaohongshu/...
go test ./...
go build ./...
```

重点保留并运行：

- `TestElementContextIsolation`
- `TestElementSharesActor`
- 上游 `LogNormal/Delay/cubicBezier/jitter` 单元测试
- 现有 session、通知、互动测试

真实浏览器最小回归：

1. 启动浏览器并确认 `Health`。
2. `start_page → search_feeds → open_note → get_note_detail → go_back`。
3. 验证真实滚轮、轮播和停留仍发生。
4. 点赞、收藏各执行 toggle 后恢复原状态。
5. 通知三 tab 可读取，notification ref 可定位。
6. 评论输入只做测试账号或专用内容验证。
7. 关闭页面和浏览器，确认 `CloseContext` 不阻塞。

工具面验收：

- MCP 正式工具仍为 22 个。
- 工具名、参数 schema、`session_id`、`confirm_token` 不变。
- `search/like/favorite/comment/notification` 仍走 session 路径。
- 无新增 raw rod 操作替代 hrod 操作。

## 5. 风险与控制

- 最大编译风险是 `cubicBezier` 重名；通过本地私有函数重命名一次性消除。
- 最大行为风险是误把 hrod 转接到上游 free functions；明确禁止。
- 目录迁移会改变 Go import path，但不改变 MCP/HTTP 对外接口。不保留旧 Go 路径 shim。
- 上游 `SetProvider` 使用包级 provider；本期生产不调用，避免并发切换和双重延迟。
- 上游 raw `Type/Click` 的等待策略与本地不同；只作为上游兼容能力存在。
- 后续拉取上游时只同步 `delay.go/input.go/mouse.go/provider.go`；若上游新增文件，先做顶层符号冲突扫描，再纳入，禁止直接覆盖 `*_ext.go`。
- 用户现有未跟踪实施文档不在白名单内，全部保留不动。

最终通过条件：上游四文件可直接比对、旧 `pkg/humanize` 引用为零、hrod 全部 API 与上下文隔离能力保留、全仓测试和构建通过、22 个工具及真实操作行为无变化。
tokens used
2,829,705
结论：采用“根目录 `humanize/` 与上游同路径、上游四文件保持原样、本地 Actor/hrod 作为同包扩展”的唯一方案。生产调用继续以 `hrod.Page/Element` 为准，不切换到上游 raw rod 函数，确保 22 个工具及操作节奏不变。

基线：

- fixup-test：`6fdc9c169a4bba264f0238dc86c4d20bd575df9f`
- upstream/main：`98f84ad3adc7172c88eda36372bcf8c9ca6211eb`
- 两边均使用 `go-rod v0.116.2`、`testify v1.11.1`，无需调整依赖。


# xiaohongshu-mcp 防封禁改造方案

## P0 — 必须做（2-4天）
**操作节奏控制 + 真实 UI 入口 + 行为链约束 + 账号维度持久化**

### 改什么

- 小时桶 → **多级滑动窗口**：最近 10 分钟 / 最近 1 小时 / 最近 24 小时
- 每种操作同时配置：
  - **随机等待区间**：控制相邻操作节奏
  - **滑动窗口预算**：控制一段时间内总量
  - **账号维度持久化状态**：防止重启、换进程后绕过限制
- 把搜索和打开笔记改成真实 UI 路径：
  - 搜索：进入首页 → 点搜索框 → 逐字输入关键词 → 等建议/结果 → 回车或点击搜索
  - 打开笔记：从首页/搜索结果卡片滚动到目标 → 点击卡片进入详情，不再直接拼详情 URL 作为首选路径
- 行为链约束从建议升级成强制校验：点赞、收藏、评论、回复前必须有当前账号最近的打开和阅读状态
- 新增轻量 `ActionStateStore`，记录当前账号的来源、阅读、滚动、失败和风控状态
- 所有等待使用偏态分布（log-normal/triangular），不要使用固定 sleep
- `Min` 只是安全下限，不是常规等待值，代码永远不要精准卡 `Min`
- 命中短等待时可以自动等待；命中长窗口预算时直接返回限流错误，让调用方稍后再试
- 连续失败、验证码、登录异常、风控提示进入账号级熔断，不继续自动重试

### 配置模型

```go
type DelayConfig struct {
    Min time.Duration  // 安全下限，不是常规执行值
    P50 time.Duration  // 典型等待
    P95 time.Duration  // 少数较长停顿
    Max time.Duration  // 绝对上限
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
    AccountID  string // 优先从登录用户信息取；取不到时用 profileDir/cookie 文件指纹兜底
    ProfileDir string
}

type ActionState struct {
    LastAction        string
    LastActionAt      time.Time
    LastOpenedFeedID  string
    LastOpenSource    string // home/search/recommend/detail_url_fallback
    LastOpenAt        time.Time
    LastReadAt        time.Time
    ReadDuration      time.Duration
    FeedScrollCount   int
    CommentDwellTime  time.Duration
    InteractionsOnFeed int
    SessionActions    int
    ConsecutiveFailures int
    RiskCooldownUntil time.Time
}
```

### 每种操作的节奏和预算

| 操作 | Min | P50 | P95 | Max | 10分钟预算 | 1小时预算 | 24小时预算 |
|:----|----:|----:|----:|----:|----------:|---------:|----------:|
| 浏览列表 | 6s | 12s | 25s | 45s | 35 | 150 | 800 |
| 搜索 | 12s | 22s | 45s | 90s | 6 | 20 | 80 |
| 打开笔记/看详情 | 10s | 25s | 70s | 150s | 18 | 80 | 400 |
| 点赞/取消点赞 | 25s | 60s | 180s | 300s | 6 | 25 | 120 |
| 收藏/取消收藏 | 35s | 90s | 240s | 480s | 4 | 15 | 60 |
| 发表评论 | 90s | 180s | 480s | 900s | 2 | 8 | 30 |
| 回复评论 | 120s | 240s | 600s | 1200s | 2 | 6 | 25 |
| 发布内容 | 180s | 480s | 1200s | 1800s | 1 | 2 | 5 |

### 全局预算

除了单操作预算，还要加全局预算，防止多个低风险操作叠加后总量过高。

| 预算类型 | 10分钟 | 1小时 | 24小时 | 说明 |
|:----|------:|-----:|------:|:----|
| 全部操作总量 | 60 | 250 | 1200 | 包含浏览、搜索、打开、互动 |
| 互动操作总量 | 10 | 40 | 160 | 点赞、收藏、评论、回复、发布 |
| 写入操作总量 | 3 | 12 | 45 | 评论、回复、发布 |
| 发布类操作 | 1 | 2 | 5 | 发笔记/发内容 |

### P0 真实 UI 路径

- `SearchAction.Search` 不再默认直接导航 `search_result_ai?keyword=...`
- 搜索默认流程：
  1. 打开首页或复用当前首页/搜索页
  2. 找搜索框，使用 `hrod.Element.Input` 逐字输入
  3. 等待建议或搜索结果区域出现
  4. 回车或点击搜索按钮
  5. 结果加载后从可见 DOM 提取卡片基础信息
- 打开笔记默认流程：
  1. 从搜索/首页结果中定位卡片
  2. `ScrollIntoView` 到卡片附近
  3. 鼠标移动到卡片随机落点并点击
  4. 等待详情页可见内容稳定
  5. 写入 `ActionStateStore.LastOpenedFeedID/LastOpenSource/LastOpenAt`
- 允许保留直接详情 URL 作为兼容兜底，但标记为 `detail_url_fallback`；互动前仍必须补阅读阶段，不能导航后立即点赞/评论

### 行为链约束（P0 强制）

- 点赞、收藏、评论、回复之前，必须存在最近一次 `打开笔记/看详情`
- 最近一次打开的 `feed_id` 必须和当前互动目标一致；不一致直接拒绝
- 最近一次打开必须来自首页/搜索结果点击；直接 URL 兜底打开的笔记需要额外阅读和滚动后才允许互动
- 点赞、收藏之前，当前笔记至少停留 20 秒
- 评论之前，当前笔记阅读总时长至少 45 秒，并且至少发生 1 次正文/图片区域滚动
- 回复之前，评论区至少停留 60 秒，并且发生过评论区滚动或目标评论定位过程
- 发布内容之前，必须经过编辑阶段，不允许直接提交
- 同一篇笔记不要连续执行多个互动动作，中间要插入阅读、滚动或停顿
- 连续打开 10 篇笔记后，强制进入 3~8 分钟休息段
- 连续发生 3 次失败、验证码、登录异常或风控提示时，立即停止，不继续重试
- 对同一篇笔记执行一次互动后，下一次互动前必须再次滚动/阅读/停顿；不要连续点赞+收藏+评论一口气完成

### 更细粒度的行为模拟

| 场景 | 建议 |
|:----|:----|
| 输入关键词 | 每字符 80~350ms，偶尔停 0.5~2s |
| 输入评论 | 每字符 120~500ms，句子之间停 1~5s |
| 打开笔记后阅读 | 20~180s，按正文长度、图片数量、评论数量动态计算 |
| 评论前阅读 | 至少 45s，必须包含正文/图片停留和评论区浏览 |
| 回复前评论区停留 | 至少 60s，必须滚动或定位到目标评论 |
| 点赞前停顿 | 阅读结束后再等 2~15s |
| 收藏前停顿 | 阅读结束后再等 5~30s |
| 评论前停顿 | 阅读结束后再等 10~60s |
| 滚动间隔 | 每次滚动后停 0.8~5s |
| 返回列表 | 3~15s，不立刻打开下一篇 |
| 异常停顿 | 每 20~40 次操作随机停 1~5 分钟 |
| 长休息 | 每 60~120 分钟随机休息 10~30 分钟 |

### 持久化内容

限流状态按账号维度持久化到文件，重启不丢。账号 key 优先使用登录用户 ID；取不到时用浏览器 profile 目录、cookies 文件路径和 cookies 文件摘要组合成稳定 key。

需要保存：
- 每种操作最近 24 小时的时间戳队列
- 全局操作时间戳队列
- 互动操作时间戳队列
- 写入操作时间戳队列
- 账号级风控冷却截止时间
- 最近一次风控/验证码/登录异常文本
- 最近一次操作类型和时间
- 最近一次打开的笔记 ID
- 最近一次打开来源：home/search/recommend/detail_url_fallback
- 最近一次打开时间、最近一次阅读时间、阅读总时长、滚动次数
- 评论区停留时长、评论区滚动次数
- 当前 session 的操作次数
- 当前 session 的连续失败次数
- 当天已发布数量

### 命中限制时的处理

- 如果只是下一次操作还没到时间，且需要等待时间小于 2 分钟，可以自动 sleep
- 如果需要等待时间超过 2 分钟，直接返回 rate limit 错误，带上 `retry_after`
- 如果命中 24 小时预算，不自动等待，直接拒绝
- 如果遇到验证码、登录失效、风控提示，不进入重试，直接记录账号级 `RiskCooldownUntil` 并停止
- 连续 3 次失败后进入账号级短熔断，建议冷却 30~120 分钟；验证码/安全验证进入更长熔断，建议冷却 6~24 小时
- 二次点击重试要谨慎：点赞/收藏/提交后状态未变化时，先等待 DOM/network 稳定并重新读取可见按钮状态；仍不确定则返回 `state_unknown`，不要马上补点第二次

### 涉及文件

- `pkg/ratelimit/limiter.go` — 重写限流逻辑，支持随机延迟 + 多级滑动窗口
- 新增 `pkg/ratelimit/store.go` — 按账号 key 持久化限流状态
- 新增 `configs/ratelimit.go` — 配置每种操作的 DelayConfig / BudgetConfig
- `handlers_api.go` / `mcp_handlers.go` — 调用点改为操作枚举
- 新增 `xiaohongshu/action_state.go` — 轻量 `ActionStateStore`，保存打开、阅读、滚动、失败和风控状态
- 新增 `xiaohongshu/ui_selectors.go` — 搜索框、结果卡片、详情页、互动按钮等 DOM 选择器集中管理
- `xiaohongshu/search.go` — P0 改为真实 UI 搜索，保留 URL 搜索作为显式兜底
- 新增 `xiaohongshu/note_open.go` — 从搜索/首页结果点击进入笔记
- `xiaohongshu/read_stage.go` — 提供阅读状态，供点赞/收藏/评论前置校验使用
- `comment_feed.go` / `like_favorite.go` — 互动前强制校验 `ActionStateStore`，并移除立即二次点击重试

---

## P1 — 建议做（4-6天）
**DOM 数据提取 + 交互质量优化**

### ① 分阶段减少 `__INITIAL_STATE__`
- P0 先保证用户可见动作走 DOM 和真实点击：搜索、筛选、打开笔记、点赞、收藏、评论、回复都不能依赖 JS 直接触发点击
- P1 再把数据提取改为 DOM 优先：渲染后的卡片、标题、作者、评论、互动状态
- `__INITIAL_STATE__` 暂时保留为数据提取 fallback，不作为点击、导航、互动状态变更的主路径
- 涉及：新增 `xiaohongshu/dom_extract.go`

### ② 完整阅读阶段
- 打开笔记后：看标题/正文 → 看图或视频停留 → 小幅滚动 → 看评论 → 停顿思考
- 阅读时长按正文长度、图片数量、评论数量动态计算
- 点赞/收藏前至少 20s；评论前至少 45s；回复前评论区至少 60s
- 涉及：`xiaohongshu/read_stage.go`，并在 `comment_feed.go` / `like_favorite.go` 强制调用

### ③ 交互前稳定性检查
- 在 `hrod.Element.Click` 前增加可交互检查：visible、stable、clickable、unobscured
- DOM 变化或遮挡时等待并重新定位，不直接点旧元素
- 二次点击只允许在明确读到“第一次点击未触发”时执行；状态不确定时返回错误给调用方

---

## P2 — 以后做（8-12天）
**浏览会话架构**

### 核心改动
- 引入 `BrowseSession` — 一个 session 内保留同一个页面
- session 保存：当前URL、来源URL、滚动位置、已看笔记
- 操作变成：`session.search()` → `session.openNote()` → `session.read()` → `session.like()` → `session.back()`

### 新增MCP工具
```
create_browse_session()
session_search(id, keyword)
session_open_note(id, result_ref)
session_read(id)
session_like(id)
session_comment(id, content)
session_back(id)
close_browse_session(id)
```

### 关键约束
- 互动只能对"已打开、已阅读"的笔记执行
- 不再接受任意 feed_id 直接跳转
- session 超时自动关闭（默认10分钟）
- 重启后 session 失效，但限流不丢

---

## 优先级建议

先搞 **P0**（账号维度限流 + 行为链状态 + 真实 UI 搜索/打开），这是当前最大风险面。
再看 **P1**（DOM 数据提取 + 阅读阶段完善 + 点击稳定性），用于减少对内部状态和 JS 注入的依赖。
最后 **P2**（浏览会话），架构级改造，工作量最大。

---

## 附：其他"像人"的细节（实现时注意）

| 维度 | 说明 |
|:----|:----|
| **操作概率** | 不是每篇都点赞/收藏/评论。大多数浏览只看不互动 |
| **频率衰减** | 连续操作越多，等待越长。连续打开10篇后应明显变慢或停止 |
| **输入模拟** | 搜索和评论模拟真实输入（逐字），不要直接 setValue 后提交 |
| **滚动行为** | 小幅滚动、停顿、偶尔回看，不要一次滚到底 |
| **异常停顿** | 偶尔停 1~5 分钟无操作 |
| **昼夜节奏** | 长时间运行要有活跃时段和休息段 |
| **失败处理** | 页面加载慢/元素没出现时，重试要退避 + 随机化 |
| **负反馈** | 遇验证码/登录异常/风控提示→立即停止，不继续重试 |
| **内容相关** | 评论内容要和笔记相关，不能模板化 |
| **设备一致性** | User-Agent、viewport、语言、时区前后一致 |
| **账号隔离** | 限流、失败、风控、发布数量都按账号/profile 维度隔离 |

---

## 参考来源

本文案的设计参考了以下开源项目：

| 编号 | 项目 | 链接 | 参考内容 |
|:---:|:----|:----|:---------|
| [1] | **ghost-cursor** ⭐1.5k | https://github.com/Xetera/ghost-cursor | Bezier 鼠标路径生成、随机控制点、easing 函数、随机落点 |
| [2] | **human-cursor** | https://github.com/CloverLabsAI/human-cursor | Playwright 拟人鼠标（ghost-cursor 升级版）、动量滚动、零瞬移 |
| [3] | **chrome-mcp-stealth** | https://github.com/Riaan-Fourie/chrome-mcp-stealth | Stealth/Fast 双模式、Bezier 鼠标（12-50步插值）、高斯输入延迟（~75ms mean）、标点停顿、滚动抖动（3-8步/次）、5层强制、注入检测 |
| [4] | **Emunium** | https://github.com/DedInc/emunium | OS 级鼠标键盘模拟、Fluent wait 链（.visible/.clickable/.stable/.unobscured） |
| [5] | **anti-bot-scraper (Go)** | https://github.com/AbdullahSaidAbdeaaziz/anti-bot-scraper | TLS 指纹伪造（uTLS）、Go 人类行为模拟（4种行为模式）、多级限流 |
| [6] | **socialcrabs** | https://github.com/adolfousier/socialcrabs | Warm-up 流程（3-5次滚动→导航→思考→操作）、内置 Rate Limiter、四阶段工作流、不混用爬取和互动 |
| [7] | **puppeteer-ghost** | https://github.com/ovftank/puppeteer-ghost | navigator.webdriver 覆盖、WebGL 供应商欺骗、语言时区随机化 |
| [8] | **CloakBrowser** | https://github.com/CloakHQ/cloakbrowser | 58 个 C++ 源码补丁（已在项目中使用）、reCAPTCHA v3 得分 0.9、humanize 模式 |
| [9] | **bumblebee** | https://github.com/socioy/bumblebee | RL 生成鼠标轨迹（参考，当前不优先使用） |

**数值参数说明：** 方案中的具体时间数值（如浏览 6s/12s/25s/45s、评论前 45s 阅读等）参考了 [3][6] 的设计思路，具体数值由 Codex (gpt-5.5) 根据工程经验给出，非实测数据，建议上线后根据实际风控反馈调整。

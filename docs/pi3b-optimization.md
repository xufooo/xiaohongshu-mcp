# 树莓派 3B 资源优化说明（perf/pi3-optimize）

目标设备：Raspberry Pi 3B，4×Cortex-A53 @1.2GHz，1GB RAM，无 swap，Chromium 常驻。
基线：`fixup-test` @ `0646295`。所有改动都在 `perf/pi3-optimize` 分支上，未推 `fixup-test` / `main`。

配套测试文档见 [`pi3b-test-plan.md`](./pi3b-test-plan.md)。

---

## 1. 结论速览

| 项 | 改动 | 依据等级 |
|:--|:--|:--|
| Chromium 低资源档（arm/arm64 默认开） | V8 old space 192MB、renderer 上限 2、关扩展/组件更新/默认浏览器检查、激进缓存回收、静音 | 本机 x86 实测（方向性）+ 代码确认 |
| 逐页资源拦截 | `Network.setBlockedURLs`，通过 `XHS_BROWSER_BLOCK_URLS` 显式开启图片/媒体拦截 | 真实页面实测：图片占详情页 93% 流量；默认不拦，避免改变平台可见资源画像 |
| Go 堆软上限 | `XHS_GO_MEMLIMIT`，低资源档默认 128MiB；`XHS_GOGC` 可选 | 代码确认 |
| 图片下载流式化 | 不再 `io.ReadAll` 整张图（原上限 50MiB），只读 307 字节头部判类型 | 代码确认 |
| 限流状态写放大 | 裁剪无变化不写盘；落盘改紧凑 JSON | 代码确认 |
| ActionState 写放大 | 落盘改紧凑 JSON | 代码确认 |
| 身份指纹采集节流 | 默认 10 分钟一次（`XHS_IDENTITY_CHECK_INTERVAL`） | 代码确认 |
| network capture 泄漏 | panic 路径也停采集 | 代码确认（缺陷修复） |
| 构建产物 | `-trimpath -ldflags="-s -w"` | CI 产物实测 −27.5% |
| 状态目录回退 | 状态目录不可写时回退 `os.TempDir()`，不再 fatal 退出 | 本机实测复现 + 修复后验证 |
| Docker `/dev/shm` | compose 补 `shm_size: "256m"`；修正文档错误说法 | 代码 + rod 源码确认 |

**明确没做**（有意为之，见 §4）：

- 不改任何拟人等待/阅读时长/限流阈值（那是防封禁设计，不是性能 bug）。
- 不做 page 复用池（改动浏览器生命周期，缺真机验证条件）。
- 不改 `WaitForXHSReady` 轮询密度（正确性关键路径，测试网密集）。
- 不默认加 `--disable-gpu` / `--disable-software-rasterizer`（实测：前者无可测收益，后者直接干掉 WebGL）。

---

## 2. 关键实测数据（本机 x86_64，Chrome 153 headless）

脚本：`cdp-probe.mjs`（自建 CDP 探针，非仓库文件）。同一 URL、同一 `--disable-features=site-per-process,TranslateUI`、同一 `--disable-dev-shm-usage`，只改附加 flag。
RSS = 该 `--user-data-dir` 下所有 Chrome 进程 RSS 之和（KB，含共享内存重复计算，只能看方向）。

| 配置 | 空页 RSS | 空页进程 | 加载后 RSS | 加载后进程 | load 事件 | WebGL |
|:--|--:|--:|--:|--:|--:|:--|
| 基线（无附加 flag） | 1,331,040 | 12 | 1,912,176 | 13 | 6388ms | 有（SwiftShader） |
| 低资源档 + `--disable-software-rasterizer` | 1,025,428 | 10 | 1,574,244 | 11 | 4290ms | **无（no webgl context）** |
| 低资源档（不含 rasterizer 开关） | 1,072,088 | 10 | 1,661,696 | 11 | 3622ms | 有（SwiftShader） |
| 只加 `--js-flags` + `--renderer-process-limit` | 1,127,440 | 10 | 1,694,276 | 11 | 11609ms | 有（SwiftShader） |

读法（**一手实测，n=1，仅方向性**）：

1. **进程数 12→10 来自 `--renderer-process-limit=2`**：只加 js-flags + renderer-limit 那一行同样是 10/11，
   说明 `--disable-gpu` 在本机 headless 下并不减少进程（Chrome 仍为软件光栅保留 GPU 进程）。
2. **`--disable-software-rasterizer` 会让 `canvas.getContext('webgl')` 直接返回 null**。
   真实用户浏览器没有这种状态，属于明显的自动化特征，因此**不进默认档**。
3. 加载后 RSS 相对基线下降约 11%~18%（1.91GB → 1.66~1.69GB）。
4. load 时间在单次采样里不可比（缓存冷热不同，11609ms 那次是冷缓存）。**不得引用为性能结论**。

**URL 说明（重要）**：本次探测目标是 `https://www.xiaohongshu.com/explore`，
但开发机所在出口 IP 被小红书风控直接拦到
`/website-login/error?...error_code=300012&error_msg=IP存在风险`，
落地页是 41 字符的错误页。所以：

- 上表绝对值**不代表**树莓派上真实信息流页面的占用，只能作 flag 之间的相对比较；
- 本机**无法**做需要登录态的小红书端到端验证（见测试文档 §5 的环境约束）。

### 2.2 本机端到端实测（真实 Chromium，优化后二进制）

用 CI 产出的 `linux/amd64` 产物 + `google-chrome-stable 153` 实跑：

```
time=... level=info  msg="GOMEMLIMIT set to low-resource default 134217728 bytes"
time=... level=info  msg="low resource profile enabled: js_heap_mb=192 renderer_limit=2"
time=... level=info  msg="blocking 8 URL patterns per page"
time=... level=info  msg="launching browser" arg_count=36
time=... level=info  msg="browser connected" pid=27
```

| 检查 | 结果 |
|:--|:--|
| `/health` | 200 `{"success":true,...}` |
| `GET /api/v1/login/status` | 200 `{"is_logged_in":false}`（无 cookies，符合预期） |
| `GET /api/v1/login/qrcode` | 200，`success=true`，`timeout=4m0s`，`img` = 6030 字符 base64 PNG → **扫码登录链路可用** |
| Chromium 实际收到的 flag | `--js-flags=--max-old-space-size=192`、`--renderer-process-limit=2`、`--disable-extensions`、`--aggressive-cache-discard`、`--no-default-browser-check`、`--mute-audio`、`--no-sandbox`、`--headless` |
| Chrome 进程数 / RSS 合计 | 11 个 / 1,640,536 KB（x86 代理量，非 Pi 数字） |
| `set blocked URLs failed` 计数 | **0** → 逐页资源拦截在真实路径上被接受 |
| MCP 工具注册 | `Registered 22 MCP tools`（无回归） |

产物体积（同一 workflow、同平台）：

| 产物 | 基线 `35501381743` | 优化后 `35502300270` | 变化 |
|:--|--:|--:|:--|
| linux/amd64 | 23,267,136 B | 16,875,704 B | **−27.5%** |
| linux/arm64 | — | 16,187,576 B | — |

### 2.3 顺带发现并修掉的问题：状态目录不可写导致服务起不来

本机复现（`HOME` 的 XDG 缓存目录只读）：

```
level=fatal msg="failed to initialize service: 初始化风控状态存储失败:
  mkdir /home/ooo/.cache/xiaohongshu-mcp: read-only file system"
```

树莓派上只读 rootfs 或 systemd `ProtectHome=true` 会命中同一路径。
修复后同一环境：

```
level=warning msg="action state dir /home/ooo/.cache/xiaohongshu-mcp/action_state 不可写
  （mkdir ...: read-only file system），回退到 /tmp/xiaohongshu-mcp-action-state"
GET /health 200
```

### 2.4 真实页面实测（内置浏览器，真实小红书，未登录）

用内置浏览器直接打开真实站点（**不是** headless Chrome —— headless 在本机出口 IP 上会被小红书
风控直接拦到 `/website-login/error?...error_code=300012`，内置浏览器则能正常打开，见 §2.5）。

**信息流 `/explore`：**

| 指标 | 值 |
|:--|--:|
| DOM 节点 | 1,338 |
| 资源条目 / 传输字节 | 50 / 169,737 B |
| 其中 xhr / fetch | 36 / 11 条（基本全是接口） |
| 图片（img） | 63 张（缩略图为主） |
| 媒体（mp4/m3u8…） | **0** |
| 字体 | **0** |
| 选择器命中 | `search_input`=1、`feed_card`=28、`like-wrapper`=28、`search_result`=1 ✅ |

**笔记详情 `/explore/<id>?xsec_token=…`（代码 `makeFeedDetailURL` 的同款 URL）：**

| 指标 | 值 |
|:--|--:|
| DOM 节点 | 1,982 |
| 资源条目 / 传输字节 | 65 / **1,772,378 B** |
| 其中 `sns-webpic` 图片 | 34 张 / **1,616,448 B（占该页 93%）** |
| `sns-avatar` 头像 | 16 张 / 29,594 B |
| 媒体 / 字体 | **0 / 0** |
| 选择器命中 | `note-detail-mask`=1、`note-container`=1、`interact-container`=1、`comments-container`=1、`note-scroller`=1、`comment-box`=1、`comment-item`=19、`show-more`=7 ✅ |
| 就绪探针成本（模仿 `ready.go` 的 detail probe，20 次均值） | **0.57 ms/次**（桌面；说明轮询本身不是瓶颈） |

**「把图片全部换成占位」的前后对比（模拟被拦）：**

| 检查 | 换之前 | 换之后 |
|:--|--:|--:|
| DOM 节点 | 1,982 | 1,982 |
| `.comment-item` | 19 | 19 |
| `.like-wrapper` / `.collect-wrapper` | 50 / 1 | 50 / 1 |
| 评论输入框 | 1 | 1 |
| 正文 `#detail-desc` | 有 | 有（文本不变） |
| `.note-scroller` 可滚动 | 是 | 是 |
| 笔记图片 URL（数据层 `noteDetailMap[].note.imageList`） | 6+ 条 | **仍在** |

结论：**文本、评论、互动、正文提取都不依赖图片像素；图片 URL 来自页面数据层，与图片请求成败无关。**
因此把图片 CDN 放进低资源档默认拦截列表，是这次改动里对 Pi 收益最大的一项
（1.5MiB/篇的下载 + 38 张大图的解码 CPU + 解码位图常驻内存）。
`fe-static.xhscdn.com`（JS/CSS）**绝不拦**，测试里有专门断言。

### 2.5 headless 与真实浏览器的差异（环境事实）

同一台机器、同一条出口：

| 客户端 | `/explore` | 笔记详情 |
|:--|:--|:--|
| 内置浏览器（正常 Chrome 画像） | ✅ 正常渲染 28 张卡片 | ✅ 正常渲染（正文/19 条评论/点赞 3002） |
| `google-chrome-stable --headless` | ❌ 302 到 `/website-login/error`，`error_code=300012 IP存在风险` | ❌ 同上 |

所以：**本机的 headless 端到端验证做不了**（会被风控拦），
但「页面结构 / 资源构成 / 提取面」这些结论用内置浏览器拿到的一手数据是有效的。

---

## 3. 逐项改动与代码位置

### 3.1 Chromium 低资源档

- `third_party/headless_browser/headless_browser.go`
  - 新增 `Config.LowMemory / JSHeapMB / RendererLimit` 与 `WithLowMemoryProfile(jsHeapMB, rendererLimit)`；
  - 新增 `applyLowMemoryLauncherProfile()`，把 `js-flags=--max-old-space-size=N`、
    `renderer-process-limit=N` 与固定 flag 一起 `Set` 到 launcher；
  - 只做**增量添加**，不整体替换 rod 的 `disable-features`（沿用 `applyCloakLauncherProfile` 注释里的既有理由）。
- `browser/browser.go`：按 `configs.LowResourceProfile()` 注入。
- `configs/browser.go`：
  - `LowResourceProfile()`：`XHS_LOW_RESOURCE` 显式优先，未设置时 `arm`/`arm64` 默认开；
  - `BrowserJSHeapMB()`：`XHS_BROWSER_JS_HEAP_MB` 优先，低资源档 192，否则 256；
  - `BrowserRendererLimit()`：`XHS_BROWSER_RENDERER_LIMIT` 优先，默认 2。

### 3.2 逐页资源拦截

- `third_party/headless_browser/headless_browser.go`：`WithBlockedURLs()` + `Browser.blockedURLs`，
  在 `Page()` 里对新建 target 调一次 `proto.NetworkSetBlockedURLs{Urls: ...}`；
  失败只 `Warn`，不影响建页。
- `configs.BrowserBlockedURLPatterns()`：`XHS_BROWSER_BLOCK_URLS`（逗号分隔，`-` 表示不拦）优先；
  未设置时返回空列表。需要性能实验时显式配置图片/头像/视频模式，且**不含** `fe-static`。
- 实测结论：**不需要先 `Network.enable`**（探针里 `setBlockedURLsWithoutEnable: "ok"`），
  因此没有引入 Network 域的事件流开销。

### 3.3 Go 运行时内存

- `configs/runtime.go`（新）：`ApplyRuntimeLimits()` 读 `XHS_GO_MEMLIMIT`（支持 `128MiB` / 字节数）
  与 `XHS_GOGC`，未设置时低资源档用 `debug.SetMemoryLimit(128MiB)`；
  软上限只让 GC 更积极，不会让进程失败。
- `main.go`：在任何服务构造之前调用。

### 3.4 图片下载流式化

- `pkg/downloader/images.go`：
  - 新增 `writeImageStream()`：写 `.tmp` → 校验总量 → `rename`，失败清理，不留半成品；
  - `DownloadImage()` 先 `io.ReadFull` 读 307 字节头部做 `filetype.Match` / `IsImage`，
    其余 `io.Copy` 流式落盘；
  - `maxRemoteImageBytes` 由 `const` 改为 `var`，仅为测试可下调阈值。
- 效果：单张图片的进程内峰值内存由"整张图"降到"307 字节 + 拷贝缓冲"。

### 3.5 SD 卡写入放大

- `pkg/ratelimit/store.go`：`State.prune()` 改为返回"是否有裁剪"；`FileStore.Save()` 用 `json.Marshal`。
- `pkg/ratelimit/limiter.go`：`loadState()` 只在裁剪确有变化时写盘。
  单次操作的写盘次数由 2 次（loadState 保存 + recordLocked 保存）降为 1 次。
- `xiaohongshu/action_state.go`：`saveLocked()` 用 `json.Marshal`（原 `MarshalIndent`）。

### 3.6 身份指纹采集节流

- `service.go`：新增 `identityMu / identityCheckedAt / identityCheckInterval` 与 `identityProbeDue()`；
  `checkFixedIdentity()` 在间隔内直接返回。先记录时间戳再采集，采集失败也不会在每个操作上重试。
- `configs.IdentityCheckInterval()`：默认 10m，`0` 表示每次都采集，非法/负值回落默认。
- 语义不变：指纹漂移仍然只告警不阻断；`XHS_FIXED_IDENTITY=0` 仍然整体关闭。

### 3.7 network capture 泄漏修复

- `service.go` `GetFeedDetailCommentsBatch()`：`capture` 用 `defer` 兜底停止，
  避免 panic 路径漏掉 `Stop()` 导致 `EachEvent` goroutine 与 ctx 常驻。

### 3.8 状态目录降级

- `xiaohongshu/action_state.go`：`NewActionStateStore()` 先 `MkdirAll`，再用临时文件
  **真探测**目录是否可写（`dirWritable`），不可写则回退 `os.TempDir()/xiaohongshu-mcp-action-state`，
  两处都不可用才返回错误。与 `pkg/ratelimit` 已有的「持久化不可用就降级内存存储」策略一致。

### 3.9 构建与部署

- `.github/workflows/build.yml`：`CGO_ENABLED=0` + `-trimpath -ldflags="-s -w"`，并打印产物大小。
- `docker/docker-compose.yml`：`shm_size: "256m"`。
- `docs/docker-shm-note.md`：修正"项目不再依赖 `--disable-dev-shm-usage`"的错误说法 ——
  rod v0.116.2 的 `launcher.New()` 默认仍带该 flag，仓库没有任何地方删它。

---

## 4. 为什么不做这些（避免踩到已有的设计决策）

| 候选 | 不做/不默认做的理由 | 证据 |
|:--|:--|:--|
| 默认加 `--disable-gpu` | 本机实测进程数与 RSS 无可测差异；而它曾在 `7722fad` 被作者一次性移除（同批"恢复上游原版参数"） | 本机探针 + `git show 7722fad` |
| 默认加 `--disable-software-rasterizer` | 实测 WebGL 直接不可用，是强自动化特征 | 本机探针 |
| 缩短拟人等待 / 阅读时长 / 限流阈值 | 这些是防封禁设计的组成部分（`docs/fixup-plan.md`），不是性能缺陷 | `docs/fixup-plan.md` |
| page 复用池（省掉每次新建 renderer） | 改的是浏览器生命周期与 BusyError 语义，本机没有真机条件验证，风险大于收益 | 见测试文档 §7 后续项 |
| 调 `WaitForXHSReady` 轮询/稳定窗 | 正确性关键路径，`ready_test.go` 密集锁定；要么真机标定要么不动 | `xiaohongshu/ready_test.go` |
| 关掉 rod 默认的 3 个"不降频" flag | headless 自动化依赖 renderer 不被节流，收益未验证 | 子代理报告 §2.2 |

---

## 5. 配置速查（树莓派 3B 推荐）

```bash
# 低资源档（arm/arm64 默认已开，这里显式写出来便于审计）
XHS_LOW_RESOURCE=1
XHS_BROWSER_JS_HEAP_MB=192          # 内存紧张可降到 128
XHS_BROWSER_RENDERER_LIMIT=2        # 极紧可试 1，注意页面可能变慢
XHS_BROWSER_BLOCK_URLS=-            # 需要真正加载图片/视频（例如要人工核对页面）时用 "-" 全部放行
XHS_GO_MEMLIMIT=128MiB
XHS_IDENTITY_CHECK_INTERVAL=10m
XHS_BROWSER_IDLE_TIMEOUT=30m        # 见测试文档 §7：Pi 上重启 Chromium 很贵
```

`XHS_BROWSER_IDLE_TIMEOUT` 仍是上游默认 5m（本次未改默认值，只写进推荐配置）：
5 分钟回收后下一次调用要重新付 Chromium 冷启动（`defaultStartupTimeout=120s` 暗示了量级），
是否拉到 `30m`/`0` 需要在真机上用内存曲线换延迟来定，属于需要标定的决策。

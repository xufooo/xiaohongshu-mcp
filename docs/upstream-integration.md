# 上游 main 融入清单（逐提交判定）

本文记录**我们这条 Pi 线**（`perf/pi3-optimize`）与**上游 main**（`xpzouying/xiaohongshu-mcp`，已同步到我们 fork 的 `main`）的差异，
以及每个上游提交「已有等价 / 需融入 / 架构绑定不适用」的判定依据。

- 同步状态：`main` = `aad2a3d`（2026-09-21 从上游快进 11 个提交后推送）
- 分叉：共同祖先 `ec9b84b`；**main 独有 62 个提交，我们独有 532 个**
- 结论：**不能整体 merge**（两条线的 browser / 交互 / 会话架构不同），逐条"看情况"处理。

## 1. 为什么不能整体合并（架构差异）

| 维度 | 上游 main | 我们（Pi 线） |
|:--|:--|:--|
| 浏览器来源 | **自带固定版本**（`browser_version.txt` = 148.0.7778.215，自建 CDN 下载 + SHA256 校验，`EnsureBrowser`） | 外部二进制：`--bin` / `ROD_BROWSER_BIN`（Pi 上用 CloakBrowser arm64） |
| 平台支持 | `platformAsset()`：linux **仅 amd64**、darwin 仅 arm64、windows 仅 amd64；不支持就 `panic("内置浏览器不可用，拒绝启动")` | **arm64 可用**（CloakBrowser 有 Linux ARM64 构建，实测 CDN 200 / 198 MiB） |
| 指纹 | 默认开（`WithFingerprint("")` + `WithStealthJS(false)`），无 mode 开关 | `XHS_BROWSER_MODE=cloak` 才开，另有 `XHS_BROWSER_USER_AGENT` 互斥逻辑 |
| 资源策略 | 无 | 低资源档：JS heap 192MB、renderer 限 2、图片/媒体 URL 拦截、空闲回收 30m |
| 页面生命周期 | 无 manager（每次 `NewBrowser`） | `browser.Manager`（常驻 + owner/token + 热页面 TTL 缓存） |

> 关键事实：**上游 main 在树莓派 3B（linux/arm64）上起不来**（平台无预编译二进制 → panic）。
> 所以 Pi 线的浏览器与低资源改动不是重复劳动，是上游不覆盖的缺口。

## 2. 判定总表

### 2.1 已有等价（无需融入，逐条核过我们的代码）

| 上游提交 | 上游做的 | 我们的等价实现 |
|:--|:--|:--|
| `3474284` #791 | cookies 保存时创建父目录 | `cookies/cookies.go` 的 `write()` 已有同样的 `MkdirAll`（同样 6 行） |
| `b8412a2` #755 | 会话文件持久化 seed + cookies 路径优先级 | `LoadSeed/SaveSeed/ResolveFingerprintSeed` 与 `COOKIES_PATH` 优先逻辑均在 |
| `85aed8b` #741 | 登录状态返回真实账号 | `LoginAction.CurrentUser` 读 `__INITIAL_STATE__.user` |
| `37719be` #761 | 点击前校验落点，够不到就报错 | `humanize` 的 `ensureClickable`/`ensurePointInViewport` 已有 |
| `d7e8265` #763 | 点完筛选后等结果真换批再读 | `readFeedIDs` + `waitFeedsChanged` |
| `2bb8df1` #760 | 筛选项按文本匹配 | `findFilterOption`（按文本/标签查找，非下标） |
| `c17940f` #789 | 通知读取/回复/点赞 | 我们的通知工具链（`like_notification`/`reply_notification`/`get_unread_count`/`list_notifications` + tab 解析） |
| `d7f176a` #790 | 主页按 tab 读笔记/收藏/点赞 | `GetMyProfile`/`GetMyProfileViaSidebar` + `activeTab.query`（note/fav）；**`ParseProfileTab`/`TabFavorites` 命名不同，行为待逐项比对** |

### 2.2 需融入（已做 / 待做）

| 上游提交 | 内容 | 我们的状态 | 处理 |
|:--|:--|:--|:--|
| `085da32` #776 | 搜索与列表只返回笔记（滤掉 `live_v2`/`hot_query`） | **缺**（无 `onlyNotes`） | ✅ **已融入**：`onlyNotes` 放在两处「JSON → []Feed」解码边界（`readHomeFeedsFromState`、`extractSearchFeedSources`），比上游的两处调用点更彻底，DOM/state 两路与 session/legacy 全部覆盖；视频笔记按 `modelType` 不误伤，已加测试 |
| `0033dc7` #775 | 取二维码不再堆积浏览器实例（同一时刻只留一个待扫码会话，新的取消旧的） | 改前我们更差：第二次调用直接 `browser busy: owner=login_qrcode_wait`，且 4 分钟内所有工具被饿死 | ✅ **已融入且更优**：`loginQrcodeSession` 登记 + `Manager.Detach`（页面专用、归还独占权）。实测：首次 7.0s 出码，第 2/3 次 **0.2s/0.3s 复用同一张码**，期间 `check_login_status` 正常返回（不再 busy），3 次调用始终 1 个浏览器实例。上游是"取消重建、重新出码"，我们复用同一张码且不阻塞其他工具 |
| `2a57ab4` #792 | publish 正文输入框改轮询定位，不再 panic | 我们更差：正文只给 5s 单次查找；标题/上传输入继承页面的 300s（真失败拖 5 分钟） | ✅ **已融入且更优**（`e9ec2be`）：统一 `publishControlMountBudget = 30s` + `waitPublishControl`（rod 自带重试退避，命中零开销，失败抛「选择器+预算+已等时长」）。**不采用**上游的 `contentElemSelectors` 多选择器列表 —— 那是我们刻意删掉的投机分支 |
| `d680e83` #752 | 笔记详情返回视频信息 | 我们有 `NoteCard.Video`（参与 `fillMissingFeedFields` 合并，**不能删**），但 `openedNoteFeedDetail` 构造详情时不填 video | ❌ **不融入**：属新功能（无调用方需求）。上游是把整个 video 流结构映射出来（+112 行），我们详情是 DOM 优先，补它属于新增能力而非修缺陷 |
| `9315948` #771 | go-sdk 升级 v1.4.0 + 开启 Stateless | go-sdk 已是 v1.4.0 | ✅ **已融入**：`StreamableHTTPOptions{Stateless: true}`。实测：原有客户端照常（`check_login_status` 1.4s、`start_page` 5.0s），且**不 initialize、不带 session 头的裸 POST 也能直接调工具**（HTTP 200 + JSON-RPC 结果，响应无 `Mcp-Session-Id`）。传输层无状态 → Pi 上少一份会话账本 |
| `af95a53` #770 | 评论区较短时也展开楼中楼 | 我们的加载循环 break 后固定走收尾 `clickMoreReplies`（`feed_detail.go` 主循环 + 收尾两段），不存在"判定加载完成排在展开之前"的顺序问题 | ✅ **无需融入**（结构不同，已在源码核对） |
| `9b2a861` #751 `aa93e0a` #764 | 先查后判；**查找评论时展开楼中楼** | `findCommentElement` 循环首个动作就是匹配 Eval（先查后判已等价）；但**查找过程不会展开折叠的楼中楼**——comment_id 正确也会滚到底报"未找到" | ✅ **已融入 #764**：新增 `expandVisibleReplies`（只挑**视口内**的 `.parent-comment > .children-comments/.reply-container .show-more`，**不滚动**、不按回复数阈值跳过，元素级 `ClickNoScroll`），查找循环里"查不到 → 先展开 → 立刻复查"，并加 `maxExpandRounds=5` 防止大楼层吃光预算。5 例合成页面验证（视口内选中 / 视口上下与"收起"不选 / 大楼层不按数量跳过）+ 形态测试 |
| `cbcaec5` #745 | 评论滚动改用真实滚轮 | 上游修的是 `dispatchEvent(WheelEvent)`（合成事件**根本不滚动**）；我们用的是容器 `scrollBy`（真实滚动），评论分批加载实测正常 | ✅ **不适用**（我们的做法不是它修的那个 bug） |
| `98d65a4` #748 | reply 工具补 panic 保护 | 22 个工具 / 22 处 `withPanicRecovery`，无遗漏 | ✅ **已有等价** |
| `a5bb5b8` #778 | 写操作确认后补足停留 | `like/favorite` 后 2–5s；通知点赞 400–800ms；通知回复 300–600ms / 500–1200ms | ✅ **已有等价** |
| `058e7b3` #754 | 收口裸鼠标调用 | 只有 `page.Actor().Mouse.Scroll`（我们 humanize 的 API），无裸 rod 鼠标调用 | ✅ **已有等价** |
| `a007e5a` #759 | 点击补按下停留与落点抖动 | `ClickWithOptions` 按下→释放间隔 40–120ms，移动带随机偏移 | ✅ **已有等价** |
| `4082976` #746 | 输入改 CDP InsertText | 源码核对我们的 `humanize.Keyboard.Type`：ASCII **每字符 1 次 CDP**（只发 keyDown，带 text，无 keyUp）；CJK 按**片段**走 IME 组合事件（`insertCompositionText`），调用次数**少于**每字符 1 次 | ❌ **不融入**：上游改成"每字符 InsertText"后，ASCII 与我们**相同**（1 次/字符）、CJK 反而**比我们多**；而我们保留 IME 组合事件更接近真人。真实发布已多次验证标题/正文写入成功，无缺陷可修 |
### 2.3 架构绑定 / 不适用

| 上游提交 | 原因 |
|:--|:--|
| `4ceb2c3` #753、`a7d1f2f` #737 | 内置浏览器下载与"唯一来源"策略：**arm64 不支持**，与我们外部二进制 + CloakBrowser 路线冲突 |
| `f44b4ed` #772 | HTTP API 对齐 MCP 能力：我们的 API 面不同（且 Pi 上主要用 MCP） |
| `16ec75c` #803 | 可选 Token 鉴权：Pi 本地部署用不上，按需再评估 |
| `f5af1db`/`c2fc4dd` #793/#794 | Docker tini / subreaper：我们是裸机部署 |
| `bd729d3`/`59762c8`/`84511f1`/`31008aa`/`80484fe` | CI / 发布流水线：fork 有自己的 CI（Build Check） |
| docs/注释/测试精简类（约 20 个） | 无行为影响 |

## 3. 融入原则（本清单的判据）

1. **只按行为融入，不照搬补丁**：上游文件结构与我们不同（如 `login_session.go` vs 我们的 `browser.Manager`），融入的是**问题的解法**，不是 diff。
2. **先证明我们确实有这个问题**：能实测/读码证明才动（如 #791 经核对已有等价，直接跳过）。
3. **收口在单一边界优于多调用点**：#776 我们放在解码边界，比上游的两处调用点覆盖更全。
4. **每批都要跑 CI + 单测**，并记录在本文件（状态列）。

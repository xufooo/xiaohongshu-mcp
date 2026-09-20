# xiaohongshu-mcp 树莓派 3B 优化测试文档

- 分支：`perf/pi3-optimize`（基线 `fixup-test` @ `0646295`，改动见 [`pi3b-optimization.md`](./pi3b-optimization.md)）
- 目标设备：Raspberry Pi 3B，4×Cortex-A53 @1.2GHz，1GB RAM，arm64（Raspberry Pi OS 64-bit），无 swap
- 编写日期：2026-09-20
- 证据等级标注：**[源码]** 直接读到代码 / **[本机实测]** 开发机 x86_64 上真实运行取数 / **[推断]** 由代码推导 / **[待真机]** 需要树莓派实测才能定论

---

## 1. 目的与范围

### 1.1 要验证什么

1. 本次资源优化**不破坏任何既有功能**（回归面）；
2. 低资源档的配置解析、flag 组装、资源拦截、运行时内存上限**按设计生效**；
3. 图片下载、限流落盘、身份采集节流、network capture 的改动**语义等价**；
4. 在真实 Chromium 上，服务能起来、健康检查通过、登录二维码链路可用；
5. 给树莓派真机一套**可直接照做**的验收步骤与判据。

### 1.2 明确不在范围内

- 需要**已登录小红书账号**的端到端功能（搜索/详情/点赞/评论/发布）。原因见 §2.3。
- 防封禁策略（拟人等待、阅读时长、限流阈值）的行为变更 —— 本次**没有改**这些默认值，因此不需要重新标定。
- 树莓派上的绝对内存/延迟数字 —— 本机没有 Pi，只能给出「怎么测 + 判据」。

---

## 2. 环境与前置条件

### 2.1 开发机（本次实际执行环境）

| 项 | 值 |
|:--|:--|
| OS | Manjaro Linux（rolling），x86_64 |
| 浏览器 | `google-chrome-stable 153.0.8010.52`（`/usr/bin/google-chrome-stable`） |
| Go 工具链 | **本机未安装**（按要求不装），编译与测试全部走 GitHub Actions |
| CI | `gh` CLI 已登录 `xufooo`，具备 `repo` + `workflow` scope |
| 用于取数的 Node | `/opt/dsh-desktop/resources/app/node_modules/node/bin/node`（v24.9.0） |

CI workflow：`.github/workflows/build.yml`（name: **Build Check**）

- `verify` job：`go test ./...` + `go build ./...` + `third_party/headless_browser` 单独 `go mod tidy && go test ./...`
- `build` matrix：`linux/windows/darwin × amd64/arm64`，上传 6 个 artifact
- 手动触发：`gh workflow run "Build Check" --ref perf/pi3-optimize`（`workflow_dispatch` 对任意分支生效）

### 2.2 编译与测试的入口（不装 Go）

```bash
cd /home/ooo/Works/xiaohongshu-mcp

# 1) 推送分支（未经同意不要推 fixup-test / main）
git push origin perf/pi3-optimize

# 2) 触发 CI
gh workflow run "Build Check" --ref perf/pi3-optimize

# 3) 看结果
gh run list --workflow build.yml --limit 3
gh run view <run-id> --log-failed          # 失败时看日志

# 4) 下产物（linux/arm64 给树莓派，linux/amd64 给本机验证）
gh run download <run-id> -n xiaohongshu-mcp-linux-arm64 -D /tmp/xhs-arm64
gh run download <run-id> -n xiaohongshu-mcp-linux-amd64 -D /tmp/xhs-amd64
```

### 2.3 已知环境约束（会影响能测到什么）

| 约束 | 事实 | 影响 |
|:--|:--|:--|
| 出口 IP 被小红书风控拦截 | **[本机实测]** 访问 `https://www.xiaohongshu.com/explore` 会 302 到 `/website-login/error?...error_code=300012&error_msg=IP存在风险…`，`document.title` = 「安全限制」 | 本机无法用真实信息流页面做功能/内存取数 |
| 无已登录 cookies | 用户确认没有可提供的登录态 | 搜索/详情/互动/发布链路**本机不可端到端验证** |
| 无树莓派可用 | 用户确认只能本机 x86 + Chrome 验证 | 所有 Pi 专属数字标 **[待真机]**，不给结论性数值 |
| 本机未装 Go | 按要求 | 单测/编译只能靠 CI，本地只用 `gofmt` 校验格式 |

> CI 上的 `go test ./...` 是本次唯一的自动化回归闸门：**143 个既有测试 + 本次新增 ~14 个测试**。

---

## 3. 变更 → 测试项映射（不漏项）

| # | 变更 | 代码位置 | 验证层级 | 具体测试项 |
|:--|:--|:--|:--|:--|
| C1 | 低资源档判定（`XHS_LOW_RESOURCE`，arm 默认开） | `configs/browser.go` | L1 单测 | T1-1 |
| C2 | V8 堆上限 / renderer 上限解析 | `configs/browser.go` | L1 单测 | T1-2 |
| C3 | flag 组装（含"不得出现 WebGL 杀手 flag"） | `third_party/headless_browser` | L1 单测 | T1-3 |
| C4 | URL 拦截模式解析（显式 / `-` / 默认含图片 CDN / 非低资源） | `configs/browser.go` | L1 单测 | T1-4 |
| C5 | `Network.setBlockedURLs` 实际可用性 | `third_party/headless_browser` | L2 探针 | T2-2 |
| C6 | 低资源档真实生效（进程数 / RSS / WebGL 未被破坏） | 全部 | L2 探针 | T2-3 |
| C7 | Go 运行时内存上限解析 | `configs/runtime.go` | L1 单测 | T1-5 |
| C8 | 图片下载流式化（分块、超限、正常） | `pkg/downloader/images.go` | L1 单测 | T1-6 |
| C9 | 限流裁剪无变化不写盘 | `pkg/ratelimit/*` | L1 单测 + 代码审查 | T1-7 |
| C10 | 身份采集节流 | `service.go` / `configs` | L1 单测 + L2 日志 | T1-8 / T2-4 |
| C11 | network capture panic 路径停止 | `service.go` | L1 回归（既有测试）+ 代码审查 | T1-9 |
| C12 | 构建裁剪（`-trimpath -s -w`） | `build.yml` | L2 CI | T2-1 |
| C13 | docker `shm_size` / 文档纠错 | `docker/`、`docs/` | L2 人工 | T3-6 |
| C14 | 全部既有功能不回归 | 全仓 | L1 CI | T1-10 |

---

## 4. L1：单元测试与 CI（自动闸门）

### 4.1 执行方式

CI 的 `verify` job 覆盖；本机不装 Go，因此**以 CI 输出为准**。失败定位：

```bash
gh run view <run-id> --log-failed
```

### 4.2 本次新增测试清单

| 编号 | 测试函数 | 文件 | 断言要点 |
|:--|:--|:--|:--|
| T1-1 | `TestLowResourceProfileExplicit` | `configs/browser_test.go` | `XHS_LOW_RESOURCE=1` 开；`0/false/off/no` 全关（压过架构默认） |
| T1-2 | `TestBrowserJSHeapMB` | `configs/browser_test.go` | 显式值优先；`0`/非法值回落；低资源档 192；非低资源档 256 |
| T1-2 | `TestBrowserRendererLimit` | `configs/browser_test.go` | 默认 2；显式覆盖；负数回落 2 |
| T1-4 | `TestBrowserBlockedURLPatterns` | `configs/browser_test.go` | 显式逗号列表（含空格）；`-` = 不拦截；空值按未设置；非低资源档返回 nil；**默认档必须含 `sns-webpic` 且不得含 `fe-static`** |
| T1-8 | `TestIdentityCheckInterval` | `configs/browser_test.go` | 默认 10m；`0` = 每次；`90s` 解析；非法/负值回落 |
| T1-5 | `TestParseByteSize` | `configs/runtime_test.go` | `128MiB`/`128MB`/`1GiB`/`512KiB`/纯字节/`64B` 正确；空串与 `abc` 报错 |
| T1-3 | `TestApplyLowMemoryLauncherProfile` | `third_party/headless_browser/headless_browser_test.go` | 5 个固定 flag 在位；`js-flags=--max-old-space-size=192`；`renderer-process-limit=2`；**断言 `disable-gpu`/`disable-software-rasterizer` 不在默认档** |
| T1-3 | `TestApplyLowMemoryLauncherProfileDefaults` | 同上 | 参数为 0 时回落 256 / 2 |
| T1-3 | `TestWithLowMemoryProfileConfig` / `TestWithBlockedURLsDefensiveCopy` | 同上 | 选项写入 Config；切片防御性复制；nil 等同不拦截 |
| T1-6 | `TestDownloadImageChunkedStreams` | `pkg/downloader/images_test.go` | 无 `Content-Length` 的分块响应也能落盘，字节与源完全一致 |
| T1-6 | `TestWriteImageStreamWithinLimit` | 同上 | 头部 + 剩余流拼接后内容一致 |
| T1-6 | `TestWriteImageStreamRejectsOversize` | 同上 | 超限报错含 `exceeds size limit`；**不留目标文件也不留 `.tmp`** |
| T1-6 | `TestReadImageBodyWithinLimit/ExceedsLimit`（既有） | 同上 | 保留的辅助函数语义未变 |
| T1-6 | `TestDownloadImageSuccessAndLimits`（既有） | 同上 | Content-Length 超限提前拒绝；错误信息不泄漏 token |

### 4.3 既有回归测试（必须全绿，共 143 个 + 子测试）

重点回归面（与本次改动最近的）：

| 既有测试 | 为什么相关 |
|:--|:--|
| `xiaohongshu/session_optimization_test.go`（6 个） | 真实文件持久化的 ActionState 阈值/累计语义；改动只动了序列化格式 |
| `xiaohongshu/ready_test.go`（7 个） | 就绪轮询与稳定窗未被改动 |
| `humanize/humanize_test.go`、`humanize/rod/hrod_test.go` | 拟人层未被改动 |
| `browser/browser_test.go` | 浏览器选项装配 |
| `service_p0_test.go` | 服务层 P0 行为 |

### 4.4 通过判据

```
verify job: success
build job (6 个平台): success
第三方模块 verify（third_party/headless_browser）: success
```

### 4.5 实测记录（本次）

| 运行 | commit | 结果 |
|:--|:--|:--|
| 基线 | `0646295`（分支刚建，无改动） | `Build Check` success，artifact 6 个均已生成 |
| 优化后 | `7dab19b` | 见 §6 执行记录（CI 结果回填） |

---

## 5. L2：本机浏览器验证（真实 Chromium）

### 5.1 T2-1 产物存在与体积

```bash
gh run download <run-id> -n xiaohongshu-mcp-linux-amd64 -D /tmp/xhs-amd64
ls -l /tmp/xhs-amd64/xiaohongshu-mcp-linux-amd64
```

判据：文件存在、可执行、体积为"裁剪后"的量级（对比基线运行同平台 artifact 的 `ls -l`；
`-s -w` 去符号表 + `-trimpath`，**[本机实测]** 预期显著小于基线）。
CI 日志里现在会打印该文件大小（`Build` 步骤末尾 `ls -l`），可直接比对。

### 5.2 T2-2 资源拦截命令可用性（独立探针）

用自建 CDP 探针（不属于仓库文件，测试脚本用完即删）：

```bash
cd /tmp && cat > cdp-probe.mjs <<'EOF'   # 完整脚本见本文件附录 A
EOF
node cdp-probe.mjs "" "https://www.xiaohongshu.com/explore" "baseline"
```

判据：

- `setBlockedURLsWithoutEnable == "ok"` → **不需要先 `Network.enable`**，实现路径成立；
- `probe.webgl.renderer` 含 `SwiftShader` → 基线 WebGL 正常。

**[本机实测]** 结果：`ok` / `ok`，WebGL = `ANGLE (Google, Vulkan 1.3.0 (SwiftShader Device (Subzero)…))`。

### 5.3 T2-3 低资源档真实生效 + 不破坏 WebGL

```bash
node cdp-probe.mjs "" "https://www.xiaohongshu.com/explore" "baseline"
node cdp-probe.mjs "--js-flags=--max-old-space-size=192|--renderer-process-limit=2" "https://www.xiaohongshu.com/explore" "lowmem"
```

判据（**[本机实测]** 已通过，数据见 `pi3b-optimization.md` §2）：

| 检查 | 基线 | 低资源档 | 判据 |
|:--|--:|--:|:--|
| 空页进程数 | 12 | 10 | 下降 |
| 加载后进程数 | 13 | 11 | 下降 |
| 加载后 RSS | 1,912,176 KB | 1,661,696 KB | 下降（约 −13%） |
| WebGL renderer | SwiftShader | SwiftShader | **必须仍然可用** |

反例（必须**不**出现在默认档）：

```bash
node cdp-probe.mjs "--disable-software-rasterizer" "about:blank" "swrast"
# 期望观察到 probe.webgl.error == "no webgl context" → 这就是它不进默认档的原因
```

### 5.4 T2-4 服务起来 + 登录二维码链路 + 低资源档日志

```bash
rm -rf /tmp/xhs-data && mkdir -p /tmp/xhs-data
ROD_BROWSER_BIN=/usr/bin/google-chrome-stable \
COOKIES_PATH=/tmp/xhs-data/cookies.json \
XHS_BROWSER_PROFILE_DIR=/tmp/xhs-data/browser-profile \
XHS_BROWSER_IDLE_TIMEOUT=2m \
XHS_LOW_RESOURCE=1 \
/tmp/xhs-amd64/xiaohongshu-mcp-linux-amd64 -port :18060 > /tmp/xhs-data/server.log 2>&1 &
sleep 3
curl -s http://127.0.0.1:18060/health
```

判据：

1. 日志出现 `low resource profile enabled: js_heap_mb=192 renderer_limit=2`（**[源码]** `browser/browser.go`）；
2. 日志出现 `blocking 8 URL patterns per page`（低资源档默认：图片 CDN + 视频分片）；
3. `/health` 返回 200；
4. 首次调用后 `ps -eo args | grep chrome` 能看到 `--js-flags=--max-old-space-size=192`
   与 `--renderer-process-limit=2` 真的传给了 Chromium；
5. 触发一次需要页面的调用，确认日志里**没有** `set blocked URLs failed` 告警
   （有告警说明拦截命令被拒，需要回退到"先 `Network.enable` 再 set"的实现）。

```bash
# 触发浏览器（扫码登录链路，不需要已有账号）
curl -s http://127.0.0.1:18060/api/v1/login/status
curl -s http://127.0.0.1:18060/api/v1/login/qrcode | head -c 200
grep -c "set blocked URLs failed" /tmp/xhs-data/server.log   # 期望 0
```

> 说明：`/api/v1/login/qrcode` 会返回 base64 二维码图片（`img` 字段）。
> 本机验证只需要"能取到二维码"，**不需要真的扫码**（没有账号，且出口 IP 被风控）。
> 若此调用返回风控错误，属于 §2.3 的环境约束，不判为回归。

### 5.5 T2-5 身份采集节流可观测

```bash
XHS_IDENTITY_CHECK_INTERVAL=0 ...   # 每次取页都采集
XHS_IDENTITY_CHECK_INTERVAL=10m ... # 默认：10 分钟内只采一次
```

判据：`XHS_IDENTITY_CHECK_INTERVAL=0` 时，连续两次需要页面的调用后，
`grep -c "browser identity" server.log` 的行为与默认档不同（默认档不重复打印）。
若两次调用间隔大于 10 分钟，默认档也应再次采集。

---

## 6. L3：树莓派 3B 真机验收（[待真机]）

> 本机没有 Pi，以下步骤是**可执行清单**；表里的数字留空，等真机填入后再判定。
> 所有命令都在 Pi 上以部署用户执行。

### 6.1 部署

```bash
# 在开发机
gh run download <run-id> -n xiaohongshu-mcp-linux-arm64 -D /tmp/xhs-arm64
scp /tmp/xhs-arm64/xiaohongshu-mcp-linux-arm64 pi@<PI_IP>:/tmp/xiaohongshu-mcp
scp -r /home/ooo/Works/xiaohongshu-mcp/deploy/... # 按需
```

```bash
# 在 Pi 上
sudo install -m 0755 /tmp/xiaohongshu-mcp /usr/local/bin/xiaohongshu-mcp
sudo mkdir -p /var/lib/xiaohongshu-mcp
```

### 6.2 记录基线（必须做，否则后面无法比较）

```bash
# 关掉低资源档跑一轮，作为 Pi 上的 A/B 基线
sudo systemctl stop xiaohongshu-mcp 2>/dev/null
ROD_BROWSER_BIN=/usr/bin/chromium-browser \
XHS_LOW_RESOURCE=0 \
XHS_BROWSER_PROFILE_DIR=/var/lib/xiaohongshu-mcp/browser-profile \
COOKIES_PATH=/var/lib/xiaohongshu-mcp/cookies.json \
XHS_BROWSER_IDLE_TIMEOUT=30m \
/usr/local/bin/xiaohongshu-mcp -port :18060 > /tmp/xhs-baseline.log 2>&1 &
```

采数（触发一次搜索/详情后再采）：

```bash
free -m                                   # Mem available
ps -eo pid,rss,comm | grep -i chrom       # 各 Chrome 进程 RSS（KB）
ps -eo args | grep -c '[c]hrome'          # 进程数
uptime                                    # load average
vcgencmd measure_temp                     # 温度（Pi 3B 降频相关）
cat /proc/<chrome-pid>/status | grep VmRSS
```

### 6.3 开低资源档复测

```bash
XHS_LOW_RESOURCE=1 XHS_BROWSER_JS_HEAP_MB=192 XHS_BROWSER_RENDERER_LIMIT=2 \
XHS_GO_MEMLIMIT=128MiB XHS_BROWSER_BLOCK_URLS=- \   # 先不拦媒体，单独量化 flag 收益
... 同上启动 ...
```

然后**单独**再跑一次带媒体拦截（去掉 `XHS_BROWSER_BLOCK_URLS=-`），对比：

| 指标 | 基线 | 低资源档 | 低资源档+媒体拦截 | 判据 |
|:--|--:|--:|--:|:--|
| Chrome 进程数 | | | | 基线 −2 左右 |
| Chrome RSS 合计 | | | | 明显下降 |
| Mem available | | | | 上升；不得出现 OOM |
| 首页/搜索页可见耗时 | | | | 不得劣化超过 20% |
| dmesg OOM 记录 | | | | 无 `Out of memory: Killed process` |
| 图片被拦后，笔记正文/评论/图片 URL 是否正常 | | | | 见 `pi3b-optimization.md` §2.4：真实页面实测提取面不变 |

### 6.4 真机验收判据（硬性）

1. `free -m` 的 available 在持续操作 10 分钟后 **不低于 120MB**；
2. `dmesg | grep -i "killed process"` **无** Chromium 被杀记录；
3. 搜索、打开笔记、读取评论、点赞、收藏、发布（图）**各至少 1 次成功**；
4. 图片/媒体拦截开启后，笔记**正文、评论、图片 URL 提取仍然正常**（真机复测 `pi3b-optimization.md` §2.4 的结论）；
5. 登录态在重启后仍有效（cookies 持久化未被破坏）；
6. 连续 20 次操作无 "browser busy" 之外的异常，无 goroutine 累积
   （`curl /health` 前后对比进程 RSS 无单调暴涨）。

### 6.5 T3-6 Docker 路径（若用 Docker 部署）

```bash
docker compose up -d
docker inspect xiaohongshu-mcp --format '{{.HostConfig.ShmSize}}'   # 期望 268435456（256MiB）
```

判据：`shm_size` 生效；容器内 `df -h /dev/shm` 显示 256M。

---

## 7. 未覆盖项与后续动作

| 项 | 现状 | 建议动作 |
|:--|:--|:--|
| 需要登录态的功能链路（搜索/详情/互动/发布） | **[待真机]** 本机无账号且 IP 被风控 | 在 Pi（家庭宽带 IP）上按 §6.4 逐项过一遍 |
| `XHS_BROWSER_IDLE_TIMEOUT` 默认值（5m） | 未改默认；Pi 上冷启动 Chromium 代价高 | 真机上测「5m vs 30m vs 0」的内存曲线与首字延迟，再决定默认 |
| page 复用池（省掉每次新建 renderer） | 未做 | 若真机实测「每次调用新建 page」是主瓶颈，再评估；需要重做大半生命周期测试 |
| `WaitForXHSReady` 轮询密度 | 未改 | 需要真机 CPU profile；改前先补测试 |
| `--disable-gpu` 在 Pi 上是否有收益 | 本机无可测差异 | 真机 A/B，用 `XHS_BROWSER_EXTRA_ARGS=--disable-gpu` 对比，注意指纹一致性 |
| 单进程模式 `--single-process` | 未做 | 真机实验项，renderer 崩溃会拖垮整个浏览器，谨慎 |
| CI 的 `gofmt` 门禁 | 未加 | 仓库现存大量未格式化文件，先单独一次「全仓 gofmt」提交，再加门禁 |

---

## 8. 回滚方案

优化全部集中在 `perf/pi3-optimize` 分支，且每一档都能用环境变量关掉，**不需要回滚代码**即可恢复上游行为：

```bash
XHS_LOW_RESOURCE=0          # 关掉低资源档（flag、资源拦截、Go 内存上限都不生效）
XHS_BROWSER_BLOCK_URLS=-    # 只关资源拦截
XHS_IDENTITY_CHECK_INTERVAL=0   # 恢复每次采集指纹
XHS_GO_MEMLIMIT=0           # 注意：0 表示"不限制"，等价于不设软上限
```

需要彻底回退时：

```bash
git checkout fixup-test     # 本地
# 远程分支保留，未推 fixup-test / main
```

需要注意的行为差异（不是回滚点，但要知情）：

- 限流状态文件与 ActionState 文件由"缩进 JSON"变成"紧凑 JSON"。**向下兼容**（仍是合法 JSON，
  旧文件照样能被 `json.Unmarshal` 读入），只是人工打开可读性下降。
- 低资源档在 `arm/arm64` 上**默认开启**。如果 Pi 上出现无法解释的页面异常，
  第一件事就是 `XHS_LOW_RESOURCE=0` 复现一次。

---

## 附录 C：为什么「树莓派的浏览器很慢」要当成一等约束（2026-09-20 实测）

### C.1 桌面上的实测事实（一手，内置浏览器 + 真实小红书）

| 观测 | 数值 | 一手性 |
|:--|:--|:--|
| 笔记详情页 DOM 规模 | 1,982 节点 | 实测 |
| 详情页单页传输 | 1.77 MB，其中图片 1.54 MB（**93%**） | 实测 |
| 搜索结果页传输 | 1.65 MB，**100% 是图片**（104 个资源全是 img） | 实测 |
| 该页字体 / 媒体请求 | **0 / 0** | 实测 |
| 详情就绪探针（模仿 `ready.go`） | **0.57 ms/次**（20 次均值，桌面） | 实测 |
| 真实详情页上一次 `Runtime.evaluate` | **两次连续 20s 超时**（renderer 卡死） | 实测 |
| 信息流页 `document.querySelectorAll` 全页扫描 | 28 张卡片 / 1,338 节点，探针 0.81 ms | 实测 |

### C.2 到 Pi 3B 上的放大倍数

**没有实测数据，不写数字。** 只能给方向：Cortex-A53 @1.2GHz 的单核性能远低于桌面核，
且 Pi 3B 内存只有 1GB、无 swap，页面渲染/JS 解析/图片解码都按数量级放大。
因此上面每一条「毫秒级」结论在 Pi 上都必须重新标定 —— 但**相对关系**成立：
探针便宜、渲染昂贵、图片占绝大部分。

### C.3 对流程优化的直接含义

在「浏览器很慢」这个前提下，**最贵的不是计算，而是重试**：一次失败重试 = 一次完整 SPA
重渲染 + 再次图片解码。所以省资源的顺序是：

1. **别加载不需要的东西** —— 图片占内容页 93~100% 的流量与解码开销；
   低资源档已默认拦图片 CDN（`392f600`），`XHS_BROWSER_BLOCK_URLS=-` 可放行。
2. **别做注定失败的探测** —— 单次 eval 最长 20s 才被判死；代码里已有的
   5s/2s eval 预算、`confirmRendererAlive` 双探针、`isConfirmedRendererDead` 熔断
   正是为这种卡死准备的，不要放宽。
3. **别反复冷启动 Chromium** —— 代码里 `defaultStartupTimeout = 120s`
   本身就说明 Pi 上启动是分钟级；空闲回收策略（默认 5m）要按「重启一次多贵」来权衡。
4. **别每次调用都新建 page/target** —— 现有 16 处 `acquirePageFor` 每次新建一个
   renderer；在慢机器上这是延迟与内存的双重放大器。

---

## 附录 B：生产 DOM 契约核验（2026-09-20，内置浏览器实测）

未登录状态下打开真实站点，逐个核对代码里写死的选择器（`xiaohongshu/ui_selectors.go`
与 `comment_feed.go` / `dom_extract.go` 里的内联选择器）。**全部命中**：

| 代码里的选择器 | 真实页面命中 | 备注 |
|:--|--:|:--|
| `#search-input-in-feeds, #search-input, #search-input-ai, input[placeholder*="搜索"]` | 1 | 信息流页 |
| `section.note-item, .note-item, .feeds-container section, .note-list section` | 28 | 信息流卡片数 |
| `.feeds-container, .note-list, .search-layout, div[data-v-]` | 1 | 结果容器 |
| `.note-detail-mask` / `.note-container` / `.interact-container` / `.comments-container` | 1 / 1 / 1 / 1 | 详情页四种就绪信号都可见 |
| `.note-scroller` | 1 | 评论滚动容器（`scrollNoteScroller` 依赖） |
| `div.input-box div.content-edit p.content-input` | 1 | 评论输入框 |
| `.btn.submit` | 1 | 评论提交按钮 |
| `.like-wrapper` / `.collect-wrapper` | 50 / 1 | 互动按钮 |
| `.show-more` | 7 | 「展开 N 条回复」，文本形如 `展开 36 条回复` |
| `.comments-container .parent-comment` | 10 | 父评论包装 |
| `:scope > .reply-container > .list-container > .comment-item` | **9** | 子评论（`feed_detail.go:630`、`dom_extract.go:78` 用的就是这个式子）✅ |
| `.children-comments > .comment-item-sub` | 0（兜底分支未用上） | 主式子已命中，兜底保留无害 |

真实评论树（一手）：

```
.comments-container
└── .list-container
    └── .parent-comment                  ×10
        ├── .comment-item                （父评论本体）
        └── .reply-container
            └── .list-container
                └── .comment-item.comment-item-sub   ×9
```

两条重要结论：

1. **子评论不在父 `.comment-item` 内部**，而在同级的 `.parent-comment > .reply-container > .list-container` 下。
   任何用 `parentComment.querySelector(':scope > .children-comments > …')` 的写法都取不到；代码用的是
   `.reply-container > .list-container > .comment-item`，实测命中 9 条 —— **代码是对的**（本次先怀疑后核验，
   避免了一个假 bug 报告）。
2. **DOM class 不能用来判断点赞状态**：详情页 DOM 上 `.like-wrapper` 带 `like-active`，
   而页面数据层 `__INITIAL_STATE__.note.noteDetailMap[].note.interactInfo` 是
   `{"liked":false,"collected":false,"likedCount":"3002","commentCount":"219",...}`。
   也就是说 `like-active` ≠ 已点赞。这与 `22d8c97`（交互状态改从页面数据层读取）的决策一致，
   后续不要再退回 DOM class 判定。

---

## 附录 A：CDP 探针脚本（测试用，不入库）

```javascript
// cdp-probe.mjs —— 用法: node cdp-probe.mjs "<flag1|flag2>" "<url>" "<label>"
// 依赖 Node >= 22（内置 WebSocket）。输出 JSON：RSS、进程数、WebGL、setBlockedURLs 可用性。
import { spawn } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { execFileSync } from 'node:child_process'

const CHROME = '/usr/bin/google-chrome-stable'
const extraFlags = (process.argv[2] || '').split('|').filter(Boolean)
const targetURL = process.argv[3] || 'about:blank'
const label = process.argv[4] || 'run'
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const userDataDir = mkdtempSync(join(tmpdir(), 'xhs-perf-'))
const port = 9300 + Math.floor(Math.random() * 400)

const child = spawn(CHROME, [
  '--headless', '--no-sandbox', '--remote-debugging-port=' + port,
  '--user-data-dir=' + userDataDir, '--disable-dev-shm-usage',
  '--disable-features=site-per-process,TranslateUI', ...extraFlags, 'about:blank',
], { stdio: 'ignore' })

const sleep2 = sleep
async function devtoolsReady() {
  for (let i = 0; i < 150; i++) {
    try { const r = await fetch(`http://127.0.0.1:${port}/json/version`); if (r.ok) return await r.json() } catch {}
    await sleep2(200)
  }
  throw new Error('devtools 未就绪')
}
function totalRSS() {
  const ps = execFileSync('ps', ['-eo', 'rss=,args='], { encoding: 'utf8' })
  let kb = 0, procs = 0
  for (const line of ps.split('\n')) {
    if (!line.includes(userDataDir)) continue
    const m = line.trim().match(/^(\d+)/); if (!m) continue
    kb += Number(m[1]); procs++
  }
  return { kb, procs }
}
class CDP {
  constructor(ws) {
    this.ws = ws; this.id = 0; this.pending = new Map(); this.events = []
    ws.addEventListener('message', (ev) => {
      const msg = JSON.parse(ev.data)
      if (msg.id && this.pending.has(msg.id)) {
        const { resolve, reject } = this.pending.get(msg.id); this.pending.delete(msg.id)
        msg.error ? reject(new Error(JSON.stringify(msg.error))) : resolve(msg.result)
      } else if (msg.method) this.events.push(msg.method)
    })
  }
  send(method, params = {}) {
    const id = ++this.id
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject })
      this.ws.send(JSON.stringify({ id, method, params }))
      setTimeout(() => { if (this.pending.has(id)) { this.pending.delete(id); reject(new Error('timeout: ' + method)) } }, 30000)
    })
  }
}
const connect = (url) => new Promise((resolve, reject) => {
  const ws = new WebSocket(url)
  ws.addEventListener('open', () => resolve(new CDP(ws)))
  ws.addEventListener('error', () => reject(new Error('ws error')))
})

const result = { label, extraFlags, targetURL }
try {
  await devtoolsReady()
  const created = await fetch(`http://127.0.0.1:${port}/json/new?about:blank`, { method: 'PUT' })
  const pageInfo = await created.json()
  const cdp = await connect(pageInfo.webSocketDebuggerUrl)
  try { await cdp.send('Network.setBlockedURLs', { urls: ['*.mp4*'] }); result.setBlockedURLsWithoutEnable = 'ok' }
  catch (e) { result.setBlockedURLsWithoutEnable = 'error: ' + e.message }
  await cdp.send('Page.enable')
  const idle = totalRSS()
  result.idleRSSKB = idle.kb; result.idleProcesses = idle.procs
  const started = Date.now()
  await cdp.send('Page.navigate', { url: targetURL })
  for (let i = 0; i < 100; i++) { if (cdp.events.includes('Page.loadEventFired')) { result.loadEventFired = true; break } await sleep(150) }
  result.loadMs = Date.now() - started
  await sleep(4000)
  const after = totalRSS()
  result.afterLoadRSSKB = after.kb; result.afterLoadProcesses = after.procs
  const probe = await cdp.send('Runtime.evaluate', {
    expression: `(() => { let webgl = {}; try { const c = document.createElement('canvas');
      const gl = c.getContext('webgl') || c.getContext('experimental-webgl');
      if (gl) { const d = gl.getExtension('WEBGL_debug_renderer_info');
        webgl.vendor = d ? gl.getParameter(d.UNMASKED_VENDOR_WEBGL) : gl.getParameter(gl.VENDOR);
        webgl.renderer = d ? gl.getParameter(d.UNMASKED_RENDERER_WEBGL) : gl.getParameter(gl.RENDERER); }
      else { webgl.error = 'no webgl context' } } catch (e) { webgl.error = String(e) }
      return JSON.stringify({ userAgent: navigator.userAgent, hardwareConcurrency: navigator.hardwareConcurrency,
        webdriver: navigator.webdriver, webgl, url: location.href, title: document.title }); })()`,
    returnByValue: true,
  })
  result.probe = JSON.parse(probe.result.value)
  cdp.ws.close()
} catch (err) { result.fatal = String(err) } finally {
  try { child.kill('SIGKILL') } catch {}
  await sleep(500)
  try { rmSync(userDataDir, { recursive: true, force: true }) } catch {}
  console.log(JSON.stringify(result, null, 2))
  process.exit(0)
}
```

---

## 附录 D：功能全量扫描与 bug 清单（2026-09-20 起，进行中）

扫描方式：内置浏览器打开真实站点 → 每页**只打一次极小探针**（不做 `scrollIntoView`、不做长等待、不做循环基准）
→ 把命中数与代码里的选择器/数据路径逐一对照。证据等级：**[实测]** = 浏览器实测；**[源码]** = 读代码确认。

> **扫描环境更正（2026-09-20 晚）**：内置浏览器**本来就是已登录的真实会话** ——
> `__INITIAL_STATE__.user.loggedIn = true`、`userInfo.guest = false`、昵称「一画一话」、`userId 6523ebde…`。
> 因此本附录里凡此前标注「未登录」的观测，一律应按**已登录态**理解（这反而更贴近 MCP 的真实使用场景）。
> 之前 `/api/v1/login/status` 返回 `is_logged_in: false` 的是 **MCP 自己的 headless Chrome**（无 cookies 且出口 IP 被风控），
> 与内置浏览器不是同一个浏览器会话。

### D.1 已扫

| # | 功能 | 页面 | 代码依赖（[源码]） | 实测（[实测]） | 结论 |
|:--|:--|:--|:--|:--|:--|
| A | `list_feeds` | `/explore` | `SelectorFeedCard` 并集；`__INITIAL_STATE__.feed.feeds.value ?? ._value` | 卡片 **30**；`feed.feeds` = `_value array:33`；`like-wrapper` 30；`a[href="/notification"]` **2** | ✅ 正常 |
| B1 | `search_feeds`（结果） | `/search_result?keyword=咖啡&source=web_explore_feed&type=51` | `makeSearchURL` → `search_result_ai`；`search.feeds`；卡片并集 | 卡片 **22**；`search.feeds` = `array:22`；搜索框并集命中 1 | ✅ 正常 |
| B2 | `search_feeds`（**筛选**） | 同上 | `div.filter` 按钮 → `Hover()` → `.filter-panel` → `div.filter-panel div.filters` → `:scope > span` 分组标题 → `div.tags` 选项 | **选择器全部命中**：`.filter-panel` 在 hover 后挂载（`DIV.filter-panel > DIV.filter > DIV.search-layout__top`）；`div.filter-panel div.filters` = **5 组**，标签依次为 排序依据/笔记类型/发布时间/搜索范围/位置距离；选项文本与代码 `filterGroups` 一致 | ✅ 正常（**含一处需注意的真实结构**，见 D.3） |

**扫描中修正的两处我自己的误报**（都是一手复测推翻的）：

1. 早前记的「搜索页顶层没有 `search` 键」是错的 —— 那是 `Object.keys(...).slice(0,8)` 截断造成的假象；完整键列表里 `search`/`note`/`notification` 都在，`search.feeds` 实测可用。
2. 更严重的一处：B2 一开始被我判成「❌ BUG #1 筛选选择器漂移，链路必然失败」，**是误报**，已在 2026-09-20 当晚复测推翻。错因是探针方法：我在派发合成 hover 事件的**同一个同步块里立刻探测**，Vue 的浮层异步渲染还没挂载，于是拿到了「不存在」的假结论；随后用真实坐标点击（`elementFromPoint` 确认点确实落在 `SPAN > DIV.filter` 上）也没打开，是因为该浮层由 **hover（mouseenter）** 触发，而点击不触发它。
   正确复测方式：派发 `mouseover/mouseenter/pointerover/pointerenter` → **等 600ms** → 再探测，此时 `.filter-panel` 存在、5 组齐全。
   **教训（写进流程）**：对异步渲染的 UI，派发事件与探测不能放在同一个同步块；且「整份 HTML 里没有该 class」只在**当前状态**下成立，不能用来证明「该元素永不存在」。

### D.2 待扫

| # | 功能 | 目标页面 | 主要风险点 |
|:--|:--|:--|:--|
| C | `get_note_detail` / `open_note` / 评论分页 | `/explore/<id>`、`/search_result/<id>` | 评论树、`.show-more`「展开 N 条回复」、`.note-scroller` |
| D | `like_feed` / `favorite_feed` / `comment_feed` / `reply_comment_in_feed` | 详情页 | `.interact-container .left .like-wrapper/.collect-wrapper`、评论输入框、互动状态来源 |
| E | `check_login_status` / `get_login_qrcode` / `delete_cookies` | `/explore`、登录浮层 | `.main-container .user .link-wrapper .channel`、`.login-container .qrcode-img` |
| F | `user_profile` | `/user/profile/<id>` | 侧栏导航入口、`userPageData` 数据层 |
| G | `list_notifications` / `get_unread_count` / `like_notification` / `reply_notification` | `/notification` | `.notification-page`、tab、`.action-like`、`textarea.comment-input` |
| H | `publish_content` / `publish_with_video` | `creator.xiaohongshu.com` | 上传页 tab、标题/正文输入、发布按钮 |

### D.3 已核验的筛选面板真实结构（[实测]，未登录态，hover 后）

```
DIV.filter-panel                     ← hover「筛选」后异步挂载
  └── （挂在 DIV.filter > DIV.search-layout__top 下）
      div.filters × 5                ← 代码用 div.filter-panel div.filters 取，命中 5
        ├── span                     ← 分组标题：排序依据 / 笔记类型 / 发布时间 / 搜索范围 / 位置距离
        └── div.tags …               ← 选项，注意**每个选项渲染成 2 个 div.tags**
```

实测各组 `div.tags` 文本（重复即同一选项出现两次）：

| 分组 | div.tags 数 | 文本 |
|:--|--:|:--|
| 排序依据 | 10 | 综合×2、最新×2、最多点赞×2、最多评论×2、最多收藏×2 |
| 笔记类型 | 5 | 不限、视频×2、图文×2 |
| 发布时间 | 7 | 不限、一天内×2、一周内×2、半年内×2 |
| 搜索范围 | 7 | 不限、已看过×2、未看过×2、已关注×2 |
| 位置距离 | 6 | 不限×2、同城×2、附近×2 |

**这一条解释了 `findFilterOption` 为什么必须做命中校验**：`div.tags` 存在重复节点，按文本取到的第一个
可能不是可点击的那个，所以代码逐个 tag 用 `elementFromPoint` 复核命中——这个设计是**对的**，
不要为了「少一次 eval」把它简化掉（那会退化成点到重复节点上）。

### D.4 第二轮扫描结果（C/D 详情互动、E 登录、F 用户主页、G 通知、H 发布）

| # | 功能 | 代码依赖（[源码]） | 实测（[实测]，已登录态） | 结论 |
|:--|:--|:--|:--|:--|
| C/D | 详情 + 互动选择器 | `.interact-container .left .like-wrapper` / `.collect-wrapper`（`like_favorite.go`）；`.comments-container`；`.parent-comment`；`:scope > .reply-container > .list-container > .comment-item`；`.show-more`；`.note-scroller` | like=**1**、collect=**1**、`collect-wrapper` 全页=1、`comments-container`=1、`parent-comment`=10、`comment-item-sub`=10、`show-more`=10、`note-scroller`=1 | ✅ 命中且不歧义。「点开『展开 N 条回复』」的交互未测：10 个 `.show-more` 全不在视口内，需要滚动内部 `.note-scroller`，成本高 → 待测 |
| E | `check_login_status` | 导航 `/explore` → 睡 3s → `pp.Has(".main-container .user .link-wrapper .channel")` | 已登录态：该选择器命中 **2**（1 个可见 [64,523,16,18] + 1 个 0×0 克隆），`.login-container`=0 → 返回 true（正确） | ⚠️ **登出态无法在此验证**（不能登出用户的真实会话）。待验证问题：登出时 XHS 是否仍渲染侧栏「我」入口？若渲染则该判定会把「未登录」误报为「已登录」。验证法：Pi 上用干净 profile 跑一次 `check_login_status` 并同时抓该页 DOM |
| F | `user_profile` 入口 | `page.Element("div.main-container li.user.side-bar-component a.link-wrapper span.channel")` —— **取第一个匹配** | 命中 **2**：第一个 [64,523,16,18] **可见**，第二个 [0,0,0,0] **隐藏**；`a[href^="/user/profile/"]` 存在 | ⚠️ **潜在脆弱点（非当前故障）**：现在顺序恰好取到可见的，但顺序无契约保证。通知入口那边代码专门循环挑可见（`findVisibleNotificationEntry`），这里没有 → 建议对齐（低风险小改） |
| G | 通知页 | `.notification-page`；3 个 `.reds-tab-item.tab-item`；`.tabs-content-container .container`；`.action-like` / `.action-reply`；`textarea.comment-input`；入口 `a[href="/notification"]` | `notification-page`=1、tabs=**3**、items=**5**、`action-like`=5、`action-reply`=5、入口=**2**、`textarea.comment-input`=0 | ✅ 选择器全部有效；入口 2 个（代码已循环挑可见）；回复输入框按需挂载，与 `waitNotificationReplyInput` 设计一致 |
| H | `publish_content`（creator 域） | `div.creator-tab`（点击 tab）；首张图 `.upload-input`、后续 `input[type="file"]`；深层 `div.d-input input`、`xhs-publish-btn`、`.ql-editor`、`#creator-editor-topic-container` | `div.creator-tab`=**9**（3 个 tab × 3 个重复节点）；文件输入 `INPUT.upload-input > DIV.drag-over > DIV.upload-wrapper`=1；`xhs-publish-btn` / `div.d-input` / `.ql-editor` / topic-container 均=0（未上传内容时本就不存在） | ✅ 早期选择器有效。`getTabElement` 已按「可见 + 精确文本 + `elementFromPoint` 未被遮挡」筛选，能对付 9 个重复节点；深层流程需带图上传才能出现 → 待测 |

### D.5 第一轮整体观（结论）

1. **没有确认的 bug。** 所有被检查的选择器与数据路径在真实站点上均命中；`div.tags` / `div.creator-tab` 这类
   **重复节点**，代码都已有「逐个复核命中」的防御（这是作者踩过坑的设计，别为省一次 eval 简化掉）。
2. 两处曾被我判成 bug 的都已澄清：筛选面板 = 我的探针方法错（已更正）；登录判定 = 在已登录会话上无法验证登出态（列为待验证项）。
3. 值得动的不是选择器，而是**资源与流程**：图片占内容页 93~100% 流量（已拦）、冷启动代价、每调用新建 page、
   以及 F 那类「取第一个匹配」的一致性强化。
4. 仍未实测：C 的「展开回复」交互、H 的带图深层流程、以及需要真机才能量的所有资源数字。

### D.6 行为级验证（只读，2026-09-20）

**读全部评论 / 评论分页（`get_note_detail` 的评论链路）—— 懒加载已实测成立**

在笔记 `/explore/6a6eb85100000000250029ff`（"共 219 条评论"）上，把内部 `.note-scroller` 滚到底（`scrollTop = scrollHeight`）后**另起一次探测**（避免 D.1 里那个"同步块探测"的错误）：

| 指标 | 滚动前 | 滚动后 | 变化 |
|:--|--:|--:|:--|
| `.comment-item` | 19 | **33** | +14 |
| `.comment-item-sub` | 9 | 13 | +4 |
| `.show-more` | 10 | 8 | −2 |
| `.note-scroller` scrollHeight | 2,969 | **5,324** | +79% |
| `.end-container`（到底标记） | 0 | 0 | 未到底 |

结论（**[实测]**）：

1. 站点**确实**靠滚动内部 `.note-scroller` 懒加载评论 → 代码 `scrollNoteScroller` + 分批循环的形状是对的。
2. 一轮滚动约 +14 条评论、滚动高度 +79%；「共 219 条」意味着读满需要十几轮 → 该功能**天然昂贵**，
   在 Pi 上表现为 DOM 持续增长 + 图片/头像请求成批到来（低资源档的图片拦截正好覆盖这部分）。
3. **未做**：把 219 条读满（成本高，且代码单次预算 9 分钟）；这部分留到真机按 `max_items` 分批验收。

> 方法论提醒（本轮唯一一次有效的行为级测量）：滚动/点击之类的操作，**探测必须放到下一次调用**，
> 不能和触发动作放在同一个同步块（见 D.1 的误报）。

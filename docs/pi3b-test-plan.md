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

**用户主页提取（`user_profile`）—— 状态路径实测通过，但一度差点误报**

在 `/user/profile/6523ebde000000002b00267b`（已登录，作者「一画一话」）实测 `__INITIAL_STATE__.user`：

| 字段 | 实测值 | 对代码的含义 |
|:--|:--|:--|
| `userPageData` 顶层键 | `tags/tabPublic/posted/liked/collected/result/basicInfo/interactions` | 与 `parseUserProfileState` 期望一致 ✅ |
| `basicInfo` 键 | `imageb/nickname/images/redId/gender/ipLocation/desc` | 与 `UserBasicInfo` 一致；`nickname=一画一话` ✅ |
| `activeTab.query` | `"note"` | 代码按 `query != note` fail-closed 选分区，正好命中 ✅ |
| `userPageData.posted` | **17** | 该账号公开笔记 17 篇 |
| `userPageData.liked` / `.collected` | 49 / 21 | 其它 tab 的计数 |
| `user.notes` 的 `length` | **5** | ⚠️ **不能按 length 判断条数**：`notes[0]` 自身是带 `0..7` 数字键的嵌套结构（分栏/分页），不是 5 篇笔记 |
| DOM `section.note-item` 计数 | 滚动前 17 → 滚动后 **9** | 网格有回收/复用，**不能按 DOM 数判断条数** |

**结论**：该工具的状态路径与分区选择实测有效；`notes` 为嵌套结构（配合 `posted=17` 可解释），
**不是 bug**。同时得到两条禁止用法：不要用 `notes.length` 当笔记条数，也不要用 DOM 卡片数当条数。

> 附：本轮扫描里第三次「差一点误报」——前两次是筛选面板与登录判定。三次都靠**再下钻一层**澄清
> （异步渲染 / 隐藏克隆 / 嵌套结构）。**方法论**：把差异当 bug 之前，至少再下钻一层数据结构或交互状态。

**`open_note`（P0：从信息流卡片点击进详情）—— 行为级实测通过**

在 `/explore` 上对第一张卡片执行真实点击（目标 `section.note-item a.cover`，即代码 `findFeedCardAnchor` 使用的同一类锚点）：

| 观测 | 结果 | 含义 |
|:--|:--|:--|
| URL | `/explore/6a91523c0000000003029840?xsec_token=…` | 点击后带 token 进入详情 ✅ |
| 标题 | 笔记标题（`oots上海南京路 - 小红书`） | 页面确实切换 ✅ |
| `.note-detail-mask` | **1** | 详情以**浮层**打开 —— 正是 `SelectorFeedDetailReady` 期望的形态 ✅ |
| `.note-container` / `.interact-container` / `.comments-container` | 1 / 1 / 1 | 四个就绪信号齐全 ✅ |
| 背后 `section.note-item` | 仍 30 张 | 信息流未被销毁，未触发整页跳转 ✅ |

结合此前直接导航 `/explore/<id>` 也能以**独立页**形态打开（`.note-detail-mask`=1 且四信号齐全）：
**两条进入路径在线上都成立**，`SelectorFeedDetailReady` 的并集写法同时覆盖了「卡片点击的浮层」与「直接 URL 的独立页」两种形态 —— 设计正确。

### D.8 行为级验证进度小结

| 功能 | 选择器级 | 行为级（只读） | 行为级（写） |
|:--|:--|:--|:--|
| `list_feeds` | ✅ | ⏸ 未做（翻页） | — |
| `search_feeds` + 筛选 | ✅ | ⏸ 未做（翻页/筛选生效） | — |
| `get_note_detail` 读评论 | ✅ | ✅ **懒加载成立**（滚一次 +14 条，scrollHeight +79%） | — |
| `open_note` | ✅ | ✅ **卡片点击→浮层详情，四就绪信号齐全** | — |
| `user_profile` | ✅ | ✅ **状态路径/分区选择有效** | — |
| `like_feed` / `favorite_feed` | ✅ 按钮唯一 | ❌ | ❌ **会改真实账号，需授权** |
| `comment_feed` / `reply_comment_in_feed` | ✅ 输入框 | ❌ | ❌ **会真发评论，需授权** |
| notification 系列（含点赞/回复） | ✅ | ⏸ 未读未读数/列表 | ❌ **回复会真发，需授权** |
| `publish_content` / `publish_with_video` | ✅ 早期 | ❌ | ❌ **会真发布，需授权 + 本地图片** |
| `check_login_status` | ✅ 已登录态 | ⚠️ 登出态待真机 | — |
| session 系列（`start_page`/`go_back`/`get_page_state`/`close_page`） | 同详情页 | ⏸ 未做 | — |

**阻塞点只有一个**：所有**写操作**都会改到内置浏览器里那个**真实账号**（`loggedIn=true`，昵称「一画一话」），
我不会在未获授权时执行。要验证写链路，需要三选一：
① 明确授权我在这个账号上操作（并指定靶子笔记）；② 提供一个测试账号/干净 profile；
③ 留到树莓派上按本文件 §6.4 的真机验收清单做。

**通知页读取（`get_unread_count` / `list_notifications`）—— 读路径实测通过**

在 `/notification`（已登录）实测：

| 观测 | 实测值 | 含义 |
|:--|:--|:--|
| 状态键 | `redBadge` / `notification` / `messageData` | 通知数据在 `notification` ✅ |
| `notification` 子键 | `isFetching` / `isUnreadCountInitialized` / `activeTabKey` / **`notificationCount`** / `notificationMap` | 未读数读取路径存在 ✅ |
| tab 真实文案 | **`评论和@` / `赞和收藏` / `新增关注`** | 与代码 `notificationTabLabel` 返回值**逐字一致** ✅ |
| items / `.action-like` / `.action-reply` | 5 / 5 / 5 | 列表与互动按钮就位 ✅ |
| 首条内容 | `青史拾页 你的好友 评论了你的笔记 06-09 … 回复` | 「评论和@」tab 的条目形态符合代码解析假设 ✅ |

> 本轮第四次「差一点误报」：tab 文案看起来像漂移点，逐字对照后发现代码写的就是线上文案，**无漂移**。
> 四次澄清分别是：筛选面板（异步渲染）、登录判定（无法在已登录会话验证登出态）、`notes` 条数（嵌套结构）、通知 tab 文案（其实一致）。

**信息流翻页（`list_feeds`）—— 行为级实测通过，并印证「状态优先」的设计**

在 `/explore` 滚到文档底部（另起一次探测）：

| 指标 | 滚动前 | 滚动后 | 说明 |
|:--|--:|--:|:--|
| DOM `section.note-item` | 30 | **30** | 网格回收复用，**不能按 DOM 数判断条数** |
| `__INITIAL_STATE__.feed.feeds` | 34 | **64** | 每轮滚动 +30，状态数组才是条数真相 |
| 文档高度 | 3,863 | 5,855 | +52%，仍在增长（未到底） |

**结论**：滚动确实触发信息流继续加载（[实测]），且**状态数组累加而 DOM 恒定** ——
这正是代码用 `readHomeFeedsFromState`（读 `feed.feeds`，`.value ?? ._value`）而不是数 DOM 卡片的理由。
`feedPageOps` / `LoadFeedBatch` 的「滚动 + 读状态增量」形状与线上一致。

> 与用户主页那条一致：**DOM 卡片数不可作为条数依据**（主页 17↔9，信息流恒定 30 而状态 34→64）。

### D.11 写操作行为级验证（真实账号，用户已授权，靶子=本人最新笔记）

靶子笔记：《同一句预言，为什么谁赢了谁就能解释？》（谶纬 / 白虎观会议 / 《白虎通义》 / 三纲六纪）。
评论与回复内容**按笔记内容撰写**（谶纬解释权、今古文之争、曹魏代汉借图谶）。

| 工具 | 实测操作 | 数据层验证 | 结论 |
|:--|:--|:--|:--|
| `like_feed`（点赞） | 点 `.interact-container .left .like-wrapper` | `liked true→false`、`likedCount 1→0`；再点回 `false→true`、`0→1` | ✅ 双向验证通过（已恢复原状） |
| `favorite_feed`（收藏） | 点 `.collect-wrapper` | `collected false→true`、count `0→1`；再点回 `false`/`0` | ✅ 双向验证通过（已恢复原状） |
| `comment_feed`（评论） | 输入 122 字内容→点 `.btn.submit`（文本「发送」） | `commentCount 0→1`、DOM `.comment-item` 0→1、正文与作者标记「一画一话 作者」均出现、输入框清空 | ✅ 端到端通过 |
| `reply_comment_in_feed`（回复评论） | 点 `.comment-item .right .interactions .reply` → 输入 79 字 → `.btn.submit` | `commentCount 1→2`、`.comment-item-sub` 0→1 | ✅ 端到端通过 |
| `get_unread_count` | 读 `notification.notificationCount` | `{"unreadCount":0,"mentions":0,"likes":0,"connections":0}` | ✅ 路径有效 |
| `list_notifications` | 读 tab=3 / 条目 / 文案 | tab key `0`、5 条、每条含 `.action-like`/`.action-reply` | ✅ |
| `like_notification` | 点第 1 条 `.action-like` | `svg use` 的 href → **`#liked`**（其余 4 条未变） | ✅ |
| `reply_notification` | 点 `.action-reply` → 输入 64 字 → 点真实「发送」 | 输入框 `textarea.comment-input`（placeholder「回复 青史拾页」）出现→消失（= 代码 `waitNotificationReplyAccepted` 判据） | ⚠️ **站点链路正常，但代码会失败 —— 见下方 BUG #2** |

#### BUG #2（已确认，本次扫描第一个功能性 bug）：`reply_notification` 提交按钮选择器失效

- **代码**：`SelectorNotificationReplySubmit = ".input-buttons .submit"`，`findNotificationSubmitButton` 在**通知行内**查找且**禁止页面级退路**（fail-closed）。
- **线上实测**：该选择器**全页命中 0**（`document.querySelectorAll('.input-buttons .submit').length === 0`）。
  真实按钮是 `BUTTON.submit`（文本「发送」），父链
  `BUTTON.submit < DIV.comment-wrapper.action-comment < DIV.actions < DIV.info < DIV.main < DIV.container`；
  同一 `.comment-wrapper` 内 `.submit` 恰好 1 个；输入框 `textarea.comment-input` 也在同一行内（`insideWhichRow=1`）。
- **后果**：`reply_notification` 会卡在提交步并返回「目标通知行内找不到发送按钮」，回复永远不会发出。
- **修复**：选择器改为 `.comment-wrapper .submit, .input-buttons .submit`（线上式 + 旧版兜底；行内查找与「发送」文本/禁用校验保持不变）。

#### 顺带得到的两条「不可用判据」（两个方向都实测了）

| 判据 | 反例（实测） |
|:--|:--|
| DOM class 判定点赞 | 取消赞后 `.like-wrapper` **仍是 `like-active`**；已收藏时 `.collect-wrapper` **仍是裸 `collect-wrapper`**（无 active 类）→ class 与状态无关，必须读数据层 `interactInfo` |
| 通知点赞状态 | 通知行的状态**确实**由 `.action-like svg use` 的 href 表达（点后 → `#liked`）→ 这里读 svg href 是对的（与 `parseNotificationLikeHref` 一致） |

### D.12 发布链路（`publish_content`）—— 除最后一击外全部实测

在 `creator.xiaohongshu.com/publish/publish?source=official`（已登录）逐段实测：

| 步骤 | 代码依赖（[源码]） | 实测（[实测]） | 结论 |
|:--|:--|:--|:--|
| 进发布页 | `urlOfPublic` | 页面可达、页头为当前账号 | ✅ |
| 切「上传图文」tab | `div.creator-tab` + `getTabElement`（可见性 + 精确文本 + `elementFromPoint` 遮挡判定） | 线上 **9 个** `.creator-tab`，其中「上传图文」有 3 个：一个 `left:-9999px`、一个 `opacity:1e-05`、一个真实（无 inline style）；前两个被代码**显式检查**跳过（源码里确实写了这两个字符串）；真实那个 `elementFromPoint` 命中 `SPAN.title`，但它是 tab 的**后代** → `this.contains(target)` 成立 → `blocked=false` → 正常点击 | ✅ 抗重复节点设计正确 |
| 新增 tab | — | 线上多了「发播客」tab（代码不认识）；因按**精确文本**匹配，无影响 | ℹ️ |
| 上传图片 | 首张 `.upload-input`、后续 `input[type="file"]`；等 `.img-preview-area .pr` 计数 | `input.upload-input`（accept `.jpg,.jpeg,.png,.webp`）注入文件后 **`.img-preview-area .pr`=1**、预览为 blob URL | ✅ |
| 标题 | `div.d-input input` | 命中，placeholder「填写标题会有更多赞哦」，写入 12 字成功 | ✅ |
| **正文编辑器** | 第 1 路 `div.ql-editor`；兜底 `findTextboxByPlaceholder`（找 `p[data-placeholder*="输入正文描述"]` → 向上找 `role="textbox"`） | **`div.ql-editor` 已不存在**（Quill 换成 **TipTap ProseMirror**：`DIV.tiptap.ProseMirror[contenteditable=true][role=textbox]`，空态占位 `data-placeholder="输入正文描述，真诚有价值的分享予人温暖"`）→ 第 1 路失效，但兜底**一层就命中** `role="textbox"` 的编辑器 | ⚠️→✅ 兜底有效；`div.ql-editor` 属可清理的死路径 |
| 可见范围 | `div.permission-card-wrapper div.d-select-content` → `div.d-options-wrapper div.d-grid-item div.custom-option` | 全部命中；选项为 公开可见 / **仅自己可见** / 仅互关好友可见 / 只给谁看 / 不给谁看；成功切到**仅自己可见** | ✅ |
| 发布按钮 | `xhs-publish-btn` + `findPublishButton`（读 `is-publish`/`submit-disabled`）→ `clickPublishWidget`：`ShadowRoot()` 穿透 **closed shadow root** 后 `ElementR("button","发布")` | `xhs-publish-btn` 存在，`submit-disabled="false"`、`is-publish="true"`；但宿主**无 light DOM 子节点、`shadowRoot` 为 null（closed）** → 页面 JS 与无障碍树都看不到内部按钮，坐标点击宿主不触发 | ⛔ **无法用页面级工具完成/验证最后一击** |

**结论**：发布链路从「进页 → 切 tab → 传图 → 标题 → 正文（含新编辑器的兜底）→ 可见范围」全部实测通过；
代码用 CDP 穿透 closed shadow root 点发布按钮的写法**只能由 MCP 自己执行**（页面 JS 够不到），
因此这一击需要由 MCP 实跑（或在真机验收时执行）来闭环。

#### 发布最后一击：已完成（2026-09-21）——closed shadow root 的正确打开方式

D.12 里「页面级工具够不到发布按钮」的说法**已解决**，而且不用坐标：

- closed shadow root 只是对 `element.shadowRoot` 隐藏；该自定义元素把内部引用挂成了**普通属性**：
  `xhs-publish-btn._sr`（shadow root）、`._props`、`._onPublish()`（0 参函数）、`._onSave()`。
- `_sr` 里的真实结构（实测）：

| 按钮 | class | rect | disabled |
|:--|:--|:--|:--|
| 暂存离开 | `.ce-btn.white` | [549,771,120,40] | false |
| **发布** | **`.ce-btn.bg-red`** | [693,771,120,40] | false |

- 触发 `_sr.querySelectorAll('button')` 中文本为「发布」的那个 `.click()` 后：
  **URL → `/publish/success?source=official…`**、页面提示「发布成功 · 2 秒后将返回发布页」。
- 随后在 **笔记管理**（`/new/note-manager`）确认：标题《亡秦者胡也，算不算预言？》在列，
  状态 **仅自己可见**、**审核中**，笔记数 17 → **18**。

**结论**：`publish_content` 的「进页 → 切 tab → 传图 → 标题 → 正文（TipTap 兜底）→ 可见范围 → 发布」整条链路
在真实账号上端到端验证通过；代码用 CDP `ShadowRoot()` + `ElementR("button","发布")` 定位的写法与我实测到的
`<button class="ce-btn bg-red">发布</button>` 结构完全吻合。

> 方法论补充（替代坐标）：遇到 closed shadow root，先看宿主元素上有没有把内部引用挂成属性
> （如 `_sr`/`_onPublish`），有的话可以直接取到真实按钮并 `click()`；这比坐标猜测可靠得多，
> 也解释了此前坐标点击全部落空的原因 —— 两个按钮实际在 [549,771] 与 [693,771]，
> 而盲点的 x=681/900/1000 全在按钮之间的空隙或右侧空白。

#### D.13 附：测试笔记已清理

发布验证完成后，在笔记管理（`/new/note-manager`）删除该测试笔记：卡片上的删除入口是
`.note-card__action-btn--del`（卡片三个图标按钮中的第三个），点击后 `.d-modal` 确认框
（「删除笔记 / 删除后将无法恢复，确定要删除《…》这篇笔记吗」）里点「确定」。
结果：列表不再出现该标题、「删除成功」提示、笔记总数 **18 → 17**（回到原状）。
全程元素级 click（语义定位），未使用坐标。

> 说明：本轮为验证写操作而在真实账号上留下的**其他痕迹**（用户已授权）：
> ① 在《同一句预言，为什么谁赢了谁就能解释？》下的 122 字评论；
> ② 该评论下的 79 字回复；
> ③ 对「青史拾页」评论的通知点赞 + 64 字回复。
> 这些是评论/通知而非独立笔记，如需清理要在对应笔记页删除，MCP 本身没有删除评论的工具。

### D.14 只读补测六项（2026-09-21）—— 全部通过，并新增两条代码级隐患

| # | 项 | 实测 | 结论 |
|:--|:--|:--|:--|
| 1 | `search_feeds` **筛选是否真生效** | 打开 `.filter-panel` 后点「最新」：`综合` 失去 active、`最新` 拿到 active；结果**整批更换**（首 5 个 feed id 由 `6a8557dc…/6a51c488…` 变为 `6ab05bfc…/6ab027fe…`） | ✅ 生效。且「最新」有 2 个重复 `div.tags`，**只有 1 个通过 `elementFromPoint` 命中校验** —— 再次印证 `findFilterOption` 必须逐个复核 |
| 2 | `search_feeds` **翻页** | 滚动 `.search-layout-wrapper` 到底：`search.feeds` **22 → 44**，该容器 scrollHeight **2140 → 3312**（DOM 卡片仍约 30，回收复用） | ✅ 懒加载成立 |
| 3 | `list_notifications` **tab 切换 + 续页** | 切「赞和收藏」：`notification.activeTabKey` **0 → 1**、条目 **5 → 20**、内容换成「赞了你的笔记/收藏了你的笔记」；再滚窗口到底：条目 **20 → 40**、`body.scrollHeight` **2158 → 4140** | ✅ 两项都成立 |
| 4 | `get_note_detail` **「展开 N 条回复」** | 元素级 click 首个 `.show-more`（文本「展开 36 条回复」）：`.comment-item` **19 → 24**、`.comment-item-sub` **9 → 14**，按钮文案变为**「展开更多回复」**（一次加载 5 条） | ✅ 成立 |
| 5 | `get_note_detail` **读到底（多轮滚动）** | 连滚 3 轮：`.comment-item` 24 → 39 → 49 → **59**，`.note-scroller` scrollHeight 3694 → 5892 → 7268 → **8604**；**无 `.end-container` 到底标记** | ✅ 增长成立、速率约 +10~15 条/轮；「共 219 条」需 **15+ 轮**，印证代码 500 轮 / 9 分钟预算的形状。未跑到真到底（省资源） |
| 6 | `user_profile` **收藏/点赞 tab 提取** | 点「收藏」后 `user.activeTab.query` 由 **`note` → `fav`**、label「收藏」、卡片 19 张；代码 `parseUserProfileState` 在 `snap.Query != "" && snap.Query != "note"` 时 fail-closed | ✅ 线上确实会出现非 `note` 的 query 值，**代码的 fail-closed 防线前提真实存在**，不会把收藏/点赞混进主页笔记 |

#### 新增代码级隐患（交给 MCP 实跑确认，不在本轮改代码）

1. **搜索页有第二个滚动容器**：搜索页 `body` 不滚动，结果在 `.search-layout-wrapper` 内，同一页面还有 AI 侧栏的 `.ai-chat-scroll-body`（sh 2768/ch 636）。
   代码的分页用的是滚轮滚动（`page.Actor().Mouse.Scroll(0, 700)`）—— 若指针不在结果容器上，滚轮可能落到别的容器，表现为「翻页不生效」。
2. **通知页是窗口级滚动**（`HTML` sh 2158/ch 836），与搜索页不同；代码对通知续页也是滚轮滚动，机制上吻合，但两类页面滚动目标不一致这一点值得在实跑时盯一下。

#### 过程记录：重页面导航会卡死（又一例证）

补测期间出现：同一会话里 `https://example.com` **秒开**，而
`/search_result?keyword=…` 的**新鲜导航连续两次 20s 超时**、`CDP Runtime.evaluate` 也两次 20s 超时；
`/explore` 与 `/notification` 则正常。最后改走 **UI 路径**（点搜索框 → 输入 → 回车，即代码的 P0 路径）才进到搜索页。
这与附录 C 的结论一致：**重页面的单次导航/求值可能远超 20s**，Pi 上只会更糟 —— 代码里已有的 eval 预算与
renderer 死亡熔断是必需项，不能放宽。

### D.15 session 四件套（`start_page` / `get_page_state` / `close_page` / `go_back`）—— 等价站点行为全部验证

各自的关键判据先读代码确认（[源码]），再在真实页面按同一判据实测（[实测]）：

| 工具 | 代码判据 | 实测 | 结论 |
|:--|:--|:--|:--|
| `start_page` | 建会话时一次 eval 记录 `{url, scroll_y}`，`scroll_y = window.scrollY \|\| document.scrollingElement.scrollTop` | `/explore` 上读取正常（`url` + `scrollY`）；`window.scrollTo(0,1200)` 后 `scrollY` 0 → **1200**，`scrollingElement.scrollTop` 同步 | ✅ 状态读取有效 |
| `get_page_state` | `probeXHSReadyFull`：URL、可见详情数（`VisibleDetailCount`）、按 URL 推断页面种类 | 信息流：`detailVisible = 0`；点卡片进详情后：**4**（mask=1）；URL 形态 `…/explore` 与 `…/explore/<id>?xsec_token=…` 可区分 | ✅ 字段有意义；⚠️ 见下方 `scroll_y` 缺陷 |
| `go_back` | `history.back()` 后轮询 `historyTargetReady`：`fromDetail`（后退前 `VisibleDetailCount>0`）时要求 **详情数归零 + 目标页就绪**；预算 `browseSessionBackTimeout` | 点卡片后：URL `…/explore/6aa7abe6…?xsec_token=…`、`detailVisible=4`、mask=1；`history.back()` 后 **约 1.8s**：URL 回 `/explore`、`detailVisible=0`、mask=0 | ✅ 判据在线上成立，15s 预算充裕 |
| `close_page` | 关闭页面并释放浏览器（`close()` + `Release(page)`，令牌归还） | 关闭该标签页后，浏览器**立即可开新页**并正常渲染 | ✅ 无残留/锁定 |

#### 新发现的代码级缺陷：`get_page_state` 的 `scroll_y` 在「内部滚动容器」页面上恒为 0

`refreshPageState` 取的是 `window.scrollY || document.scrollingElement.scrollTop`，但**这两个页面都不靠窗口滚动**：

| 页面 | 窗口指标 | 真实滚动发生在 |
|:--|:--|:--|
| 搜索结果页 `/search_result_ai` | `document.body.scrollHeight == innerHeight == 836`，`window.scrollTo(0, bottom)` 后 `scrollY` 仍 **0** | `.search-layout-wrapper`（实测 scrollTop 0 → 1420，scrollHeight 2140 → 3312） |
| 笔记详情页 | 窗口同样不可滚 | `.note-scroller`（实测 scrollTop 0 → **2335**，scrollHeight 2969） |

**后果**：`get_page_state` 报的 `scroll_y` 在这两类页面上**恒为 0**，与真实阅读/滚动位置不符。

**✅ 已修（2026-09-21，`e9ec2be`）**：三处内联 `window.scrollY || document.scrollingElement.scrollTop`
收敛为共享片段 `xhsScrollYJS` 的 `scrollY()` —— 窗口能滚就用窗口值；窗口不可滚时回退到
本附录实测命中的两个滚动宿主（详情页 `.note-scroller`、搜索页 `.search-layout-wrapper`），
取可滚动量更大的那个。只列这两个实测容器、不做全树扫描（该函数在每次就绪探测里都会执行，
全树 `getComputedStyle` 在 Pi 上代价过高）。新增 `TestScrollYIsSingleSource` 防止再内联。
注：上游 main 至今未修此缺陷。

### D.16 「下一步工具」指引重构（2026-09-21，单元测试 + 本机服务实测）

**背景**：调用方经常调错下一个工具。改前有三套并存的指引，互相矛盾：

| 位置 | 改前 | 问题 |
|:--|:--|:--|
| 错误响应 | `next_step.tool`（由错误文本子串匹配得到） | 不给参数；工具名靠猜 |
| 成功响应 | 手写的静态 `available_tools`（每个处理函数一份，共 8 张表） | 与真实页面状态无关，会推荐当前状态不允许的工具 |
| `get_page_state` | `recommended_action`（动词枚举） + `actions[]` 菜单 + `current.next_hint` 长散文菜单 | 三份指引互相竞争；`start_page` 只给 `continue｜wait｜retry｜recreate`，既不指名工具也不给参数 |

**改法**（jev 裁决：单一 typed `next_step` 1.0、状态单一来源 1.0、带已知参数 0.98、删散文菜单 0.99、状态优先且失败要响亮 1.0）：

- 新增统一结构 `NextStep{tool, args, reason, hint}`，**错误响应与成功响应共用**；
  成功响应统一为 `{data, next_step, available_tools}`。
- 会话内唯一解析器 `guidanceLocked`（合并原先 4 个解析函数），
  删除 `current.next_hint` / `current.available_tools` / `current.results_count` 重复键，
  `available_actions` 统一成 `available_tools`。
- 指引由**会话已跟踪的状态**推导，纯内存、不额外探测页面（Pi 上不增加开销）；
  刚调用过的工具不再被推荐（读完评论改推 `go_back`）。
- 页面未就绪只暴露 `get_page_state`/`close_page`；session 与页面不一致时禁用全部详情工具。
- `start_page` 的动词枚举删除，失败时先读登录态再给精确指引。

**状态矩阵（单元测试，9 例全通过）**：

| 会话状态 | `next_step.tool` | `args` | 允许的工具（节选） |
|:--|:--|:--|:--|
| 首页、无结果 | `search_feeds` | `session_id` | + `list_feeds` / `get_unread_count` / `list_notifications` |
| 有未读搜索结果 | `open_note` | `session_id` + `result_ref` | + `search_feeds` |
| 全部结果已读 | `open_note` | `result_ref`（回退到 0） | + `search_feeds` |
| 已打开并读取 | `get_note_detail` | `session_id` | + `like_feed`/`favorite_feed`/`comment_feed`/`reply_comment_in_feed`/`go_back` |
| 刚读完评论（排除 get_note_detail） | `go_back` | `session_id` | 同上 |
| 打开但未读完 | `go_back` | `session_id` | 仅 `go_back` |
| 通知页有可写条目 | `reply_notification` | `session_id` + `notification_ref` | + `like_notification` |
| 通知页无可写条目 | `list_notifications` | `session_id` + `tab=mentions` | 不含 `like/reply_notification` |
| 页面未就绪 | `get_page_state` | `session_id` | 仅 `get_page_state`/`close_page` |
| session 与页面不一致 | `open_note`（无结果时 `search_feeds`） | `session_id`(+`result_ref`) | 不含任何详情工具 |

**实机验证（本机服务 + MCP Streamable HTTP `/mcp`）**：

| 调用 | 响应 `next_step` | 结论 |
|:--|:--|:--|
| `start_page`（本地 profile 未登录） | `{tool: get_login_qrcode, reason: 探索页已加载但处于未登录状态}` | ✅ 改前是**裸文本、无任何指引**；且风险词表会把登录页的「获取验证码」误判为验证码风险，故改为先读登录态 |
| `close_page` / `get_page_state` / `like_feed`（不存在的 session_id） | `{tool: start_page}` | ✅ 三处都不再是裸文本 |
| 缺 `session_id` 的 `list_feeds` | SDK 返回 `invalid params: required: missing properties: ["session_id"]` | ✅ 必填参数由 schema 兜住，`next_step` 负责 schema 管不了的**状态类**错误 |

**防漂移测试**：`TestNextStepArgsMatchToolSchemas` 用反射取 MCP 参数结构体的 json tag 作为合法键空间，
再扫描源码里所有 `NextStepArgs(map[string]any{...})` 的键；该测试当场发现并修掉了自造的
`get_note_detail` 非法参数 `load_comments`（真实 schema 里只有 `session_id`/`max_items`/`cursor`/
`click_more_replies`/`reply_limit`/`scroll_speed`）。

**未完成项（需要所有者动作）**：登录态下的成功路径指引（`search_feeds` → `open_note(result_ref)` →
`get_note_detail` → `close_page`）尚未在本机服务上跑通 —— 本机 Chrome profile 未登录，
需要用手机扫码（`get_login_qrcode`）或在服务侧放入 `cookies.json` 后才能补齐这一条 [未见]。

### D.17 本轮优化的一手证据（2026-09-21，本机 x86 + Chrome 153）

**① 热页面复用与 TTL（CDP page 目标数实测）**

| 时点 | page 目标数 | 说明 |
|:--|--:|:--|
| `check_login_status` 之后 | **1**（`https://www.xiaohongshu.com/explore`） | 页面被放入热页面缓存，不再立即关闭 |
| 等 35s（> TTL 30s）后 | **0** | 到期自动关闭，内存归还 |

改前行为：每次 `Release` 立即关页 → 调用间隙 page 目标数恒为 0。

**② 首屏资源结构（真实 `/explore` 与详情页，新 profile，18–22s 观察窗）**

| 场景 | 总下载 | 请求数 | 其中 JS | 其中图片 |
|:--|--:|--:|--:|--:|
| 发现页 · 不拦截 | 5.35 MB | 209 | 2.97 MB (44) | 2.00 MB (92) |
| 发现页 · 项目拦截 | 3.34 MB | 119 | 2.97 MB | ≈0（仅剩 2–3 张 UI 小图） |
| 详情页 · 不拦截 | 5.50 MB | 207 | 2.97 MB | 2.15 MB (92) |
| 详情页 · 项目拦截 | 3.34 MB | 122 | 2.97 MB | ≈0 |

**③ 持久 profile 的 HTTP 缓存效应（同一 profile 连续两次访问）**

| 次 | 总下载 | JS 下载 |
|:--|--:|--:|
| 第 1 次（冷缓存） | 3.38 MB | 3.01 MB |
| 第 2 次（热缓存） | **0.27 MB** | 0.11 MB |

**④ 端到端计时（本机 `start_page`，冷/热 profile）**：7.9s / 9.1s / 9.9s —— **本机看不出差别**，
说明快机上网速与 CPU 都不是瓶颈、耗时被探测与固定等待主导；JS 解析成本在 Pi 的 A53 上是数量级放大 `[推断]`，
必须真机标定才能定论。

**⑤ 登录路径**：`get_login_qrcode` 首调用 6.3s 并正常返回二维码；第二次调用返回
`browser busy: owner=login_qrcode_wait`（既有设计：首个调用持有页面等待扫码，最长 4 分钟），
错误响应带 `next_step=tool: check_login_status`，指引正确。

**⑥ 浏览器指纹对照（裸启动实测，供选型参考）**

| 特征 | 全量 Chrome 153 `--headless` | chrome-headless-shell 153 |
|:--|:--|:--|
| `navigator.webdriver` | false | **true** |
| `navigator.plugins.length` | 5 | **0** |
| `window.chrome` | object | **undefined** |
| UA | 含 `HeadlessChrome` | 含 `HeadlessChrome` |
| 真实 `/explore` PSS | 632.7 MB / 9 进程 | **45.8 MB / 1 进程** |

### D.18 真机标定脚本（`scripts/pi-calibrate.sh`，2026-09-21）

代码侧优化已经做完一批，但在 Pi 上的收益全部是 `[推断]`。这个脚本把「有没有生效」变成可读的数字，
**只依赖 bash + curl + grep + /proc**（板上不需要装 node/python）：

```bash
PORT=18160 BROWSER_BIN=/usr/bin/chromium \
  ./scripts/pi-calibrate.sh /usr/local/bin/xiaohongshu-mcp
```

它自拉起服务、跑完自关，输出七段：

| 段 | 量什么 | 判据 |
|:--|:--|:--|
| 1 | 服务起到 MCP 可用 | — |
| 2 | 生效配置自述：低资源档 / profile 是否持久 / 拦截模式条数 | arm64 上应有低资源档与拦截；若出现「缓存目录不可写」则冷启动缓存不生效 |
| 3 | `start_page` 冷启动耗时 + `feed_card_count` + 风险/未登录提示 | 与桌面口径对比，看放大倍数 |
| 4 | 浏览器进程树 RSS / **PSS**（比例分摊，推荐口径） | Pi 3B 只有 1GB，PSS 是硬约束 |
| 5 | `get_page_state.browser`：`pages_created` / `warm_page_reused` / `navigation_skipped` / `blocked_url_patterns` / `profile_persistent` / `idle_timeout_seconds` | 用来确认热页面复用与跳导航**真的命中**（不是只写在代码里） |
| 6 | 连续两次 `start_page` 后 `navigation_skipped` 是否增加 | 增加=省掉了一次整页加载 |
| 7 | 浏览器空闲关闭汇总（`pages_created` / `warm_page_reused`） | — |

**本机（x86 桌面）自测结果**：脚本可跑通，未登录路径会明确提示先扫码登录；
在桌面口径下同一页面 PSS 约 **825 MB / 10 进程**（这个数只是校准脚本本身是否工作，不代表 Pi）。

> 登录态下才能跑完第 5/6 段：先 `get_login_qrcode` → 手机扫码 → `check_login_status`，再重跑。


### D.19 发布链路与分享链接的端到端验收（2026-09-21，真实账号 + CloakBrowser/stock Chrome）

**发布（`publish_content`，可见性=仅自己可见）**

| 步骤 | 修前 | 修后（实测） |
|:--|:--|:--|
| stock Chrome 153 | `点击发布按钮失败: 元素不可点击: obscured`（closed shadow root 命中重定向） | **发布成功**：`/publish/success`，`publish_success` 505ms / 2 探测 |
| CloakBrowser 146（Pi 用的就是它） | `输入标题失败: scroll made no progress` → 修滚动点后 `element did not become visible` | **发布成功**：`isError=false`、`Status:发布完成`，全链路 42s |

修的三处（均在 `humanize/`）：closed shadow root 的命中判据单一来源；滚轮改打在目标自身（夹进容器可见区）；
被遮挡时按遮挡物性质分流——对话框/引导浮层→等它让开、等不到就点它自己的关闭控件；页面自身固定条（底部 sticky 发布条）→保留按覆盖物位置滚动。

**分享链接打开笔记（验收口径 = 提取链接并打开，短链那次 302 与长链同过程）**

| 输入 | 实测 |
|:--|:--|
| 搜索结果打开《西安团建｜9个宝藏露营地合集✨》 | 7.3s，`isError=false` |
| 该页真实长链（164 字符含 `xsec_token`）作 `share_url` | **4.9s，打开同一篇，标题一致** |
| **整句分享文案**（含中文 + emoji）作 `share_url` | **3.1s，提取链接后打开同一篇，标题一致** |
| `http://xhslink.com/...` / `xhslink.com/...` / `https://...` | 三种写法均通过校验并进入导航 |
| `https://evil.com/...` / 文案里没有链接 | 0.9s / 0.1s 内被拒 |

两类修复：`normalizeShareURLScheme`（只补/升级 scheme，host 白名单等严格校验保留）+
`extractShareURL`（从整句分享文案里取链接）。

**未验**：真实 `xhslink` 短链那一次 302 之后的落地页。一手证据表明**网页端不产生短链**：
PC 分享面板只有「复制图片 / 复制笔记链接」；移动端 UA 下提示「打开App查看更多 / 点击右上角分享给好友」，
整页 HTML 里 0 处 `xhslink`。短链是 App 专有形态（所有者确认"提取链接打开就行，过程一样"）。

**测试笔记已清理（2026-09-21，所有者指出风险后立即执行）**：两篇《等待机制本地验证》
（12:24 与 14:42 各一篇，均"仅自己可见"）已在创作页笔记管理里删除：
删除确认框原文「删除笔记 删除后将无法恢复，确定要删除《等待机制本地验证》这篇笔记吗」→ 确定；
重新加载笔记管理页独立复核 `testNoteHits=0`，本人真实笔记（同一句预言…、穷到买不起书…、读书三年不看菜园子…）未受影响。

**草稿也被清空（同日补充）**：复核时发现 **草稿箱里有 14 条草稿**，全部是本次调试留下的——
1 条《等待机制本地验证》（14:39）+ 13 条「暂无笔记标题」（14:28–14:38，反复打开发布页/发布失败时编辑器自动存的）。
逐条按 `data-draft-id` 定位，点「删除」→ 确认框「草稿删除后不可找回 取消 删除」→ 删除；
再次全新加载复核：`草稿箱(0)`、`图文笔记(0)`、草稿节点 0、测试痕迹 0。

> 教训（这次踩得很重）：
> 1. 真实账号上的写操作（发布/评论/点赞）**做完就清**，不要留"先不删"的敞口；
> 2. **不要反复打开创作页/编辑器做诊断**——小红书会**自动存草稿**，一次调试就可能留下十几条痕迹；
> 3. 内容验证一律用合成页面/本地 HTML，**绝不往真实账号发"测试笔记"这种无意义内容**（那本身就是机器人特征）；
> 4. 需要留证的用日志/截图，不留在账号上。

**风险误报（D.19 附带）**：`permission_denied` 组曾含 `仅自己可见`——我们自己的测试可见性，
导致每次发布私有笔记必然被判一次风控。已删除该词，并把页面规则与 Go 侧分类并成唯一来源
（详见 `pi3b-source-analysis.md` §15）。

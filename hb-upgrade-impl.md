## 一、修改白名单

- `third_party/headless_browser/headless_browser.go`
- `third_party/headless_browser/headless_browser_test.go`
- `browser/browser.go`
- 新增 `browser/browser_test.go`
- `configs/browser.go`
- 新增 `configs/seed.go`
- 新增 `configs/seed_test.go`
- `main.go`
- `cmd/login/main.go`
- `service.go`
- `go.mod`
- `go.sum`
- `README.md`
- `README_EN.md`

禁止修改：

- `pkg/humanize/rod/hrod.go`
- MCP 注册、22 工具、session、互动、通知实现
- `cookies/cookies.go`：已有 `LoadSeed/SaveSeed`，直接复用
- 浏览器 Manager 生命周期逻辑

## 二、wrapper 精确改动

### 1. 合并 imports 和结构

在 [headless_browser.go](/home/dietpi/work/xiaohongshu-mcp/third_party/headless_browser/headless_browser.go:4)：

新增官方 v0.4.0 所需：

- `math/rand`
- `runtime`
- `strconv`

保留现有扩展所需：

- `context`
- `fmt`
- `os`
- `sync`

`Browser` 合并为：

- 官方字段：`stealthJS`、`uaOverride`
- 现有字段：`browserCancel`、`closeOnce`、`closeErr`
- 公共字段：`browser`、`launcher`

不要同时保留 `stealth` 和 `stealthJS` 两套状态，统一为 `stealthJS`。

### 2. 合并 `Config`

在 [Config](/home/dietpi/work/xiaohongshu-mcp/third_party/headless_browser/headless_browser.go:30) 保留现有字段，并加入官方字段：

- `Fingerprint bool`
- `FingerprintPlatform string`
- `FingerprintSeed int`
- `StealthJS bool`
- `Language string`
- `ExtraFlags map[string]string`

现有扩展继续保留：

- `UserDataDir`
- `ExtraArgs`
- `CloakProfile`

`newDefaultConfig`：

- `Headless=true`
- `StealthJS=true`
- fingerprint 默认关闭
- 其他保持零值

### 3. Option API

加入官方 API：

- `WithFingerprint(platform string)`
- `WithFingerprintSeed(seed int)`
- `WithStealthJS(enabled bool)`
- `WithLanguage(language string)`
- `WithExtraFlags(flags map[string]string)`

保留全部现有 API：

- `WithUserDataDir`
- `WithStealth`
- `WithCloakLauncherProfile`
- `CloakLauncherProfile`
- `WithExtraArgs`

兼容关系：

```go
func WithStealth(enabled bool) Option {
    return WithStealthJS(enabled)
}
```

`WithExtraFlags` 和 `WithExtraArgs` 必须防御性复制 map/slice，避免调用方后续修改配置。

### 4. `New` 签名绝不能回退

保留现有签名：

```go
func New(ctx context.Context, options ...Option) (*Browser, error)
```

禁止采用官方：

```go
func New(options ...Option) *Browser
```

继续保留：

- nil context 转 `context.Background()`
- 可取消的 `launcher.Context(ctx).Launch()`
- 显式 `Connect()` 错误返回
- `browserCancel`
- launch/connect 失败后的 Kill + 异步 Cleanup
- `NoDefaultDevice()` Cloak 路径
- cookies 非 panic 写入

### 5. launcher 选项合并

在 [New](/home/dietpi/work/xiaohongshu-mcp/third_party/headless_browser/headless_browser.go:74) 按以下顺序配置：

1. `Headless`、`--no-sandbox`
2. `CloakProfile`
3. 显式 User-Agent
4. fingerprint seed/platform
5. `ExtraFlags`
6. Chrome binary
7. UserDataDir 和缓存限制
8. Proxy
9. 现有 `ExtraArgs`

fingerprint 开启时：

- platform 空值走 `autoFingerprintPlatform`
- seed 为 0 才随机生成
- 设置 `--fingerprint`
- 设置 `--fingerprint-platform`

增加配置冲突校验：

- `Fingerprint=true` 且 `UserAgent!=""`：`New` 直接返回明确错误。
- 防止其他直接调用 wrapper 的代码制造 UA/Client-Hints 不一致。
- 主程序层负责确保不会同时传入。

`ExtraArgs` 仍最后应用，保持现有显式参数优先级；日志应打印最终启动参数。验收时禁止通过 `CLOAK_FLAGS` 重复传 `user-agent/fingerprint/fingerprint-platform`。

### 6. UA override

合入官方函数：

- `autoFingerprintPlatform`
- `buildUAOverride`
- `primaryLang`

连接浏览器后：

- 仅 `Fingerprint=true` 时调用 `buildUAOverride`
- 保存到 `Browser.uaOverride`
- 构建失败必须终止启动，不能仅记录 warning 后无声降级
- 失败清理继续使用有期限的关闭路径

`buildUAOverride`保持官方分工：

- UA 和 Chrome version 从 `Browser.getVersion` 实读
- brands/fullVersionList 使用真实 Chrome 版本
- platform 字段留给 CloakBrowser
- `Language` 写入 `AcceptLanguage`

### 7. 页面创建路径

当前主路径是 [hrod Browser.Page](/home/dietpi/work/xiaohongshu-mcp/pkg/humanize/rod/hrod.go:66) → wrapper `Page()`，因此 UA override 必须放在 wrapper `Page()`，不能只放官方 `NewPage()`。

`Page()`流程：

1. `stealthJS=true`：`stealth.Page`
2. 否则：原生 `browser.Page`
3. 若 `uaOverride != nil`，对新 page 调用 override
4. override 失败返回 error，不能返回半配置页面

`NewPage()`继续只调用 `Page()`，避免重复应用 override。

### 8. Close 补丁完整保留

原样保留：

- `Close()`
- `CloseContext(ctx)`
- `Health(ctx)`
- `close(ctx)`
- `closeOnce/closeErr`
- renderer/CDP 异常时 Kill
- Cleanup 与 context 竞速

绝不采用官方同步 `MustClose()+Cleanup()`。

## 三、主程序 fingerprint 策略

### 最终决策

- 普通 Chrome 模式：不启用 fingerprint，行为不变。
- Cloak 模式且未配置自定义 UA：默认启用 fingerprint。
- Cloak 模式且配置了 `XHS_BROWSER_USER_AGENT`：显式 UA 优先，跳过 fingerprint 和 UA override，并输出 warning。
- Cloak 模式继续关闭 go-rod stealth JS。
- 默认语言 `zh-CN`；`XHS_BROWSER_LANG` 可覆盖。

### 稳定 seed

不能使用官方默认的“每次启动随机 seed”，否则现有持久 cookies、profile 和 identity drift 检查会看到跨进程画像变化。

在 `configs` 增加与上游 main 同源的 seed 解析：

```text
XHS_FP_SEED
    ↓ 未配置
cookies session 文件中的 seed
    ↓ 不存在
crypto/rand 生成正整数并 SaveSeed
```

现有 [cookies.Cookier](/home/dietpi/work/xiaohongshu-mcp/cookies/cookies.go:24) 已支持 `LoadSeed/SaveSeed`，无需改 cookies 实现。

### 文件改动

[configs/browser.go](/home/dietpi/work/xiaohongshu-mcp/configs/browser.go:9)：

- 增加 `fingerprintSeed`
- 增加 `SetFingerprintSeed`
- 增加 `FingerprintSeed`
- 增加 `BrowserLanguage()`：读取 `XHS_BROWSER_LANG`，空时返回 `zh-CN`

新增 `configs/seed.go`：

- `ResolveFingerprintSeed(cookies.Cookier) int`
- `newSeed() int`
- 优先级为 env → cookies 文件 → 生成并保存

[main.go](/home/dietpi/work/xiaohongshu-mcp/main.go:47)：

- 仅 `UseCloakBrowser()` 时创建 cookie store
- 调用 `ResolveFingerprintSeed`
- 写入全局 config
- 必须在创建 `XiaohongshuService` 前完成

[cmd/login/main.go](/home/dietpi/work/xiaohongshu-mcp/cmd/login/main.go:33)：

- 登录浏览器与服务浏览器使用同一个 seed 解析规则
- 传入 `browser.WithFingerprintSeed`
- 避免“登录时一种画像、正式运行另一种画像”

[service.go](/home/dietpi/work/xiaohongshu-mcp/service.go:1673)：

- `newBrowser` 传入 `browser.WithFingerprintSeed(configs.FingerprintSeed())`
- 传入 `browser.WithLanguage(configs.BrowserLanguage())`

[browser/browser.go](/home/dietpi/work/xiaohongshu-mcp/browser/browser.go:15)：

- `browserConfig` 增加 `fingerprintSeed`、`language`
- 新增高层 `WithFingerprintSeed`、`WithLanguage`
- Cloak 且 UA 为空时追加：
  - `headless_browser.WithFingerprint("")`
  - `headless_browser.WithFingerprintSeed(seed)`
  - `headless_browser.WithLanguage(language)`
  - `headless_browser.WithExtraFlags({"fingerprint-brand":"Chrome"})`
- Cloak 且 UA 非空时不追加上述四项
- 继续追加 `WithStealth(false)` 和 `CloakLauncherProfile()`

不再增加另一套 `WithFingerprintEnabled`：现有 `WithCloakBrowser` 就是唯一启用边界。

## 四、go.mod

在 [go.mod](/home/dietpi/work/xiaohongshu-mcp/go.mod:5)：

- 注释从“v0.3.0 Cleanup 卡死”改为“官方 v0.4.0 Cleanup 仍无期限，保留本地 bounded wrapper”
- require：
  - `github.com/xpzouying/headless_browser v0.3.0`
  - 改为 `v0.4.0`
- `replace => ./third_party/headless_browser` 原样保留
- 执行 `go mod tidy`

当前 `go.sum` 已有 v0.4.0 校验项，tidy 后不应重新出现 v0.3.0。

## 五、实施顺序

### P0：纯 wrapper 升基线

1. 合并 Config、Browser 字段和 Option。
2. 合并 fingerprint helper。
3. 将官方 fingerprint 配置嵌入现有 context-aware `New`。
4. 在 `Page()`应用 UA override。
5. 保留 CloseContext、Health、Page、cookies、Cloak、ExtraArgs 扩展。
6. 更新 wrapper 单元测试。

验收：

- 两套 API 均存在。
- `New(ctx,...)(*Browser,error)`未变。
- `CloseContext`完整存在。
- `WithStealth`与`WithStealthJS`控制同一字段。
- wrapper 子模块 `go test ./...` 通过。

### P1：主程序接入

1. 加入持久 seed 解析及测试。
2. `browser/browser.go` 接入 Cloak fingerprint。
3. main、login、service 使用同一 seed/language。
4. 更新 go.mod/go.sum。
5. 更新中英文环境变量说明。

验收：

- Chrome 模式启动参数无 fingerprint。
- Cloak 模式、UA 为空时包含 fingerprint/platform/brand。
- Cloak 模式、UA 非空时不包含 fingerprint，并有清晰 warning。
- login 和 service 日志中的 seed 一致。
- 重启后 seed 不变。

### P2：真实浏览器验收

用 CloakBrowser 验证：

- `navigator.webdriver == false`
- UA 主版本与 `userAgentData.brands/fullVersionList`一致
- `navigator.platform`与 fingerprint-platform 一致
- `navigator.languages`与配置语言一致
- 两个新 page 的身份一致
- 关闭重启后身份指纹一致
- profile/cookies 加载正常，登录状态不丢
- 原 Cloak launcher 仍移除 `enable-automation`
- `NoDefaultDevice()`仍生效
- 自定义 ExtraArgs、proxy、profileDir 仍生效

关闭验收：

- 正常关闭成功
- Chrome 无响应时 `hrod.Browser.Close()`在 10 秒边界内返回
- 超时后浏览器进程被 Kill
- 不出现同步 `launcher.Cleanup()`无限等待

最终执行：

```bash
go test ./...
go vet ./...
go mod verify
git diff --check
```

并确认 MCP 工具注册仍为 22。

## 六、主要风险

- 首次开启 fingerprint 会改变现有 Cloak 用户画像，服务端可能要求重新登录一次；seed 持久化后不得继续漂移。
- UA override 不修改 cookie domain/path，但服务端可能把旧 cookies 与旧 UA/指纹绑定，因此必须验收登录状态。
- `WithUserAgent`与 fingerprint 同时使用会制造 UA/Client-Hints 冲突，必须由主程序互斥、wrapper 二次拒绝。
- CloakBrowser 源码指纹与 go-rod stealth JS 不能叠加；Cloak 路径继续强制关闭 stealth JS。
- `XHS_BROWSER_EXTRA_ARGS/CLOAK_FLAGS`若重复设置 fingerprint 类 flag，可能覆盖 typed option；部署配置必须清理重复项。
- 官方 `New`签名不能覆盖本地签名，否则 `browser.NewBrowser`、启动取消和错误处理全部回归。
- 不允许为了贴近官方 v0.4.0 而删除 bounded Close、UserDataDir、Cloak、ExtraArgs 或非 panic 页面创建能力。
tokens used
2,688,279
结论：采用“官方 v0.4.0 + 现有扩展”，并在 Cloak 模式默认启用 fingerprint，但必须使用持久化 seed。显式 `XHS_BROWSER_USER_AGENT` 优先：一旦设置，自适应 fingerprint/UA override 自动关闭，避免 UA 与 Client Hints 冲突。


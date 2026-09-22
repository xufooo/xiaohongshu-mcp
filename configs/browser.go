package configs

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var (
	useHeadless = true

	binPath = ""

	profileDir = ""

	browserMode = "auto"

	browserIdleTimeout = 5 * time.Minute

	browserSessionIdleGrace = time.Minute

	browserExtraArgs []string

	browserUserAgent = ""

	fingerprintSeed = 0
)

func InitHeadless(h bool) {
	useHeadless = h
}

// IsHeadless 是否无头模式。
func IsHeadless() bool {
	return useHeadless
}

func SetBinPath(b string) {
	binPath = b
}

func GetBinPath() string {
	return binPath
}

// SetProfileDir 设置浏览器持久 profile 目录。
func SetProfileDir(path string) {
	profileDir = path
}

// GetProfileDir 返回浏览器持久 profile 目录。
func GetProfileDir() string {
	return profileDir
}

// SetBrowserMode 设置浏览器模式，支持 auto、chrome 和 cloak。
func SetBrowserMode(mode string) {
	browserMode = strings.ToLower(strings.TrimSpace(mode))
}

// UseCloakBrowser 判断当前是否使用 CloakBrowser。
func UseCloakBrowser() bool {
	switch browserMode {
	case "cloak":
		return true
	case "chrome":
		return false
	}

	return false
}

// CloakLauncherProfile 判断是否启用 CloakBrowser 专用 launcher 配置。
func CloakLauncherProfile() bool {
	return UseCloakBrowser()
}

// SetBrowserIdleTimeout 设置浏览器空闲回收时间。
func SetBrowserIdleTimeout(timeout time.Duration) {
	browserIdleTimeout = timeout
}

// GetBrowserIdleTimeout 返回浏览器空闲回收时间。
func GetBrowserIdleTimeout() time.Duration {
	return browserIdleTimeout
}

// SetBrowserSessionIdleGrace 设置 session 释放后的浏览器保留宽限时间。
func SetBrowserSessionIdleGrace(grace time.Duration) {
	browserSessionIdleGrace = grace
}

// GetBrowserSessionIdleGrace 返回 session 释放后的浏览器保留宽限时间。
func GetBrowserSessionIdleGrace() time.Duration {
	return browserSessionIdleGrace
}

// GetBrowserStartupTimeout 返回用户配置的浏览器启动超时时间。
func GetBrowserStartupTimeout() time.Duration {
	rawTimeout := strings.TrimSpace(os.Getenv("XHS_BROWSER_STARTUP_TIMEOUT"))
	if rawTimeout == "" {
		return 0
	}
	timeout, err := time.ParseDuration(rawTimeout)
	if err != nil {
		return 0
	}
	return timeout
}

// SetBrowserExtraArgs 设置附加浏览器启动参数。
func SetBrowserExtraArgs(args []string) {
	browserExtraArgs = append([]string(nil), args...)
}

// GetBrowserExtraArgs 返回附加浏览器启动参数。
func GetBrowserExtraArgs() []string {
	return append([]string(nil), browserExtraArgs...)
}

func SetBrowserUserAgent(userAgent string) {
	browserUserAgent = userAgent
}

func GetBrowserUserAgent() string {
	if browserUserAgent != "" {
		return browserUserAgent
	}
	return strings.TrimSpace(os.Getenv("XHS_BROWSER_USER_AGENT"))
}

// SetFingerprintSeed 设置 CloakBrowser fingerprint 的持久 seed。
func SetFingerprintSeed(seed int) {
	fingerprintSeed = seed
}

// FingerprintSeed 返回当前配置的 fingerprint seed。
func FingerprintSeed() int {
	return fingerprintSeed
}

// BrowserLanguage 返回浏览器语言；读取 XHS_BROWSER_LANG，空时默认 zh-CN。
func BrowserLanguage() string {
	if lang := strings.TrimSpace(os.Getenv("XHS_BROWSER_LANG")); lang != "" {
		return lang
	}
	return "zh-CN"
}

// UseFixedIdentity 是否启用浏览器身份指纹漂移检测。
func UseFixedIdentity() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("XHS_FIXED_IDENTITY"))) {
	case "0", "false", "off", "no":
		return false
	default:
		return true
	}
}

// LowResourceProfile 判断是否启用低资源（树莓派 3B 等 1GB 设备）浏览器档。
// 规则：XHS_LOW_RESOURCE 显式设置时以它为准；未设置时 arm/arm64 默认启用
// （本项目的首要部署目标是树莓派）。x86/darwin 默认不启用，保持上游行为。
func LowResourceProfile() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("XHS_LOW_RESOURCE"))) {
	case "1", "true", "on", "yes":
		return true
	case "0", "false", "off", "no":
		return false
	}
	switch runtime.GOARCH {
	case "arm", "arm64":
		return true
	default:
		return false
	}
}

// BrowserJSHeapMB 返回 V8 old space 上限（MB）。XHS_BROWSER_JS_HEAP_MB 优先，
// 未设置或非法时低资源档用 192MB，其他用 256MB。
func BrowserJSHeapMB() int {
	if v := envPositiveInt("XHS_BROWSER_JS_HEAP_MB"); v > 0 {
		return v
	}
	if LowResourceProfile() {
		return 192
	}
	return 256
}

// BrowserRendererLimit 返回 renderer 进程数上限。XHS_BROWSER_RENDERER_LIMIT 优先，
// 默认 2（配合 rod 默认关闭 site-per-process，压住 1GB 设备上的进程数）。
func BrowserRendererLimit() int {
	if v := envPositiveInt("XHS_BROWSER_RENDERER_LIMIT"); v > 0 {
		return v
	}
	return 2
}

// BrowserBlockedURLPatterns 返回逐页拦截的 URL 模式。
// XHS_BROWSER_BLOCK_URLS 逗号分隔且**优先**（设为 "-" 表示不拦截任何资源，
// 空值按"未设置"处理）；未设置时不拦截资源，低资源媒体拦截需显式配置。
func BrowserBlockedURLPatterns() []string {
	raw := strings.TrimSpace(os.Getenv("XHS_BROWSER_BLOCK_URLS"))
	if raw == "-" {
		return nil
	}
	if raw != "" {
		patterns := make([]string, 0, 4)
		for _, part := range strings.Split(raw, ",") {
			if p := strings.TrimSpace(part); p != "" {
				patterns = append(patterns, p)
			}
		}
		return patterns
	}
	return nil
}

// defaultIdentityCheckInterval 身份指纹采集的默认节流间隔。
// 同一浏览器 + 同一 profile 下指纹不会变化，10 分钟采集一次足以发现漂移。
const defaultIdentityCheckInterval = 10 * time.Minute

// IdentityCheckInterval 返回身份指纹采集的最小间隔。
// XHS_IDENTITY_CHECK_INTERVAL 未设置用 10m；"0" 表示每次都采集；非法值回落默认。
func IdentityCheckInterval() time.Duration {
	raw := strings.TrimSpace(os.Getenv("XHS_IDENTITY_CHECK_INTERVAL"))
	if raw == "" {
		return defaultIdentityCheckInterval
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed < 0 {
		return defaultIdentityCheckInterval
	}
	return parsed
}

// DefaultBrowserIdleTimeout 返回浏览器空闲回收默认值。
// 低资源档（树莓派）上 Chromium 冷启动与首屏导航是分钟级成本，
// 5 分钟就回收等于把冷启动反复重付；默认放宽到 30 分钟。
// XHS_BROWSER_IDLE_TIMEOUT 可覆盖（0 或负值表示不自动回收）。
func DefaultBrowserIdleTimeout() time.Duration {
	if LowResourceProfile() {
		return 30 * time.Minute
	}
	return 5 * time.Minute
}

// envPositiveInt 读取正整数环境变量，缺失或非法返回 0。
func envPositiveInt(name string) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return 0
	}
	return v
}

func UseWriteConfirmation() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("XHS_WRITE_CONFIRM"))) {
	case "1", "true", "on", "yes":
		return true
	default:
		return false
	}
}

func UseNetworkCapture() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("XHS_NETWORK_CAPTURE"))) {
	case "1", "true", "on", "yes":
		return true
	default:
		return false
	}
}

// BrowserExtraArgsFromEnv 读取用户配置的附加启动参数。
// 参数必须使用 --名称 或 --名称=值 形式，以空白字符分隔。
func BrowserExtraArgsFromEnv() []string {
	args := make([]string, 0)
	if lang := strings.TrimSpace(os.Getenv("XHS_BROWSER_LANG")); lang != "" {
		args = append(args, "--lang="+lang)
	}
	if timezone := strings.TrimSpace(os.Getenv("XHS_BROWSER_TIMEZONE")); timezone != "" {
		args = append(args, "--timezone="+timezone)
	}
	args = append(args, strings.Fields(os.Getenv("CLOAK_FLAGS"))...)
	args = append(args, strings.Fields(os.Getenv("XHS_BROWSER_EXTRA_ARGS"))...)
	return args
}

// profilePersistent 记录启动时解析出的 profile 是否落在持久目录，
// 供 get_page_state.browser 暴露（false = 每次冷启动都是冷缓存）。
var profilePersistent bool

// SetBrowserProfilePersistent 由入口层在解析 profile 后调用。
func SetBrowserProfilePersistent(persistent bool) { profilePersistent = persistent }

// BrowserProfilePersistent 返回 profile 是否持久。
func BrowserProfilePersistent() bool { return profilePersistent }

// DirWritable 用一次临时文件创建探测目录是否真的可写（MkdirAll 成功不代表可写）。
func DirWritable(dir string) bool {
	file, err := os.CreateTemp(dir, ".probe-*")
	if err != nil {
		return false
	}
	name := file.Name()
	_ = file.Close()
	_ = os.Remove(name)
	return true
}

// defaultBrowserProfileDir 返回默认的浏览器 profile 目录（与 action_state 同一约定）。
func defaultBrowserProfileDir() string {
	if dir, err := os.UserCacheDir(); err == nil && dir != "" {
		return filepath.Join(dir, "xiaohongshu-mcp", "browser-profile")
	}
	return filepath.Join(os.TempDir(), "xiaohongshu-mcp-browser-profile")
}

// ResolveBrowserProfileDir 解析浏览器持久 profile 目录。
//
// XHS_BROWSER_PROFILE_DIR 显式设置时原样使用（第二个返回值恒为 true）。
// 未设置时给一个稳定路径，而不是让 go-rod 用 /tmp/rod/user-data/<随机>：
// 随机目录等于每次冷启动都是冷缓存，实测同一 profile 第二次访问首屏下载
// 3.38MB → 0.27MB（−92%），其中 JS 3.01MB → 0.11MB。
//
// 第二个返回值表示缓存是否持久：false 表示落到了临时目录（只读 rootfs），
// 调用方应明确告警——此时行为与"不设 profile"等价。
func ResolveBrowserProfileDir() (string, bool) {
	if v := strings.TrimSpace(os.Getenv("XHS_BROWSER_PROFILE_DIR")); v != "" {
		return v, true
	}
	dir := defaultBrowserProfileDir()
	if err := os.MkdirAll(dir, 0755); err == nil && DirWritable(dir) {
		return dir, true
	}
	return filepath.Join(os.TempDir(), "xiaohongshu-mcp-browser-profile"), false
}

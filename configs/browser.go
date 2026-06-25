package configs

import (
	"os"
	"strings"
	"time"
)

var (
	useHeadless = true

	binPath = ""

	profileDir = ""

	browserMode = "auto"

	browserIdleTimeout = 5 * time.Minute

	browserExtraArgs []string
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

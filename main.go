package main

import (
	"flag"
	"os"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/configs"
	"github.com/xpzouying/xiaohongshu-mcp/cookies"
)

func main() {
	logrus.SetOutput(os.Stdout)
	logrus.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})

	var (
		headless bool
		binPath  string // 浏览器二进制文件路径
		port     string
	)
	flag.BoolVar(&headless, "headless", true, "是否无头模式")
	flag.StringVar(&binPath, "bin", "", "浏览器二进制文件路径")
	flag.StringVar(&port, "port", ":18060", "端口")
	flag.Parse()

	if len(binPath) == 0 {
		binPath = os.Getenv("ROD_BROWSER_BIN")
	}
	profileDir := os.Getenv("XHS_BROWSER_PROFILE_DIR")
	browserMode := os.Getenv("XHS_BROWSER_MODE")
	browserUserAgent := os.Getenv("XHS_BROWSER_USER_AGENT")
	idleTimeout := 5 * time.Minute
	if rawTimeout := os.Getenv("XHS_BROWSER_IDLE_TIMEOUT"); rawTimeout != "" {
		parsed, err := time.ParseDuration(rawTimeout)
		if err != nil {
			logrus.Warnf("invalid XHS_BROWSER_IDLE_TIMEOUT %q, using %s", rawTimeout, idleTimeout)
		} else {
			idleTimeout = parsed
		}
	}
	sessionIdleGrace := time.Minute
	if rawGrace := os.Getenv("XHS_BROWSER_SESSION_IDLE_GRACE"); rawGrace != "" {
		parsed, err := time.ParseDuration(rawGrace)
		if err != nil {
			logrus.Warnf("invalid XHS_BROWSER_SESSION_IDLE_GRACE %q, using %s", rawGrace, sessionIdleGrace)
		} else {
			sessionIdleGrace = parsed
		}
	}
	if binPath != "" {
		logrus.Infof("using browser binary: %s", binPath)
	} else {
		logrus.Infof("browser binary is not configured; rod will auto-detect or download Chromium")
	}

	configs.InitHeadless(headless)
	configs.SetBinPath(binPath)
	configs.SetProfileDir(profileDir)
	configs.SetBrowserMode(browserMode)
	configs.SetBrowserIdleTimeout(idleTimeout)
	configs.SetBrowserSessionIdleGrace(sessionIdleGrace)
	configs.SetBrowserUserAgent(browserUserAgent)
	configs.SetBrowserExtraArgs(configs.BrowserExtraArgsFromEnv())
	if profileDir != "" {
		logrus.Infof("using persistent browser profile: %s", profileDir)
	}
	logrus.Infof("browser idle timeout: %s", idleTimeout)
	if configs.UseCloakBrowser() {
		logrus.Info("using CloakBrowser mode")
		// 仅在未配置显式 UA 时解析并固定 fingerprint seed（fingerprint 跳过时不得落盘），
		// 且必须在创建服务前完成，保证登录/服务同一画像。
		if configs.GetBrowserUserAgent() == "" {
			cookieLoader := cookies.NewLoadCookie(cookies.GetCookiesFilePath())
			seed := configs.ResolveFingerprintSeed(cookieLoader)
			configs.SetFingerprintSeed(seed)
			logrus.Info("CloakBrowser fingerprint seed pinned")
		}
	}

	// 初始化服务
	xiaohongshuService, err := NewXiaohongshuService()
	if err != nil {
		logrus.Fatalf("failed to initialize service: %v", err)
	}

	// 创建并启动应用服务器
	appServer := NewAppServer(xiaohongshuService)
	if err := appServer.Start(port); err != nil {
		logrus.Fatalf("failed to run server: %v", err)
	}
}

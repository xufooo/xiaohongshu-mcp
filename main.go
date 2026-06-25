package main

import (
	"flag"
	"os"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/configs"
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
	idleTimeout := 5 * time.Minute
	if rawTimeout := os.Getenv("XHS_BROWSER_IDLE_TIMEOUT"); rawTimeout != "" {
		parsed, err := time.ParseDuration(rawTimeout)
		if err != nil {
			logrus.Warnf("invalid XHS_BROWSER_IDLE_TIMEOUT %q, using %s", rawTimeout, idleTimeout)
		} else {
			idleTimeout = parsed
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
	configs.SetBrowserExtraArgs(configs.BrowserExtraArgsFromEnv())
	if profileDir != "" {
		logrus.Infof("using persistent browser profile: %s", profileDir)
	}
	logrus.Infof("browser idle timeout: %s", idleTimeout)
	if configs.UseCloakBrowser() {
		logrus.Info("using CloakBrowser mode")
	}

	// 初始化服务
	xiaohongshuService := NewXiaohongshuService()

	// 创建并启动应用服务器
	appServer := NewAppServer(xiaohongshuService)
	if err := appServer.Start(port); err != nil {
		logrus.Fatalf("failed to run server: %v", err)
	}
}

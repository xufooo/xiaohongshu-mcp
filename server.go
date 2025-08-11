package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// setupRouter 设置路由
func setupRouter() *gin.Engine {
	// 设置 Gin 模式
	gin.SetMode(gin.ReleaseMode)

	router := gin.New()

	router.Use(gin.Logger())
	router.Use(gin.Recovery())

	// 添加中间件
	router.Use(errorHandlingMiddleware())
	router.Use(corsMiddleware())

	// 健康检查
	router.GET("/health", healthHandler)

	// MCP 端点 - 使用 SSE 协议
	mcpHandler := createMCPHandler()
	router.Any("/mcp", gin.WrapH(mcpHandler))
	router.Any("/mcp/*path", gin.WrapH(mcpHandler))

	// API 路由组
	api := router.Group("/api/v1")
	{
		api.GET("/login/status", checkLoginStatusHandler)

		api.POST("/publish", publishHandler)

		// Feeds 相关路由
		api.GET("/feeds/list", listFeedsHandler)
	}

	return router
}

// startServer 启动服务器
func startServer() error {
	router := setupRouter()

	port := ":18060"
	server := &http.Server{
		Addr:    port,
		Handler: router,
	}

	// 启动服务器的 goroutine
	go func() {
		logrus.Infof("启动 HTTP 服务器: %s", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logrus.Errorf("服务器启动失败: %v", err)
			os.Exit(1)
		}
	}()

	// 等待中断信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logrus.Infof("正在关闭服务器...")

	// 优雅关闭
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 关闭 HTTP 服务器
	if err := server.Shutdown(ctx); err != nil {
		logrus.Errorf("服务器关闭失败: %v", err)
		return err
	}

	logrus.Infof("服务器已关闭")
	return nil
}

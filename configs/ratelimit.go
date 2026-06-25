package configs

import (
	"github.com/xpzouying/xiaohongshu-mcp/cookies"
	"github.com/xpzouying/xiaohongshu-mcp/pkg/ratelimit"
)

// DefaultRateLimitConfig 返回当前进程使用的账号维度限流配置。
func DefaultRateLimitConfig() ratelimit.Config {
	cfg := ratelimit.DefaultConfig()
	cfg.Account = ratelimit.AccountKey{
		AccountID:   Username,
		ProfileDir:  GetProfileDir(),
		CookiesPath: cookies.GetCookiesFilePath(),
	}
	return cfg
}

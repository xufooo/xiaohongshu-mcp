package configs

// Username 用作限流模块的账号标识。
// 真实昵称通过 loginAction.CurrentUser 从页面读取，此值仅用作 rate limit key。
const (
	Username = "xiaohongshu-mcp"
)

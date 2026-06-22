# 遗留问题修复方案

fixup 分支还有两个 P2 问题需要写修复方案。请阅读代码后，为每个问题输出具体的代码级修复方案（Markdown 格式）。

## 问题 1：xiaohongshu/ 下大量 time.Sleep 未响应 context 取消

请阅读 xiaohongshu/ 目录下的所有 .go 文件（publish.go, comment_feed.go, publish_video.go, feed_detail.go, like_favorite.go, login.go, feeds.go），分析：
- 哪些函数已经有 ctx context.Context 参数
- time.Sleep 的分布
- 可复用的封装模式（已经有一个 sleepRandom() 函数）

同时阅读 pkg/humanize/util.go（已有 sleepWithContext）和 service.go（page.Context(ctx) 调用模式）。

输出：最小侵入的替换方案，不改函数签名。

## 问题 2：hrod Page.wrapPage() 重建 actor 丢失鼠标/键盘状态

请阅读 pkg/humanize/rod/hrod.go 中：
- Page.wrapPage()（L110-123）：每次都 humanize.NewWithContext 创建新 actor
- Page.Context()（L131-136）：调用 wrapPage 后额外设置了 ctx 和 actor.SetContext
- Page.Timeout()（L139-141）：只调 wrapPage
- Browser.wrapPage()（L68-79）：做了 InitPosition()

同时阅读 pkg/humanize/humanize.go（Actor 结构体）
pkg/humanize/mouse.go（initialized 标志）
pkg/humanize/keyboard.go（lastEl 字段）

输出：复用 p.actor 而非重建的方案。

## 输出格式

请直接输出完整的 Markdown 内容，包含：
- 每个问题的根因分析
- 需要修改的文件、行号、代码片段
- 风险评估
- 优先级建议
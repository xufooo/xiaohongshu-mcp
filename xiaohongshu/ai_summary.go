package xiaohongshu

import (
	"context"
	"errors"
	"fmt"
	"strings"

	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
)

// AI 总结（搜索页的「问点点」/ AI 搜索答案）。
//
// 实测（search_result_ai）：AI 答案与完成标志都在页面 state 的
// conversation.activeConversation.rounds[].aiMessage 里——
// text 流式增长，round.isComplete 与 aiMessage.isFinished 是**页面自己的完成标志**。
// 答案是在笔记就绪**之后**才开始生成的（实测 x86：搜索 11.4s 返回时 0 字，+13s 才 1437 字完成），
// 所以搜索路径只"顺手读一次"（3s 上限，读不到就不返回），要完整答案必须显式等——
// 等的判据就是上面两个标志，不是"预计要多久"。

// readAISummaryFromState 等 AI 总结生成完再返回；页面根本没有 AI 会话时立刻如实报错。
func readAISummaryFromState(ctx context.Context, page *hrod.Page) (*AIChatReply, error) {
	var last *AIChatReply
	var lastErr error

	counter := &evalTimeoutCounter{}
	err := waitForCondition(waitRound{
		Kind: "ai_summary",
		Page: page,
		Probe: func() (string, bool, error) {
			probe, err := probeAIResponseState(ctx, page, counter, "", -1)
			if err != nil {
				lastErr = err
				return "", false, err
			}
			lastErr = nil
			if !probe.ConversationActive {
				// 普通搜索页没有 AI 会话：等下去也不会出现，立刻如实报错。
				return "", false, fatalWait(errors.New("当前搜索页没有 AI 总结（该关键词未触发 AI 回复）"))
			}
			reply, pending := normalizeAIResponse(probe)
			if reply == nil {
				return "", false, errors.New("AI 总结尚未生成内容")
			}
			last = reply
			return reply.Content, !pending, nil
		},
		OnExhausted: func() error {
			if lastErr != nil {
				return fmt.Errorf("等待 AI 总结失败: %w", lastErr)
			}
			if last != nil {
				return fmt.Errorf("AI 总结生成超时（已收到 %d 字）", len([]rune(last.Content)))
			}
			return errors.New("AI 总结生成超时")
		},
	})
	if err != nil {
		return nil, err
	}
	if last == nil {
		return nil, errors.New("AI 总结为空")
	}
	last.HasMore = false
	last.Content = strings.TrimSpace(last.Content)
	return last, nil
}

package xiaohongshu

import (
	"errors"
	"fmt"
	"time"

	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
)

// waitloop.go：所有「等一个条件成立」的地方共用同一套机制。
//
// 机制（2026-09-21 定，见 docs/pi3b-source-analysis.md §11）：
//   - 什么时候再探测：由页面自身的信号决定（DOM 静默 settle），不按固定间隔轮询；
//     窗口由本机实测的探测成本给出（见 pageSignalWindow），是给自己定节奏，不是预测就绪时刻。
//   - 什么时候算卡死：看有没有动静——状态指纹变了 / DOM 变了 / 有数据请求事件，
//     三者全无持续 readyStallBudget 才报错。慢机器只是慢，它的页面一直在动。
//   - 什么时候放弃：只用一个刻意宽松的上限兜底（waitCeilings），它不参与"该等多久"。
//
// 这样同一份代码在 x86 与树莓派上都不会因为"机器当时的状态"被判错。

// fatalWaitError 标记"必须立刻结束等待"的错误（例如页面命中风控信号）。
type fatalWaitError struct{ err error }

func (e fatalWaitError) Error() string { return e.err.Error() }
func (e fatalWaitError) Unwrap() error { return e.err }

// fatalWait 把错误标记为致命：不再继续等，直接返回给调用方。
func fatalWait(err error) error { return fatalWaitError{err} }

// isFatalWaitError 判断错误是否应立刻结束等待。
func isFatalWaitError(err error) bool {
	if err == nil {
		return false
	}
	if IsFatalRendererError(err) {
		return true
	}
	var fatal fatalWaitError
	return errors.As(err, &fatal)
}

// waitRound 是「等一个条件成立」的一轮配置。
type waitRound struct {
	// Kind 是观测与失败上限的种类，形如 "ready:detail"、"search_results"。
	Kind string
	Page *hrod.Page
	// Probe 探测一次：返回状态指纹、条件是否成立、错误。
	// 指纹只用于判断"页面有没有动静"（慢机器上它会持续变化）。
	Probe func() (fingerprint string, ready bool, err error)
	// Ceiling 覆盖失败上限（0 = 用 waitCeilings[Kind]，再退回 60s）。
	Ceiling time.Duration
	// OnReady 条件成立后的收尾（可空）。
	OnReady func()
	// OnExhausted 到达失败上限时给出最终错误（可空）：
	// 返回 nil 表示"按兜底路径算成功"（例如 URL 兜底命中）。
	OnExhausted func() error
}

// waitForCondition 按上面的机制等条件成立。
func waitForCondition(round waitRound) error {
	if round.Page == nil {
		return errors.New("等待条件失败: page 为空")
	}
	ceiling := round.Ceiling
	if ceiling <= 0 {
		if registered, ok := waitCeilingForKind(round.Kind); ok {
			ceiling = registered
		} else {
			ceiling = 60 * time.Second
		}
	}

	started := time.Now()
	probes := 0
	defer func() { observeWaitWithProbes(round.Kind, time.Since(started), probes) }()

	deadline := started.Add(ceiling)
	lastProgressAt := started
	fingerprint := ""
	haveFingerprint := false
	var lastErr error
	pollMin, pollMax := waitPollRange(round.Kind)

	for {
		if err := round.Page.Err(); err != nil {
			return err
		}

		probes++
		probeStarted := time.Now()
		current, ready, err := round.Probe()
		// 探测成本入观测：慢机器上探测更贵，信号窗随之放宽（见 pageSignalWindow）。
		observeWait("probe:"+round.Kind, time.Since(probeStarted))

		if err != nil {
			if isFatalWaitError(err) {
				return err
			}
			lastErr = err
		} else {
			lastErr = nil
			if !haveFingerprint || current != fingerprint {
				lastProgressAt = time.Now()
			}
			fingerprint, haveFingerprint = current, true
			if ready {
				if round.OnReady != nil {
					round.OnReady()
				}
				return nil
			}
		}

		if !time.Now().Before(deadline) {
			if round.OnExhausted != nil {
				return round.OnExhausted()
			}
			if lastErr != nil {
				return fmt.Errorf("等待超时(%s, kind=%s): %w", ceiling, round.Kind, lastErr)
			}
			return fmt.Errorf("等待超时(%s, kind=%s): 条件未成立", ceiling, round.Kind)
		}

		// 页面长时间毫无动静就不是"慢"，是卡住了：早报错，别耗到失败上限。
		if time.Since(lastProgressAt) >= readyStallBudget {
			if lastErr != nil {
				return fmt.Errorf("页面停止推进（%s 内无 DOM 变化、状态指纹不变, kind=%s）: %w",
					readyStallBudget, round.Kind, lastErr)
			}
			return fmt.Errorf("页面停止推进（%s 内无 DOM 变化、状态指纹不变, kind=%s）",
				readyStallBudget, round.Kind)
		}

		settle, maxWait := pageSignalWindow(round.Kind, pollMax)
		signal, err := waitForPageSignal(round.Page.Rod.GetContext(), round.Page, settle, maxWait)
		if err != nil {
			// 拿不到页面信号（正在整页跳转 / 上下文已销毁）：用小步长兜底，避免空转。
			lastErr = fmt.Errorf("等待页面变化信号失败: %w", err)
			if sleepErr := round.Page.Sleep(pollMin); sleepErr != nil {
				return sleepErr
			}
			continue
		}
		if signal.Mutations > 0 {
			lastProgressAt = time.Now()
		}
	}
}

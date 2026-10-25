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
//   - 状态指纹与 DOM 静默都不能证明卡死：在途请求可能长期不改变 DOM。
//     只有目标条件成立才成功；取消、明确致命错误或安全上限才结束等待。
//   - 什么时候放弃：只用一个刻意宽松的上限兜底（waitCeilings），它不参与"该等多久"。
//
// 安全上限到达只表示结果未知，不证明页面已损坏。

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
	// 指纹供探测方描述状态；静止不作为失败依据。
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
	return waitForConditionWithClock(round, time.Now)
}

// A clock seam keeps safety-bound tests deterministic without real minute waits.
func waitForConditionWithClock(round waitRound, now func() time.Time) error {
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

	deadline := now().Add(ceiling)
	var lastErr error
	pollMin, pollMax := waitPollRange(round.Kind)

	for {
		if err := round.Page.Err(); err != nil {
			return err
		}

		probes++
		probeStarted := time.Now()
		_, ready, err := round.Probe()
		// 探测成本入观测：慢机器上探测更贵，信号窗随之放宽（见 pageSignalWindow）。
		observeWait("probe:"+round.Kind, time.Since(probeStarted))

		if isFatalWaitError(err) {
			return err
		}
		if pageErr := round.Page.Err(); pageErr != nil {
			return pageErr
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = nil
			if ready {
				if round.OnReady != nil {
					round.OnReady()
				}
				return nil
			}
		}

		if !now().Before(deadline) {
			if round.OnExhausted != nil {
				return round.OnExhausted()
			}
			if lastErr != nil {
				return fmt.Errorf("等待达到安全上限，条件未确认(%s, kind=%s): %w", ceiling, round.Kind, lastErr)
			}
			return fmt.Errorf("等待达到安全上限，条件未确认(%s, kind=%s): 条件未成立", ceiling, round.Kind)
		}

		settle, maxWait := pageSignalWindow(round.Kind, pollMax)
		_, err = waitForPageSignal(round.Page.Rod.GetContext(), round.Page, settle, maxWait)
		if err != nil {
			if pageErr := round.Page.Err(); pageErr != nil {
				return pageErr
			}
			if isFatalWaitError(err) {
				return err
			}
			// 拿不到页面信号（正在整页跳转 / 上下文已销毁）：用小步长兜底，避免空转。
			lastErr = fmt.Errorf("等待页面变化信号失败: %w", err)
			if sleepErr := round.Page.Sleep(pollMin); sleepErr != nil {
				return sleepErr
			}
			continue
		}
	}
}

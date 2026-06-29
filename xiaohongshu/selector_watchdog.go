package xiaohongshu

import (
	"fmt"
	"sync"
	"time"

	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
	"github.com/sirupsen/logrus"
)

// SelectorHealthKind 选择器健康状态
type SelectorHealthKind string

const (
	SelectorHealthUnknown  SelectorHealthKind = "unknown"   // 未检测
	SelectorHealthHealthy  SelectorHealthKind = "healthy"   // 上次检测正常
	SelectorHealthSuspicious SelectorHealthKind = "suspicious" // count=0 但非 Required
	SelectorHealthDegraded SelectorHealthKind = "degraded"  // Required 选择器 count=0
)

// SelectorHealthEntry 单个选择器的健康记录
type SelectorHealthEntry struct {
	Name        string             `json:"name"`
	Selector    string             `json:"selector"`
	Purpose     string             `json:"purpose"`
	Required    bool               `json:"required"`
	LastChecked time.Time          `json:"last_checked"`
	LastCount   int                `json:"last_count"`
	LastVisible int                `json:"last_visible"`
	Status      SelectorHealthKind `json:"status"`
	Samples     []string           `json:"samples,omitempty"`
}

// SelectorWatchdog 选择器健康看门狗
// 负责检测上游（小红书）DOM 变更导致的选择器失效，发出警告。
// 不阻断操作——只记录和报告，由调用方决定如何处理。
type SelectorWatchdog struct {
	mu      sync.RWMutex
	entries map[string]*SelectorHealthEntry // name -> entry
}

// NewSelectorWatchdog 创建看门狗
func NewSelectorWatchdog() *SelectorWatchdog {
	return &SelectorWatchdog{
		entries: make(map[string]*SelectorHealthEntry),
	}
}

// Register 注册一个选择器到监控列表
func (w *SelectorWatchdog) Register(spec SelectorSpec) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.entries[spec.Name] = &SelectorHealthEntry{
		Name:     spec.Name,
		Selector: spec.Selector,
		Purpose:  spec.Purpose,
		Required: spec.Required,
		Status:   SelectorHealthUnknown,
	}
}

// RegisterAll 注册所有核心选择器
func (w *SelectorWatchdog) RegisterAll() {
	w.Register(SearchInputSpec)
	w.Register(SearchResultSpec)
	w.Register(FeedDetailReadySpec)
	w.Register(CommentBoxSpec)
	w.Register(LikeButtonSpec)
}

// ProbeOne 用已有的页面 probe 一个选择器的健康状况。
// 返回 (是否健康, 变更警告)
func (w *SelectorWatchdog) ProbeOne(page *hrod.Page, name string) (healthy bool, warning string) {
	w.mu.RLock()
	entry, ok := w.entries[name]
	w.mu.RUnlock()
	if !ok {
		return false, ""
	}

	results, err := ProbeSelectors(page, []SelectorSpec{{
		Name:        entry.Name,
		Selector:    entry.Selector,
		Required:    entry.Required,
		VisibleOnly: false,
	}})
	if err != nil {
		return false, fmt.Sprintf("探测选择器 %q 失败: %v", name, err)
	}
	if len(results) == 0 {
		return false, fmt.Sprintf("探测选择器 %q 无返回结果", name)
	}

	r := results[0]
	w.mu.Lock()
	entry.LastChecked = time.Now()
	entry.LastCount = r.Count
	entry.LastVisible = r.VisibleCount
	entry.Samples = r.Samples

	prevStatus := entry.Status

	if r.Count > 0 {
		entry.Status = SelectorHealthHealthy
	} else if entry.Required {
		entry.Status = SelectorHealthDegraded
	} else {
		entry.Status = SelectorHealthSuspicious
	}
	currStatus := entry.Status
	w.mu.Unlock()

	// 仅在状态恶化时发出警告
	var warn string
	if currStatus == SelectorHealthDegraded && prevStatus != SelectorHealthDegraded {
		warn = fmt.Sprintf("⚠️ 上游变更: 核心选择器 %q(%s) 命中数为 0, 功能可能不可用",
			entry.Name, entry.Purpose)
		logrus.Warn(warn)
	} else if currStatus == SelectorHealthSuspicious && prevStatus == SelectorHealthHealthy {
		warn = fmt.Sprintf("⚠️ 选择器 %q(%s) 命中数为 0(非必需), DOM 可能变化",
			entry.Name, entry.Purpose)
		logrus.Warn(warn)
	}

	return r.Count > 0, warn
}

// ProbeAll 用页面 probe 所有已注册的选择器。
// 返回 (是否全部健康, 警告列表)
func (w *SelectorWatchdog) ProbeAll(page *hrod.Page) (allHealthy bool, warnings []string) {
	w.mu.RLock()
	names := make([]string, 0, len(w.entries))
	for name := range w.entries {
		names = append(names, name)
	}
	w.mu.RUnlock()

	specs := make([]SelectorSpec, 0, len(names))
	for _, name := range names {
		w.mu.RLock()
		entry := w.entries[name]
		w.mu.RUnlock()
		if entry != nil {
			specs = append(specs, SelectorSpec{
				Name:        entry.Name,
				Selector:    entry.Selector,
				Required:    entry.Required,
				VisibleOnly: false,
			})
		}
	}

	results, err := ProbeSelectors(page, specs)
	if err != nil {
		return false, []string{fmt.Sprintf("批量探测选择器失败: %v", err)}
	}

	allHealthy = true
	for _, r := range results {
		w.mu.Lock()
		entry := w.entries[r.Name]
		if entry != nil {
			prevStatus := entry.Status
			entry.LastChecked = time.Now()
			entry.LastCount = r.Count
			entry.LastVisible = r.VisibleCount
			entry.Samples = r.Samples

			if r.Count > 0 {
				entry.Status = SelectorHealthHealthy
			} else if entry.Required {
				entry.Status = SelectorHealthDegraded
				allHealthy = false
			} else {
				entry.Status = SelectorHealthSuspicious
			}

			// 状态恶化时记录
			if entry.Status == SelectorHealthDegraded && prevStatus != SelectorHealthDegraded {
				warn := fmt.Sprintf("⚠️ 上游变更: 核心选择器 %q(%s) 命中数为 0, 功能可能不可用",
					entry.Name, entry.Purpose)
				warnings = append(warnings, warn)
				logrus.Warn(warn)
			} else if entry.Status == SelectorHealthSuspicious && prevStatus == SelectorHealthHealthy {
				warn := fmt.Sprintf("⚠️ 选择器 %q(%s) 命中数为 0(非必需), DOM 可能变化",
					entry.Name, entry.Purpose)
				warnings = append(warnings, warn)
				logrus.Warn(warn)
			}
		}
		w.mu.Unlock()
	}

	return allHealthy, warnings
}

// Status 返回所有选择器当前健康状态快照
func (w *SelectorWatchdog) Status() []SelectorHealthEntry {
	w.mu.RLock()
	defer w.mu.RUnlock()
	result := make([]SelectorHealthEntry, 0, len(w.entries))
	for _, e := range w.entries {
		result = append(result, *e)
	}
	return result
}

// Summary 返回简洁的健康摘要
func (w *SelectorWatchdog) Summary() string {
	w.mu.RLock()
	defer w.mu.RUnlock()

	var total, healthy, degraded, suspicious, unknown int
	for _, e := range w.entries {
		total++
		switch e.Status {
		case SelectorHealthHealthy:
			healthy++
		case SelectorHealthDegraded:
			degraded++
		case SelectorHealthSuspicious:
			suspicious++
		default:
			unknown++
		}
	}
	return fmt.Sprintf("选择器健康: %d/%d 正常, %d 退化, %d 可疑, %d 未检测",
		healthy, total, degraded, suspicious, unknown)
}

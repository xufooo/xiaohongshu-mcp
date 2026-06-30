package xiaohongshu

import (
	"fmt"
	"sync"
	"time"

	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
	"github.com/sirupsen/logrus"
)

// DefaultSelectorWatchdog 全局默认看门狗实例。
// 由 AppServer 初始化时设置，所有 WaitForXHSReady 调用自动使用。
// 为 nil 时不进行探测。
var DefaultSelectorWatchdog *SelectorWatchdog

// SelectorHealthKind 选择器健康状态
type SelectorHealthKind string

const (
	SelectorHealthUnknown    SelectorHealthKind = "unknown"    // 未检测
	SelectorHealthHealthy   SelectorHealthKind = "healthy"    // 上次检测正常
	SelectorHealthSuspicious SelectorHealthKind = "suspicious" // count=0 但非 Required
	SelectorHealthDegraded  SelectorHealthKind = "degraded"   // Required 选择器 count=0
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
	LastWarning string             `json:"last_warning,omitempty"`
}

// SelectorWatchdog 选择器健康看门狗
//
// 检测上游（小红书）DOM 变更导致的选择器失效，发出警告。
// 不阻断操作——只记录和报告，由调用方决定是否降级。
//
// 使用方式：
//  1. 服务启动时 RegisterAll() 注册核心选择器
//  2. 每次页面导航成功后，按页面上下文调用 ProbeForKind()
//  3. 通过 /health/selectors 端点查询状态
//
// 例：
//
//	watchdog := NewSelectorWatchdog()
//	watchdog.RegisterAll()
//	watchdog.ProbeForKind(page, XHSReadySearch) // 搜索页探测 search 相关选择器
type SelectorWatchdog struct {
	mu      sync.RWMutex
	entries map[string]*SelectorHealthEntry
	probing map[string]bool
}

// NewSelectorWatchdog 创建看门狗
func NewSelectorWatchdog() *SelectorWatchdog {
	return &SelectorWatchdog{
		entries: make(map[string]*SelectorHealthEntry),
		probing: make(map[string]bool),
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

// selectorsForKind 获取指定页面上下文中需要探测的选择器名称列表
func selectorsForKind(kind XHSReadyKind) []string {
	switch kind {
	case XHSReadySearch:
		return []string{"search_input", "search_result"}
	case XHSReadyDetail:
		return []string{"feed_detail_ready", "like_button", "comment_box"}
	case XHSReadyCommentBox:
		return []string{"comment_box"}
	case XHSReadyHome:
		// 首页没有注册的 spec，暂不探测
		return nil
	default:
		return nil
	}
}

// ProbeForKind 按页面上下文探测相关选择器。
// 只在选择器状态为 unknown(未检测) 或非 healthy(退化/可疑) 时重新探测，
// 已确认 healthy 的跳过，避免每次页面就绪都重复 probe。
func (w *SelectorWatchdog) ProbeForKind(page *hrod.Page, kind XHSReadyKind) (warnings []string) {
	names := selectorsForKind(kind)
	if len(names) == 0 {
		return nil
	}

	// 过滤出需要重新探测的选择器（unknown、非healthy、或距上次probe超过24h）。
	// 同一选择器已有探测进行中时跳过，避免多个就绪点或后台 goroutine 重复 eval。
	w.mu.Lock()
	if w.probing == nil {
		w.probing = make(map[string]bool)
	}
	probeNames := make([]string, 0, len(names))
	specs := make([]SelectorSpec, 0, len(names))
	for _, name := range names {
		if entry, ok := w.entries[name]; ok {
			if w.probing[name] {
				continue
			}
			if entry.Status != SelectorHealthHealthy || time.Since(entry.LastChecked) >= 24*time.Hour {
				w.probing[name] = true
				probeNames = append(probeNames, name)
				specs = append(specs, SelectorSpec{
					Name:        entry.Name,
					Selector:    entry.Selector,
					Required:    entry.Required,
					VisibleOnly: entry.Name == "search_input" || entry.Name == "comment_box" || entry.Name == "like_button",
				})
			}
		}
	}
	w.mu.Unlock()
	defer func() {
		if len(probeNames) == 0 {
			return
		}
		w.mu.Lock()
		for _, name := range probeNames {
			delete(w.probing, name)
		}
		w.mu.Unlock()
	}()

	if len(specs) == 0 {
		return nil // 全部已确认 healthy 且未过期，跳过
	}

	results, err := ProbeSelectors(page, specs)
	if err != nil {
		warn := fmt.Sprintf("探测选择器失败(kind=%s): %v", kind, err)
		logrus.Warn(warn)
		return []string{warn}
	}

	for _, r := range results {
		w.mu.Lock()
		entry := w.entries[r.Name]
		if entry == nil {
			w.mu.Unlock()
			continue
		}

		prevStatus := entry.Status
		name := entry.Name
		purpose := entry.Purpose
		entry.LastChecked = time.Now()
		entry.LastCount = r.Count
		entry.LastVisible = r.VisibleCount
		entry.Samples = r.Samples

		// 用 visible count 判断可见性要求的选择器，否则用原始 count
		checkCount := r.Count
		if entry.Name == "search_input" || entry.Name == "comment_box" || entry.Name == "like_button" {
			checkCount = r.VisibleCount
		}

		if checkCount > 0 {
			entry.Status = SelectorHealthHealthy
		} else if entry.Required {
			entry.Status = SelectorHealthDegraded
		} else {
			entry.Status = SelectorHealthSuspicious
		}
		currStatus := entry.Status
		warn := selectorHealthWarning(currStatus, name, purpose)
		entry.LastWarning = warn
		w.mu.Unlock()

		// 状态恶化时打 warn，Status() 仍保留最近一次 warning 供健康检查读取。
		shouldWarn := false
		if currStatus == SelectorHealthDegraded && prevStatus != SelectorHealthDegraded {
			shouldWarn = true
			logrus.Warn(warn)
		} else if currStatus == SelectorHealthSuspicious && prevStatus == SelectorHealthHealthy {
			shouldWarn = true
			logrus.Warn(warn)
		}
		if shouldWarn && warn != "" {
			warnings = append(warnings, warn)
		}
	}
	return warnings
}

func selectorHealthWarning(status SelectorHealthKind, name, purpose string) string {
	switch status {
	case SelectorHealthDegraded:
		return fmt.Sprintf("⚠️ 上游变更: 核心选择器 %q(%s) 命中数为 0, 功能可能不可用", name, purpose)
	case SelectorHealthSuspicious:
		return fmt.Sprintf("⚠️ 选择器 %q(%s) 命中数为 0(非必需), DOM 可能变化", name, purpose)
	default:
		return ""
	}
}

// Status 返回所有选择器当前健康状态快照
func (w *SelectorWatchdog) Status() []SelectorHealthEntry {
	w.mu.RLock()
	defer w.mu.RUnlock()
	result := make([]SelectorHealthEntry, 0, len(w.entries))
	for _, e := range w.entries {
		entry := *e
		if entry.Samples != nil {
			entry.Samples = append([]string{}, entry.Samples...) // 浅拷贝防止外部修改
		}
		result = append(result, entry)
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

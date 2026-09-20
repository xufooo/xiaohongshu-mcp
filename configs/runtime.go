package configs

import (
	"os"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/sirupsen/logrus"
)

// defaultLowResourceMemoryLimit 低资源档的 Go 堆软上限：128MiB。
// Chromium 才是 1GB 设备上的内存大头，Go 侧保持在 128MiB 以内可以避免
// Go 与 renderer 抢内存导致 OOM-kill；软上限只让 GC 更积极，不会让进程失败。
const defaultLowResourceMemoryLimit = 128 << 20

// ApplyRuntimeLimits 按环境变量收紧 Go 运行时内存行为。
//   - XHS_GO_MEMLIMIT：软上限，支持 "128MiB" / "134217728"；未设置时低资源档用 128MiB。
//   - XHS_GOGC：GC 触发比例（0 表示关闭自动 GC，需谨慎）。
func ApplyRuntimeLimits() {
	if raw := strings.TrimSpace(os.Getenv("XHS_GO_MEMLIMIT")); raw != "" {
		if bytes, err := parseByteSize(raw); err == nil {
			if bytes <= 0 {
				// 显式 0 表示"不设软上限"，不是非法值。
				logrus.Info("GOMEMLIMIT disabled by XHS_GO_MEMLIMIT=0")
			} else {
				debug.SetMemoryLimit(bytes)
				logrus.Infof("GOMEMLIMIT set to %d bytes", bytes)
			}
		} else {
			logrus.Warnf("invalid XHS_GO_MEMLIMIT %q, ignored", raw)
		}
	} else if LowResourceProfile() {
		debug.SetMemoryLimit(defaultLowResourceMemoryLimit)
		logrus.Infof("GOMEMLIMIT set to low-resource default %d bytes", int64(defaultLowResourceMemoryLimit))
	}

	if raw := strings.TrimSpace(os.Getenv("XHS_GOGC")); raw != "" {
		if percent, err := strconv.Atoi(raw); err == nil && percent >= 0 {
			debug.SetGCPercent(percent)
			logrus.Infof("GOGC set to %d", percent)
		} else {
			logrus.Warnf("invalid XHS_GOGC %q, ignored", raw)
		}
	}
}

// parseByteSize 解析 "128MiB" / "128MB" / "134217728" 形式的大小；无后缀按字节处理。
func parseByteSize(raw string) (int64, error) {
	value := strings.TrimSpace(raw)
	upper := strings.ToUpper(value)
	multiplier := int64(1)
	for _, suffix := range []struct {
		name   string
		factor int64
	}{
		{"GIB", 1 << 30},
		{"MIB", 1 << 20},
		{"KIB", 1 << 10},
		{"GB", 1 << 30},
		{"MB", 1 << 20},
		{"KB", 1 << 10},
		{"B", 1},
	} {
		if strings.HasSuffix(upper, suffix.name) {
			multiplier = suffix.factor
			value = strings.TrimSpace(value[:len(value)-len(suffix.name)])
			break
		}
	}
	number, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, err
	}
	return number * multiplier, nil
}

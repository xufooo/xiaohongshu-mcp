package configs

import (
	"crypto/rand"
	"encoding/binary"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/cookies"
)

// ResolveFingerprintSeed 解析 CloakBrowser fingerprint 的持久 seed，保证跨进程画像稳定。
// 优先级：XHS_FP_SEED 环境变量 → cookies 会话文件中的 seed → crypto/rand 生成并保存。
// 非零且可靠的 seed 才返回；环境变量非法时忽略并继续往下走。
func ResolveFingerprintSeed(cookier cookies.Cookier) int {
	if raw := strings.TrimSpace(os.Getenv("XHS_FP_SEED")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return n
		}
		logrus.Warn("XHS_FP_SEED 非法，已忽略")
	}
	if seed := cookier.LoadSeed(); seed > 0 {
		return seed
	}
	seed := newSeed()
	if err := cookier.SaveSeed(seed); err != nil {
		logrus.Warnf("保存 fingerprint seed 失败: %v", err)
	}
	return seed
}

// newSeed 用 crypto/rand 生成一个正整数的持久 seed。
// crypto/rand 失败时退回时间戳，保证始终返回非零正整数。
func newSeed() int {
	var b [4]byte
	if _, err := rand.Read(b[:]); err == nil {
		if n := int(binary.BigEndian.Uint32(b[:]) & 0x7fffffff); n != 0 {
			return n
		}
	}
	n := int(time.Now().UnixNano() & 0x7fffffff)
	if n == 0 {
		return 1
	}
	return n
}

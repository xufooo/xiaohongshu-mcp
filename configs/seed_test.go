package configs

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/cookies"
)

func testCookier(t *testing.T) cookies.Cookier {
	t.Helper()
	return cookies.NewLoadCookie(filepath.Join(t.TempDir(), "cookies.json"))
}

func TestNewSeedPositive(t *testing.T) {
	for i := 0; i < 3; i++ {
		if seed := newSeed(); seed <= 0 {
			t.Fatalf("newSeed() = %d, 应为正整数", seed)
		}
	}
}

func TestResolveFingerprintSeedEnv(t *testing.T) {
	t.Setenv("XHS_FP_SEED", "424242")
	c := testCookier(t)
	if seed := ResolveFingerprintSeed(c); seed != 424242 {
		t.Fatalf("XHS_FP_SEED 应优先: got %d, want 424242", seed)
	}
}

func TestResolveFingerprintSeedEnvInvalid(t *testing.T) {
	t.Setenv("XHS_FP_SEED", "not-a-number")
	c := testCookier(t)
	_ = c.SaveSeed(777)
	if seed := ResolveFingerprintSeed(c); seed != 777 {
		t.Fatalf("非法 XHS_FP_SEED 应回退到 cookie seed: got %d, want 777", seed)
	}
}

func TestResolveFingerprintSeedFromCookie(t *testing.T) {
	t.Setenv("XHS_FP_SEED", "")
	c := testCookier(t)
	_ = c.SaveSeed(888)
	if seed := ResolveFingerprintSeed(c); seed != 888 {
		t.Fatalf("应从 cookie 文件读 seed: got %d, want 888", seed)
	}
}

func TestResolveFingerprintSeedGeneratesAndSaves(t *testing.T) {
	t.Setenv("XHS_FP_SEED", "")
	c := testCookier(t)
	seed := ResolveFingerprintSeed(c)
	if seed <= 0 {
		t.Fatalf("应生成正整数 seed, got %d", seed)
	}
	if saved := c.LoadSeed(); saved != seed {
		t.Fatalf("新 seed 应保存回 cookie 文件: saved %d, want %d", saved, seed)
	}
	// 再次解析应复用已保存的 seed，不重新生成
	if again := ResolveFingerprintSeed(c); again != seed {
		t.Fatalf("重启后 seed 应保持不变: got %d, want %d", again, seed)
	}
}
// TestResolveFingerprintSeedInvalidEnvNoLeak 非法 XHS_FP_SEED 不输出原值到日志。
func TestResolveFingerprintSeedInvalidEnvNoLeak(t *testing.T) {
	raw := "secret-invalid-seed-value"
	t.Setenv("XHS_FP_SEED", raw)
	c := testCookier(t)

	var buf bytes.Buffer
	oldOut := logrus.StandardLogger().Out
	logrus.SetOutput(&buf)
	defer logrus.SetOutput(oldOut)

	_ = ResolveFingerprintSeed(c)

	if strings.Contains(buf.String(), raw) {
		t.Fatalf("日志泄露 XHS_FP_SEED 原值: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "已忽略") {
		t.Fatalf("应记录忽略提示: %q", buf.String())
	}
}

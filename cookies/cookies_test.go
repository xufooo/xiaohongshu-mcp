package cookies

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveSeedCreatesFileWith0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookies.json")
	c := NewLoadCookie(path)
	if err := c.SaveSeed(42); err != nil {
		t.Fatalf("SaveSeed 失败: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("文件不存在: %v", err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Fatalf("权限应为 0600，实际 %o", fi.Mode().Perm())
	}
	if got := c.LoadSeed(); got != 42 {
		t.Fatalf("seed 读取不符: %d", got)
	}
}

func TestSaveCookiesTightensPermission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookies.json")
	if err := os.WriteFile(path, []byte("[]"), 0644); err != nil {
		t.Fatalf("预建文件失败: %v", err)
	}
	c := NewLoadCookie(path)
	if err := c.SaveCookies([]byte(`[{"name":"a","value":"b"}]`)); err != nil {
		t.Fatalf("SaveCookies 失败: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("文件不存在: %v", err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Fatalf("权限应收紧为 0600，实际 %o", fi.Mode().Perm())
	}
}

func TestSeedAndCookiesMutuallyPreserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookies.json")
	c := NewLoadCookie(path)
	if err := c.SaveSeed(7); err != nil {
		t.Fatalf("SaveSeed 失败: %v", err)
	}
	cks := []byte(`[{"name":"session","value":"xyz"}]`)
	if err := c.SaveCookies(cks); err != nil {
		t.Fatalf("SaveCookies 失败: %v", err)
	}
	// seed 保留
	if got := c.LoadSeed(); got != 7 {
		t.Fatalf("SaveCookies 后 seed 丢失: %d", got)
	}
	// cookies 保留
	loaded, err := c.LoadCookies()
	if err != nil {
		t.Fatalf("LoadCookies 失败: %v", err)
	}
	if string(loaded) != string(cks) {
		t.Fatalf("cookies 内容变化: %s", loaded)
	}
	// 再写 seed，cookies 仍保留
	if err := c.SaveSeed(9); err != nil {
		t.Fatalf("第二次 SaveSeed 失败: %v", err)
	}
	if got := c.LoadSeed(); got != 9 {
		t.Fatalf("seed 未更新: %d", got)
	}
	loaded2, err := c.LoadCookies()
	if err != nil {
		t.Fatalf("LoadCookies 失败: %v", err)
	}
	if string(loaded2) != string(cks) {
		t.Fatalf("SaveSeed 后 cookies 丢失: %s", loaded2)
	}
}

func TestV1BareArrayStillReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookies.json")
	// v1 裸 cookie 数组格式
	raw := `[{"name":"a","value":"1"},{"name":"b","value":"2"}]`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatalf("写 v1 文件失败: %v", err)
	}
	c := NewLoadCookie(path)
	loaded, err := c.LoadCookies()
	if err != nil {
		t.Fatalf("v1 裸数组读取失败: %v", err)
	}
	if string(loaded) != raw {
		t.Fatalf("v1 内容不符: %s", loaded)
	}
	if got := c.LoadSeed(); got != 0 {
		t.Fatalf("v1 无 seed，应返回 0，实际 %d", got)
	}
}

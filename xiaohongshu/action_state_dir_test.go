package xiaohongshu

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEnsureWritableStateDirKeepsWritable 可写目录原样返回。
func TestEnsureWritableStateDirKeepsWritable(t *testing.T) {
	want := filepath.Join(t.TempDir(), "state")
	got, err := ensureWritableStateDir(want)
	if err != nil {
		t.Fatalf("可写目录不应报错: %v", err)
	}
	if got != want {
		t.Fatalf("应沿用调用方目录，got %s want %s", got, want)
	}
	if !dirWritable(got) {
		t.Fatalf("返回的目录应可写: %s", got)
	}
}

// TestEnsureWritableStateDirFallsBackWhenMkdirFails 父目录不可写（MkdirAll 失败）时回退，不得让服务起不来。
func TestEnsureWritableStateDirFallsBackWhenMkdirFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 忽略目录权限，跳过")
	}
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.Mkdir(blocked, 0o500); err != nil {
		t.Fatalf("准备不可写目录失败: %v", err)
	}

	got, err := ensureWritableStateDir(filepath.Join(blocked, "sub"))
	if err != nil {
		t.Fatalf("应回退到临时目录而不是报错: %v", err)
	}
	if got == filepath.Join(blocked, "sub") {
		t.Fatal("不可写目录不应被采用")
	}
	if !dirWritable(got) {
		t.Fatalf("回退目录应可写: %s", got)
	}
}

// TestEnsureWritableStateDirFallsBackWhenDirNotWritable 目录存在但不可写时同样回退（MkdirAll 成功不代表可写）。
func TestEnsureWritableStateDirFallsBackWhenDirNotWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 忽略目录权限，跳过")
	}
	blocked := filepath.Join(t.TempDir(), "readonly")
	if err := os.Mkdir(blocked, 0o500); err != nil {
		t.Fatalf("准备只读目录失败: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })

	got, err := ensureWritableStateDir(blocked)
	if err != nil {
		t.Fatalf("只读目录应回退而不是报错: %v", err)
	}
	if got == blocked {
		t.Fatal("只读目录不应被采用")
	}
}

// TestDefaultActionStateRootEnv 显式配置优先。
func TestDefaultActionStateRootEnv(t *testing.T) {
	t.Setenv("XHS_ACTION_STATE_STORE", "/tmp/custom-action-state")
	if got := defaultActionStateRoot(); got != "/tmp/custom-action-state" {
		t.Fatalf("应使用显式配置，got %s", got)
	}
}

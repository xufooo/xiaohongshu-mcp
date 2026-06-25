package configs

import "testing"

func TestUseCloakBrowser(t *testing.T) {
	originalMode := browserMode
	t.Cleanup(func() {
		SetBrowserMode(originalMode)
	})

	SetBrowserMode("chrome")
	if UseCloakBrowser() {
		t.Fatal("chrome mode should disable CloakBrowser mode")
	}

	SetBrowserMode("cloak")
	if !UseCloakBrowser() {
		t.Fatal("cloak mode should enable CloakBrowser mode")
	}
}

func TestCloakLauncherProfile(t *testing.T) {
	originalMode := browserMode
	t.Cleanup(func() {
		SetBrowserMode(originalMode)
	})

	SetBrowserMode("chrome")
	if CloakLauncherProfile() {
		t.Fatal("chrome mode should disable CloakBrowser launcher profile")
	}

	SetBrowserMode("cloak")
	if !CloakLauncherProfile() {
		t.Fatal("cloak mode should enable CloakBrowser launcher profile")
	}
}

func TestBrowserExtraArgsFromEnv(t *testing.T) {
	t.Setenv("XHS_BROWSER_LANG", "zh-CN")
	t.Setenv("XHS_BROWSER_TIMEZONE", "Asia/Shanghai")
	t.Setenv("CLOAK_FLAGS", "--disable-gpu")
	t.Setenv("XHS_BROWSER_EXTRA_ARGS", "--lang=en-US --disable-extensions")

	args := BrowserExtraArgsFromEnv()
	want := []string{
		"--lang=zh-CN",
		"--timezone=Asia/Shanghai",
		"--disable-gpu",
		"--lang=en-US",
		"--disable-extensions",
	}
	if len(args) != len(want) {
		t.Fatalf("argument count = %d, want %d", len(args), len(want))
	}
	for index := range want {
		if args[index] != want[index] {
			t.Fatalf("args[%d] = %q, want %q", index, args[index], want[index])
		}
	}
}

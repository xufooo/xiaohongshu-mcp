package xiaohongshu

import (
	"errors"
	"fmt"
	"testing"
)

// 致命错误（风控信号 / 渲染器已死）必须立刻结束等待，不能被当成"再等等"。
func TestFatalWaitErrorClassification(t *testing.T) {
	if !isFatalWaitError(fatalWait(errors.New("风控"))) {
		t.Fatal("fatalWait 标记的错误应致命")
	}
	wrapped := fmt.Errorf("外层包装: %w", fatalWait(errors.New("风控")))
	if !isFatalWaitError(wrapped) {
		t.Fatal("被包装的致命错误也应致命")
	}
	if isFatalWaitError(errors.New("普通探测失败")) {
		t.Fatal("普通错误不应致命")
	}
	if isFatalWaitError(nil) {
		t.Fatal("nil 不应致命")
	}
	if !isFatalWaitError(fmt.Errorf("renderer 已死: %w", ErrFatalRendererError)) {
		t.Fatal("渲染器致命错误应致命")
	}
}

// nil 页面必须立刻报错，而不是空转或 panic。
func TestWaitForConditionRejectsNilPage(t *testing.T) {
	err := waitForCondition(waitRound{Kind: "search_results", Probe: func() (string, bool, error) {
		t.Fatal("不应探测")
		return "", false, nil
	}})
	if err == nil {
		t.Fatal("nil 页面应报错")
	}
}

// 默认节奏单一来源：HomeSearch 低频（冷启动 CPU 压力），其余 300/500ms。
func TestWaitPollRangeSingleSource(t *testing.T) {
	min, max := waitPollRange("ready:" + string(XHSReadyHomeSearch))
	if min != homeSearchPollMin || max != homeSearchPollMax {
		t.Fatalf("HomeSearch 节奏 = %v/%v", min, max)
	}
	for _, kind := range []string{"ready:detail", "search_results", "publish_success"} {
		min, max = waitPollRange(kind)
		if min != defaultReadyPollMin || max != defaultReadyPollMax {
			t.Fatalf("%s 节奏 = %v/%v", kind, min, max)
		}
	}
}

// 发布成功的判据必须准确：只有"离开发布表单页"才算成功，URL 为空不算。
func TestPublishLeftForm(t *testing.T) {
	cases := map[string]bool{
		"https://creator.xiaohongshu.com/publish/publish?source=official": false,
		"https://creator.xiaohongshu.com/publish/publish":                 false,
		"https://creator.xiaohongshu.com/publish/success":                 true,
		"https://www.xiaohongshu.com/explore/abc123":                      true,
		"": false,
	}
	for rawURL, want := range cases {
		if got := publishLeftForm(rawURL); got != want {
			t.Fatalf("publishLeftForm(%q) = %v, 期望 %v", rawURL, got, want)
		}
	}
}

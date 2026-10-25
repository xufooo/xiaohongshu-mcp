package xiaohongshu

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
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

// Departure is an observation only; even a plausible success URL is unconfirmed.
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

func TestPublishDepartureRemainsUnconfirmed(t *testing.T) {
	for _, path := range []string{"publish/success", "login", "error", "explore/abc"} {
		t.Run(path, func(t *testing.T) {
			response := []byte(fmt.Sprintf(`{"targetInfo":{"targetId":"target-1","url":"https://creator.xiaohongshu.com/%s"}}`, path))
			session := newCurrentPageURLSession(t, &currentPageURLCDPClient{response: response})
			var unconfirmed *PublishUnconfirmedError
			if err := waitPublishSuccess(session.page); !errors.As(err, &unconfirmed) {
				t.Fatalf("departure must return a typed unconfirmed outcome: %v", err)
			}
		})
	}
}

func TestSafePublishURLRedactsSensitiveParts(t *testing.T) {
	cases := map[string]string{
		"https://creator.xiaohongshu.com/explore/abc?token=secret#top": "https://creator.xiaohongshu.com/explore/abc",
		"https://user:pass@creator.xiaohongshu.com/publish/success":       "https://creator.xiaohongshu.com/publish/success",
		"not a url":                                                     "[redacted]",
	}
	for rawURL, want := range cases {
		if got := safePublishURL(rawURL); got != want {
			t.Fatalf("safePublishURL(%q) = %q, 期望 %q", rawURL, got, want)
		}
	}
}

func TestReadyProgressIgnoresUnrelatedPageDecoration(t *testing.T) {
	base := xhsReadyProbe{URL: ExploreURL, AppCount: 1, ReadyState: "interactive"}
	decorated := base
	decorated.Title = "animated title"
	decorated.ScrollY = 200
	decorated.StateFragment = "unrelated state"
	if readyProgressFingerprint(base) != readyProgressFingerprint(decorated) {
		t.Fatal("decoration reset business progress")
	}
	decorated.HomeFeedCount = 1
	if readyProgressFingerprint(base) == readyProgressFingerprint(decorated) {
		t.Fatal("feed loading did not advance business progress")
	}
}

func TestWaitConditionReturnsFatalWithoutWaitingForPageSignal(t *testing.T) {
	session := newCurrentPageURLSession(t, &currentPageURLCDPClient{})
	failure := fmt.Errorf("renderer: %w", ErrFatalRendererError)
	calls := 0
	err := waitForCondition(waitRound{Page: session.page, Probe: func() (string, bool, error) {
		calls++
		return "", false, failure
	}})
	if err != failure || calls != 1 {
		t.Fatalf("fatal probe continued waiting: calls=%d err=%v", calls, err)
	}
}

func TestWaitUnchangedStateCanCompleteAfterFormerStallBudget(t *testing.T) {
	client := &currentPageURLCDPClient{response: runtimeEvaluateStringResponse(t, `{"reason":"max","mutations":0}`)}
	session := newCurrentPageURLSession(t, client)
	clock := time.Unix(0, 0)
	calls := 0
	err := waitForConditionWithClock(waitRound{
		Page: session.page, Ceiling: 3 * time.Minute,
		Probe: func() (string, bool, error) {
			calls++
			clock = clock.Add(70 * time.Second)
			return "unchanged while request runs", calls == 2, nil
		},
	}, func() time.Time { return clock })
	if err != nil || calls != 2 {
		t.Fatalf("quiet page rejected before target completion: calls=%d err=%v", calls, err)
	}
}

func TestWaitSafetyCeilingAndCallerCancellationStillBoundQuietPage(t *testing.T) {
	for _, cancelProbe := range []bool{false, true} {
		client := &currentPageURLCDPClient{}
		session := newCurrentPageURLSession(t, client)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		page := session.page.Context(ctx)
		clock := time.Unix(0, 0)
		ceilingErr := errors.New("safety ceiling, outcome unknown")
		want := ceilingErr
		if cancelProbe {
			want = context.Canceled
		}
		err := waitForConditionWithClock(waitRound{
			Page: page, Ceiling: time.Minute,
			Probe: func() (string, bool, error) {
				clock = clock.Add(2 * time.Minute)
				if cancelProbe {
					cancel()
				}
				return "unchanged", cancelProbe, nil
			},
			OnExhausted: func() error { return ceilingErr },
		}, func() time.Time { return clock })
		if !errors.Is(err, want) || client.runtimeCall != 0 {
			t.Fatalf("bound lost or another probe issued: err=%v runtime=%d", err, client.runtimeCall)
		}
	}
}

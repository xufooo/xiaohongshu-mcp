package xiaohongshu

import (
	"testing"
	"time"

	"github.com/go-rod/rod/lib/proto"
)

// 只有会改变页面内容的请求才算「页面还在动」：图片/脚本不算，
// 否则 Pi 上几百个图片请求会让"卡死判定"永远不生效。
func TestTracksRequestType(t *testing.T) {
	for _, kind := range []proto.NetworkResourceType{
		proto.NetworkResourceTypeDocument,
		proto.NetworkResourceTypeXHR,
		proto.NetworkResourceTypeFetch,
	} {
		if !tracksRequestType(kind) {
			t.Fatalf("%s 应计入数据请求", kind)
		}
	}
	for _, kind := range []proto.NetworkResourceType{
		proto.NetworkResourceTypeImage,
		proto.NetworkResourceTypeScript,
		proto.NetworkResourceTypeStylesheet,
		proto.NetworkResourceTypeWebSocket,
	} {
		if tracksRequestType(kind) {
			t.Fatalf("%s 不应计入数据请求", kind)
		}
	}
}

// 事件配对：requestWillBeSent → loadingFinished，重复 id 不重复计数，未知 id 无副作用。
func TestNetworkActivityCountsInFlight(t *testing.T) {
	n := newNetworkActivity()
	if n.inFlight() != 0 {
		t.Fatal("初始应为 0")
	}
	start := time.Now()
	n.started("r1", start)
	n.started("r2", start)
	n.started("r1", start) // 重复事件不应重复计数
	if got := n.inFlight(); got != 2 {
		t.Fatalf("在途 = %d, 期望 2", got)
	}

	n.finished("r1", start.Add(400*time.Millisecond))
	if got := n.inFlight(); got != 1 {
		t.Fatalf("落地一个后在途 = %d, 期望 1", got)
	}
	// loadingFailed 与 loadingFinished 都走 finished：未知 id 必须无副作用。
	n.finished("unknown", start)
	if got := n.inFlight(); got != 1 {
		t.Fatalf("未知 id 影响了在途数: %d", got)
	}
}

// 卡死判定用的是"最近有没有网络动静"，所以每个数据请求事件都要刷新时间戳；
// 非数据请求（图片等）不算动静。
func TestNetworkActivityLastEventTracksDataRequests(t *testing.T) {
	n := newNetworkActivity()
	if !n.lastEvent().IsZero() {
		t.Fatal("还没事件时 lastEvent 应为零值")
	}

	started := time.Now()
	n.started("r1", started)
	if got := n.lastEvent(); !got.Equal(started) {
		t.Fatalf("lastEvent = %v, 期望 %v", got, started)
	}

	finished := started.Add(time.Second)
	n.finished("r1", finished)
	if got := n.lastEvent(); !got.Equal(finished) {
		t.Fatalf("落地事件也应刷新 lastEvent: %v", got)
	}

	// nil 观察器（页面没挂上观察）必须是安全的零值。
	var none *networkActivity
	if !none.lastEvent().IsZero() || none.inFlight() != 0 {
		t.Fatal("nil 观察器应为零值且安全")
	}
}

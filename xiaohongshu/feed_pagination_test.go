package xiaohongshu

import (
	"context"
	"testing"
)

func TestFullFeedBatchOnlyProbesEndWhenNeeded(t *testing.T) {
	for _, tc := range []struct {
		name       string
		feeds      []Feed
		atEnd      bool
		wantMore   bool
		wantProbes int
	}{
		{"local remaining", []Feed{{ID: "a"}, {ID: "b"}}, true, true, 0},
		{"terminal", []Feed{{ID: "a"}}, true, false, 1},
		{"continuable", []Feed{{ID: "a"}}, false, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probes := 0
			cursor := &FeedCursor{}
			feeds, more, err := loadFeedBatchWithOps(context.Background(), cursor, 1, feedPageOps{
				collect: func() ([]Feed, error) { return tc.feeds, nil },
				atEnd: func() bool {
					probes++
					return tc.atEnd
				},
			})
			if err != nil || len(feeds) != 1 || feeds[0].ID != "a" || more != tc.wantMore || probes != tc.wantProbes {
				t.Fatalf("batch=%v more=%t probes=%d err=%v", feeds, more, probes, err)
			}
			if len(cursor.ReturnedIDs) != 1 || cursor.ReturnedIDs[0] != "a" {
				t.Fatalf("cursor changed: %+v", cursor)
			}
		})
	}
}

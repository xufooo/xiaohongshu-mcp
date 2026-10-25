package xiaohongshu

import (
	"context"
	"errors"
	"fmt"
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
				atEnd: func() (bool, error) {
					probes++
					return tc.atEnd, nil
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

func TestPartialFeedBatchPreservesTerminalErrors(t *testing.T) {
	for _, stage := range []string{"collect", "scroll", "growth"} {
		for _, failure := range []error{context.Canceled, context.DeadlineExceeded, fmt.Errorf("wrapped: %w", ErrFatalRendererError), errors.New("ordinary")} {
			t.Run(stage+"/"+failure.Error(), func(t *testing.T) {
				calls := 0
				ops := feedPageOps{
					collect: func() ([]Feed, error) {
						calls++
						if stage == "collect" && calls > 1 {
							return nil, failure
						}
						return []Feed{{ID: "a"}}, nil
					},
					scroll: func() error {
						if stage == "scroll" {
							return failure
						}
						return nil
					},
					waitForGrowth: func(context.Context, map[string]bool) (bool, bool, error) {
						if stage == "growth" {
							return false, false, failure
						}
						return true, false, nil
					},
				}
				feeds, more, err := loadFeedBatchWithOps(context.Background(), &FeedCursor{}, 2, ops)
				terminal := isFatalWaitError(failure) || errors.Is(failure, context.Canceled) || errors.Is(failure, context.DeadlineExceeded)
				if len(feeds) != 1 || !more || (terminal && !errors.Is(err, failure)) || (!terminal && err != nil) {
					t.Fatalf("partial result semantics: feeds=%v more=%t err=%v", feeds, more, err)
				}
			})
		}
	}
}

func TestFullFeedBatchRejectsCancellationDuringCollect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, _, err := loadFeedBatchWithOps(ctx, &FeedCursor{}, 1, feedPageOps{
		collect: func() ([]Feed, error) {
			cancel()
			return []Feed{{ID: "a"}}, nil
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("full batch masked cancellation: %v", err)
	}
}

func TestFeedEndProbeDoesNotSwallowErrors(t *testing.T) {
	for _, failure := range []error{context.Canceled, context.DeadlineExceeded, ErrFatalRendererError} {
		_, more, err := loadFeedBatchWithOps(context.Background(), &FeedCursor{}, 1, feedPageOps{
			collect: func() ([]Feed, error) { return []Feed{{ID: "a"}}, nil },
			atEnd:   func() (bool, error) { return false, failure },
		})
		if !errors.Is(err, failure) || !more {
			t.Fatalf("end-probe failure became successful pagination: more=%t err=%v", more, err)
		}
	}
}

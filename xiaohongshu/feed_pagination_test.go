package xiaohongshu

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTakeNewFeedsDeduplicatesAcrossCursor(t *testing.T) {
	cursor := &FeedCursor{ReturnedIDs: []string{"a"}}
	feeds := []Feed{{ID: "a"}, {ID: "b"}, {ID: "b"}, {ID: "c"}}
	batch := takeNewFeeds(feeds, cursor, 2)
	require.Equal(t, []string{"b", "c"}, []string{batch[0].ID, batch[1].ID})
	require.Equal(t, []string{"a", "b", "c"}, cursor.ReturnedIDs)
}

func TestFeedCursorMatchesQuery(t *testing.T) {
	cursor := &FeedCursor{Kind: FeedPageSearch, Keyword: "go", FilterKey: `[{"sort_by":"最新"}]`}
	require.NoError(t, cursor.Validate(FeedPageSearch, "go", `[{"sort_by":"最新"}]`))
	require.Error(t, cursor.Validate(FeedPageHome, "", ""))
	require.Error(t, cursor.Validate(FeedPageSearch, "rust", `[{"sort_by":"最新"}]`))
}

func TestLoadFeedBatchWithOpsCollectErrorOnFirstCall(t *testing.T) {
	expectedErr := errors.New("collect failed")
	_, _, err := loadFeedBatchWithOps(context.Background(), &FeedCursor{}, 10, feedPageOps{
		collect: func() ([]Feed, error) { return nil, expectedErr },
	})
	require.ErrorIs(t, err, expectedErr)
}

func TestLoadFeedBatchWithOpsReturnsBatchWhenMaxItemsReached(t *testing.T) {
	feeds, _, err := loadFeedBatchWithOps(context.Background(), &FeedCursor{}, 2, feedPageOps{
		collect: func() ([]Feed, error) { return []Feed{{ID: "a"}, {ID: "b"}}, nil },
	})
	require.NoError(t, err)
	require.Len(t, feeds, 2)
}

func TestLoadFeedBatchWithOpsScrollingGrowth(t *testing.T) {
	callCount := 0
	feeds, hasMore, err := loadFeedBatchWithOps(context.Background(), &FeedCursor{}, 3, feedPageOps{
		collect: func() ([]Feed, error) {
			callCount++
			if callCount == 1 {
				return []Feed{{ID: "a"}}, nil
			}
			return []Feed{{ID: "a"}, {ID: "b"}, {ID: "c"}}, nil
		},
		scroll: func() error { return nil },
		waitForGrowth: func(ctx context.Context, before map[string]bool) (bool, bool, error) {
			return true, false, nil
		},
	})
	require.NoError(t, err)
	require.Len(t, feeds, 3)
	require.True(t, hasMore)
}

func TestLoadFeedBatchWithOpsAtEndReturnsHasMoreFalse(t *testing.T) {
	feeds, hasMore, err := loadFeedBatchWithOps(context.Background(), &FeedCursor{}, 10, feedPageOps{
		collect: func() ([]Feed, error) { return []Feed{{ID: "a"}}, nil },
		scroll:  func() error { return nil },
		waitForGrowth: func(ctx context.Context, before map[string]bool) (bool, bool, error) {
			return false, true, nil
		},
	})
	require.NoError(t, err)
	require.Len(t, feeds, 1)
	require.False(t, hasMore)
}

func TestLoadFeedBatchWithOpsContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := loadFeedBatchWithOps(ctx, &FeedCursor{}, 10, feedPageOps{
		collect: func() ([]Feed, error) { return []Feed{{ID: "a"}}, nil },
		scroll:  func() error { return nil },
		waitForGrowth: func(ctx context.Context, before map[string]bool) (bool, bool, error) {
			<-ctx.Done()
			return false, false, ctx.Err()
		},
	})
	require.ErrorIs(t, err, context.Canceled)
}

func TestFeedCursorNilReturnsNoError(t *testing.T) {
	var cursor *FeedCursor
	require.NoError(t, cursor.Validate(FeedPageHome, "", ""))
}

func TestFeedKeyUsesIDWhenPresent(t *testing.T) {
	require.Equal(t, "feed-1", feedKey(Feed{ID: "feed-1"}))
}

func TestFeedKeyFallsBackToCardComposite(t *testing.T) {
	key := feedKey(Feed{NoteCard: NoteCard{DisplayTitle: "title", User: User{UserID: "u1", Nickname: "n1"}}})
	require.Equal(t, "card:title|u1|n1", key)
}

func TestTakeNewFeedsSkipsEmptyKey(t *testing.T) {
	cursor := &FeedCursor{}
	feeds := []Feed{
		{NoteCard: NoteCard{DisplayTitle: "", User: User{UserID: ""}}},
		{ID: "a"},
	}
	batch := takeNewFeeds(feeds, cursor, 10)
	require.Len(t, batch, 1)
	require.Equal(t, "a", batch[0].ID)
}

func TestLoadFeedBatchWithOpsNoGrowthReturnsPartialBatch(t *testing.T) {
	feeds, hasMore, err := loadFeedBatchWithOps(context.Background(), &FeedCursor{}, 10, feedPageOps{
		collect: func() ([]Feed, error) { return []Feed{{ID: "a"}}, nil },
		scroll:  func() error { return nil },
		waitForGrowth: func(ctx context.Context, before map[string]bool) (bool, bool, error) {
			return false, false, nil
		},
	})
	require.NoError(t, err)
	require.Len(t, feeds, 1)
	require.False(t, hasMore)
}

func TestLoadFeedBatchWithOpsScrollErrorWithPartialResult(t *testing.T) {
	callCount := 0
	feeds, hasMore, err := loadFeedBatchWithOps(context.Background(), &FeedCursor{}, 10, feedPageOps{
		collect: func() ([]Feed, error) {
			callCount++
			return []Feed{{ID: "a"}}, nil
		},
		scroll: func() error {
			if callCount >= 1 {
				return errors.New("scroll failed")
			}
			return nil
		},
	})
	require.NoError(t, err)
	require.Len(t, feeds, 1)
	require.True(t, hasMore)
}

func TestFeedCursorCreatedAtSetWithinReasonableBounds(t *testing.T) {
	before := time.Now()
	cursor := &FeedCursor{Kind: FeedPageHome, CreatedAt: before}
	after := time.Now()
	require.False(t, cursor.CreatedAt.Before(before.Add(-time.Second)))
	require.False(t, cursor.CreatedAt.After(after.Add(time.Second)))
}

func TestTakeNewFeedsRespectsMaxItemsLimit(t *testing.T) {
	cursor := &FeedCursor{}
	feeds := []Feed{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}}
	batch := takeNewFeeds(feeds, cursor, 2)
	require.Len(t, batch, 2)
	require.Equal(t, "a", batch[0].ID)
	require.Equal(t, "b", batch[1].ID)
}

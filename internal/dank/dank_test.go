package dank

import (
	"context"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// cancelAfter cancels once Err has been called remain times. The entry check
// uses one call, so a budget above that only trips inside the walk.
type cancelAfter struct {
	context.Context
	cancel context.CancelFunc
	remain atomic.Int32
}

func (c *cancelAfter) Err() error {
	if c.remain.Add(-1) == 0 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestGenerateAtFixedLength(t *testing.T) {
	encoder := NewDankEncoder("(a|b)(1|2)")
	results := encoder.GenerateAtFixedLength(2)

	expected := []string{"a1", "a2", "b1", "b2"}
	require.Equal(t, expected, results)
}

func TestGenerateAtFixedLength_NegativeLength(t *testing.T) {
	encoder := NewDankEncoder("(a|b)(1|2)")
	results := encoder.GenerateAtFixedLength(-1)
	require.Empty(t, results)
}

func TestGenerateAtFixedLengthWithLimit(t *testing.T) {
	// (a|b|c|d)[0-9] produces 40 strings of length 2
	encoder := NewDankEncoder("(a|b|c|d)[0-9]")

	// Test limit = 5
	limited5 := encoder.GenerateAtFixedLengthWithLimit(2, 5)
	require.Len(t, limited5, 5)
	require.Equal(t, []string{"a0", "a1", "a2", "a3", "a4"}, limited5)

	// Test limit = 10
	limited10 := encoder.GenerateAtFixedLengthWithLimit(2, 10)
	require.Len(t, limited10, 10)

	// Test limit larger than possible (e.g. 100)
	limited100 := encoder.GenerateAtFixedLengthWithLimit(2, 100)
	require.Len(t, limited100, 40)

	// Test limit = 0 (unlimited)
	unlimited := encoder.GenerateAtFixedLengthWithLimit(2, 0)
	require.Len(t, unlimited, 40)

	// Test limit < 0 (unlimited)
	negLimit := encoder.GenerateAtFixedLengthWithLimit(2, -5)
	require.Len(t, negLimit, 40)
}

func TestGenerateAtFixedLengthWithContext_Cancelled(t *testing.T) {
	encoder := NewDankEncoder("(a|b|c|d)[0-9]")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	results, err := encoder.GenerateAtFixedLengthWithContext(ctx, 2)
	require.Error(t, err)
	require.Equal(t, context.Canceled, err)
	require.Empty(t, results)
}

func TestGenerateAtFixedLengthWithContext_Timeout(t *testing.T) {
	// Create a pattern with large branching factor
	encoder := NewDankEncoder("[a-z][a-z][a-z][a-z][a-z]")

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	time.Sleep(1 * time.Millisecond) // Ensure timeout is expired

	_, err := encoder.GenerateAtFixedLengthWithContext(ctx, 5)
	require.Error(t, err)
	require.Equal(t, context.DeadlineExceeded, err)
}

func TestGenerateAtFixedLengthWithContext_CancelsDuringWalk(t *testing.T) {
	encoder := NewDankEncoder("[a-z][a-z][a-z][a-z][a-z]")
	parent, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ctx := &cancelAfter{Context: parent, cancel: cancel}
	ctx.remain.Store(4)

	done := make(chan error, 1)
	go func() {
		_, err := encoder.GenerateAtFixedLengthWithContext(ctx, 5)
		done <- err
	}()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(3 * time.Second):
		t.Fatal("walk did not observe cancellation")
	}
}

func TestGenerateAtFixedLengthWithContextAndLimit(t *testing.T) {
	encoder := NewDankEncoder("(a|b|c|d)[0-9]")

	// Normal execution with limit
	ctx := context.Background()
	results, err := encoder.GenerateAtFixedLengthWithContextAndLimit(ctx, 2, 7)
	require.NoError(t, err)
	require.Len(t, results, 7)

	// Cancelled context with limit
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	results, err = encoder.GenerateAtFixedLengthWithContextAndLimit(canceledCtx, 2, 7)
	require.Error(t, err)
	require.Equal(t, context.Canceled, err)
	require.Empty(t, results)
}

func TestNumWordsSaturates(t *testing.T) {
	for _, n := range []int{14, 15} {
		pattern := strings.Repeat("[a-z]", n)
		require.Equal(t, int64(math.MaxInt64), NewDankEncoder(pattern).NumWords(n, n), "length %d", n)
		require.Equal(t, int64(math.MaxInt64), CountWords(pattern, n, n), "count-only, length %d", n)
	}

	// The long branch overflows at length 15 but only accepts at length 16.
	pattern := strings.Repeat("[a-z]", 15) + "x|z"
	require.Equal(t, int64(1), NewDankEncoder(pattern).NumWords(1, 15))
	require.Equal(t, int64(1), CountWords(pattern, 1, 15))
}

func TestGenerateAtImpossibleLength(t *testing.T) {
	pattern := strings.Repeat("[a-z]", 12) + "(a|bb)?"
	encoder := NewDankEncoder(pattern)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	results, err := encoder.GenerateAtFixedLengthWithContextAndLimit(ctx, 15, 500)
	require.NoError(t, err)
	require.Empty(t, results)
}

func TestCountWordsDistinctStringsInLengthRange(t *testing.T) {
	const pattern = `(a|a|b)(1|2)?`
	encoder := NewDankEncoder(pattern)
	for _, tc := range []struct {
		minLen, maxLen int
		want           int64
	}{
		{0, 0, 0},
		{1, 1, 2},
		{2, 2, 4},
		{1, 256, 6},
		{3, 256, 0},
	} {
		require.Equal(t, tc.want, encoder.NumWords(tc.minLen, tc.maxLen))
		require.Equal(t, tc.want, CountWords(pattern, tc.minLen, tc.maxLen))
	}
}

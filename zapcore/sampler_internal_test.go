// Copyright (c) 2026 Uber Technologies, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package zapcore

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCounterIncCheckReset(t *testing.T) {
	var c counter

	// First event in a bucket starts the count.
	assert.Equal(t, uint64(1), c.IncCheckReset(0))
	assert.Equal(t, uint64(2), c.IncCheckReset(0))
	assert.Equal(t, uint64(3), c.IncCheckReset(0))

	// Moving to an older bucket (clock rollback) never restarts the count.
	assert.Equal(t, uint64(4), c.IncCheckReset(-1))
	assert.Equal(t, uint64(5), c.IncCheckReset(0))

	// Only a strictly newer bucket lazily resets the stale state.
	assert.Equal(t, uint64(1), c.IncCheckReset(1))
	assert.Equal(t, uint64(2), c.IncCheckReset(1))

	// Jumping several buckets ahead at once also resets.
	assert.Equal(t, uint64(1), c.IncCheckReset(5))
}

// fakeRollbackClock returns wall-only times (no monotonic component) that the
// test can move arbitrarily.
type fakeRollbackClock struct{ now time.Time }

func (c *fakeRollbackClock) Now() time.Time { return c.now.Round(0) }
func (c *fakeRollbackClock) NewTicker(d time.Duration) *time.Ticker {
	return time.NewTicker(d)
}

// TestSamplerBucketMonotonic verifies that elapsed-time buckets are immune to
// a wall-clock rollback and only advance on a genuinely new interval.
func TestSamplerBucketMonotonic(t *testing.T) {
	const tick = time.Second
	clk := &fakeRollbackClock{now: time.Unix(100, 0)}

	s, ok := NewSamplerWithOptions(
		NewNopCore(), tick, 1, 1, SamplerClock(clk),
	).(*sampler)
	require.True(t, ok, "expected *sampler")

	assert.Equal(t, int64(0), s.bucket())

	clk.now = clk.now.Add(500 * time.Millisecond)
	assert.Equal(t, int64(0), s.bucket())

	// Virtual wall-clock rollback keeps us in (or before) the same bucket.
	clk.now = clk.now.Add(-time.Second)
	assert.True(t, s.bucket() <= 0, "bucket must not advance past 0 after rollback")

	// Advance well beyond the original boundary to reach a new bucket.
	clk.now = clk.now.Add(3 * time.Second)
	assert.Equal(t, int64(2), s.bucket())
}

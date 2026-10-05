// Copyright (c) 2024 Uber Technologies, Inc.
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

package zapcore_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap/zaptest/observer"

	//revive:disable:dot-imports
	. "go.uber.org/zap/zapcore"

	"github.com/stretchr/testify/assert"
)

// manualClock is a Clock whose time only moves forward (or is explicitly
// rolled back) under the test's control. It never moves on its own, so the
// sampler's tick boundaries are fully deterministic and no real sleeping is
// required.
type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func newManualClock(now time.Time) *manualClock {
	return &manualClock{now: now}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *manualClock) Rollback(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(-d)
}

func (c *manualClock) NewTicker(d time.Duration) *time.Ticker {
	return time.NewTicker(d)
}

// logSequenceAt logs the i-th event for the given level, attaching the
// sequence number as an "iter" field and using t as the entry timestamp.
func logSequenceAt(core Core, i int, lvl Level, t time.Time) {
	core = core.With([]Field{makeInt64Field("iter", i)})
	if ce := core.Check(Entry{Level: lvl, Message: "msg", Time: t}, nil); ce != nil {
		ce.Write()
	}
}

func logAt(core Core, lvl Level, msg string) {
	if ce := core.Check(Entry{Level: lvl, Message: msg}, nil); ce != nil {
		ce.Write()
	}
}

// loggedIxs extracts the "iter" sequence numbers from logged entries.
func loggedIxs(entries []observer.LoggedEntry) []int64 {
	ixs := make([]int64, 0, len(entries))
	for _, e := range entries {
		for _, f := range e.Context {
			if f.Key == "iter" {
				ixs = append(ixs, f.Integer)
			}
		}
	}
	return ixs
}

// Within a single bucket the first two entries pass and every second entry
// thereafter passes.
func TestSamplerClock_Frequency(t *testing.T) {
	clock := newManualClock(time.Now())
	core, logs := observer.New(DebugLevel)
	sampler := NewSamplerWithOptions(core, time.Second, 2, 2, SamplerClock(clock))

	for i := 1; i <= 10; i++ {
		writeSequence(sampler, i, InfoLevel)
	}
	assert.Equal(t, []int64{1, 2, 4, 6, 8, 10}, loggedIxs(logs.TakeAll()))
}

// A virtual wall-clock rollback must not reset the bucket: the counter keeps
// advancing and the first-N allowance is not re-issued. The entry timestamps
// are deliberately placed in the future to prove that bucket progression is
// driven by the injected clock, not by Entry.Time.
func TestSamplerClock_RollbackDoesNotReset(t *testing.T) {
	base := time.Now()
	clock := newManualClock(base)
	core, logs := observer.New(DebugLevel)
	sampler := NewSamplerWithOptions(core, time.Second, 2, 2, SamplerClock(clock))

	// Bucket 1: the counter advances to 10.
	for i := 1; i <= 10; i++ {
		logSequenceAt(sampler, i, InfoLevel, base)
	}
	assert.Equal(t, []int64{1, 2, 4, 6, 8, 10}, loggedIxs(logs.TakeAll()))

	// Virtual rollback: the clock moves backwards to well before the bucket
	// boundary while entry timestamps stay in the future.
	clock.Rollback(time.Hour)
	for i := 11; i <= 20; i++ {
		logSequenceAt(sampler, i, InfoLevel, base.Add(2*time.Hour))
	}
	// The counter is at 11..20; entries pass where (n-2)%2 == 0.
	assert.Equal(t, []int64{12, 14, 16, 18, 20}, loggedIxs(logs.TakeAll()))

	// Advance past the bucket boundary: only now does the bucket reset and
	// the first-N allowance get re-issued.
	clock.Advance(2 * time.Hour)
	for i := 21; i <= 23; i++ {
		logSequenceAt(sampler, i, InfoLevel, base.Add(2*time.Hour))
	}
	assert.Equal(t, []int64{21, 22}, loggedIxs(logs.TakeAll()))
}

// Crossing a bucket boundary clears the stale counter state so the first N
// entries of the new bucket are logged again.
func TestSamplerClock_NewBucketClearsCounts(t *testing.T) {
	base := time.Now()
	clock := newManualClock(base)
	core, logs := observer.New(DebugLevel)
	sampler := NewSamplerWithOptions(core, time.Second, 2, 2, SamplerClock(clock))

	for i := 1; i <= 10; i++ {
		logSequenceAt(sampler, i, InfoLevel, base)
	}
	assert.Equal(t, []int64{1, 2, 4, 6, 8, 10}, loggedIxs(logs.TakeAll()))

	clock.Advance(time.Second)
	for i := 1; i <= 10; i++ {
		logSequenceAt(sampler, i, InfoLevel, base.Add(time.Second))
	}
	assert.Equal(t, []int64{1, 2, 4, 6, 8, 10}, loggedIxs(logs.TakeAll()))
}

// Each level is counted independently: traffic at one level does not consume
// the other level's allowance.
func TestSamplerClock_LevelsIndependent(t *testing.T) {
	clock := newManualClock(time.Now())
	core, logs := observer.New(DebugLevel)
	sampler := NewSamplerWithOptions(core, time.Second, 2, 2, SamplerClock(clock))

	for i := 1; i <= 10; i++ {
		writeSequence(sampler, i, InfoLevel)
		writeSequence(sampler, i, WarnLevel)
	}

	var info, warn []observer.LoggedEntry
	for _, e := range logs.TakeAll() {
		switch e.Entry.Level {
		case InfoLevel:
			info = append(info, e)
		case WarnLevel:
			warn = append(warn, e)
		}
	}
	assert.Equal(t, []int64{1, 2, 4, 6, 8, 10}, loggedIxs(info))
	assert.Equal(t, []int64{1, 2, 4, 6, 8, 10}, loggedIxs(warn))
}

// Each message is counted independently at the same level.
func TestSamplerClock_MessagesIndependent(t *testing.T) {
	clock := newManualClock(time.Now())
	core, logs := observer.New(DebugLevel)
	sampler := NewSamplerWithOptions(core, time.Second, 2, 2, SamplerClock(clock))

	for i := 1; i <= 10; i++ {
		logAt(sampler, InfoLevel, "alpha")
		logAt(sampler, InfoLevel, "beta")
	}
	assert.Equal(t, 12, logs.Len(), "each message keeps its own allowance")
}

// A failing (panicking) observation hook must not change the sampling
// decision: the entry is still sampled or dropped exactly as decided.
func TestSamplerClock_HookFailureDoesNotChangeDecision(t *testing.T) {
	clock := newManualClock(time.Now())
	core, logs := observer.New(DebugLevel)
	panicHook := func(Entry, SamplingDecision) {
		panic("sampler hook failure must not change the sampling decision")
	}
	sampler := NewSamplerWithOptions(
		core, time.Second, 2, 2,
		SamplerClock(clock),
		SamplerHook(panicHook),
	)

	for i := 1; i <= 10; i++ {
		writeSequence(sampler, i, InfoLevel)
	}
	assert.Equal(t, []int64{1, 2, 4, 6, 8, 10}, loggedIxs(logs.TakeAll()))
}

// Concurrent counting is checked with a barrier: all goroutines are released
// at once and contend over the same per-bucket counter. The hook-recorded
// sampled/dropped counts must match the actual number of logged entries.
func TestSamplerClock_ConcurrentCounting(t *testing.T) {
	const (
		numGoroutines = 16
		eventsPerG    = 100
		first         = 2
		thereafter    = 3
	)

	clock := newManualClock(time.Now())
	core, logs := observer.New(DebugLevel)
	hook, dropped, sampled := makeSamplerCountingHook()
	sampler := NewSamplerWithOptions(
		core, time.Hour, first, thereafter,
		SamplerClock(clock),
		SamplerHook(hook),
	)

	barrier := make(chan struct{})
	var wg sync.WaitGroup
	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-barrier // released simultaneously: all goroutines contend together
			for i := 0; i < eventsPerG; i++ {
				writeSequence(sampler, i, InfoLevel)
			}
		}()
	}
	close(barrier)
	wg.Wait()

	total := numGoroutines * eventsPerG
	expectedSampled := first + (total-first)/thereafter
	assert.Equal(t, expectedSampled, logs.Len(), "unexpected number of sampled logs")
	assert.Equal(t, expectedSampled, int(sampled.Load()), "hook recorded the wrong sampled count")
	assert.Equal(t, total-expectedSampled, int(dropped.Load()), "hook recorded the wrong dropped count")
}

// Without SamplerClock the sampler keeps its historical behavior and drives
// bucket boundaries from Entry.Time.
func TestSamplerClock_DefaultUsesEntryTime(t *testing.T) {
	base := time.Now()
	core, logs := observer.New(DebugLevel)
	sampler := NewSamplerWithOptions(core, time.Second, 2, 2)

	for i := 1; i <= 10; i++ {
		logSequenceAt(sampler, i, InfoLevel, base)
	}
	assert.Equal(t, []int64{1, 2, 4, 6, 8, 10}, loggedIxs(logs.TakeAll()))

	for i := 1; i <= 10; i++ {
		logSequenceAt(sampler, i, InfoLevel, base.Add(time.Second))
	}
	assert.Equal(t, []int64{1, 2, 4, 6, 8, 10}, loggedIxs(logs.TakeAll()))
}

// Ten events in one bucket: the first two pass, then every second event
// passes. The clock never moves backwards, so the bucket boundary is stable.
func Example_samplerClock() {
	clock := newManualClock(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	core, logs := observer.New(InfoLevel)
	sampler := NewSamplerWithOptions(core, time.Second, 2, 2, SamplerClock(clock))

	for i := 1; i <= 10; i++ {
		logSequenceAt(sampler, i, InfoLevel, clock.Now())
	}

	for _, e := range logs.TakeAll() {
		for _, f := range e.Context {
			if f.Key == "iter" {
				fmt.Println(f.Integer)
			}
		}
	}
	// Output:
	// 1
	// 2
	// 4
	// 6
	// 8
	// 10
}

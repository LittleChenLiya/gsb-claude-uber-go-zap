// Copyright (c) 2016-2022 Uber Technologies, Inc.
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
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap/internal/ztest"
	//revive:disable:dot-imports
	. "go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fakeSampler(lvl LevelEnabler, tick time.Duration, first, thereafter int) (Core, *observer.ObservedLogs) {
	core, logs := observer.New(lvl)
	// Keep using deprecated constructor for cc.
	core = NewSampler(core, tick, first, thereafter)
	return core, logs
}

// controllableClock is a deterministic Clock for sampler tests. Time is fixed
// and wall-only (no monotonic component), and can be moved freely, including
// backwards, without any real sleeping.
type controllableClock struct {
	mu  sync.RWMutex
	now time.Time
}

var _ Clock = (*controllableClock)(nil)

func newControllableClock() *controllableClock {
	return &controllableClock{
		// Round(0) drops the monotonic clock reading so backwards movement is
		// observable through plain wall-time subtraction.
		now: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Round(0),
	}
}

func (c *controllableClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.now
}

func (c *controllableClock) NewTicker(d time.Duration) *time.Ticker {
	return time.NewTicker(d)
}

// add moves the clock forward (or, with a negative duration, backwards).
func (c *controllableClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d).Round(0)
}

// writeEntry reports whether the sampler admitted an entry at the given level.
func writeEntry(core Core, lvl Level, msg string) bool {
	if ce := core.Check(Entry{Level: lvl, Message: msg}, nil); ce != nil {
		ce.Write()
		return true
	}
	return false
}

func assertSequence(t testing.TB, logs []observer.LoggedEntry, lvl Level, seq ...int64) {
	seen := make([]int64, len(logs))
	for i, entry := range logs {
		require.Equal(t, "", entry.Message, "Message wasn't created by writeSequence.")
		require.Equal(t, 1, len(entry.Context), "Unexpected number of fields.")
		require.Equal(t, lvl, entry.Level, "Unexpected level.")
		f := entry.Context[0]
		require.Equal(t, "iter", f.Key, "Unexpected field key.")
		require.Equal(t, Int64Type, f.Type, "Unexpected field type")
		seen[i] = f.Integer
	}
	assert.Equal(t, seq, seen, "Unexpected sequence logged at level %v.", lvl)
}

func writeSequence(core Core, n int, lvl Level) {
	// All tests using writeSequence verify that counters are shared between
	// parent and child cores.
	core = core.With([]Field{makeInt64Field("iter", n)})
	if ce := core.Check(Entry{Level: lvl, Time: time.Now()}, nil); ce != nil {
		ce.Write()
	}
}

func TestSampler(t *testing.T) {
	for _, lvl := range []Level{DebugLevel, InfoLevel, WarnLevel, ErrorLevel, DPanicLevel, PanicLevel, FatalLevel} {
		sampler, logs := fakeSampler(DebugLevel, time.Minute, 2, 3)

		// Ensure that counts aren't shared between levels.
		probeLevel := DebugLevel
		if lvl == DebugLevel {
			probeLevel = InfoLevel
		}
		for i := 0; i < 10; i++ {
			writeSequence(sampler, 1, probeLevel)
		}
		// Clear any output.
		logs.TakeAll()

		for i := 1; i < 10; i++ {
			writeSequence(sampler, i, lvl)
		}
		assertSequence(t, logs.TakeAll(), lvl, 1, 2, 5, 8)
	}
}

func TestLevelOfSampler(t *testing.T) {
	levels := []Level{DebugLevel, InfoLevel, WarnLevel, ErrorLevel, DPanicLevel, PanicLevel, FatalLevel}
	for _, lvl := range levels {
		lvl := lvl
		t.Run(lvl.String(), func(t *testing.T) {
			t.Parallel()

			sampler, _ := fakeSampler(lvl, time.Minute, 2, 3)
			assert.Equal(t, lvl, LevelOf(sampler), "Sampler level did not match.")
		})
	}
}

func TestSamplerDisabledLevels(t *testing.T) {
	sampler, logs := fakeSampler(InfoLevel, time.Minute, 1, 100)

	// Shouldn't be counted, because debug logging isn't enabled.
	writeSequence(sampler, 1, DebugLevel)
	writeSequence(sampler, 2, InfoLevel)
	assertSequence(t, logs.TakeAll(), InfoLevel, 2)
}

func TestSamplerTicking(t *testing.T) {
	// Ensure that we're resetting the sampler's counter every tick. Buckets are
	// driven by an injected, controllable clock, so this test performs no real
	// sleeps.
	tick := 10 * time.Millisecond
	clock := newControllableClock()

	core, logs := observer.New(DebugLevel)
	sampler := NewSamplerWithOptions(core, tick, 5, 10, SamplerClock(clock))

	// If we log five or fewer messages every tick, none of them should be
	// dropped.
	for bucket := 0; bucket < 2; bucket++ {
		for i := 1; i <= 5; i++ {
			writeSequence(sampler, i, InfoLevel)
		}
		clock.add(tick)
	}
	assertSequence(
		t,
		logs.TakeAll(),
		InfoLevel,
		1, 2, 3, 4, 5, // first bucket
		1, 2, 3, 4, 5, // second bucket
	)

	// If we log quickly, we should drop some logs. The first five statements
	// each tick should be logged, then every tenth.
	for bucket := 0; bucket < 3; bucket++ {
		for i := 1; i < 18; i++ {
			writeSequence(sampler, i, InfoLevel)
		}
		clock.add(tick)
	}

	assertSequence(
		t,
		logs.TakeAll(),
		InfoLevel,
		1, 2, 3, 4, 5, 15, // first bucket
		1, 2, 3, 4, 5, 15, // second bucket
		1, 2, 3, 4, 5, 15, // third bucket
	)
}

// TestSamplerClockRollbackAndNewBucket verifies the core acceptance criteria:
// the first N entries in a bucket are admitted, subsequent entries follow the
// sampling frequency, a virtual rollback of the clock does not reset the
// counts, and only advancing into a genuinely new bucket clears them.
func TestSamplerClockRollbackAndNewBucket(t *testing.T) {
	const tick = time.Second
	clock := newControllableClock()

	var cc countingCore
	sampler := NewSamplerWithOptions(&cc, tick, 2, 3, SamplerClock(clock))

	check := func(msg string, want bool) {
		t.Helper()
		assert.Equal(t, want, writeEntry(sampler, InfoLevel, msg),
			"unexpected decision for message %q", msg)
	}

	// First bucket: first two pass, then every third (n = 5, 8, ...).
	check("m", true)  // n=1
	check("m", true)  // n=2
	check("m", false) // n=3
	check("m", false) // n=4
	check("m", true)  // n=5
	check("m", false) // n=6
	check("m", false) // n=7
	check("m", true)  // n=8

	// Move the wall clock backwards within the same tick. The in-progress
	// bucket's counts must survive: the first two entries are not let through
	// again.
	clock.add(-tick)
	check("m", false) // n=9
	check("m", false) // n=10
	check("m", true)  // n=11

	// Moving forward again, but not past the original bucket boundary, must
	// still be the same bucket.
	clock.add(tick / 2)
	check("m", false) // n=12

	// Advance to a genuinely new bucket: counts start over.
	clock.add(2 * tick)
	check("m", true)  // n=1 in new bucket
	check("m", true)  // n=2 in new bucket
	check("m", false) // n=3 in new bucket
}

// TestSamplerClockIndependentLevels verifies that two levels each get their
// own quota and neither crowds the other out, both in the same bucket and
// after a bucket change.
func TestSamplerClockIndependentLevels(t *testing.T) {
	const tick = time.Second
	clock := newControllableClock()

	var cc countingCore
	sampler := NewSamplerWithOptions(&cc, tick, 2, 3, SamplerClock(clock))

	check := func(lvl Level, want bool) {
		t.Helper()
		assert.Equal(t, want, writeEntry(sampler, lvl, "m"),
			"unexpected decision at level %v", lvl)
	}

	// Each level independently admits its first two entries.
	check(InfoLevel, true)
	check(WarnLevel, true)
	check(InfoLevel, true)
	check(WarnLevel, true)
	// n=3 for each: dropped for both.
	check(InfoLevel, false)
	check(WarnLevel, false)
	// n=4 for each: dropped.
	check(InfoLevel, false)
	check(WarnLevel, false)
	// n=5 for each: every third after the first two is admitted.
	check(InfoLevel, true)
	check(WarnLevel, true)

	// A new bucket resets both levels independently.
	clock.add(tick)
	check(InfoLevel, true)
	check(WarnLevel, true)
}

// TestSamplerHookPanicDoesNotChangeDecision verifies that a panicking
// observation callback is recovered from and does not alter the sampling
// result, whether the entry is sampled or dropped.
func TestSamplerHookPanicDoesNotChangeDecision(t *testing.T) {
	clock := newControllableClock()
	var cc countingCore
	panickingHook := func(Entry, SamplingDecision) { panic("boom") }
	sampler := NewSamplerWithOptions(
		&cc, time.Second, 1, 0, SamplerClock(clock),
		SamplerHook(panickingHook),
	)

	assert.NotPanics(t, func() {
		assert.True(t, writeEntry(sampler, InfoLevel, "first"))
		// thereafter == 0: everything after the first entry in this bucket is
		// dropped, and the hook panics again without unwinding us.
		assert.False(t, writeEntry(sampler, InfoLevel, "first"))
	})
	assert.Equal(t, uint32(1), cc.logs.Load())
}

type countingCore struct {
	logs atomic.Uint32
}

func (c *countingCore) Check(ent Entry, ce *CheckedEntry) *CheckedEntry {
	return ce.AddCore(ent, c)
}

func (c *countingCore) Write(Entry, []Field) error {
	c.logs.Add(1)
	return nil
}

func (c *countingCore) With([]Field) Core { return c }
func (*countingCore) Enabled(Level) bool  { return true }
func (*countingCore) Sync() error         { return nil }

func TestSamplerConcurrent(t *testing.T) {
	const (
		logsPerTick   = 10
		numMessages   = 5
		numTicks      = 25
		numGoroutines = 10
		tick          = 10 * time.Millisecond

		// We'll make a total of,
		// (numGoroutines * numTicks * logsPerTick * 2) log attempts
		// with numMessages unique messages.
		numLogAttempts = numGoroutines * logsPerTick * numTicks * 2
		// Of those, we'll accept (logsPerTick * numTicks) entries
		// for each unique message.
		expectedCount = numMessages * logsPerTick * numTicks
		// The rest will be dropped.
		expectedDropped = numLogAttempts - expectedCount
	)

	clock := ztest.NewMockClock()

	cc := &countingCore{}

	hook, dropped, sampled := makeSamplerCountingHook()
	sampler := NewSamplerWithOptions(
		cc, tick, logsPerTick, 100000,
		SamplerHook(hook),
		SamplerClock(clock),
	)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(i int, ticker *time.Ticker) {
			defer wg.Done()
			defer ticker.Stop()

			for {
				select {
				case <-stop:
					return

				case <-ticker.C:
					for j := 0; j < logsPerTick*2; j++ {
						msg := fmt.Sprintf("msg%v", i%numMessages)
						ent := Entry{
							Level:   DebugLevel,
							Message: msg,
							Time:    clock.Now(),
						}
						if ce := sampler.Check(ent, nil); ce != nil {
							ce.Write()
						}

						// Give a chance for other goroutines to run.
						runtime.Gosched()
					}
				}
			}
		}(i, clock.NewTicker(tick))
	}

	clock.Add(tick * numTicks)
	close(stop)
	wg.Wait()

	assert.Equal(
		t,
		expectedCount,
		int(cc.logs.Load()),
		"Unexpected number of logs",
	)
	assert.Equal(t,
		expectedCount,
		int(sampled.Load()),
		"Unexpected number of logs sampled",
	)
	assert.Equal(t,
		expectedDropped,
		int(dropped.Load()),
		"Unexpected number of logs dropped",
	)
}

func TestSamplerRaces(t *testing.T) {
	sampler, _ := fakeSampler(DebugLevel, time.Minute, 1, 1000)

	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			<-start
			for j := 0; j < 100; j++ {
				writeSequence(sampler, j, InfoLevel)
			}
			wg.Done()
		}()
	}

	close(start)
	wg.Wait()
}

// TestSamplerConcurrentBarrier drives many goroutines through a single frozen
// bucket using start/done barriers, so the exact number of sampled and dropped
// events can be checked without any timing or sleeps.
func TestSamplerConcurrentBarrier(t *testing.T) {
	const (
		numGoroutines = 10
		eventsPerG    = 100
		first         = 2
		thereafter    = 3

		totalAttempts = numGoroutines * eventsPerG // 1000
	)

	// Frozen clock: the bucket never changes during the run.
	clock := newControllableClock()

	cc := &countingCore{}
	hook, dropped, sampled := makeSamplerCountingHook()
	sampler := NewSamplerWithOptions(
		cc, time.Second, first, thereafter,
		SamplerClock(clock),
		SamplerHook(hook),
	)

	var (
		wg    sync.WaitGroup
		ready = make(chan struct{}, numGoroutines) // each g signals readiness
		start = make(chan struct{})                // start barrier
	)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ready <- struct{}{}
			<-start // start barrier: nobody counts until all are ready

			for j := 0; j < eventsPerG; j++ {
				writeEntry(sampler, InfoLevel, "same-message")
			}
		}()
	}

	// Park on the ready barrier until every goroutine is blocked on start.
	for i := 0; i < numGoroutines; i++ {
		<-ready
	}
	close(start)
	wg.Wait()

	// Within one bucket: n=1,2 are admitted and then every 3rd after that
	// (n=5,8,...,998).
	wantSampled := first + (1000-first)/thereafter // 2 + 332 = 334
	wantDropped := totalAttempts - wantSampled     // 666

	assert.Equal(t, wantSampled, int(cc.logs.Load()),
		"unexpected number of logs written")
	assert.Equal(t, wantSampled, int(sampled.Load()),
		"unexpected number of sampled callbacks")
	assert.Equal(t, wantDropped, int(dropped.Load()),
		"unexpected number of dropped callbacks")
}

func TestSamplerUnknownLevels(t *testing.T) {
	// Prove that out-of-bounds levels don't panic.
	unknownLevels := []Level{
		DebugLevel - 1,
		FatalLevel + 1,
	}

	for _, lvl := range unknownLevels {
		t.Run(lvl.String(), func(t *testing.T) {
			sampler, logs := fakeSampler(lvl, time.Minute, 2, 3)
			for i := 1; i < 10; i++ {
				writeSequence(sampler, i, lvl)
			}

			// Expect no sampling for unknown levels.
			assertSequence(t, logs.TakeAll(), lvl, 1, 2, 3, 4, 5, 6, 7, 8, 9)
		})
	}
}

func TestSamplerWithZeroThereafter(t *testing.T) {
	var counter countingCore

	clock := newControllableClock()

	// Logs two messages per second.
	sampler := NewSamplerWithOptions(
		&counter, time.Second, 2, 0, SamplerClock(clock),
	)

	now := clock.Now()

	for i := 0; i < 1000; i++ {
		ent := Entry{
			Level:   InfoLevel,
			Message: "msg",
			Time:    now,
		}
		if ce := sampler.Check(ent, nil); ce != nil {
			ce.Write()
		}
	}

	assert.Equal(t, 2, int(counter.logs.Load()),
		"Unexpected number of logs")

	// Buckets are driven by the injected clock, not by entry times.
	clock.add(time.Second)

	for i := 0; i < 1000; i++ {
		ent := Entry{
			Level:   InfoLevel,
			Message: "msg",
			Time:    now,
		}
		if ce := sampler.Check(ent, nil); ce != nil {
			ce.Write()
		}
	}

	assert.Equal(t, 4, int(counter.logs.Load()),
		"Unexpected number of logs")
}

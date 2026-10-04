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
	"errors"
	"fmt"
	"sync"
	"testing"

	"go.uber.org/multierr"
	//revive:disable:dot-imports
	. "go.uber.org/zap/zapcore"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest/observer"
)

// recordingCore is a fake Core that records every Entry and field slice it
// receives, and can be configured to fail on Write and/or Sync.
type recordingCore struct {
	mu sync.Mutex

	level   Level
	entries []Entry
	fields  [][]Field
	context []Field

	writeErr error
	syncErr  error
	syncs    int
}

func newRecordingCore(level Level) *recordingCore {
	return &recordingCore{level: level}
}

func (c *recordingCore) Enabled(lvl Level) bool {
	return lvl >= c.level
}

func (c *recordingCore) Level() Level {
	return c.level
}

func (c *recordingCore) With(fields []Field) Core {
	c.mu.Lock()
	defer c.mu.Unlock()
	return &recordingCore{
		level:   c.level,
		context: append(append([]Field(nil), c.context...), fields...),
	}
}

func (c *recordingCore) Check(ent Entry, ce *CheckedEntry) *CheckedEntry {
	if c.Enabled(ent.Level) {
		return ce.AddCore(ent, c)
	}
	return ce
}

func (c *recordingCore) Write(ent Entry, fields []Field) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, ent)
	c.fields = append(c.fields, fields)
	return c.writeErr
}

func (c *recordingCore) Sync() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.syncs++
	return c.syncErr
}

func (c *recordingCore) snapshot() ([]Entry, [][]Field, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.entries, c.fields, c.syncs
}

func TestFanoutWriteFailure(t *testing.T) {
	// Three fake Cores; the second fails on Write. The first and third must
	// still receive the same Entry and fields, and the per-target results
	// must precisely identify the second target.
	c1 := newRecordingCore(DebugLevel)
	c2 := newRecordingCore(DebugLevel)
	c3 := newRecordingCore(DebugLevel)
	c2.writeErr = errors.New("write boom")

	fan := NewFanout(
		Target{Name: "first", Core: c1},
		Target{Name: "second", Core: c2},
		Target{Name: "third", Core: c3},
	)

	ent := Entry{Level: InfoLevel, Message: "hello"}
	fields := []Field{makeInt64Field("k", 42)}

	results := fan.WriteResults(ent, fields)
	require.Len(t, results, 3, "expected one result per target")

	// First and third succeeded; second failed.
	assert.True(t, results[0].OK(), "first target should succeed")
	assert.False(t, results[1].OK(), "second target should fail")
	assert.True(t, results[2].OK(), "third target should succeed")

	// The result precisely identifies the second target.
	assert.Equal(t, "second", results[1].Name)
	assert.Equal(t, StageWrite, results[1].Stage)
	assert.EqualError(t, results[1].Err, "write boom")

	// The first and third received the same Entry and fields.
	gotEntries, gotFields, _ := c1.snapshot()
	require.Len(t, gotEntries, 1)
	assert.Equal(t, ent, gotEntries[0])
	require.Len(t, gotFields, 1)
	assert.Equal(t, fields, gotFields[0])

	gotEntries, gotFields, _ = c3.snapshot()
	require.Len(t, gotEntries, 1)
	assert.Equal(t, ent, gotEntries[0])
	require.Len(t, gotFields, 1)
	assert.Equal(t, fields, gotFields[0])

	// The second target also received the entry (it was called).
	gotEntries, _, _ = c2.snapshot()
	require.Len(t, gotEntries, 1, "second target should still be called")
	assert.Equal(t, ent, gotEntries[0])

	// The aggregated error returned by Write carries the target name and
	// stage, and is not just the last error.
	err := fan.Write(ent, fields)
	require.Error(t, err)
	errs := multierr.Errors(err)
	require.Len(t, errs, 1, "only one target failed")

	var te *TargetError
	require.ErrorAs(t, errs[0], &te)
	assert.Equal(t, "second", te.Name)
	assert.Equal(t, StageWrite, te.Stage)
	assert.EqualError(t, te.Err, "write boom")
	assert.Contains(t, err.Error(), `target "second" write:`)
}

func TestFanoutSyncFailure(t *testing.T) {
	// Two of three targets fail on Sync. Both errors must be preserved.
	c1 := newRecordingCore(DebugLevel)
	c2 := newRecordingCore(DebugLevel)
	c3 := newRecordingCore(DebugLevel)
	c1.syncErr = errors.New("sync one")
	c3.syncErr = errors.New("sync two")

	fan := NewFanout(
		Target{Name: "a", Core: c1},
		Target{Name: "b", Core: c2},
		Target{Name: "c", Core: c3},
	)

	results := fan.SyncResults()
	require.Len(t, results, 3)

	assert.False(t, results[0].OK())
	assert.True(t, results[1].OK())
	assert.False(t, results[2].OK())

	assert.Equal(t, "a", results[0].Name)
	assert.Equal(t, StageSync, results[0].Stage)
	assert.EqualError(t, results[0].Err, "sync one")

	assert.Equal(t, "c", results[2].Name)
	assert.Equal(t, StageSync, results[2].Stage)
	assert.EqualError(t, results[2].Err, "sync two")

	// All targets were synced, even after a failure.
	_, _, s1 := c1.snapshot()
	_, _, s2 := c2.snapshot()
	_, _, s3 := c3.snapshot()
	assert.Equal(t, 1, s1)
	assert.Equal(t, 1, s2)
	assert.Equal(t, 1, s3)

	// The aggregated error preserves both errors in target order.
	err := fan.Sync()
	require.Error(t, err)
	errs := multierr.Errors(err)
	require.Len(t, errs, 2, "both sync errors should be preserved")

	var te *TargetError
	require.ErrorAs(t, errs[0], &te)
	assert.Equal(t, "a", te.Name)
	assert.Equal(t, StageSync, te.Stage)

	require.ErrorAs(t, errs[1], &te)
	assert.Equal(t, "c", te.Name)
	assert.Equal(t, StageSync, te.Stage)
}

func TestFanoutWithDoesNotModifyParent(t *testing.T) {
	// With at the combinator layer must not modify the parent Core.
	c1 := newRecordingCore(DebugLevel)
	c2 := newRecordingCore(DebugLevel)

	fan := NewFanout(
		Target{Name: "a", Core: c1},
		Target{Name: "b", Core: c2},
	)

	child := fan.With([]Field{makeInt64Field("k", 1)})
	require.NotSame(t, fan, child, "With should return a new Core")

	// The parent's targets must not have been modified.
	_, fields1, _ := c1.snapshot()
	_, fields2, _ := c2.snapshot()
	assert.Empty(t, fields1, "parent target a should not be modified")
	assert.Empty(t, fields2, "parent target b should not be modified")

	// The child is a different Core; writing to it does not touch the parent.
	ent := Entry{Level: InfoLevel, Message: "child"}
	if ce := child.Check(ent, nil); ce != nil {
		ce.Write()
	}
	entries1, _, _ := c1.snapshot()
	entries2, _, _ := c2.snapshot()
	assert.Empty(t, entries1, "parent target a should not receive child writes")
	assert.Empty(t, entries2, "parent target b should not receive child writes")
}

func TestFanoutWithFieldsCopiedPerTarget(t *testing.T) {
	// Fields added via With should be copied separately to each Core, so
	// that one encoder consuming (or mutating) the fields cannot affect
	// another target. We use a core whose With mutates the fields slice in
	// place, and a second core that stores what it received. If the
	// combinator passed the same slice to both targets, the second target
	// would see the first target's mutation.
	c1 := &mutatingWithCore{level: DebugLevel}
	c2 := &storingWithCore{level: DebugLevel}

	fan := NewFanout(
		Target{Name: "a", Core: c1},
		Target{Name: "b", Core: c2},
	)

	fields := []Field{makeInt64Field("original", 1)}
	fan.With(fields)

	// The first target mutated its own copy.
	require.Len(t, c1.received, 1)
	assert.Equal(t, "mutated", c1.received[0][0].Key, "target a should have mutated its own copy")

	// The second target should have received the original fields, not the
	// mutated copy from target a.
	require.Len(t, c2.received, 1)
	assert.Equal(t, "original", c2.received[0][0].Key, "target b should see original fields")
}

// mutatingWithCore is a Core that mutates the fields slice in place when
// With is called. It's used to verify that the combinator copies fields per
// target.
type mutatingWithCore struct {
	level    Level
	mu       sync.Mutex
	received [][]Field
}

func (c *mutatingWithCore) Enabled(lvl Level) bool { return lvl >= c.level }
func (c *mutatingWithCore) Level() Level           { return c.level }

func (c *mutatingWithCore) With(fields []Field) Core {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Mutate the fields slice in place.
	if len(fields) > 0 {
		fields[0] = makeInt64Field("mutated", 99)
	}
	// Store a copy of what we received (after mutation).
	c.received = append(c.received, append([]Field(nil), fields...))
	return c
}

func (c *mutatingWithCore) Check(ent Entry, ce *CheckedEntry) *CheckedEntry {
	if c.Enabled(ent.Level) {
		return ce.AddCore(ent, c)
	}
	return ce
}

func (c *mutatingWithCore) Write(ent Entry, fields []Field) error { return nil }
func (c *mutatingWithCore) Sync() error                           { return nil }

// storingWithCore is a Core that stores the fields it received on With
// without mutating them.
type storingWithCore struct {
	level    Level
	mu       sync.Mutex
	received [][]Field
}

func (c *storingWithCore) Enabled(lvl Level) bool { return lvl >= c.level }
func (c *storingWithCore) Level() Level           { return c.level }

func (c *storingWithCore) With(fields []Field) Core {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.received = append(c.received, append([]Field(nil), fields...))
	return c
}

func (c *storingWithCore) Check(ent Entry, ce *CheckedEntry) *CheckedEntry {
	if c.Enabled(ent.Level) {
		return ce.AddCore(ent, c)
	}
	return ce
}

func (c *storingWithCore) Write(ent Entry, fields []Field) error { return nil }
func (c *storingWithCore) Sync() error                           { return nil }

func TestFanoutCheckDisabledTargets(t *testing.T) {
	// Check must only add enabled targets to the CheckedEntry. We use the
	// observer package to verify which targets received the entry.
	debugCore, debugLogs := observer.New(DebugLevel)
	warnCore, warnLogs := observer.New(WarnLevel)

	fan := NewFanout(
		Target{Name: "debug", Core: debugCore},
		Target{Name: "warn", Core: warnCore},
	)

	tests := []struct {
		lvl       Level
		wantDebug bool
		wantWarn  bool
	}{
		{DebugLevel, true, false},
		{InfoLevel, true, false},
		{WarnLevel, true, true},
		{ErrorLevel, true, true},
	}

	for _, tt := range tests {
		debugLogs.TakeAll()
		warnLogs.TakeAll()
		ent := Entry{Level: tt.lvl, Message: "m"}
		ce := fan.Check(ent, nil)
		require.NotNil(t, ce, "level %s should be logged", tt.lvl)
		ce.Write()

		if tt.wantDebug {
			assert.Len(t, debugLogs.All(), 1, "debug target should receive level %s", tt.lvl)
		} else {
			assert.Empty(t, debugLogs.All(), "debug target should not receive level %s", tt.lvl)
		}
		if tt.wantWarn {
			assert.Len(t, warnLogs.All(), 1, "warn target should receive level %s", tt.lvl)
		} else {
			assert.Empty(t, warnLogs.All(), "warn target should not receive level %s", tt.lvl)
		}
	}
}

func TestFanoutEnabled(t *testing.T) {
	c1 := newRecordingCore(WarnLevel)
	c2 := newRecordingCore(ErrorLevel)

	fan := NewFanout(
		Target{Name: "a", Core: c1},
		Target{Name: "b", Core: c2},
	)

	assert.False(t, fan.Enabled(DebugLevel))
	assert.False(t, fan.Enabled(InfoLevel))
	assert.True(t, fan.Enabled(WarnLevel))
	assert.True(t, fan.Enabled(ErrorLevel))
}

func TestFanoutLevel(t *testing.T) {
	c1 := newRecordingCore(WarnLevel)
	c2 := newRecordingCore(ErrorLevel)

	fan := NewFanout(
		Target{Name: "a", Core: c1},
		Target{Name: "b", Core: c2},
	)

	assert.Equal(t, WarnLevel, LevelOf(fan), "Level should be the minimum of all targets")
}

func TestFanoutEmpty(t *testing.T) {
	fan := NewFanout()

	assert.False(t, fan.Enabled(DebugLevel))
	assert.Equal(t, InvalidLevel, LevelOf(fan))
	assert.Nil(t, fan.Check(Entry{Level: InfoLevel}, nil))
	assert.NoError(t, fan.Write(Entry{}, nil))
	assert.NoError(t, fan.Sync())
	assert.Empty(t, fan.WriteResults(Entry{}, nil))
	assert.Empty(t, fan.SyncResults())
}

func TestFanoutErrorOrdering(t *testing.T) {
	// When multiple targets fail, errors must be reported in target order.
	c1 := newRecordingCore(DebugLevel)
	c2 := newRecordingCore(DebugLevel)
	c3 := newRecordingCore(DebugLevel)
	c1.writeErr = errors.New("e1")
	c2.writeErr = errors.New("e2")
	c3.writeErr = errors.New("e3")

	fan := NewFanout(
		Target{Name: "t1", Core: c1},
		Target{Name: "t2", Core: c2},
		Target{Name: "t3", Core: c3},
	)

	results := fan.WriteResults(Entry{}, nil)
	require.Len(t, results, 3)
	assert.Equal(t, "t1", results[0].Name)
	assert.Equal(t, "t2", results[1].Name)
	assert.Equal(t, "t3", results[2].Name)

	err := fan.Write(Entry{}, nil)
	errs := multierr.Errors(err)
	require.Len(t, errs, 3)
	for i, want := range []string{"t1", "t2", "t3"} {
		var te *TargetError
		require.ErrorAs(t, errs[i], &te)
		assert.Equal(t, want, te.Name, "error %d should be from %s", i, want)
	}
}

func TestFanoutTargetErrorUnwrap(t *testing.T) {
	base := errors.New("base error")
	te := &TargetError{Name: "x", Stage: StageWrite, Err: base}

	assert.EqualError(t, te, `target "x" write: base error`)
	assert.ErrorIs(t, te, base, "Unwrap should expose the underlying error")

	teSync := &TargetError{Name: "y", Stage: StageSync, Err: base}
	assert.EqualError(t, teSync, `target "y" sync: base error`)
}

func TestFanoutDoesNotCloseTargets(t *testing.T) {
	// FanoutCore must not close targets; lifetime is the owner's responsibility.
	c1 := newRecordingCore(DebugLevel)
	fan := NewFanout(Target{Name: "a", Core: c1})

	// Sync should not close anything (recordingCore has no close, but verify
	// Sync is just forwarded).
	assert.NoError(t, fan.Sync())
	_, _, syncs := c1.snapshot()
	assert.Equal(t, 1, syncs)
}

func ExampleNewFanout() {
	// Build three named targets. In a real application these might write to
	// different destinations (a file, a network sink, and an in-memory
	// observer).
	console := newRecordingCore(DebugLevel)
	remote := newRecordingCore(DebugLevel)
	audit := newRecordingCore(DebugLevel)
	remote.writeErr = errors.New("connection refused")

	fan := NewFanout(
		Target{Name: "console", Core: console},
		Target{Name: "remote", Core: remote},
		Target{Name: "audit", Core: audit},
	)

	ent := Entry{Level: InfoLevel, Message: "user logged in"}
	results := fan.WriteResults(ent, nil)

	// Check each target's result individually.
	for _, r := range results {
		if !r.OK() {
			fmt.Printf("target %s failed: %v\n", r.Name, r.Err)
		}
	}

	// Output: target remote failed: connection refused
}

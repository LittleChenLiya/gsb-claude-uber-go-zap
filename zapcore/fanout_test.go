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
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS
// FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package zapcore_test

import (
	"errors"
	"strings"
	"testing"

	//revive:disable:dot-imports
	. "go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeCore is a configurable Core that records the entries and fields it
// receives and may fail Write or Sync on demand.
type fakeCore struct {
	enabled      bool
	withFields   []Field
	seenIntegers []int64
	entries      []fakeEntry
	writeErr     error
	syncErr      error
	syncs        int
	withMutates  bool
}

type fakeEntry struct {
	ent    Entry
	fields []Field
}

func (c *fakeCore) Enabled(Level) bool { return c.enabled }

func (c *fakeCore) With(fields []Field) Core {
	if len(fields) > 0 {
		// Record the value as observed before any mutation.
		c.seenIntegers = append(c.seenIntegers, fields[0].Integer)
	}
	c.withFields = append(c.withFields, fields...)
	if c.withMutates && len(fields) > 0 {
		// A misbehaving target that mutates the field slice handed to With.
		fields[0].Integer = fields[0].Integer + 1000
	}
	return c
}

func (c *fakeCore) Check(ent Entry, ce *CheckedEntry) *CheckedEntry {
	if c.enabled {
		return ce.AddCore(ent, c)
	}
	return ce
}

func (c *fakeCore) Write(ent Entry, fields []Field) error {
	c.entries = append(c.entries, fakeEntry{ent: ent, fields: fields})
	return c.writeErr
}

func (c *fakeCore) Sync() error {
	c.syncs++
	return c.syncErr
}

func TestFanoutNoTargets(t *testing.T) {
	t.Parallel()

	assert.Equal(t, NewNopCore(), NewFanout(), "Expected NopCore with no targets.")
}

func TestFanoutNilCorePanics(t *testing.T) {
	t.Parallel()

	assert.Panics(t, func() {
		NewFanout(Target{Name: "a", Core: NewNopCore()}, Target{Name: "b"})
	})
}

func TestFanoutDefaultTargetName(t *testing.T) {
	t.Parallel()

	first, _ := observer.New(DebugLevel)
	failing := &fakeCore{enabled: true, writeErr: errors.New("boom")}
	core := NewFanout(Target{Name: "first", Core: first}, Target{Core: failing})

	err := core.Write(Entry{Level: InfoLevel, Message: "m"}, nil)
	require.Error(t, err)

	var targetErrs TargetErrors
	require.True(t, errors.As(err, &targetErrs))
	require.Len(t, targetErrs, 1)
	assert.Equal(t, "core-1", targetErrs[0].Target)
	assert.Equal(t, WriteStage, targetErrs[0].Stage)
}

// Three fake cores: the second fails Write. The first and third must still
// receive the identical Entry and fields, and the receipt must point exactly
// at the second target.
func TestFanoutWriteOneFailureFansOut(t *testing.T) {
	t.Parallel()

	first := &fakeCore{enabled: true}
	second := &fakeCore{enabled: true, writeErr: errors.New("disk full")}
	third := &fakeCore{enabled: true}
	core := NewFanout(
		Target{Name: "first", Core: first},
		Target{Name: "second", Core: second},
		Target{Name: "third", Core: third},
	)

	ent := Entry{Level: WarnLevel, Message: "log-at-warn"}
	fields := []Field{makeInt64Field("k", 42)}
	err := core.Write(ent, fields)

	require.Error(t, err)
	var targetErrs TargetErrors
	require.True(t, errors.As(err, &targetErrs))
	require.Len(t, targetErrs, 1, "Exactly one target should be reported as failed.")
	assert.Equal(t, TargetError{
		Target: "second",
		Stage:  WriteStage,
		Err:    second.writeErr,
	}, targetErrs[0])
	assert.Contains(t, err.Error(), `fan-out target "second": write: disk full`)
	assert.True(t, errors.Is(err, second.writeErr))

	// The failure must not stop the targets before or after it.
	for i, c := range []*fakeCore{first, second, third} {
		require.Len(t, c.entries, 1, "target %d should receive exactly one entry", i)
		assert.Equal(t, ent, c.entries[0].ent)
		assert.Equal(t, fields, c.entries[0].fields)
	}
}

// Two failing Syncs must both be reported, in target order.
func TestFanoutSyncCollectsAllErrors(t *testing.T) {
	t.Parallel()

	errA := errors.New("sync a failed")
	errC := errors.New("sync c failed")
	first := &fakeCore{enabled: true, syncErr: errA}
	second := &fakeCore{enabled: true}
	third := &fakeCore{enabled: true, syncErr: errC}
	core := NewFanout(
		Target{Name: "first", Core: first},
		Target{Name: "second", Core: second},
		Target{Name: "third", Core: third},
	)

	err := core.Sync()
	require.Error(t, err)

	var targetErrs TargetErrors
	require.True(t, errors.As(err, &targetErrs))
	require.Len(t, targetErrs, 2, "Both failing Syncs must be retained.")
	assert.Equal(t, "first", targetErrs[0].Target)
	assert.Equal(t, SyncStage, targetErrs[0].Stage)
	assert.Equal(t, errA, targetErrs[0].Err)
	assert.Equal(t, "third", targetErrs[1].Target)
	assert.Equal(t, SyncStage, targetErrs[1].Stage)
	assert.Equal(t, errC, targetErrs[1].Err)

	// The successful target in the middle must still have been synced.
	assert.Equal(t, 1, second.syncs)
	assert.Equal(t, 1, first.syncs)
	assert.Equal(t, 1, third.syncs)

	assert.Equal(t, 2, len(targetErrs.ForTarget("first"))+len(targetErrs.ForTarget("third")))
	assert.Nil(t, targetErrs.ForTarget("second"))
}

func TestFanoutWriteAllSuccess(t *testing.T) {
	t.Parallel()

	core := NewFanout(
		Target{Name: "a", Core: &fakeCore{enabled: true}},
		Target{Name: "b", Core: &fakeCore{enabled: true}},
	)
	assert.NoError(t, core.Write(Entry{Level: InfoLevel}, nil))
	assert.NoError(t, core.Sync())
}

// Check must skip targets disabled for the entry level.
func TestFanoutCheckSkipsDisabledTargets(t *testing.T) {
	t.Parallel()

	debugCore, debugLogs := observer.New(DebugLevel)
	warnCore, warnLogs := observer.New(WarnLevel)
	core := NewFanout(
		Target{Name: "debug", Core: debugCore},
		Target{Name: "warn", Core: warnCore},
	)

	infoEnt := Entry{Level: InfoLevel, Message: "log-at-info"}
	if ce := core.Check(infoEnt, nil); ce != nil {
		ce.Write()
	}

	assert.Len(t, debugLogs.All(), 1)
	assert.Len(t, warnLogs.All(), 0, "Disabled target must not receive the entry via Check.")

	assert.True(t, core.Enabled(DebugLevel))
	assert.Equal(t, DebugLevel, LevelOf(core))

	// A fan-out of fully disabled cores enables nothing and Check adds
	// nothing.
	disabled := NewFanout(Target{Name: "off", Core: &fakeCore{enabled: false}})
	assert.False(t, disabled.Enabled(InfoLevel))
	assert.Nil(t, disabled.Check(Entry{Level: InfoLevel}, nil))
}

// With must hand each target an independent copy of the fields: one target
// mutating its slice must not affect the other targets.
func TestFanoutWithCopiesFieldsPerTarget(t *testing.T) {
	t.Parallel()

	mutatorFirst := &fakeCore{enabled: true, withMutates: true}
	middle, middleLogs := observer.New(DebugLevel)
	mutatorLast := &fakeCore{enabled: true, withMutates: true}
	core := NewFanout(
		Target{Name: "mutator-first", Core: mutatorFirst},
		Target{Name: "middle", Core: middle},
		Target{Name: "mutator-last", Core: mutatorLast},
	)

	fields := []Field{makeInt64Field("k", 42)}
	withCore := core.With(fields)

	// The slice passed in must be left untouched.
	assert.Equal(t, int64(42), fields[0].Integer)

	ent := Entry{Level: InfoLevel, Message: "m"}
	if ce := withCore.Check(ent, nil); ce != nil {
		ce.Write()
	}

	// The well-behaved middle target observes the original field.
	logs := middleLogs.All()
	require.Len(t, logs, 1)
	require.Len(t, logs[0].Context, 1)
	assert.Equal(t, makeInt64Field("k", 42), logs[0].Context[0])

	// Both mutating targets observed the original value through their own
	// private copy: if the slices had been shared, the second target would
	// have seen the first target's mutation (1042).
	assert.Equal(t, []int64{42}, mutatorFirst.seenIntegers)
	assert.Equal(t, []int64{42}, mutatorLast.seenIntegers)
}

// With on a fan-out Core must return a new Core and leave the parent
// unchanged: logging through either must not share With-added fields.
func TestFanoutWithDoesNotMutateParent(t *testing.T) {
	t.Parallel()

	parentObs, parentLogs := observer.New(DebugLevel)
	core := NewFanout(Target{Name: "obs", Core: parentObs})

	child := core.With([]Field{makeInt64Field("child", 1)})

	parentEnt := Entry{Level: InfoLevel, Message: "parent"}
	if ce := core.Check(parentEnt, nil); ce != nil {
		ce.Write()
	}
	childEnt := Entry{Level: InfoLevel, Message: "child"}
	if ce := child.Check(childEnt, nil); ce != nil {
		ce.Write()
	}

	all := parentLogs.All()
	require.Len(t, all, 2)
	assert.Equal(t, []Field{}, all[0].Context, "Parent Core must not gain child fields.")
	assert.Equal(t, []Field{makeInt64Field("child", 1)}, all[1].Context)
}

// NewTee keeps its historical behavior: errors are joined (not attributed by
// target), and the single/no input shortcuts are preserved.
func TestNewTeeBehaviorUnchanged(t *testing.T) {
	t.Parallel()

	errA := errors.New("a")
	errB := errors.New("b")
	tee := NewTee(
		&fakeCore{enabled: true, syncErr: errA},
		&fakeCore{enabled: true, syncErr: errB},
	)
	err := tee.Sync()
	assert.ErrorIs(t, err, errA)
	assert.ErrorIs(t, err, errB)

	var targetErrs TargetErrors
	assert.False(t, errors.As(err, &targetErrs), "NewTee must not return the new TargetErrors type.")

	single := NewNopCore()
	assert.Equal(t, single, NewTee(single))
	assert.Equal(t, NewNopCore(), NewTee())
}

func TestTargetErrorsForTarget(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("sentinel")
	errs := TargetErrors{
		{Target: "a", Stage: WriteStage, Err: errors.New("w")},
		{Target: "b", Stage: SyncStage, Err: sentinel},
	}
	require.Len(t, errs.ForTarget("b"), 1)
	assert.Equal(t, SyncStage, errs.ForTarget("b")[0].Stage)
	assert.True(t, errors.Is(errs, sentinel))
	assert.False(t, errors.Is(errs, errors.New("absent")))
	assert.Equal(t,
		errs[0].Error()+"\n"+errs[1].Error(),
		errs.Error())
	assert.True(t, strings.Contains(errs.Error(), `target "a": write`))
	assert.True(t, strings.Contains(errs.Error(), `target "b": sync`))
}

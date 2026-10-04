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

package zapcore

import (
	"fmt"

	"go.uber.org/multierr"
)

// Stage identifies the operation that produced an error from a target.
type Stage uint8

const (
	// StageWrite indicates that an error occurred during Write.
	StageWrite Stage = iota
	// StageSync indicates that an error occurred during Sync.
	StageSync
)

// String returns the lower-case name of the stage.
func (s Stage) String() string {
	switch s {
	case StageWrite:
		return "write"
	case StageSync:
		return "sync"
	default:
		return "unknown"
	}
}

// Target pairs a zapcore.Core with a human-readable name. The name is used
// to identify the target in per-target error reports.
type Target struct {
	// Name is a human-readable identifier for the target. It appears in
	// error messages and is useful for distinguishing multiple targets of
	// the same type.
	Name string
	// Core is the underlying zapcore.Core that receives log entries.
	Core Core
}

// TargetError is an error from a specific target and stage. It wraps the
// underlying error so callers can identify which target and operation failed.
type TargetError struct {
	// Name is the target's name.
	Name string
	// Stage is the operation that failed (StageWrite or StageSync).
	Stage Stage
	// Err is the underlying error returned by the target.
	Err error
}

func (e *TargetError) Error() string {
	return fmt.Sprintf("target %q %s: %v", e.Name, e.Stage, e.Err)
}

// Unwrap returns the underlying error, so errors.Is and errors.As work as
// expected.
func (e *TargetError) Unwrap() error { return e.Err }

// TargetResult is the outcome of an operation on a single target.
type TargetResult struct {
	// Name is the target's name.
	Name string
	// Stage is the operation that was performed.
	Stage Stage
	// Err is the error from the operation, or nil if it succeeded.
	Err error
}

// OK reports whether the target succeeded (Err is nil).
func (r TargetResult) OK() bool { return r.Err == nil }

// FanoutCore is a Core that delivers log entries to multiple named targets
// and returns per-target results. Unlike NewTee, which silently aggregates
// errors with multierr, FanoutCore lets callers inspect each target's Write
// and Sync results individually.
//
// FanoutCore does not cache or replay log entries, and it does not close
// any targets. Target lifetime (including closing) is the responsibility of
// the caller.
type FanoutCore interface {
	Core
	// WriteResults writes the entry and fields to every target and returns
	// the per-target results in target order.
	WriteResults(Entry, []Field) []TargetResult
	// SyncResults syncs every target and returns the per-target results in
	// target order.
	SyncResults() []TargetResult
}

type fanoutCore struct {
	targets []Target
}

var (
	_ FanoutCore     = (*fanoutCore)(nil)
	_ leveledEnabler = (*fanoutCore)(nil)
)

// NewFanout creates a FanoutCore that delivers log entries to the given
// named targets. If no targets are provided, it returns a no-op Core.
//
// Unlike NewTee, NewFanout returns per-target results from WriteResults and
// SyncResults, and the errors returned by Write and Sync carry the target
// name and stage.
func NewFanout(targets ...Target) FanoutCore {
	if len(targets) == 0 {
		return &fanoutCore{}
	}
	return &fanoutCore{targets: targets}
}

func (f *fanoutCore) Level() Level {
	if len(f.targets) == 0 {
		return InvalidLevel
	}
	minLvl := _maxLevel
	for i := range f.targets {
		if lvl := LevelOf(f.targets[i].Core); lvl < minLvl {
			minLvl = lvl
		}
	}
	return minLvl
}

func (f *fanoutCore) Enabled(lvl Level) bool {
	for i := range f.targets {
		if f.targets[i].Core.Enabled(lvl) {
			return true
		}
	}
	return false
}

func (f *fanoutCore) Check(ent Entry, ce *CheckedEntry) *CheckedEntry {
	for i := range f.targets {
		ce = f.targets[i].Core.Check(ent, ce)
	}
	return ce
}

func (f *fanoutCore) With(fields []Field) Core {
	clone := &fanoutCore{targets: make([]Target, len(f.targets))}
	for i := range f.targets {
		// Copy fields per target so that one encoder consuming (or
		// mutating) the fields cannot affect another target.
		fieldsCopy := append([]Field(nil), fields...)
		clone.targets[i] = Target{
			Name: f.targets[i].Name,
			Core: f.targets[i].Core.With(fieldsCopy),
		}
	}
	return clone
}

func (f *fanoutCore) Write(ent Entry, fields []Field) error {
	return combineResults(f.WriteResults(ent, fields))
}

func (f *fanoutCore) WriteResults(ent Entry, fields []Field) []TargetResult {
	results := make([]TargetResult, len(f.targets))
	for i := range f.targets {
		results[i] = TargetResult{
			Name:  f.targets[i].Name,
			Stage: StageWrite,
			Err:   f.targets[i].Core.Write(ent, fields),
		}
	}
	return results
}

func (f *fanoutCore) Sync() error {
	return combineResults(f.SyncResults())
}

func (f *fanoutCore) SyncResults() []TargetResult {
	results := make([]TargetResult, len(f.targets))
	for i := range f.targets {
		results[i] = TargetResult{
			Name:  f.targets[i].Name,
			Stage: StageSync,
			Err:   f.targets[i].Core.Sync(),
		}
	}
	return results
}

// combineResults aggregates per-target errors into a single multierr error,
// wrapping each error with its target name and stage. Errors are appended in
// target order.
func combineResults(results []TargetResult) error {
	var err error
	for _, r := range results {
		if r.Err != nil {
			err = multierr.Append(err, &TargetError{
				Name:  r.Name,
				Stage: r.Stage,
				Err:   r.Err,
			})
		}
	}
	return err
}

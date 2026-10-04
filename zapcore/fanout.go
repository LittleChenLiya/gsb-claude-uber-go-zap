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

package zapcore

import (
	"errors"
	"fmt"
	"strings"
)

// Stage describes the operation a fan-out target was performing when an
// error occurred.
type Stage string

const (
	// WriteStage indicates that the error happened while writing an Entry.
	WriteStage Stage = "write"
	// SyncStage indicates that the error happened while syncing a Core.
	SyncStage Stage = "sync"
)

// Target pairs a Core with a human-readable name used to attribute errors
// returned by a fan-out Core.
type Target struct {
	// Name identifies the target in the errors returned by the fan-out Core.
	// If empty, a name of the form "core-N" is assigned.
	Name string

	// Core receives a copy of every Entry the fan-out Core is asked to write.
	Core Core
}

// NewFanout creates a Core that duplicates log entries into two or more
// named target Cores.
//
// Unlike [NewTee], a failure of one target never prevents the remaining
// targets from being written or synced: every target is always attempted,
// and the returned error is a [TargetErrors] value describing every failure
// (with the failing target name and the [Stage] at which it failed), never
// just the last error.
//
// Fields added with [Core.With] are copied independently into every target,
// so one target consuming or mutating its encoder state cannot affect the
// others.
//
// The fan-out Core never caches or replays entries and never closes its
// targets; lifecycle management of the underlying Cores is left to the code
// that owns them.
//
// Calling it with no targets returns a no-op Core.
func NewFanout(targets ...Target) Core {
	if len(targets) == 0 {
		return NewNopCore()
	}
	cores := make([]fanoutTarget, len(targets))
	for i := range targets {
		if targets[i].Core == nil {
			panic(fmt.Sprintf("zapcore: fan-out target %d has a nil Core", i))
		}
		name := targets[i].Name
		if name == "" {
			name = fmt.Sprintf("core-%d", i)
		}
		cores[i] = fanoutTarget{name: name, core: targets[i].Core}
	}
	return fanoutCore(cores)
}

type fanoutTarget struct {
	name string
	core Core
}

type fanoutCore []fanoutTarget

var (
	_ leveledEnabler = fanoutCore(nil)
	_ Core           = fanoutCore(nil)
)

// With returns a new fan-out Core in which every target carries its own copy
// of the given fields. The receiver (the parent Core) is never modified.
func (fc fanoutCore) With(fields []Field) Core {
	clone := make(fanoutCore, len(fc))
	for i := range fc {
		// Hand each target an independent copy of the fields so one target
		// consuming them cannot change what the others see.
		fieldsCopy := make([]Field, len(fields))
		copy(fieldsCopy, fields)
		clone[i] = fanoutTarget{
			name: fc[i].name,
			core: fc[i].core.With(fieldsCopy),
		}
	}
	return clone
}

func (fc fanoutCore) Level() Level {
	minLvl := _maxLevel // fc is never empty
	for i := range fc {
		if lvl := LevelOf(fc[i].core); lvl < minLvl {
			minLvl = lvl
		}
	}
	return minLvl
}

func (fc fanoutCore) Enabled(lvl Level) bool {
	for i := range fc {
		if fc[i].core.Enabled(lvl) {
			return true
		}
	}
	return false
}

// Check only adds targets that are enabled for the entry's level, so
// disabled targets never receive the entry.
func (fc fanoutCore) Check(ent Entry, ce *CheckedEntry) *CheckedEntry {
	for i := range fc {
		if fc[i].core.Enabled(ent.Level) {
			ce = fc[i].core.Check(ent, ce)
		}
	}
	return ce
}

// Write writes the entry and fields to every target regardless of whether
// earlier targets failed. Errors are collected in target order.
func (fc fanoutCore) Write(ent Entry, fields []Field) error {
	var errs TargetErrors
	for i := range fc {
		if err := fc[i].core.Write(ent, fields); err != nil {
			errs = append(errs, TargetError{
				Target: fc[i].name,
				Stage:  WriteStage,
				Err:    err,
			})
		}
	}
	return errs.nilIfEmpty()
}

// Sync syncs every target regardless of whether earlier targets failed.
// Errors are collected in target order.
func (fc fanoutCore) Sync() error {
	var errs TargetErrors
	for i := range fc {
		if err := fc[i].core.Sync(); err != nil {
			errs = append(errs, TargetError{
				Target: fc[i].name,
				Stage:  SyncStage,
				Err:    err,
			})
		}
	}
	return errs.nilIfEmpty()
}

// TargetError describes a single failure of one fan-out target.
type TargetError struct {
	// Target is the Name of the target that failed.
	Target string
	// Stage is the operation that failed: [WriteStage] or [SyncStage].
	Stage Stage
	// Err is the error returned by the target.
	Err error
}

func (e TargetError) Error() string {
	return fmt.Sprintf("fan-out target %q: %s: %v", e.Target, e.Stage, e.Err)
}

// Unwrap returns the underlying target error so that errors.Is and
// errors.As reach it.
func (e TargetError) Unwrap() error { return e.Err }

// TargetErrors is the list of per-target failures returned by a fan-out Core.
// It is ordered by target position, only contains targets that failed, and is
// nil when every target succeeded.
type TargetErrors []TargetError

func (errs TargetErrors) Error() string {
	var sb strings.Builder
	for i := range errs {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(errs[i].Error())
	}
	return sb.String()
}

// Unwrap returns the wrapped target errors. It enables errors.Is and
// errors.As on joined errors on Go versions that support it.
func (errs TargetErrors) Unwrap() []error { return errs.unwrap() }

// Is reports whether any target error in the list matches target.
func (errs TargetErrors) Is(target error) bool {
	for i := range errs {
		if errors.Is(errs[i].Err, target) {
			return true
		}
	}
	return false
}

// As finds the first target error in the list that matches target, and sets
// *target to that error value.
func (errs TargetErrors) As(target any) bool {
	for i := range errs {
		if errors.As(errs[i].Err, target) {
			return true
		}
	}
	return false
}

// ForTarget returns the errors attributed to the named target in the order in
// which the targets were attempted. It returns nil if the target did not
// fail.
func (errs TargetErrors) ForTarget(name string) []TargetError {
	var out []TargetError
	for i := range errs {
		if errs[i].Target == name {
			out = append(out, errs[i])
		}
	}
	return out
}

func (errs TargetErrors) unwrap() []error {
	out := make([]error, len(errs))
	for i := range errs {
		out[i] = errs[i]
	}
	return out
}

func (errs TargetErrors) nilIfEmpty() error {
	if len(errs) == 0 {
		return nil
	}
	return errs
}

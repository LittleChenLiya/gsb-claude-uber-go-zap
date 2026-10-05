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

package zap

import (
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	userpkg "go.uber.org/zap/internal/callerbound/user"
	userxpkg "go.uber.org/zap/internal/callerbound/userx"
	wrappkg "go.uber.org/zap/internal/callerbound/wrap"
	"go.uber.org/zap/internal/stacktrace"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

const (
	wrapPkg  = "go.uber.org/zap/internal/callerbound/wrap"
	userPkg  = "go.uber.org/zap/internal/callerbound/user"
	userxPkg = "go.uber.org/zap/internal/callerbound/userx"
)

// thisCaller returns the file, line, and function of its caller.
func thisCaller(t *testing.T) (file string, line int, function string) {
	t.Helper()
	pc, file, line, ok := runtime.Caller(1)
	require.True(t, ok, "runtime.Caller failed")
	fn := runtime.FuncForPC(pc)
	require.NotNil(t, fn, "FuncForPC failed")
	return file, line, fn.Name()
}

func TestCallerBoundaryWrapperDepths(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	logger := New(core, AddCaller(), WithCallerBoundary(wrapPkg))

	file, line1, function := thisCaller(t)
	wrappkg.Info1(logger, "one layer")
	_, line3, _ := thisCaller(t)
	wrappkg.Info3(logger, "three layers")

	entries := logs.All()
	require.Len(t, entries, 2)

	for i, wantLine := range []int{line1 + 1, line3 + 1} {
		caller := entries[i].Caller
		assert.True(t, caller.Defined, "entry %d: caller must be defined", i)
		assert.Equal(t, function, caller.Function, "entry %d: caller must be the business call site", i)
		assert.Equal(t, file, caller.File, "entry %d: caller file", i)
		assert.Equal(t, wantLine, caller.Line, "entry %d: caller must point at the business call line", i)
	}
}

func TestCallerBoundaryExactPackageMatch(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	// Register only "user"; the "userx" package shares a prefix but must
	// not be treated as a wrapper.
	logger := New(core, AddCaller(), WithCallerBoundary(userPkg))

	userxpkg.Log(logger, "prefix must not match")

	entries := logs.All()
	require.Len(t, entries, 1)
	caller := entries[0].Caller
	assert.True(t, caller.Defined)
	assert.Equal(t, userxPkg+".Log", caller.Function,
		"exact boundary matching must not skip userX frames")
}

func TestCallerBoundaryNoBusinessFrameIsUndefined(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	// Register every package on the call stack as a wrapper so that no
	// business frame remains.
	logger := New(core, AddCaller(),
		WithCallerBoundary("go.uber.org/zap", "testing", "runtime"))

	logger.Info("no business frame")

	entries := logs.All()
	require.Len(t, entries, 1)
	assert.False(t, entries[0].Caller.Defined,
		"caller must be undefined when no business frame is found")
}

func TestCallerBoundaryStacktraceStartsAtCaller(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	logger := New(core,
		AddCaller(),
		AddStacktrace(zapcore.InfoLevel),
		WithCallerBoundary(wrapPkg),
	)

	wrappkg.Info3(logger, "stack and caller share a start")

	entries := logs.All()
	require.Len(t, entries, 1)
	entry := entries[0]
	require.True(t, entry.Caller.Defined)
	require.NotEmpty(t, entry.Stack)

	firstFrame, _, _ := strings.Cut(entry.Stack, "\n")
	assert.Equal(t, entry.Caller.Function, firstFrame,
		"stack trace must start at the same frame as the reported caller")
	assert.NotContains(t, entry.Stack, wrapPkg,
		"stack trace must not contain skipped wrapper frames")
}

func TestCallerBoundaryDisabledCallerCapturesNothing(t *testing.T) {
	var captures int
	orig := captureStack
	captureStack = func(skip int, depth stacktrace.Depth) *stacktrace.Stack {
		captures++
		return orig(skip, depth)
	}
	defer func() { captureStack = orig }()

	core, logs := observer.New(zapcore.InfoLevel)

	// Caller annotation disabled: the boundary policy must not cause any
	// stack collection.
	logger := New(core, WithCallerBoundary(wrapPkg))
	wrappkg.Info1(logger, "no caller, no capture")
	require.Equal(t, 1, logs.Len())
	assert.Equal(t, 0, captures, "disabled caller must not collect stacks")

	// Caller annotation enabled: exactly one capture per log call.
	logger = logger.WithOptions(AddCaller())
	wrappkg.Info1(logger, "caller enabled")
	assert.Equal(t, 1, captures)
}

func TestCallerBoundaryCloneInheritsOwnPolicy(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	base := New(core, AddCaller(), WithCallerBoundary(wrapPkg))

	// The clone extends its own copy of the policy; the base logger must
	// be unaffected.
	clone := base.WithOptions(WithCallerBoundary(userPkg))
	// A clone of the clone inherits the clone's policy unchanged.
	grandchild := clone.WithOptions()

	// base: "user" is not a registered wrapper, so userpkg.Log is the caller.
	userpkg.Log(base, "base")

	// clone and grandchild: "user" is registered, so the caller is the
	// business call site in this test.
	_, lineClone, function := thisCaller(t)
	userpkg.Log(clone, "clone")
	_, lineGrandchild, _ := thisCaller(t)
	userpkg.Log(grandchild, "grandchild")

	entries := logs.All()
	require.Len(t, entries, 3)

	assert.Equal(t, userPkg+".Log", entries[0].Caller.Function,
		"base logger policy must not gain the clone's wrapper packages")

	assert.Equal(t, function, entries[1].Caller.Function,
		"clone must apply its own extended policy")
	assert.Equal(t, lineClone+1, entries[1].Caller.Line)

	assert.Equal(t, function, entries[2].Caller.Function,
		"clone of a clone must inherit the policy")
	assert.Equal(t, lineGrandchild+1, entries[2].Caller.Line)
}

func TestCallerBoundaryDefaultSkipUnchanged(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	// Without a boundary policy, the default fixed-skip rules apply: the
	// wrapper itself is reported as the caller.
	logger := New(core, AddCaller())

	wrappkg.Info1(logger, "default behavior")

	entries := logs.All()
	require.Len(t, entries, 1)
	assert.Equal(t, wrapPkg+".Info1", entries[0].Caller.Function,
		"default skip rules must be unchanged without a boundary policy")
}

func TestPackagePath(t *testing.T) {
	tests := map[string]string{
		"go.uber.org/zap.(*Logger).Info":                  "go.uber.org/zap",
		"go.uber.org/zap/internal/callerbound/wrap.Info1": wrapPkg,
		"go.uber.org/zap.TestPackagePath":                 "go.uber.org/zap",
		"runtime.main":                                    "runtime",
		"runtime.goexit":                                  "runtime",
		"main.main":                                       "main",
	}
	for funcName, want := range tests {
		assert.Equal(t, want, packagePath(funcName), "packagePath(%q)", funcName)
	}
}

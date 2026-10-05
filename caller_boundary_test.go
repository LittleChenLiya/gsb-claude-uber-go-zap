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

package zap_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.uber.org/zap"
	"go.uber.org/zap/internal/testpkg/user"
	"go.uber.org/zap/internal/testpkg/userx"
	"go.uber.org/zap/internal/testpkg/wrapper"
	"go.uber.org/zap/internal/testpkg/wrapper1"
	"go.uber.org/zap/zaptest/observer"
)

// testPkg is the import path of the test package itself.
const testPkg = "go.uber.org/zap_test"

// fixture packages used by the boundary tests.
const (
	wrapperPkg  = "go.uber.org/zap/internal/testpkg/wrapper"
	wrapper1Pkg = "go.uber.org/zap/internal/testpkg/wrapper1"
	wrapper2Pkg = "go.uber.org/zap/internal/testpkg/wrapper2"
	wrapper3Pkg = "go.uber.org/zap/internal/testpkg/wrapper3"
	userPkg     = "go.uber.org/zap/internal/testpkg/user"
	userxPkg    = "go.uber.org/zap/internal/testpkg/userx"
)

// newBoundaryLogger builds a logger with the given options and returns it
// together with the observed logs.
func newBoundaryLogger(t *testing.T, opts ...zap.Option) (*zap.Logger, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zap.DebugLevel)
	return zap.New(core, opts...), logs
}

// TestCallerBoundaryOneLayer verifies that a single-layer wrapper is skipped
// and the caller points to the business call site.
func TestCallerBoundaryOneLayer(t *testing.T) {
	log, logs := newBoundaryLogger(t,
		zap.AddCaller(),
		zap.AddCallerSkipPackage(wrapperPkg),
	)

	wrapper.LogInfo(log, "hello")

	entries := logs.AllUntimed()
	require.Len(t, entries, 1, "expected one log entry")
	caller := entries[0].Caller
	assert.True(t, caller.Defined, "caller should be defined")
	assert.Contains(t, caller.Function, "TestCallerBoundaryOneLayer",
		"caller should point to the business test function, not the wrapper")
	assert.NotContains(t, caller.Function, "wrapper.LogInfo",
		"caller should not point to the wrapper function")
}

// TestCallerBoundaryThreeLayers verifies that a three-layer wrapper stack is
// skipped and the caller points to the business call site.
func TestCallerBoundaryThreeLayers(t *testing.T) {
	log, logs := newBoundaryLogger(t,
		zap.AddCaller(),
		zap.AddCallerSkipPackage(wrapper1Pkg, wrapper2Pkg, wrapper3Pkg),
	)

	wrapper1.LogInfo(log, "hello")

	entries := logs.AllUntimed()
	require.Len(t, entries, 1, "expected one log entry")
	caller := entries[0].Caller
	assert.True(t, caller.Defined, "caller should be defined")
	assert.Contains(t, caller.Function, "TestCallerBoundaryThreeLayers",
		"caller should point to the business test function, not the wrapper stack")
	assert.NotContains(t, caller.Function, "wrapper1",
		"caller should not point to any wrapper function")
}

// TestCallerBoundaryPackageMatch verifies that package matching is performed
// at package boundaries. Registering "user" should skip frames from "user"
// but not from "userx".
func TestCallerBoundaryPackageMatch(t *testing.T) {
	log, logs := newBoundaryLogger(t,
		zap.AddCaller(),
		zap.AddCallerSkipPackage(userPkg),
	)

	// user.LogInfo should be skipped; the caller should be this test.
	user.LogInfo(log, "from user")
	// userx.LogInfo should NOT be skipped; the caller should be userx.LogInfo.
	userx.LogInfo(log, "from userx")

	entries := logs.AllUntimed()
	require.Len(t, entries, 2, "expected two log entries")

	// First entry: user package was skipped, caller is the test function.
	assert.Contains(t, entries[0].Caller.Function, "TestCallerBoundaryPackageMatch",
		"user package should be skipped; caller should be the test function")

	// Second entry: userx package was NOT skipped, caller is userx.LogInfo.
	assert.Contains(t, entries[1].Caller.Function, "userx.LogInfo",
		"userx package should not be skipped when user is registered")
}

// TestCallerBoundaryDisabled verifies that when caller annotation is disabled,
// no caller is collected (collection count is zero).
func TestCallerBoundaryDisabled(t *testing.T) {
	log, logs := newBoundaryLogger(t,
		zap.AddCaller(),
		zap.WithCaller(false),
		zap.AddCallerSkipPackage(wrapperPkg),
	)

	wrapper.LogInfo(log, "hello")

	entries := logs.AllUntimed()
	require.Len(t, entries, 1, "expected one log entry")
	assert.False(t, entries[0].Caller.Defined,
		"caller should be undefined when caller annotation is disabled")
}

// TestCallerBoundaryClone verifies that cloned loggers inherit the caller
// boundary strategy.
func TestCallerBoundaryClone(t *testing.T) {
	log, logs := newBoundaryLogger(t,
		zap.AddCaller(),
		zap.AddCallerSkipPackage(wrapperPkg),
	)

	// Clone via WithOptions.
	cloned := log.WithOptions(zap.Fields(zap.String("k", "v")))
	wrapper.LogInfo(cloned, "hello")

	// Clone via With.
	cloned2 := log.With(zap.String("k2", "v2"))
	wrapper.LogInfo(cloned2, "hello2")

	// Clone via Named.
	cloned3 := log.Named("child")
	wrapper.LogInfo(cloned3, "hello3")

	// Clone via Sugar.
	sugared := log.Sugar()
	sugared.Infow("hello4")

	entries := logs.AllUntimed()
	require.Len(t, entries, 4, "expected four log entries")
	for i, e := range entries {
		assert.True(t, e.Caller.Defined, "entry %d: caller should be defined", i)
		assert.Contains(t, e.Caller.Function, "TestCallerBoundaryClone",
			"entry %d: caller should point to the test function", i)
	}
}

// TestCallerBoundaryStackConsistency verifies that the stacktrace starts from
// the same frame as the caller.
func TestCallerBoundaryStackConsistency(t *testing.T) {
	log, logs := newBoundaryLogger(t,
		zap.AddCaller(),
		zap.AddStacktrace(zap.DebugLevel),
		zap.AddCallerSkipPackage(wrapperPkg),
	)

	wrapper.LogInfo(log, "hello")

	entries := logs.AllUntimed()
	require.Len(t, entries, 1, "expected one log entry")
	caller := entries[0].Caller
	assert.True(t, caller.Defined, "caller should be defined")

	// The stacktrace should start from the same frame as the caller.
	stack := entries[0].Stack
	require.NotEmpty(t, stack, "stacktrace should not be empty")
	firstLine := strings.SplitN(stack, "\n", 2)[0]
	assert.Contains(t, firstLine, caller.Function,
		"stacktrace should start from the same frame as the caller")
}

// TestCallerBoundaryNoBoundary verifies that without a boundary registered,
// the caller behaves as before (points to the wrapper).
func TestCallerBoundaryNoBoundary(t *testing.T) {
	log, logs := newBoundaryLogger(t, zap.AddCaller())

	wrapper.LogInfo(log, "hello")

	entries := logs.AllUntimed()
	require.Len(t, entries, 1, "expected one log entry")
	caller := entries[0].Caller
	assert.True(t, caller.Defined, "caller should be defined")
	// Without a boundary, the caller points to the wrapper (existing behavior).
	assert.Contains(t, caller.Function, "wrapper.LogInfo",
		"without a boundary, caller should point to the wrapper")
}

// TestCallerBoundaryNoCandidate verifies that when every frame in the stack
// matches a registered wrapper package, the caller is marked undefined.
func TestCallerBoundaryNoCandidate(t *testing.T) {
	log, logs := newBoundaryLogger(t,
		zap.AddCaller(),
		// Register every package that appears in the stack so that no
		// business frame remains.
		zap.AddCallerSkipPackage(
			wrapperPkg,
			testPkg,
			"testing",
			"runtime",
		),
	)

	wrapper.LogInfo(log, "hello")

	entries := logs.AllUntimed()
	require.Len(t, entries, 1, "expected one log entry")
	assert.False(t, entries[0].Caller.Defined,
		"caller should be undefined when no business frame is found")
}

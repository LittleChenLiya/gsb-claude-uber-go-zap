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

	"go.uber.org/zap/internal/stacktrace"
)

// callerBoundary is an explicit caller-boundary policy.
//
// # Policy
//
// Instead of guessing the depth of logging wrappers with a fixed
// AddCallerSkip, the policy registers the package paths of wrapper
// packages. When caller annotation runs, zap walks the real runtime
// frames of the current goroutine -- inlined frames included, exactly as
// runtime.CallersFrames expands them -- and skips every frame whose
// function belongs to a registered wrapper package. The first remaining
// frame is the business call site reported as the caller, and it is also
// the frame at which the stack trace (if enabled) starts.
//
// Package matching is by exact package path: registering
// "example.com/user" does not match frames from "example.com/userX".
//
// If no business frame is found (every frame on the stack belongs to a
// registered wrapper package), the caller is reported as undefined and
// the stack trace is left empty.
//
// The policy belongs to the Logger it was applied to: WithOptions and
// other cloning operations inherit a copy of it, and later changes to
// one Logger never affect another. When caller annotation is disabled,
// the policy is inert and causes no additional stack collection.
type callerBoundary struct {
	// wrappers is the set of exact package paths treated as logging
	// wrappers. It is never mutated after construction; options that
	// extend the policy copy it first (copy-on-write), so a policy is
	// safe to share between a Logger and its clones.
	wrappers map[string]struct{}
}

// isWrapper reports whether the named function belongs to a registered
// wrapper package. funcName is a fully-qualified function name as
// reported by runtime.Frame.Function, e.g.
// "go.uber.org/zap/internal/callerbound/wrap.Info1".
func (b *callerBoundary) isWrapper(funcName string) bool {
	_, ok := b.wrappers[packagePath(funcName)]
	return ok
}

// firstBusinessFrame advances through the captured stack, skipping frames
// from registered wrapper packages, and returns the first business frame
// along with whether more frames follow it. The third return value
// reports whether any business frame was found at all.
//
// Frames are consumed from the supplied stack, so upon return the stack
// is positioned right after the returned frame -- formatting the
// remainder of the stack yields a trace that starts at the same frame as
// the reported caller.
func (b *callerBoundary) firstBusinessFrame(
	stack *stacktrace.Stack,
	frame runtime.Frame,
	more bool,
) (runtime.Frame, bool, bool) {
	for {
		if !b.isWrapper(frame.Function) {
			return frame, more, true
		}
		if !more {
			return runtime.Frame{}, false, false
		}
		frame, more = stack.Next()
	}
}

// packagePath extracts the package path from a fully-qualified function
// name as reported by runtime.Frame.Function.
//
// For example:
//
//	"go.uber.org/zap.(*Logger).Info"                  -> "go.uber.org/zap"
//	"go.uber.org/zap/internal/callerbound/wrap.Info1" -> "go.uber.org/zap/internal/callerbound/wrap"
//	"runtime.main"                                    -> "runtime"
//
// The comparison is an exact match on the whole path, so a registered
// "example.com/user" never matches functions from "example.com/userX".
func packagePath(funcName string) string {
	// The package path ends at the first dot after the final slash: the
	// path itself may contain dots (e.g. "go.uber.org"), but the segment
	// after the last slash is "pkg.Func" or "pkg.(*T).Method".
	lastSlash := strings.LastIndexByte(funcName, '/')
	dot := strings.IndexByte(funcName[lastSlash+1:], '.')
	if dot < 0 {
		return funcName
	}
	return funcName[:lastSlash+1+dot]
}

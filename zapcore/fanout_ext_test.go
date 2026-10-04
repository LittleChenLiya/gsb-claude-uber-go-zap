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
	"fmt"

	"go.uber.org/zap/internal/ztest"
	//revive:disable:dot-imports
	. "go.uber.org/zap/zapcore"
)

// ExampleNewFanout shows how to fan a single entry out to several named
// targets and inspect the per-target write and sync receipts.
func ExampleNewFanout() {
	newCore := func(ws WriteSyncer) Core {
		return NewCore(NewJSONEncoder(EncoderConfig{}), ws, DebugLevel)
	}

	stdout := newCore(&ztest.Discarder{})
	broken := newCore(&ztest.FailWriter{}) // every Write fails
	stderrSink := &ztest.Discarder{}
	stderrSink.SetError(errors.New("sync unavailable"))
	stderr := newCore(stderrSink)

	core := NewFanout(
		Target{Name: "stdout", Core: stdout},
		Target{Name: "broken", Core: broken},
		Target{Name: "stderr", Core: stderr},
	)

	// One failing target does not stop the others; the returned error
	// identifies each failing target and the stage at which it failed.
	if err := core.Write(
		Entry{Level: ErrorLevel, Message: "disk nearly full"},
		[]Field{makeInt64Field("remaining_bytes", 1)},
	); err != nil {
		var targetErrs TargetErrors
		if errors.As(err, &targetErrs) {
			for _, te := range targetErrs {
				fmt.Printf("%s failed during %s: %v\n", te.Target, te.Stage, te.Err)
			}
			// Inspect a specific target directly.
			if errs := targetErrs.ForTarget("broken"); len(errs) > 0 {
				fmt.Printf("target broken failed %d time(s) on write\n", len(errs))
			}
		}
	}

	// Sync collects failures from every target without aborting early.
	if err := core.Sync(); err != nil {
		var targetErrs TargetErrors
		if errors.As(err, &targetErrs) {
			fmt.Printf("%d target(s) failed to sync\n", len(targetErrs))
			for _, te := range targetErrs {
				fmt.Printf("  - %s: %v\n", te.Target, te.Err)
			}
		}
	}

	// Output:
	// broken failed during write: failed
	// target broken failed 1 time(s) on write
	// 1 target(s) failed to sync
	//   - stderr: sync unavailable
}

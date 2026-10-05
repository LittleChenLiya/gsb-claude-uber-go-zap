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

package zapcore_test

import (
	"fmt"
	"time"

	//revive:disable:dot-imports
	. "go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// ExampleSamplerClock drives a sampler with an injected, controllable clock
// (no real sleeps) and reports what happens to ten identical events in one
// time bucket: the first two are logged, then every fourth. Rolling the wall
// clock backwards does not reset the counts; only a genuinely new bucket does.
func ExampleSamplerClock() {
	tick := time.Second
	clock := newControllableClock()

	core, logs := observer.New(DebugLevel)
	sampler := NewSamplerWithOptions(
		core,
		tick,
		2, // first two entries in a bucket are logged
		4, // ...then every fourth (n = 6, 10, 14, ...)
		SamplerClock(clock),
	)

	for i := 1; i <= 10; i++ {
		ent := Entry{Level: InfoLevel, Message: "same message"}
		if ce := sampler.Check(ent, nil); ce != nil {
			ce.Write()
			fmt.Printf("event %d: logged\n", i)
		} else {
			fmt.Printf("event %d: dropped\n", i)
		}
	}

	// Roll the wall clock backwards inside the same bucket. The counter keeps
	// accumulating (these are events 11 and 12) rather than restarting, so the
	// first two entries are not granted again: neither n=11 nor n=12 matches
	// the every-fourth frequency ((11-2)%4 = 1, (12-2)%4 = 2).
	clock.add(-tick / 2)
	for i := 11; i <= 12; i++ {
		if ce := sampler.Check(Entry{Level: InfoLevel, Message: "same message"}, nil); ce != nil {
			ce.Write()
			fmt.Printf("event %d: logged\n", i)
		} else {
			fmt.Printf("event %d: dropped\n", i)
		}
	}

	// Advance to a genuinely new bucket (past the original boundary despite
	// the rollback): stale per-(level, message) state is cleared and counting
	// starts over, so the first entry of the new bucket is logged.
	clock.add(3 * tick)
	if ce := sampler.Check(Entry{Level: InfoLevel, Message: "same message"}, nil); ce != nil {
		ce.Write()
		fmt.Println("event 13: logged in new bucket")
	} else {
		fmt.Println("event 13: dropped")
	}

	fmt.Println("entries ultimately written:", logs.Len())

	// Output:
	// event 1: logged
	// event 2: logged
	// event 3: dropped
	// event 4: dropped
	// event 5: dropped
	// event 6: logged
	// event 7: dropped
	// event 8: dropped
	// event 9: dropped
	// event 10: logged
	// event 11: dropped
	// event 12: dropped
	// event 13: logged in new bucket
	// entries ultimately written: 5
}

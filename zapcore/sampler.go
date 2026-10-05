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

package zapcore

import (
	"sync/atomic"
	"time"
)

const (
	_numLevels        = _maxLevel - _minLevel + 1
	_countersPerLevel = 4096
)

type counter struct {
	// bucket is the time bucket this counter currently belongs to, counted
	// from the sampler's start time. It only ever moves forward, so a clock
	// rollback leaves the in-flight bucket's count intact.
	bucket  atomic.Int64
	counter atomic.Uint64
}

type counters [_numLevels][_countersPerLevel]counter

func newCounters() *counters {
	return &counters{}
}

func (cs *counters) get(lvl Level, key string) *counter {
	i := lvl - _minLevel
	j := fnv32a(key) % _countersPerLevel
	return &cs[i][j]
}

// fnv32a, adapted from "hash/fnv", but without a []byte(string) alloc
func fnv32a(s string) uint32 {
	const (
		offset32 = 2166136261
		prime32  = 16777619
	)
	hash := uint32(offset32)
	for i := 0; i < len(s); i++ {
		hash ^= uint32(s[i])
		hash *= prime32
	}
	return hash
}

// IncCheckReset increments the counter for the given time bucket, lazily
// resetting it when the bucket has advanced since the last event.
//
// Buckets are numbered relative to the sampler's start time. A counter only
// ever moves forward to a strictly greater bucket: if the injected clock jumps
// backwards (for example, because the wall clock is adjusted), stale state is
// retained and the current bucket's count keeps accumulating, so a clock
// rollback can never cause the first N entries of a bucket to be let through a
// second time. Per-(level, message) state is cleaned up lazily the first time
// that key is seen in a new bucket; there is no background sweeper.
func (c *counter) IncCheckReset(bucket int64) uint64 {
	lastBucket := c.bucket.Load()
	if bucket <= lastBucket {
		// Same bucket, or the clock rolled backwards. Either way the current
		// bucket's count keeps accumulating, so the first N entries of a
		// bucket cannot be let through twice.
		return c.counter.Add(1)
	}

	// Stale state from an earlier bucket: claim the new bucket. The counter
	// is reset before the claim is published so a concurrent increment in the
	// new bucket cannot be lost.
	c.counter.Store(1)
	if !c.bucket.CompareAndSwap(lastBucket, bucket) {
		// We raced with another goroutine that also claimed a new bucket, so
		// the counter already holds its first event and we reincrement it.
		return c.counter.Add(1)
	}
	return 1
}

// SamplingDecision is a decision represented as a bit field made by sampler.
// More decisions may be added in the future.
type SamplingDecision uint32

const (
	// LogDropped indicates that the Sampler dropped a log entry.
	LogDropped SamplingDecision = 1 << iota
	// LogSampled indicates that the Sampler sampled a log entry.
	LogSampled
)

// optionFunc wraps a func so it satisfies the SamplerOption interface.
type optionFunc func(*sampler)

func (f optionFunc) apply(s *sampler) {
	f(s)
}

// SamplerOption configures a Sampler.
type SamplerOption interface {
	apply(*sampler)
}

// nopSamplingHook is the default hook used by sampler.
func nopSamplingHook(Entry, SamplingDecision) {}

// SamplerHook registers a function  which will be called when Sampler makes a
// decision.
//
// This hook may be used to get visibility into the performance of the sampler.
// For example, use it to track metrics of dropped versus sampled logs.
//
//	var dropped atomic.Int64
//	zapcore.SamplerHook(func(ent zapcore.Entry, dec zapcore.SamplingDecision) {
//	  if dec&zapcore.LogDropped > 0 {
//	    dropped.Inc()
//	  }
//	})
//
// A hook that panics is recovered from and never changes the sampling
// decision; it only prevents the hook invocation from unwinding the caller's
// stack.
func SamplerHook(hook func(entry Entry, dec SamplingDecision)) SamplerOption {
	return optionFunc(func(s *sampler) {
		s.hook = hook
	})
}

// SamplerClock configures the Clock used to bucket entries into sampling
// intervals.
//
// By default the sampler uses [DefaultClock], whose [time.Time] readings carry
// a monotonic component: time is measured as elapsed time since the sampler
// was created, so adjusting the wall clock backwards cannot rewind an
// in-progress bucket and cause the first N entries to be let through again.
//
// Inject a controllable clock (such as a fake/mock clock in tests) to advance
// sampling buckets deterministically without real sleeps. Buckets are derived
// solely from elapsed time reported by this clock; the Time field of logged
// entries is not consulted.
func SamplerClock(clock Clock) SamplerOption {
	return optionFunc(func(s *sampler) {
		s.clock = clock
	})
}

// NewSamplerWithOptions creates a Core that samples incoming entries, which
// caps the CPU and I/O load of logging while attempting to preserve a
// representative subset of your logs.
//
// Zap samples by logging the first N entries with a given level and message
// each tick. If more Entries with the same level and message are seen during
// the same interval, every Mth message is logged and the rest are dropped.
//
// For example,
//
//	core = NewSamplerWithOptions(core, time.Second, 10, 5)
//
// This will log the first 10 log entries with the same level and message
// in a one second interval as-is. Following that, it will allow through
// every 5th log entry with the same level and message in that interval.
//
// If thereafter is zero, the Core will drop all log entries after the first N
// in that interval.
//
// Sampler can be configured to report sampling decisions with the SamplerHook
// option, and the clock that drives the tick buckets can be replaced with the
// SamplerClock option.
//
// Keep in mind that Zap's sampling implementation is optimized for speed over
// absolute precision; under load, each tick may be slightly over- or
// under-sampled.
func NewSamplerWithOptions(core Core, tick time.Duration, first, thereafter int, opts ...SamplerOption) Core {
	s := &sampler{
		Core:       core,
		tick:       tick,
		counts:     newCounters(),
		first:      uint64(first),
		thereafter: uint64(thereafter),
		hook:       nopSamplingHook,
		clock:      DefaultClock,
	}
	for _, opt := range opts {
		opt.apply(s)
	}
	// Anchor buckets at the (possibly injected) clock's reading so bucket
	// numbers are measured as elapsed time. start is captured after options
	// are applied so a custom clock takes effect.
	s.start = s.clock.Now()

	return s
}

type sampler struct {
	Core

	counts            *counters
	tick              time.Duration
	first, thereafter uint64
	hook              func(Entry, SamplingDecision)
	clock             Clock
	start             time.Time
}

// bucket returns the current time bucket number relative to the sampler's
// start time. It is computed from elapsed time, so a rollback of the wall
// clock cannot make it decrease (the start and current readings share a
// monotonic clock component).
func (s *sampler) bucket() int64 {
	now := s.clock.Now()
	return int64(now.Sub(s.start) / s.tick)
}

var (
	_ Core           = (*sampler)(nil)
	_ leveledEnabler = (*sampler)(nil)
)

// NewSampler creates a Core that samples incoming entries, which
// caps the CPU and I/O load of logging while attempting to preserve a
// representative subset of your logs.
//
// Zap samples by logging the first N entries with a given level and message
// each tick. If more Entries with the same level and message are seen during
// the same interval, every Mth message is logged and the rest are dropped.
//
// Keep in mind that zap's sampling implementation is optimized for speed over
// absolute precision; under load, each tick may be slightly over- or
// under-sampled.
//
// Deprecated: use NewSamplerWithOptions.
func NewSampler(core Core, tick time.Duration, first, thereafter int) Core {
	return NewSamplerWithOptions(core, tick, first, thereafter)
}

func (s *sampler) Level() Level {
	return LevelOf(s.Core)
}

func (s *sampler) With(fields []Field) Core {
	return &sampler{
		Core:       s.Core.With(fields),
		tick:       s.tick,
		counts:     s.counts,
		first:      s.first,
		thereafter: s.thereafter,
		hook:       s.hook,
		clock:      s.clock,
		start:      s.start,
	}
}

func (s *sampler) Check(ent Entry, ce *CheckedEntry) *CheckedEntry {
	if !s.Enabled(ent.Level) {
		return ce
	}

	if ent.Level >= _minLevel && ent.Level <= _maxLevel {
		counter := s.counts.get(ent.Level, ent.Message)
		n := counter.IncCheckReset(s.bucket())
		if n > s.first && (s.thereafter == 0 || (n-s.first)%s.thereafter != 0) {
			s.observe(ent, LogDropped)
			return ce
		}
		s.observe(ent, LogSampled)
	}
	return s.Core.Check(ent, ce)
}

// observe invokes the sampling hook while shielding the sampling decision from
// a panicking hook: the panic is swallowed so it can neither drop an entry
// that was sampled nor admit an entry that was dropped.
func (s *sampler) observe(ent Entry, dec SamplingDecision) {
	defer func() { _ = recover() }()
	s.hook(ent, dec)
}

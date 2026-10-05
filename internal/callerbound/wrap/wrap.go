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

// Package wrap is a test fixture: a logging wrapper package whose frames a
// caller-boundary policy is expected to skip.
package wrap

import "go.uber.org/zap/zapcore"

// Logger is the minimal logging surface the wrapper needs. *zap.Logger
// satisfies it; depending on the interface (rather than on zap directly)
// keeps this fixture importable from zap's own tests.
type Logger interface {
	Info(msg string, fields ...zapcore.Field)
}

// Info1 is a one-layer wrapper around Logger.Info.
func Info1(log Logger, msg string) {
	log.Info(msg)
}

// Info3 is a three-layer wrapper around Logger.Info.
func Info3(log Logger, msg string) {
	info3Layer2(log, msg)
}

func info3Layer2(log Logger, msg string) {
	info3Layer3(log, msg)
}

func info3Layer3(log Logger, msg string) {
	log.Info(msg)
}

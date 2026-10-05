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
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCallerPackagePath(t *testing.T) {
	tests := []struct {
		function string
		want     string
	}{
		{"go.uber.org/zap.(*Logger).Info", "go.uber.org/zap"},
		{"go.uber.org/zap/internal/stacktrace.Take", "go.uber.org/zap/internal/stacktrace"},
		{"runtime.main", "runtime"},
		{"main.main", "main"},
		{"go.uber.org/zap_test.TestFoo", "go.uber.org/zap_test"},
		{"example.com/foo/bar.Baz", "example.com/foo/bar"},
		{"example.com/foo.(*Type).Method", "example.com/foo"},
		{"noSlashNoDot", "noSlashNoDot"},
	}
	for _, tt := range tests {
		t.Run(tt.function, func(t *testing.T) {
			assert.Equal(t, tt.want, callerPackagePath(tt.function))
		})
	}
}

func TestIsWrapperPackage(t *testing.T) {
	tests := []struct {
		name       string
		registered []string
		function   string
		want       bool
	}{
		{
			name:       "exact match",
			registered: []string{"example.com/foo"},
			function:   "example.com/foo.Bar",
			want:       true,
		},
		{
			name:       "sub-package match",
			registered: []string{"example.com/foo"},
			function:   "example.com/foo/bar.Baz",
			want:       true,
		},
		{
			name:       "prefix without boundary does not match",
			registered: []string{"example.com/foo"},
			function:   "example.com/foobar.Baz",
			want:       false,
		},
		{
			name:       "user does not match userx",
			registered: []string{"example.com/user"},
			function:   "example.com/userx.Baz",
			want:       false,
		},
		{
			name:       "user matches user",
			registered: []string{"example.com/user"},
			function:   "example.com/user.Baz",
			want:       true,
		},
		{
			name:       "no registered packages",
			registered: nil,
			function:   "example.com/foo.Bar",
			want:       false,
		},
		{
			name:       "multiple registered packages",
			registered: []string{"example.com/foo", "example.com/bar"},
			function:   "example.com/bar.Baz",
			want:       true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := &Logger{callerSkipPackages: tt.registered}
			assert.Equal(t, tt.want, log.isWrapperPackage(tt.function))
		})
	}
}

func TestAddCallerSkipPackageOption(t *testing.T) {
	log := NewNop()
	assert.Empty(t, log.callerSkipPackages, "expected no registered packages by default")

	log = log.WithOptions(AddCallerSkipPackage("example.com/foo"))
	assert.Equal(t, []string{"example.com/foo"}, log.callerSkipPackages,
		"expected one registered package")

	log = log.WithOptions(AddCallerSkipPackage("example.com/bar", "example.com/baz"))
	assert.Equal(t, []string{"example.com/foo", "example.com/bar", "example.com/baz"},
		log.callerSkipPackages, "expected three registered packages")
}

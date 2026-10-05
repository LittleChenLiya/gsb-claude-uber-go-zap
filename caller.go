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

import "strings"

// callerPackagePath returns the import path of the package that owns the
// given function name.
//
// The function name is expected to be in the form used by
// runtime.Frame.Function: "path/to/package.Func", "path/to/package.(*Type).
// Method", or "package.Func" for packages without a path.
//
// For example:
//
//	callerPackagePath("go.uber.org/zap.(*Logger).Info") == "go.uber.org/zap"
//	callerPackagePath("runtime.main") == "runtime"
//	callerPackagePath("main.main") == "main"
func callerPackagePath(function string) string {
	// The package path is everything before the first dot that follows the
	// last slash. If there is no slash, the package path is everything
	// before the first dot.
	i := strings.LastIndexByte(function, '/')
	if i < 0 {
		i = 0
	} else {
		i++ // skip the slash
	}
	j := strings.IndexByte(function[i:], '.')
	if j < 0 {
		return function
	}
	return function[:i+j]
}

// isWrapperPackage reports whether the given function belongs to a package
// registered with AddCallerSkipPackage.
//
// Matching is performed at package boundaries: a registered path matches the
// frame's package path only if they are equal, or if the frame's path is a
// sub-package of the registered path. This prevents "example.com/foo" from
// accidentally matching "example.com/foobar".
func (log *Logger) isWrapperPackage(function string) bool {
	if len(log.callerSkipPackages) == 0 {
		return false
	}
	pkgPath := callerPackagePath(function)
	for _, registered := range log.callerSkipPackages {
		if pkgPath == registered {
			return true
		}
		if strings.HasPrefix(pkgPath, registered+"/") {
			return true
		}
	}
	return false
}

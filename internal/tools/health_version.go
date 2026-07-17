package tools

import "runtime"

// goVersionString is the build-time Go version string. Set via
// runtime.Version() in package init so the value is computed once
// and not per-call.
var goVersionString = runtime.Version()

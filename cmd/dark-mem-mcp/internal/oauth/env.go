// Package oauth — env.go: indirection layer for os.Getenv so tests can
// stub environment variable reads without polluting the whole package
// with build tags.
package oauth

import "os"

// osGetenvImpl is the default implementation. In tests, the variable
// is reassigned at the start of each TestMain to return deterministic
// values. This avoids dependency on the actual process environment.
var osGetenvImpl = func(key string) string {
	return os.Getenv(key)
}
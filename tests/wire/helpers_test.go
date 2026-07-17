package wire

// osEnviron exposes os.Environ for the wire package without
// importing `os` at the package top level (kept lean so the
// `go test -tags=wire ./tests/wire/...` footprint stays small).
import "os"

// osEnviron returns the process environment as a slice of "k=v"
// strings. Mirrors os.Environ.
func osEnviron() []string { return os.Environ() }

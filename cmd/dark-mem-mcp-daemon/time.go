package main

import "time"

// nowUnixNano returns wall-clock time as Unix nanoseconds. Stub-able
// for tests.
var nowUnixNano = func() int64 { return time.Now().UnixNano() }

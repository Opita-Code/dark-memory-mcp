// Package onnx — Windows binary bridge for the bundled libonnxruntime.
//
// The onnx package extracts onnxruntimeWindowsDLL to a temp path on
// first use; yalue/onnxruntime_go then dlopens it via the
// ORT_SHARED_LIBRARY_PATH env var. This file is selected by the
// `windows` build tag so each platform binary embeds its own copy.
//
//go:build windows

package onnx

import _ "embed"

//go:embed bundled/windows-amd64/onnxruntime.dll
var onnxruntimeWindowsDLL []byte

// The Linux and macOS binaries are intentionally empty on this build.
// Declared (vs. in binary_other.go) so the onnx.go code can reference
// them unconditionally; platformBinary() selects the right one based
// on runtime.GOOS + runtime.GOARCH.
var onnxruntimeLinuxSO []byte     //nolint:unused // populated on linux+amd64
var onnxruntimeDarwinDylib []byte //nolint:unused // populated on darwin+arm64

// unsupportedPlatform is false on this build; New() succeeds.
var unsupportedPlatform = false

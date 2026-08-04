// Package onnx — Linux amd64 binary bridge for the bundled
// libonnxruntime. See binary_windows.go for the architecture.
//
//go:build linux && amd64

package onnx

import _ "embed"

//go:embed bundled/linux-amd64/libonnxruntime.so.1.22.0
var onnxruntimeLinuxSO []byte

// Windows and macOS binaries are intentionally empty on this build.
var onnxruntimeWindowsDLL []byte  //nolint:unused // populated on windows-amd64
var onnxruntimeDarwinDylib []byte //nolint:unused // populated on darwin+arm64

// unsupportedPlatform is false on this build; New() succeeds.
var unsupportedPlatform = false

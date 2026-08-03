// Package onnx — macOS arm64 binary bridge for the bundled
// libonnxruntime. See binary_windows.go for the architecture.
//
//go:build darwin && arm64

package onnx

import _ "embed"

//go:embed bundled/darwin-arm64/libonnxruntime.1.22.0.dylib
var onnxruntimeDarwinDylib []byte

// Windows and Linux binaries are intentionally empty on this build.
var onnxruntimeWindowsDLL []byte //nolint:unused // populated on windows-amd64
var onnxruntimeLinuxSO []byte    //nolint:unused // populated on linux+amd64

// unsupportedPlatform is false on this build; New() succeeds.
var unsupportedPlatform = false

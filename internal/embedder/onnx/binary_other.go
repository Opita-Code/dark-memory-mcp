// Package onnx — unsupported-platform stub.
//
// Every platform we ship gets its own binary_*.go file. This file is
// the catch-all for platforms we don't ship (linux/arm64, freebsd,
// windows/arm64, etc). On those builds, the ONNX adapter is compiled
// to a stub that always returns embedder.ErrDisabled.
//
//go:build !windows && !(linux && amd64) && !(darwin && arm64)

package onnx

// All three globals are declared here so onnx.go can reference them
// unconditionally. platformBinary() picks the right one based on
// runtime.GOOS; on unsupported platforms, all three are empty.
var onnxruntimeWindowsDLL []byte
var onnxruntimeLinuxSO []byte
var onnxruntimeDarwinDylib []byte

// unsupportedPlatform is true on this build; New() returns ErrDisabled.
var unsupportedPlatform = true

// Package onnx data — embed.FS bridge for the bundled ONNX model + vocab.
//
// All binary assets used by the ONNX adapter live alongside this file so
// a single //go:embed directive can pull them into the binary at build
// time. The onnx package extracts these bytes to a temp path on first
// use (idempotent; SHA-pinned) so yalue/onnxruntime_go can dlopen them.
//
// # Trust boundary
//
// The bundled model_quantized.onnx is sha-pinned at compile time
// (DefaultExpectedSHA256). On first extraction the adapter verifies
// the extracted bytes against the pinned SHA before opening the
// ONNX Runtime session; mismatch → ErrSHA256Mismatch → ErrDisabled.
//
// # Why bundle instead of lazy-download
//
// row 163 amendment: "A vibe-coder installing dark-memory should never
// see 'first save will download 22 MB ONNX model from huggingface.co'".
// The bundle is the entire point. See CHANGELOG v2.9.1-alpha.
package data

import _ "embed"

//go:embed model_quantized.onnx
var modelQuantizedONNX []byte

//go:embed vocab.txt
var vocabTxt []byte

// ModelBytes returns the bundled ONNX model. Caller MUST NOT mutate
// the slice — it's the canonical bytes referenced by SHA verification.
func ModelBytes() []byte { return modelQuantizedONNX }

// VocabBytes returns the bundled WordPiece vocabulary (BERT format,
// one token per line, ~30 522 tokens).
func VocabBytes() []byte { return vocabTxt }

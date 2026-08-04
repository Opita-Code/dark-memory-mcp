//go:build tools

// Package tools pins release tooling for `go generate`.
//
// Run `go generate ./...` after tagging a release to sync the
// `version` field of server.json, mcpb/manifest.json and the npm
// package.json files with the git tag. The generator is idempotent;
// commit the resulting diff as part of the release.
//
// This file carries no imports — the build tag keeps it out of normal
// builds; its only job is hosting the go:generate directive.
package tools

//go:generate go run ./cmd/gen-metadata

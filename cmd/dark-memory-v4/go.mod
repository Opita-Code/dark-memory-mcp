// dark-memory-v4 binary — sibling module that imports the parent
// library. Mirrors the cmd/dark-mem-cli / cmd/dark-mem-mcp pattern
// (separate go.mod with replace directive pointing at the parent
// module). Stdlib-only at this layer; the heavy v4alpha packages
// come from the parent.
//
// As of BUG-6 (2026-09-27) the binary uses:
//   - modernc.org/sqlite v1.53.0 (transitive from parent; embeds
//     SQLite 3.53.2 with the WAL-reset bug fixed in 3.51.3).
//   - google/uuid (transitive from parent session package).
module github.com/dark-agents/dark-memory-mcp/cmd/dark-memory-v4

go 1.25.5

require github.com/dark-agents/dark-memory-mcp v0.0.0

require (
	github.com/99designs/go-keychain v0.0.0-20191008050251-8e49817e8af4 // indirect
	github.com/99designs/keyring v1.2.2 // indirect
	github.com/danieljoos/wincred v1.1.2 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/dvsekhvalnov/jose2go v1.5.0 // indirect
	github.com/godbus/dbus v0.0.0-20190726142602-4481cbc300e2 // indirect
	github.com/google/jsonschema-go v0.4.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/gsterjov/go-libsecret v0.0.0-20161001094733-a6f4afe4910c // indirect
	github.com/mark3labs/mcp-go v0.56.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mtibben/percent v0.2.1 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	github.com/spf13/cast v1.7.1 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.44.0 // indirect
	golang.org/x/term v0.24.0 // indirect
	golang.org/x/text v0.18.0 // indirect
	modernc.org/libc v1.73.4 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
	modernc.org/sqlite v1.53.0 // indirect
)

replace github.com/dark-agents/dark-memory-mcp => ../..

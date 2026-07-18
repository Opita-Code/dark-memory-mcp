// test-patched-judge: standalone E2E test for the patched SelfHarnessClient.Judge
// Sibling module that imports the parent library.
module github.com/dark-agents/dark-memory-mcp/cmd/test-patched-judge

go 1.25.5

require github.com/dark-agents/dark-memory-mcp v0.0.0

require github.com/pelletier/go-toml/v2 v2.4.3 // indirect

replace github.com/dark-agents/dark-memory-mcp => ../..

// cmd/verify-phase14 — operator-facing live verification for Phase 14.
//
// Bind `judge-minimax-cn-live` to the default project, then run
// drift_judge via the orchestrator (with the real LLMJudge against
// api.minimaxi.com), and print the resulting verdict. NEVER echoes
// the auth token.
//
// Usage:
//
//	VERIFY_PHASE14=1 go run ./cmd/verify-phase14/
//
// Prints a JSON summary with verdict + provider_id + latency.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/artifact"
	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/safety"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	sqlitestore "github.com/dark-agents/dark-memory-mcp/internal/store/sqlite"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/judge/v4judge"
)

func main() {
	if os.Getenv("VERIFY_PHASE14") != "1" {
		fmt.Println("set VERIFY_PHASE14=1 to run live verification")
		os.Exit(0)
	}
	key := os.Getenv("MINIMAX_API_KEY_CN")
	if key == "" {
		key = os.Getenv("MINIMAX_API_KEY")
	}
	if key == "" {
		fmt.Println("MINIMAX_API_KEY_CN or MINIMAX_API_KEY must be set")
		os.Exit(1)
	}

	// 1) Open the operator's actual DB.
	dbPath := os.Getenv("DARK_DB_PATH")
	if dbPath == "" {
		dbPath = "C:\\Users\\Nico\\AppData\\Local\\dark-agents\\dark.db"
	}
	st, err := sqlitestore.Open(context.Background(), store.Config{
		Driver: store.DriverSQLite, DSN: dbPath, WALMode: true, ForeignKeys: true,
	})
	if err != nil {
		fail("open store", err)
	}
	defer st.Close()

	if err := st.SetActiveProject(context.Background(), "default"); err != nil {
		fail("set active project", err)
	}

	proj, err := st.GetProject(context.Background(), "default")
	if err != nil {
		fail("get default project", err)
	}
	if proj == nil {
		fail("default project missing", nil)
	}

	// 2) Bind judge-minimax-cn-live to the project's NLIConfig primary.
	if proj.NLIConfig == nil {
		proj.NLIConfig = &project.NLIConfig{
			Enabled:           true,
			Primary:           project.NLIPrimary{},
			LatencyBudgetMS:   30000,
			MaxPremiseBytes:   65536,
			MaxHypothesisBytes: 4096,
			MaxCacheEntries:   1000,
			CacheTTLSeconds:   300,
		}
	}
	proj.NLIConfig.Primary = project.NLIPrimary{
		ProviderID: "judge-minimax-cn-live",
		Endpoint:   "https://api.minimaxi.com/v1/chat/completions",
		AuthToken:  key,
		TimeoutMS:  60000,
		ModelRev:   "MiniMax-M3",
	}
	proj.NLIConfig.Enabled = true
	if err := proj.NLIConfig.Validate(); err != nil {
		fail("validate nli_config", err)
	}
	if err := st.CreateProject(context.Background(), proj); err != nil {
		fail("persist project", err)
	}

	// 3) Build the orchestrator + LLMJudge (real HTTP client).
	orch := orchestration.New(st, &safety.Holder{})
	j, err := v4judge.NewLLMJudge(v4judge.ProviderConfig{
		ProviderID: "judge-minimax-cn-live",
		Endpoint:   "https://api.minimaxi.com/v1/chat/completions",
		AuthToken:  key,
		TimeoutMS:  60000,
		ModelRev:   "MiniMax-M3",
	}, &http.Client{Timeout: 90 * time.Second})
	if err != nil {
		fail("NewLLMJudge", err)
	}
	orch.WithLLMJudge(j)

	// 4) Run drift_judge on a known aligned artifact.
	tmpDir, err := os.MkdirTemp("", "verify-phase14-*")
	if err != nil {
		fail("mkdir temp", err)
	}
	defer os.RemoveAll(tmpDir)
	artifactPath := tmpDir + "/artifact.go"
	if err := os.WriteFile(artifactPath, []byte(`package main
import "fmt"
func main() { fmt.Println("Hello, World!") }
`), 0644); err != nil {
		fail("write artifact", err)
	}

	specIntent := "a simple hello world function in Go (must compile and print 'Hello, World!')"

	out, err := orch.DriftJudge(context.Background(), orchestration.DriftJudgeInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: artifactPath},
		SpecIntent:  specIntent,
	})
	if err != nil {
		fail("DriftJudge", err)
	}

	// 5) Print summary — never echoes auth_token.
	summary := map[string]any{
		"project_id":  proj.ProjectID,
		"bound_provider_id": proj.NLIConfig.Primary.ProviderID,
		"endpoint":    proj.NLIConfig.Primary.Endpoint,
		"auth_token_len": len(key),
		"verdict":     out.Verdict,
		"confidence":  out.Confidence,
		"provider_id": out.ProviderID,
		"model_rev":   out.ModelRev,
		"latency_ms":  out.LatencyMS,
		"reasoning":   out.Reasoning,
		"artifact_source": out.ArtifactSource,
		"artifact_sha256": out.ArtifactSHA256[:16] + "...",
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(summary)
}

func fail(stage string, err error) {
	fmt.Printf("[FAIL] stage=%s err=%v\n", stage, err)
	os.Exit(1)
}
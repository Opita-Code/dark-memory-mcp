# vibe-flow/cascade-sub-specs.ps1
# Cascades 10 sub-specs from spec_id 153 (the RFC).
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8

$wrapper = "C:\Users\Nico\AppData\Local\Temp\opencode\dark-agents-call.ps1"
$tmpDir = Join-Path $env:TEMP "dark-mem-cascade"
if (Test-Path $tmpDir) { Get-ChildItem $tmpDir -Filter *.json -ErrorAction SilentlyContinue | Remove-Item }
New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null

# ---- Step 1: drift_log entries marking 141-145 as superseded ----
foreach ($oldId in @(141, 142, 143, 144, 145)) {
    $argsFile = Join-Path $tmpDir ("drift-" + $oldId + ".json")
    $payload = @{
        artifact_id     = $oldId
        spec_id         = 153
        verdict         = "drift_detected"
        spec_diff       = '{"supersedes":[141,142,143,144,145],"reason":"fragmented CRUD plan replaced by single canonical RFC","rfc":"vibe-flow/main/DARK_MEMORY_MCP_RFC.md"}'
        judge_reasoning = "spec_ids 141-145 describe a flat CRUD-by-table tool surface (52 tools). The RFC (spec_id 153) defines an intent-driven design (25 orchestrators, context objects, sequence-aware responses). The earlier specs are SUPERSEDED by the RFC; future work references the RFC only."
        reconciled_at   = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss.fffZ")
    } | ConvertTo-Json -Depth 4 -Compress
    $payload | Out-File -FilePath $argsFile -Encoding utf8 -NoNewline
    $out = & powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_drift_log" -ArgsFile $argsFile -TimeoutMs 15000 2>&1
    $first = ($out | Select-Object -First 1)
    Write-Host ("drift " + $oldId + ": " + $first)
}

# ---- Step 2: sub-spec data ----
$subSpecs = @(
    @{ id = 'storage'; title = 'sub-spec 1 storage foundation refine existing';
       tasks = @('go.mod module dual-driver deps', 'Store interface 40+ methods', 'sqlite full impl', 'postgres full impl pgxpool', 'migrate sqlite v1-v6', 'migrate postgres v1-v6', 'runtime factory import-cycle workaround', 'tests/dual_driver passes on both drivers') }
    @{ id = 'invariants'; title = 'sub-spec 2 six invariants audit sessions canary watchdog cache mod sanitize';
       tasks = @('audit write_audit RecordWrite + ListWrites', 'session SaveSession GetSession CloseSession ListSessions', 'safety canary NewCanary Holder ValidatePayload HashPayload', 'safety markers injectionMarkers regex + CheckPayload', 'constitution watchdog VerifyConstitutionHash on Store.Open', 'llm cache re-hash on Get + mismatch anomaly event', 'mods loader content sanitize via injectionMarkers', 'tests/invariants/inv1..inv6 one test per invariant') }
    @{ id = 'context-economy'; title = 'sub-spec 3 context objects plus economy pipeline';
       tasks = @('context/artifact.go ArtifactContext composed view', 'context/session.go SessionContext composed view', 'context/policy.go PolicyContext composed view', 'economy/pipeline.go Atlan 5-bucket dedup filter truncate compress cap', 'economy/recall.go RecallOptions integration', 'context/compose.go ComposeArtifact + ComposeSession', 'tests/context/artifact_context_test') }
    @{ id = 'orchestrators'; title = 'sub-spec 4 25 orchestrators workflow functions';
       tasks = @('PublishVibe returns PublishResult', 'ResearchTopic returns ResearchContext', 'RecallContext returns RecallContext', 'Judge returns Verdict', 'JudgeConsensus returns ConsensusResult', 'ActivePolicy returns PolicyContext', 'MemoryState returns MemoryState', 'ResolveDrift returns DriftResult', 'VibeSpec returns SpecContext', 'tests/orchestrator per-orchestrator test') }
    @{ id = 'mcp-server'; title = 'sub-spec 5 dark-memory-mcp MCP server 25 tools';
       tasks = @('cmd/dark-memory-mcp/main.go parse env construct Store register server', 'internal/server/server.go setup + tool registry', 'internal/tools/registry.go Tool type', 'internal/tools/wiring.go orchestrator to MCP adapter', 'internal/tools/errors.go typed-error to MCP response', 'internal/tools/next.go NextAction serialisation', 'cmd/dark-memory-mcp/go.mod separate, requires library', 'tests/e2e/server_test 1000 mixed calls no deadlock') }
    @{ id = 'cli'; title = 'sub-spec 6 dark-memory-cli admin plus inspect read-only';
       tasks = @('cmd/dark-memory-cli/main.go subcommand dispatch stdlib flag', 'cmd/dark-memory-cli/migrate.go', 'cmd/dark-memory-cli/vacuum.go', 'cmd/dark-memory-cli/schema_status.go', 'cmd/dark-memory-cli/set_driver.go', 'cmd/dark-memory-inspect/main.go read-only diagnostic', 'cmd/dark-memory-cli/go.mod separate requires library', 'tests/cli/cli_test') }
    @{ id = 'dark-recall-v23'; title = 'sub-spec 7 dark-recall plugin v2.3';
       tasks = @('detect dark-memory-mcp in opencode.jsonc', 'Layer 2 call dark_memory_research_topic', 'Layer 2 scan prefill against safety Canary before injection', 'Layer 1 call dark_memory_active_policy', 'Layer 3 use boundary markers', 'fallback to dark_mem_* when dark-memory-mcp absent', 'tests dark-recall v2.3 vs v2.1 behavior matrix') }
    @{ id = 'deprecation-shim'; title = 'sub-spec 8 dark-research-mcp deprecation shim';
       tasks = @('dark_mem.go emit deprecated successor sunset fields on every response', 'dark_research.go same shim on overlapping tools', 'README Coexistence section', 'No code changes to dark_mem behavior') }
    @{ id = 'runbooks'; title = 'sub-spec 9 runbooks plus operational docs';
       tasks = @('docs/RUNBOOK.md Postgres install driver switch vacuum retention', 'docs/COEXISTENCE.md dark-research-mcp plus dark-memory-mcp story', 'docs/INVARIANTS.md six invariants for operators', 'docs/CONTEXT_OBJECTS.md shape and intent of each context type', 'docs/PERFORMANCE.md P50 P99 targets', 'docs/MIGRATION.md step-by-step from SQLite-only to Postgres') }
    @{ id = 'human-gate'; title = 'sub-spec 10 drift verdict plus human gate';
       tasks = @('per sub-spec 1-9 dark_ssd_drift_judge against generated artifacts', 'resolve any drift_detected verdicts', 'final report generated vs RFC commitments', 'human gate review with operator', 'tag sub-specs ready-for-publish') }
)

$created = @()

foreach ($spec in $subSpecs) {
    $argsFile = Join-Path $tmpDir ("spec-" + $spec.id + ".json")

    # Build the spec + tasks as JSON strings (PowerShell here-string trick
    # is too fragile across character classes; just use string concatenation).
    $tasksParts = @()
    $n = 0
    foreach ($t in $spec.tasks) {
        $n++
        # Build JSON-encoded task description (escape backslash and quote)
        $desc = $t -replace '\\','\\\\' -replace '"','\\"'
        $tasksParts += ('{"id":"' + $spec.id + '.' + $n + '","description":"' + $desc + '"}')
    }
    $tasksJson = '[' + ($tasksParts -join ',') + ']'
    $titleEsc = $spec.title -replace '\\','\\\\' -replace '"','\\"'
    $specJson = '{"spec_id":"' + $spec.id + '","title":"' + $titleEsc + '"}'
    $constJson = '{"ref":"vibe-flow/constitution/dark-memory-mcp.constitution.toml","summary":"Sub-spec cascading from spec_id 153 RFC"}'

    # Combine into final payload object and convert to JSON
    $payloadObj = @{ case_kind = "C1"; session_id = "dark-memory-mcp-v1"; constitution = $constJson; spec = $specJson; tasks = $tasksJson }
    $payload = $payloadObj | ConvertTo-Json -Depth 6 -Compress
    $payload | Out-File -FilePath $argsFile -Encoding utf8 -NoNewline

    $out = & powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_spec_create" -ArgsFile $argsFile -TimeoutMs 20000 2>&1
    $line = ($out | Select-Object -First 1)
    Write-Host ("[try] " + $spec.id + " -> " + $line)
    if ($line -match '"spec_id":\s*(\d+)') {
        $newId = $Matches[1]
        Write-Host ("[ok]  " + $spec.id + " -> spec_id " + $newId) -ForegroundColor Green
        $created += [PSCustomObject]@{ id = $spec.id; spec_id = [int]$newId }
    } else {
        Write-Host ("[FAIL] " + $spec.id) -ForegroundColor Red
    }
}

Write-Host ""
Write-Host "=== Cascade complete ===" -ForegroundColor Cyan
$created | Format-Table -AutoSize | Out-String
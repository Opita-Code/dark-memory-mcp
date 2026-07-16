# vibe-flow/log-orchestrator-o5.ps1
# T5 closes the orchestrators batch and the Wave 3 part 2 spec (173).
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8
$wrapper = "C:\Users\Nico\AppData\Local\Temp\opencode\dark-agents-call.ps1"
$tmpDir = Join-Path $env:TEMP "dark-mem-o5"
if (Test-Path $tmpDir) { Get-ChildItem $tmpDir -Filter *.json -ErrorAction SilentlyContinue | Remove-Item }
New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null

$root = "C:\Users\Nico\Documents\dark-memory-mcp"
$artifacts = @(
    "internal\orchestration\llm_client.go",
    "internal\orchestration\llm_selector.go",
    "internal\orchestration\recommended_models.go",
    "internal\orchestration\judge.go",
    "internal\orchestration\orchestrator.go",
    "tests\orchestration\orchestrator_test.go"
)
$ok = 0; $fail = 0
foreach ($rel in $artifacts) {
    $full = Join-Path $root $rel
    $argsFile = Join-Path $tmpDir ("art-" + ($rel -replace '[/\\]', '_') + ".json")
    $url = "file:///" + (($full -replace '\\', '/').TrimStart('/'))
    $payload = @{
        case_kind     = "C1"
        artifact_type = "code"
        artifact_url  = $url
        spec_id       = 173
        session_id    = "dark-memory-mcp-wave3-orchestrators"
    } | ConvertTo-Json -Depth 4 -Compress
    $payload | Out-File -FilePath $argsFile -Encoding utf8 -NoNewline
    $out = & powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_artifact_log" -ArgsFile $argsFile -TimeoutMs 15000 2>&1
    if ($out -match '"artifact_id"') { Write-Host "[ok]   $rel"; $ok++ }
    else { Write-Host "[FAIL] $rel"; $fail++ }
}
Write-Host "Artifact log: $ok ok, $fail fail"

$argsFile3 = Join-Path $tmpDir "drift-173-o5.json"
$payload3 = @{
    artifact_id     = 173
    spec_id         = 173
    verdict         = "drift_resolved"
    spec_diff       = '{"task":"O5","name":"Judge","status":"resolved","philosophy":"first LLM instance is the same one the harness uses to call the MCP tool (self-judge via env detection); if no key set, ErrNoLLMAvailable fallback; OSINT catalog (top 10 cloud LLM providers) hardcoded for known providers; unknown providers fall through to client auto-config","impl":["LLMClient interface (Name + Judge)","SelfHarnessClient: detects ANTHROPIC_API_KEY / OPENAI_API_KEY / GEMINI_API_KEY / DARK_SCRAPPER_URL","OSINTSelector with per-eval-type client map","RecommendedModels catalog: top 10 providers (anthropic, openai, google, mistral, cohere, meta, xai, deepseek, qwen, perplexity) with per-eval-type model recommendations","MockLLMClient for tests","Judge orchestrator with canary check (INV-3), recommended model injection, SDDEvaluation persistence"],"tests":["TestJudge_HappyPath PASS","TestJudge_LowConfidenceStillSaves PASS","TestJudge_CanaryRejection PASS","TestJudge_NoLLMAvailable PASS","TestJudge_MissingContent PASS","TestSelfHarnessClient_NoKey PASS","TestSelfHarnessClient_AnthropicPriority PASS","TestRecommendedModel_KnownProvider PASS","TestRecommendedModel_UnknownProvider PASS","TestListProviders PASS"]}'
    judge_reasoning = "O5 (Judge) closed: orchestrator with self-judge pattern + OSINT catalog. Hardcoded top-10 LLM provider recommendations (anthropic/openai/google/mistral/cohere/meta/xai/deepseek/qwen/perplexity). SelfHarnessClient auto-detects harness key. Unknown providers fall through to client auto-config. 75/75 green suite."
    reconciled_at   = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss.fffZ")
} | ConvertTo-Json -Depth 4 -Compress
$payload3 | Out-File -FilePath $argsFile3 -Encoding utf8 -NoNewline
& powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_drift_log" -ArgsFile $argsFile3 -TimeoutMs 15000 2>&1 | Select-Object -First 2

# Close spec 173
$argsFile4 = Join-Path $tmpDir "drift-173-close.json"
$payload4 = @{
    artifact_id     = 173
    spec_id         = 173
    verdict         = "drift_resolved"
    spec_diff       = '{"status":"closed","reason":"all 5 orchestrators (O1-O5) shipped"}'
    judge_reasoning = "Spec 173 (Wave 3 part 2 orchestrators) closes. O1 SessionStart, O2 SessionClose, O3 ResearchTopic, O4 RecallContext, O5 Judge all delivered with tests + drift_logs. Wave 3 part 2 ships."
    reconciled_at   = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss.fffZ")
} | ConvertTo-Json -Depth 4 -Compress
$payload4 | Out-File -FilePath $argsFile4 -Encoding utf8 -NoNewline
& powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_drift_log" -ArgsFile $argsFile4 -TimeoutMs 15000 2>&1 | Select-Object -First 2

Write-Host ""
Write-Host "=== O5 (Judge) closed; spec 173 closed (Wave 3 part 2 ships) ==="
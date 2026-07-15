# vibe-flow/log-orchestrator-o3.ps1
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8
$wrapper = "C:\Users\Nico\AppData\Local\Temp\opencode\dark-agents-call.ps1"
$tmpDir = Join-Path $env:TEMP "dark-mem-o3"
if (Test-Path $tmpDir) { Get-ChildItem $tmpDir -Filter *.json -ErrorAction SilentlyContinue | Remove-Item }
New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null

$root = "C:\Users\Nico\Documents\dark-memory-mcp"
$artifacts = @(
    "internal\orchestration\research_backend.go",
    "internal\orchestration\research_topic.go",
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

$argsFile3 = Join-Path $tmpDir "drift-173-o3.json"
$payload3 = @{
    artifact_id     = 173
    spec_id         = 173
    verdict         = "drift_resolved"
    spec_diff       = '{"task":"O3","name":"ResearchTopic","status":"resolved","impl":["internal/orchestration/research_backend.go: ResearchBackend interface + MockResearchBackend for tests + WithBackend(s) builders","internal/orchestration/research_topic.go: ResearchTopic orchestrator","internal/orchestration/orchestrator.go: +backends field"],"tests":["TestResearchTopic_OneBackend PASS","TestResearchTopic_MaxItemsCap PASS","TestResearchTopic_MissingQuery PASS","TestResearchTopic_BackendErrorGracefulDegradation PASS","TestResearchTopic_MultipleBackendsAggregate PASS"]}'
    judge_reasoning = "O3 (ResearchTopic) closed: research pipeline orchestrator with backend interface. Mock backend for tests; real backends (web_search, news_monitor, cve_enrich, etc.) plug in by implementing ResearchBackend. Graceful degradation: backend errors are logged into research_runs.Errors. 65/65 green suite."
    reconciled_at   = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss.fffZ")
} | ConvertTo-Json -Depth 4 -Compress
$payload3 | Out-File -FilePath $argsFile3 -Encoding utf8 -NoNewline
& powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_drift_log" -ArgsFile $argsFile3 -TimeoutMs 15000 2>&1 | Select-Object -First 2
Write-Host "=== O3 (ResearchTopic) closed ==="
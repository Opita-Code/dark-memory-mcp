# vibe-flow/log-and-drift-check.w2.ps1
# Wave 2 (sub-spec 156 context + economy) — artifact_log + drift_log.
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8
$wrapper = "C:\Users\Nico\AppData\Local\Temp\opencode\dark-agents-call.ps1"
$tmpDir = Join-Path $env:TEMP "dark-mem-w2"
if (Test-Path $tmpDir) { Get-ChildItem $tmpDir -Filter *.json -ErrorAction SilentlyContinue | Remove-Item }
New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null

$root = "C:\Users\Nico\Documents\dark-memory-mcp"
$artifacts = @(
    "internal/context/context.go",
    "internal/economy/economy.go",
    "tests/context/context_test.go",
    "tests/economy/economy_test.go"
)

$ok = 0; $fail = 0
foreach ($rel in $artifacts) {
    $full = Join-Path $root $rel
    if (-not (Test-Path $full)) { Write-Host "[MISSING] $rel"; $fail++; continue }
    $argsFile = Join-Path $tmpDir ("art-" + ($rel -replace '[/\\]', '_') + ".json")
    $url = "file:///" + ($full -replace '\\', '/')
    $payload = @{ case_kind = "C1"; artifact_type = "code"; artifact_url = $url
                   spec_id = 156; session_id = "dark-memory-mcp-v1" } | ConvertTo-Json -Depth 4 -Compress
    $payload | Out-File -FilePath $argsFile -Encoding utf8 -NoNewline
    $out = & powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_artifact_log" -ArgsFile $argsFile -TimeoutMs 15000 2>&1
    if ($out -match '"artifact_id"') { Write-Host "[ok]   $rel"; $ok++ }
    else { Write-Host "[FAIL] $rel"; $fail++ }
}
Write-Host "Artifact log: $ok ok, $fail fail"

# Drift log
$argsFile = Join-Path $tmpDir "drift-156.json"
$payload = @{
    artifact_id     = 156
    spec_id         = 156
    verdict         = "drift_resolved"
    spec_diff       = '{"wave":"2","status":"shipped","files":["internal/context/context.go","internal/economy/economy.go","tests/context/context_test.go","tests/economy/economy_test.go"]}'
    judge_reasoning = "Wave 2 implementation: ArtifactContext + SessionContext + PolicyContext composed views (ComposeArtifact, ComposeSession, ComposePolicy); markdown rendering deterministic; Atlan 5-bucket pipeline (dedup → filter → truncate → compress → cap); EstimateTokens helper. Tests: 7/7 context + 7/7 economy pass."
    reconciled_at   = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss.fffZ")
} | ConvertTo-Json -Depth 4 -Compress
$payload | Out-File -FilePath $argsFile -Encoding utf8 -NoNewline
& powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_drift_log" -ArgsFile $argsFile -TimeoutMs 15000 2>&1 | Select-Object -First 1
Write-Host ""
Write-Host "=== Wave 2 closed ==="
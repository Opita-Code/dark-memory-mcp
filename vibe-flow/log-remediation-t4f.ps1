# vibe-flow/log-remediation-t4f.ps1
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8
$wrapper = "C:\Users\Nico\AppData\Local\Temp\opencode\dark-agents-call.ps1"
$tmpDir = Join-Path $env:TEMP "dark-mem-t4f"
if (Test-Path $tmpDir) { Get-ChildItem $tmpDir -Filter *.json -ErrorAction SilentlyContinue | Remove-Item }
New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null

$root = "C:\Users\Nico\Documents\dark-memory-mcp"
$artifacts = @(
    "internal\store\sqlite\store.go",
    "tests\project\project_test.go"
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
        spec_id       = 171
        session_id    = "dark-memory-mcp-v1-remediation"
    } | ConvertTo-Json -Depth 4 -Compress
    $payload | Out-File -FilePath $argsFile -Encoding utf8 -NoNewline
    $out = & powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_artifact_log" -ArgsFile $argsFile -TimeoutMs 15000 2>&1
    if ($out -match '"artifact_id"') { Write-Host "[ok]   $rel"; $ok++ }
    else { Write-Host "[FAIL] $rel"; $fail++ }
}
Write-Host "Artifact log: $ok ok, $fail fail"

$argsFile3 = Join-Path $tmpDir "drift-171.json"
$driftPayload = @{
    artifact_id     = 171
    spec_id         = 171
    verdict         = "drift_resolved"
    spec_diff       = '{"task":"T4f","finding":"W3-002 (constitutions + mods)","status":"resolved","design_decision":["constitutions = GLOBAL by design (no project_id column; config of the system)","mods catalog = GLOBAL by design (UNIQUE mod_id; shared registry)","mod_loads = PROJECT-SCOPED (has project_id from v7; now filters correctly)"],"impl":["SaveConstitution/GetConstitution/ListConstitutions/VerifyConstitutionHash documented as global-by-design (no logic change needed)","SaveMod/GetMod/ListMods documented as global-by-design (no logic change needed)","RecordModLoad tags project_id","ListModLoads filters by project_id"],"tests":["TestProject_Constitution_GlobalByDesign PASS","TestProject_Mods_CrossProject_Loads PASS"]}'
    judge_reasoning = "T4f closed: W3-002 for constitutions and mods resolved with mixed design. constitutions GLOBAL (config system level, no project_id column by design). mods catalog GLOBAL (UNIQUE mod_id), mod_loads PROJECT-SCOPED (already had project_id column from v7; just needed WHERE filter). Both decisions documented in code with rationale. 48/48 green suite. Spec 171 still drift_detected until T4g + T5 close."
    reconciled_at   = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss.fffZ")
} | ConvertTo-Json -Depth 4 -Compress
$driftPayload | Out-File -FilePath $argsFile3 -Encoding utf8 -NoNewline
& powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_drift_log" -ArgsFile $argsFile3 -TimeoutMs 15000 2>&1 | Select-Object -First 2
Write-Host ""
Write-Host "=== T4f (Constitution + Mods) closed: drift_resolved ==="
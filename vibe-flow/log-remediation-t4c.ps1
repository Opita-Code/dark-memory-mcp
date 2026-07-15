# vibe-flow/log-remediation-t4c.ps1
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8
$wrapper = "C:\Users\Nico\AppData\Local\Temp\opencode\dark-agents-call.ps1"
$tmpDir = Join-Path $env:TEMP "dark-mem-t4c"
if (Test-Path $tmpDir) { Get-ChildItem $tmpDir -Filter *.json -ErrorAction SilentlyContinue | Remove-Item }
New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null

$root = "C:\Users\Nico\Documents\dark-memory-mcp"
$artifacts = @(
    "internal\migrate\sqlite\ddl.go",
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
    spec_diff       = '{"task":"T4c","finding":"W3-002 (brands + compliance)","status":"resolved","design_decision":"brands=scoped (composite PK), compliance=global","impl":["migration v8: vibe_brands PK -> UNIQUE(project_id, brand_id) via rename-recreate","fixup: v7 missed vibe_brands in ALTER TABLE list; v8 adds project_id column first","SaveBrandGuide tags project_id; ON CONFLICT(project_id, brand_id)","GetBrandGuide / DeleteBrandGuide / ListBrandGuides filter by project_id","SaveComplianceRule / GetComplianceRule / ListComplianceRules unchanged (global by design)","extensive doc on Store.SaveComplianceRule to flag the global-by-design choice"],"tests":["TestProject_Brands_CrossProject_CompositeUnique PASS (acme-corp + acme-eu + acme-us each see their own override)","TestProject_Compliance_GlobalByDesign PASS (rule visible from any project)"],"extras":["all SetActiveProject calls in tests now use strict t.Fatalf on error (no silent drops)","follow-up note: migration v8 should be wrapped in a single TX in production deploy (currently per-statement; low-risk in fresh DB, worth noting in BRIDGE_AND_COEXISTENCE.md)"]}'
    judge_reasoning = "T4c closed: W3-002 for brands resolved with composite PK (composite UNIQUE(project_id, brand_id)) to enable multi-tenant brand_id === 'acme-base' across sub-projects with distinct voice/visual. Migration v8 first fixes a v7 oversight (vibe_brands missing from ALTER TABLE list), then uses rename-recreate to swap the PK. Two tests added: brands cross-project and compliance global-by-design. Compliance stays global by jurisdiction (it's a property of law). Full suite 43/43 green. Spec 171 still drift_detected until T4d-T4g + T5 close."
    reconciled_at   = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss.fffZ")
} | ConvertTo-Json -Depth 4 -Compress
$driftPayload | Out-File -FilePath $argsFile3 -Encoding utf8 -NoNewline
& powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_drift_log" -ArgsFile $argsFile3 -TimeoutMs 15000 2>&1 | Select-Object -First 2
Write-Host ""
Write-Host "=== T4c (Brands + Compliance) closed: drift_resolved ==="
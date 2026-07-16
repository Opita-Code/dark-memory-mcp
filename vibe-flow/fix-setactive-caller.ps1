Set-Location "C:\Users\Nico\Documents\dark-memory-mcp"
$files = @("tests\project\project_test.go", "tests\dual_driver\store_test.go")
foreach ($f in $files) {
    $content = Get-Content $f -Raw
    $new = $content -replace 's\.SetActiveProject\("([^"]*)"\)', 's.SetActiveProject(ctx, "$1")'
    if ($new -ne $content) {
        Set-Content -Path $f -Value $new -Encoding utf8 -NoNewline
        Write-Host "[updated] $f"
    } else {
        Write-Host "[no-change] $f"
    }
}
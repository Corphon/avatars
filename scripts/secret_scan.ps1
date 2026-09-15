# S0.1: Fail if committed configs contain plaintext API key material.
# Usage: powershell -File scripts/secret_scan.ps1
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
$patterns = @(
    'api_key:\s*["'']?sk-[a-zA-Z0-9]',
    'api_key:\s*["'']?[a-zA-Z0-9_\-]{24,}["'']?\s*$'
)
$targets = Get-ChildItem -Path $root -Recurse -Include *.yaml,*.yml,*.env,*.json -File |
    Where-Object {
        $_.FullName -notmatch '\\vendor\\' -and
        $_.FullName -notmatch '\\.git\\' -and
        $_.FullName -notmatch '\\node_modules\\'
    }

$hits = @()
foreach ($file in $targets) {
    $lines = Get-Content -LiteralPath $file.FullName
    for ($i = 0; $i -lt $lines.Count; $i++) {
        $line = $lines[$i]
        if ($line -match '^\s*#' -or $line -match 'api_key_env') { continue }
        foreach ($pat in $patterns) {
            if ($line -match $pat) {
                $rel = $file.FullName.Substring($root.Length).TrimStart('\','/')
                $hits += "${rel}:$($i+1): $line"
            }
        }
    }
}

if ($hits.Count -gt 0) {
    Write-Host "SECRET SCAN FAILED — plaintext api_key material found:" -ForegroundColor Red
    $hits | ForEach-Object { Write-Host "  $_" }
    exit 1
}
Write-Host "SECRET SCAN OK — no plaintext api_key values in configs." -ForegroundColor Green
exit 0

param([switch]$Dependencies)
$ErrorActionPreference='Stop'
$repoRoot=Split-Path -Parent $PSScriptRoot
$devRoot=Join-Path $repoRoot '.cache/dev'
$pidFile=Join-Path $devRoot 'processes.json'
if (Test-Path -LiteralPath $pidFile) {
    $processes=Get-Content -LiteralPath $pidFile -Raw | ConvertFrom-Json
    foreach ($p in $processes.PSObject.Properties) {
        $process=Get-Process -Id $p.Value -ErrorAction SilentlyContinue
        $expected=Join-Path $devRoot "bin/$($p.Name).exe"
        if ($process -and -not $process.Path) { throw "Cannot verify recorded $($p.Name) process ownership. Run with the same privileges used to start it; the PID record was retained." }
        if ($process -and $process.Path -eq $expected) { Stop-Process -Id $process.Id; Write-Output "$($p.Name) stopped" }
    }
    '{}' | Set-Content -LiteralPath $pidFile -Encoding UTF8
}
if ($Dependencies) {
    & docker compose --env-file (Join-Path $devRoot 'compose.env') -f (Join-Path $repoRoot 'deploy/compose/development.yaml') stop
    if ($LASTEXITCODE -ne 0) { throw 'Could not stop development dependencies.' }
}

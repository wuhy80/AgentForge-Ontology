param([string]$Tenant='demo-tenant', [string]$Subject='developer')
$ErrorActionPreference='Stop'
$repoRoot=Split-Path -Parent $PSScriptRoot
$secretFile=Join-Path $repoRoot '.cache/dev/secrets.json'
if (-not (Test-Path -LiteralPath $secretFile)) { throw 'Run scripts/start-dev.ps1 first.' }
$secrets=Get-Content -LiteralPath $secretFile -Raw -Encoding UTF8 | ConvertFrom-Json
$priorAuth=$env:ONTOLOGY_AUTH_SECRET
$priorCache=$env:GOCACHE
$priorWork=$env:GOWORK
Push-Location (Join-Path $repoRoot 'libs/platform')
try {
    $env:ONTOLOGY_AUTH_SECRET=$secrets.AuthSecret
    $env:GOCACHE=Join-Path $repoRoot '.cache/go-build'
    $env:GOWORK='off'
    & go run ./cmd/token -tenant $Tenant -subject $Subject
    if ($LASTEXITCODE -ne 0) { throw 'Token generation failed.' }
} finally { Pop-Location; $env:ONTOLOGY_AUTH_SECRET=$priorAuth; $env:GOCACHE=$priorCache; $env:GOWORK=$priorWork }

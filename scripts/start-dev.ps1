param([switch]$DependenciesOnly, [switch]$SourceDatabases)
$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
$devRoot = Join-Path $repoRoot '.cache/dev'
New-Item -ItemType Directory -Force -Path $devRoot | Out-Null
$secretFile = Join-Path $devRoot 'secrets.json'
function New-LocalSecret {
    $bytes = New-Object byte[] 32
    $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
    try { $rng.GetBytes($bytes) } finally { $rng.Dispose() }
    return [BitConverter]::ToString($bytes).Replace('-', '').ToLowerInvariant()
}
if (-not (Test-Path -LiteralPath $secretFile)) {
    [ordered]@{
        MySQLRootPassword = New-LocalSecret
        MySQLPassword = New-LocalSecret
        AuthSecret = New-LocalSecret
        ServiceToken = New-LocalSecret
        CursorSecret = New-LocalSecret
        DataSourceKey = New-LocalSecret
        SourcePostgresPassword = New-LocalSecret
    } | ConvertTo-Json | Set-Content -LiteralPath $secretFile -Encoding UTF8
}
$secrets = Get-Content -LiteralPath $secretFile -Raw -Encoding UTF8 | ConvertFrom-Json
if (-not $secrets.PSObject.Properties['DataSourceKey']) {
    $secrets | Add-Member -NotePropertyName DataSourceKey -NotePropertyValue (New-LocalSecret)
    $secrets | ConvertTo-Json | Set-Content -LiteralPath $secretFile -Encoding UTF8
}
if (-not $secrets.PSObject.Properties['SourcePostgresPassword']) {
    $secrets | Add-Member -NotePropertyName SourcePostgresPassword -NotePropertyValue (New-LocalSecret)
    $secrets | ConvertTo-Json | Set-Content -LiteralPath $secretFile -Encoding UTF8
}
foreach ($entry in $secrets.PSObject.Properties) {
    if ($entry.Value -notmatch '^[a-f0-9]{48,128}$') { throw "Invalid local secret: $($entry.Name)" }
}
$composeEnv = Join-Path $devRoot 'compose.env'
@("ONTOLOGY_MYSQL_ROOT_PASSWORD=$($secrets.MySQLRootPassword)", "ONTOLOGY_MYSQL_PASSWORD=$($secrets.MySQLPassword)", "ONTOLOGY_SOURCE_POSTGRES_PASSWORD=$($secrets.SourcePostgresPassword)") | Set-Content -LiteralPath $composeEnv -Encoding ASCII
$compose = Join-Path $repoRoot 'deploy/compose/development.yaml'
$composeProfiles = if ($SourceDatabases) { @('--profile', 'sources') } else { @() }
& docker compose --env-file $composeEnv -f $compose @composeProfiles up -d --wait --wait-timeout 90
if ($LASTEXITCODE -ne 0) { throw 'Development dependencies did not become ready. Start Docker Desktop and inspect docker compose logs.' }
if ($DependenciesOnly) { Write-Output 'Selected development dependencies are ready.'; return }

$adminFile = Join-Path $devRoot 'admin.json'
if (-not (Test-Path -LiteralPath $adminFile)) {
    [ordered]@{Tenant='demo-tenant'; Username='admin'; Password=(New-LocalSecret)} | ConvertTo-Json | Set-Content -LiteralPath $adminFile -Encoding UTF8
}
$initialAdmin = Get-Content -LiteralPath $adminFile -Raw -Encoding UTF8 | ConvertFrom-Json

$envNames = @('GOCACHE','GOWORK','ONTOLOGY_MYSQL_DSN','ONTOLOGY_AUTH_SECRET','ONTOLOGY_SERVICE_TOKEN','ONTOLOGY_CURSOR_SECRET','ONTOLOGY_DATA_SOURCE_KEY','ONTOLOGY_OPA_URL','ONTOLOGY_METADATA_URL','ONTOLOGY_DATA_URL','ONTOLOGY_LISTEN','ONTOLOGY_PUBLIC_ORIGIN','ONTOLOGY_BOOTSTRAP_TENANT','ONTOLOGY_BOOTSTRAP_USERNAME','ONTOLOGY_BOOTSTRAP_PASSWORD','ONTOLOGY_PYTHON_IMAGE')
$savedEnvironment = @{}
foreach ($name in $envNames) { $savedEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }
try {
    $env:GOCACHE = Join-Path $repoRoot '.cache/go-build'
    $env:GOWORK = 'off'
    $env:ONTOLOGY_AUTH_SECRET = $secrets.AuthSecret
    $env:ONTOLOGY_SERVICE_TOKEN = $secrets.ServiceToken
    $env:ONTOLOGY_CURSOR_SECRET = $secrets.CursorSecret
    $env:ONTOLOGY_DATA_SOURCE_KEY = $secrets.DataSourceKey
    $env:ONTOLOGY_OPA_URL = 'http://127.0.0.1:28181'
    $env:ONTOLOGY_METADATA_URL = 'http://127.0.0.1:18080'
    $env:ONTOLOGY_DATA_URL = 'http://127.0.0.1:18081'
    $env:ONTOLOGY_PUBLIC_ORIGIN = 'http://127.0.0.1:5173'
    if (-not $env:ONTOLOGY_PYTHON_IMAGE) { $env:ONTOLOGY_PYTHON_IMAGE = 'python:3.12-alpine' }
    $imageProbePreference = $ErrorActionPreference
    try {
        # A missing image is expected on first startup. Windows PowerShell 5
        # must not turn native stderr into a terminating error before the pull.
        $ErrorActionPreference = 'Continue'
        & docker image inspect $env:ONTOLOGY_PYTHON_IMAGE --format '{{.Id}}' 2>$null | Out-Null
        $imageProbeExitCode = $LASTEXITCODE
    } finally { $ErrorActionPreference = $imageProbePreference }
    if ($imageProbeExitCode -ne 0) {
        & docker pull $env:ONTOLOGY_PYTHON_IMAGE
        if ($LASTEXITCODE -ne 0) { throw 'Python runtime image is unavailable; inspect the image setting and Docker access.' }
    }
    $binRoot = Join-Path $devRoot 'bin'
    New-Item -ItemType Directory -Force -Path $binRoot | Out-Null
    $processes = @{}
    $pidFile = Join-Path $devRoot 'processes.json'
    if (Test-Path -LiteralPath $pidFile) {
        $existing = Get-Content -LiteralPath $pidFile -Raw | ConvertFrom-Json
        foreach ($p in $existing.PSObject.Properties) {
            $process = Get-Process -Id $p.Value -ErrorAction SilentlyContinue
            if ($process -and -not $process.Path) { throw "Cannot verify recorded $($p.Name) process ownership. Run with the same privileges used to start it; the PID record was retained." }
            if ($process -and $process.Path -eq (Join-Path $binRoot "$($p.Name).exe")) { $processes[$p.Name] = $p.Value }
        }
    }
    foreach ($service in @('metadata','data','execution')) {
        if ($processes.ContainsKey($service)) { continue }
        $binary = Join-Path $binRoot "$service.exe"
        Push-Location (Join-Path $repoRoot "services/$service")
        try { & go build -o $binary ./cmd/server; if ($LASTEXITCODE -ne 0) { throw "$service build failed" } } finally { Pop-Location }
        $port = @{metadata=23306; data=23307; execution=23308}[$service]
        $env:ONTOLOGY_MYSQL_DSN = "ontology:$($secrets.MySQLPassword)@tcp(127.0.0.1:$port)/ontology_${service}?charset=utf8mb4&parseTime=true&loc=UTC"
        & $binary -migrate
        if ($LASTEXITCODE -ne 0) { throw "$service migration needs inspection; no server was started for this service" }
        if ($service -eq 'metadata') {
            $env:ONTOLOGY_BOOTSTRAP_TENANT = $initialAdmin.Tenant
            $env:ONTOLOGY_BOOTSTRAP_USERNAME = $initialAdmin.Username
            $env:ONTOLOGY_BOOTSTRAP_PASSWORD = $initialAdmin.Password
            & $binary -bootstrap-admin
            if ($LASTEXITCODE -ne 0) { throw 'Administrator initialization failed; no Metadata server was started.' }
            $env:ONTOLOGY_BOOTSTRAP_PASSWORD = $null
            $env:ONTOLOGY_BOOTSTRAP_USERNAME = $null
            $env:ONTOLOGY_BOOTSTRAP_TENANT = $null
        }
        $env:ONTOLOGY_LISTEN = @{metadata='127.0.0.1:18080'; data='127.0.0.1:18081'; execution='127.0.0.1:18082'}[$service]
        $process = Start-Process -FilePath $binary -WorkingDirectory $repoRoot -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $devRoot "$service.stdout.log") -RedirectStandardError (Join-Path $devRoot "$service.stderr.log")
        $processes[$service] = $process.Id
        $processes | ConvertTo-Json | Set-Content -LiteralPath $pidFile -Encoding UTF8
    }
    foreach ($port in @(18080,18081,18082)) {
        $ready = $false
        for ($attempt=0; $attempt -lt 20; $attempt++) {
            try { $response = Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:$port/readyz" -TimeoutSec 2; if ($response.StatusCode -eq 200) { $ready = $true; break } } catch {}
            Start-Sleep -Milliseconds 500
        }
        if (-not $ready) { throw "Service on $port is not ready. Inspect .cache/dev/*.stderr.log." }
    }
    foreach ($service in @('metadata','data','execution')) {
        $process = Get-Process -Id $processes[$service] -ErrorAction SilentlyContinue
        if (-not $process -or $process.Path -ne (Join-Path $binRoot "$service.exe")) {
            throw "Cannot verify the $service server process. Another application may own the port; inspect .cache/dev/*.stderr.log."
        }
    }
    Write-Output 'Metadata: http://127.0.0.1:18080 ; Data: http://127.0.0.1:18081 ; Execution: http://127.0.0.1:18082'
    Write-Output 'Sign in with the account initialized in .cache/dev/admin.json. Existing account passwords are never reset by startup. Stop services with scripts/stop-dev.ps1.'
} finally {
    foreach ($name in $envNames) { [Environment]::SetEnvironmentVariable($name, $savedEnvironment[$name], 'Process') }
}

param([switch]$Integration, [switch]$SourceIntegration, [switch]$ExecutionIntegration)
$ErrorActionPreference='Stop'
$repoRoot=Split-Path -Parent $PSScriptRoot
$names=@('GOCACHE','GOWORK','MYSQL_TEST_DSN','MYSQL_SOURCE_TEST_DSN','POSTGRES_SOURCE_TEST_DSN','METADATA_TEST_DSN','IDENTITY_TEST_DSN','IDENTITY_TEST_OPA_URL','ONTOLOGY_TEST_METADATA_DSN','ONTOLOGY_TEST_DATA_DSN','ONTOLOGY_TEST_OPA_URL','STORAGE_RELOCATION_TEST_DSN','EXECUTION_TEST_DSN','EXECUTION_TEST_DOCKER','ONTOLOGY_TEST_EXECUTION_DSN')
$prior=@{}
foreach($name in $names){$prior[$name]=[Environment]::GetEnvironmentVariable($name,'Process')}
try {
    if($SourceIntegration -or $ExecutionIntegration){$Integration=$true}
    $env:GOCACHE=Join-Path $repoRoot '.cache/go-build'
    $env:GOWORK='off'
    if($Integration){
        $env:MYSQL_SOURCE_TEST_DSN=$null
        $file=Join-Path $repoRoot '.cache/dev/secrets.json'
        if(-not(Test-Path -LiteralPath $file)){throw 'Run scripts/start-dev.ps1 -DependenciesOnly first.'}
        $secrets=Get-Content -LiteralPath $file -Raw -Encoding UTF8|ConvertFrom-Json
        $env:ONTOLOGY_TEST_METADATA_DSN="ontology:$($secrets.MySQLPassword)@tcp(127.0.0.1:23306)/ontology_metadata?charset=utf8mb4&parseTime=true&loc=UTC"
        $env:ONTOLOGY_TEST_DATA_DSN="ontology:$($secrets.MySQLPassword)@tcp(127.0.0.1:23307)/ontology_data?charset=utf8mb4&parseTime=true&loc=UTC"
        $env:ONTOLOGY_TEST_OPA_URL='http://127.0.0.1:28181'
        $env:EXECUTION_TEST_DSN="ontology:$($secrets.MySQLPassword)@tcp(127.0.0.1:23308)/ontology_execution?charset=utf8mb4&parseTime=true&loc=UTC"
        $env:ONTOLOGY_TEST_EXECUTION_DSN=$env:EXECUTION_TEST_DSN
        $env:EXECUTION_TEST_DOCKER=if($ExecutionIntegration){'1'}else{$null}
        $env:METADATA_TEST_DSN=$env:ONTOLOGY_TEST_METADATA_DSN
        $env:IDENTITY_TEST_DSN=$env:ONTOLOGY_TEST_METADATA_DSN
        $env:IDENTITY_TEST_OPA_URL=$env:ONTOLOGY_TEST_OPA_URL
        $env:STORAGE_RELOCATION_TEST_DSN=$env:ONTOLOGY_TEST_DATA_DSN
        $env:POSTGRES_SOURCE_TEST_DSN=$null
        if($SourceIntegration){
            if(-not $secrets.SourcePostgresPassword){throw 'Run scripts/start-dev.ps1 -DependenciesOnly -SourceDatabases first.'}
            $sourcePassword=[Uri]::EscapeDataString($secrets.SourcePostgresPassword)
            $env:POSTGRES_SOURCE_TEST_DSN="postgres://ontology:$sourcePassword@127.0.0.1:25432/ontology_sources?sslmode=disable"
        }
    } else {
        foreach($name in $names|Where-Object{$_ -notin @('GOCACHE','GOWORK')}){[Environment]::SetEnvironmentVariable($name,$null,'Process')}
    }
    foreach($module in @('contracts','libs/storage','libs/platform','services/metadata','services/data','services/execution','services/mcp','tests/integration','tools/storage_relocation')){
        if($Integration){$env:MYSQL_TEST_DSN=if($module -eq 'services/data'){$env:ONTOLOGY_TEST_DATA_DSN}else{$env:ONTOLOGY_TEST_METADATA_DSN}}
        Push-Location(Join-Path $repoRoot $module)
        try {
            & go test -count=1 ./...
            if($LASTEXITCODE -ne 0){throw "$module tests failed"}
            & go vet ./...
            if($LASTEXITCODE -ne 0){throw "$module vet failed"}
        } finally {Pop-Location}
    }
    & go vet (Join-Path $repoRoot 'deploy/execution/go/runner.go')
    if($LASTEXITCODE -ne 0){throw 'Go runtime launcher vet failed'}
} finally {foreach($name in $names){[Environment]::SetEnvironmentVariable($name,$prior[$name],'Process')}}

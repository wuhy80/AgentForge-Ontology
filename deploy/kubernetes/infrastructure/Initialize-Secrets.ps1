param(
    [string]$CredentialDirectory = 'D:\AgentForgeData\credentials',
    [string]$Docker = 'D:\Programs\Docker\Docker\resources\bin\docker.exe',
    [string]$Kubectl = 'D:\Programs\Docker\Docker\resources\bin\kubectl.exe'
)

$ErrorActionPreference = 'Stop'
$env:Path = (Split-Path -Parent $Docker) + ';' + $env:Path
$utf8 = New-Object Text.UTF8Encoding($false)
New-Item -ItemType Directory -Path $CredentialDirectory -Force | Out-Null
$currentSid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
& icacls.exe $CredentialDirectory /inheritance:r /grant:r `
    ('*' + $currentSid + ':(OI)(CI)F') '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' /q | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'Cannot restrict credential directory permissions.' }

function New-RandomPassword {
    $bytes = New-Object byte[] 32
    $generator = [Security.Cryptography.RandomNumberGenerator]::Create()
    try { $generator.GetBytes($bytes) } finally { $generator.Dispose() }
    return ([BitConverter]::ToString($bytes)).Replace('-', '').ToLowerInvariant()
}

$credentialPath = Join-Path $CredentialDirectory 'infrastructure.json'
if (-not (Test-Path -LiteralPath $credentialPath)) {
    $credentials = [ordered]@{
        mysql = @{ username = 'agentforge'; database = 'agentforge'; password = (New-RandomPassword); rootPassword = (New-RandomPassword) }
        redis = @{ username = 'default'; password = (New-RandomPassword) }
        influxdb = @{ database = 'agentforge' }
    }
    [IO.File]::WriteAllText($credentialPath, ($credentials | ConvertTo-Json -Depth 6), $utf8)
}

$tokenPath = Join-Path $CredentialDirectory 'influxdb-admin-token.json'
if (-not (Test-Path -LiteralPath $tokenPath)) {
    $tokenOutput = & $Docker --context desktop-linux run --rm --user 0 `
        --mount ('type=bind,source=' + $CredentialDirectory + ',target=/credentials') `
        influxdb:3.12.1-core influxdb3 create token --admin --offline `
        --output-file /credentials/influxdb-admin-token.json 2>&1
    if ($LASTEXITCODE -ne 0) { throw 'InfluxDB offline token generation failed. Verify the image and token CLI options; captured output is withheld to protect the token.' }
}
$credentials = Get-Content -LiteralPath $credentialPath -Raw | ConvertFrom-Json
$tokenFile = Get-Content -LiteralPath $tokenPath -Raw | ConvertFrom-Json
if (-not $tokenFile.token) { throw 'InfluxDB token file is empty.' }
$credentials.influxdb | Add-Member -MemberType NoteProperty -Name adminToken -Value $tokenFile.token -Force
[IO.File]::WriteAllText($credentialPath, ($credentials | ConvertTo-Json -Depth 6), $utf8)

$namespace = @{ apiVersion = 'v1'; kind = 'Namespace'; metadata = @{ name = 'agentforge-infra' } }
$namespace | ConvertTo-Json -Depth 6 | & $Kubectl --context docker-desktop apply -f -
if ($LASTEXITCODE -ne 0) { throw 'Cannot create infrastructure namespace.' }

$sha256 = [Security.Cryptography.SHA256]::Create()
try {
    $passwordHash = ([BitConverter]::ToString($sha256.ComputeHash([Text.Encoding]::UTF8.GetBytes($credentials.redis.password)))).Replace('-', '').ToLowerInvariant()
}
finally { $sha256.Dispose() }
$secrets = @(
    @{ Name = 'mysql-auth'; Values = @{ 'root-password' = $credentials.mysql.rootPassword; 'app-password' = $credentials.mysql.password } },
    @{ Name = 'redis-auth'; Values = @{ password = $credentials.redis.password; 'users.acl' = "user default on #$passwordHash ~* &* +@all`n" } },
    @{ Name = 'influxdb-auth'; Values = @{ 'admin-token' = $tokenFile.token; 'admin-token.json' = ($tokenFile | ConvertTo-Json -Compress) } }
)
foreach ($secret in $secrets) {
    $resource = @{
        apiVersion = 'v1'; kind = 'Secret'; type = 'Opaque'
        metadata = @{ name = $secret.Name; namespace = 'agentforge-infra' }
        stringData = $secret.Values
    }
    $resource | ConvertTo-Json -Depth 10 | & $Kubectl --context docker-desktop apply -f -
    if ($LASTEXITCODE -ne 0) { throw ('Cannot apply secret: ' + $secret.Name) }
}
Write-Output ('Credentials saved locally: ' + $credentialPath)

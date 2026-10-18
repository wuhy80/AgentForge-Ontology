param(
    [string]$RuntimeDirectory = 'D:\AgentForgeData\access',
    [string]$Kubectl = 'D:\Programs\Docker\Docker\resources\bin\kubectl.exe'
)

$ErrorActionPreference = 'Stop'
$mutex = New-Object Threading.Mutex($false, 'Local\AgentForgeInfrastructurePortForward')
if (-not $mutex.WaitOne(0)) { exit 0 }

$connections = @(
    @{ Name = 'mysql'; Mapping = '13306:3306'; Process = $null },
    @{ Name = 'redis'; Mapping = '16379:6379'; Process = $null },
    @{ Name = 'influxdb'; Mapping = '18181:8181'; Process = $null }
)
New-Item -ItemType Directory -Path $RuntimeDirectory -Force | Out-Null

try {
    while ($true) {
        foreach ($connection in $connections) {
            if ($null -eq $connection.Process -or $connection.Process.HasExited) {
                $connection.Process = Start-Process -FilePath $Kubectl -ArgumentList @(
                    '--context', 'docker-desktop', '-n', 'agentforge-infra',
                    'port-forward', '--address=127.0.0.1', '--pod-running-timeout=30s',
                    ('service/' + $connection.Name), $connection.Mapping
                ) -WindowStyle Hidden -PassThru `
                    -RedirectStandardOutput (Join-Path $RuntimeDirectory ($connection.Name + '.log')) `
                    -RedirectStandardError (Join-Path $RuntimeDirectory ($connection.Name + '.error.log'))
            }
        }
        Start-Sleep -Seconds 5
    }
}
finally {
    foreach ($connection in $connections) {
        if ($null -ne $connection.Process -and -not $connection.Process.HasExited) {
            $connection.Process.Kill()
        }
    }
    $mutex.ReleaseMutex()
    $mutex.Dispose()
}

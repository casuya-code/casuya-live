param(
    [switch]$Stop
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$tools = Join-Path $root ".tools"
$logs = Join-Path $tools "logs"
$bin = Join-Path $tools "bin"
$redisBin = Join-Path $tools "redis"
New-Item -ItemType Directory -Path $logs -Force | Out-Null

function Start-Svc {
    param(
        [string]$Name,
        [string]$File,
        [string[]]$ArgsList,
        [string]$WorkDir,
        [hashtable]$Env
    )
    foreach ($k in $Env.Keys) {
        [System.Environment]::SetEnvironmentVariable($k, [string]$Env[$k], "Process")
    }
    $out = Join-Path $logs "$Name.out.log"
    $err = Join-Path $logs "$Name.err.log"
    # PowerShell 5.1 rejects BOTH an empty and a null -ArgumentList; omit the
    # parameter entirely when the service takes no arguments.
    $sp = @{
        FilePath           = $File
        WorkingDirectory   = $WorkDir
        WindowStyle        = "Hidden"
        RedirectStandardOutput = $out
        RedirectStandardError  = $err
        PassThru           = $true
    }
    if ($ArgsList -and $ArgsList.Count -gt 0) { $sp.ArgumentList = $ArgsList }
    $p = Start-Process @sp
    $started = (Get-Date)
    while (-not $p.HasExited -and (Get-Date) -lt $started.AddSeconds(1)) { Start-Sleep -Milliseconds 100 }
    if ($p.HasExited) {
        Write-Host "[$Name] FAILED (exit $($p.ExitCode))" -ForegroundColor Red
        $errTxt = if (Test-Path $err) { Get-Content $err -Tail 5 } else { "" }
        $errTxt | ForEach-Object { Write-Host "    $_" -ForegroundColor DarkGray }
    } else {
        Write-Host "[$Name] started (PID $($p.Id))" -ForegroundColor Green
    }
    $p.Id | Set-Content -Path (Join-Path $logs "$Name.pid")
}

function Test-Redis {
    try {
        $cli = Join-Path $redisBin "redis-cli.exe"
        $ok = & $cli -p 6379 PING 2>$null
        return ($ok -eq "PONG")
    } catch { return $false }
}

if ($Stop) {
    Write-Host "Stopping CASUYA-LIVE..." -ForegroundColor Cyan
    Get-ChildItem $logs -Filter *.pid -ErrorAction SilentlyContinue | ForEach-Object {
        $pidVal = Get-Content $_.FullName
        $proc = Get-Process -Id $pidVal -ErrorAction SilentlyContinue
        if ($proc) { Stop-Process -Id $pidVal -Force; Write-Host "  stopped $($_.BaseName) (PID $pidVal)" }
        Remove-Item $_.FullName -Force
    }
    Get-Process redis-server -ErrorAction SilentlyContinue | Stop-Process -Force
    Write-Host "Done." -ForegroundColor Cyan
    exit 0
}

Write-Host "CASUYA-LIVE local stack" -ForegroundColor Cyan

$svc = @()
if (-not (Test-Redis)) {
    Start-Svc -Name "redis" -File (Join-Path $redisBin "redis-server.exe") -ArgsList @("--port","6379","--dir",$tools) -WorkDir $redisBin -Env @{}
} else {
    Write-Host "[redis] already running" -ForegroundColor Green
}

Start-Sleep -Milliseconds 300

$svc += Start-Svc -Name "relay" -File "node" -ArgsList @((Join-Path $root "web-interface-js\backend\server.js")) `
    -WorkDir (Join-Path $root "web-interface-js\backend") `
    -Env @{ "REDIS_URL" = "redis://localhost:6379/0"; "PORT" = "8080"; "ADMIN_SECRET" = "local-admin-secret" }

$svc += Start-Svc -Name "analytics" -File (Join-Path $root "analytics-engine-py\.venv\Scripts\python.exe") `
    -ArgsList @("src\main.py") -WorkDir (Join-Path $root "analytics-engine-py") `
    -Env @{ "REDIS_URL" = "redis://localhost:6379/0"; "INTERNAL_AUTH_SECRET" = "local-dev-secret"; "EXECUTION_SERVICE_URL" = "http://localhost:8081" }

$svc += Start-Svc -Name "mock" -File "node" -ArgsList @((Join-Path $root "mock-vendor\server.js")) `
    -WorkDir (Join-Path $root "mock-vendor") `
    -Env @{ "INTERNAL_AUTH_SECRET" = "local-dev-secret"; "MOCK_VENDOR_PORT" = "19999"; "MOCK_TRADE_PORT" = "19998"; "REDIS_URL" = "redis://localhost:6379/0" }

$svc += Start-Svc -Name "ingestor" -File (Join-Path $bin "ingestor.exe") -WorkDir (Join-Path $root "data-ingestion-go") `
    -Env @{ "REDIS_URL" = "redis://localhost:6379/0"; "PROVIDER_WS_URL" = "ws://localhost:19999/vendor" }

$svc += Start-Svc -Name "executor" -File (Join-Path $bin "executor.exe") -WorkDir (Join-Path $root "execution-engine-go") `
    -Env @{ "REDIS_URL" = "redis://localhost:6379/0"; "BOOKMAKER_WS_URL" = "ws://localhost:19998/trade"; "BOOKMAKER_API_KEY" = "local-test-key"; "INTERNAL_AUTH_SECRET" = "local-dev-secret"; "PORT" = "8081" }

$svc += Start-Svc -Name "frontend" -File "npm.cmd" -ArgsList @("run","dev") `
    -WorkDir (Join-Path $root "web-interface-js\frontend") `
    -Env @{ "NEXT_PUBLIC_WS_BACKEND_URL" = "ws://localhost:8080/ws"; "PORT" = "3000" }

Write-Host ""
Write-Host "Health checks:" -ForegroundColor Cyan

$checks = @(
    @{ Name = "relay    "; Url = "http://localhost:8080/healthz" },
    @{ Name = "executor "; Url = "http://localhost:8081/" },
    @{ Name = "dashboard"; Url = "http://localhost:3000" }
)
foreach ($c in $checks) {
    $ok = $false
    for ($i = 0; $i -lt 20; $i++) {
        try {
            $r = Invoke-WebRequest -Uri $c.Url -UseBasicParsing -TimeoutSec 2
            if ($r.StatusCode -lt 500) { $ok = $true; break }
        } catch {
            if ($_.Exception.Response) { $ok = $true; break }
        }
        Start-Sleep -Milliseconds 500
    }
    Write-Host ("  {0}  {1}" -f $c.Name, $(if ($ok) { "UP" } else { "DOWN" })) -ForegroundColor $(if ($ok) { "Green" } else { "Red" })
}

Write-Host ""
Write-Host "Dashboard: http://localhost:3000" -ForegroundColor Cyan
Write-Host "Logs: $logs" -ForegroundColor DarkGray
Write-Host "Stop: .\start-local.ps1 -Stop" -ForegroundColor DarkGray
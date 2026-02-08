param(
    [int]$DurationSec = 120,
    [int]$SampleIntervalSec = 10,
    [int]$ConsumeN = 20
)

$ErrorActionPreference = "Stop"

$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$root = Resolve-Path (Join-Path $here "..")
Set-Location $root

$compose = "docker compose -f infra/docker-compose.yml"
$container = "infra-collector-go-1"
$stream = "COINMARK_RAW"

Write-Host "== Collector PoC Check =="
Write-Host "DurationSec=$DurationSec SampleIntervalSec=$SampleIntervalSec ConsumeN=$ConsumeN"

Write-Host "`n[1/4] Service status"
Invoke-Expression "$compose ps collector-go nats"

Write-Host "`n[2/4] Resource sampling (collector-go)"
$samples = @()
$loops = [Math]::Max(1, [Math]::Ceiling($DurationSec / [double]$SampleIntervalSec))
for ($i = 0; $i -lt $loops; $i++) {
    $line = docker stats --no-stream --format "{{.Name}}|{{.CPUPerc}}|{{.MemUsage}}|{{.MemPerc}}" |
        Select-String -Pattern "^$container\|" |
        ForEach-Object { $_.Line }

    if ($line) {
        $parts = $line -split "\|"
        $samples += [PSCustomObject]@{
            Time = Get-Date
            CpuPerc = $parts[1]
            MemUsage = $parts[2]
            MemPerc = $parts[3]
        }
        Write-Host "[$($samples.Count)] $($parts[1])  $($parts[2])  $($parts[3])"
    }
    else {
        Write-Host "[$($samples.Count + 1)] collector container not found"
    }

    Start-Sleep -Seconds $SampleIntervalSec
}

Write-Host "`n[3/4] JetStream stream check"
$streamMessages = 0
try {
    $jsz = Invoke-RestMethod -Uri "http://localhost:8222/jsz?streams=true" -Method Get
    $target = $null
    if ($jsz.streams -is [System.Array]) {
        $target = $jsz.streams | Where-Object { $_.name -eq $stream } | Select-Object -First 1
    }
    if (-not $target -and $jsz.account_details) {
        foreach ($acct in $jsz.account_details) {
            if ($acct.stream_detail) {
                $target = $acct.stream_detail | Where-Object { $_.name -eq $stream } | Select-Object -First 1
                if ($target) { break }
            }
        }
    }
    if ($null -ne $target) {
        $streamMessages = [int64]$target.state.messages
        Write-Host "Stream $stream messages: $streamMessages"
    }
    else {
        Write-Host "Stream $stream not found"
    }
}
catch {
    Write-Host "JetStream query failed: $($_.Exception.Message)"
}

Write-Host "`n[4/4] Collector log summary"
$logs = Invoke-Expression "$compose logs --since=${DurationSec}s --tail=400 collector-go"
$heartbeat = @($logs | Select-String -Pattern "collector heartbeat")
$reconnect = @($logs | Select-String -Pattern "(trade|depth) ws disconnected")
$sendFail = @($logs | Select-String -Pattern "nats publish failed")

Write-Host "heartbeat lines: $($heartbeat.Count)"
Write-Host "reconnect lines: $($reconnect.Count)"
Write-Host "send_fail lines: $($sendFail.Count)"

Write-Host "`n== Summary =="
if ($samples.Count -gt 0) {
    $cpuValues = $samples |
        ForEach-Object { ($_.CpuPerc -replace '%', '') } |
        ForEach-Object { [double]$_ }
    $cpuAvg = ($cpuValues | Measure-Object -Average).Average
    $cpuMax = ($cpuValues | Measure-Object -Maximum).Maximum
    Write-Host ("CPU avg={0:N2}% max={1:N2}%" -f $cpuAvg, $cpuMax)
}

Write-Host "stream messages: $streamMessages"
Write-Host "reconnect=$($reconnect.Count), send_fail=$($sendFail.Count)"

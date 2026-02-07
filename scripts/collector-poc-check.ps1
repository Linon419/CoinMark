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
$topic = "coinmark.raw_trade.poc"

Write-Host "== Collector PoC Check =="
Write-Host "DurationSec=$DurationSec SampleIntervalSec=$SampleIntervalSec ConsumeN=$ConsumeN"

Write-Host "`n[1/4] Service status"
Invoke-Expression "$compose ps collector-go redpanda"

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

Write-Host "`n[3/4] Topic sample consume"
$consumeLines = @()
try {
    $consume = Invoke-Expression "$compose exec -T redpanda rpk topic consume $topic --brokers localhost:9092 -n $ConsumeN -f '%v'"
    $consumeLines = @($consume | Where-Object { $_ -and $_.Trim() -ne "" })
    if ($consumeLines.Count -eq 1 -and $consumeLines[0] -match '\}\{') {
        $consumeLines = @([regex]::Split($consumeLines[0], '(?<=\})(?=\{)') | Where-Object { $_ -and $_.Trim() -ne "" })
    }
    Write-Host "Consumed messages: $($consumeLines.Count)"
    $consumeLines | Select-Object -First 3 | ForEach-Object { Write-Host $_ }
}
catch {
    Write-Host "Consume failed: $($_.Exception.Message)"
}

Write-Host "`n[4/4] Collector log summary"
$logs = Invoke-Expression "$compose logs --since=${DurationSec}s --tail=400 collector-go"
$heartbeat = @($logs | Select-String -Pattern "collector heartbeat")
$reconnect = @($logs | Select-String -Pattern "trade ws disconnected")
$sendFail = @($logs | Select-String -Pattern "kafka send failed")

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

Write-Host "sampled messages: $($consumeLines.Count)"
Write-Host "reconnect=$($reconnect.Count), send_fail=$($sendFail.Count)"

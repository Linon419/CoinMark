$ErrorActionPreference = "Stop"

$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$root = Resolve-Path (Join-Path $here "..")
$api = Join-Path $root "apps\\api"

Set-Location $api

$env:DATABASE_URL = "sqlite+aiosqlite:///./coinmark.db"
$env:REDIS_URL = "redis://localhost:6379/0"
$env:API_LOG_LEVEL = "info"

Write-Host "启动 API：http://127.0.0.1:8000"
.\.venv\Scripts\python -m uvicorn coinmark_api.main:app --host 127.0.0.1 --port 8000


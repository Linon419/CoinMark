# CoinMark API（FastAPI + Ingest Worker）

## 运行方式

### 方式 A：Docker（推荐）

从项目根目录启动：
- `cd CoinMark`
- `docker compose -f infra\\docker-compose.yml up -d --build`

服务：
- API：`http://localhost:8000`
- Web：`http://localhost:5173`

### 方式 B：本地 Python（无 Docker）

1) 安装依赖（示例）：
- `python -m venv .venv`
- `.\.venv\Scripts\pip install -r requirements.txt`

2) 设置环境变量（示例）：
- `DATABASE_URL=sqlite+aiosqlite:///./coinmark.db`
- `REDIS_URL=redis://localhost:6379/0`

3) 启动 API：
- `.\.venv\Scripts\python -m uvicorn coinmark_api.main:app --host 127.0.0.1 --port 8000`

4) 启动 Ingest（另开一个终端）：
- `.\.venv\Scripts\python -m coinmark_api.ingest.run`

> 提示：全量订阅会占用较多资源，开发期可用 `INGEST_SYMBOL_LIMIT=50` 先跑通链路。

## Telegram 双 Bot（Long Polling）

本项目支持两个 Telegram Bot：

- 通知 Bot：推送市场异动（私聊）
- 查询 Bot：命令查询价格/资金/吸筹/异动

### 1) 配置环境变量（见 `.env.example`）

- `TG_ENABLED=true`
- `TG_NOTIFY_BOT_TOKEN=...`
- `TG_QUERY_BOT_TOKEN=...`
- `TG_NOTIFY_CHAT_ID=...`
- 可选：
  - `TG_NOTIFY_MARKET=swap`
  - `TG_NOTIFY_BATCH_WINDOW_SEC=30`
  - `TG_NOTIFY_BATCH_MAX_ITEMS=5`
  - `TG_NOTIFY_MIN_LEVEL=warning`

### 2) Docker 启动

在仓库根目录：

- `docker compose -f infra/docker-compose.yml up -d --build tg-bot`

### 3) 查询 Bot 命令

- `/help`
- `/price BTCUSDT`
- `/fund BTCUSDT`
- `/absorb BTCUSDT`
- `/anomaly BTCUSDT`

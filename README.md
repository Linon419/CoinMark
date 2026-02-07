# CoinMark（CoinArch）

一个面向 **Binance Spot + USDT-M Futures** 的市场数据聚合与异动监控项目：
- 全量订阅 `*USDT` 交易对的 `aggTrade` WebSocket（Spot + Swap 两套连接池）
- 按时间桶（默认 `15m/1h/1d`）聚合成交额，并严格拆分 **主动买入/主动卖出成交额**
- 提供 CoinArchBot 榜单 API：
  - 持仓与市值比例排行（`OI Notional / Market Cap`，市值默认取 Binance 网页端 bapi 并标注来源）
  - 当前资金费率 Top15（`premiumIndex.lastFundingRate`）
  - 多空量能排行（`taker_buy/sell notional` 相对历史均值的倍数）

## 快速开始（本地）

前置：
- 推荐：Docker Desktop（最接近线上：PostgreSQL + Redis）
- 如果你当前机器没有 Docker，也可以先用 SQLite 模式跑通（用于开发/演示，不建议当作线上方案）

启动：
1. 复制环境变量示例：
   - `copy CoinMark\.env.example CoinMark\.env`
2. 启动：
   - `cd CoinMark`
   - `docker compose -f infra\docker-compose.yml up -d --build`
3. 健康检查：
   - `http://localhost:8000/healthz`

### 无 Docker（SQLite 开发模式）

在 `CoinMark/apps/api` 下创建虚拟环境并安装依赖后，设置环境变量运行：
- `DATABASE_URL=sqlite+aiosqlite:///./coinmark.db`
- `REDIS_URL=redis://localhost:6379/0`（当前版本未强依赖 Redis，可先填占位）

然后启动 API（示例）：
- `python -m uvicorn coinmark_api.main:app --host 127.0.0.1 --port 8000`

已提供脚本（Windows）：
- 启动 API（SQLite）：`powershell -ExecutionPolicy Bypass -File scripts\\dev-sqlite.ps1`
- Smoke（单测 + WS 落库）：`powershell -ExecutionPolicy Bypass -File scripts\\smoke.ps1`

> 注意：SQLite 模式主要用于你本机开发验证；真实跑全量订阅与高并发，请使用 PostgreSQL（compose 或云上）。

## 目录结构

- `apps/api`：FastAPI + WS Ingest Worker
- `apps/web`：前端（Vite + React，最小可视化页面）
- `infra`：docker-compose、Nginx 等
- `docs`：设计文档与部署文档

## 指标口径声明（重要）

本项目所有展示指标必须来自 Binance 官方 API/WebSocket 原始字段，或对其做可验证聚合；不使用“资金流入/资金往来”等误导性表述。

“市值（Market Cap）”默认使用 Binance 网页端未文档化接口计算（`market_cap ~= cs * c`），该来源**不保证稳定**，系统中会标注 `market_cap_source` 与更新时间；你也可以替换为 CoinGecko/CMC 等更稳定的数据源。

## 已实现 API（MVP）

- `GET /healthz`
- `GET /api/symbol/getpairs?market=spot|swap`
- `GET /api/aggregate/basicinfo?market=spot|swap&limit=50`
- `GET /api/kline/GetKlines?market=spot|swap&symbol=BTCUSDT&interval=1h&limit=200`
- `GET /api/bot/fundingRateTop?limit=15&order=abs|desc|asc`
- `GET /api/bot/longShortVolumeRank?market=spot|swap&bucket=15m|1h|1d&limit=10`
- `GET /api/bot/oiMarketCapRank?limit=15`

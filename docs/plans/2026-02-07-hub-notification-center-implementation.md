# Hub 推送 + 通知中心实施说明（无鉴权版）

## 1. 后端
- WebSocket 地址：`/api/hub/market`
- 连接成功消息：`{ kind: "connected", connectionId, ts }`
- 心跳：服务端下发 `ping`，客户端回 `{"op":"ping"}`
- 订阅协议：
  - 请求：`{"op":"subscribe","markets":["swap"],"symbols":["BTCUSDT"],"types":["ANOMALY_BREAKOUT_UP"]}`
  - 响应：`{ kind:"subscribed", markets, symbols, types, ts }`

## 2. 事件来源
- 当前接入来源：`anomaly_events` 表增量事件
- 推送类型：`ANOMALY_*`
- 推送去重：后端 60 秒窗口（可配置）
- 广播限速：每秒最大事件数（可配置）

## 3. 前端
- `HubClient`：自动连接、指数退避重连、心跳
- 通知中心能力：
  - 全局静音
  - 按类型静音
  - 按币种静音
  - 30 秒指纹去重（重复不弹 toast，仅累加计数）

## 4. 关键配置
- 后端（`.env`）：
  - `HUB_ENABLED=true`
  - `HUB_ALLOWED_ORIGINS=*`
  - `HUB_HEARTBEAT_TIMEOUT_SEC=45`
  - `HUB_DEDUPE_WINDOW_SEC=60`
- 前端（`docker-compose`）：
  - `VITE_HUB_URL=ws://localhost:8000/api/hub/market`

## 5. 下一步建议
- 三阶段可加鉴权：连接时携带 token，Hub 路由校验后绑定用户上下文
- 扩展事件源：吸筹信号、机构挂单强信号进入同一 Hub 事件总线

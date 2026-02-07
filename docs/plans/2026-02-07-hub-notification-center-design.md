# CoinMark Hub 推送 + 断线重连 + 通知中心设计（2026-02-07）

## 1. 目标
- 为 CoinMark 增加一套“实时通知基础设施”，覆盖：
  - Hub 实时推送
  - 前端自动断线重连
  - 通知中心（可静音、可去重）
- 通知语义对齐交易场景：只推送“有操作价值”的事件，避免刷屏。

## 2. 范围
- **包含**：后端推送通道、前端连接管理、通知存储与展示、去重与静音策略。
- **不包含**：短信/邮件/电话等外部告警通道（后续可扩展）。

## 3. 总体架构
- 后端：`FastAPI` 新增 Hub 通道（WebSocket），统一事件格式。
- 前端：新增 `HubClient` 服务，负责连接、订阅、重连、心跳与状态同步。
- UI：新增“通知中心抽屉/面板”，展示最近通知，支持筛选、静音、清空。

## 4. 事件模型（统一协议）
- 事件结构：
  - `id`: 全局唯一 ID（`type:symbol:ts:hash`）
  - `type`: 事件类型（如 `SWAP_ALERT`、`ABSORPTION_SIGNAL`）
  - `level`: `info|warning|critical`
  - `title`: 标题
  - `content`: 内容
  - `symbol`: 可选
  - `market`: 可选（`spot|swap|both`）
  - `ts`: 事件毫秒时间戳
  - `meta`: 扩展字段（对象）

## 5. 后端设计

### 5.1 Hub 通道
- 新增 WebSocket 路由：`/api/hub/market`
- 支持 query/header token 鉴权（沿用现有 Token 体系）。
- 连接管理：
  - `ConnectionManager` 维护活跃连接、订阅偏好、最后心跳时间。
  - 心跳超时自动清理死连接。

### 5.2 事件生产与广播
- 在现有异动/吸筹/机构挂单扫描循环中增加“事件生成器”。
- 事件只在满足阈值时入队，再通过 Hub 广播。
- 广播策略：
  - 可按市场/币种过滤（减少无关噪声）。
  - 异常连接自动降级，不影响主流程。

### 5.3 后端去重（第一层）
- `dedupe_key = type + symbol + state + rounded_ts_window`
- 同一 key 在窗口期内（默认 60s）只推送一次。

## 6. 前端设计

### 6.1 HubClient（连接层）
- 封装：`connect / disconnect / subscribe / onEvent / onStatus`
- 重连策略：指数退避 + 抖动（1s, 2s, 4s...最大 30s）
- 网络切换恢复：监听 `online/offline`，在线后立即重连。
- 状态通知：`connecting / connected / reconnecting / disconnected`。

### 6.2 通知中心（状态层）
- 新建 store：`apps/web/src/stores/notificationCenter.ts`
- 存储字段：
  - `items`（固定上限，默认 200）
  - `muted`（总静音）
  - `muteTypes`（按类型静音）
  - `muteSymbols`（按币种静音）
  - `dedupeCache`（最近事件指纹缓存）

### 6.3 前端去重（第二层）
- 指纹：`fingerprint = type + symbol + normalizedContent + bucket(30s)`
- 重复事件在 30 秒内不弹 toast，仅更新中心列表中的计数与时间。

### 6.4 静音策略
- 全局静音：不弹 toast，但保留通知中心记录。
- 类型静音：如关闭 `SPOT_MARKET_OVERVIEW`。
- 币种静音：如关闭 `BTCUSDT`。
- 失焦静音（可选）：页面隐藏时仅入中心，不弹即时提示。

## 7. 前端展示
- 顶部新增“通知铃铛”入口（未读数徽标）。
- 通知中心支持：
  - 全部/未读/类型筛选
  - 标记已读、全部已读、清空
  - 静音开关（全局/类型/币种）

## 8. 配置项（建议）
- 后端：
  - `HUB_ENABLED=true`
  - `HUB_HEARTBEAT_TIMEOUT_SEC=45`
  - `HUB_DEDUPE_WINDOW_SEC=60`
- 前端：
  - `VITE_HUB_URL=ws://localhost:8000/api/hub/market`
  - `VITE_NOTIFY_MAX_ITEMS=200`
  - `VITE_NOTIFY_DEDUPE_WINDOW_MS=30000`

## 9. 验收标准
- 断网/恢复后 30 秒内可自动重连并继续接收事件。
- 同类重复事件在去重窗口内不重复弹出。
- 静音后不弹 toast，但通知中心仍能看到新事件。
- 高峰期（每秒 20+ 事件）前端无明显卡顿，列表可控。

## 10. 风险与应对
- 事件风暴：后端限速 + 前端批处理渲染。
- 连接抖动：指数退避 + 在线状态监听。
- 通知噪声：默认启用双层去重，支持用户侧静音策略。

## 11. 实施顺序（里程碑）
- M1：后端 Hub 路由与连接管理。
- M2：后端事件生产 + 广播 + 去重。
- M3：前端 HubClient + 自动重连。
- M4：通知中心 store + UI + 静音/去重。
- M5：联调压测与阈值调优。


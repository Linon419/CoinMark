# CoinMark 全市场扫描架构升级方案（V1）

日期：2026-02-07  
状态：Draft（可执行）

## 1. 背景与现状

当前 `ingest` 是单进程“全包”模型：
- 同时负责 Binance WS 接入（trade + depth）
- 同时负责聚合计算、异动扫描、回填、入库

在“全市场（spot+swap 全 USDT）”下，瓶颈很明显：
- `ingest` CPU 长时间高位（接近或超过 1 核满载）
- 单点压力大，任何一个环节抖动都可能拖慢全链路
- 扩容手段有限（基本只能纵向加机器）

结论：要继续全市场扫描，建议从“单体 ingest”升级为“采集 / 传输 / 计算”解耦架构。

## 2. 目标（SLO）

### 2.1 业务目标
- 保持全市场扫描能力（不降覆盖）
- 异动发现与通知链路稳定
- 支持后续多策略并行（吸筹、机构位、异常、榜单）

### 2.2 技术目标
- 采集层可水平扩容（按 symbol 分片）
- 单组件故障不影响整条链路
- 支持消息回放，便于补算与排障
- 核心链路可观测（延迟、堆积、丢包、重试）

## 3. 新架构总览

```text
Binance WS
   ↓
[Collector 集群]
   ↓ (raw_trade / raw_depth)
[Message Bus: Redpanda/Kafka]
   ↓
[Stream Processor 集群]
   ├─ 聚合计算（1m/15m/1h/4h/1d）
   ├─ 异动检测（breakout/volume/amplitude）
   ├─ 吸筹/机构位等特征计算
   └─ 通知事件生成
   ↓
[Storage]
   ├─ ClickHouse（高频时序/明细/聚合）
   ├─ Postgres（配置/元数据/API关系数据）
   └─ Redis（热点缓存/状态游标）
   ↓
[API + TG Bot + Web]
```

## 4. 组件设计

## 4.1 Collector（建议 Go）
- 只做三件事：连 WS、轻量清洗、写总线
- 不做重计算，不直接写业务库
- 按 `symbol hash` 分片，多实例运行

关键点：
- trade 与 depth 分 topic
- 每条消息携带 `market/symbol/event_time/exchange_seq`
- 断线重连、幂等去重、基础指标上报

## 4.2 Message Bus（建议 Redpanda 起步）
- topic：
  - `coinmark.raw_trade`
  - `coinmark.raw_depth`
  - `coinmark.signal_events`（可选）
- 分区键：`symbol`
- 保留策略：建议 24h~72h（用于回放补算）

为何优先 Redpanda：
- Kafka 协议兼容，部署运维更轻
- 单机/小集群起步成本低，后续可平滑扩展

## 4.3 Stream Processor（Python 可保留）
- 从总线消费并计算，不直接依赖 WS
- 可拆为多个 worker：
  - `trade-agg-worker`
  - `depth-feature-worker`
  - `anomaly-worker`
  - `signal-worker`
- worker 失败可单独重启，不影响采集层

## 4.4 Storage 分工
- ClickHouse：高频写入 + 时间窗口聚合查询
- Postgres：配置、白名单、用户偏好、低频关系数据
- Redis：热点查询缓存、通知去重状态、消费位点辅助

## 5. 数据与主题约定（MVP）

## 5.1 raw_trade 消息
- `market`：spot/swap
- `symbol`：BTCUSDT
- `event_time_ms`：交易时间
- `agg_id`：交易聚合序列
- `price` / `qty` / `quote_notional`
- `is_buyer_maker`
- `ingest_ts_ms`

## 5.2 raw_depth 消息
- `market` / `symbol` / `event_time_ms`
- `bids[5]` / `asks[5]`（价格+数量）
- `ingest_ts_ms`

## 5.3 幂等与有序
- 以 `symbol` 作为分区键保证同币种局部有序
- 消费端用 `symbol + event_time_ms + seq` 做幂等

## 6. 部署建议

## 6.1 过渡期（单机/小规模）
- 1~2 个 Collector
- 1 个 Redpanda 节点
- 2~4 个 Processor worker
- 现有 API/TG/Web 基本不动

## 6.2 生产期（多机）
- Collector 水平扩容（按 market 和 symbol hash）
- Redpanda 3 节点
- Processor 按功能独立扩容
- ClickHouse 独立部署（建议副本+分片）

## 7. 迁移路线（不影响现网）

### Phase 0：基线与监控（1~2 天）
- 补齐当前指标：CPU、事件延迟、队列长度、DB 写耗时
- 固化压测场景与验收门槛

### Phase 1：抽离采集层（3~5 天）
- 新建 Collector，把 WS 数据写入总线
- 现有 ingest 继续跑，双轨验证（不切流）

### Phase 2：迁移计算层（5~7 天）
- 将聚合/异动 worker 改为消费总线
- 结果仍写现有表，保证 API 无感

### Phase 3：切换与收敛（2~3 天）
- 切掉旧 ingest 的 WS 直连逻辑
- 保留旧路径开关 1~2 周可回滚

## 8. 风险与回滚

主要风险：
- 新总线引入后，消费位点与幂等处理错误
- 计算结果与旧链路短期不一致

回滚策略：
- 全部改造通过开关控制
- 任一阶段可切回“旧 ingest 直连 WS”
- 回滚前后以同一批 symbol 对账（事件数、TopN、关键告警）

## 9. 验收标准

- 全市场下 `ingest` 不再是单点瓶颈（CPU 峰值明显下降）
- 事件链路端到端延迟稳定（目标 < 3s，可按环境调整）
- TG 通知与前端榜单无明显漏报
- 故障演练通过：任一 worker 重启不影响全链路

## 10. 近期执行建议（本周）

1. 先落地 Phase 0 指标和压测基线。  
2. 用 Redpanda 本地单节点搭最小 PoC。  
3. 先迁 `trade`，再迁 `depth`（分两步降低风险）。

---

> 备注：本方案目标是“先把架构拆开并可扩展”，不是一次性推倒重来。优先稳定迁移，再做性能极限优化。

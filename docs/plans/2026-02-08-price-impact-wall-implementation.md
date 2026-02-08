# 2026-02-08 持续买入 + 价格影响挂单墙实现说明

## 目标
- 保留现有 `SignalLab` 持续买入信号（`signal_lab_persistent_buy`）。
- 新增“可影响价格的挂单墙”信号（`signal_lab_bid_wall` / `signal_lab_ask_wall`）。
- 不存原始全量盘口，只存通过规则的候选挂单。

## 数据约束
- 当前采集是 Binance `depth5` + 1m 聚合特征（`orderbook_feature_buckets`），没有 `depth1000` 全量历史。
- 因此文档中的“完整路径流动性 impact_ratio”使用可解释代理值：
  - `impact_ratio = absorb_score / 30`
  - `survive_count = persistence_score * 0.6`
  - `cancel_ratio = cancel_penalty / 100`

## 挂单候选存储（仅有效候选）
新增表：`price_impact_wall_candidates`
- 唯一键：`(market, symbol, bucket_start_ms, zone_type)`
- 只落库 `MEDIUM/HIGH` 候选（LOW 直接丢弃）
- 自动清理 7 天前数据

## 评分规则（对齐文档思想）
候选评分点：
- `real_score`（基础分）
- `survive_count`（存活）
- `impact_ratio`（影响力）
- `cancel_ratio`（假墙风险）
- `aggTrades` 同向确认（`buyRatio/netFlow`）

置信度：
- `score >= 6` => `HIGH`
- `score >= 3` => `MEDIUM`
- 其他 => `LOW`

## API
- `GET /api/signal-lab/walls/realtime`
  - 实时生成候选，并可写入评分流 `anomaly_events`
- `GET /api/signal-lab/walls`
  - 查询最新候选（支持市场、方向、置信度过滤）

## 评分流事件类型
- `signal_lab_persistent_buy`
- `signal_lab_bid_wall`
- `signal_lab_ask_wall`

以上事件已接入：
- `/api/aggregate/hotMarkets`
- Telegram 推送叙事与等级评分
- 前端异常筛选与首页标签映射

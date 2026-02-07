# CoinMark 盘口指标 V1 设计与实施方案（2026-02-06）

## 1. 目标与范围

- 目标：在现有 `日内看盘` 基础上加入盘口过程数据，提升短线决策可解释性。
- 核心原则：
  - 与现有资金流（净流入）和 OI 联动，不做孤立指标。
  - 先做分钟级稳定版本（V1），避免高频噪音和过度复杂化。
  - 现货缺失时自动仅展示合约数据，不误导用户。
- V1 范围：
  - 后端新增盘口特征入库（1m）
  - 后端新增盘口日内接口
  - 前端日内看盘新增盘口确认区

## 2. 指标定义（V1）

### 2.1 `spreadBps`

- 定义：`(ask1 - bid1) / mid * 10000`
- 含义：交易成本与流动性风险；越大代表越不利于追价。

### 2.2 `depthImbalanceL5`

- 定义：`(sumBidNotionalL5 - sumAskNotionalL5) / (sumBidNotionalL5 + sumAskNotionalL5)`
- 范围：`[-1, 1]`
- 含义：买卖盘压力偏向。

### 2.3 `micropriceShiftBps`

- 定义：`(microprice - mid) / mid * 10000`
- 其中：`microprice = (ask1 * bidQty1 + bid1 * askQty1) / (bidQty1 + askQty1)`
- 含义：比 mid 更敏感的短时方向倾向。

### 2.4 `aggrBuyRatio`

- 定义：`takerBuyNotional / (takerBuyNotional + takerSellNotional)`
- 范围：`[0, 1]`
- 含义：主动成交方向强弱。

### 2.5 `replenishScore`

- 思路：盘口被吃薄后是否快速回补。
- 计算（V1 简化）：
  - 当 L1 总名义深度较前值下降超过阈值，记为一次“耗尽事件”。
  - 若后续恢复到基准阈值以上，记为“回补成功”。
  - `replenishScore = 回补成功 / 耗尽事件 * 100`，无耗尽事件时记中性值 `50`。

### 2.6 `wallPressureL5`

- 定义：`(maxBidLevelNotionalL5 - maxAskLevelNotionalL5) / (maxBidLevelNotionalL5 + maxAskLevelNotionalL5)`
- 含义：买卖单墙偏向。

## 3. 数据流设计

## 3.1 订阅流

- `aggTrade`：主动成交方向与名义金额。
- `depth5@100ms`：前 5 档盘口结构。

### 3.2 聚合层

- 内存按 `market + symbol + minuteBucket` 聚合。
- 每次 flush 只落已收盘分钟，避免反复写同一分钟导致复杂合并。

### 3.3 存储层

- 新表：`orderbook_feature_buckets`
- 粒度：`1m`
- 主键：`(market, symbol, bucket, bucket_start_ms)`

## 4. API 设计

### 4.1 新接口

- `GET /api/coin/detail/orderbook/intraday`
- 参数：
  - `symbol`（必填）
  - `bucket`（V1 固定 `1m`，接口保留扩展）
  - `limit`（默认 `60`）

### 4.2 返回结构

- 顶层：
  - `symbol`
  - `bucket`
  - `spotAvailable`
  - `swapAvailable`
  - `decisionHint`（`long_bias|short_bias|neutral`）
  - `decisionScore`（0-100）
- `items[]`：每分钟 6 个盘口特征

## 5. 前端展示设计（IntradayPage）

- 新增“盘口确认”模块：
  - 主图：`spreadBps`（线）
  - 副图：`depthImbalanceL5`、`aggrBuyRatio`（线/柱）
  - 标签：`decisionHint + decisionScore`
- 与资金图联动：
  - 页面文案强调“资金流是结果、盘口是过程”。
- 现货缺失：
  - 自动隐藏现货盘口系列，仅展示合约。

## 6. 决策融合（V1）

- 做多偏向：
  - 净流入持续为正 + OI上升 + 盘口分数高
- 做空偏向：
  - 净流入持续为负 + OI上升 + 盘口分数低
- 观望：
  - 指标冲突或盘口流动性恶化（spread 突增）

## 7. 风险与降级

- Binance 深度流异常时：
  - 不影响现有资金流页面。
  - 新接口返回空数组与中性判定。
- 样本不足时：
  - 前端展示“数据不足”提示，不输出方向性建议。

## 8. 执行步骤（实施顺序）

1. 后端新增模型与迁移（新表）
2. 新增盘口聚合器（内存分钟聚合）
3. WS 订阅接入 `depth5@100ms` 并写聚合器
4. flush 入库与 upsert
5. 新增盘口 intraday API
6. 前端 IntradayPage 接盘口模块
7. docker 重建 + 冒烟验证

## 9. 验收标准

- `SPORTFUNUSDT`（仅合约）下：
  - 盘口接口 200 且 `spotAvailable=false`
  - 日内看盘不展示现货盘口系列
- 主流币（如 `BTCUSDT`）下：
  - 盘口指标连续更新（1 分钟粒度）
  - 决策标签可显示并随数据变化


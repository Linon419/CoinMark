# CoinMark 盘口真实挂单位置（机构/大户）识别 V2 设计文档（2026-02-06）

## 1. 目标与范围

- **目标**：在现有盘口与资金流能力上，新增“机构/大户真实挂单位置”识别能力，输出可用于交易决策的 Alpha 信号。
- **市场范围**：`spot + swap` 同时支持。
- **核心输出**：
  - 真实买盘区（Real Bid Zones）
  - 真实卖盘区（Real Ask Zones）
  - 区域强度分数（0-100）
  - 依据标签（可解释）
- **非目标（V2 暂不做）**：
  - 不直接自动下单
  - 不做跨交易所合并盘口
  - 不做 tick 级超高频在线学习

---

## 2. 业务背景与策略假设

结合你给的 RUNE 案例，策略重点从“单点入场”升级为“入场 + 过程跟踪 + 结果管理”：

1. 大资金通常分段进场，不是一次性完成。
2. 真实挂单位置具备“持续性 + 吸收能力 + 回补能力”，而不是瞬时大单。
3. 入场后若资金持续流入但价格未快速上涨，不能简单视为失效，反而可能是持续吸筹阶段。

对应到系统能力：
- 识别“起点信号”（Early）
- 识别“延续信号”（Continuation）
- 区分“诱单/假墙”与“可执行参考价位”

---

## 3. 数据输入与粒度

## 3.1 输入数据

- `orderbook_feature_buckets`（1m）
  - spread、depth imbalance、replenish 等盘口结构特征
- `trade_buckets`（1m）
  - taker buy/sell、quote notional、OHLC
- 可选实时补充（后续）：原始 depth 快照流用于更细颗粒回放

## 3.2 时间与价格离散化

- 时间粒度：**1m**（与当前系统一致，先保证稳定）
- 价格分箱：按相对中间价 `mid` 的 `bps` 区间分桶
  - 推荐默认：`bin = 2 bps`
  - 覆盖区间：`±80 bps`（可按币种波动率动态扩展）

---

## 4. 真实挂单位置算法（V2）

## 4.1 候选区间生成

对每分钟盘口，将 L1-LN（建议 N=20）价位映射到 bps 价格桶，得到候选 `zone_id`。

每个 zone 维护滚动统计（20m/60m/180m）：
- 挂单名义量
- 出现时长
- 撤单/回补事件
- 附近成交与价格反应

## 4.2 特征定义

对每个候选区间计算以下特征（归一化到 0-1）：

1. **持久性 `P_persist`**
   - 区间在窗口内“有效存在”的时间占比
2. **撤单惩罚 `P_cancel_penalty`**
   - 出现后短时撤销比例（反 spoof）
3. **吸收能力 `P_absorb`**
   - 区间附近发生主动成交后，价格未明显穿透的能力
4. **回补能力 `P_replenish`**
   - 被吃薄后恢复挂单的速度与成功率
5. **防守有效性 `P_defend`**
   - 触碰次数后反弹/回落统计
6. **资金一致性 `P_flow_align`**
   - 区间方向与净主动流方向一致程度

## 4.3 大户/机构权重（规模维度）

- 不用固定阈值，采用**分位数动态阈值**：
  - 每币对每窗口统计挂单名义量分布
  - `q90~q95` 定义为“大单层”
- 区间大单占比越高，机构可信度越高：`P_size`

## 4.4 综合评分

区间真实度分数：

`RealScore = 100 * (w1*P_persist + w2*P_absorb + w3*P_replenish + w4*P_defend + w5*P_flow_align + w6*P_size - w7*P_cancel_penalty)`

推荐初始权重：
- `w1=0.20, w2=0.20, w3=0.15, w4=0.15, w5=0.10, w6=0.15, w7=0.05`

等级划分：
- `>= 75`: Institution-Strong（重点关注）
- `60-75`: Institution-Possible（可跟踪）
- `< 60`: 弱参考或噪声

---

## 5. 信号状态机（入场 + 延续）

每个 zone 维护状态：
- `DETECT`（初步发现）
- `WATCH`（可观察）
- `CONFIRM`（确认）
- `DECAY`（衰减）

状态转移（简化）：
- `DETECT -> WATCH`：20m 评分达阈值
- `WATCH -> CONFIRM`：60m 评分和资金一致性持续
- `CONFIRM -> DECAY`：撤单惩罚上升或价格有效穿透

关键增强：
- 增加 **Continuation** 逻辑：即使短窗触发减弱，只要 120m 资金方向持续且冲击可控，维持 `WATCH`。

---

## 6. API 设计（建议）

## 6.1 全币扫描

`GET /api/aggregate/orderbookInstitutionalLevels`

参数：
- `market=spot|swap|both`（默认 `both`）
- `limit`（默认 100）
- `state=WATCH|CONFIRM|ALL`
- `lookbackMinutes`（默认 360）

返回核心字段：
- `symbol`
- `market`
- `zoneType`（bid/ask）
- `zoneLow` / `zoneHigh`
- `realScore`
- `state`
- `reasons[]`
- `ts`

## 6.2 单币详情

`GET /api/coin/detail/orderbook/institutional-levels?symbol=XXX&market=swap`

返回：
- `topBidZones[]`
- `topAskZones[]`
- `continuationState`
- `riskFlags[]`

---

## 7. 存储设计（建议）

新表：`orderbook_real_levels_1m`

关键字段：
- `market, symbol, bucket_start_ms, zone_id, zone_type`
- `zone_low, zone_high`
- `real_score, signal_state`
- `persist_score, absorb_score, replenish_score, defend_score, flow_align_score, size_score, cancel_penalty`
- `reasons (jsonb)`

索引建议：
- `(market, bucket_start_ms)`
- `(market, symbol, bucket_start_ms)`
- `(market, signal_state, bucket_start_ms)`

---

## 8. 前端展示（MVP）

异动统计页新增“吸筹信号扫描（全币）V2”展示列：
- 时间
- Symbol
- 市场（spot/swap）
- 状态（WATCH/CONFIRM）
- 分数
- 区间价格
- 依据标签

币种详情页新增区块：
- 顶部：当前主信号状态 + 分数 + 冷却提示
- 中部：真实买盘区/卖盘区（按分数排序）
- 底部：最近 120m 延续性解释

---

## 9. 默认参数（本次拍板）

- `market`: **both**
- 时间窗口：20m / 60m / 180m
- Continuation 窗口：120m
- 价格分箱：2 bps
- 扫描标的：按成交额 TopN（默认 120，可调）
- 刷新周期：60s

---

## 10. 回测与验收

## 10.1 回测指标

- 触发后 1h / 4h / 24h 方向胜率
- 触发后最大不利波动（MAE）
- 触发后最大有利波动（MFE）
- 信号稳定性（状态抖动率）

## 10.2 验收标准（MVP）

1. 能稳定产出 `spot + swap` 双市场信号
2. 在“仅触发信号”过滤下，空窗期可解释（无触发）
3. 单币与全币口径一致（同 symbol 同时段状态一致性 >= 95%）
4. 无明显性能回退（API P95 增幅 < 20%）

---

## 11. 风险与控制

- **假单风险**：通过撤单惩罚 + 成交确认抑制
- **过拟合风险**：窗口与阈值保持分层，不追求单一场景极致
- **性能风险**：优先分钟级聚合，不引入毫秒级重计算
- **解释风险**：每条信号必须输出 `reasons`，禁止黑箱评分

---

## 12. 执行计划（Plan 模式）

### Phase 1（数据层）
- 新增真实挂单区间表与索引
- 增加分钟级特征聚合器

### Phase 2（算法层）
- 实现区间评分与状态机
- 实现 continuation 逻辑

### Phase 3（接口层）
- 新增全币扫描 API
- 新增单币详情 API

### Phase 4（展示层）
- 异动页接入全币扫描表格
- 币详情接入区间可视化

### Phase 5（验证与上线）
- 回测指标跑批
- 线上灰度 + 参数微调

---

## 13. 待你确认的最后两项

1. 异动页是否默认只展示 `WATCH+CONFIRM`（建议是）
2. 是否需要在信号里附“建议动作”字段（如试多/减仓/观望，建议先不加）


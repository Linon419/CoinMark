# Market Anomaly V2 设计文档（2026-02-07）

## 1. 背景与目标
当前 CoinMark 已具备异动事件采集与展示能力，但“可读性/可交易性”仍弱于目标站 Watching 页面：
- 事件偏原始字段，缺少“一句话叙事”
- 缺少统一优先级排序（严重度）
- 缺少稀有性标记（是否近期首次）
- 缺少同主题事件合并（生命周期）

V2 目标：在不重构底层采集的前提下，用最小改动提升“看盘决策效率”。

## 2. 本阶段范围（MVP）
本阶段仅实现以下四点：
1. 后端输出统一 `severityScore`（0-100）
2. 后端输出 `narrative`（中文一句话）
3. 后端输出 `firstSeenInWindow`（窗口内首次）
4. 前端异动表按 `severityScore` 排序并展示叙事/标签

不在本阶段：
- 跨窗口统计学习
- 信号自动调参
- 完整生命周期图谱（触发/持续/失效）

## 3. 数据与接口设计
在 `GET /api/aggregate/hotMarkets` 的 `items[]` 增量扩展字段（兼容旧字段）：
- `severityScore: number`（0-100）
- `severityLevel: "info" | "warning" | "critical"`
- `narrative: string`
- `firstSeenInWindow: boolean`

说明：
- 旧字段保持不变，前端逐步迁移
- 评分为规则型评分，可解释、可复算

## 4. 规则设计
### 4.1 严重度评分（示例）
按事件类型加权：
- `breakout_up/down`：基础 60
- `volume_spike`：基础 45
- `amplitude_spike`：基础 40

再叠加细节因子：
- `volumeFactor`：每 +1 增加 3 分（上限 +20）
- `amplitude`：每 +1% 增加 2 分（上限 +20）
- `strengthScore`：按比例折算（上限 +20）

最终裁剪到 `0-100`。

### 4.2 等级映射
- `>= 80` => `critical`
- `>= 55` => `warning`
- 其余 => `info`

### 4.3 一句话叙事
模板示例：
- 向上突破：`BTCUSDT 向上突破 4h 关键位 42000，量能放大 3.2x`
- 向下跌破：`ETHUSDT 跌破 4h 支撑 2200，短时波动扩大`
- 量能异动：`SOLUSDT 15m 量能异常放大 4.1x`
- 振幅异动：`XRPUSDT 15m 振幅显著扩大至 2.8%`

### 4.4 窗口首次标记
在查询时间窗（如 6h）内：
- 同 `symbol + eventType` 仅第一条标记 `firstSeenInWindow=true`
- 后续同类事件为 `false`

## 5. 前端展示策略
页面：`AnomaliesPage`
- 默认按 `severityScore` 倒序展示（同分按时间倒序）
- 新增列：`等级`、`叙事`、`首次`
- 颜色规范：
  - `critical` => 红
  - `warning` => 橙
  - `info` => 蓝

## 6. 验收标准
1. `hotMarkets` 返回包含新增字段
2. 前端可见叙事文本与等级标签
3. 过滤条件不变，兼容旧逻辑
4. 构建与测试通过

## 7. 风险与回退
风险：
- 评分规则初期可能偏激进或保守
- 文本模板可能存在信息冗余

回退：
- 保留旧字段与排序逻辑开关
- 新字段异常时前端回退显示 `title`

## 8. 后续迭代（V2.1）
- 增加“事件生命周期合并”（同主题聚合）
- 增加“近期首次（7天）”与命中率回测
- 将叙事生成抽到独立模块，便于扩展多语言

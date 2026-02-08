
盘口算法技术文档
Order Book & aggTrades 监控系统算法设计
数据源：Binance | 基于aggTrades 1min桶 + Order Book快照

机密文档 · 仅限内部使用


第一章：系统概述
本系统通过实时监控币安盘口数据，识别大户资金流向，定位能够影响价格走向的关键挂单位置。输出信号可直接对接BUFF评分系统中的“大户挂单”副指标（+1分）。
双数据源架构
数据源	检测目标	优势	局限
aggTrades 1min桶	持续性买入/卖出	已成交不可撤，100%真实	看不到未成交的埋伏单
Order Book快照	大户埋伏单/压力墙	能提前发现意图	有假单风险，需过滤

核心原则：aggTrades检测“已发生的事实”，Order Book检测“未来的意图”。两者叠加时信号可信度最高。


第二章：aggTrades 模块（已成交检测）
2.1 数据结构
你已有全币种1min的aggTrade桶，每桶包含：
字段	含义	用途
p (price)	成交价	计算成交额
q (quantity)	成交量	计算成交额
m (isBuyerMaker)	true=卖方主动成交，false=买方主动成交	判断主动买卖方向
T (timestamp)	成交时间戳	时间窗口计算
p * q	单笔成交额(USDT)	大单识别核心

2.2 大单阈值计算
大单不能用固定金额，因为BTC和DOGE的“大”完全不同。每个币种独立计算。

算法：滚动窗口Z-Score异常检测
window_size = 1440          # 24小时 (1440个1min桶)
lookback = window_size 桶内所有单笔成交额

μ = mean(lookback)
σ = std(lookback)

z_score = (单笔成交额 - μ) / σ

if z_score >= 3.0  → ★ 超大单
if z_score >= 2.0  → 大单
if z_score < 2.0   → 普通单，忽略

为什么用Z-Score而不是固定倍数：固定倍数(如μ+2σ)在分布偏斜时会失效。币圈成交量分布往往是重尾的，Z-Score更稳健。如果分布极度偏斜，可对成交额取log后再算Z-Score。

2.3 三种信号模式
Signal A：单笔大额买单
对应场景：ROI“盘口刚刚有一笔大额买单”

触发条件：
  1. 单笔成交额 z_score >= 2.0
  2. m = false  (主动买入)

输出：
  {币种, 时间, 方向:买, 成交价, 成交额, z_score,
   类型: 'single_large'}

Signal B：持续性买入
对应场景：MASK“盘口出现持续性买入”

算法：大单频率 + 时间分散度
detection_window = 240min (4小时)

Step 1: 收集窗口内所有大单(z>=2)且m=false
  large_buys = [{time, amount, z_score}, ...]

Step 2: 判断数量是否足够
  if len(large_buys) < 3: 跳过

Step 3: 判断时间分散度
  intervals = [t(i+1) - t(i) for i in range(n-1)]
  avg_interval = mean(intervals)

  if avg_interval < 2min:
    → 可能是同一人拆单, 合并为1笔single_large
  if avg_interval >= 5min:
    → 多个时间点反复大额买入 = persistent_buying

Step 4: 确认方向一致性
  buy_ratio = 大单买入额 / (大单买入额 + 大单卖出额)
  if buy_ratio >= 0.70:
    → 确认持续性买入

关键参数说明：detection_window和avg_interval的阈值需要根据实际数据回测调整。不同币种流动性差异巨大，BTC可能5min就算分散，小币可能30min才算。初始值先跑，后续按币种分类优化。

Signal C：累计净流入斜率
补充检测：不依赖大单识别，而是看整体资金流方向。

slope_window = 60min

对窗口内每个1min桶:
  net_flow_i = sum(p*q where m=false) - sum(p*q where m=true)

累计序列: cumulative = [net_flow_1, net_flow_1+net_flow_2, ...]

线性回归: y = kx + b
  k > 0 且 R² > 0.7  → 稳定持续买入
  k < 0 且 R² > 0.7  → 稳定持续卖出
  R² < 0.5          → 无明确方向，不触发

这个信号不单独使用，作为Signal B的确认层。当Signal B触发且Signal C的斜率方向一致时，可信度最高。


第三章：Order Book 模块（純盘口检测）
3.1 数据获取
API: GET /api/v3/depth?symbol={SYMBOL}&limit=1000

轮询间隔: 1min (与aggTrade桶同步)
存储: 每次快照存为一条记录，保留时间戳

3.2 流动性异常聚集点检测
核心目标：找到盘口中挂单量远超周围的价位区间。

算法：分桶聚合 + Z-Score
Step 1: 分桶
  当前价格 = last_price
  bucket_size = last_price * 0.005  (0.5%一个桶)
  
  买方挂单按价格分桶:
  桶[-1] = (last_price*0.995, last_price]  这个区间的所有挂单累加
  桶[-2] = (last_price*0.990, last_price*0.995]
  ....
  桶[-N] = 最远价位
  卖方同理，向上分桶

Step 2: 计算每个桶的总挂单额(USDT)
  bucket_value[i] = sum(价格 * 数量) for 桶i内所有挂单

Step 3: 找异常桶
  μ = mean(所有桶的value)
  σ = std(所有桶的value)
  z_score[i] = (bucket_value[i] - μ) / σ

  z_score >= 3.0  → ★ 极强墙 (wall)
  z_score >= 2.0  → 显著聚集点

3.3 影响力评估
不是所有异常聚集点都能影响价格。关键在于“墙”相对于“路径上流动性”的比值。

wall_price = 异常桶的中心价位
wall_value = 该桶总挂单额

path_liquidity = 从现价到wall_price之间
               所有挂单的总额

impact_ratio = wall_value / path_liquidity

impact_ratio > 3.0  → 极强影响力，价格几乎必然被挡
impact_ratio > 1.5  → 显著影响力
impact_ratio < 1.0  → 可能被穿透，不可靠

TRUMP例子验证：时价 9.957，大户买单的挂在 7.4 和 6.45。距离现价 25%-35%。这么远的距离意味着：1) 路径上流动性稀薄，impact_ratio很高；2) 距离太远不可能是假单（操纵近价无意义）。


第四章：假单过滤
假单是盘口分析的最大噪音源。以下四层过滤器逻辑上递进，全部通过才认定为真单。

Filter 1: 存活时间
连续快照对比:

snapshot_T0: 价位7.4出现大单 → 标记, 存活=1
snapshot_T1: 价位7.4仍在       → 存活=2
snapshot_T2: 价位7.4仍在       → 存活=3
snapshot_T3: 价位7.4消失       → 假单，丢弃

存活次数 >= survive_threshold → 通过
survive_threshold = 5 (即连续5次快照都在，
                       若轮询间隔1min则为5分钟)

Filter 2: 价格距离
distance_pct = abs(wall_price - last_price) / last_price

if distance_pct < 0.01 (1%):
  → 太近，极可能是假单(用于短期操纵盘口)
  → 需要survive_threshold加倍至 15次

if distance_pct > 0.20 (20%):
  → 远距离挂单，假单概率极低
  → survive_threshold可降至 3次

Filter 3: 整数关口降权
price_str = str(wall_price)

if price_str 末尾为 '0' 或 '00' 或 '000':
  → 整数关口(如 10.0, 8.00, 5.000)
  → 不直接过滤，但降权: z_score * 0.6
  → 降权后z_score仍 >= 2.0才保留

Filter 4: 价格逼近时的行为验证
当价格接近墙位(distance < 2%)时:

监控墙的变化:
  墙仍在且金额不减  → 真单确认 ★
  墙还在但金额减少>30%  → 正在被吃，部分真
  墙消失                 → 假单确认，移除信号

四层过滤总结：真单 = 大额(z>=2) + 远离现价或近价但存活久 + 非整数关口或降权后仍强 + 价格逼近时不撤单


第五章：信号输出规格
5.1 信号类型
信号类型	触发源	含义	对应场景
SINGLE_LARGE_BUY	aggTrades	单笔大额主动买入	ROI“刚刚有一笔大额买单”
SINGLE_LARGE_SELL	aggTrades	单笔大额主动卖出	反向信号
PERSISTENT_BUY	aggTrades	多笔大单分散买入	MASK“持续性买入”
PERSISTENT_SELL	aggTrades	多笔大单分散卖出	反向信号
BID_WALL	Order Book	买方异常聚集点	TRUMP“大户买单挂在7.4”
ASK_WALL	Order Book	卖方异常聚集点	压力位

5.2 输出数据结构
{
  "symbol":       "TRUMPUSDT",
  "signal_type":  "BID_WALL",
  "direction":    "buy",
  "price":        7.4,
  "value_usdt":   820000,
  "z_score":      4.2,
  "distance_pct": -25.7,        // 负数=低于现价
  "impact_ratio": 5.8,
  "survive_count": 12,           // 已存活12次快照
  "first_seen":   "2025-04-01T02:15:00Z",
  "last_seen":    "2025-04-01T02:27:00Z",
  "confidence":   "HIGH",        // HIGH/MEDIUM/LOW
}

5.3 置信度计算
score = 0

// 基础分
if z_score >= 3.0:  score += 2
elif z_score >= 2.0: score += 1

// 存活加分 (Order Book信号)
if survive_count >= 10: score += 2
elif survive_count >= 5: score += 1

// 距离加分
if distance_pct > 20%: score += 1  // 远距离不太可能假

// 影响力加分
if impact_ratio > 3.0: score += 2
elif impact_ratio > 1.5: score += 1

// aggTrades确认加分
if 同方向aggTrades信号存在: score += 2

confidence:
  score >= 6 → HIGH
  score >= 3 → MEDIUM
  score < 3  → LOW


第六章：实现指南
6.1 系统架构
┌───────────────────┐     ┌───────────────────┐
│ aggTrades 1min桶  │     │ Order Book快照  │
│ (已有数据)       │     │ (1min轮询)      │
└───────┬───────────┘     └───────┬───────────┘
        │                         │
        ▼                         ▼
┌───────────────────┐     ┌───────────────────┐
│ 大单识别器        │     │ 分桶聚合器        │
│ Z-Score >= 2.0   │     │ Z-Score >= 2.0   │
└───────┬───────────┘     └───────┬───────────┘
        │                         │
        │     ┌─────────────┐     │
        └───▶│ 信号融合器  │◀───┘
              │ + 假单过滤 │
              │ + 置信度   │
              └─────┬───────┘
                    │
                    ▼
              ┌─────────────┐
              │ 告警输出    │
              │ → BUFF +1分 │
              │ → Telegram  │
              └─────────────┘

6.2 全参数表
参数名	默认值	说明	调优方向
z_threshold	2.0	大单/异常桶的Z-Score阈值	降低发现更多但噪音增加
z_threshold_mega	3.0	超大单阈值	提高只留最强信号
lookback_minutes	1440	基准计算的回看窗口	24h是合理起点
detection_window	240	持续性检测窗口(min)	小币可加大至480
min_large_count	3	持续性最少大单数	降低可发现更早期信号
min_avg_interval	5	判定“分散”的最小间隔(min)	BTC可缩小，小币加大
buy_ratio_threshold	0.70	方向一致性阈值	提高更严格
bucket_pct	0.005	盘口分桶宽度(0.5%)	小币可加大至1%
survive_threshold	5	存活次数阈值	加大更严格过滤假单
round_number_discount	0.6	整数关口z_score折扣	可调至0.5-0.8
slope_r2_threshold	0.7	累计净流入R²阈值	降低放宽信号
impact_ratio_min	1.5	最低影响力比值	提高只留强墙
depth_limit	1000	Order Book拉取深度	根据币种流动性调整

6.3 每分钟执行流程
every 1 minute:
  for symbol in watchlist:

    // === aggTrades模块 ===
    bucket = get_latest_1min_bucket(symbol)
    history = get_lookback_buckets(symbol, 1440)

    μ, σ = calc_stats(history)

    for trade in bucket:
      z = (trade.amount - μ) / σ
      if z >= 2.0 and trade.m == false:
        emit SINGLE_LARGE_BUY

    check_persistent_pattern(symbol, window=240)
    check_cumulative_slope(symbol, window=60)

    // === Order Book模块 ===
    depth = fetch_depth(symbol, limit=1000)
    buckets = aggregate_to_price_buckets(depth, pct=0.5%)
    anomalies = find_z_score_anomalies(buckets, threshold=2.0)

    for wall in anomalies:
      wall.survive_count = check_survival(wall, history)
      wall.impact_ratio = calc_impact(wall, depth)
      apply_fake_order_filters(wall)

      if wall.passed_all_filters:
        calc_confidence(wall)
        emit BID_WALL or ASK_WALL


第七章：回测与调参
你已有全币种历史aggTrade数据，可以直接回测。

7.1 回测方法
Step 1: 对历史数据跑算法，生成所有信号时间点

Step 2: 对每个信号时间点，记录:
  T+1h 价格变化%
  T+4h 价格变化%
  T+24h 价格变化%
  最大回撤%

Step 3: 统计各信号类型的:
  胜率 (T+Xh后方向正确的比例)
  平均收益
  最大回撤

Step 4: 按confidence分组统计，验证:
  HIGH置信度信号胜率 > MEDIUM > LOW

7.2 参数调优策略
•先用默认参数跑一遍全量数据，建立基线
•按币种分类统计：BTC/ETH、主流山寨币、小币/彩票币，三类可能需要不同参数
•核心调优维度：z_threshold和detection_window。其他参数的影响相对较小
•避免过拟合：用前70%数据训练，后30%验证

重要提醒：回测只能验证aggTrades模块。Order Book历史数据需要你从现在开始存储快照，积累一段时间后才能回测。建议立即开始存储。

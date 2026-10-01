# 全市场覆盖与负载优化方案

日期：2026-10-01
状态：Draft（待确认）
关联：`2026-02-07-full-market-architecture-v1.md`（采集/总线已落地）、`2026-02-26-clickhouse-daily-partition.md`（分区脚本未在现网执行）

## 1. 目标

1. 成交类数据覆盖 Binance 全部 USDT 交易对（合约约 530、现货约 430），让小币的净流入、涨幅、量能、异动都能算出来。
2. 放开覆盖后，ClickHouse 和 api 的负载不随币种数线性增长。
3. 市值覆盖只在合约上线的币，并随价格实时变化。

不在本次范围：盘口深度（depth）扩到全市场；新增交易所。

## 2. 现状（2026-10-01 线上实测）

### 2.1 覆盖范围被三处配置卡住

| 配置 | 线上值 | 作用 |
|---|---|---|
| `COLLECTOR_SYMBOL_LIMIT`（spot、swap 两个 collector） | 50 | 成交和深度**共用**这一个限制，只采市值前 50 |
| `ANOMALY_SCAN_TOP_N`（api） | 80 | 市场异动、climax 扫描的币种数 |
| `INGEST_SYMBOL_LIMIT`（ingest） | 80 | **代码里没有引用，是失效配置** |

结果：最近 1 小时 `trade_buckets` 里只有 spot 75 个、swap 62 个币。过去 31 天覆盖率 ≥80% 的币，spot 只有 61 个、swap 只有 37 个，币会随着市值排名变化进进出出。

### 2.2 负载在 ClickHouse 查询，不在采集

- 采集很轻：NATS 最近 1 小时 29.7 万条消息、225MB，约 82 条/秒。
- ClickHouse 很重：最近 1 小时扫描约 **41GB**，而新写入只有约 5MB。

| 查询（`system.query_log`，1 小时） | 次数 | 扫描量 | 平均耗时 |
|---|---|---|---|
| 全币种 1m 成交桶（`QueryTradeBuckets`） | 120 | 19.5GB | 280ms |
| 按币聚合（`QueryTradeFlowAggRange`） | 30 | 7.8GB | 1127ms |
| 成交额排名（`QueryTradeAggVolume`，带 argMax 去重子查询） | 119 | 7.8GB | 97ms |
| 成交桶 | 178 | 5.1GB | 105ms |
| 盘口特征（`QueryOrderbookFeatures`） | 119 | 1.3GB | 30ms |

原因：

1. **`trade_buckets`、`orderbook_feature_buckets` 没有分区**（`partition_key` 为空，1 个分区）。排序键是 `(market, symbol, bucket, bucket_start_ms)`，时间排在最后，“所有币最近 N 分钟”这类查询用不上索引，每次都扫 31 天全表。`ingest-go` 的建表语句已经带了按天分区（`store.go:606/624`），但现网表建于此前，迁移脚本 `scripts/clickhouse-partition-by-day.sql` 从未执行。
2. **后台任务定时轮询 ClickHouse 并在 Go 里重算**：

| 任务 | 位置 | 线上间隔 | 查询 |
|---|---|---|---|
| climax 扫描 | `hub/runtime.go` → `service/signal_lab.go:459/476/637` | 60s | 成交额排名 + 1m 成交桶 + 盘口特征 |
| 市场异动·分钟 | `service/market_yidong.go:59/63` | 120s | 1m 成交桶 + 30 天 1d 成交桶 |
| 市场异动·量能 | `service/market_yidong.go:209` | 120s | 1m 成交桶 |
| 吸筹扫描 | `service/absorption.go:232` | 线上关闭 | — |
| ingest 成交桶巡检 | `ingest-go/internal/runtime/loops.go`（watchdog） | 60s | 前 60 个币最近 10 分钟 |

扫描量 ≈ 币种数 × 表的天数。币种从 50 放到 500，扫描量预计超过 400GB/小时。2 月全市场运行时 ingest CPU 打满（见 2026-02-07 文档），与此一致。

### 2.3 市值缺口

ingest 的市值只来自 Binance 现货的两个网页接口。今天扫描的 213 个合约里有 50 个没有市值，主要是只在合约上线的币。Alpha 代币列表补齐的改动已完成（未提交），见 4.4。

## 3. 原站（CoinArch）的做法

- 实时数据通过 SignalR（`/api/marketHub`，ASP.NET Core）推送。15 秒内只收到 3 条消息、约 98KB：全部现货和全部合约的当日 OHLC，以及 `WATCHING_TOP`（每个币的涨幅、OI 变化、OI 金额、资金费率、合约净流入 `ff`、现货净流入 `sf`、推送次数、趋势评分都已算好）。
- 说明它在**内存里增量维护全市场状态**，定时推快照，数据库不在实时路径上。
- 币种元数据 750 个：流通量来源 Binance 553、CoinMarketCap 182、手工 2。**存流通量，不存市值**，市值按实时价格计算；保留 `prevCirculSupply` 用于发现代币解锁。另外维护了一份“只有合约没有现货”的列表（163 个）。

## 4. 方案

### 4.1 P1：ClickHouse 按天分区（不改代码）

- 执行现成脚本 `scripts/clickhouse-partition-by-day.sql`：建影子表 → 去重回灌 → `RENAME` 原子切换 → 观察后删除备份。
- 需要停 ingest 写入几分钟。缺失的分钟数据由 watchdog 自动修复，或手动回补。
- 先在本地 Docker 用线上表结构和一份抽样数据完整演练一次，并记录耗时。
- 预期：“最近 N 分钟/今天”的查询只读 1~2 个分区，扫描量降到现在的 1/15~1/30。
- 回滚：备份表 `*_old_nopartition` 保留到验证通过，`RENAME` 回去即可。

### 4.2 P2：内存市场状态 `MarketState`（核心）

在 api-go 新增 `internal/marketstate`，直接消费 NATS 里的原始成交，增量维护全市场状态。实时功能从内存读取，ClickHouse 只负责历史和回测。

**数据结构**（每个 market × symbol 一份）：

- 当日（UTC 0 点起）：开、高、低、收，主动买入额，主动卖出额，成交额，笔数。日内累计净流入就是 CoinArch 的“净流入”。
- 1m 环形缓冲：最近 24 小时，1440 格，每格存 OHLC、买入额、卖出额、成交额、笔数。5m/15m/1h/4h/24h 等滚动窗口都由它现算。
- 内存估算：约 1000 个交易对 × 1440 格 × 约 64 字节 ≈ 90MB。api 容器现在用 218MB / 上限 512MB，可以放下；如果偏紧，1m 只保留 6 小时，再加一个 1h 环形缓冲。

**启动与衔接**：

1. 记下启动时刻 `T0`，从 ClickHouse 读当日和最近 24 小时的 1m 桶（分区后只读 2 个分区），填满状态。
2. 创建 JetStream 临时消费者，`DeliverByStartTime = T0 - 2min`（流保留 1 小时，足够）。重叠的 2 分钟按 aggTrade ID 去重。
3. 启动完成前，接口仍走原来的 ClickHouse 查询。

**迁移顺序**（先迁最重的后台任务）：

1. `market_yidong` 分钟扫描和量能扫描
2. climax 扫描（`signal_lab`）
3. 多头指数、TG 排行（`tg_rank.go`、`telegram/query.go` 里重复的两份实现顺便合并）
4. `aggregate/basicinfo`、BOLL 候选的日内净流入（`boll_pump.go:106`）

单币详情、历史 K 线、回测这类按需查询保持走 ClickHouse，分区后已经足够快。

**容错**：消费中断超过 N 秒时，标记状态为不可用，接口自动回退到 ClickHouse 查询并记日志；恢复后重新执行启动流程。

**备选方案**：状态放在 ingest 里维护（ingest 本来就在消费成交），每隔几秒把快照写到 Redis，api 从 Redis 读。优点是 api 重启不用重新加载；缺点是滚动窗口只能用 ingest 预先算好的那几种，快照体积也大。推荐方案 A：状态放在 api 里，因为实际使用这些数据的是 api。

### 4.3 P3：collector 成交与深度分开限制

- `collector-go/internal/config`：新增 `COLLECTOR_DEPTH_SYMBOL_LIMIT`；`COLLECTOR_SYMBOL_LIMIT` 改为只控制成交。不设置新变量时保持现有行为（深度沿用成交的限制）。
- `collector.go:111/122`：成交和深度分别生成订阅列表。
- 分批放开成交：50 → 200 → 0（全部），每一步观察 24 小时：NATS 消息速率、ingest CPU、ClickHouse 每小时扫描量、collector 断线次数。
- 深度保持 50。依赖深度的鲸鱼墙、吸筹、盘口特征只覆盖前 50 个币。
- `ANOMALY_SCAN_TOP_N` 在 P2 迁移完成后再调大；删除失效的 `INGEST_SYMBOL_LIMIT`。

### 4.4 P4：市值改为“存流通量、实时算市值”

- 已完成（未提交）：ingest 增加 Binance Alpha 代币列表补齐，价格和合约标记价格偏差 ≤5% 才采用；`1000PEPE` 这类按本体复制市值并换算价格和流通量。实测 50 个缺口能补上 43 个。附单元测试。
- 待做：
  - 读取方（`bot_oi_mcap.go`、`tg_rank.go`、`signal_lab.go`、P2 的 MarketState）改为 `流通量 × 实时价格`；表结构不变，`circulating_supply` 字段已经有了。
  - 增加 CoinMarketCap 作为第三个数据源，只补前两个来源都没有的币，每天刷新一次。免费额度每月 1 万次，足够用。**需要申请 API Key。**
  - 保存上一期流通量，用于发现代币解锁（可选）。
- 删除 api-go 里没有调用方的 `service/market_supply.go`，它和 ingest 的市值逻辑重复。

## 5. 上线顺序与回滚

| 顺序 | 内容 | 依赖 | 回滚方式 |
|---|---|---|---|
| 1 | P4 已完成部分（Alpha 补齐）提交上线 | 无 | 回退镜像 |
| 2 | P1 分区迁移 | 本地演练通过、维护窗口 | `RENAME` 回备份表 |
| 3 | P2 MarketState：先只读不切流，与 ClickHouse 结果对账 24 小时 | P1 | 开关 `MARKET_STATE_ENABLED=false` |
| 4 | P2 逐个迁移后台任务 | 对账通过 | 每个任务单独开关 |
| 5 | P3 分批放开成交采集 | P2 第 1、2 项迁移完成 | 改回 `COLLECTOR_SYMBOL_LIMIT=50` |
| 6 | P4 其余部分 | CMC API Key | 回退镜像 |

## 6. 验收标准

- ClickHouse 每小时扫描量：P1 后 < 5GB；P2、P3 完成并放开全市场后 < 10GB（现在 50 个币是 41GB）。
- 覆盖：最近 1 小时 `trade_buckets` 里 swap 交易对 ≥ 500 个。
- 正确性：MarketState 的当日累计净流入、涨幅，与 ClickHouse 计算结果偏差 < 0.5%，抽查 20 个币；与 CoinArch 的“净流入”抽查对比（口径：合约，UTC 0 点起累计）。
- 资源：api 内存 < 450MB，ingest CPU 峰值 < 1 核。
- 市值：成交额 ≥ 500 万 U 的合约，缺市值的比例 < 3%。

## 7. 待确认

1. P1 的维护窗口（停 ingest 写入约 5~10 分钟）。
2. 是否申请 CoinMarketCap API Key。
3. P2 选方案 A（状态放 api）还是方案 B（放 ingest + Redis）。
4. 小币是否需要盘口深度相关功能（决定 P3 的深度限制是否保持 50）。

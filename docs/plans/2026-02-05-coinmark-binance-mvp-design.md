# CoinMark（CoinArch）开发设计文档（Binance 数据源 MVP）

日期：2026-02-05  
面向：简历项目（偏后端/全栈），优先可演示、可量化、可扩展  
目标数据源：Binance（Spot + USDT-M Futures）

---

## 1. 目标与成功标准

### 1.1 项目目标（要解决什么问题）
做一个“加密市场数据聚合 + 异动监控”的 Web 应用，提供：
- 主页面：涨跌榜、滚动异动、1 小时热度、热点与市场异动快讯
- 币种详情：基本信息、小时/每日资金快照、近 20 日高低点等图表
- 收藏：支持关注币种，快速查看自选列表

### 1.2 成功标准（简历可量化）
以本地压测与线上监控数据作为量化指标来源（不靠“编数字”）：
- API：`p95 < 200ms`（缓存命中场景），`p95 < 800ms`（需要拉取/计算场景）
- 吞吐：`>= 200 RPS`（本地单机 Docker 环境，具体以压测报告为准）
- 数据刷新：主榜单 `<= 15s` 更新一次；详情页历史数据按需加载
- 稳定性：Binance 限流/抖动时不崩溃，前端可降级显示“上次更新时间 + 重试”
- 上线验证（完成 M4 后）：有可访问的线上 Demo（CloudFront 域名或自定义域名），并能提供部署截图/压测报告作为证据

### 1.3 非目标（MVP 不做）
- 不做交易/下单（避免合规与安全风险）
- 不做账号体系（仅做匿名收藏或本地 token）
- 不做复杂策略回测（只做数据展示与异动检测）

---

## 2. 技术选型（推荐方案）

> 你已经确认“先接入 Binance 接口”，为保证迭代效率与简历通用性，MVP 推荐采用“后端聚合 + 前端可视化”。

### 2.1 后端（推荐：Python）
- 语言/框架：Python 3.10 + FastAPI
- HTTP 客户端：httpx（async）
- 缓存：Redis（可选，MVP 强烈建议上）
- 持久化：PostgreSQL（存历史快照/收藏/任务状态）
- 后台任务：内置 async scheduler（MVP）或 APScheduler
- 指标与可观测性：Prometheus metrics + 结构化日志（JSON）

### 2.2 前端（推荐：Vite + React）
- Vite + React + TypeScript
- 图表：ECharts（更贴近你现有截图风格）
- UI：Arco Design（你抓包里出现 `arco.*.js`，一致性更好）

### 2.3 部署（MVP）
- docker-compose：`api` + `web` + `postgres` + `redis`
- 可选：部署到一台云主机（后续再做，不阻塞 MVP）

### 2.4 AWS 上线方案（你选择的方案 1）
目标：**能在简历里真实使用并可演示** `S3 / CloudFront / EC2`，同时具备基础工程化（HTTPS、日志、健康检查、回滚）。

推荐资源组合：
- 前端：`S3`（静态托管）+ `CloudFront`（CDN 加速）
- 后端：`EC2`（Docker/Compose 部署 FastAPI）+ `Nginx`（反代/HTTPS）
- 域名（可选）：`Route 53`
- 日志与告警（可选但很加分）：`CloudWatch Logs/Agent`
- 数据库/缓存（可选后续增强）：`RDS(PostgreSQL)`、`ElastiCache(Redis)`（MVP 可先继续容器化自带）

不建议为了关键词硬上（MVP 先不做）：
- `EKS`：学习/运维成本高，面试会被深挖集群网络、滚动发布、可观测性、权限等细节
- `Lambda + API Gateway`：需要把 API 改成 serverless 形态，不如先把核心业务做完整

---

## 3. 数据源与口径（Binance）

### 3.1 Binance 基础域名
- Spot REST：`https://api.binance.com`
- USDT-M Futures REST：`https://fapi.binance.com`

### 3.2 MVP 需要的典型数据
- 交易对列表：`/api/v3/exchangeInfo`（Spot），`/fapi/v1/exchangeInfo`（Futures）
- 行情（24h 统计）：Spot `GET /api/v3/ticker/24hr`，Futures `GET /fapi/v1/ticker/24hr`
- K 线：Spot `GET /api/v3/klines`，Futures `GET /fapi/v1/klines`
- 资金费率：Futures `GET /fapi/v1/fundingRate`（历史）
- 持仓量（Open Interest）：Futures `GET /fapi/v1/openInterest`（当前）

### 3.3 限流与稳定性策略
- 交易对列表 `exchangeInfo`：启动时拉取 + Redis/内存缓存 24h
- 主榜单数据：统一批量拉取（尽量避免每个币种单独请求）
- 失败重试：指数退避 + 熔断（短时间失败多次则降级到缓存/上次结果）
- 口径统一：明确“现货 vs 合约”的指标定义与展示开关

---

## 4. 功能范围（按页面拆解）

### 4.1 主页面（functions.md 的“main page”）
模块拆解（MVP）：
1) 数据聚合（涨/跌榜）
   - 输入：24h ticker（Spot/Futures）
   - 输出：涨幅/跌幅 Top N + 交易量、成交额（按口径选择）
2) 滚动 / 近 15 分钟
   - 输入：WebSocket miniTicker 或轮询（MVP 可先轮询）
   - 输出：近 15 分钟涨跌幅 Top N
3) K 线 / 1 小时
   - 输入：近 1 小时 K 线（或 24h ticker + 1h kline）
   - 输出：1h 涨跌幅 Top N
4) 热点/市场异动
   - 输入：自定义“异动规则引擎”（见 6.1）
   - 输出：类似快讯的自然语言摘要（可模板化生成）
5) 收藏
   - 输出：用户自选币种列表（匿名 token）

### 4.2 币种详情页（functions.md 的“Coin detail page”）
1) 基本信息
   - 今日价格、价格区间、振幅、昨日/上周/本周涨跌、资金费率、持仓量等
2) 小时快照（过去 24h）
   - 每小时净流入/持仓变化（MVP 可先用 OI + 价格变化近似）
3) 每日快照（近 30 天）
   - 按日累计净资金（MVP 用“突破支撑/阻力（简化版）”，并在 UI 标注口径）
4) 近日数据
   - 每日净资金柱状图 + 近 20 日高低点折线

---

## 5. 系统架构与数据流

### 5.1 架构图（文字版）
- 前端 Web：展示榜单、图表、收藏与详情页
- 后端 API：
  - Market Aggregator：拉取 Binance 数据、做聚合/排序/指标计算
  - Anomaly Engine：根据规则产出“异动事件”
  - Storage：落库快照与事件，供历史图表与回溯
- 基础设施：PostgreSQL + Redis

### 5.2 数据刷新策略（MVP）
- `ticker/24hr`：每 10~15 秒拉取（可配置）
- `klines`：详情页按需拉取 + 缓存（TTL 1~5 分钟）
- `openInterest`：详情页/热点按需拉取（TTL 30~60 秒）
- 事件/快讯：每 15 秒评估一次规则

---

## 6. 关键计算逻辑（MVP 版本）

### 6.1 异动规则（可解释、可面试深挖）
先做 3 类可落地规则（每条规则都能解释“为什么这样定义”，并且能把“信号证据”存下来，便于面试深挖）：

1) **突破支撑/阻力（非简化：多触点水平位 + 过滤假突破 + 可解释证据）**
   - 目标：比“用最近 N 根高低点画区间”更稳，尽量减少震荡区间的来回打脸（whipsaw），同时保持可实现与可解释。
   - 核心思路：先从 K 线中提取**枢轴点（pivot/fractal）** → 形成候选水平位（S/R levels）→ 计算强度（触碰次数/成交量/时间衰减）→ 定义突破判定（收盘确认 + 波动阈值）→ 增加确认机制（连续收盘/回踩确认）过滤假突破。

   **(a) 枢轴点提取（Pivot High/Low）**
   - 给定窗口 `w`（例如 2~4），当 `high[i]` 是区间 `[i-w, i+w]` 的最大值，则 `i` 为 pivot high；`low[i]` 类似。
   - 直觉：pivot 代表“短期供需转折点”，多次出现的价格带更可能成为水平阻力/支撑。

   **(b) 水平位聚类（Level Clustering）**
   - 将 pivot price 聚合成“水平位”，聚类容忍度使用**波动自适应阈值**而非固定百分比，推荐：
     - `tol = max(pct * price, k_atr * ATR)`（例如 `pct=0.15%`，`k_atr=0.2`）
   - 每个 level 记录：
     - `level_price`（聚类中心价/加权均值）
     - `touches`（触碰次数）
     - `last_touch_at`（最后触碰时间）
     - `timeframe`（来源周期：15m/1h/4h/1d）
     - `strength_score`（强度分：触碰次数 + 近期性衰减 + 触碰时成交量加权）
   - 过滤：`touches >= min_touches`（例如 3）才作为有效支撑/阻力。

   **(c) 突破判定（Breakout Definition）**
   - 只用“收盘价”判定突破，避免影线噪声：
     - 向上突破阻力：`close > level_price + margin`
     - 向下跌破支撑：`close < level_price - margin`
   - `margin` 建议同样使用波动自适应：
     - `margin = max(pct_break * price, k_break_atr * ATR)`（例如 `pct_break=0.10%`，`k_break_atr=0.25`）

   **(d) 假突破过滤（Confirmation）**
   - 两种确认方式，MVP 建议都支持并可配置：
     1) **连续收盘确认**：要求 `confirm_closes` 根 K 线都在突破方向（例如 2 根）
     2) **回踩确认（Retest）**：突破后在 `retest_window` 内回踩 level 不破（上破后 `low >= level_price - margin`），并再次上行
   - 可选增强（更像“交易系统”但仍可解释）：
     - 成交量确认：突破 K 线成交量 > `avg_volume * vol_factor`（例如 1.5x）
     - 合约 OI 确认：突破时 Open Interest 同向增加（需要 `swap` 的 OI 数据）

   **(e) 多周期一致性（Multi-timeframe, 可选但推荐）**
   - 水平位优先来自更大周期（例如 4h/1d），突破信号来自更小周期（例如 15m/1h）。
   - **本项目默认（你选择 A）**：`15m` 产生信号 + `4h` 生成水平位。
   - 事件中写明：`"15m 突破 4h 阻力"`，可解释性与“像真项目”都会更强。

   **(f) 事件证据（必须落库/可追溯）**
   - 每条“突破/跌破”事件保存：
     - level 详情（price、touches、timeframe、strength_score）
     - 触发 candle（open/high/low/close/volume、ATR、margin）
     - 采用的确认方式与是否通过（连续收盘/回踩）

   **(g) 推荐默认参数（可配置）**
   - `tf_signal=15m`（信号周期，默认）
   - `tf_level=4h`（水平位周期，默认）
   - `pivot_window=3`
   - `min_touches=3`
   - `atr_period=14`
   - `tol=max(0.15%*price, 0.2*ATR)`
   - `margin=max(0.10%*price, 0.25*ATR)`
   - `confirm_closes=2`
   - `retest_window=24`（在 `15m` 周期下约 6 小时；若改用 `1h` 信号周期，可相应调小）
   - `vol_confirm_factor=1.5`（可选）

2) **振幅异常（Amplitude Spike）**
   - `abs(return_5m)` 或 `abs(return_15m)` 超过阈值（比如 2%）触发
   - 增强建议：阈值使用“历史分位数/滚动标准差”而不是固定值（例如过去 7 天同周期 return 的 95 分位）

3) **量能异常（Volume Spike）**
   - `volume_factor = current_volume / avg_volume(过去X周期)`
   - 增强建议：对不同市值/不同交易量币种做分层阈值，避免小币种噪声

事件输出使用模板生成（中文）：
- `"{symbol} - ₮{price} ({return_15m}%)，{reason}（{tf_signal} 突破 {tf_level} {level_price}，touches={touches}，margin={margin}），日内振幅 {amplitude}%，用时 {duration}"`

### 6.2 榜单指标
MVP 只做“确定可算”的指标，避免过度玄学：
- 5m/15m/1h/24h return（涨跌幅）
- 24h 交易量/成交额（取 Binance 口径）
- 日内振幅（可用 24h high/low 或 K 线计算）

---

## 7. API 设计（对齐抓包路径风格）

> 你 HAR 里出现了类似路径，MVP 可以沿用，便于后续扩展。

### 7.1 交易对
- `GET /api/symbol/getpairs?market=spot|swap`
  - 返回：交易对列表（只保留 USDT 计价、过滤杠杆代币等）

### 7.2 主页面聚合
- `GET /api/aggregate/basicinfo?market=spot|swap`
  - 返回：涨跌榜、滚动榜、1h 榜等聚合数据（一次返回，减少前端请求）
- `GET /api/aggregate/hotMarkets?market=spot|swap`
  - 返回：热点/异动快讯列表

### 7.3 详情页数据
- `GET /api/kline/GetKlines?symbol=BTCUSDT&market=spot|swap&interval=1h&limit=200`
- `GET /api/aggregate/fundSnapshots?symbol=BTCUSDT&market=swap&period=30d`
- `GET /api/aggregate/oiHistory?symbol=BTCUSDT&period=24h`
- `GET /api/aggregate/fundData?symbol=BTCUSDT`

### 7.4 收藏
- `GET /api/user/favorites`
- `POST /api/user/favorites`（body：symbols）
- `DELETE /api/user/favorites/{symbol}`

鉴权（MVP 简化）：
- 使用匿名 `client_id`（前端 localStorage 生成 UUID），后端以此作为收藏分区键

---

## 8. 数据模型（MVP）

建议表（可先少建，逐步补齐）：
- `symbols`：交易对元数据（market、base、quote、精度、状态）
- `snapshots_24h`：ticker 快照（每 15s 或 1m 一条，按需）
- `anomaly_events`：异动事件（规则、触发值、时间、描述）
- `favorites`：`client_id + symbol + market`

---

## 9. 目录结构建议（落地友好）

在 `CoinMark/` 下建议这样组织（后续实现时创建）：
- `apps/api/`：FastAPI 项目
- `apps/web/`：Vite + React 前端
- `infra/`：docker-compose、数据库初始化脚本
- `docs/`：设计文档、接口文档、压测报告

---

## 10. 测试与验证（简历“证据”来源）

### 10.1 单元测试
- 指标计算函数（return、amplitude、volume_factor）
- 异动规则判定（输入一组 K 线/价格序列，输出是否触发）

### 10.2 集成测试
- Mock Binance 响应（或录制固定样本 JSON），验证 API 返回结构稳定

### 10.3 压测与报告
- 用 `k6` 或 `locust` 压测关键接口：
  - `/api/aggregate/basicinfo`
  - `/api/kline/GetKlines`
- 输出报告写进 `docs/benchmarks/`，作为简历量化指标依据

---

## 11. 里程碑（MVP 交付节奏）

M1（可运行 + 可演示）
- 前端：主页面静态布局 + 详情弹窗/路由
- 后端：接入 Binance ticker + klines + 交易对列表
- 能在本地 docker-compose 跑起来

M2（异动 + 收藏）
- 异动规则引擎（3 条规则）+ 快讯列表
- 收藏 API + 前端自选

M3（可量化 + 可写简历）
- 加 Redis 缓存 + 指标监控
- 压测脚本与报告
- README（中文）+ 架构图（文字/简单图）

M4（AWS 可演示上线，对齐你想写的云关键词）
- 前端：构建产物部署到 `S3`，用 `CloudFront` 加速与自定义域名（可选）
- 后端：在 `EC2` 上以 Docker/Compose 运行 API，配置 `Nginx` 反代 + HTTPS（Let's Encrypt）
- 可观测性（可选但推荐）：CloudWatch Agent 收集 Nginx/API 日志与基础指标
- 交付物：`docs/deploy/aws-ec2-s3-cloudfront.md` + 部署截图/访问链接 + 回滚说明

---

## 12. 简历 bullet（先给你一个可替换模板）

等你实现 M2/M3 后，可以写成这种“业务语境 + 技术 + 数字”的 bullet：
- 构建基于 Binance Spot/USDT-M Futures 的市场数据聚合服务（FastAPI + PostgreSQL + Redis），实现涨跌榜/滚动异动/币种详情图表等核心接口，支持秒级刷新与历史快照回溯。
- 设计并实现可解释的异动检测规则引擎（突破/振幅/量能），生成中文市场快讯；通过缓存与批量拉取降低外部 API 调用成本，将关键接口缓存命中场景 `p95` 延迟控制在可观测指标范围内（以压测报告为准）。
- 完成容器化部署（docker-compose）与压测/监控闭环（k6/locust + Prometheus 指标），沉淀可复用的性能验证与故障降级策略（限流/重试/熔断）。
- 将前端静态站点部署至 `AWS S3 + CloudFront`，后端以容器化方式部署至 `EC2` 并通过 `Nginx` 提供 HTTPS 反向代理；补齐上线所需的健康检查、日志与基础回滚流程。

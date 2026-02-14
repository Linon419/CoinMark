# CoinMark 架构优化（三阶段）Implementation Plan

> **For Claude/Codex:** 按阶段执行，先做阶段 1（仅参数降载），每阶段完成都要做观测与复盘，再进入下一阶段。

**Goal:** 在不牺牲核心行情可用性的前提下，显著降低 CPU 占用，减少实时链路抖动和超时，稳定资金快照与聚合接口。

**Current Bottleneck:**
- 当前是“全市场 + 高频 depth(100ms) + 双市场(spot/swap)”模式，`collector -> NATS(JetStream) -> ingest -> ClickHouse` 全链路都在高压运行。
- JetStream 同时承担高频实时与持久化，导致 NATS CPU/IO 压力大，历史恢复慢，易触发 publish timeout 或重启抖动。
- ingest 在高流量下需要同时解码、聚合、写入，容易成为第二热点。

**Target Architecture:**
- **实时层（高频）**：`collector -> NATS Core（非持久） -> ingest`
- **持久层（低频/结果）**：`ingest 聚合 -> ClickHouse`
- **配置/小事务层**：`SQLite`（用户、配置、小事务）
- **API 读取层**：行情接口主读 `ClickHouse`，必要时短缓存
- **恢复机制**：异常恢复以“窗口回补”为主，不依赖 JetStream 长历史重放

**Expected Result:**
- 容器 CPU 明显下降（优先降低 NATS、ingest）
- 实时链路 publish timeout 明显减少或消失
- 资金快照/聚合接口稳定，和目标数据源保持同口径（时区仅展示差异）

---

### Phase 1：参数降载（当天可落地，低风险）

**Objective:** 不改业务逻辑，先把高压流量降到可控区间。

**Files:**
- Modify: `infra/docker-compose.yml`
- Modify: `.env`
- Modify: `.env.example`

**Changes:**
1. 深度更新节流：`COLLECTOR_DEPTH_UPDATE_MS` 从 `100` 调整到 `300`（建议）或 `500`（更稳）
2. 可选符号限流：`COLLECTOR_SYMBOL_LIMIT` 先支持灰度（0=全量，>0=限量）
3. 资源上调（优先）：
   - `nats` 内存上限上调（避免频繁抖动）
   - `ingest` CPU/内存配额适度上调
4. 保留现有 API 兼容路径，不改接口协议

**Validation:**
- 观察 10~15 分钟：`docker stats` 与服务日志
- 目标：
  - NATS CPU 峰值明显回落
  - collector `*_send_fail` 基本归零
  - ingest runtime 连续稳定（无频繁重启）

**Rollback:**
- 回滚 compose/env 参数即可，风险最低。

---

### Phase 2：实时/持久链路解耦（核心改造）

**Objective:** 把“高频实时”和“可靠持久”彻底拆开。

**Files（预期）:**
- Modify: `infra/docker-compose.yml`
- Modify: `apps/collector-go/*`
- Modify: `apps/ingest-go/*`
- Modify: `apps/api/coinmark_api/services/*`（如需读取适配）

**Changes:**
1. 高频 raw topic 改走 NATS Core（不落 JetStream）
2. ingest 只订阅实时流并做窗口聚合（如 1s/1m）
3. 聚合结果直接写 ClickHouse（主事实表）
4. JetStream 仅保留“必须回放”的低频事件（如告警、任务状态）

**Validation:**
- 对比 Phase 1：
  - NATS CPU/磁盘 IO 再下降
  - ingest 延迟更稳定
  - 实时接口尾延迟下降

**Rollback:**
- 保留旧 subject/consumer 兼容开关，支持一键切回 JetStream 路径。

---

### Phase 3：读链路收口 + 恢复机制

**Objective:** 让 API 查询稳定、口径统一、可快速恢复。

**Files（预期）:**
- Modify: `apps/api/coinmark_api/api/routes/*`
- Modify: `apps/api/coinmark_api/services/clickhouse.py`
- Modify: `apps/api/coinmark_api/services/*backfill*`（新增或改造）
- Modify: `docs/deploy/*`（运维手册）

**Changes:**
1. 行情相关 API 全部主读 ClickHouse（SQLite 仅小事务）
2. 增加短窗口回补任务（按 symbol + time range）
3. 增加数据质量检查：缺桶率、延迟、跨源对账
4. 明确时区策略：存储 UTC，展示可切北京时间

**Validation:**
- 接口对账：`/api/aggregate/fundSnapshots` 与目标站点同口径
- 观测指标：缺桶率、查询 P95、回补成功率

**Rollback:**
- API 可临时启用 SQLite 兜底读取（仅短期应急）。

---

### Delivery Checklist（每阶段都做）

1. 发布前：记录 baseline（CPU、吞吐、错误率）
2. 发布后：观察 10~30 分钟并记录对比
3. 验收通过再进下一阶段，避免“多变量同时变更”
4. 每阶段产出变更说明与回滚说明

---

### Acceptance KPI（建议）

- NATS CPU 平均下降 ≥ 30%
- ingest CPU 平均下降 ≥ 20%
- collector publish timeout 基本为 0
- `fundSnapshots` 接口稳定返回，且数据口径一致（时间仅展示差异）

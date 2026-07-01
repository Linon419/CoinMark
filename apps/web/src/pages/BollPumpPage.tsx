import { useCallback, useEffect, useMemo, useState } from "react";
import { Button, Drawer, InputNumber, Message, Pagination, Select, Space, Switch, Table, Tag, Typography } from "@arco-design/web-react";
import BollPumpChart from "../components/BollPumpChart";
import {
  buildBollPumpTradeCandidates,
  filterActiveBollPumpStates,
  hasBollPumpFourHourBreakout,
  isBollPumpFourHourKeyK,
} from "./bollPumpCandidates";
import {
  fetchBollPumpDetail,
  fetchBollPumpFlowSnapshots,
  fetchBollPumpSignals,
  fetchBollPumpSettings,
  fetchBollPumpStates,
  fetchBollPumpStats,
  saveBollPumpSettings,
  type BollPumpDetail,
  type BollPumpFlowSnapshot,
  type BollPumpSettings,
  type BollPumpSignal,
  type BollPumpState,
} from "../services/bollPump";

const TIMEFRAMES = ["1m", "3m", "5m", "15m", "30m", "1h"];
const STAT_TIMEFRAMES = ["1m", "3m", "5m", "15m", "30m", "1h", "4h"];
const TRADE_CANDIDATE_PAGE_SIZE = 12;

function defaultBollPumpSettings(): BollPumpSettings {
  return {
    enabled: true,
    market: "swap",
    timeframes: TIMEFRAMES,
    symbol_limit: 1000,
    scan_timeout_sec: 45,
    boll_period: 20,
    boll_std_dev: 2,
    atr_period: 14,
    startup_windows: { "1m": 12, "3m": 10, "5m": 8, "15m": 6, "30m": 5, "1h": 4 },
    gain_thresholds: { "1m": 0.02, "3m": 0.025, "5m": 0.03, "15m": 0.04, "30m": 0.05, "1h": 0.06 },
    volume_thresholds: { "1m": 5, "3m": 3, "5m": 2.5, "15m": 2, "30m": 1.8, "1h": 1.5 },
    background_lookback: 80,
    background_recent_window: 10,
    background_recent_min_pass: 7,
    low_volume_factor: 0.8,
    middle_near_bandwidth_factor: 0.35,
    thin_quote_volume_24h: 2_000_000,
    watch_trend_check_candles: 6,
    watch_trend_max_drawdown_pct: 0.01,
    watch_trend_max_drawdown_atr: 0.75,
    trend_clean_bonus: 10,
    trend_wick_penalty: -25,
    trend_weak_penalty: 0,
    trend_wick_body_max_ratio: 0.35,
    trend_efficiency_min: 0.3,
    minimum_trend_timeframe: "15m",
    minimum_trend_check_candles: 20,
    minimum_trend_gain_pct: 0.01,
    minimum_trend_efficiency_min: 0.45,
    minimum_trend_rising_ratio: 0.6,
    resistance_lookback: 60,
    resistance_swing_span: 2,
    resistance_cluster_atr: 0.5,
    resistance_cluster_pct: 0.008,
    resistance_breakout_buffer_pct: 0.003,
    resistance_max_distance_pct: 0.04,
    resistance_min_touches: 2,
    resistance_breakout_score: 10,
    resistance_4h_lookback: 60,
    resistance_4h_swing_span: 2,
    resistance_4h_cluster_atr: 0.5,
    resistance_4h_cluster_pct: 0.008,
    resistance_4h_breakout_buffer_pct: 0.003,
    resistance_4h_max_distance_pct: 0.04,
    resistance_4h_min_touches: 2,
    resistance_4h_breakout_bonus: 15,
    key_k_4h_enabled: true,
    key_k_4h_lookback: 120,
    key_k_4h_threshold: 0.72,
    key_k_4h_min_volume_ratio: 0.8,
    key_k_4h_min_body_pct: 0.012,
    key_k_4h_max_sticky_score: 1,
    key_k_4h_telegram_threshold: 72,
    watch_telegram_threshold: 70,
    confirm1_telegram_threshold: 75,
    confirm2_telegram_threshold: 80,
    confluence_window_ms: 10 * 60 * 1000,
    stage_expiry_candles: 60,
  };
}

function cloneSettings(settings: BollPumpSettings): BollPumpSettings {
  return JSON.parse(JSON.stringify(settings));
}

function levelColor(level: string) {
  if (level === "KEY_K_4H") return "purple";
  if (level === "COMPLETED") return "red";
  if (level === "CONFIRM_2") return "red";
  if (level === "CONFIRM_1") return "orange";
  return "arcoblue";
}

function fmtNum(v: number, digits = 2) {
  if (!Number.isFinite(v)) return "-";
  return v.toFixed(digits);
}

function fmtTime(ms: number) {
  if (!Number.isFinite(ms)) return "-";
  const d = new Date(ms);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function fmtPrice(v: number) {
  if (!Number.isFinite(v)) return "-";
  if (v >= 100) return v.toFixed(2);
  if (v >= 1) return v.toFixed(4);
  return v.toPrecision(5);
}

function fmtCompactUSD(v: number) {
  if (!Number.isFinite(v)) return "-";
  const abs = Math.abs(v);
  if (abs >= 1_000_000_000) return `${(v / 1_000_000_000).toFixed(2)}B`;
  if (abs >= 1_000_000) return `${(v / 1_000_000).toFixed(2)}M`;
  if (abs >= 1_000) return `${(v / 1_000).toFixed(1)}K`;
  return v.toFixed(0);
}

function flowLabel(v?: BollPumpFlowSnapshot) {
  if (!v || !Number.isFinite(v.netNotional)) return "资金 -";
  const prefix = v.netNotional >= 0 ? "净流入" : "净流出";
  return `${prefix} ${fmtCompactUSD(Math.abs(v.netNotional))}`;
}

function hasFourHourResistanceBreakout(signal: BollPumpSignal) {
  return hasBollPumpFourHourBreakout(signal);
}

function isFourHourKeyK(signal: BollPumpSignal) {
  return isBollPumpFourHourKeyK(signal);
}

export default function BollPumpPage() {
  const { Title, Text } = Typography;
  const [signals, setSignals] = useState<BollPumpSignal[]>([]);
  const [states, setStates] = useState<BollPumpState[]>([]);
  const [stats, setStats] = useState<any>(null);
  const [settings, setSettings] = useState<BollPumpSettings>(() => defaultBollPumpSettings());
  const [settingsDraft, setSettingsDraft] = useState<BollPumpSettings>(() => defaultBollPumpSettings());
  const [detail, setDetail] = useState<BollPumpDetail | null>(null);
  const [open, setOpen] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [loading, setLoading] = useState(false);
  const [savingSettings, setSavingSettings] = useState(false);
  const [selectedTimeframe, setSelectedTimeframe] = useState<string | null>(null);
  const [inactiveStateCount, setInactiveStateCount] = useState(0);
  const [candidatePage, setCandidatePage] = useState(1);
  const [flowSnapshots, setFlowSnapshots] = useState<Record<string, BollPumpFlowSnapshot>>({});

  const refresh = useCallback(async () => {
    setLoading(true);
    try {
      const timeframeParam = selectedTimeframe ? `&timeframe=${encodeURIComponent(selectedTimeframe)}` : "";
      const [sig, st, stat] = await Promise.all([
        fetchBollPumpSignals(`market=swap&limit=300${timeframeParam}`),
        fetchBollPumpStates(`market=swap&limit=300&min_priority_score=60${timeframeParam}`),
        fetchBollPumpStats(),
      ]);
      const activeStates = filterActiveBollPumpStates((st.items || []) as BollPumpState[]);
      setSignals(sig.items || []);
      setStates(activeStates);
      setInactiveStateCount(Math.max(0, (st.items || []).length - activeStates.length));
      setStats(stat);
    } finally {
      setLoading(false);
    }
  }, [selectedTimeframe]);

  const loadSettings = async () => {
    const res = await fetchBollPumpSettings();
    setSettings(res.settings);
    setSettingsDraft(cloneSettings(res.settings));
  };

  useEffect(() => {
    void refresh();
    const timer = setInterval(() => {
      if (document.visibilityState === "visible") void refresh();
    }, 15000);
    return () => clearInterval(timer);
  }, [refresh]);

  useEffect(() => {
    void loadSettings();
  }, []);

  useEffect(() => {
    setCandidatePage(1);
  }, [selectedTimeframe]);

  const patchDraft = (patch: Partial<BollPumpSettings>) => {
    setSettingsDraft((prev) => ({ ...prev, ...patch }));
  };

  const setTimeframeEnabled = (tf: string, enabled: boolean) => {
    setSettingsDraft((prev) => {
      const next = enabled ? [...prev.timeframes, tf] : prev.timeframes.filter((x) => x !== tf);
      if (next.length === 0) return prev;
      return { ...prev, timeframes: TIMEFRAMES.filter((x) => next.includes(x)) };
    });
  };

  const setTFNumber = (group: "startup_windows" | "gain_thresholds" | "volume_thresholds", tf: string, value: number) => {
    setSettingsDraft((prev) => ({ ...prev, [group]: { ...prev[group], [tf]: value } }));
  };

  const saveSettings = async () => {
    setSavingSettings(true);
    try {
      const res = await saveBollPumpSettings(settingsDraft);
      setSettings(res.settings);
      setSettingsDraft(cloneSettings(res.settings));
      setSettingsOpen(false);
      Message.success("BOLL 参数已保存");
      void refresh();
    } catch (err: any) {
      Message.error(`保存失败：${err?.message || err}`);
    } finally {
      setSavingSettings(false);
    }
  };

  const openSignalDetail = useCallback(async (signalId?: number) => {
    if (!signalId) return;
    setDetail(await fetchBollPumpDetail(signalId));
    setOpen(true);
  }, []);

  const thresholdColumns = useMemo(
    () => [
      { title: "周期", dataIndex: "timeframe", width: 70 },
      {
        title: "启动K数",
        render: (_: any, r: { timeframe: string }) => (
          <InputNumber size="small" min={1} max={120} value={settingsDraft.startup_windows[r.timeframe]} onChange={(v) => setTFNumber("startup_windows", r.timeframe, Number(v || 1))} />
        ),
      },
      {
        title: "涨幅%",
        render: (_: any, r: { timeframe: string }) => (
          <InputNumber
            size="small"
            min={0.1}
            max={100}
            step={0.1}
            value={Number(((settingsDraft.gain_thresholds[r.timeframe] || 0) * 100).toFixed(3))}
            onChange={(v) => setTFNumber("gain_thresholds", r.timeframe, Number(v || 0) / 100)}
          />
        ),
      },
      {
        title: "量能倍数",
        render: (_: any, r: { timeframe: string }) => (
          <InputNumber size="small" min={0.1} max={50} step={0.1} value={settingsDraft.volume_thresholds[r.timeframe]} onChange={(v) => setTFNumber("volume_thresholds", r.timeframe, Number(v || 0.1))} />
        ),
      },
    ],
    [settingsDraft]
  );

  const signalColumns = useMemo(
    () => [
      { title: "时间", width: 124, render: (_: any, r: BollPumpSignal) => <span className="cm-mono">{fmtTime(r.signal_time_ms)}</span> },
      {
        title: "Symbol",
        dataIndex: "symbol",
        width: 174,
        render: (v: string, r: BollPumpSignal) => (
          <span className="cm-bollSymbolCell">
            <strong className="cm-symbol">{v}</strong>
            {isFourHourKeyK(r) ? (
              <Tag className="cm-bollKeyKTag" color="purple">
                4H关键K
              </Tag>
            ) : null}
            {hasFourHourResistanceBreakout(r) ? (
              <Tag className="cm-bollBreakoutTag" color="gold">
                4H突破
              </Tag>
            ) : null}
          </span>
        ),
      },
      { title: "周期", dataIndex: "timeframe", width: 70 },
      { title: "等级", width: 108, render: (_: any, r: BollPumpSignal) => <Tag color={levelColor(r.signal_level)}>{r.signal_level}</Tag> },
      { title: "价格", width: 112, align: "right", render: (_: any, r: BollPumpSignal) => <span className="cm-mono">{fmtPrice(Number(r.price || 0))}</span> },
      { title: "量能", width: 92, align: "right", render: (_: any, r: BollPumpSignal) => <span className="cm-mono">{fmtNum(Number(r.volume_ratio || 0))}x</span> },
      { title: "带宽", width: 96, align: "right", render: (_: any, r: BollPumpSignal) => <span className="cm-mono">{fmtNum(Number(r.boll_bandwidth || 0), 4)}</span> },
      {
        title: "分数",
        width: 76,
        align: "right",
        render: (_: any, r: BollPumpSignal) => <strong className={hasFourHourResistanceBreakout(r) ? "cm-mono cm-bollBreakoutScore" : "cm-mono"}>{fmtNum(Number(r.priority_score || r.score || 0), 0)}</strong>,
      },
      {
        title: "操作",
        width: 86,
        align: "right",
        render: (_: any, r: BollPumpSignal) => (
          <Button
            size="small"
            onClick={async () => {
              await openSignalDetail(r.id);
            }}
          >
            详情
          </Button>
        ),
      },
    ],
    [openSignalDetail]
  );

  const stateColumns = useMemo(
    () => [
      { title: "Symbol", dataIndex: "symbol", width: 122, render: (v: string) => <strong className="cm-symbol">{v}</strong> },
      { title: "触发", dataIndex: "timeframe", width: 66 },
      {
        title: "主导",
        width: 74,
        render: (_: any, r: BollPumpState) => {
          const dominant = r.dominant_timeframe || r.timeframe;
          const changed = dominant !== r.timeframe;
          return <Tag className={changed ? "cm-bollDominantTfTag" : undefined} color={changed ? "gold" : "gray"}>{dominant}</Tag>;
        },
      },
      { title: "状态", width: 126, render: (_: any, r: BollPumpState) => <Tag color={levelColor(r.status)}>{r.status}</Tag> },
      { title: "反弹", dataIndex: "bounce_count", width: 70, align: "right" },
      { title: "共振", width: 76, align: "right", render: (_: any, r: BollPumpState) => <span className="cm-mono">+{fmtNum(Number(r.confluence_score || 0), 0)}</span> },
      { title: "优先级", width: 86, align: "right", render: (_: any, r: BollPumpState) => <strong className="cm-mono">{fmtNum(Number(r.priority_score || 0), 0)}</strong> },
    ],
    []
  );

  const countsByLevel = stats?.countsByLevel || {};
  const countsByTimeframe = stats?.countsByTimeframe || {};
  const resistanceBreakoutCount = useMemo(() => signals.filter(hasFourHourResistanceBreakout).length, [signals]);
  const keyK4HCount = useMemo(() => signals.filter(isFourHourKeyK).length, [signals]);
  const tradeCandidates = useMemo(() => buildBollPumpTradeCandidates(states, signals, 0), [states, signals]);
  const candidatePageCount = Math.max(1, Math.ceil(tradeCandidates.length / TRADE_CANDIDATE_PAGE_SIZE));
  const effectiveCandidatePage = Math.min(candidatePage, candidatePageCount);
  const candidatePageStart = (effectiveCandidatePage - 1) * TRADE_CANDIDATE_PAGE_SIZE;
  const candidatePageEnd = Math.min(tradeCandidates.length, candidatePageStart + TRADE_CANDIDATE_PAGE_SIZE);
  const pagedTradeCandidates = useMemo(() => tradeCandidates.slice(candidatePageStart, candidatePageEnd), [tradeCandidates, candidatePageStart, candidatePageEnd]);
  const candidateRangeStart = tradeCandidates.length > 0 ? candidatePageStart + 1 : 0;
  const candidateFlowSymbols = useMemo(() => [...new Set(pagedTradeCandidates.map((x) => x.symbol).filter(Boolean))].join(","), [pagedTradeCandidates]);

  useEffect(() => {
    setCandidatePage((prev) => Math.min(prev, candidatePageCount));
  }, [candidatePageCount]);

  useEffect(() => {
    if (!candidateFlowSymbols) {
      setFlowSnapshots({});
      return;
    }
    let cancelled = false;
    const tzOffsetMin = new Date().getTimezoneOffset();
    fetchBollPumpFlowSnapshots(`market=swap&symbols=${encodeURIComponent(candidateFlowSymbols)}&timeMode=local&tzOffsetMin=${tzOffsetMin}`)
      .then((res) => {
        if (cancelled) return;
        const next: Record<string, BollPumpFlowSnapshot> = {};
        for (const item of res.items || []) {
          next[item.symbol] = item;
        }
        setFlowSnapshots(next);
      })
      .catch(() => {
        if (!cancelled) setFlowSnapshots({});
      });
    return () => {
      cancelled = true;
    };
  }, [candidateFlowSymbols]);

  return (
    <div className="cm-page cm-bollPage">
      <div className="cm-section cm-bollHeader">
        <div className="cm-sectionHeader">
          <div>
            <Title heading={5} style={{ margin: 0 }}>
              BOLL 泵盘扫描器
            </Title>
            <Text className="cm-muted">swap | BOLL多周期 + 4h关键K</Text>
          </div>
          <Space>
            <Button loading={loading} onClick={refresh}>
              刷新
            </Button>
            <Button
              type="primary"
              onClick={() => {
                setSettingsDraft(cloneSettings(settings));
                setSettingsOpen(true);
              }}
            >
              参数
            </Button>
          </Space>
        </div>
        <Space wrap>
          <Tag color={settings.enabled ? "green" : "gray"}>{settings.enabled ? "扫描开启" : "扫描暂停"}</Tag>
          <span className="cm-pill">信号 {signals.length}</span>
          <span className="cm-pill">活跃 {states.length}</span>
          <span className="cm-pill">候选 {tradeCandidates.length}</span>
          {inactiveStateCount > 0 ? <span className="cm-pill">隐藏失效 {inactiveStateCount}</span> : null}
          <span className="cm-pill">WATCH {countsByLevel.WATCH || 0}</span>
          <span className="cm-pill">CONFIRM_1 {countsByLevel.CONFIRM_1 || 0}</span>
          <span className="cm-pill">CONFIRM_2 {countsByLevel.CONFIRM_2 || 0}</span>
          <span className="cm-pill cm-bollKeyKPill">4h关键K {countsByLevel.KEY_K_4H || keyK4HCount}</span>
          <span className="cm-pill cm-bollBreakoutPill">4h突破 {resistanceBreakoutCount}</span>
          <span className="cm-pill">更新时间 {stats?.generatedAtMs ? new Date(stats.generatedAtMs).toLocaleTimeString() : "-"}</span>
        </Space>
      </div>

      <div className="cm-bollStats">
        {STAT_TIMEFRAMES.map((tf) => (
          <button
            key={tf}
            type="button"
            className={selectedTimeframe === tf ? "cm-bollStatItem cm-bollStatButton cm-bollStatButton--active" : "cm-bollStatItem cm-bollStatButton"}
            aria-pressed={selectedTimeframe === tf}
            onClick={() => setSelectedTimeframe((prev) => (prev === tf ? null : tf))}
          >
            <span className="cm-muted">{tf}</span>
            <strong>{countsByTimeframe[tf] || 0}</strong>
          </button>
        ))}
      </div>

      <div className="cm-bollTradeBand">
        <div className="cm-sectionHeader">
          <Title heading={6} style={{ margin: 0 }}>
            当前可交易候选
          </Title>
          <Space size={8}>
            <Text className="cm-muted cm-bollCandidateCount">
              {candidateRangeStart}-{candidatePageEnd} / {tradeCandidates.length}
            </Text>
            <Tag color="orange">CONFIRM_1</Tag>
            <Tag color="red">CONFIRM_2 / COMPLETED</Tag>
          </Space>
        </div>
        {tradeCandidates.length > 0 ? (
          <>
            <div className="cm-bollCandidateGrid">
              {pagedTradeCandidates.map((candidate) => (
                <div className="cm-bollCandidateCard" key={`${candidate.symbol}-${candidate.timeframe}`}>
                  <div className="cm-bollCandidateTop">
                    <strong className="cm-symbol">{candidate.symbol}</strong>
                    <Tag color={candidate.trade_label === "重点" ? "red" : candidate.trade_label === "关键K" ? "purple" : "orange"}>{candidate.trade_label}</Tag>
                  </div>
                  <div className="cm-bollCandidateMeta">
                    <span>
                      触发 <b>{candidate.timeframe}</b>
                    </span>
                    <span>
                      主导 <b>{candidate.dominant_timeframe || candidate.timeframe}</b>
                    </span>
                    <span>
                      反弹 <b>{candidate.bounce_count}</b>
                    </span>
                  </div>
                  <div className="cm-bollCandidateFlow">
                    <span className={Number(flowSnapshots[candidate.symbol]?.netNotional || 0) >= 0 ? "cm-number--pos" : "cm-number--neg"}>{flowLabel(flowSnapshots[candidate.symbol])}</span>
                    <span>
                      买/卖 {fmtCompactUSD(Number(flowSnapshots[candidate.symbol]?.buyNotional || 0))}/{fmtCompactUSD(Number(flowSnapshots[candidate.symbol]?.sellNotional || 0))}
                    </span>
                  </div>
                  <div className="cm-bollCandidateBottom">
                    <Tag color={levelColor(candidate.status)}>{candidate.status}</Tag>
                    {candidate.is_key_k_4h ? <Tag color="purple">4H关键K</Tag> : null}
                    {candidate.has_4h_breakout ? <Tag color="gold">4H突破</Tag> : null}
                    <strong className="cm-mono cm-bollCandidateScore">{fmtNum(Number(candidate.priority_score || 0), 0)}</strong>
                    <Button size="mini" disabled={!candidate.latest_signal_id} onClick={() => openSignalDetail(candidate.latest_signal_id)}>
                      详情
                    </Button>
                  </div>
                </div>
              ))}
            </div>
            {tradeCandidates.length > TRADE_CANDIDATE_PAGE_SIZE ? (
              <div className="cm-bollCandidatePager">
                <Pagination size="small" current={effectiveCandidatePage} pageSize={TRADE_CANDIDATE_PAGE_SIZE} total={tradeCandidates.length} onChange={(page) => setCandidatePage(page)} />
              </div>
            ) : null}
          </>
        ) : (
          <div className="cm-bollCandidateEmpty">暂无确认候选</div>
        )}
      </div>

      <div className="cm-bollWorkbench">
        <div className="cm-card cm-bollPanel">
          <div className="cm-sectionHeader">
            <Title heading={6} style={{ margin: 0 }}>
              最近信号
            </Title>
          </div>
          <Table
            className="cm-bollTable"
            rowKey="id"
            size="small"
            columns={signalColumns as any}
            data={signals as any}
            pagination={{ pageSize: 20 }}
            scroll={{ x: 938 }}
            rowClassName={(record) => {
              const row = record as BollPumpSignal;
              if (isFourHourKeyK(row)) return "cm-bollTableRow--keyK";
              if (hasFourHourResistanceBreakout(row)) return "cm-bollTableRow--resistanceBreakout";
              return "";
            }}
          />
        </div>
        <div className="cm-card cm-bollPanel">
          <div className="cm-sectionHeader">
            <Title heading={6} style={{ margin: 0 }}>
              活跃状态
            </Title>
          </div>
          <Table className="cm-bollTable" rowKey="id" size="small" columns={stateColumns as any} data={states as any} pagination={{ pageSize: 20 }} scroll={{ x: 620 }} />
        </div>
      </div>

      <Drawer className="cm-bollDetailDrawer" width="80%" visible={open} onCancel={() => setOpen(false)} footer={null} title={detail?.signal ? `${detail.signal.symbol} ${detail.signal.timeframe}` : "BOLL detail"}>
        {open ? <BollPumpChart detail={detail} bollPeriod={settings.boll_period} bollStdDev={settings.boll_std_dev} /> : null}
      </Drawer>

      <Drawer
        width={760}
        visible={settingsOpen}
        onCancel={() => setSettingsOpen(false)}
        title="BOLL 扫描参数"
        footer={
          <Space>
            <Button onClick={() => setSettingsDraft(defaultBollPumpSettings())}>恢复默认</Button>
            <Button onClick={() => setSettingsDraft(cloneSettings(settings))}>撤销修改</Button>
            <Button type="primary" loading={savingSettings} onClick={saveSettings}>
              保存
            </Button>
          </Space>
        }
      >
        <div className="cm-bollSettings">
          <div className="cm-bollSettingsBlock">
            <Title heading={6} style={{ margin: 0 }}>
              运行
            </Title>
            <div className="cm-bollSettingsGrid">
              <label>
                <span>扫描开关</span>
                <Switch checked={settingsDraft.enabled} onChange={(v) => patchDraft({ enabled: v })} />
              </label>
              <label>
                <span>市场</span>
                <Select value={settingsDraft.market} onChange={(v) => patchDraft({ market: v as "swap" | "spot" })}>
                  <Select.Option value="swap">swap</Select.Option>
                  <Select.Option value="spot">spot</Select.Option>
                </Select>
              </label>
              <label>
                <span>合约上限</span>
                <InputNumber min={20} max={1000} step={10} value={settingsDraft.symbol_limit} onChange={(v) => patchDraft({ symbol_limit: Number(v || 1000) })} />
              </label>
              <label>
                <span>超时秒数</span>
                <InputNumber min={5} max={300} step={5} value={settingsDraft.scan_timeout_sec} onChange={(v) => patchDraft({ scan_timeout_sec: Number(v || 45) })} />
              </label>
            </div>
            <div className="cm-bollTfButtons">
              {TIMEFRAMES.map((tf) => {
                const active = settingsDraft.timeframes.includes(tf);
                return (
                  <Button key={tf} size="small" type={active ? "primary" : "secondary"} onClick={() => setTimeframeEnabled(tf, !active)}>
                    {tf}
                  </Button>
                );
              })}
            </div>
          </div>

          <div className="cm-bollSettingsBlock">
            <Title heading={6} style={{ margin: 0 }}>
              指标
            </Title>
            <div className="cm-bollSettingsGrid">
              <label>
                <span>BOLL 周期</span>
                <InputNumber min={5} max={100} value={settingsDraft.boll_period} onChange={(v) => patchDraft({ boll_period: Number(v || 20) })} />
              </label>
              <label>
                <span>标准差倍数</span>
                <InputNumber min={0.5} max={5} step={0.1} value={settingsDraft.boll_std_dev} onChange={(v) => patchDraft({ boll_std_dev: Number(v || 2) })} />
              </label>
              <label>
                <span>ATR 周期</span>
                <InputNumber min={2} max={100} value={settingsDraft.atr_period} onChange={(v) => patchDraft({ atr_period: Number(v || 14) })} />
              </label>
              <label>
                <span>失效K数</span>
                <InputNumber min={5} max={300} value={settingsDraft.stage_expiry_candles} onChange={(v) => patchDraft({ stage_expiry_candles: Number(v || 60) })} />
              </label>
            </div>
          </div>

          <div className="cm-bollSettingsBlock">
            <Title heading={6} style={{ margin: 0 }}>
              启动阈值
            </Title>
            <Table rowKey="timeframe" size="small" pagination={false} columns={thresholdColumns as any} data={TIMEFRAMES.map((timeframe) => ({ timeframe })) as any} />
          </div>

          <div className="cm-bollSettingsBlock">
            <Title heading={6} style={{ margin: 0 }}>
              低波动加分
            </Title>
            <div className="cm-bollSettingsGrid">
              <label>
                <span>回看K数</span>
                <InputNumber min={20} max={500} value={settingsDraft.background_lookback} onChange={(v) => patchDraft({ background_lookback: Number(v || 80) })} />
              </label>
              <label>
                <span>近期窗口</span>
                <InputNumber min={3} max={100} value={settingsDraft.background_recent_window} onChange={(v) => patchDraft({ background_recent_window: Number(v || 10) })} />
              </label>
              <label>
                <span>横盘通过数</span>
                <InputNumber min={1} max={100} value={settingsDraft.background_recent_min_pass} onChange={(v) => patchDraft({ background_recent_min_pass: Number(v || 7) })} />
              </label>
              <label>
                <span>低量系数</span>
                <InputNumber min={0.05} max={2} step={0.05} value={settingsDraft.low_volume_factor} onChange={(v) => patchDraft({ low_volume_factor: Number(v || 0.8) })} />
              </label>
              <label>
                <span>中轨附近系数</span>
                <InputNumber min={0.05} max={2} step={0.05} value={settingsDraft.middle_near_bandwidth_factor} onChange={(v) => patchDraft({ middle_near_bandwidth_factor: Number(v || 0.35) })} />
              </label>
              <label>
                <span>最低24h成交额</span>
                <InputNumber min={100000} max={100000000} step={100000} value={settingsDraft.thin_quote_volume_24h} onChange={(v) => patchDraft({ thin_quote_volume_24h: Number(v || 2000000) })} />
              </label>
            </div>
          </div>

          <div className="cm-bollSettingsBlock">
            <Title heading={6} style={{ margin: 0 }}>
              趋势过滤
            </Title>
            <div className="cm-bollSettingsGrid">
              <label>
                <span>WATCH检查K数</span>
                <InputNumber min={2} max={60} value={settingsDraft.watch_trend_check_candles} onChange={(v) => patchDraft({ watch_trend_check_candles: Number(v || 6) })} />
              </label>
              <label>
                <span>最大回撤%</span>
                <InputNumber
                  min={0.1}
                  max={20}
                  step={0.1}
                  value={Number((settingsDraft.watch_trend_max_drawdown_pct * 100).toFixed(2))}
                  onChange={(v) => patchDraft({ watch_trend_max_drawdown_pct: Number(v || 1) / 100 })}
                />
              </label>
              <label>
                <span>ATR回撤倍数</span>
                <InputNumber min={0.1} max={5} step={0.05} value={settingsDraft.watch_trend_max_drawdown_atr} onChange={(v) => patchDraft({ watch_trend_max_drawdown_atr: Number(v || 0.75) })} />
              </label>
              <label>
                <span>清晰趋势加分</span>
                <InputNumber min={0} max={30} step={1} value={settingsDraft.trend_clean_bonus} onChange={(v) => patchDraft({ trend_clean_bonus: Number(v || 0) })} />
              </label>
              <label>
                <span>插针扣分</span>
                <InputNumber min={-50} max={0} step={1} value={settingsDraft.trend_wick_penalty} onChange={(v) => patchDraft({ trend_wick_penalty: Number(v || 0) })} />
              </label>
              <label>
                <span>插针实体阈值</span>
                <InputNumber min={0.05} max={0.8} step={0.05} value={settingsDraft.trend_wick_body_max_ratio} onChange={(v) => patchDraft({ trend_wick_body_max_ratio: Number(v || 0.35) })} />
              </label>
              <label>
                <span>趋势效率下限</span>
                <InputNumber min={0.05} max={1} step={0.05} value={settingsDraft.trend_efficiency_min} onChange={(v) => patchDraft({ trend_efficiency_min: Number(v || 0.3) })} />
              </label>
              <label>
                <span>最小趋势周期</span>
                <Select value={settingsDraft.minimum_trend_timeframe} onChange={(v) => patchDraft({ minimum_trend_timeframe: v as "15m" | "30m" | "1h" })}>
                  <Select.Option value="15m">15m</Select.Option>
                  <Select.Option value="30m">30m</Select.Option>
                  <Select.Option value="1h">1h</Select.Option>
                </Select>
              </label>
              <label>
                <span>中轨趋势K数</span>
                <InputNumber min={3} max={40} value={settingsDraft.minimum_trend_check_candles} onChange={(v) => patchDraft({ minimum_trend_check_candles: Number(v || 20) })} />
              </label>
              <label>
                <span>中轨涨幅%</span>
                <InputNumber
                  min={0.1}
                  max={20}
                  step={0.1}
                  value={Number((settingsDraft.minimum_trend_gain_pct * 100).toFixed(2))}
                  onChange={(v) => patchDraft({ minimum_trend_gain_pct: Number(v || 1) / 100 })}
                />
              </label>
              <label>
                <span>中轨上升比例</span>
                <InputNumber
                  min={10}
                  max={100}
                  step={5}
                  value={Number((settingsDraft.minimum_trend_rising_ratio * 100).toFixed(0))}
                  onChange={(v) => patchDraft({ minimum_trend_rising_ratio: Number(v || 60) / 100 })}
                />
              </label>
            </div>
          </div>

          <div className="cm-bollSettingsBlock">
            <Title heading={6} style={{ margin: 0 }}>
              当前周期箱体阻力
            </Title>
            <div className="cm-bollSettingsGrid">
              <label>
                <span>回看K数</span>
                <InputNumber min={20} max={300} value={settingsDraft.resistance_lookback} onChange={(v) => patchDraft({ resistance_lookback: Number(v || 60) })} />
              </label>
              <label>
                <span>Swing跨度</span>
                <InputNumber min={1} max={5} value={settingsDraft.resistance_swing_span} onChange={(v) => patchDraft({ resistance_swing_span: Number(v || 2) })} />
              </label>
              <label>
                <span>聚类ATR倍数</span>
                <InputNumber min={0.1} max={3} step={0.1} value={settingsDraft.resistance_cluster_atr} onChange={(v) => patchDraft({ resistance_cluster_atr: Number(v || 0.5) })} />
              </label>
              <label>
                <span>聚类价格%</span>
                <InputNumber
                  min={0.1}
                  max={5}
                  step={0.1}
                  value={Number((settingsDraft.resistance_cluster_pct * 100).toFixed(2))}
                  onChange={(v) => patchDraft({ resistance_cluster_pct: Number(v || 0.8) / 100 })}
                />
              </label>
              <label>
                <span>突破缓冲%</span>
                <InputNumber
                  min={0.1}
                  max={5}
                  step={0.1}
                  value={Number((settingsDraft.resistance_breakout_buffer_pct * 100).toFixed(2))}
                  onChange={(v) => patchDraft({ resistance_breakout_buffer_pct: Number(v || 0.3) / 100 })}
                />
              </label>
              <label>
                <span>最大距离%</span>
                <InputNumber
                  min={0.5}
                  max={20}
                  step={0.5}
                  value={Number((settingsDraft.resistance_max_distance_pct * 100).toFixed(2))}
                  onChange={(v) => patchDraft({ resistance_max_distance_pct: Number(v || 4) / 100 })}
                />
              </label>
              <label>
                <span>最少触碰数</span>
                <InputNumber min={1} max={8} value={settingsDraft.resistance_min_touches} onChange={(v) => patchDraft({ resistance_min_touches: Number(v || 2) })} />
              </label>
              <label>
                <span>突破得分</span>
                <InputNumber min={1} max={50} value={settingsDraft.resistance_breakout_score} onChange={(v) => patchDraft({ resistance_breakout_score: Number(v || 10) })} />
              </label>
            </div>
          </div>

          <div className="cm-bollSettingsBlock">
            <Title heading={6} style={{ margin: 0 }}>
              4h阻力突破
            </Title>
            <div className="cm-bollSettingsGrid">
              <label>
                <span>回看K数</span>
                <InputNumber min={20} max={200} value={settingsDraft.resistance_4h_lookback} onChange={(v) => patchDraft({ resistance_4h_lookback: Number(v || 60) })} />
              </label>
              <label>
                <span>Swing跨度</span>
                <InputNumber min={1} max={5} value={settingsDraft.resistance_4h_swing_span} onChange={(v) => patchDraft({ resistance_4h_swing_span: Number(v || 2) })} />
              </label>
              <label>
                <span>聚类ATR倍数</span>
                <InputNumber min={0.1} max={3} step={0.1} value={settingsDraft.resistance_4h_cluster_atr} onChange={(v) => patchDraft({ resistance_4h_cluster_atr: Number(v || 0.5) })} />
              </label>
              <label>
                <span>聚类价格%</span>
                <InputNumber
                  min={0.1}
                  max={5}
                  step={0.1}
                  value={Number((settingsDraft.resistance_4h_cluster_pct * 100).toFixed(2))}
                  onChange={(v) => patchDraft({ resistance_4h_cluster_pct: Number(v || 0.8) / 100 })}
                />
              </label>
              <label>
                <span>突破缓冲%</span>
                <InputNumber
                  min={0.1}
                  max={5}
                  step={0.1}
                  value={Number((settingsDraft.resistance_4h_breakout_buffer_pct * 100).toFixed(2))}
                  onChange={(v) => patchDraft({ resistance_4h_breakout_buffer_pct: Number(v || 0.3) / 100 })}
                />
              </label>
              <label>
                <span>最大距离%</span>
                <InputNumber
                  min={0.5}
                  max={20}
                  step={0.5}
                  value={Number((settingsDraft.resistance_4h_max_distance_pct * 100).toFixed(2))}
                  onChange={(v) => patchDraft({ resistance_4h_max_distance_pct: Number(v || 4) / 100 })}
                />
              </label>
              <label>
                <span>最少触碰数</span>
                <InputNumber min={1} max={6} value={settingsDraft.resistance_4h_min_touches} onChange={(v) => patchDraft({ resistance_4h_min_touches: Number(v || 2) })} />
              </label>
              <label>
                <span>突破加分</span>
                <InputNumber min={1} max={50} value={settingsDraft.resistance_4h_breakout_bonus} onChange={(v) => patchDraft({ resistance_4h_breakout_bonus: Number(v || 15) })} />
              </label>
            </div>
          </div>

          <div className="cm-bollSettingsBlock">
            <Title heading={6} style={{ margin: 0 }}>
              4h关键K
            </Title>
            <div className="cm-bollSettingsGrid">
              <label>
                <span>扫描开关</span>
                <Switch checked={settingsDraft.key_k_4h_enabled} onChange={(v) => patchDraft({ key_k_4h_enabled: v })} />
              </label>
              <label>
                <span>回看K数</span>
                <InputNumber min={30} max={500} value={settingsDraft.key_k_4h_lookback} onChange={(v) => patchDraft({ key_k_4h_lookback: Number(v || 120) })} />
              </label>
              <label>
                <span>KeyK阈值</span>
                <InputNumber min={0.1} max={1} step={0.01} value={settingsDraft.key_k_4h_threshold} onChange={(v) => patchDraft({ key_k_4h_threshold: Number(v || 0.72) })} />
              </label>
              <label>
                <span>最低量比</span>
                <InputNumber min={0} max={20} step={0.1} value={settingsDraft.key_k_4h_min_volume_ratio} onChange={(v) => patchDraft({ key_k_4h_min_volume_ratio: Number(v || 0) })} />
              </label>
              <label>
                <span>最低实体%</span>
                <InputNumber
                  min={1.2}
                  max={20}
                  step={0.1}
                  value={Number((settingsDraft.key_k_4h_min_body_pct * 100).toFixed(2))}
                  onChange={(v) => patchDraft({ key_k_4h_min_body_pct: Number(v || 0) / 100 })}
                />
              </label>
              <label>
                <span>Sticky上限</span>
                <InputNumber min={0.1} max={1} step={0.05} value={settingsDraft.key_k_4h_max_sticky_score} onChange={(v) => patchDraft({ key_k_4h_max_sticky_score: Number(v || 1) })} />
              </label>
              <label>
                <span>提醒分</span>
                <InputNumber min={0} max={200} value={settingsDraft.key_k_4h_telegram_threshold} onChange={(v) => patchDraft({ key_k_4h_telegram_threshold: Number(v || 72) })} />
              </label>
            </div>
          </div>

          <div className="cm-bollSettingsBlock">
            <Title heading={6} style={{ margin: 0 }}>
              提醒和共振
            </Title>
            <div className="cm-bollSettingsGrid">
              <label>
                <span>WATCH提醒分</span>
                <InputNumber min={0} max={100} value={settingsDraft.watch_telegram_threshold} onChange={(v) => patchDraft({ watch_telegram_threshold: Number(v || 70) })} />
              </label>
              <label>
                <span>CONFIRM_1提醒分</span>
                <InputNumber min={0} max={100} value={settingsDraft.confirm1_telegram_threshold} onChange={(v) => patchDraft({ confirm1_telegram_threshold: Number(v || 75) })} />
              </label>
              <label>
                <span>CONFIRM_2提醒分</span>
                <InputNumber min={0} max={100} value={settingsDraft.confirm2_telegram_threshold} onChange={(v) => patchDraft({ confirm2_telegram_threshold: Number(v || 80) })} />
              </label>
              <label>
                <span>共振窗口分钟</span>
                <InputNumber min={1} max={60} value={Math.round(settingsDraft.confluence_window_ms / 60000)} onChange={(v) => patchDraft({ confluence_window_ms: Number(v || 10) * 60000 })} />
              </label>
            </div>
          </div>
        </div>
      </Drawer>
    </div>
  );
}

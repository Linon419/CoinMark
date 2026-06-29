import { useEffect, useMemo, useState } from "react";
import { Button, Drawer, InputNumber, Message, Select, Space, Switch, Table, Tag, Typography } from "@arco-design/web-react";
import BollPumpChart from "../components/BollPumpChart";
import {
  fetchBollPumpDetail,
  fetchBollPumpSignals,
  fetchBollPumpSettings,
  fetchBollPumpStates,
  fetchBollPumpStats,
  saveBollPumpSettings,
  type BollPumpDetail,
  type BollPumpSettings,
  type BollPumpSignal,
  type BollPumpState,
} from "../services/bollPump";

const TIMEFRAMES = ["1m", "3m", "5m", "15m", "30m", "1h"];

function defaultBollPumpSettings(): BollPumpSettings {
  return {
    enabled: true,
    market: "swap",
    timeframes: TIMEFRAMES,
    symbol_limit: 200,
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
  if (level === "CONFIRM_2") return "red";
  if (level === "CONFIRM_1") return "orange";
  return "arcoblue";
}

function fmtNum(v: number, digits = 2) {
  if (!Number.isFinite(v)) return "-";
  return v.toFixed(digits);
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

  const refresh = async () => {
    setLoading(true);
    try {
      const [sig, st, stat] = await Promise.all([
        fetchBollPumpSignals(),
        fetchBollPumpStates("market=swap&limit=100&min_priority_score=60"),
        fetchBollPumpStats(),
      ]);
      setSignals(sig.items || []);
      setStates(st.items || []);
      setStats(stat);
    } finally {
      setLoading(false);
    }
  };

  const loadSettings = async () => {
    const res = await fetchBollPumpSettings();
    setSettings(res.settings);
    setSettingsDraft(cloneSettings(res.settings));
  };

  useEffect(() => {
    void refresh();
    void loadSettings();
    const timer = setInterval(() => {
      if (document.visibilityState === "visible") void refresh();
    }, 15000);
    return () => clearInterval(timer);
  }, []);

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
      { title: "时间", render: (_: any, r: BollPumpSignal) => new Date(r.signal_time_ms).toLocaleString() },
      { title: "Symbol", dataIndex: "symbol" },
      { title: "周期", dataIndex: "timeframe" },
      { title: "等级", render: (_: any, r: BollPumpSignal) => <Tag color={levelColor(r.signal_level)}>{r.signal_level}</Tag> },
      { title: "价格", render: (_: any, r: BollPumpSignal) => Number(r.price).toPrecision(6) },
      { title: "量能", render: (_: any, r: BollPumpSignal) => `${fmtNum(Number(r.volume_ratio || 0))}x` },
      { title: "带宽", render: (_: any, r: BollPumpSignal) => fmtNum(Number(r.boll_bandwidth || 0), 4) },
      { title: "分数", render: (_: any, r: BollPumpSignal) => fmtNum(Number(r.priority_score || r.score || 0), 0) },
      {
        title: "操作",
        render: (_: any, r: BollPumpSignal) => (
          <Button
            size="small"
            onClick={async () => {
              setDetail(await fetchBollPumpDetail(r.id));
              setOpen(true);
            }}
          >
            详情
          </Button>
        ),
      },
    ],
    []
  );

  const stateColumns = useMemo(
    () => [
      { title: "Symbol", dataIndex: "symbol" },
      { title: "周期", dataIndex: "timeframe" },
      { title: "状态", render: (_: any, r: BollPumpState) => <Tag color={levelColor(r.status)}>{r.status}</Tag> },
      { title: "反弹", dataIndex: "bounce_count" },
      { title: "共振", render: (_: any, r: BollPumpState) => `+${fmtNum(Number(r.confluence_score || 0), 0)}` },
      { title: "优先级", render: (_: any, r: BollPumpState) => fmtNum(Number(r.priority_score || 0), 0) },
    ],
    []
  );

  const countsByLevel = stats?.countsByLevel || {};
  const countsByTimeframe = stats?.countsByTimeframe || {};

  return (
    <div className="cm-page">
      <div className="cm-section">
        <div className="cm-sectionHeader">
          <div>
            <Title heading={5} style={{ margin: 0 }}>
              BOLL 泵盘扫描器
            </Title>
            <Text className="cm-muted">swap | 1m / 3m / 5m / 15m / 30m / 1h</Text>
          </div>
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
        </div>
        <Space wrap>
          <Tag color={settings.enabled ? "green" : "gray"}>{settings.enabled ? "扫描开启" : "扫描暂停"}</Tag>
          <span className="cm-pill">信号 {signals.length}</span>
          <span className="cm-pill">活跃 {states.length}</span>
          <span className="cm-pill">WATCH {countsByLevel.WATCH || 0}</span>
          <span className="cm-pill">CONFIRM_1 {countsByLevel.CONFIRM_1 || 0}</span>
          <span className="cm-pill">CONFIRM_2 {countsByLevel.CONFIRM_2 || 0}</span>
          <span className="cm-pill">更新时间 {stats?.generatedAtMs ? new Date(stats.generatedAtMs).toLocaleTimeString() : "-"}</span>
        </Space>
      </div>

      <div className="cm-card cm-bollStats">
        {["1m", "3m", "5m", "15m", "30m", "1h"].map((tf) => (
          <div key={tf} className="cm-bollStatItem">
            <span className="cm-muted">{tf}</span>
            <strong>{countsByTimeframe[tf] || 0}</strong>
          </div>
        ))}
      </div>

      <div className="cm-grid-2">
        <div className="cm-card cm-bollPanel">
          <div className="cm-sectionHeader">
            <Title heading={6} style={{ margin: 0 }}>
              最近信号
            </Title>
          </div>
          <Table rowKey="id" size="small" columns={signalColumns as any} data={signals as any} pagination={{ pageSize: 20 }} />
        </div>
        <div className="cm-card cm-bollPanel">
          <div className="cm-sectionHeader">
            <Title heading={6} style={{ margin: 0 }}>
              活跃状态
            </Title>
          </div>
          <Table rowKey="id" size="small" columns={stateColumns as any} data={states as any} pagination={{ pageSize: 20 }} />
        </div>
      </div>

      <Drawer width="80%" visible={open} onCancel={() => setOpen(false)} footer={null} title={detail?.signal ? `${detail.signal.symbol} ${detail.signal.timeframe}` : "BOLL detail"}>
        <Text className="cm-muted">{detail?.signal?.reason || ""}</Text>
        <BollPumpChart detail={detail} />
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
                <span>扫描数量</span>
                <InputNumber min={20} max={1000} step={10} value={settingsDraft.symbol_limit} onChange={(v) => patchDraft({ symbol_limit: Number(v || 200) })} />
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

import { useEffect, useMemo, useState } from "react";
import { Button, InputNumber, Space, Table, Tag, Typography } from "@arco-design/web-react";

type MarketScope = "spot" | "swap" | "both";

type BacktestParams = {
  z_threshold: number;
  lookback_minutes: number;
  detection_window_minutes: number;
  min_large_count: number;
  buy_ratio_threshold: number;
  forecast_horizon_minutes: number;
  cooldown_minutes: number;
  symbol_limit: number;
};

type BacktestRun = {
  runId: string;
  status: "running" | "done" | "failed";
  error?: string | null;
  result?: any;
};

type RealtimeSignal = {
  market: "spot" | "swap";
  symbol: string;
  ts: number;
  close: number;
  netFlow: number;
  zScore: number;
  largeBuyCount: number;
  buyRatio: number;
  score: number;
  signalState: "NONE" | "WATCH" | "CONFIRM" | "STRONG";
};

const API_BASE = (import.meta as any).env?.VITE_API_BASE || "";

async function req<T>(path: string, init?: RequestInit): Promise<T> {
  const r = await fetch(`${API_BASE}${path}`, {
    headers: { "Content-Type": "application/json" },
    ...init,
  });
  if (!r.ok) throw new Error(`HTTP ${r.status}`);
  return (await r.json()) as T;
}

function stateColor(state: string) {
  if (state === "STRONG") return "red";
  if (state === "CONFIRM") return "orange";
  if (state === "WATCH") return "arcoblue";
  return "gray";
}

export default function SignalLabPage() {
  const { Title, Text } = Typography;

  const [market, setMarket] = useState<MarketScope>("both");
  const [days, setDays] = useState<number>(7);
  const [syncScoreFlow, setSyncScoreFlow] = useState<boolean>(true);
  const [params, setParams] = useState<BacktestParams>({
    z_threshold: 2,
    lookback_minutes: 1440,
    detection_window_minutes: 240,
    min_large_count: 3,
    buy_ratio_threshold: 0.7,
    forecast_horizon_minutes: 60,
    cooldown_minutes: 30,
    symbol_limit: 200,
  });

  const [run, setRun] = useState<BacktestRun | null>(null);
  const [realtime, setRealtime] = useState<any>(null);
  const [loadingBacktest, setLoadingBacktest] = useState(false);
  const [loadingRealtime, setLoadingRealtime] = useState(false);

  const startBacktest = async () => {
    setLoadingBacktest(true);
    try {
      const created = await req<{ runId: string; status: string }>("/api/signal-lab/backtest", {
        method: "POST",
        body: JSON.stringify({ market, days, params }),
      });
      setRun({ runId: created.runId, status: "running" });
    } finally {
      setLoadingBacktest(false);
    }
  };

  const refreshRealtime = async () => {
    setLoadingRealtime(true);
    try {
      const query = new URLSearchParams({
        market,
        limit: "100",
        syncScoreFlow: String(syncScoreFlow),
        zThreshold: String(params.z_threshold),
        lookbackMinutes: String(params.lookback_minutes),
        detectionWindowMinutes: String(params.detection_window_minutes),
        minLargeCount: String(params.min_large_count),
        buyRatioThreshold: String(params.buy_ratio_threshold),
        cooldownMinutes: String(params.cooldown_minutes),
        symbolLimit: String(params.symbol_limit),
      });
      const data = await req<any>(`/api/signal-lab/realtime?${query.toString()}`);
      setRealtime(data);
    } finally {
      setLoadingRealtime(false);
    }
  };

  useEffect(() => {
    if (!run || run.status !== "running") return;
    const timer = setInterval(async () => {
      const latest = await req<BacktestRun>(`/api/signal-lab/runs/${run.runId}`);
      setRun(latest);
      if (latest.status !== "running") clearInterval(timer);
    }, 2000);
    return () => clearInterval(timer);
  }, [run?.runId, run?.status]);

  useEffect(() => {
    void refreshRealtime();
    const timer = setInterval(() => {
      if (document.visibilityState === "visible") {
        void refreshRealtime();
      }
    }, 15000);
    return () => clearInterval(timer);
  }, [market, syncScoreFlow, params.z_threshold, params.lookback_minutes, params.detection_window_minutes, params.min_large_count, params.buy_ratio_threshold, params.cooldown_minutes, params.symbol_limit]);

  const realtimeColumns = useMemo(
    () => [
      {
        title: "时间",
        render: (_: any, row: RealtimeSignal) => new Date(row.ts).toLocaleString(),
      },
      {
        title: "市场",
        render: (_: any, row: RealtimeSignal) => <Tag color={row.market === "swap" ? "purple" : "green"}>{row.market}</Tag>,
      },
      {
        title: "Symbol",
        dataIndex: "symbol",
      },
      {
        title: "状态",
        render: (_: any, row: RealtimeSignal) => <Tag color={stateColor(row.signalState)}>{row.signalState}</Tag>,
      },
      {
        title: "评分",
        render: (_: any, row: RealtimeSignal) => Number(row.score || 0).toFixed(1),
      },
      {
        title: "买入占比",
        render: (_: any, row: RealtimeSignal) => `${(Number(row.buyRatio || 0) * 100).toFixed(1)}%`,
      },
      {
        title: "大单次数",
        render: (_: any, row: RealtimeSignal) => Number(row.largeBuyCount || 0),
      },
      {
        title: "净流入",
        render: (_: any, row: RealtimeSignal) => Number(row.netFlow || 0).toFixed(2),
      },
    ],
    []
  );

  const sampleColumns = useMemo(
    () => [
      { title: "时间", render: (_: any, row: any) => new Date(row.ts).toLocaleString() },
      { title: "市场", render: (_: any, row: any) => <Tag color={row.market === "swap" ? "purple" : "green"}>{row.market}</Tag> },
      { title: "Symbol", dataIndex: "symbol" },
      { title: "评分", render: (_: any, row: any) => Number(row.score || 0).toFixed(1) },
      { title: "信号", render: (_: any, row: any) => <Tag color={stateColor(row.signalState)}>{row.signalState}</Tag> },
      { title: "H收益", render: (_: any, row: any) => `${(Number(row.retH || 0) * 100).toFixed(2)}%` },
      { title: "最大回撤", render: (_: any, row: any) => `${(Number(row.maxDrawdown || 0) * 100).toFixed(2)}%` },
    ],
    []
  );

  return (
    <div className="cm-page">
      <div className="cm-section">
        <div className="cm-sectionHeader">
          <Title heading={5} style={{ margin: 0 }}>
            Signal Lab（回测 + 实时联动）
          </Title>
          <Space>
            <Button type="primary" loading={loadingBacktest} onClick={startBacktest}>
              启动 7 天回测
            </Button>
            <Button loading={loadingRealtime} onClick={refreshRealtime}>
              刷新实时
            </Button>
          </Space>
        </div>
        <Text className="cm-muted">
          当前：{market} | 回测天数 {days} | 写入现有评分流 {syncScoreFlow ? "开启" : "关闭"}
        </Text>
      </div>

      <div className="cm-section">
        <div className="cm-card" style={{ padding: 12 }}>
          <Space wrap>
            <Tag>市场</Tag>
            <Button size="small" type={market === "both" ? "primary" : "secondary"} onClick={() => setMarket("both")}>both</Button>
            <Button size="small" type={market === "spot" ? "primary" : "secondary"} onClick={() => setMarket("spot")}>spot</Button>
            <Button size="small" type={market === "swap" ? "primary" : "secondary"} onClick={() => setMarket("swap")}>swap</Button>
            <Tag>z阈值</Tag>
            <InputNumber value={params.z_threshold} min={1} max={6} step={0.1} onChange={(v) => setParams((p) => ({ ...p, z_threshold: Number(v || 2) }))} />
            <Tag>检测窗口(分钟)</Tag>
            <InputNumber value={params.detection_window_minutes} min={15} max={1440} step={15} onChange={(v) => setParams((p) => ({ ...p, detection_window_minutes: Number(v || 240) }))} />
            <Tag>最少大单数</Tag>
            <InputNumber value={params.min_large_count} min={1} max={20} onChange={(v) => setParams((p) => ({ ...p, min_large_count: Number(v || 3) }))} />
            <Tag>买入占比</Tag>
            <InputNumber value={params.buy_ratio_threshold} min={0.5} max={1} step={0.01} onChange={(v) => setParams((p) => ({ ...p, buy_ratio_threshold: Number(v || 0.7) }))} />
            <Tag>TopN</Tag>
            <InputNumber value={params.symbol_limit} min={20} max={400} step={10} onChange={(v) => setParams((p) => ({ ...p, symbol_limit: Number(v || 200) }))} />
            <Button size="small" type={syncScoreFlow ? "primary" : "secondary"} onClick={() => setSyncScoreFlow((v) => !v)}>
              评分流 {syncScoreFlow ? "ON" : "OFF"}
            </Button>
          </Space>
        </div>
      </div>

      <div className="cm-section">
        <div className="cm-card" style={{ padding: 12 }}>
          <div className="cm-sectionHeader">
            <Title heading={6} style={{ margin: 0 }}>
              实时信号联动
            </Title>
            <Text className="cm-muted">
              已写入评分流事件：{Number(realtime?.insertedEvents || 0)}
            </Text>
          </div>
          <Table
            rowKey={(r) => `${(r as RealtimeSignal).market}-${(r as RealtimeSignal).symbol}-${(r as RealtimeSignal).ts}`}
            columns={realtimeColumns as any}
            data={(realtime?.signals || []) as RealtimeSignal[]}
            pagination={{ pageSize: 20 }}
            size="small"
          />
        </div>
      </div>

      <div className="cm-section">
        <div className="cm-card" style={{ padding: 12 }}>
          <div className="cm-sectionHeader">
            <Title heading={6} style={{ margin: 0 }}>
              回测结果（7天）
            </Title>
            <Text className="cm-muted">
              {run ? `runId: ${run.runId} | 状态: ${run.status}` : "尚未启动"}
            </Text>
          </div>
          {run?.status === "failed" && <Text type="error">{run.error || "回测失败"}</Text>}
          {run?.result?.summary && (
            <Space wrap style={{ marginBottom: 8 }}>
              <Tag color="arcoblue">总信号: {run.result.summary.signals}</Tag>
              <Tag color="green">胜率: {(Number(run.result.summary.winRate || 0) * 100).toFixed(2)}%</Tag>
              {Object.entries(run.result.markets || {}).map(([m, val]: any) => (
                <Tag key={m} color={m === "swap" ? "purple" : "green"}>
                  {m}: 信号 {val.signals} / 胜率 {(Number(val.winRate || 0) * 100).toFixed(2)}%
                </Tag>
              ))}
            </Space>
          )}
          <Table
            rowKey={(r) => `${(r as any).market}-${(r as any).symbol}-${(r as any).ts}`}
            columns={sampleColumns as any}
            data={(run?.result?.samples || []) as any[]}
            pagination={{ pageSize: 20 }}
            size="small"
          />
        </div>
      </div>
    </div>
  );
}

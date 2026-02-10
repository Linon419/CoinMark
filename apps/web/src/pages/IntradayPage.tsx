import { useEffect, useMemo, useRef, useState } from "react";
import { Button, Input, Message, Space, Typography } from "@arco-design/web-react";
import { useNavigate, useParams } from "react-router-dom";
import EChart from "../components/EChart";

type BucketKey = "1m" | "5m" | "15m" | "1h";

type FundIntradayResp = {
  symbol: string;
  bucket: BucketKey;
  spotAvailable?: boolean;
  swapAvailable?: boolean;
  items: Array<{
    bucketStartMs: number;
    swapValue: number;
    spotValue: number;
    swapDelta?: number;
    spotDelta?: number;
  }>;
};

type OrderbookIntradayItem = {
  bucketStartMs: number;
  swap: {
    spreadBps: number | null;
    depthImbalanceL20: number | null;
    micropriceShiftBps: number | null;
    wallPressureL20: number | null;
    aggrBuyRatio: number | null;
    replenishScore: number | null;
    sampleCount: number;
  };
  spot: {
    spreadBps: number | null;
    depthImbalanceL20: number | null;
    micropriceShiftBps: number | null;
    wallPressureL20: number | null;
    aggrBuyRatio: number | null;
    replenishScore: number | null;
    sampleCount: number;
  };
};

type OrderbookIntradayResp = {
  symbol: string;
  bucket: "1m";
  spotAvailable: boolean;
  swapAvailable: boolean;
  items: OrderbookIntradayItem[];
};

type AbsorptionSignalResp = {
  symbol: string;
  signalState: "NONE" | "WATCH" | "CONFIRM" | "STRONG";
  direction: "LONG_BIAS";
  score: number;
  cooldown: {
    active: boolean;
    secondsRemaining: number;
  };
  windows: Record<string, { passed: boolean; score: number }>;
  reasons: string[];
  ts: number;
};

type OrderbookWindow = "1m" | "3m" | "5m" | "15m" | "1h";

const ORDERBOOK_WINDOWS: Array<{ key: OrderbookWindow; label: string }> = [
  { key: "1m", label: "1m" },
  { key: "3m", label: "3m" },
  { key: "5m", label: "5m" },
  { key: "15m", label: "15m" },
  { key: "1h", label: "1h" },
];

const ORDERBOOK_WINDOW_POINTS: Record<OrderbookWindow, number> = {
  "1m": 120,
  "3m": 120,
  "5m": 120,
  "15m": 96,
  "1h": 72,
};

function orderbookWindowMs(window: OrderbookWindow): number {
  if (window === "1m") return 60_000;
  if (window === "3m") return 3 * 60_000;
  if (window === "5m") return 5 * 60_000;
  if (window === "15m") return 15 * 60_000;
  return 60 * 60_000;
}

function aggregateOrderbookSide(rows: Array<OrderbookIntradayItem["swap"]>): OrderbookIntradayItem["swap"] {
  const totalSample = rows.reduce((sum, row) => sum + (row.sampleCount || 0), 0);
  const weightedAvg = (getter: (row: OrderbookIntradayItem["swap"]) => number | null): number | null => {
    let weightedSum = 0;
    let weight = 0;
    for (const row of rows) {
      const value = getter(row);
      const sample = row.sampleCount || 0;
      if (value == null || !Number.isFinite(value) || sample <= 0) continue;
      weightedSum += value * sample;
      weight += sample;
    }
    return weight > 0 ? weightedSum / weight : null;
  };

  return {
    spreadBps: weightedAvg((row) => row.spreadBps),
    depthImbalanceL20: weightedAvg((row) => row.depthImbalanceL20),
    micropriceShiftBps: weightedAvg((row) => row.micropriceShiftBps),
    wallPressureL20: weightedAvg((row) => row.wallPressureL20),
    aggrBuyRatio: weightedAvg((row) => row.aggrBuyRatio),
    replenishScore: weightedAvg((row) => row.replenishScore),
    sampleCount: totalSample,
  };
}

function aggregateOrderbookItems(items: OrderbookIntradayItem[], window: OrderbookWindow): OrderbookIntradayItem[] {
  const maxPoints = ORDERBOOK_WINDOW_POINTS[window];
  if (!items.length) return [];
  if (window === "1m") return items.slice(-maxPoints);

  const bucketMs = orderbookWindowMs(window);
  const groups = new Map<number, OrderbookIntradayItem[]>();
  for (const item of items) {
    const bucketStartMs = Math.floor(item.bucketStartMs / bucketMs) * bucketMs;
    const group = groups.get(bucketStartMs);
    if (group) group.push(item);
    else groups.set(bucketStartMs, [item]);
  }

  const merged = Array.from(groups.entries())
    .sort((a, b) => a[0] - b[0])
    .map(([bucketStartMs, rows]) => ({
      bucketStartMs,
      swap: aggregateOrderbookSide(rows.map((row) => row.swap)),
      spot: aggregateOrderbookSide(rows.map((row) => row.spot)),
    }));

  return merged.slice(-maxPoints);
}

const API_BASE = (import.meta as any).env?.VITE_API_BASE || "";

async function getJson<T>(path: string): Promise<T> {
  const response = await fetch(`${API_BASE}${path}`);
  if (!response.ok) throw new Error(`HTTP ${response.status}`);
  return (await response.json()) as T;
}

function formatTime(ts: number) {
  return new Date(ts).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });
}

function formatCnCompact(v: number | null, digits = 2) {
  if (v == null || !Number.isFinite(v)) return "-";
  const abs = Math.abs(v);
  if (abs >= 1e8) return `${(v / 1e8).toFixed(digits)}亿`;
  if (abs >= 1e4) return `${(v / 1e4).toFixed(digits)}万`;
  if (abs >= 1) return v.toFixed(2);
  return v.toFixed(4);
}

function hasSpotSeries(resp?: FundIntradayResp): boolean {
  if (!resp) return false;
  if (resp.spotAvailable != null) return !!resp.spotAvailable;
  return (resp.items || []).some((x) => Math.abs(x.spotValue || 0) > 0 || Math.abs(x.spotDelta || 0) > 0);
}

function fmtBps(v: number | null, digits = 2) {
  if (v == null || !Number.isFinite(v)) return "-";
  return `${v >= 0 ? "+" : ""}${v.toFixed(digits)}bps`;
}

function fmtPct(v: number | null, digits = 1) {
  if (v == null || !Number.isFinite(v)) return "-";
  return `${(v * 100).toFixed(digits)}%`;
}

function fmtSignedPct(v: number | null, digits = 1) {
  if (v == null || !Number.isFinite(v)) return "-";
  return `${v >= 0 ? "+" : ""}${v.toFixed(digits)}%`;
}

const BUCKETS: Array<{ key: BucketKey; label: string; limit: number }> = [
  { key: "1m", label: "1分钟资金快照", limit: 60 },
  { key: "5m", label: "5分钟资金快照", limit: 72 },
  { key: "15m", label: "15分钟资金快照", limit: 96 },
  { key: "1h", label: "1小时资金快照", limit: 24 },
];

export default function IntradayPage() {
  const { Title, Text } = Typography;
  const { symbol } = useParams();
  const navigate = useNavigate();
  const initSym = (symbol || "BTCUSDT").toUpperCase();
  const [inputSym, setInputSym] = useState(initSym);
  const [activeSym, setActiveSym] = useState(initSym);
  const [loading, setLoading] = useState(false);
  const [lastUpdatedMs, setLastUpdatedMs] = useState<number | null>(null);
  const [dataMap, setDataMap] = useState<Record<string, FundIntradayResp>>({});
  const [orderbook, setOrderbook] = useState<OrderbookIntradayResp | null>(null);
  const [absorptionSignal, setAbsorptionSignal] = useState<AbsorptionSignalResp | null>(null);
  const [orderbookWindow, setOrderbookWindow] = useState<OrderbookWindow>("5m");
  const refreshSeq = useRef(0);

  useEffect(() => {
    const sym = (symbol || "BTCUSDT").toUpperCase();
    setInputSym(sym);
    setActiveSym(sym);
  }, [symbol]);

  const load = async () => {
    const sym = (activeSym || "BTCUSDT").toUpperCase();
    const seq = ++refreshSeq.current;
    setLoading(true);
    try {
      const results = await Promise.all(
        BUCKETS.map((bucket) =>
          getJson<FundIntradayResp>(`/api/coin/detail/fund/intraday?symbol=${sym}&bucket=${bucket.key}&limit=${bucket.limit}`)
        )
      );
      const orderbookResp = await getJson<OrderbookIntradayResp>(
        `/api/coin/detail/orderbook/intraday?symbol=${sym}&bucket=1m&limit=240`
      );
      const signalResp = await getJson<AbsorptionSignalResp>(
        `/api/coin/detail/orderbook/absorption-signal?symbol=${sym}&market=swap`
      );
      if (seq !== refreshSeq.current) return;
      const next: Record<string, FundIntradayResp> = {};
      for (const item of results) next[item.bucket] = item;
      setDataMap(next);
      setOrderbook(orderbookResp);
      setAbsorptionSignal(signalResp);
      setLastUpdatedMs(Date.now());
    } catch (error: any) {
      Message.error(`拉取失败：${error?.message || error}`);
      setAbsorptionSignal(null);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    load();
    const timer = setInterval(load, 60000);
    return () => clearInterval(timer);
  }, [activeSym]);

  const applySymbol = () => {
    const sym = inputSym.trim().toUpperCase();
    if (!sym) return;
    setActiveSym(sym);
    navigate(`/intraday/${sym}`);
  };

  const buildOption = (resp: FundIntradayResp | undefined, title: string) => {
    const rows = resp?.items || [];
    const showSpot = hasSpotSeries(resp);
    const xs = rows.map((x) => formatTime(x.bucketStartMs));
    const swap = rows.map((x) => x.swapValue || 0);
    const spot = rows.map((x) => x.spotValue || 0);
    const swapDelta = rows.map((x) => x.swapDelta || 0);
    const spotDelta = rows.map((x) => x.spotDelta || 0);

    const series: any[] = [
      {
        name: "合约增量",
        type: "bar",
        data: swapDelta,
        barWidth: 6,
        itemStyle: { color: "rgba(59,130,246,0.26)" },
        emphasis: { itemStyle: { color: "rgba(59,130,246,0.45)" } },
      },
      {
        name: "合约",
        type: "line",
        data: swap,
        symbol: "circle",
        symbolSize: 4,
        lineStyle: { width: 2, color: "#3b82f6" },
        itemStyle: { color: "#3b82f6" },
        label: { show: false },
      },
    ];
    if (showSpot) {
      series.splice(1, 0, {
        name: "现货增量",
        type: "bar",
        data: spotDelta,
        barWidth: 6,
        itemStyle: { color: "rgba(255,122,69,0.26)" },
        emphasis: { itemStyle: { color: "rgba(255,122,69,0.45)" } },
      });
      series.push({
        name: "现货",
        type: "line",
        data: spot,
        symbol: "circle",
        symbolSize: 4,
        lineStyle: { width: 2, color: "#ff7a45" },
        itemStyle: { color: "#ff7a45" },
        label: { show: false },
      });
    }

    return {
      title: { text: title, left: 0, top: 0, textStyle: { fontSize: 13, fontWeight: 600, color: "#0f172a" } },
      tooltip: {
        trigger: "axis",
        valueFormatter: (v: number) => formatCnCompact(v),
      },
      grid: { left: 50, right: 20, top: 26, bottom: 28 },
      legend: {
        data: showSpot ? ["合约增量", "现货增量", "合约", "现货"] : ["合约增量", "合约"],
        right: 0,
        top: 0,
        itemWidth: 10,
        itemHeight: 10,
        textStyle: { fontSize: 12, color: "#64748b" },
      },
      xAxis: {
        type: "category",
        data: xs,
        axisLine: { lineStyle: { color: "#e2e8f0" } },
        axisTick: { show: false },
        axisLabel: { color: "#94a3b8" },
      },
      yAxis: {
        type: "value",
        axisLabel: { formatter: (v: number) => formatCnCompact(v), color: "#94a3b8" },
        splitLine: { lineStyle: { color: "rgba(148,163,184,0.25)", type: "dashed" } },
      },
      series,
    };
  };

  const options = useMemo(
    () =>
      BUCKETS.map((bucket) => ({
        key: bucket.key,
        option: buildOption(dataMap[bucket.key], bucket.label),
      })),
    [dataMap]
  );

  const orderbookOption = useMemo(() => {
    const rows = aggregateOrderbookItems(orderbook?.items || [], orderbookWindow);
    const preferSwap = !!orderbook?.swapAvailable;
    const selected = rows.map((item) => (preferSwap ? item.swap : item.spot));
    const xs = rows.map((item) => formatTime(item.bucketStartMs));
    const spread = selected.map((item) => item.spreadBps ?? null);
    const imbalance = selected.map((item) => item.depthImbalanceL20 ?? null);
    const aggr = selected.map((item) => (item.aggrBuyRatio == null ? null : item.aggrBuyRatio * 100));
    const replenish = selected.map((item) => item.replenishScore ?? null);

    return {
      tooltip: {
        trigger: "axis",
      },
      grid: [
        { left: 50, right: 24, top: 24, height: 90 },
        { left: 50, right: 24, top: 152, height: 80 },
      ],
      xAxis: [
        {
          type: "category",
          data: xs,
          axisLine: { lineStyle: { color: "#e2e8f0" } },
          axisTick: { show: false },
          axisLabel: { show: false, color: "#94a3b8" },
          gridIndex: 0,
        },
        {
          type: "category",
          data: xs,
          axisLine: { lineStyle: { color: "#e2e8f0" } },
          axisTick: { show: false },
          axisLabel: { color: "#94a3b8" },
          gridIndex: 1,
        },
      ],
      yAxis: [
        {
          type: "value",
          axisLabel: { formatter: (v: number) => fmtBps(v), color: "#94a3b8" },
          splitLine: { lineStyle: { color: "rgba(148,163,184,0.25)", type: "dashed" } },
          gridIndex: 0,
        },
        {
          type: "value",
          min: -100,
          max: 100,
          axisLabel: { formatter: (v: number) => `${v}%`, color: "#94a3b8" },
          splitLine: { lineStyle: { color: "rgba(148,163,184,0.25)", type: "dashed" } },
          gridIndex: 1,
        },
      ],
      series: [
        {
          name: "Spread",
          type: "line",
          data: spread,
          xAxisIndex: 0,
          yAxisIndex: 0,
          symbol: "none",
          lineStyle: { width: 2, color: "#64748b" },
        },
        {
          name: "回补分",
          type: "line",
          data: replenish,
          xAxisIndex: 0,
          yAxisIndex: 0,
          symbol: "none",
          lineStyle: { width: 2, color: "#16a34a" },
        },
        {
          name: "深度不平衡",
          type: "bar",
          data: imbalance.map((v) => (v == null ? null : v * 100)),
          xAxisIndex: 1,
          yAxisIndex: 1,
          barWidth: 6,
          itemStyle: {
            color: (p: any) => ((p.value || 0) >= 0 ? "rgba(59,130,246,0.45)" : "rgba(239,68,68,0.45)"),
          },
          markLine: {
            symbol: ["none", "none"],
            label: { show: false },
            lineStyle: { type: "dashed", width: 1, color: "rgba(100,116,139,0.7)" },
            data: [{ yAxis: 10 }, { yAxis: 5 }, { yAxis: 0 }, { yAxis: -5 }, { yAxis: -10 }],
          },
        },
        {
          name: "主动买占比",
          type: "line",
          data: aggr,
          xAxisIndex: 1,
          yAxisIndex: 1,
          symbol: "none",
          lineStyle: { width: 2, color: "#f59e0b" },
        },
      ],
    };
  }, [orderbook, orderbookWindow]);

  const latestOrderbook = useMemo(() => {
    const rows = aggregateOrderbookItems(orderbook?.items || [], orderbookWindow);
    if (!rows.length) return null;
    const preferSwap = !!orderbook?.swapAvailable;
    const pick = (item: OrderbookIntradayItem) => (preferSwap ? item.swap : item.spot);
    const newest = rows[rows.length - 1];
    const latestValid = [...rows].reverse().find((item) => (pick(item).sampleCount ?? 0) > 0) || newest;
    const selected = pick(latestValid);
    return {
      marketLabel: preferSwap ? "合约" : "现货",
      ts: latestValid.bucketStartMs,
      newestTs: newest.bucketStartMs,
      isFallback: latestValid.bucketStartMs !== newest.bucketStartMs,
      spreadBps: selected.spreadBps,
      depthImbalancePct: selected.depthImbalanceL20 == null ? null : selected.depthImbalanceL20 * 100,
      aggrBuyPct: selected.aggrBuyRatio == null ? null : selected.aggrBuyRatio * 100,
      replenishScore: selected.replenishScore,
      sampleCount: selected.sampleCount,
    };
  }, [orderbook, orderbookWindow]);

  const spotAllMissing = useMemo(() => {
    const rows = BUCKETS.map((bucket) => dataMap[bucket.key]).filter(Boolean) as FundIntradayResp[];
    if (!rows.length) return false;
    return rows.every((resp) => !hasSpotSeries(resp));
  }, [dataMap]);

  const signalClassName = useMemo(() => {
    const state = absorptionSignal?.signalState;
    if (state === "STRONG" || state === "CONFIRM") return "cm-number--pos";
    if (state === "WATCH") return "cm-number--mono";
    return "cm-muted";
  }, [absorptionSignal]);

  return (
    <div className="cm-section">
      <div className="cm-card" style={{ padding: 12 }}>
        <div className="cm-intradayHeader">
          <div>
            <Title heading={6} style={{ margin: 0 }}>
              日内看盘
            </Title>
            <Text className="cm-muted">自动刷新：1分钟（折线=日内累计，柱状=单根增量）</Text>
          </div>
          <Space size="small">
            <Input
              value={inputSym}
              onChange={setInputSym}
              placeholder="输入币对，如 BTCUSDT"
              style={{ width: 160 }}
              size="small"
              allowClear
              onPressEnter={applySymbol}
            />
            <Button size="small" type="primary" loading={loading} onClick={applySymbol}>
              查看
            </Button>
            <Button size="small" type="secondary" loading={loading} onClick={load}>
              刷新
            </Button>
          </Space>
        </div>
        <div className="cm-muted" style={{ marginTop: 4 }}>
          {lastUpdatedMs
            ? `更新时间：${new Date(lastUpdatedMs).toLocaleTimeString([], {
                hour: "2-digit",
                minute: "2-digit",
                second: "2-digit",
                hour12: false,
              })}`
            : ""}
        </div>
        {spotAllMissing && (
          <div className="cm-muted" style={{ marginTop: 6, color: "#166534" }}>
            当前币对无现货数据，已仅展示合约资金曲线。
          </div>
        )}
      </div>

      <div className="cm-intradayGrid">
        {options.map((item) => (
          <div key={item.key} className="cm-card" style={{ padding: 12 }}>
            <EChart option={item.option} height={240} />
          </div>
        ))}
      </div>

      <div className="cm-card" style={{ padding: 12, marginTop: 12 }}>
        <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center" }}>
          <div>
            <Title heading={6} style={{ margin: 0 }}>
              吸筹信号
            </Title>
            <Text className="cm-muted">4h 预警 / 1d 确认 / 3d 强确认（仅信号，不下单）</Text>
          </div>
          <div className={signalClassName} style={{ fontWeight: 700 }}>
            {absorptionSignal?.signalState || "NONE"} · {absorptionSignal?.score ?? 0}
          </div>
        </div>
        <div style={{ display: "grid", gridTemplateColumns: "repeat(3, minmax(0, 1fr))", gap: 8, marginTop: 8 }}>
          <div className="cm-card" style={{ padding: 8 }}>
            <Text className="cm-muted">4h</Text>
            <div className="cm-number--mono">{absorptionSignal?.windows?.["4h"]?.passed ? "通过" : "未通过"}</div>
          </div>
          <div className="cm-card" style={{ padding: 8 }}>
            <Text className="cm-muted">1d</Text>
            <div className="cm-number--mono">{absorptionSignal?.windows?.["1d"]?.passed ? "通过" : "未通过"}</div>
          </div>
          <div className="cm-card" style={{ padding: 8 }}>
            <Text className="cm-muted">3d</Text>
            <div className="cm-number--mono">{absorptionSignal?.windows?.["3d"]?.passed ? "通过" : "未通过"}</div>
          </div>
        </div>
        <div className="cm-muted" style={{ marginTop: 8 }}>
          冷却：
          {absorptionSignal?.cooldown?.active
            ? `开启（剩余 ${Math.max(0, Math.floor((absorptionSignal.cooldown.secondsRemaining || 0) / 60))} 分钟）`
            : "未开启"}
        </div>
        <div className="cm-muted" style={{ marginTop: 4 }}>
          触发依据：{(absorptionSignal?.reasons || []).join("；") || "-"}
        </div>
      </div>

      <div className="cm-card" style={{ padding: 12, marginTop: 12 }}>
        <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: 8 }}>
          <div>
            <Title heading={6} style={{ margin: 0 }}>
              盘口原始指标
            </Title>
            <Text className="cm-muted">Spread / 深度不平衡 / 主动买占比 / 回补分（1分钟）</Text>
          </div>

          <div className="cm-subTabs" style={{ display: "flex", gap: 6 }}>
            {ORDERBOOK_WINDOWS.map((item) => (
              <button
                key={item.key}
                type="button"
                className={`cm-subTab ${orderbookWindow === item.key ? "cm-subTab--active" : ""}`}
                onClick={() => setOrderbookWindow(item.key)}
              >
                {item.label}
              </button>
            ))}
          </div>

        </div>
        <EChart option={orderbookOption} height={250} />
        <div style={{ display: "grid", gridTemplateColumns: "repeat(4, minmax(0, 1fr))", gap: 8, marginTop: 8 }}>
          <div className="cm-card" style={{ padding: 8 }}>
            <Text className="cm-muted">当前Spread</Text>
            <div className="cm-number--mono">{fmtBps(latestOrderbook?.spreadBps ?? null)}</div>
          </div>
          <div className="cm-card" style={{ padding: 8 }}>
            <Text className="cm-muted">当前深度不平衡</Text>
            <div className="cm-number--mono">{fmtSignedPct(latestOrderbook?.depthImbalancePct ?? null)}</div>
          </div>
          <div className="cm-card" style={{ padding: 8 }}>
            <Text className="cm-muted">当前主动买占比</Text>
            <div className="cm-number--mono">{fmtSignedPct(latestOrderbook?.aggrBuyPct ?? null)}</div>
          </div>
          <div className="cm-card" style={{ padding: 8 }}>
            <Text className="cm-muted">样本数（{orderbookWindow}）</Text>
            <div className="cm-number--mono">{latestOrderbook?.sampleCount ?? 0}</div>
          </div>
        </div>
        {(latestOrderbook?.sampleCount ?? 0) <= 0 && (
          <div style={{ marginTop: 8 }}>
            <Text style={{ color: "#d46b08" }}>本分钟无有效盘口样本，请等待下一分钟或切换币对。</Text>
          </div>
        )}
        {(latestOrderbook?.sampleCount ?? 0) > 0 && latestOrderbook?.isFallback && (
          <div style={{ marginTop: 8 }}>
            <Text style={{ color: "#6b7280" }}>
              当前分钟仍在采集中，已展示最近有效样本（{formatTime(latestOrderbook.ts)}）。
            </Text>
          </div>
        )}
        <div className="cm-muted" style={{ marginTop: 6 }}>
          解读：深度不平衡 &gt; 0 表示买盘更厚；主动买占比 &gt; 50% 偏多；Spread 越低越好；回补分越高表示被吃后补单更快。
        </div>
        <div className="cm-muted" style={{ marginTop: 6 }}>
          最新{latestOrderbook?.isFallback ? "有效" : ""}（{latestOrderbook?.marketLabel || "-"} {latestOrderbook ? formatTime(latestOrderbook.ts) : "--:--"}
          ）：Spread {fmtBps(latestOrderbook?.spreadBps ?? null)}，深度 {fmtSignedPct(latestOrderbook?.depthImbalancePct ?? null)}，主动买占比
          {fmtSignedPct(latestOrderbook?.aggrBuyPct ?? null)}
        </div>
      </div>
    </div>
  );
}

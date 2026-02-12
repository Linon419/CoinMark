import { useEffect, useMemo, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { Button, Select, Space, Tag, Typography } from "@arco-design/web-react";
import { IconClose, IconRefresh } from "@arco-design/web-react/icon";
import EChart from "../components/EChart";
import QuantChart, { type CvdMode, type QuantItem } from "../components/QuantChart";
import QuantHelp from "../components/QuantHelp";

type Market = "spot" | "swap";
type TimeDisplayMode = "local" | "utc";

type TabKey = "basic" | "hourly" | "daily" | "recent" | "quant";
type HourlySubTab = "snapshot" | "netflow";

type BasicResp = {
  basic: {
    symbol: string;
    asset: string;
    market: Market;
    lastPrice: number | null;
    priceChangePercent24h: number | null;
    highPrice24h: number | null;
    lowPrice24h: number | null;
    quoteVolume24h: number | null;
    eventTimeMs: number;
    source: string;
  };
  requestedMarket?: Market;
  effectiveMarket?: Market;
  marketFallback?: boolean;
  timeMode?: "utc" | "local";
  tzOffsetMin?: number;
  basicKvp?: Array<{ label: string; value: string }>;
  symbolStatus?: string | null;
  funding: { lastFundingRate: number; markPrice: number; timeMs: number } | null;
  openInterest: { openInterest: number; markPrice: number; oiNotionalUsd: number; timeMs: number } | null;
  marketCap: { priceUsd: number; circulatingSupply: number; marketCapUsd: number; source: string; timeMs: number } | null;
  srLevels: Array<{ levelPrice: number; touches: number; strengthScore: number; lastTouchMs: number }>;
  degraded?: boolean;
  warnings?: string[];
};

type FlowResp = {
  symbol: string;
  items: Array<{
    bucketStartMs: number;
    spotNetNotional: number;
    swapNetNotional: number;
    spotQuoteNotional: number;
    swapQuoteNotional: number;
    livePartial?: boolean;
  }>;
};

type OrderbookIntradayResp = {
  symbol: string;
  bucket: "1m";
  spotAvailable: boolean;
  swapAvailable: boolean;
  items: Array<{
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
  }>;
};

type QuantDashboardResp = {
  symbol: string;
  market: Market;
  requestedMarket: Market;
  marketFallback: boolean;
  bucket?: string;
  items: QuantItem[];
};

type WhaleRadarItem = {
  price: number;
  distancePct: number | null;
  value: number;
  durationMin: number;
  side?: "BUY" | "SELL" | "UNKNOWN";
  nature: "REAL" | "SPOOF" | "WATCH";
  label: string;
  lastSeenTs: number;
};

type WhaleRadarResp = {
  symbol: string;
  market: "spot" | "swap" | "spot+swap";
  sourceMarkets?: Array<"spot" | "swap">;
  lookbackMinutes: number;
  minLimit: number | null;
  spoofLimit: number | null;
  latestPrice?: number | null;
  snapshotTs?: number;
  items: WhaleRadarItem[];
  ts: number;
};

type QuantBucket = "15m" | "1h" | "4h" | "1d";

const QUANT_BUCKET_OPTIONS: Array<{ value: QuantBucket; label: string }> = [
  { value: "15m", label: "15m" },
  { value: "1h", label: "1h" },
  { value: "4h", label: "4h" },
  { value: "1d", label: "1D" },
];

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

function aggregateOrderbookSide(
  rows: Array<OrderbookIntradayResp["items"][number]["swap"]>
): OrderbookIntradayResp["items"][number]["swap"] {
  const totalSample = rows.reduce((sum, row) => sum + (row.sampleCount || 0), 0);
  const weightedAvg = (getter: (row: OrderbookIntradayResp["items"][number]["swap"]) => number | null): number | null => {
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

function aggregateOrderbookItems(items: OrderbookIntradayResp["items"], window: OrderbookWindow): OrderbookIntradayResp["items"] {
  const maxPoints = ORDERBOOK_WINDOW_POINTS[window];
  if (!items.length) return [];
  if (window === "1m") return items.slice(-maxPoints);

  const bucketMs = orderbookWindowMs(window);
  const groups = new Map<number, OrderbookIntradayResp["items"]>();
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

type FundSnapshotResp = {
  symbol: string;
  timezone: string;
  timeMode?: "utc" | "local";
  tzOffsetMin?: number;
  source: string;
  items: Array<{
    key: number;
    labelTsMs: number;
    swapValue: number | null;
    spotValue: number | null;
  }>;
};

type FundSnapshotHealthResp = {
  symbol: string;
  timezone: string;
  timeMode?: "utc" | "local";
  tzOffsetMin?: number;
  healthy: boolean;
  reason: string;
  latest1mByMarket: {
    spot: number | null;
    swap: number | null;
  };
  freshWindowSec: number;
  checkWindowHours: number;
  h1m1Consistency: {
    compared: number;
    mismatch: number;
  };
  lastRepairAtMs: number | null;
  repairCooldownMs: number;
  repairCooldownRemainingMs: number;
  canTriggerRepair: boolean;
};

type RecentDailyResp = {
  market: Market;
  symbol: string;
  items: Array<{
    bucketStartMs: number;
    open: number;
    high: number;
    low: number;
    close: number;
  }>;
};

type OiResp = {
  symbol: string;
  hours: number;
  items: Array<{
    bucketStartMs: number;
    openInterestUsd: number;
    openInterest: number;
    closePrice?: number | null;
  }>;
};

type OiDailyResp = {
  symbol: string;
  days: number;
  items: Array<{
    bucketStartMs: number;
    openInterestUsd: number;
    openInterest: number;
    closePrice?: number | null;
  }>;
};

type LsrResp = {
  symbol: string;
  period: string;
  items: Array<{
    bucketStartMs: number;
    accountRatio: number;
    topAccountRatio: number;
    topPositionRatio: number;
  }>;
};

type SrResp = {
  symbol: string;
  market: Market;
  timeframe?: string;
  supports: Array<{ low: number; high: number }>;
  resistances: Array<{ low: number; high: number }>;
};

const API_BASE = (import.meta as any).env?.VITE_API_BASE || "";

async function getJson<T>(path: string): Promise<T> {
  const r = await fetch(`${API_BASE}${path}`);
  if (!r.ok) throw new Error(`HTTP ${r.status}`);
  return (await r.json()) as T;
}

function keepLastIfSameSymbol<T>(next: T | null, prev: T | null, symbol: string): T | null {
  if (next != null) return next;
  if (prev == null || typeof prev !== "object") return null;
  const prevSymbol = (prev as any).symbol || (prev as any).basic?.symbol;
  if (typeof prevSymbol === "string" && prevSymbol.toUpperCase() !== symbol.toUpperCase()) {
    return null;
  }
  return prev;
}

const usdCompact = new Intl.NumberFormat("en-US", {
  notation: "compact",
  maximumFractionDigits: 2,
});

function formatPrice(v: number | null) {
  if (!Number.isFinite(v as number)) return "-";
  const value = v as number;
  const abs = Math.abs(value);
  if (abs === 0) return "0";
  if (abs < 0.01) return value.toFixed(8);
  if (abs < 1) return value.toFixed(6);
  if (abs < 1000) return value.toFixed(2);
  return value.toFixed(0);
}

function formatPriceFixed(v: number | null, digits = 1) {
  if (!Number.isFinite(v as number)) return "-";
  return (v as number).toFixed(digits);
}

function formatPct(v: number | null, digits = 2) {
  if (v == null || !Number.isFinite(v)) return "-";
  const s = (v * 100).toFixed(digits);
  return `${v >= 0 ? "+" : ""}${s}%`;
}

function formatSignedPct(v: number | null, digits = 1) {
  if (v == null || !Number.isFinite(v)) return "-";
  const s = v.toFixed(digits);
  return `${v >= 0 ? "+" : ""}${s}%`;
}

function formatDurationMin(mins: number | null | undefined) {
  if (mins == null || !Number.isFinite(mins)) return "-";
  const total = Math.max(0, Math.floor(mins));
  const h = Math.floor(total / 60);
  const m = total % 60;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}

function formatBps(v: number | null, digits = 2) {
  if (v == null || !Number.isFinite(v)) return "-";
  return `${v >= 0 ? "+" : ""}${v.toFixed(digits)}bps`;
}

function formatCnCompact(v: number | null, digits = 2) {
  if (v == null || !Number.isFinite(v)) return "-";
  const abs = Math.abs(v);
  if (abs >= 1e8) return `${(v / 1e8).toFixed(digits)}亿`;
  if (abs >= 1e4) return `${(v / 1e4).toFixed(digits)}万`;
  if (abs >= 1) return v.toFixed(2);
  return v.toFixed(4);
}

function formatWhaleSource(markets?: Array<"spot" | "swap">) {
  if (!markets || markets.length === 0) return "数据源未知";
  const uniq = Array.from(new Set(markets));
  const hasSpot = uniq.includes("spot");
  const hasSwap = uniq.includes("swap");
  if (hasSpot && hasSwap) return "双源 spot+swap";
  if (hasSpot) return "spot";
  if (hasSwap) return "swap";
  return "数据源未知";
}

function formatRatio(v: number | null, digits = 2) {
  if (v == null || !Number.isFinite(v)) return "-";
  return (v as number).toFixed(digits);
}

function getTimeParts(ts: number, _mode: TimeDisplayMode) {
  const d = new Date(ts);
  const month = d.getMonth() + 1;
  const day = d.getDate();
  const hour = d.getHours();
  const minute = d.getMinutes();
  return { month, day, hour, minute };
}

function formatHour(ts: number, mode: TimeDisplayMode) {
  const { hour, minute } = getTimeParts(ts, mode);
  return `${`${hour}`.padStart(2, "0")}:${`${minute}`.padStart(2, "0")}`;
}

function formatDay(ts: number, mode: TimeDisplayMode) {
  const { month, day } = getTimeParts(ts, mode);
  return `${`${month}`.padStart(2, "0")}/${`${day}`.padStart(2, "0")}`;
}

function formatDayNum(ts: number, mode: TimeDisplayMode) {
  const { day } = getTimeParts(ts, mode);
  return `${day}`;
}

function formatMonthDay(ts: number, mode: TimeDisplayMode) {
  const { month, day } = getTimeParts(ts, mode);
  return `${`${month}`.padStart(2, "0")}${`${day}`.padStart(2, "0")}`;
}

function formatMonthDaySlash(ts: number, mode: TimeDisplayMode) {
  const { month, day } = getTimeParts(ts, mode);
  return `${`${month}`.padStart(2, "0")}/${`${day}`.padStart(2, "0")}`;
}

function formatMonthDayTime(ts: number, mode: TimeDisplayMode) {
  const { month, day, hour, minute } = getTimeParts(ts, mode);
  return `${`${month}`.padStart(2, "0")}/${`${day}`.padStart(2, "0")} ${`${hour}`.padStart(2, "0")}:${`${minute}`.padStart(2, "0")}`;
}

function formatTzTime(ts: number, timeZone: string) {
  return new Date(ts).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false, timeZone });
}

function formatTs(ts: number | null | undefined) {
  if (!ts || !Number.isFinite(ts)) return "-";
  const d = new Date(ts);
  const y = d.getFullYear();
  const m = `${d.getMonth() + 1}`.padStart(2, "0");
  const day = `${d.getDate()}`.padStart(2, "0");
  const hh = `${d.getHours()}`.padStart(2, "0");
  const mm = `${d.getMinutes()}`.padStart(2, "0");
  const ss = `${d.getSeconds()}`.padStart(2, "0");
  return `${y}-${m}-${day} ${hh}:${mm}:${ss}`;
}

function isDayStartByMode(ts: number, _mode: TimeDisplayMode) {
  const d = new Date(ts);
  return d.getHours() === 0 && d.getMinutes() === 0;
}

export default function CoinPage() {
  const { Title, Text } = Typography;
  const { symbol } = useParams();
  const sym = (symbol || "").toUpperCase();
  const [market, setMarket] = useState<Market>("swap");
  const [timeDisplayMode, setTimeDisplayMode] = useState<TimeDisplayMode>("local");
  const [activeTab, setActiveTab] = useState<TabKey>("hourly");
  const [hourlyTab, setHourlyTab] = useState<HourlySubTab>("snapshot");
  const [basic, setBasic] = useState<BasicResp | null>(null);
  const [hourly, setHourly] = useState<FundSnapshotResp | null>(null);
  const [hourlyHealth, setHourlyHealth] = useState<FundSnapshotHealthResp | null>(null);
  const [hourlyFlow, setHourlyFlow] = useState<FlowResp | null>(null);
  const [daily, setDaily] = useState<FlowResp | null>(null);
  const [dailyRecent, setDailyRecent] = useState<FlowResp | null>(null);
  const [recentDaily, setRecentDaily] = useState<RecentDailyResp | null>(null);
  const [oiHourly, setOiHourly] = useState<OiResp | null>(null);
  const [oiDaily, setOiDaily] = useState<OiDailyResp | null>(null);
  const [lsrHourly, setLsrHourly] = useState<LsrResp | null>(null);
  const [srShort, setSrShort] = useState<SrResp | null>(null);
  const [srShort15, setSrShort15] = useState<SrResp | null>(null);
  const [orderbook, setOrderbook] = useState<OrderbookIntradayResp | null>(null);
  const [orderbookWindow, setOrderbookWindow] = useState<OrderbookWindow>("5m");
  const [quantBucket, setQuantBucket] = useState<QuantBucket>("1h");
  const [quantCvdMode, setQuantCvdMode] = useState<CvdMode>("visible");
  const [quantVisibleAnchorTs, setQuantVisibleAnchorTs] = useState<number | null>(null);
  const [quantData, setQuantData] = useState<QuantDashboardResp | null>(null);
  const [whaleRadar, setWhaleRadar] = useState<WhaleRadarResp | null>(null);
  const [loading, setLoading] = useState(false);
  const [nowTick, setNowTick] = useState(Date.now());
  const hideSpotSeries = market === "spot" && !!basic?.marketFallback;
  const whaleSourceText = formatWhaleSource(whaleRadar?.sourceMarkets);

  const load = async () => {
    if (!sym) return;
    setLoading(true);
    const safeGet = async <T,>(url: string): Promise<T | null> => {
      try {
        return await getJson<T>(url);
      } catch {
        return null;
      }
    };
    try {
      const tzOffsetMin = new Date().getTimezoneOffset();
      const [b, h, hh, hf, d, dr, r, oi, oid, lsr, sr, sr15, ob, qd, wr] = await Promise.all([
        safeGet<BasicResp>(
          `/api/coin/detail/basic?market=${market}&symbol=${sym}&timeMode=${timeDisplayMode}&tzOffsetMin=${tzOffsetMin}`
        ),
        safeGet<FundSnapshotResp>(
          `/api/coin/detail/fund/snapshots?symbol=${sym}&timeMode=${timeDisplayMode}&tzOffsetMin=${tzOffsetMin}`
        ),
        safeGet<FundSnapshotHealthResp>(
          `/api/coin/detail/fund/snapshot-health?symbol=${sym}&timeMode=${timeDisplayMode}&tzOffsetMin=${tzOffsetMin}`
        ),
        safeGet<FlowResp>(`/api/coin/detail/flows/hourly?symbol=${sym}&hours=24`),
        safeGet<FlowResp>(`/api/coin/detail/flows/daily?symbol=${sym}&days=30`),
        safeGet<FlowResp>(`/api/coin/detail/flows/daily?symbol=${sym}&days=30&includeToday=1`),
        safeGet<RecentDailyResp>(`/api/coin/detail/recent/daily?market=${market}&symbol=${sym}&days=20&includeToday=1`),
        safeGet<OiResp>(`/api/coin/detail/oi/hourly?market=${market}&symbol=${sym}&hours=24`),
        safeGet<OiDailyResp>(`/api/coin/detail/oi/daily?market=${market}&symbol=${sym}&days=30`),
        safeGet<LsrResp>(`/api/coin/detail/lsr/hourly?symbol=${sym}&limit=6`),
        safeGet<SrResp>(`/api/coin/detail/sr/short?market=${market}&symbol=${sym}&days=5&limit=5&timeframe=1h`),
        safeGet<SrResp>(`/api/coin/detail/sr/short?market=${market}&symbol=${sym}&days=5&limit=5&timeframe=15m`),
        safeGet<OrderbookIntradayResp>(`/api/coin/detail/orderbook/intraday?symbol=${sym}&bucket=1m&limit=240`),
        safeGet<QuantDashboardResp>(
          `/api/coin/detail/quant-dashboard?symbol=${sym}&market=${market}&bucket=${quantBucket}&limit=240`
        ),
        safeGet<WhaleRadarResp>(
          `/api/coin/detail/orderbook/whale-radar?symbol=${sym}&lookbackMinutes=240&topK=20`
        ),
      ]);
      setBasic((prev) => keepLastIfSameSymbol(b, prev, sym));
      setHourly((prev) => keepLastIfSameSymbol(h, prev, sym));
      setHourlyHealth((prev) => keepLastIfSameSymbol(hh, prev, sym));
      setHourlyFlow((prev) => keepLastIfSameSymbol(hf, prev, sym));
      setDaily((prev) => keepLastIfSameSymbol(d, prev, sym));
      setDailyRecent((prev) => keepLastIfSameSymbol(dr, prev, sym));
      setRecentDaily((prev) => keepLastIfSameSymbol(r, prev, sym));
      setOiHourly((prev) => keepLastIfSameSymbol(oi, prev, sym));
      setOiDaily((prev) => keepLastIfSameSymbol(oid, prev, sym));
      setLsrHourly((prev) => keepLastIfSameSymbol(lsr, prev, sym));
      setSrShort((prev) => keepLastIfSameSymbol(sr, prev, sym));
      setSrShort15((prev) => keepLastIfSameSymbol(sr15, prev, sym));
      setOrderbook((prev) => keepLastIfSameSymbol(ob, prev, sym));
      setQuantData((prev) => keepLastIfSameSymbol(qd, prev, sym));
      setWhaleRadar((prev) => keepLastIfSameSymbol(wr, prev, sym));
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    load();
  }, [sym, market, timeDisplayMode, quantBucket]);

  useEffect(() => {
    const id = setInterval(() => {
      setNowTick(Date.now());
      if (document.visibilityState === "visible") {
        void load();
      }
    }, 60 * 1000);
    return () => clearInterval(id);
  }, [sym, market, timeDisplayMode, quantBucket]);

  const hourlyOption = useMemo(() => {
    const items = hourly?.items || [];
    const xs = items.map((x) => formatHour(x.labelTsMs, timeDisplayMode));
    const spot = items.map((x) => (x.spotValue == null ? null : x.spotValue));
    const swap = items.map((x) => (x.swapValue == null ? null : x.swapValue));
    const series: any[] = [
      {
        name: "合约",
        type: "line",
        data: swap,
        symbol: "circle",
        symbolSize: 6,
        lineStyle: { width: 2, color: "#3b82f6" },
        itemStyle: { color: "#3b82f6" },
        label: {
          show: true,
          formatter: (p: any) => (p.value == null ? "" : formatCnCompact(p.value)),
          color: "#111827",
          fontSize: 11,
        },
      },
    ];
    if (!hideSpotSeries) {
      series.push({
        name: "现货",
        type: "line",
        data: spot,
        symbol: "circle",
        symbolSize: 6,
        lineStyle: { width: 2, color: "#ff7a45" },
        itemStyle: { color: "#ff7a45" },
        label: {
          show: true,
          formatter: (p: any) => (p.value == null ? "" : formatCnCompact(p.value)),
          color: "#111827",
          fontSize: 11,
        },
      });
    }
    return {
      tooltip: {
        trigger: "axis",
        valueFormatter: (v: number) => (v == null ? "-" : formatCnCompact(v)),
      },
      grid: { left: 50, right: 20, top: 24, bottom: 32 },
      xAxis: {
        type: "category",
        data: xs,
        boundaryGap: false,
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
  }, [hourly, timeDisplayMode, hideSpotSeries]);

  const hourlyFlowOption = useMemo(() => {
    const items = hourlyFlow?.items || [];
    const xs = items.map((x) => formatHour(x.bucketStartMs, timeDisplayMode));
    const spot = items.map((x) => x.spotNetNotional || 0);
    const swap = items.map((x) => x.swapNetNotional || 0);
    const buildBarData = (vals: number[]) =>
      vals.map((v) => ({
        value: v,
        label: {
          show: true,
          position: v >= 0 ? "top" : "bottom",
          offset: [0, v >= 0 ? -2 : 2],
        },
      }));
    const swapData = buildBarData(swap);
    const spotData = buildBarData(spot);
    const series: any[] = [
      {
        name: "合约",
        type: "bar",
        data: swapData,
        barWidth: 8,
        itemStyle: { color: "#3b82f6" },
        labelLayout: { hideOverlap: true },
        label: {
          show: true,
          formatter: (p: any) => formatCnCompact(p.value),
          color: "#3b82f6",
          fontSize: 10,
          distance: 4,
        },
      },
    ];
    if (!hideSpotSeries) {
      series.push({
        name: "现货",
        type: "bar",
        data: spotData,
        barWidth: 8,
        itemStyle: { color: "#ff7a45" },
        labelLayout: { hideOverlap: true },
        label: {
          show: true,
          formatter: (p: any) => formatCnCompact(p.value),
          color: "#ff7a45",
          fontSize: 10,
          distance: 4,
        },
      });
    }
    return {
      tooltip: {
        trigger: "axis",
        valueFormatter: (v: number) => (v == null ? "-" : formatCnCompact(v)),
      },
      grid: { left: 50, right: 20, top: 24, bottom: 32 },
      xAxis: {
        type: "category",
        data: xs,
        boundaryGap: true,
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
  }, [hourlyFlow, timeDisplayMode, hideSpotSeries]);

  const dailyAccOption = useMemo(() => {
    const items = dailyRecent?.items || daily?.items || [];
    const count = items.length;
    const xs = items.map((_, idx) => String(count - idx));
    const spotNet = items.map((x) => x.spotNetNotional || 0);
    const swapNet = items.map((x) => x.swapNetNotional || 0);
    const buildReverseCum = (vals: number[]) => {
      const out = new Array<number>(vals.length);
      let acc = 0;
      for (let i = vals.length - 1; i >= 0; i -= 1) {
        acc += vals[i] || 0;
        out[i] = acc;
      }
      return out;
    };
    const spot = buildReverseCum(spotNet);
    const swap = buildReverseCum(swapNet);
    const minVal = hideSpotSeries ? Math.min(...swap) : Math.min(...spot, ...swap);
    const maxVal = hideSpotSeries ? Math.max(...swap) : Math.max(...spot, ...swap);
    const pad = Math.max(Math.abs(maxVal - minVal) * 0.08, Math.abs(maxVal) * 0.02, 1);
    const series: any[] = [
      {
        name: "合约",
        type: "line",
        data: swap,
        symbol: "circle",
        symbolSize: 5,
        lineStyle: { width: 2, color: "#3b82f6" },
        itemStyle: { color: "#3b82f6" },
        label: {
          show: true,
          formatter: (p: any) => formatCnCompact(p.value),
          color: "#3b82f6",
          fontSize: 11,
          rotate: 25,
        },
      },
    ];
    if (!hideSpotSeries) {
      series.push({
        name: "现货",
        type: "line",
        data: spot,
        symbol: "circle",
        symbolSize: 5,
        lineStyle: { width: 2, color: "#ff7a45" },
        itemStyle: { color: "#ff7a45" },
        label: {
          show: true,
          formatter: (p: any) => formatCnCompact(p.value),
          color: "#ff7a45",
          fontSize: 11,
          rotate: 25,
        },
      });
    }
    return {
      tooltip: {
        trigger: "axis",
        formatter: (params: any) => {
          const idx = params?.[0]?.dataIndex ?? 0;
          const ts = items[idx]?.bucketStartMs;
          const day = ts ? formatMonthDay(ts, timeDisplayMode) : "-";
          const swapDay = formatCnCompact(swapNet[idx] ?? null);
          const swapCum = formatCnCompact(swap[idx] ?? null);
          const spotDay = formatCnCompact(spotNet[idx] ?? null);
          const spotCum = formatCnCompact(spot[idx] ?? null);
          if (hideSpotSeries) {
            return `${day} 净资金<br/>合约当日 ${swapDay}<br/>合约累计 ${swapCum}`;
          }
          return `${day} 净资金<br/>合约当日 ${swapDay}<br/>合约累计 ${swapCum}<br/>现货当日 ${spotDay}<br/>现货累计 ${spotCum}`;
        },
      },
      grid: { left: 50, right: 20, top: 24, bottom: 18 },
      xAxis: {
        type: "category",
        data: xs,
        boundaryGap: false,
        axisLine: { lineStyle: { color: "#e2e8f0" } },
        axisTick: { show: false },
        axisLabel: { color: "#94a3b8" },
      },
      yAxis: {
        type: "value",
        min: minVal - pad,
        max: maxVal + pad,
        axisLabel: { show: false },
        splitLine: { lineStyle: { color: "rgba(148,163,184,0.25)", type: "dashed" } },
      },
      series,
    };
  }, [daily, dailyRecent, timeDisplayMode, hideSpotSeries]);

  const oiDailyLineOption = useMemo(() => {
    const items = oiDaily?.items || [];
    const xs = items.map((x) => formatMonthDay(x.bucketStartMs, timeDisplayMode));
    const line = items.map((x) => (Number.isFinite(x.openInterest) ? x.openInterest : null));
    return {
      tooltip: {
        trigger: "axis",
        valueFormatter: (v: number) => formatCnCompact(v),
      },
      grid: { left: 50, right: 20, top: 24, bottom: 16 },
      xAxis: {
        type: "category",
        data: xs,
        boundaryGap: true,
        axisLine: { lineStyle: { color: "#e2e8f0" } },
        axisTick: { show: false },
        axisLabel: { show: false },
      },
      yAxis: [
        {
          type: "value",
          axisLabel: { show: false },
          splitLine: { lineStyle: { color: "rgba(148,163,184,0.25)", type: "dashed" } },
        },
      ],
      series: [
        {
          name: "持仓量",
          type: "line",
          data: line,
          symbol: "circle",
          symbolSize: 5,
          lineStyle: { width: 2, color: "#2dd4bf" },
          itemStyle: { color: "#2dd4bf" },
          label: {
            show: true,
            formatter: (p: any) => formatCnCompact(p.value),
            color: "#0f172a",
            fontSize: 11,
          },
        },
      ],
    };
  }, [oiDaily, timeDisplayMode]);

  const oiDailyPriceOption = useMemo(() => {
    const items = oiDaily?.items || [];
    const xs = items.map((x) => formatMonthDay(x.bucketStartMs, timeDisplayMode));
    const bars = items.map((x) => (Number.isFinite(x.closePrice as number) ? x.closePrice : null));
    return {
      tooltip: {
        trigger: "axis",
        valueFormatter: (v: number) => (v == null ? "-" : `₮${formatPriceFixed(v, 1)}`),
      },
      grid: { left: 50, right: 20, top: 10, bottom: 32 },
      xAxis: {
        type: "category",
        data: xs,
        boundaryGap: true,
        axisLine: { lineStyle: { color: "#e2e8f0" } },
        axisTick: { show: false },
        axisLabel: { color: "#94a3b8", rotate: 35 },
      },
      yAxis: [
        {
          type: "value",
          axisLabel: { show: false },
          splitLine: { show: false },
        },
      ],
      series: [
        {
          name: "收盘价",
          type: "bar",
          data: bars,
          barWidth: 10,
          itemStyle: { color: "#cbd5e1" },
          label: {
            show: true,
            formatter: (p: any) => `₮${formatPriceFixed(p.value, 1)}`,
            color: "#94a3b8",
            fontSize: 10,
          },
        },
      ],
    };
  }, [oiDaily, timeDisplayMode]);

  const oiLineOption = useMemo(() => {
    const items = oiHourly?.items || [];
    const xs = items.map((x) => formatHour(x.bucketStartMs, timeDisplayMode));
    const line = items.map((x) => (Number.isFinite(x.openInterest) ? x.openInterest : null));
    return {
      tooltip: {
        trigger: "axis",
        valueFormatter: (v: number) => formatCnCompact(v),
      },
      grid: { left: 50, right: 20, top: 24, bottom: 16 },
      xAxis: {
        type: "category",
        data: xs,
        boundaryGap: true,
        axisLine: { lineStyle: { color: "#e2e8f0" } },
        axisTick: { show: false },
        axisLabel: { show: false },
      },
      yAxis: [
        {
          type: "value",
          axisLabel: { show: false },
          splitLine: { lineStyle: { color: "rgba(148,163,184,0.25)", type: "dashed" } },
        },
        {
          type: "value",
          axisLabel: { show: false },
          splitLine: { show: false },
        },
      ],
      series: [
        {
          name: "持仓量",
          type: "line",
          data: line,
          symbol: "circle",
          symbolSize: 5,
          lineStyle: { width: 2, color: "#2dd4bf" },
          itemStyle: { color: "#2dd4bf" },
          label: {
            show: true,
            formatter: (p: any) => formatCnCompact(p.value),
            color: "#0f172a",
            fontSize: 11,
          },
        },
      ],
    };
  }, [oiHourly, timeDisplayMode]);

  const oiPriceOption = useMemo(() => {
    const items = oiHourly?.items || [];
    const xs = items.map((x) => formatHour(x.bucketStartMs, timeDisplayMode));
    const bars = items.map((x) => (Number.isFinite(x.closePrice as number) ? x.closePrice : null));
    return {
      tooltip: {
        trigger: "axis",
        valueFormatter: (v: number) => (v == null ? "-" : `₮${formatPriceFixed(v, 1)}`),
      },
      grid: { left: 50, right: 20, top: 10, bottom: 32 },
      xAxis: {
        type: "category",
        data: xs,
        boundaryGap: true,
        axisLine: { lineStyle: { color: "#e2e8f0" } },
        axisTick: { show: false },
        axisLabel: { color: "#94a3b8", rotate: 35 },
      },
      yAxis: [
        {
          type: "value",
          axisLabel: { show: false },
          splitLine: { show: false },
        },
      ],
      series: [
        {
          name: "收盘价",
          type: "bar",
          data: bars,
          barWidth: 10,
          itemStyle: { color: "#cbd5e1" },
          label: {
            show: true,
            formatter: (p: any) => `₮${formatPriceFixed(p.value, 1)}`,
            color: "#94a3b8",
            fontSize: 10,
          },
        },
      ],
    };
  }, [oiHourly, timeDisplayMode]);

  const recentFundOption = useMemo(() => {
    const items = dailyRecent?.items || daily?.items || [];
    const labelThreshold = 2e8;
    const xs = items.map((x) => formatMonthDaySlash(x.bucketStartMs, timeDisplayMode));
    const swap = items.map((x) => x.swapNetNotional || 0);
    const spot = items.map((x) => x.spotNetNotional || 0);
    const buildBarData = (vals: number[]) =>
      vals.map((v) => ({
        value: v,
        label: {
          show: Math.abs(v) >= labelThreshold,
          position: v >= 0 ? "top" : "bottom",
          offset: [0, v >= 0 ? -2 : 2],
        },
      }));
    const swapData = buildBarData(swap);
    const spotData = buildBarData(spot);
    const series: any[] = [
      {
        name: "合约",
        type: "bar",
        data: swapData,
        barWidth: 8,
        itemStyle: { color: "#3b82f6" },
        labelLayout: { hideOverlap: true },
        label: {
          show: true,
          formatter: (p: any) => (Math.abs(p.value || 0) >= labelThreshold ? formatCnCompact(p.value) : ""),
          color: "#3b82f6",
          fontSize: 10,
          distance: 4,
        },
      },
    ];
    if (!hideSpotSeries) {
      series.push({
        name: "现货",
        type: "bar",
        data: spotData,
        barWidth: 8,
        itemStyle: { color: "#ff7a45" },
        labelLayout: { hideOverlap: true },
        label: {
          show: true,
          formatter: (p: any) => (Math.abs(p.value || 0) >= labelThreshold ? formatCnCompact(p.value) : ""),
          color: "#ff7a45",
          fontSize: 10,
          distance: 4,
        },
      });
    }
    return {
      tooltip: {
        trigger: "axis",
        valueFormatter: (v: number) => formatCnCompact(v),
      },
      grid: { left: 50, right: 20, top: 24, bottom: 24 },
      xAxis: {
        type: "category",
        data: xs,
        axisLine: { lineStyle: { color: "#e2e8f0" } },
        axisTick: { show: false },
        axisLabel: { color: "#94a3b8" },
      },
      yAxis: {
        type: "value",
        axisLabel: { show: false },
        splitLine: { lineStyle: { color: "rgba(148,163,184,0.25)", type: "dashed" } },
      },
      series,
    };
  }, [daily, dailyRecent, timeDisplayMode, hideSpotSeries]);

  const recentHighLowOption = useMemo(() => {
    const items = recentDaily?.items || [];
    const xs = items.map((x) => formatMonthDaySlash(x.bucketStartMs, timeDisplayMode));
    const highs = items.map((x) => x.high || 0);
    const lows = items.map((x) => x.low || 0);
    const percents = items.map((x) => {
      if (!x.low || !x.high) return 0;
      return ((x.high - x.low) / x.low) * 100;
    });
    return {
      tooltip: {
        trigger: "axis",
        formatter: (params: any) => {
          const idx = params?.[0]?.dataIndex ?? 0;
          const day = xs[idx] || "-";
          const high = formatPriceFixed(highs[idx], 1);
          const low = formatPriceFixed(lows[idx], 1);
          const pct = percents[idx]?.toFixed(1);
          return `${day}<br/>高 ${high}<br/>低 ${low}<br/>振幅 ${pct}%`;
        },
      },
      grid: { left: 50, right: 20, top: 24, bottom: 40 },
      xAxis: [
        {
          type: "category",
          data: xs,
          axisLine: { lineStyle: { color: "#e2e8f0" } },
          axisTick: { show: false },
          axisLabel: { color: "#94a3b8" },
        },
        {
          type: "category",
          data: xs,
          position: "bottom",
          offset: 18,
          axisLine: { show: false },
          axisTick: { show: false },
          axisLabel: {
            formatter: (_: string, idx: number) => {
              const v = percents[idx] ?? 0;
              const s = `${v.toFixed(1)}%`;
              return v >= 0 ? `{pos|${s}}` : `{neg|${s}}`;
            },
            rich: {
              pos: { color: "#16a34a", fontSize: 11 },
              neg: { color: "#ef4444", fontSize: 11 },
            },
          },
        },
      ],
      yAxis: {
        type: "value",
        axisLabel: { show: false },
        splitLine: { lineStyle: { color: "rgba(148,163,184,0.25)", type: "dashed" } },
      },
      series: [
        {
          name: "高点",
          type: "line",
          data: highs,
          symbol: "circle",
          symbolSize: 4,
          lineStyle: { width: 2, color: "#22c55e" },
          itemStyle: { color: "#22c55e" },
          label: {
            show: true,
            formatter: (p: any) => formatPriceFixed(p.value, 1),
            position: "top",
            color: "#22c55e",
            fontSize: 10,
          },
        },
        {
          name: "低点",
          type: "line",
          data: lows,
          symbol: "circle",
          symbolSize: 4,
          lineStyle: { width: 2, color: "#ef4444" },
          itemStyle: { color: "#ef4444" },
          label: {
            show: true,
            formatter: (p: any) => formatPriceFixed(p.value, 1),
            position: "bottom",
            color: "#ef4444",
            fontSize: 10,
          },
        },
      ],
    };
  }, [recentDaily, timeDisplayMode]);

  const orderbookOption = useMemo(() => {
    const rows = aggregateOrderbookItems(orderbook?.items || [], orderbookWindow);
    const preferSwap = !!orderbook?.swapAvailable;
    const showSpot = !hideSpotSeries && !!orderbook?.spotAvailable;
    const selected = rows.map((x) => (preferSwap ? x.swap : x.spot));
    const xs = rows.map((x) => formatHour(x.bucketStartMs, timeDisplayMode));
    const spread = selected.map((x) => x.spreadBps ?? null);
    const replenish = selected.map((x) => x.replenishScore ?? null);
    const imbalance = selected.map((x) => (x.depthImbalanceL20 == null ? null : x.depthImbalanceL20 * 100));
    const aggrBuy = selected.map((x) => (x.aggrBuyRatio == null ? null : x.aggrBuyRatio * 100));

    const series: any[] = [
      {
        name: "Spread",
        type: "line",
        data: spread,
        symbol: "none",
        lineStyle: { width: 2, color: "#64748b" },
        xAxisIndex: 0,
        yAxisIndex: 0,
      },
      {
        name: "回补分",
        type: "line",
        data: replenish,
        symbol: "none",
        lineStyle: { width: 2, color: "#16a34a" },
        xAxisIndex: 0,
        yAxisIndex: 0,
      },
      {
        name: "深度不平衡",
        type: "bar",
        data: imbalance,
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
        xAxisIndex: 1,
        yAxisIndex: 1,
      },
      {
        name: "主动买占比",
        type: "line",
        data: aggrBuy,
        symbol: "none",
        lineStyle: { width: 2, color: "#f59e0b" },
        xAxisIndex: 1,
        yAxisIndex: 1,
      },
    ];

    if (showSpot) {
      const spotSpread = rows.map((x) => x.spot.spreadBps ?? null);
      const spotImb = rows.map((x) => (x.spot.depthImbalanceL20 == null ? null : x.spot.depthImbalanceL20 * 100));
      series.push({
        name: "现货Spread",
        type: "line",
        data: spotSpread,
        symbol: "none",
        lineStyle: { width: 1, type: "dashed", color: "#ff7a45" },
        xAxisIndex: 0,
        yAxisIndex: 0,
      });
      series.push({
        name: "现货深度不平衡",
        type: "line",
        data: spotImb,
        symbol: "none",
        lineStyle: { width: 1, type: "dashed", color: "#fb923c" },
        xAxisIndex: 1,
        yAxisIndex: 1,
      });
    }

    return {
      tooltip: { trigger: "axis" },
      legend: {
        top: 0,
        right: 0,
        textStyle: { fontSize: 11, color: "#64748b" },
      },
      grid: [
        { left: 50, right: 18, top: 26, height: 90 },
        { left: 50, right: 18, top: 146, height: 90 },
      ],
      xAxis: [
        {
          type: "category",
          data: xs,
          axisLine: { lineStyle: { color: "#e2e8f0" } },
          axisTick: { show: false },
          axisLabel: { show: false },
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
          axisLabel: { formatter: (v: number) => formatBps(v), color: "#94a3b8" },
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
      series,
    };
  }, [orderbook, orderbookWindow, hideSpotSeries, timeDisplayMode]);

  const latestOrderbook = useMemo(() => {
    const rows = aggregateOrderbookItems(orderbook?.items || [], orderbookWindow);
    if (!rows.length) return null;
    const preferSwap = !!orderbook.swapAvailable;
    const pick = (item: OrderbookIntradayResp["items"][number]) => (preferSwap ? item.swap : item.spot);
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

  const oiChanges = useMemo(() => {
    const items = oiHourly?.items || [];
    if (items.length < 2) return [] as number[];
    const baseItem =
      [...items].reverse().find((x) => {
        return isDayStartByMode(x.bucketStartMs, timeDisplayMode);
      }) || items[0];
    const base = baseItem?.openInterest;
    if (!Number.isFinite(base) || !base) return [] as number[];
    const tail = items.slice(-6);
    return tail.map((x) => ((x.openInterest - base) / base) * 100);
  }, [oiHourly, timeDisplayMode]);

  const lsrRows = useMemo(() => {
    return lsrHourly?.items || [];
  }, [lsrHourly]);

  const sr4hRows = useMemo(() => {
    const levels = basic?.srLevels || [];
    const last = basic?.basic.lastPrice ?? null;
    const hasLast = Number.isFinite(last as number);
    const rows = levels.map((l) => {
      const type = hasLast ? (l.levelPrice <= (last as number) ? "支撑" : "阻力") : "—";
      return {
        type,
        price: l.levelPrice,
        touches: l.touches,
        strength: l.strengthScore,
        lastTouchMs: l.lastTouchMs,
        dist: hasLast ? Math.abs(l.levelPrice - (last as number)) : Number.POSITIVE_INFINITY,
      };
    });
    if (!hasLast) {
      return rows.slice(0, 10);
    }
    const supports = rows
      .filter((r) => r.type === "支撑")
      .sort((a, b) => a.dist - b.dist)
      .slice(0, 5);
    const resistances = rows
      .filter((r) => r.type === "阻力")
      .sort((a, b) => a.dist - b.dist)
      .slice(0, 5);
    return [...supports, ...resistances];
  }, [basic]);

  const coverageHint = useMemo(() => {
    if (hourlyHealth?.reason === "no_1m_rows") {
      return "该币种暂未进入深度扫描覆盖，盘口/资金快照可能为空。";
    }
    if (orderbook && !orderbook.swapAvailable && !orderbook.spotAvailable) {
      return "当前无可用盘口样本，请稍后刷新或切换币种。";
    }
    return "";
  }, [hourlyHealth, orderbook]);

  if (!sym) {
    return (
      <div className="cm-page">
        <div className="cm-card" style={{ padding: 16 }}>
          <Title heading={5} style={{ margin: 0 }}>
            币种详情
          </Title>
          <Text className="cm-muted">请从主页面选择币种</Text>
        </div>
      </div>
    );
  }

  return (
    <div className="cm-page cm-coinPage">
      <div className="cm-card cm-coinHeader">
        <div className="cm-coinLeft">
          <span className="cm-coinSymbol">{sym}</span>
          <Select value={market} onChange={(v) => setMarket(v as Market)} size="mini" style={{ width: 120 }}>
            <Select.Option value="swap">合约</Select.Option>
            <Select.Option value="spot">现货</Select.Option>
          </Select>
        </div>
        <div className="cm-coinTabs">
          {(
            [
              { key: "basic", label: "基本信息" },
              { key: "hourly", label: "小时快照" },
              { key: "daily", label: "每日快照" },
              { key: "recent", label: "近日数据" },
              { key: "quant", label: "量化仪表盘" },
            ] as Array<{ key: TabKey; label: string }>
          ).map((t) => (
            <button
              key={t.key}
              type="button"
              className={`cm-tab ${activeTab === t.key ? "cm-tab--active" : ""}`}
              onClick={() => setActiveTab(t.key)}
            >
              {t.label}
            </button>
          ))}
        </div>
        <div className="cm-coinActions">
          <div className="cm-timeSwitch" role="group" aria-label="时间口径切换">
            <button
              type="button"
              className={`cm-timeSwitchBtn ${timeDisplayMode === "local" ? "cm-timeSwitchBtn--active" : ""}`}
              onClick={() => setTimeDisplayMode("local")}
            >
              本地
            </button>
            <button
              type="button"
              className={`cm-timeSwitchBtn ${timeDisplayMode === "utc" ? "cm-timeSwitchBtn--active" : ""}`}
              onClick={() => setTimeDisplayMode("utc")}
            >
              UTC
            </button>
          </div>
          <Button shape="circle" type="secondary" icon={<IconRefresh />} loading={loading} onClick={load} />
          <Link to="/" className="cm-coinClose" aria-label="关闭">
            <IconClose />
          </Link>
        </div>
      </div>

      {basic?.symbolStatus && basic.symbolStatus !== "TRADING" && (
        <div className="cm-section">
          <div className="cm-card" style={{ padding: 12 }}>
            <Text style={{ color: "#d46b08" }}>
              该交易对当前非 TRADING（{basic.symbolStatus}），暂无成交数据。
            </Text>
          </div>
        </div>
      )}

      {hideSpotSeries && (
        <div className="cm-section">
          <div className="cm-card" style={{ padding: 12 }}>
            <Text style={{ color: "#166534" }}>现货无该交易对，已自动回退为合约数据；页面已隐藏现货曲线。</Text>
          </div>
        </div>
      )}

      {(basic?.degraded || (basic?.warnings?.length || 0) > 0) && (
        <div className="cm-section">
          <div className="cm-card" style={{ padding: 12 }}>
            <Text style={{ color: "#b45309" }}>
              行情源本次请求不稳定，已自动降级返回可用数据。
              {basic?.warnings?.length ? `（${basic.warnings.join(" / ")}）` : ""}
            </Text>
          </div>
        </div>
      )}

      {coverageHint && (
        <div className="cm-section">
          <div className="cm-card" style={{ padding: 12 }}>
            <Text style={{ color: "#92400e" }}>{coverageHint}</Text>
          </div>
        </div>
      )}

      {activeTab === "basic" && (
        <div className="cm-section">
          <div className="cm-card" style={{ padding: 12 }}>
            <div className="cm-sectionHeader">
              <Title heading={6} style={{ margin: 0 }}>
                基本信息
              </Title>
              <Tag color="arcoblue">口径可追溯</Tag>
            </div>
            <div className="cm-basicLayout">
              <div className="cm-basicLeft">
                <div className="cm-infoGrid cm-infoGrid--basic">
                  {(basic?.basicKvp || []).length === 0 ? (
                    <Text className="cm-muted">暂无数据</Text>
                  ) : (
                    (basic?.basicKvp || []).map((item, idx) => (
                      <div key={`basic-${idx}`}>
                        <Text className="cm-muted">{item.label}</Text>
                        <div className="cm-number--mono">{item.value}</div>
                      </div>
                    ))
                  )}
                </div>
              </div>
              <div className="cm-basicRight">
                <div className="cm-basicBlock">
                  <div className="cm-basicBlockTitle">
                    <span>多空比</span>
                    <span className="cm-muted">人数 | 大户数 | 大户持仓</span>
                  </div>
                  <div className="cm-lsrTable">
                    <div className="cm-lsrHeader">
                      <span className="cm-muted">时间</span>
                      <span className="cm-muted">人数</span>
                      <span className="cm-muted">大户数</span>
                      <span className="cm-muted">大户持仓</span>
                    </div>
                    {lsrRows.length === 0 ? (
                      <Text className="cm-muted">暂无数据</Text>
                    ) : (
                      lsrRows.map((r) => (
                        <div key={r.bucketStartMs} className="cm-lsrRow">
                          <span className="cm-muted">{formatHour(r.bucketStartMs, timeDisplayMode)}</span>
                          <span className="cm-number--mono">{formatRatio(r.accountRatio)}</span>
                          <span className="cm-number--mono">{formatRatio(r.topAccountRatio)}</span>
                          <span className="cm-number--mono">{formatRatio(r.topPositionRatio)}</span>
                        </div>
                      ))
                    )}
                  </div>
                </div>
                <div className="cm-basicBlock">
                  <div className="cm-basicBlockTitle">
                    <span>4h 支撑/阻力</span>
                    <span className="cm-muted">(ZigZag)</span>
                  </div>
                  <div className="cm-srDetailTable">
                    <div className="cm-srDetailHeader">
                      <span className="cm-muted">类型</span>
                      <span className="cm-muted">价格</span>
                      <span className="cm-muted">触达</span>
                      <span className="cm-muted">强度</span>
                      <span className="cm-muted">最近</span>
                    </div>
                    {sr4hRows.length === 0 ? (
                      <Text className="cm-muted">暂无数据</Text>
                    ) : (
                      sr4hRows.map((r, i) => (
                        <div key={`sr4h-${i}`} className="cm-srDetailRow">
                          <span className={r.type === "支撑" ? "cm-number--pos" : "cm-number--neg"}>{r.type}</span>
                          <span className="cm-number--mono">{formatPrice(r.price)}</span>
                          <span className="cm-number--mono">{r.touches}</span>
                          <span className="cm-number--mono">{formatPriceFixed(r.strength, 2)}</span>
                          <span className="cm-muted">{formatMonthDayTime(r.lastTouchMs, timeDisplayMode)}</span>
                        </div>
                      ))
                    )}
                  </div>
                </div>
                <div className="cm-basicBlock">
                  <div className="cm-basicBlockTitle">
                    <span>短线支撑(1h)</span>
                    <span className="cm-muted">(根据近5日数据分析)</span>
                  </div>
                  <div className="cm-srTable">
                    {(srShort?.supports || []).map((r, i) => (
                      <div key={`s-${i}`} className="cm-srRow">
                        <span className="cm-muted">{i + 1}</span>
                        <span className="cm-number--mono">
                          {formatPriceFixed(r.low, 2)} - {formatPriceFixed(r.high, 2)}
                        </span>
                      </div>
                    ))}
                    {(!srShort || srShort.supports.length === 0) && <Text className="cm-muted">暂无数据</Text>}
                  </div>
                </div>
                <div className="cm-basicBlock">
                  <div className="cm-basicBlockTitle">
                    <span>短线阻力(1h)</span>
                    <span className="cm-muted">(根据近5日数据分析)</span>
                  </div>
                  <div className="cm-srTable">
                    {(srShort?.resistances || []).map((r, i) => (
                      <div key={`r-${i}`} className="cm-srRow">
                        <span className="cm-muted">{i + 1}</span>
                        <span className="cm-number--mono">
                          {formatPriceFixed(r.low, 2)} - {formatPriceFixed(r.high, 2)}
                        </span>
                      </div>
                    ))}
                    {(!srShort || srShort.resistances.length === 0) && <Text className="cm-muted">暂无数据</Text>}
                  </div>
                </div>
                <div className="cm-basicBlock">
                  <div className="cm-basicBlockTitle">
                    <span>短线支撑(15m)</span>
                    <span className="cm-muted">(根据近5日数据分析)</span>
                  </div>
                  <div className="cm-srTable">
                    {(srShort15?.supports || []).map((r, i) => (
                      <div key={`s15-${i}`} className="cm-srRow">
                        <span className="cm-muted">{i + 1}</span>
                        <span className="cm-number--mono">
                          {formatPriceFixed(r.low, 2)} - {formatPriceFixed(r.high, 2)}
                        </span>
                      </div>
                    ))}
                    {(!srShort15 || srShort15.supports.length === 0) && <Text className="cm-muted">暂无数据</Text>}
                  </div>
                </div>
                <div className="cm-basicBlock">
                  <div className="cm-basicBlockTitle">
                    <span>短线阻力(15m)</span>
                    <span className="cm-muted">(根据近5日数据分析)</span>
                  </div>
                  <div className="cm-srTable">
                    {(srShort15?.resistances || []).map((r, i) => (
                      <div key={`r15-${i}`} className="cm-srRow">
                        <span className="cm-muted">{i + 1}</span>
                        <span className="cm-number--mono">
                          {formatPriceFixed(r.low, 2)} - {formatPriceFixed(r.high, 2)}
                        </span>
                      </div>
                    ))}
                    {(!srShort15 || srShort15.resistances.length === 0) && <Text className="cm-muted">暂无数据</Text>}
                  </div>
                </div>
              </div>
            </div>
            <div style={{ marginTop: 8 }}>
              <Text className="cm-muted">数据源：{basic?.basic.source || "—"}</Text>
            </div>
          </div>
        </div>
      )}

      {activeTab === "hourly" && (
        <>
          <div className="cm-section">
            <div className="cm-card" style={{ padding: 8 }}>
              <div className="cm-subTabs">
                {(
                  [
                    { key: "snapshot", label: "累计资金快照" },
                    { key: "netflow", label: "每小时净流入" },
                  ] as Array<{ key: HourlySubTab; label: string }>
                ).map((t) => (
                  <button
                    key={t.key}
                    type="button"
                    className={`cm-subTab ${hourlyTab === t.key ? "cm-subTab--active" : ""}`}
                    onClick={() => setHourlyTab(t.key)}
                  >
                    {t.label}
                  </button>
                ))}
              </div>
            </div>
          </div>

          {hourlyTab === "snapshot" && (
            <>
              <div className="cm-section">
                <div className="cm-card" style={{ padding: 12 }}>
                  <div className="cm-snapshotHeader">
                    <div style={{ display: "flex", flexDirection: "column", gap: 2 }}>
                      <Title heading={6} style={{ margin: 0 }}>
                        今日每小时资金快照
                      </Title>
                      <Text className="cm-muted">
                        数据来源：trade_buckets（前端显示：浏览器本地时间；后端计算口径：{timeDisplayMode.toUpperCase()}）
                      </Text>
                      <Text className="cm-muted">悉尼时间：{formatTzTime(nowTick, "Australia/Sydney")}</Text>
                    </div>
                    <div className="cm-miniLegend">
                      <span className="cm-miniLegendItem">
                        <span className="cm-miniDot cm-miniDot--swap" /> 合约
                      </span>
                      {!hideSpotSeries && (
                        <span className="cm-miniLegendItem">
                          <span className="cm-miniDot cm-miniDot--spot" /> 现货
                        </span>
                      )}
                    </div>
                  </div>
                  <div className="cm-card" style={{ padding: 10, marginBottom: 8 }}>
                    <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: 6 }}>
                      <Text style={{ fontWeight: 600 }}>资金快照健康状态</Text>
                      <Tag color={hourlyHealth == null ? "gray" : hourlyHealth.healthy ? "green" : "red"}>
                        {hourlyHealth == null ? "未知" : hourlyHealth.healthy ? "健康" : "异常"}
                      </Tag>
                    </div>
                    <div className="cm-muted" style={{ marginBottom: 4 }}>
                      检测结果：{hourlyHealth?.reason || "-"}；一致性（1h vs 1m）：
                      {hourlyHealth?.h1m1Consistency
                        ? `${hourlyHealth.h1m1Consistency.mismatch}/${hourlyHealth.h1m1Consistency.compared}`
                        : "-"}
                    </div>
                    <div className="cm-muted" style={{ marginBottom: 4 }}>
                      最新1m：spot {formatTs(hourlyHealth?.latest1mByMarket?.spot || null)} / swap {formatTs(hourlyHealth?.latest1mByMarket?.swap || null)}
                    </div>
                    <div className="cm-muted">
                      最近回补：{formatTs(hourlyHealth?.lastRepairAtMs || null)}；冷却剩余：
                      {hourlyHealth ? `${Math.max(0, Math.floor((hourlyHealth.repairCooldownRemainingMs || 0) / 1000))}s` : "-"}
                    </div>
                  </div>
                  <EChart option={hourlyOption} height={260} />
                </div>
              </div>

              <div className="cm-section">
                <div className="cm-card" style={{ padding: 12 }}>
                  <div className="cm-snapshotHeader">
                    <Title heading={6} style={{ margin: 0 }}>
                      24小时持仓量快照
                    </Title>
                    <Text className="cm-muted">数据来源：Binance openInterestHist（OI）+ trade_buckets（收盘价）</Text>
                  </div>
                  <div className="cm-oiSplit">
                    <EChart option={oiLineOption} height={170} />
                    <EChart option={oiPriceOption} height={140} />
                  </div>
                  <div className="cm-oiChanges">
                    {oiChanges.map((v, i) => (
                      <span key={`${i}-${v}`} className={v >= 0 ? "cm-number--pos" : "cm-number--neg"}>
                        {formatSignedPct(v, 1)}
                      </span>
                    ))}
                  </div>
                </div>
              </div>

              <div className="cm-section">
                <div className="cm-card" style={{ padding: 12 }}>
                  <div className="cm-snapshotHeader">
                    <div style={{ display: "flex", flexDirection: "column", gap: 2 }}>
                      <Title heading={6} style={{ margin: 0 }}>
                        盘口原始指标
                      </Title>
                      <Text className="cm-muted">Spread / 深度不平衡 / 主动买占比 / 回补分（可切换窗口）</Text>
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
                      <div className="cm-number--mono">{formatBps(latestOrderbook?.spreadBps ?? null)}</div>
                    </div>
                    <div className="cm-card" style={{ padding: 8 }}>
                      <Text className="cm-muted">当前深度不平衡</Text>
                      <div className="cm-number--mono">{formatSignedPct(latestOrderbook?.depthImbalancePct ?? null)}</div>
                    </div>
                    <div className="cm-card" style={{ padding: 8 }}>
                      <Text className="cm-muted">当前主动买占比</Text>
                      <div className="cm-number--mono">{formatSignedPct(latestOrderbook?.aggrBuyPct ?? null)}</div>
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
                        当前分钟仍在采集中，已展示最近有效样本（{formatHour(latestOrderbook.ts, timeDisplayMode)}）。
                      </Text>
                    </div>
                  )}
                  <div className="cm-muted" style={{ marginTop: 6 }}>
                    解读：深度不平衡 &gt; 0 表示买盘更厚；主动买占比 &gt; 50% 偏多；Spread 越低越好；回补分越高表示被吃后补单更快。
                  </div>
                  <div className="cm-muted" style={{ marginTop: 6 }}>
                    最新{latestOrderbook?.isFallback ? "有效" : ""}（{latestOrderbook?.marketLabel || "-"} {latestOrderbook ? formatHour(latestOrderbook.ts, timeDisplayMode) : "--:--"}
                    ）：
                    Spread {formatBps(latestOrderbook?.spreadBps ?? null)}，深度
                    {formatSignedPct(latestOrderbook?.depthImbalancePct ?? null)}，主动买占比
                    {formatSignedPct(latestOrderbook?.aggrBuyPct ?? null)}
                  </div>
                </div>
              </div>
            </>
          )}

          {hourlyTab === "netflow" && (
            <div className="cm-section">
              <div className="cm-card" style={{ padding: 12 }}>
                <div className="cm-snapshotHeader">
                  <div style={{ display: "flex", flexDirection: "column", gap: 2 }}>
                    <Title heading={6} style={{ margin: 0 }}>
                      每小时净流入
                    </Title>
                    <Text className="cm-muted">数据来源：trade_buckets_1h（每小时净流入）</Text>
                  </div>
                  <div className="cm-miniLegend">
                    <span className="cm-miniLegendItem">
                      <span className="cm-miniDot cm-miniDot--swap" /> 合约
                    </span>
                    {!hideSpotSeries && (
                      <span className="cm-miniLegendItem">
                        <span className="cm-miniDot cm-miniDot--spot" /> 现货
                      </span>
                    )}
                  </div>
                </div>
                <EChart option={hourlyFlowOption} height={260} />
              </div>
            </div>
          )}
        </>
      )}

      {activeTab === "daily" && (
        <div className="cm-section">
          <div className="cm-card" style={{ padding: 12 }}>
            <div className="cm-snapshotHeader">
              <Title heading={6} style={{ margin: 0 }}>
                按日累计净资金
              </Title>
              <div className="cm-miniLegend">
                <span className="cm-miniLegendItem">
                  <span className="cm-miniDot cm-miniDot--swap" /> 合约
                </span>
                {!hideSpotSeries && (
                  <span className="cm-miniLegendItem">
                    <span className="cm-miniDot cm-miniDot--spot" /> 现货
                  </span>
                )}
              </div>
            </div>
            <EChart option={dailyAccOption} height={240} />
          </div>
          <div className="cm-card" style={{ padding: 12, marginTop: 12 }}>
            <div className="cm-snapshotHeader">
              <Title heading={6} style={{ margin: 0 }}>
                近30天持仓量快照
              </Title>
            </div>
            <div className="cm-oiSplit">
              <EChart option={oiDailyLineOption} height={170} />
              <EChart option={oiDailyPriceOption} height={140} />
            </div>
          </div>
        </div>
      )}

      {activeTab === "recent" && (
        <div className="cm-section">
          <div className="cm-card" style={{ padding: 12 }}>
            <div className="cm-snapshotHeader">
              <Title heading={6} style={{ margin: 0 }}>
                每日净资金
              </Title>
              <div className="cm-miniLegend">
                <span className="cm-miniLegendItem">
                  <span className="cm-miniDot cm-miniDot--swap" /> 合约
                </span>
                {!hideSpotSeries && (
                  <span className="cm-miniLegendItem">
                    <span className="cm-miniDot cm-miniDot--spot" /> 现货
                  </span>
                )}
              </div>
            </div>
            <EChart option={recentFundOption} height={220} />
            <div className="cm-snapshotHeader" style={{ marginTop: 12 }}>
              <Title heading={6} style={{ margin: 0 }}>
                近20日高低点
              </Title>
            </div>
            <EChart option={recentHighLowOption} height={260} />
          </div>
        </div>
      )}

      {activeTab === "quant" && (
        <div className="cm-section">
          <div className="cm-card" style={{ padding: 12 }}>
            <div className="cm-snapshotHeader" style={{ marginBottom: 10 }}>
              <Title heading={6} style={{ margin: 0 }}>
                量化 K 线
              </Title>
              <Space>
                <QuantHelp title="量化指标说明" />
                <span className="cm-muted">周期</span>
                <Select value={quantBucket} onChange={(v) => setQuantBucket(v as QuantBucket)} style={{ width: 100 }}>
                  {QUANT_BUCKET_OPTIONS.map((option) => (
                    <Select.Option key={option.value} value={option.value}>
                      {option.label}
                    </Select.Option>
                  ))}
                </Select>
                <Select value={quantCvdMode} onChange={(v) => setQuantCvdMode(v as CvdMode)} style={{ width: 130 }}>
                  <Select.Option value="visible">Visible</Select.Option>
                  <Select.Option value="rolling24h">Rolling 24H</Select.Option>
                  <Select.Option value="session">Session</Select.Option>
                </Select>
              </Space>
            </div>
            <div style={{ display: "grid", gridTemplateColumns: "minmax(0, 1fr) 360px", gap: 12, alignItems: "start" }}>
              <QuantChart
                items={quantData?.items ?? []}
                height={620}
                symbol={sym}
                market={market}
                bucket={quantBucket}
                cvdMode={quantCvdMode}
                visibleAnchorTs={quantVisibleAnchorTs}
                onVisibleAnchorChange={setQuantVisibleAnchorTs}
              />
              <div className="cm-card" style={{ padding: 10 }}>
                <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: 8 }}>
                  <Text style={{ fontWeight: 600 }}>Whale Radar · {whaleSourceText}</Text>
                  <Text className="cm-muted">全档位 · 近4小时</Text>
                </div>
                <div className="cm-muted" style={{ marginBottom: 8 }}>
                  阈值：{formatCnCompact(whaleRadar?.minLimit ?? null)} · Spoof：{formatCnCompact(whaleRadar?.spoofLimit ?? null)}
                </div>
                <div style={{ display: "grid", gridTemplateColumns: "0.8fr 1.2fr 0.9fr 1fr 0.8fr 1fr", gap: 6, marginBottom: 6 }}>
                  <Text className="cm-muted">方向</Text>
                  <Text className="cm-muted">价格</Text>
                  <Text className="cm-muted">距离</Text>
                  <Text className="cm-muted">强度</Text>
                  <Text className="cm-muted">持续</Text>
                  <Text className="cm-muted">性质</Text>
                </div>
                <div style={{ display: "grid", gap: 6, maxHeight: 560, overflowY: "auto" }}>
                  {(whaleRadar?.items || []).map((row, idx) => (
                    <div
                      key={`${row.price}-${row.lastSeenTs}-${idx}`}
                      style={{
                        display: "grid",
                        gridTemplateColumns: "0.8fr 1.2fr 0.9fr 1fr 0.8fr 1fr",
                        gap: 6,
                        padding: "6px 4px",
                        borderRadius: 6,
                        background: idx % 2 === 0 ? "rgba(148,163,184,0.08)" : "transparent",
                      }}
                    >
                      <span className={row.side === "BUY" ? "cm-number--pos" : row.side === "SELL" ? "cm-number--neg" : "cm-number--mono"}>
                        {row.side === "BUY" ? "买" : row.side === "SELL" ? "卖" : "-"}
                      </span>
                      <span className="cm-number--mono">{formatPriceFixed(row.price, 2)}</span>
                      <span className={row.distancePct != null && row.distancePct >= 0 ? "cm-number--neg" : "cm-number--pos"}>
                        {formatSignedPct(row.distancePct ?? null, 2)}
                      </span>
                      <span className="cm-number--mono">{formatCnCompact(row.value, 2)}</span>
                      <span className="cm-number--mono">{formatDurationMin(row.durationMin)}</span>
                      <span className={row.nature === "REAL" ? "cm-number--pos" : row.nature === "SPOOF" ? "cm-number--neg" : "cm-number--mono"}>
                        {row.label}
                      </span>
                    </div>
                  ))}
                  {(whaleRadar?.items || []).length === 0 && <Text className="cm-muted">暂无大户挂单异动</Text>}
                </div>
              </div>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

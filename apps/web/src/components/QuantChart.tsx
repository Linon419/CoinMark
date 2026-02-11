import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  createChart,
  CandlestickSeries,
  LineSeries,
  HistogramSeries,
  type IChartApi,
  type ISeriesApi,
  type CandlestickData,
  type SingleValueData,
  type HistogramData,
  ColorType,
  CrosshairMode,
} from "lightweight-charts";
import EChart from "./EChart";

export type QuantItem = {
  ts: number;
  o: number;
  h: number;
  l: number;
  c: number;
  vol_buy: number;
  vol_sell: number;
  vol_total: number;
  delta: number;
  cvd: number;
  vwap: number | null;
  count: number;
};

export type CvdMode = "visible" | "rolling24h" | "session";
type QuantMarket = "spot" | "swap";
type QuantBucket = "15m" | "1h" | "4h" | "1d";

type Props = {
  items: QuantItem[];
  height?: number;
  symbol?: string;
  market?: QuantMarket;
  bucket?: QuantBucket;
  cvdMode?: CvdMode;
  visibleAnchorTs?: number | null;
  onVisibleAnchorChange?: (ts: number | null) => void;
};

const MAX_POINTS = 240;

function bucketMs(bucket: QuantBucket): number {
  if (bucket === "15m") return 15 * 60 * 1000;
  if (bucket === "1h") return 60 * 60 * 1000;
  if (bucket === "4h") return 4 * 60 * 60 * 1000;
  return 24 * 60 * 60 * 1000;
}

function getVwapLookback(bucket: QuantBucket): number {
  const minutes = Math.max(1, Math.floor(bucketMs(bucket) / 60_000));
  return Math.max(6, Math.min(240, Math.floor((24 * 60) / minutes)));
}

function normalizeSymbol(symbol: string) {
  return symbol.trim().toLowerCase();
}

function getBinanceWsUrl(market: QuantMarket, symbol: string, bucket: QuantBucket) {
  const stream = `${normalizeSymbol(symbol)}@kline_${bucket}`;
  if (market === "spot") {
    return `wss://stream.binance.com:9443/ws/${stream}`;
  }
  return `wss://fstream.binance.com/ws/${stream}`;
}

function rebuildDerived(items: QuantItem[], bucket: QuantBucket): QuantItem[] {
  if (!items.length) return [];
  const lookback = getVwapLookback(bucket);
  let cvdAcc = 0;

  return items.map((item, idx) => {
    const startIdx = Math.max(0, idx - lookback + 1);
    let sumCv = 0;
    let sumV = 0;
    for (let i = startIdx; i <= idx; i += 1) {
      const c = items[i].c;
      const v = items[i].vol_total;
      if (Number.isFinite(c) && Number.isFinite(v) && v > 0) {
        sumCv += c * v;
        sumV += v;
      }
    }

    cvdAcc += item.delta;
    return {
      ...item,
      vwap: sumV > 0 ? sumCv / sumV : null,
      cvd: cvdAcc,
    };
  });
}

function buildItemFromKline(kline: any): QuantItem | null {
  if (!kline || typeof kline !== "object") return null;

  const ts = Number(kline.t);
  const o = Number(kline.o);
  const h = Number(kline.h);
  const l = Number(kline.l);
  const c = Number(kline.c);
  const count = Number(kline.n || 0);
  if (![ts, o, h, l, c].every((n) => Number.isFinite(n))) return null;

  const baseVolRaw = Number(kline.v);
  const quoteVolRaw = Number(kline.q);
  const takerBuyQuoteRaw = Number(kline.Q);

  const baseVol = Number.isFinite(baseVolRaw) ? Math.max(0, baseVolRaw) : 0;
  const volTotal = Number.isFinite(quoteVolRaw) && quoteVolRaw > 0 ? quoteVolRaw : baseVol * c;
  const volBuy = Number.isFinite(takerBuyQuoteRaw) && takerBuyQuoteRaw >= 0 ? takerBuyQuoteRaw : 0;
  const volSell = Math.max(0, volTotal - volBuy);

  return {
    ts,
    o,
    h,
    l,
    c,
    vol_buy: volBuy,
    vol_sell: volSell,
    vol_total: volTotal,
    delta: volBuy - volSell,
    cvd: 0,
    vwap: null,
    count: Number.isFinite(count) ? count : 0,
  };
}

function tsToLwTime(ms: number) {
  return (ms / 1000) as import("lightweight-charts").UTCTimestamp;
}

function buildVpvrOption(items: QuantItem[]) {
  if (!items.length) return null;

  const lows = items.map((x) => x.l).filter(Boolean);
  const highs = items.map((x) => x.h).filter(Boolean);
  if (!lows.length || !highs.length) return null;

  const priceLow = Math.min(...lows);
  const priceHigh = Math.max(...highs);
  const range = priceHigh - priceLow;
  if (range <= 0) return null;

  const binCount = 30;
  const binSize = range / binCount;

  const buyBins = new Float64Array(binCount);
  const sellBins = new Float64Array(binCount);

  for (const item of items) {
    const mid = (item.h + item.l) / 2;
    const idx = Math.min(Math.floor((mid - priceLow) / binSize), binCount - 1);
    if (idx >= 0) {
      buyBins[idx] += item.vol_buy;
      sellBins[idx] += item.vol_sell;
    }
  }

  let pocIdx = 0;
  let maxVol = 0;
  const labels: string[] = [];
  const buyData: number[] = [];
  const sellData: number[] = [];

  for (let i = 0; i < binCount; i++) {
    const binMid = priceLow + (i + 0.5) * binSize;
    labels.push(formatBinPrice(binMid));
    buyData.push(buyBins[i]);
    sellData.push(sellBins[i]);
    const total = buyBins[i] + sellBins[i];
    if (total > maxVol) {
      maxVol = total;
      pocIdx = i;
    }
  }

  const pocLabel = labels[pocIdx];

  return {
    tooltip: {
      trigger: "axis" as const,
      axisPointer: { type: "shadow" as const },
      formatter: (params: any) => {
        const price = params?.[0]?.name || "";
        const buy = params?.find((p: any) => p.seriesName === "Buy")?.value || 0;
        const sell = params?.find((p: any) => p.seriesName === "Sell")?.value || 0;
        return `${price}<br/>Buy: ${fmtVol(buy)}<br/>Sell: ${fmtVol(sell)}`;
      },
    },
    grid: { left: 4, right: 4, top: 20, bottom: 20 },
    title: {
      text: "VPVR",
      left: 4,
      top: 0,
      textStyle: { fontSize: 11, fontWeight: 600, color: "#64748b" },
    },
    xAxis: {
      type: "value" as const,
      axisLabel: { show: false },
      axisLine: { show: false },
      axisTick: { show: false },
      splitLine: { show: false },
    },
    yAxis: {
      type: "category" as const,
      data: labels,
      axisLabel: { fontSize: 9, color: "#94a3b8" },
      axisLine: { show: false },
      axisTick: { show: false },
    },
    series: [
      {
        name: "Buy",
        type: "bar" as const,
        stack: "vol",
        data: buyData,
        barWidth: "60%",
        itemStyle: {
          color: (p: any) =>
            labels[p.dataIndex] === pocLabel
              ? "rgba(34,197,94,0.85)"
              : "rgba(34,197,94,0.45)",
        },
      },
      {
        name: "Sell",
        type: "bar" as const,
        stack: "vol",
        data: sellData,
        barWidth: "60%",
        itemStyle: {
          color: (p: any) =>
            labels[p.dataIndex] === pocLabel
              ? "rgba(239,68,68,0.85)"
              : "rgba(239,68,68,0.45)",
        },
      },
    ],
  };
}

function formatBinPrice(v: number): string {
  const abs = Math.abs(v);
  if (abs < 0.01) return v.toFixed(6);
  if (abs < 1) return v.toFixed(4);
  if (abs < 100) return v.toFixed(2);
  return v.toFixed(0);
}

function fmtVol(v: number): string {
  if (!v || !Number.isFinite(v)) return "-";
  const abs = Math.abs(v);
  if (abs >= 1e8) return `${(v / 1e8).toFixed(2)}亿`;
  if (abs >= 1e4) return `${(v / 1e4).toFixed(2)}万`;
  return v.toFixed(2);
}

export default function QuantChart({
  items,
  height = 620,
  symbol = "BTCUSDT",
  market = "swap",
  bucket = "1h",
  cvdMode = "visible",
  visibleAnchorTs = null,
  onVisibleAnchorChange,
}: Props) {
  const containerRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<IChartApi | null>(null);
  const seriesRef = useRef<{
    candle: ISeriesApi<"Candlestick"> | null;
    vwap: ISeriesApi<"Line"> | null;
    delta: ISeriesApi<"Histogram"> | null;
    cvd: ISeriesApi<"Line"> | null;
  }>({ candle: null, vwap: null, delta: null, cvd: null });
  const [liveItems, setLiveItems] = useState<QuantItem[]>(() => rebuildDerived(items, bucket));
  const liveItemsRef = useRef<QuantItem[]>(liveItems);
  const needFitContentRef = useRef(true);

  useEffect(() => {
    const next = rebuildDerived(items, bucket);
    liveItemsRef.current = next;
    setLiveItems(next);
    needFitContentRef.current = true;
  }, [items, bucket, symbol, market]);

  const mergeRealtimeKline = useCallback(
    (kline: any) => {
      const patch = buildItemFromKline(kline);
      if (!patch) return;

      setLiveItems((prev) => {
        const base = prev.length ? prev : liveItemsRef.current;
        if (!base.length) return prev;

        const last = base[base.length - 1];
        if (patch.ts < last.ts) return prev;

        let next: QuantItem[];
        if (patch.ts === last.ts) {
          next = [...base.slice(0, -1), { ...last, ...patch }];
        } else {
          next = [...base, patch];
          if (next.length > MAX_POINTS) {
            next = next.slice(next.length - MAX_POINTS);
          }
        }

        const derived = rebuildDerived(next, bucket);
        liveItemsRef.current = derived;
        return derived;
      });
    },
    [bucket]
  );

  useEffect(() => {
    const sym = (symbol || "").trim().toUpperCase();
    if (!sym) return;

    let ws: WebSocket | null = null;
    let reconnectTimer: number | null = null;
    let attempt = 0;
    let stopped = false;

    const connect = () => {
      if (stopped) return;
      const url = getBinanceWsUrl(market, sym, bucket);
      ws = new WebSocket(url);

      ws.onopen = () => {
        attempt = 0;
      };

      ws.onmessage = (event) => {
        try {
          const payload = JSON.parse(String(event.data || "{}"));
          const kline = payload?.k || payload?.data?.k;
          if (!kline) return;
          mergeRealtimeKline(kline);
        } catch {
          // ignore malformed payload
        }
      };

      ws.onclose = () => {
        if (stopped) return;
        attempt += 1;
        const delay = Math.min(10_000, 1000 * 2 ** Math.min(attempt, 4)) + Math.floor(Math.random() * 300);
        reconnectTimer = window.setTimeout(connect, delay);
      };

      ws.onerror = () => {
        try {
          ws?.close();
        } catch {
          // no-op
        }
      };
    };

    connect();

    return () => {
      stopped = true;
      if (reconnectTimer != null) {
        window.clearTimeout(reconnectTimer);
      }
      try {
        ws?.close();
      } catch {
        // no-op
      }
    };
  }, [symbol, market, bucket, mergeRealtimeKline]);

  useEffect(() => {
    if (!containerRef.current) return;

    const chart = createChart(containerRef.current, {
      width: containerRef.current.clientWidth,
      height,
      layout: {
        background: { type: ColorType.Solid, color: "#ffffff" },
        textColor: "#333",
        fontSize: 11,
      },
      grid: {
        vertLines: { color: "rgba(197,203,206,0.3)" },
        horzLines: { color: "rgba(197,203,206,0.3)" },
      },
      crosshair: { mode: CrosshairMode.Normal },
      rightPriceScale: { borderColor: "rgba(197,203,206,0.8)" },
      timeScale: {
        borderColor: "rgba(197,203,206,0.8)",
        timeVisible: true,
        secondsVisible: false,
      },
    });

    const candleSeries = chart.addSeries(CandlestickSeries, {
      upColor: "#22c55e",
      downColor: "#ef4444",
      borderUpColor: "#22c55e",
      borderDownColor: "#ef4444",
      wickUpColor: "#22c55e",
      wickDownColor: "#ef4444",
    }, 0);

    const vwapSeries = chart.addSeries(LineSeries, {
      color: "#f59e0b",
      lineWidth: 2,
      lineStyle: 2,
      priceScaleId: "right",
      title: "VWAP",
    }, 0);

    const deltaSeries = chart.addSeries(HistogramSeries, {
      priceFormat: { type: "volume" },
      title: "Delta",
    }, 1);

    const cvdSeries = chart.addSeries(LineSeries, {
      color: "#3b82f6",
      lineWidth: 2,
      title: "CVD",
      priceFormat: { type: "volume" },
    }, 2);

    chartRef.current = chart;
    seriesRef.current = {
      candle: candleSeries,
      vwap: vwapSeries,
      delta: deltaSeries,
      cvd: cvdSeries,
    };

    const visibleHandler = (range: any) => {
      if (!range || !onVisibleAnchorChange) return;
      const from = Number(range.from);
      if (!Number.isFinite(from)) return;
      const ts = Math.floor(from * 1000);
      onVisibleAnchorChange(ts);
    };
    chart.timeScale().subscribeVisibleTimeRangeChange(visibleHandler);

    const onResize = () => {
      if (containerRef.current) {
        chart.applyOptions({ width: containerRef.current.clientWidth });
      }
    };
    window.addEventListener("resize", onResize);

    return () => {
      window.removeEventListener("resize", onResize);
      chart.timeScale().unsubscribeVisibleTimeRangeChange(visibleHandler);
      chart.remove();
      chartRef.current = null;
    };
  }, [height, onVisibleAnchorChange]);

  useEffect(() => {
    const { candle, vwap, delta, cvd } = seriesRef.current;
    if (!candle || !liveItems.length) return;

    const candleData: CandlestickData[] = liveItems.map((x) => ({
      time: tsToLwTime(x.ts),
      open: x.o,
      high: x.h,
      low: x.l,
      close: x.c,
    }));

    const vwapData: SingleValueData[] = liveItems
      .filter((x) => x.vwap != null)
      .map((x) => ({
        time: tsToLwTime(x.ts),
        value: x.vwap!,
      }));

    const deltaData: HistogramData[] = liveItems.map((x) => ({
      time: tsToLwTime(x.ts),
      value: x.delta,
      color: x.delta >= 0 ? "rgba(34,197,94,0.7)" : "rgba(239,68,68,0.7)",
    }));

    const getSessionAnchorUtc = (ts: number) => {
      const d = new Date(ts);
      const day = d.getUTCDay();
      const diff = (day + 6) % 7;
      d.setUTCHours(0, 0, 0, 0);
      d.setUTCDate(d.getUTCDate() - diff);
      return d.getTime();
    };

    const mode: CvdMode = cvdMode;
    const lastTs = liveItems[liveItems.length - 1]?.ts ?? 0;
    const defaultVisibleTs = liveItems[Math.max(0, Math.floor((liveItems.length - 1) * 0.4))]?.ts ?? liveItems[0]?.ts ?? 0;
    const anchorTs =
      mode === "rolling24h"
        ? lastTs - 24 * 60 * 60 * 1000
        : mode === "session"
          ? getSessionAnchorUtc(lastTs)
          : (visibleAnchorTs ?? defaultVisibleTs);

    let cumulative = 0;
    const cvdData: Array<SingleValueData | { time: import("lightweight-charts").UTCTimestamp }> = [];
    for (const x of liveItems) {
      const time = tsToLwTime(x.ts);
      if (x.ts < anchorTs) {
        cvdData.push({ time });
        continue;
      }
      cumulative += x.delta;
      cvdData.push({ time, value: cumulative });
    }

    candle.setData(candleData);
    vwap?.setData(vwapData);
    delta?.setData(deltaData);
    cvd?.setData(cvdData as any);

    if (needFitContentRef.current) {
      chartRef.current?.timeScale().fitContent();
      needFitContentRef.current = false;
    }
  }, [liveItems, cvdMode, visibleAnchorTs]);

  const vpvrOption = useMemo(() => buildVpvrOption(liveItems), [liveItems]);

  return (
    <div style={{ display: "flex", gap: 8 }}>
      <div ref={containerRef} style={{ flex: 1, minWidth: 0 }} />
      {vpvrOption && (
        <div style={{ width: 140, flexShrink: 0 }}>
          <EChart option={vpvrOption} height={height} />
        </div>
      )}
    </div>
  );
}

import { useEffect, useRef, useMemo } from "react";
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

type Props = {
  items: QuantItem[];
  height?: number;
};

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

export default function QuantChart({ items, height = 620 }: Props) {
  const containerRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<IChartApi | null>(null);
  const seriesRef = useRef<{
    candle: ISeriesApi<"Candlestick"> | null;
    vwap: ISeriesApi<"Line"> | null;
    delta: ISeriesApi<"Histogram"> | null;
    cvd: ISeriesApi<"Line"> | null;
  }>({ candle: null, vwap: null, delta: null, cvd: null });

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

    const onResize = () => {
      if (containerRef.current) {
        chart.applyOptions({ width: containerRef.current.clientWidth });
      }
    };
    window.addEventListener("resize", onResize);

    return () => {
      window.removeEventListener("resize", onResize);
      chart.remove();
      chartRef.current = null;
    };
  }, [height]);

  useEffect(() => {
    const { candle, vwap, delta, cvd } = seriesRef.current;
    if (!candle || !items.length) return;

    const candleData: CandlestickData[] = items.map((x) => ({
      time: tsToLwTime(x.ts),
      open: x.o,
      high: x.h,
      low: x.l,
      close: x.c,
    }));

    const vwapData: SingleValueData[] = items
      .filter((x) => x.vwap != null)
      .map((x) => ({
        time: tsToLwTime(x.ts),
        value: x.vwap!,
      }));

    const deltaData: HistogramData[] = items.map((x) => ({
      time: tsToLwTime(x.ts),
      value: x.delta,
      color: x.delta >= 0 ? "rgba(34,197,94,0.7)" : "rgba(239,68,68,0.7)",
    }));

    const cvdData: SingleValueData[] = items.map((x) => ({
      time: tsToLwTime(x.ts),
      value: x.cvd,
    }));

    candle.setData(candleData);
    vwap?.setData(vwapData);
    delta?.setData(deltaData);
    cvd?.setData(cvdData);

    chartRef.current?.timeScale().fitContent();
  }, [items]);

  const vpvrOption = useMemo(() => buildVpvrOption(items), [items]);

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

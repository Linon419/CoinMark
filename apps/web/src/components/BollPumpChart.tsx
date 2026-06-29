import { useEffect, useMemo, useRef, useState } from "react";
import {
  CandlestickSeries,
  ColorType,
  CrosshairMode,
  HistogramSeries,
  LineSeries,
  createChart,
  createSeriesMarkers,
  type CandlestickData,
  type HistogramData,
  type IChartApi,
  type ISeriesApi,
  type SeriesMarker,
  type SingleValueData,
  type UTCTimestamp,
} from "lightweight-charts";
import type { BollPumpDetail } from "../services/bollPump";

type PriceLine = ISeriesApi<"Line", UTCTimestamp>;
type ChartCandle = { time: number; open: number; high: number; low: number; close: number; volume: number };
type WsStatus = "idle" | "connecting" | "live" | "reconnecting" | "closed";

const MAX_DETAIL_CANDLES = 320;

function toLwTime(ms: number) {
  return Math.floor(ms / 1000) as UTCTimestamp;
}

function isFiniteNumber(v: unknown): v is number {
  return typeof v === "number" && Number.isFinite(v);
}

function fmtCompact(v: number, digits = 4) {
  if (!Number.isFinite(v)) return "-";
  if (Math.abs(v) >= 100) return v.toFixed(2);
  if (Math.abs(v) >= 1) return v.toFixed(digits);
  return v.toPrecision(5);
}

function fmtPct(v: number) {
  if (!Number.isFinite(v)) return "-";
  const sign = v > 0 ? "+" : "";
  return `${sign}${v.toFixed(2)}%`;
}

function fmtTime(ms: number) {
  if (!Number.isFinite(ms)) return "-";
  const d = new Date(ms);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function normalizeDetailCandles(detail: BollPumpDetail | null) {
  return [...(detail?.candles || [])]
    .filter((c) => [c.time, c.open, c.high, c.low, c.close, c.volume].every(isFiniteNumber))
    .sort((a, b) => a.time - b.time) as ChartCandle[];
}

function buildBollData(candles: ChartCandle[], period: number, stdDev: number) {
  const safePeriod = Math.max(2, Math.floor(period || 20));
  const safeStdDev = Number.isFinite(stdDev) && stdDev > 0 ? stdDev : 2;
  const upper: SingleValueData<UTCTimestamp>[] = [];
  const middle: SingleValueData<UTCTimestamp>[] = [];
  const lower: SingleValueData<UTCTimestamp>[] = [];

  for (let i = safePeriod - 1; i < candles.length; i++) {
    const window = candles.slice(i - safePeriod + 1, i + 1);
    const mean = window.reduce((sum, x) => sum + x.close, 0) / safePeriod;
    const variance = window.reduce((sum, x) => sum + (x.close - mean) ** 2, 0) / safePeriod;
    const band = Math.sqrt(variance) * safeStdDev;
    const time = toLwTime(candles[i].time);
    middle.push({ time, value: mean });
    upper.push({ time, value: mean + band });
    lower.push({ time, value: mean - band });
  }

  return { upper, middle, lower };
}

function buildEmaData(candles: Array<{ time: number; close: number }>, period: number) {
  if (candles.length < period) return [] as SingleValueData<UTCTimestamp>[];
  const out: SingleValueData<UTCTimestamp>[] = [];
  const k = 2 / (period + 1);
  let ema = 0;
  for (let i = 0; i < candles.length; i++) {
    const close = candles[i].close;
    if (i < period) {
      ema += close;
      if (i === period - 1) {
        ema /= period;
        out.push({ time: toLwTime(candles[i].time), value: ema });
      }
      continue;
    }
    ema = close * k + ema * (1 - k);
    out.push({ time: toLwTime(candles[i].time), value: ema });
  }
  return out;
}

function buildPeriodChanges(candles: Array<{ close: number }>) {
  const last = candles[candles.length - 1];
  if (!last || last.close <= 0) return [];
  return [1, 3, 5, 10, 20]
    .map((bars) => {
      const prev = candles[candles.length - 1 - bars];
      if (!prev || prev.close <= 0) return null;
      return { label: `${bars}K`, value: (last.close / prev.close - 1) * 100 };
    })
    .filter(Boolean) as Array<{ label: string; value: number }>;
}

function getBinanceWsUrl(market: string | undefined, symbol: string, timeframe: string) {
  const stream = `${symbol.trim().toLowerCase()}@kline_${timeframe}`;
  if (String(market || "").toLowerCase() === "spot") {
    return `wss://stream.binance.com:9443/ws/${stream}`;
  }
  return `wss://fstream.binance.com/ws/${stream}`;
}

function buildCandleFromKline(kline: any): ChartCandle | null {
  const time = Number(kline?.t);
  const open = Number(kline?.o);
  const high = Number(kline?.h);
  const low = Number(kline?.l);
  const close = Number(kline?.c);
  const volume = Number(kline?.v);
  if (![time, open, high, low, close, volume].every(Number.isFinite)) return null;
  return { time, open, high, low, close, volume };
}

function wsStatusLabel(status: WsStatus) {
  if (status === "live") return "WS live";
  if (status === "connecting") return "WS connecting";
  if (status === "reconnecting") return "WS reconnecting";
  if (status === "closed") return "WS closed";
  return "WS idle";
}

function buildMarkers(detail: BollPumpDetail | null) {
  const markers = detail?.markers || [];
  return markers
    .filter((m) => isFiniteNumber(m.time))
    .map((m) => {
      const confirm = m.kind === "confirm";
      return {
        time: toLwTime(m.time),
        position: confirm ? "belowBar" : "aboveBar",
        color: confirm ? "#f97316" : "#3b82f6",
        shape: confirm ? "arrowUp" : "circle",
        text: m.label,
      } satisfies SeriesMarker<UTCTimestamp>;
    });
}

export default function BollPumpChart({ detail, bollPeriod = 20, bollStdDev = 2 }: { detail: BollPumpDetail | null; bollPeriod?: number; bollStdDev?: number }) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const chartRef = useRef<IChartApi | null>(null);
  const markerApiRef = useRef<ReturnType<typeof createSeriesMarkers<UTCTimestamp>> | null>(null);
  const [liveCandles, setLiveCandles] = useState<ChartCandle[]>([]);
  const [wsStatus, setWsStatus] = useState<WsStatus>("idle");
  const seriesRef = useRef<{
    candle: ISeriesApi<"Candlestick", UTCTimestamp> | null;
    upper: PriceLine | null;
    middle: PriceLine | null;
    lower: PriceLine | null;
    ema10: PriceLine | null;
    volume: ISeriesApi<"Histogram", UTCTimestamp> | null;
  }>({ candle: null, upper: null, middle: null, lower: null, ema10: null, volume: null });

  useEffect(() => {
    setLiveCandles(normalizeDetailCandles(detail));
  }, [detail]);

  useEffect(() => {
    const signal = detail?.signal;
    const symbol = signal?.symbol?.trim().toUpperCase();
    const timeframe = signal?.timeframe;
    if (!symbol || !timeframe) {
      setWsStatus("idle");
      return;
    }

    let ws: WebSocket | null = null;
    let reconnectTimer: number | null = null;
    let attempt = 0;
    let stopped = false;

    const mergeCandle = (patch: ChartCandle) => {
      setLiveCandles((prev) => {
        if (!prev.length) return [patch];
        const last = prev[prev.length - 1];
        if (patch.time < last.time) return prev;
        const next =
          patch.time === last.time
            ? [...prev.slice(0, -1), { ...last, ...patch }]
            : [...prev, patch].slice(-MAX_DETAIL_CANDLES);
        return next;
      });
    };

    const connect = () => {
      if (stopped) return;
      setWsStatus(attempt > 0 ? "reconnecting" : "connecting");
      ws = new WebSocket(getBinanceWsUrl(signal.market, symbol, timeframe));

      ws.onopen = () => {
        attempt = 0;
        setWsStatus("live");
      };

      ws.onmessage = (event) => {
        try {
          const payload = JSON.parse(String(event.data || "{}"));
          const kline = payload?.k || payload?.data?.k;
          const candle = buildCandleFromKline(kline);
          if (candle) mergeCandle(candle);
        } catch {
          // ignore malformed WS payload
        }
      };

      ws.onclose = () => {
        if (stopped) return;
        attempt += 1;
        setWsStatus("reconnecting");
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
      if (reconnectTimer != null) window.clearTimeout(reconnectTimer);
      try {
        ws?.close();
      } catch {
        // no-op
      }
      setWsStatus("closed");
    };
  }, [detail?.signal?.id, detail?.signal?.market, detail?.signal?.symbol, detail?.signal?.timeframe]);

  const chartData = useMemo(() => {
    const candles = liveCandles;

    const candleData = candles.map((c) => ({
      time: toLwTime(c.time),
      open: c.open,
      high: c.high,
      low: c.low,
      close: c.close,
    })) as CandlestickData<UTCTimestamp>[];

    const volumeData = candles.map((c) => ({
      time: toLwTime(c.time),
      value: Number(c.volume || 0),
      color: c.close >= c.open ? "rgba(34,197,94,0.45)" : "rgba(239,68,68,0.45)",
    })) as HistogramData<UTCTimestamp>[];

    const boll = buildBollData(candles, bollPeriod, bollStdDev);
    const last = candles[candles.length - 1];

    return {
      candleData,
      volumeData,
      upper: boll.upper,
      middle: boll.middle,
      lower: boll.lower,
      ema10: buildEmaData(candles, 10),
      changes: buildPeriodChanges(candles),
      markers: buildMarkers(detail),
      last,
    };
  }, [detail, liveCandles, bollPeriod, bollStdDev]);

  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;

    const chart = createChart(el, {
      autoSize: true,
      height: 520,
      layout: {
        background: { type: ColorType.Solid, color: "transparent" },
        textColor: "#94a3b8",
        fontSize: 11,
      },
      grid: {
        vertLines: { color: "rgba(148,163,184,0.14)" },
        horzLines: { color: "rgba(148,163,184,0.14)" },
      },
      crosshair: {
        mode: CrosshairMode.Normal,
        vertLine: { color: "rgba(148,163,184,0.50)", labelBackgroundColor: "#0f172a" },
        horzLine: { color: "rgba(148,163,184,0.50)", labelBackgroundColor: "#0f172a" },
      },
      rightPriceScale: {
        borderColor: "rgba(148,163,184,0.24)",
        scaleMargins: { top: 0.08, bottom: 0.22 },
      },
      timeScale: {
        borderColor: "rgba(148,163,184,0.24)",
        timeVisible: true,
        secondsVisible: false,
        rightOffset: 8,
        barSpacing: 9,
      },
      handleScroll: { mouseWheel: true, pressedMouseMove: true, horzTouchDrag: true, vertTouchDrag: false },
      handleScale: { axisPressedMouseMove: true, mouseWheel: true, pinch: true },
    });

    const candle = chart.addSeries(
      CandlestickSeries,
      {
        upColor: "#22c55e",
        downColor: "#ef4444",
        borderVisible: false,
        wickUpColor: "#22c55e",
        wickDownColor: "#ef4444",
        priceLineVisible: true,
        lastValueVisible: true,
      },
      0
    );

    const upper = chart.addSeries(LineSeries, { color: "#60a5fa", lineWidth: 1, title: "BOLL U", priceLineVisible: false }, 0);
    const middle = chart.addSeries(LineSeries, { color: "#f59e0b", lineWidth: 1, lineStyle: 2, title: "BOLL M", priceLineVisible: false }, 0);
    const lower = chart.addSeries(LineSeries, { color: "#60a5fa", lineWidth: 1, title: "BOLL L", priceLineVisible: false }, 0);
    const ema10 = chart.addSeries(LineSeries, { color: "#e879f9", lineWidth: 2, title: "EMA10", priceLineVisible: false }, 0);
    const volume = chart.addSeries(
      HistogramSeries,
      {
        priceFormat: { type: "volume" },
        priceScaleId: "volume",
        title: "Volume",
        lastValueVisible: false,
        priceLineVisible: false,
      },
      0
    );
    volume.priceScale().applyOptions({ scaleMargins: { top: 0.78, bottom: 0 } });

    chartRef.current = chart;
    markerApiRef.current = createSeriesMarkers(candle, []);
    seriesRef.current = { candle, upper, middle, lower, ema10, volume };

    return () => {
      markerApiRef.current?.detach();
      markerApiRef.current = null;
      chart.remove();
      chartRef.current = null;
      seriesRef.current = { candle: null, upper: null, middle: null, lower: null, ema10: null, volume: null };
    };
  }, []);

  useEffect(() => {
    const { candle, upper, middle, lower, ema10, volume } = seriesRef.current;
    if (!candle) return;

    candle.setData(chartData.candleData);
    upper?.setData(chartData.upper);
    middle?.setData(chartData.middle);
    lower?.setData(chartData.lower);
    ema10?.setData(chartData.ema10);
    volume?.setData(chartData.volumeData);
    markerApiRef.current?.setMarkers(chartData.markers);
    chartRef.current?.timeScale().fitContent();
  }, [chartData]);

  const signal = detail?.signal;
  const last = chartData.last;

  if (!detail) {
    return <div className="cm-tvChartEmpty">选择一条信号查看 K 线</div>;
  }

  return (
    <div className="cm-tvChartShell">
      <div className="cm-tvChartHeader">
        <div>
          <div className="cm-tvChartTitle">
            <strong>{signal?.symbol || "-"}</strong>
            <span>{signal?.timeframe || "-"}</span>
            <span>{signal?.signal_level || "-"}</span>
          </div>
          <div className="cm-tvChartSub">{signal?.reason || ""}</div>
        </div>
        <div className="cm-tvChartStats">
          <span className={`cm-tvWsStatus cm-tvWsStatus--${wsStatus}`}>{wsStatusLabel(wsStatus)}</span>
          <span>Last {last ? fmtCompact(last.close) : "-"}</span>
          <span>Signal {signal ? fmtCompact(Number(signal.price || 0)) : "-"}</span>
          <span>Vol {signal ? `${fmtCompact(Number(signal.volume_ratio || 0), 2)}x` : "-"}</span>
          <span>{signal ? fmtTime(signal.signal_time_ms) : "-"}</span>
        </div>
      </div>
      <div className="cm-tvChangeStrip">
        {chartData.changes.map((x) => (
          <span key={x.label} className={x.value >= 0 ? "cm-tvChange cm-tvChange--up" : "cm-tvChange cm-tvChange--down"}>
            <b>{x.label}</b>
            {fmtPct(x.value)}
          </span>
        ))}
      </div>
      <div className="cm-tvLegend">
        <span className="cm-tvLegendItem cm-tvLegendItem--up">K</span>
        <span className="cm-tvLegendItem cm-tvLegendItem--band">BOLL U/L</span>
        <span className="cm-tvLegendItem cm-tvLegendItem--mid">BOLL M</span>
        <span className="cm-tvLegendItem cm-tvLegendItem--ema">EMA10</span>
        <span className="cm-tvLegendItem cm-tvLegendItem--vol">Volume</span>
      </div>
      <div ref={containerRef} className="cm-tvChart" />
    </div>
  );
}

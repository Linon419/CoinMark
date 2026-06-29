import { useEffect, useMemo, useRef } from "react";
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

function fmtTime(ms: number) {
  if (!Number.isFinite(ms)) return "-";
  const d = new Date(ms);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function buildLineData(indicators: BollPumpDetail["indicators"], field: "upper" | "middle" | "lower") {
  return indicators
    .filter((x) => isFiniteNumber(x.time) && isFiniteNumber(x[field]) && x[field] > 0)
    .map((x) => ({ time: toLwTime(x.time), value: x[field] })) as SingleValueData<UTCTimestamp>[];
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

export default function BollPumpChart({ detail }: { detail: BollPumpDetail | null }) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const chartRef = useRef<IChartApi | null>(null);
  const markerApiRef = useRef<ReturnType<typeof createSeriesMarkers<UTCTimestamp>> | null>(null);
  const seriesRef = useRef<{
    candle: ISeriesApi<"Candlestick", UTCTimestamp> | null;
    upper: PriceLine | null;
    middle: PriceLine | null;
    lower: PriceLine | null;
    volume: ISeriesApi<"Histogram", UTCTimestamp> | null;
  }>({ candle: null, upper: null, middle: null, lower: null, volume: null });

  const chartData = useMemo(() => {
    const candles = (detail?.candles || [])
      .filter((c) => [c.time, c.open, c.high, c.low, c.close].every(isFiniteNumber))
      .sort((a, b) => a.time - b.time);

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

    const indicators = detail?.indicators || [];
    const last = candles[candles.length - 1];

    return {
      candleData,
      volumeData,
      upper: buildLineData(indicators, "upper"),
      middle: buildLineData(indicators, "middle"),
      lower: buildLineData(indicators, "lower"),
      markers: buildMarkers(detail),
      last,
    };
  }, [detail]);

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
    seriesRef.current = { candle, upper, middle, lower, volume };

    return () => {
      markerApiRef.current?.detach();
      markerApiRef.current = null;
      chart.remove();
      chartRef.current = null;
      seriesRef.current = { candle: null, upper: null, middle: null, lower: null, volume: null };
    };
  }, []);

  useEffect(() => {
    const { candle, upper, middle, lower, volume } = seriesRef.current;
    if (!candle) return;

    candle.setData(chartData.candleData);
    upper?.setData(chartData.upper);
    middle?.setData(chartData.middle);
    lower?.setData(chartData.lower);
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
          <span>Last {last ? fmtCompact(last.close) : "-"}</span>
          <span>Signal {signal ? fmtCompact(Number(signal.price || 0)) : "-"}</span>
          <span>Vol {signal ? `${fmtCompact(Number(signal.volume_ratio || 0), 2)}x` : "-"}</span>
          <span>{signal ? fmtTime(signal.signal_time_ms) : "-"}</span>
        </div>
      </div>
      <div className="cm-tvLegend">
        <span className="cm-tvLegendItem cm-tvLegendItem--up">K</span>
        <span className="cm-tvLegendItem cm-tvLegendItem--band">BOLL U/L</span>
        <span className="cm-tvLegendItem cm-tvLegendItem--mid">BOLL M</span>
        <span className="cm-tvLegendItem cm-tvLegendItem--vol">Volume</span>
      </div>
      <div ref={containerRef} className="cm-tvChart" />
    </div>
  );
}

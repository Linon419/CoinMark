import { useMemo } from "react";
import EChart from "./EChart";
import type { BollPumpDetail } from "../services/bollPump";

export default function BollPumpChart({ detail }: { detail: BollPumpDetail | null }) {
  const option = useMemo(() => {
    const candles = detail?.candles || [];
    const indicators = detail?.indicators || [];
    const x = candles.map((c) => new Date(c.time).toLocaleString());
    const markData = (detail?.markers || [])
      .map((m) => {
        const idx = candles.findIndex((c) => c.time === m.time);
        return idx >= 0
          ? {
              coord: [idx, m.price],
              value: m.label,
              itemStyle: { color: m.kind === "confirm" ? "#ef4444" : "#3b82f6" },
            }
          : null;
      })
      .filter(Boolean);

    return {
      tooltip: { trigger: "axis" },
      legend: { data: ["K", "upper", "middle", "lower"] },
      grid: [{ left: 48, right: 24, top: 36, height: 260 }],
      xAxis: [{ type: "category", data: x }],
      yAxis: [{ scale: true }],
      dataZoom: [{ type: "inside" }, { type: "slider", height: 18 }],
      series: [
        {
          name: "K",
          type: "candlestick",
          data: candles.map((c) => [c.open, c.close, c.low, c.high]),
          markPoint: { data: markData },
        },
        { name: "upper", type: "line", showSymbol: false, data: indicators.map((i) => i.upper) },
        { name: "middle", type: "line", showSymbol: false, data: indicators.map((i) => i.middle) },
        { name: "lower", type: "line", showSymbol: false, data: indicators.map((i) => i.lower) },
      ],
    };
  }, [detail]);

  return <EChart option={option as any} height={360} />;
}

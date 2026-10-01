// 美国现货加密 ETF 资金流的展示逻辑（数据来自 /api/etf/flows）。

export type EtfFlowPoint = { date: string; netInflow: number };

export type EtfFlowSummary = {
  asset: string;
  latest: { date: string; netInflow: number; netAssets: number; cumNetInflow: number; valueTraded: number };
  sum5: number;
  sum20: number;
  series: EtfFlowPoint[];
};

export type FlowTone = "up" | "down" | "flat";

export function flowTone(v: number): FlowTone {
  if (v > 0) return "up";
  if (v < 0) return "down";
  return "flat";
}

// fmtUsdCn 美元金额按亿/万显示；signed 时正数带“+”。
export function fmtUsdCn(v: number, signed = true): string {
  if (!Number.isFinite(v) || v === 0) return "0";
  const abs = Math.abs(v);
  let s: string;
  if (abs >= 1e8) s = `${(abs / 1e8).toFixed(2)}亿`;
  else if (abs >= 1e5) s = `${Math.round(abs / 1e4)}万`;
  else if (abs >= 1e4) s = `${(abs / 1e4).toFixed(1)}万`;
  else s = `${Math.round(abs)}`;
  if (v < 0) return `-${s}`;
  return signed ? `+${s}` : s;
}

// flowBars 迷你柱状图：按本行最大绝对值缩放到 maxPx，至少 1px。
export function flowBars(series: EtfFlowPoint[], maxPx: number) {
  const peak = Math.max(0, ...series.map((p) => Math.abs(p.netInflow)));
  return series.map((p) => ({
    date: p.date,
    value: p.netInflow,
    tone: flowTone(p.netInflow),
    height: peak > 0 ? Math.max(1, Math.round((Math.abs(p.netInflow) / peak) * maxPx)) : 1,
  }));
}

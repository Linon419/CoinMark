const API_BASE = (import.meta as any).env?.VITE_API_BASE || "";

export type BollPumpSignal = {
  id: number;
  market: string;
  symbol: string;
  timeframe: string;
  signal_level: "WATCH" | "CONFIRM_1" | "CONFIRM_2";
  price: number;
  volume_ratio: number;
  boll_bandwidth: number;
  bounce_count: number;
  score: number;
  confluence_score: number;
  priority_score: number;
  signal_time_ms: number;
  candle_start_ms: number;
  reason: string;
};

export type BollPumpState = {
  id: number;
  market: string;
  symbol: string;
  timeframe: string;
  status: string;
  current_score: number;
  confluence_score: number;
  priority_score: number;
  bounce_count: number;
  updated_at?: string;
};

export type BollPumpDetail = {
  signal: BollPumpSignal;
  state?: BollPumpState | null;
  candles: Array<{ time: number; open: number; high: number; low: number; close: number; volume: number }>;
  indicators: Array<{ time: number; middle: number; upper: number; lower: number; atr14: number; bandwidth: number }>;
  markers: Array<{ time: number; label: string; price: number; kind: string }>;
};

async function req<T>(path: string): Promise<T> {
  const r = await fetch(`${API_BASE}${path}`);
  if (!r.ok) throw new Error(`HTTP ${r.status}`);
  return (await r.json()) as T;
}

export function fetchBollPumpSignals(params = "market=swap&limit=100") {
  return req<{ items: BollPumpSignal[]; limit: number }>(`/api/boll-pump/signals?${params}`);
}

export function fetchBollPumpStates(params = "market=swap&limit=100") {
  return req<{ items: BollPumpState[]; limit: number }>(`/api/boll-pump/states?${params}`);
}

export function fetchBollPumpStats(params = "market=swap") {
  return req<any>(`/api/boll-pump/stats?${params}`);
}

export function fetchBollPumpDetail(id: number) {
  return req<BollPumpDetail>(`/api/boll-pump/signals/${id}/detail`);
}

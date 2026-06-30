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
  dominant_timeframe?: string;
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

export type BollPumpSettings = {
  enabled: boolean;
  market: "swap" | "spot";
  timeframes: string[];
  symbol_limit: number;
  scan_timeout_sec: number;
  boll_period: number;
  boll_std_dev: number;
  atr_period: number;
  startup_windows: Record<string, number>;
  gain_thresholds: Record<string, number>;
  volume_thresholds: Record<string, number>;
  background_lookback: number;
  background_recent_window: number;
  background_recent_min_pass: number;
  low_volume_factor: number;
  middle_near_bandwidth_factor: number;
  thin_quote_volume_24h: number;
  watch_trend_check_candles: number;
  watch_trend_max_drawdown_pct: number;
  watch_trend_max_drawdown_atr: number;
  trend_clean_bonus: number;
  trend_wick_penalty: number;
  trend_weak_penalty: number;
  trend_wick_body_max_ratio: number;
  trend_efficiency_min: number;
  minimum_trend_timeframe: "15m" | "30m" | "1h";
  minimum_trend_check_candles: number;
  minimum_trend_gain_pct: number;
  minimum_trend_efficiency_min: number;
  minimum_trend_rising_ratio: number;
  resistance_4h_lookback: number;
  resistance_4h_swing_span: number;
  resistance_4h_cluster_atr: number;
  resistance_4h_cluster_pct: number;
  resistance_4h_breakout_buffer_pct: number;
  resistance_4h_max_distance_pct: number;
  resistance_4h_min_touches: number;
  resistance_4h_breakout_bonus: number;
  watch_telegram_threshold: number;
  confirm1_telegram_threshold: number;
  confirm2_telegram_threshold: number;
  confluence_window_ms: number;
  stage_expiry_candles: number;
};

async function req<T>(path: string, init?: RequestInit): Promise<T> {
  const r = await fetch(`${API_BASE}${path}`, init);
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

export function fetchBollPumpSettings() {
  return req<{ settings: BollPumpSettings }>(`/api/boll-pump/settings`);
}

export function saveBollPumpSettings(settings: BollPumpSettings) {
  return req<{ settings: BollPumpSettings }>(`/api/boll-pump/settings`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(settings),
  });
}

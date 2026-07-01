export type BollPumpCandidateState = {
  symbol: string;
  timeframe: string;
  dominant_timeframe?: string;
  status: string;
  priority_score: number;
  current_score?: number;
  confluence_score?: number;
  bounce_count: number;
  expires_at_candle_ms?: number | null;
};

export type BollPumpCandidateSignal = {
  id: number;
  symbol: string;
  timeframe: string;
  signal_level?: string;
  signal_time_ms?: number;
  score?: number;
  priority_score?: number;
  reason?: string;
};

export type BollPumpTradeCandidate = BollPumpCandidateState & {
  trade_label: "重点" | "可参与" | "突破" | "关键K";
  latest_signal_id?: number;
  has_4h_breakout: boolean;
  is_key_k_4h?: boolean;
};

const inactiveStatuses = new Set(["IDLE", "EXPIRED", "INVALIDATED"]);
const tradeStatuses = new Set(["WATCH", "CONFIRM_1", "CONFIRM_2", "COMPLETED"]);
const candidateSignalLevels = new Set(["WATCH", "CONFIRM_1", "CONFIRM_2"]);
const keyKSignalLevels = new Set(["KEY_K_4H"]);
const keyKMaxAgeMs = 24 * 60 * 60 * 1000;

export function filterActiveBollPumpStates<T extends { status: string; expires_at_candle_ms?: number | null }>(states: T[], nowMs = Date.now()): T[] {
  return states.filter((state) => {
    if (inactiveStatuses.has(String(state.status || "").toUpperCase())) {
      return false;
    }
    if (typeof state.expires_at_candle_ms === "number" && state.expires_at_candle_ms > 0 && state.expires_at_candle_ms <= nowMs) {
      return false;
    }
    return true;
  });
}

export function isBollPumpTradeCandidate(state: { status: string }): boolean {
  return tradeStatuses.has(String(state.status || "").toUpperCase());
}

export function hasBollPumpFourHourBreakout(signal?: { reason?: string | null }): boolean {
  return String(signal?.reason || "").toLowerCase().includes("4h resistance breakout");
}

export function isBollPumpFourHourKeyK(signal?: { signal_level?: string | null }): boolean {
  return keyKSignalLevels.has(String(signal?.signal_level || "").toUpperCase());
}

function isCandidateSignal(signal: BollPumpCandidateSignal): boolean {
  return candidateSignalLevels.has(String(signal.signal_level || "").toUpperCase());
}

function tradeLabelForStatus(status: string): BollPumpTradeCandidate["trade_label"] {
  const upper = String(status || "").toUpperCase();
  if (upper === "COMPLETED" || upper === "CONFIRM_2") {
    return "重点";
  }
  if (upper === "CONFIRM_1") {
    return "可参与";
  }
  return "突破";
}

export function buildBollPumpTradeCandidates(
  states: BollPumpCandidateState[],
  signals: BollPumpCandidateSignal[],
  limit = 12,
  nowMs = Date.now(),
): BollPumpTradeCandidate[] {
  const latestByKey = new Map<string, BollPumpCandidateSignal>();
  for (const signal of signals) {
    if (!isCandidateSignal(signal)) {
      continue;
    }
    const key = `${signal.symbol}:${signal.timeframe}`;
    const prev = latestByKey.get(key);
    if (!prev || Number(signal.signal_time_ms || 0) > Number(prev.signal_time_ms || 0)) {
      latestByKey.set(key, signal);
    }
  }

  const stateCandidates = filterActiveBollPumpStates(states, nowMs)
    .filter(isBollPumpTradeCandidate)
    .filter((state) => latestByKey.has(`${state.symbol}:${state.timeframe}`))
    .map((state) => {
      const latest = latestByKey.get(`${state.symbol}:${state.timeframe}`);
      const status = String(state.status || "").toUpperCase();
      return {
        ...state,
        trade_label: tradeLabelForStatus(status),
        latest_signal_id: latest?.id,
        has_4h_breakout: hasBollPumpFourHourBreakout(latest),
      } satisfies BollPumpTradeCandidate;
    });

  const keyKCandidates = signals
    .filter(isBollPumpFourHourKeyK)
    .filter((signal) => {
      const ts = Number(signal.signal_time_ms || 0);
      return ts > 0 && nowMs - ts <= keyKMaxAgeMs;
    })
    .map((signal) => {
      const score = Number(signal.priority_score || signal.score || 0);
      return {
        symbol: signal.symbol,
        timeframe: signal.timeframe || "4h",
        dominant_timeframe: "4h",
        status: "KEY_K_4H",
        priority_score: score,
        current_score: score,
        confluence_score: 0,
        bounce_count: 0,
        trade_label: "关键K",
        latest_signal_id: signal.id,
        has_4h_breakout: hasBollPumpFourHourBreakout(signal),
        is_key_k_4h: true,
      } satisfies BollPumpTradeCandidate;
    });

  const sorted = [...stateCandidates, ...keyKCandidates].sort((a, b) => {
    if (b.priority_score !== a.priority_score) {
      return b.priority_score - a.priority_score;
    }
    if (a.is_key_k_4h !== b.is_key_k_4h) {
      return a.is_key_k_4h ? -1 : 1;
    }
    if (a.has_4h_breakout !== b.has_4h_breakout) {
      return a.has_4h_breakout ? -1 : 1;
    }
    return b.bounce_count - a.bounce_count;
  });

  return limit > 0 ? sorted.slice(0, limit) : sorted;
}

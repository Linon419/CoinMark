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
  reason?: string;
};

export type BollPumpTradeCandidate = BollPumpCandidateState & {
  trade_label: "重点" | "可参与";
  latest_signal_id?: number;
  has_4h_breakout: boolean;
};

const inactiveStatuses = new Set(["IDLE", "EXPIRED", "INVALIDATED"]);
const tradeStatuses = new Set(["CONFIRM_1", "CONFIRM_2", "COMPLETED"]);
const confirmSignalLevels = new Set(["CONFIRM_1", "CONFIRM_2"]);

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

function isConfirmSignal(signal: BollPumpCandidateSignal): boolean {
  return confirmSignalLevels.has(String(signal.signal_level || "").toUpperCase());
}

export function buildBollPumpTradeCandidates(
  states: BollPumpCandidateState[],
  signals: BollPumpCandidateSignal[],
  limit = 12,
  nowMs = Date.now(),
): BollPumpTradeCandidate[] {
  const latestByKey = new Map<string, BollPumpCandidateSignal>();
  for (const signal of signals) {
    if (!isConfirmSignal(signal)) {
      continue;
    }
    const key = `${signal.symbol}:${signal.timeframe}`;
    const prev = latestByKey.get(key);
    if (!prev || Number(signal.signal_time_ms || 0) > Number(prev.signal_time_ms || 0)) {
      latestByKey.set(key, signal);
    }
  }

  return filterActiveBollPumpStates(states, nowMs)
    .filter(isBollPumpTradeCandidate)
    .filter((state) => latestByKey.has(`${state.symbol}:${state.timeframe}`))
    .map((state) => {
      const latest = latestByKey.get(`${state.symbol}:${state.timeframe}`);
      const status = String(state.status || "").toUpperCase();
      return {
        ...state,
        trade_label: status === "COMPLETED" || status === "CONFIRM_2" ? "重点" : "可参与",
        latest_signal_id: latest?.id,
        has_4h_breakout: hasBollPumpFourHourBreakout(latest),
      } satisfies BollPumpTradeCandidate;
    })
    .sort((a, b) => {
      if (a.has_4h_breakout !== b.has_4h_breakout) {
        return a.has_4h_breakout ? -1 : 1;
      }
      if (b.priority_score !== a.priority_score) {
        return b.priority_score - a.priority_score;
      }
      return b.bounce_count - a.bounce_count;
    })
    .slice(0, limit);
}

export type SignalLabPresentationSignal = {
  eventType?: string | null;
  buyRatio?: number | null;
};

const eventTypeLabels: Record<string, string> = {
  signal_lab_persistent_buy: "持续买入",
  signal_lab_single_large: "单笔大额",
  signal_lab_climax_long: "放量反转多",
  signal_lab_climax_short: "放量反转空",
};

const eventTypeColors: Record<string, string> = {
  signal_lab_persistent_buy: "arcoblue",
  signal_lab_single_large: "orange",
  signal_lab_climax_long: "green",
  signal_lab_climax_short: "red",
};

export function signalLabEventTypeLabel(eventType?: string | null): string {
  if (!eventType) {
    return "-";
  }
  return eventTypeLabels[eventType] || eventType;
}

export function signalLabEventTypeColor(eventType?: string | null): string {
  if (!eventType) {
    return "gray";
  }
  return eventTypeColors[eventType] || "gray";
}

export function formatSignalLabBuyRatio(row: SignalLabPresentationSignal): string {
  if (typeof row.buyRatio !== "number" || !Number.isFinite(row.buyRatio)) {
    return "-";
  }
  return `${(row.buyRatio * 100).toFixed(1)}%`;
}

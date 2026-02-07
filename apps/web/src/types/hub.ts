export type HubLevel = "info" | "warning" | "critical";

export type HubStatus = "connecting" | "connected" | "reconnecting" | "disconnected";

export type HubEvent = {
  id: string;
  type: string;
  level: HubLevel;
  title: string;
  content: string;
  symbol?: string;
  market?: "spot" | "swap" | "both";
  ts: number;
  meta?: Record<string, unknown>;
};

export type HubFilters = {
  markets?: string[];
  symbols?: string[];
  types?: string[];
};

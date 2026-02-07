import type { HubEvent, HubFilters, HubStatus } from "../types/hub";

const API_BASE = (import.meta as any).env?.VITE_API_BASE || "";

function getDefaultHubUrl(): string {
  if (API_BASE) {
    return `${API_BASE.replace(/^http/i, "ws")}/api/hub/market`;
  }
  const wsProto = window.location.protocol === "https:" ? "wss" : "ws";
  return `${wsProto}://${window.location.host}/api/hub/market`;
}

const HUB_URL = (import.meta as any).env?.VITE_HUB_URL || getDefaultHubUrl();

function normalizeEvent(raw: any): HubEvent | null {
  if (!raw || typeof raw !== "object") {
    return null;
  }
  const ts = Number(raw.ts || Date.now());
  return {
    id: String(raw.id || `evt_${ts}_${Math.random().toString(16).slice(2, 8)}`),
    type: String(raw.type || "UNKNOWN"),
    level: raw.level === "critical" || raw.level === "warning" ? raw.level : "info",
    title: String(raw.title || "通知"),
    content: String(raw.content || ""),
    symbol: raw.symbol ? String(raw.symbol).toUpperCase() : undefined,
    market: raw.market,
    ts,
    meta: raw.meta && typeof raw.meta === "object" ? raw.meta : undefined,
  };
}

export class HubClient {
  private socket: WebSocket | null = null;
  private status: HubStatus = "disconnected";
  private reconnectAttempt = 0;
  private reconnectTimer: number | null = null;
  private pingTimer: number | null = null;
  private manualClose = false;
  private pendingFilters: HubFilters | null = null;

  private eventHandlers = new Set<(event: HubEvent) => void>();
  private statusHandlers = new Set<(status: HubStatus) => void>();

  constructor() {
    window.addEventListener("online", this.handleOnline);
    window.addEventListener("offline", this.handleOffline);
  }

  private handleOnline = () => {
    if (this.status !== "connected") {
      this.connect();
    }
  };

  private handleOffline = () => {
    this.setStatus("disconnected");
  };

  private setStatus(next: HubStatus) {
    if (this.status === next) {
      return;
    }
    this.status = next;
    this.statusHandlers.forEach((handler) => handler(next));
  }

  onEvent(handler: (event: HubEvent) => void): () => void {
    this.eventHandlers.add(handler);
    return () => this.eventHandlers.delete(handler);
  }

  onStatus(handler: (status: HubStatus) => void): () => void {
    this.statusHandlers.add(handler);
    handler(this.status);
    return () => this.statusHandlers.delete(handler);
  }

  connect() {
    if (this.manualClose) {
      this.manualClose = false;
    }

    if (this.socket && (this.socket.readyState === WebSocket.OPEN || this.socket.readyState === WebSocket.CONNECTING)) {
      return;
    }

    this.setStatus(this.reconnectAttempt > 0 ? "reconnecting" : "connecting");
    const ws = new WebSocket(HUB_URL);
    this.socket = ws;

    ws.onopen = () => {
      this.reconnectAttempt = 0;
      this.setStatus("connected");
      this.startPing();
      if (this.pendingFilters) {
        this.sendSubscribe(this.pendingFilters);
      }
    };

    ws.onmessage = (event) => {
      try {
        const payload = JSON.parse(String(event.data || "{}"));
        if (payload.kind === "event") {
          const normalized = normalizeEvent(payload.data);
          if (normalized) {
            this.eventHandlers.forEach((handler) => handler(normalized));
          }
          return;
        }
        if (payload.kind === "ping") {
          this.send({ op: "ping", ts: Date.now() });
        }
      } catch {
        // ignore malformed payload
      }
    };

    ws.onclose = () => {
      this.stopPing();
      this.socket = null;
      if (this.manualClose) {
        this.setStatus("disconnected");
        return;
      }
      this.scheduleReconnect();
    };

    ws.onerror = () => {
      // onclose handles reconnect
    };
  }

  disconnect() {
    this.manualClose = true;
    if (this.reconnectTimer != null) {
      window.clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    this.stopPing();
    const ws = this.socket;
    this.socket = null;
    if (ws) {
      ws.close();
    }
    this.setStatus("disconnected");
  }

  subscribe(filters: HubFilters) {
    this.pendingFilters = {
      markets: filters.markets?.map((x) => x.toLowerCase()),
      symbols: filters.symbols?.map((x) => x.toUpperCase()),
      types: filters.types?.map((x) => x.toUpperCase()),
    };
    this.sendSubscribe(this.pendingFilters);
  }

  private sendSubscribe(filters: HubFilters) {
    this.send({ op: "subscribe", ...filters });
  }

  private send(payload: Record<string, unknown>) {
    if (!this.socket || this.socket.readyState !== WebSocket.OPEN) {
      return;
    }
    this.socket.send(JSON.stringify(payload));
  }

  private startPing() {
    this.stopPing();
    this.pingTimer = window.setInterval(() => {
      this.send({ op: "ping", ts: Date.now() });
    }, 20000);
  }

  private stopPing() {
    if (this.pingTimer != null) {
      window.clearInterval(this.pingTimer);
      this.pingTimer = null;
    }
  }

  private scheduleReconnect() {
    if (this.reconnectTimer != null) {
      return;
    }
    this.reconnectAttempt += 1;
    const base = Math.min(30000, 1000 * 2 ** Math.min(this.reconnectAttempt, 5));
    const jitter = Math.floor(Math.random() * 800);
    const delay = base + jitter;
    this.setStatus("reconnecting");
    this.reconnectTimer = window.setTimeout(() => {
      this.reconnectTimer = null;
      this.connect();
    }, delay);
  }
}

export const hubClient = new HubClient();

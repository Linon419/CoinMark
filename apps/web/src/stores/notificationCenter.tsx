import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Message } from "@arco-design/web-react";
import { hubClient } from "../services/hubClient";
import { DEFAULT_NOTIFY_TYPES } from "../constants/notify";
import type { HubEvent, HubStatus } from "../types/hub";

export type NotificationItem = {
  id: string;
  fingerprint: string;
  type: string;
  level: "info" | "warning" | "critical";
  title: string;
  content: string;
  symbol?: string;
  market?: "spot" | "swap" | "both";
  ts: number;
  count: number;
  unread: boolean;
};

type NotificationCenterState = {
  items: NotificationItem[];
  unread: number;
  open: boolean;
  showAllTypes: boolean;
  muted: boolean;
  muteTypes: string[];
  muteSymbols: string[];
  hubStatus: HubStatus;
};

type NotificationCenterContextValue = NotificationCenterState & {
  openPanel: () => void;
  closePanel: () => void;
  markAllRead: () => void;
  clearAll: () => void;
  toggleShowAllTypes: () => void;
  toggleMute: () => void;
  toggleTypeMute: (type: string) => void;
  toggleSymbolMute: (symbol: string) => void;
};

const STORAGE_MUTED = "coinmark_notify_muted";
const STORAGE_SHOW_ALL_TYPES = "coinmark_notify_show_all_types";
const STORAGE_MUTE_TYPES = "coinmark_notify_mute_types";
const STORAGE_MUTE_SYMBOLS = "coinmark_notify_mute_symbols";
const MAX_ITEMS = Number((import.meta as any).env?.VITE_NOTIFY_MAX_ITEMS || 200);
const DEDUPE_WINDOW_MS = Number((import.meta as any).env?.VITE_NOTIFY_DEDUPE_WINDOW_MS || 30000);

function normalizeContent(raw: string): string {
  return String(raw || "")
    .toLowerCase()
    .replace(/\s+/g, " ")
    .trim();
}

function makeFingerprint(event: HubEvent): string {
  const bucket = Math.floor(Number(event.ts || Date.now()) / 30000);
  const normalized = normalizeContent(event.content || event.title);
  return `${event.type}|${event.symbol || ""}|${normalized}|${bucket}`;
}

function readBoolean(key: string, fallback: boolean): boolean {
  const raw = localStorage.getItem(key);
  if (raw == null) {
    return fallback;
  }
  return raw === "true";
}

function readStringArray(key: string): string[] {
  const raw = localStorage.getItem(key);
  if (!raw) {
    return [];
  }
  try {
    const parsed = JSON.parse(raw);
    if (Array.isArray(parsed)) {
      return parsed.map((x) => String(x));
    }
    return [];
  } catch {
    return [];
  }
}

const NotificationCenterContext = createContext<NotificationCenterContextValue | null>(null);

export function NotificationCenterProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<NotificationItem[]>([]);
  const [open, setOpen] = useState(false);
  const [showAllTypes, setShowAllTypes] = useState(() => readBoolean(STORAGE_SHOW_ALL_TYPES, false));
  const [muted, setMuted] = useState(() => readBoolean(STORAGE_MUTED, false));
  const [muteTypes, setMuteTypes] = useState<string[]>(() => readStringArray(STORAGE_MUTE_TYPES));
  const [muteSymbols, setMuteSymbols] = useState<string[]>(() => readStringArray(STORAGE_MUTE_SYMBOLS));
  const [hubStatus, setHubStatus] = useState<HubStatus>("disconnected");

  const itemsRef = useRef<NotificationItem[]>([]);
  const stateRef = useRef({ muted, muteTypes, muteSymbols });

  useEffect(() => {
    stateRef.current = { muted, muteTypes, muteSymbols };
  }, [muted, muteTypes, muteSymbols]);

  useEffect(() => {
    itemsRef.current = items;
  }, [items]);

  useEffect(() => {
    localStorage.setItem(STORAGE_SHOW_ALL_TYPES, String(showAllTypes));
  }, [showAllTypes]);

  useEffect(() => {
    localStorage.setItem(STORAGE_MUTED, String(muted));
  }, [muted]);

  useEffect(() => {
    localStorage.setItem(STORAGE_MUTE_TYPES, JSON.stringify(muteTypes));
  }, [muteTypes]);

  useEffect(() => {
    localStorage.setItem(STORAGE_MUTE_SYMBOLS, JSON.stringify(muteSymbols));
  }, [muteSymbols]);

  const pushEvent = useCallback((event: HubEvent) => {
    const now = Date.now();
    const fingerprint = makeFingerprint(event);
    const duplicate = itemsRef.current.some((item) => item.fingerprint === fingerprint && now - item.ts <= DEDUPE_WINDOW_MS);

    setItems((prev) => {
      const next = [...prev];
      const index = next.findIndex((item) => item.fingerprint === fingerprint && now - item.ts <= DEDUPE_WINDOW_MS);
      if (index >= 0) {
        const target = next[index];
        next[index] = {
          ...target,
          ts: now,
          count: target.count + 1,
          unread: true,
        };
        if (index > 0) {
          const [head] = next.splice(index, 1);
          next.unshift(head);
        }
        return next;
      }

      const appended: NotificationItem = {
        id: event.id,
        fingerprint,
        type: event.type,
        level: event.level,
        title: event.title,
        content: event.content,
        symbol: event.symbol,
        market: event.market,
        ts: Number(event.ts || now),
        count: 1,
        unread: true,
      };

      return [appended, ...next].slice(0, MAX_ITEMS);
    });

    const current = stateRef.current;
    const typeMuted = current.muteTypes.includes(event.type);
    const symbolMuted = !!event.symbol && current.muteSymbols.includes(event.symbol);
    const shouldToast = !duplicate && !current.muted && !typeMuted && !symbolMuted;
    if (shouldToast) {
      const content = `${event.symbol ? `${event.symbol} ` : ""}${event.content || event.title}`.trim();
      if (event.level === "critical") {
        Message.error(content);
      } else if (event.level === "warning") {
        Message.warning(content);
      } else {
        Message.info(content);
      }
    }
  }, []);

  useEffect(() => {
    const offEvent = hubClient.onEvent(pushEvent);
    const offStatus = hubClient.onStatus(setHubStatus);
    hubClient.connect();

    return () => {
      offEvent();
      offStatus();
      hubClient.disconnect();
    };
  }, [pushEvent]);

  useEffect(() => {
    hubClient.subscribe(showAllTypes ? {} : { types: [...DEFAULT_NOTIFY_TYPES] });
  }, [showAllTypes]);

  const unread = useMemo(() => items.reduce((acc, item) => acc + (item.unread ? 1 : 0), 0), [items]);

  const markAllRead = useCallback(() => {
    setItems((prev) => prev.map((item) => ({ ...item, unread: false })));
  }, []);

  const clearAll = useCallback(() => {
    setItems([]);
  }, []);

  const toggleShowAllTypes = useCallback(() => {
    setShowAllTypes((prev) => !prev);
  }, []);

  const toggleMute = useCallback(() => {
    setMuted((prev) => !prev);
  }, []);

  const toggleTypeMute = useCallback((type: string) => {
    setMuteTypes((prev) => {
      if (prev.includes(type)) {
        return prev.filter((item) => item !== type);
      }
      return [...prev, type];
    });
  }, []);

  const toggleSymbolMute = useCallback((symbol: string) => {
    setMuteSymbols((prev) => {
      if (prev.includes(symbol)) {
        return prev.filter((item) => item !== symbol);
      }
      return [...prev, symbol];
    });
  }, []);

  const value = useMemo<NotificationCenterContextValue>(
    () => ({
      items,
      unread,
      open,
      showAllTypes,
      muted,
      muteTypes,
      muteSymbols,
      hubStatus,
      openPanel: () => setOpen(true),
      closePanel: () => setOpen(false),
      markAllRead,
      clearAll,
      toggleShowAllTypes,
      toggleMute,
      toggleTypeMute,
      toggleSymbolMute,
    }),
    [
      clearAll,
      hubStatus,
      items,
      markAllRead,
      muteSymbols,
      muteTypes,
      muted,
      open,
      showAllTypes,
      toggleMute,
      toggleShowAllTypes,
      toggleSymbolMute,
      toggleTypeMute,
      unread,
    ]
  );

  return <NotificationCenterContext.Provider value={value}>{children}</NotificationCenterContext.Provider>;
}

export function useNotificationCenter() {
  const context = useContext(NotificationCenterContext);
  if (!context) {
    throw new Error("useNotificationCenter must be used inside NotificationCenterProvider");
  }
  return context;
}

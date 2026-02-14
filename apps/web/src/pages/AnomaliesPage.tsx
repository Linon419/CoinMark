import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { Button, Checkbox, Select, Space, Table, Tag, Typography } from "@arco-design/web-react";

type Market = "spot" | "swap";

type AbsorptionSignalItem = {
  symbol: string;
  signalState: "NONE" | "WATCH" | "CONFIRM" | "STRONG" | "FADING";
  direction: "LONG_BIAS" | "SHORT_BIAS";
  score: number;
  windows: Record<string, { passed: boolean; score: number }>;
  reasons: string[];
  ts: number;
};

type HotItem = {
  id: number;
  market?: string;
  symbol: string;
  eventType: string;
  tfSignal: string;
  tfLevel: string | null;
  eventTimeMs: number;
  title: string;
  details: any;
  dailyAlertCount?: number;
};

type WhaleWallGroupedItem = {
  key: string;
  market: string;
  symbol: string;
  side: string;
  wallPrice: number;
  status: "ACTIVE" | "FILLED" | "CANCELED";
  count: number;
  firstTimeMs: number;
  eventTimeMs: number;
  title: string;
  eventType: string;
};

const API_BASE = (import.meta as any).env?.VITE_API_BASE || "";

async function getJson<T>(path: string): Promise<T> {
  const r = await fetch(`${API_BASE}${path}`);
  if (!r.ok) throw new Error(`HTTP ${r.status}`);
  return (await r.json()) as T;
}

const SINCE_OPTIONS = [
  { label: "最近 6 小时", value: 360 },
  { label: "最近 24 小时", value: 1440 },
  { label: "最近 3 天", value: 4320 },
  { label: "最近 7 天", value: 10080 },
];

function normalizeEventType(et: string): string {
  return String(et || "").trim().toLowerCase();
}

function isWhaleWallEventType(et: string): boolean {
  const n = normalizeEventType(et);
  return n === "whale_wall_far" || n === "anomaly_whale_wall_far" || n === "whale_wall_filled" || n === "whale_wall_canceled";
}

export default function AnomaliesPage() {
  const { Title, Text } = Typography;
  const [market, setMarket] = useState<Market>("swap");
  const [sinceMinutes, setSinceMinutes] = useState<number>(360);
  const [hotItems, setHotItems] = useState<HotItem[]>([]);
  const [whaleWallItems, setWhaleWallItems] = useState<HotItem[]>([]);
  const [absorptionItems, setAbsorptionItems] = useState<AbsorptionSignalItem[]>([]);
  const [includeShortBias, setIncludeShortBias] = useState(false);
  const [loading, setLoading] = useState(false);

  const refresh = async () => {
    setLoading(true);
    const safeGet = async <T,>(path: string, fallback: T): Promise<T> => {
      try {
        return await getJson<T>(path);
      } catch {
        return fallback;
      }
    };

    try {
      const direction = includeShortBias ? "all" : "long";
      const absorptionLookbackMinutes = Math.max(sinceMinutes, 3 * 24 * 60);
      const tzOffsetMin = new Date().getTimezoneOffset();

      const [hot, hotSpot, hotSwap, absorption] = await Promise.all([
        safeGet<{ items: HotItem[] }>(
          `/api/aggregate/hotMarkets?market=${market}&limit=200&sinceMinutes=${sinceMinutes}&timeMode=local&tzOffsetMin=${tzOffsetMin}`,
          { items: [] }
        ),
        safeGet<{ items: HotItem[] }>(
          `/api/aggregate/hotMarkets?market=spot&limit=200&sinceMinutes=${sinceMinutes}&timeMode=local&tzOffsetMin=${tzOffsetMin}`,
          { items: [] }
        ),
        safeGet<{ items: HotItem[] }>(
          `/api/aggregate/hotMarkets?market=swap&limit=200&sinceMinutes=${sinceMinutes}&timeMode=local&tzOffsetMin=${tzOffsetMin}`,
          { items: [] }
        ),
        safeGet<{ items: AbsorptionSignalItem[] }>(
          `/api/aggregate/orderbookAbsorptionSignals?market=${market}&limit=100&onlySignals=1&signalLookbackMinutes=${absorptionLookbackMinutes}&direction=${direction}`,
          { items: [] }
        ),
      ]);

      const normalizeHot = (arr: any[]): HotItem[] =>
        (arr || [])
          .map((e: any) => ({
            id: Number(e?.id ?? 0),
            market: String(e?.market ?? ""),
            symbol: String(e?.symbol ?? ""),
            eventType: String(e?.eventType ?? e?.event_type ?? ""),
            tfSignal: String(e?.tfSignal ?? e?.tf_signal ?? ""),
            tfLevel: e?.tfLevel ?? e?.tf_level ?? null,
            eventTimeMs: Number(e?.eventTimeMs ?? e?.event_time_ms ?? 0),
            title: String(e?.title ?? ""),
            details: e?.details,
            dailyAlertCount: Number(e?.dailyAlertCount ?? e?.daily_alert_count ?? 0),
          }))
          .filter((e: HotItem) => e.symbol && Number.isFinite(e.eventTimeMs) && e.eventTimeMs > 0);

      const normalizedHot = normalizeHot(hot.items || []);
      const normalizedWhaleSpot = normalizeHot(hotSpot.items || []);
      const normalizedWhaleSwap = normalizeHot(hotSwap.items || []);

      setHotItems(normalizedHot);
      setWhaleWallItems(
        [...normalizedWhaleSpot, ...normalizedWhaleSwap]
          .filter((e) => isWhaleWallEventType(e.eventType))
          .sort((a, b) => b.eventTimeMs - a.eventTimeMs)
      );
      setAbsorptionItems(absorption.items || []);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void refresh();
  }, [market, sinceMinutes, includeShortBias]);

  useEffect(() => {
    const timer = setInterval(() => {
      if (document.visibilityState === "visible") {
        void refresh();
      }
    }, 15_000);
    return () => clearInterval(timer);
  }, [market, sinceMinutes, includeShortBias]);

  const absorptionColumns = useMemo(
    () => [
      { title: "时间", render: (_: any, r: AbsorptionSignalItem) => new Date(r.ts).toLocaleString() },
      { title: "Symbol", render: (_: any, r: AbsorptionSignalItem) => <Link to={`/coin/${r.symbol}`}>{r.symbol}</Link> },
      {
        title: "状态",
        render: (_: any, r: AbsorptionSignalItem) => {
          const color = r.signalState === "STRONG" ? "red" : r.signalState === "CONFIRM" ? "orange" : r.signalState === "WATCH" ? "blue" : r.signalState === "FADING" ? "purple" : "gray";
          return <Tag color={color}>{r.signalState}</Tag>;
        },
      },
      {
        title: "方向",
        render: (_: any, r: AbsorptionSignalItem) => <Tag color={r.direction === "LONG_BIAS" ? "green" : "red"}>{r.direction}</Tag>,
      },
      { title: "分数", render: (_: any, r: AbsorptionSignalItem) => <span style={{ fontWeight: 700 }}>{r.score}</span> },
      {
        title: "窗口通过",
        render: (_: any, r: AbsorptionSignalItem) => {
          const passed4h = r.windows?.["4h"]?.passed ?? false;
          const passed1d = r.windows?.["1d"]?.passed ?? false;
          const passed3d = r.windows?.["3d"]?.passed ?? false;
          return (
            <Space wrap>
              <Tag color={passed4h ? "green" : "gray"}>4h</Tag>
              <Tag color={passed1d ? "green" : "gray"}>1d</Tag>
              <Tag color={passed3d ? "green" : "gray"}>3d</Tag>
            </Space>
          );
        },
      },
      { title: "依据", render: (_: any, r: AbsorptionSignalItem) => <span className="cm-muted">{(r.reasons || []).join("；")}</span> },
    ],
    []
  );

  const eventTypeLabel: Record<string, string> = {
    breakout_up: "突破",
    breakout_down: "跌破",
    volume_spike: "量能",
    amplitude_spike: "振幅",
    new_high_1d: "今日新高",
    new_high_7d: "7日新高",
    new_high_30d: "30日新高",
    new_low_1d: "今日新低",
    new_low_7d: "7日新低",
    new_low_30d: "30日新低",
    price_rise_small_5m: "5分钟小涨",
    price_rise_medium_5m: "5分钟中涨",
    price_rise_large_5m: "5分钟大涨",
    price_fall_small_5m: "5分钟小跌",
    price_fall_medium_5m: "5分钟中跌",
    price_fall_large_5m: "5分钟大跌",
    price_rise_small_2h: "2小时小涨",
    price_rise_medium_2h: "2小时中涨",
    price_rise_large_2h: "2小时大涨",
    price_fall_small_2h: "2小时小跌",
    price_fall_medium_2h: "2小时中跌",
    price_fall_large_2h: "2小时大跌",
    intraday_peak_reversal: "冲高回落",
    intraday_bottom_rebound: "探底回升",
    volume_rise_small_15m: "放量小涨",
    volume_rise_medium_15m: "放量中涨",
    volume_rise_large_15m: "放量大涨",
    volume_fall_small_15m: "放量小跌",
    volume_fall_medium_15m: "放量中跌",
    volume_fall_large_15m: "放量大跌",
    signal_lab_persistent_buy: "持续吸筹",
    signal_lab_bid_wall: "买盘墙",
    signal_lab_ask_wall: "卖盘墙",
    whale_wall_far: "类型提醒",
    anomaly_whale_wall_far: "类型提醒",
    ANOMALY_WHALE_WALL_FAR: "类型提醒",
    whale_wall_filled: "大挂单已成交",
    whale_wall_canceled: "大挂单已撤销",
  };

  const getEventTypeLabel = (eventType: string): string => {
    const t = String(eventType || "");
    const n = normalizeEventType(t);
    return eventTypeLabel[t] || eventTypeLabel[n] || t;
  };

  const whaleWallGroupedItems = useMemo<WhaleWallGroupedItem[]>(() => {
    const groups = new Map<string, WhaleWallGroupedItem>();
    for (const r of whaleWallItems) {
      const details = r.details && typeof r.details === "object" ? r.details : {};
      const side = String(details.side ?? "").toLowerCase();
      const wallPriceRaw = Number(details.wallPrice ?? details.wall_price ?? 0);
      const wallPrice = Number.isFinite(wallPriceRaw) ? wallPriceRaw : 0;
      const mk = String(r.market || "spot").toLowerCase();
      const et = normalizeEventType(r.eventType);
      const status: WhaleWallGroupedItem["status"] = et === "whale_wall_filled" ? "FILLED" : et === "whale_wall_canceled" ? "CANCELED" : "ACTIVE";
      const key = `${mk}|${r.symbol}|${side || "na"}|${wallPrice > 0 ? wallPrice.toFixed(6) : "na"}`;

      const existed = groups.get(key);
      if (!existed) {
        groups.set(key, {
          key,
          market: mk,
          symbol: r.symbol,
          side: side || "-",
          wallPrice,
          status,
          count: 1,
          firstTimeMs: r.eventTimeMs,
          eventTimeMs: r.eventTimeMs,
          title: r.title || "-",
          eventType: r.eventType,
        });
        continue;
      }

      existed.count += 1;
      existed.firstTimeMs = Math.min(existed.firstTimeMs, r.eventTimeMs);
      if (r.eventTimeMs >= existed.eventTimeMs) {
        existed.eventTimeMs = r.eventTimeMs;
        existed.status = status;
        existed.title = r.title || existed.title;
        existed.eventType = r.eventType;
        if (side) existed.side = side;
        if (wallPrice > 0) existed.wallPrice = wallPrice;
      }
    }
    return Array.from(groups.values()).sort((a, b) => b.eventTimeMs - a.eventTimeMs);
  }, [whaleWallItems]);

  const hotColumns = useMemo(
    () => [
      { title: "时间", width: 180, render: (_: any, r: HotItem) => new Date(r.eventTimeMs).toLocaleString() },
      { title: "Symbol", width: 120, render: (_: any, r: HotItem) => <Link to={`/coin/${r.symbol}`}>{r.symbol}</Link> },
      { title: "类型", width: 140, render: (_: any, r: HotItem) => <Tag color="orange">{getEventTypeLabel(r.eventType)}</Tag> },
      { title: "标题", render: (_: any, r: HotItem) => r.title || "-" },
      { title: "信号", width: 100, render: (_: any, r: HotItem) => r.tfSignal || "-" },
      { title: "当日次数", width: 110, render: (_: any, r: HotItem) => Number(r.dailyAlertCount || 0) },
      { title: "水平", width: 100, render: (_: any, r: HotItem) => r.tfLevel || "-" },
    ],
    []
  );

  const whaleWallColumns = useMemo(
    () => [
      { title: "最新", width: 180, render: (_: any, r: WhaleWallGroupedItem) => new Date(r.eventTimeMs).toLocaleString() },
      { title: "首次", width: 180, render: (_: any, r: WhaleWallGroupedItem) => new Date(r.firstTimeMs).toLocaleString() },
      { title: "Symbol", width: 120, render: (_: any, r: WhaleWallGroupedItem) => <Link to={`/coin/${r.symbol}`}>{r.symbol}</Link> },
      { title: "市场", width: 90, render: (_: any, r: WhaleWallGroupedItem) => r.market.toUpperCase() },
      {
        title: "状态",
        width: 100,
        render: (_: any, r: WhaleWallGroupedItem) => {
          const color = r.status === "FILLED" ? "green" : r.status === "CANCELED" ? "gray" : "red";
          return <Tag color={color}>{r.status}</Tag>;
        },
      },
      { title: "次数", width: 80, render: (_: any, r: WhaleWallGroupedItem) => r.count },
      { title: "方向/墙价", width: 160, render: (_: any, r: WhaleWallGroupedItem) => `${r.side.toUpperCase()} / ${r.wallPrice > 0 ? r.wallPrice : "-"}` },
      { title: "标题", render: (_: any, r: WhaleWallGroupedItem) => r.title || getEventTypeLabel(r.eventType) },
    ],
    []
  );

  return (
    <div className="cm-page">
      <div className="cm-section">
        <div className="cm-sectionHeader">
          <Title heading={5} style={{ margin: 0 }}>
            市场异动 / 信号扫描
          </Title>
          <Space>
            <Select value={market} onChange={(v) => setMarket(v as Market)} style={{ width: 120 }}>
              <Select.Option value="swap">合约 (swap)</Select.Option>
              <Select.Option value="spot">现货 (spot)</Select.Option>
            </Select>
            <Select value={sinceMinutes} onChange={(v) => setSinceMinutes(v as number)} style={{ width: 140 }}>
              {SINCE_OPTIONS.map((o) => (
                <Select.Option key={o.value} value={o.value}>
                  {o.label}
                </Select.Option>
              ))}
            </Select>
            <Button loading={loading} onClick={refresh} type="primary">
              刷新
            </Button>
          </Space>
        </div>
      </div>

      <div className="cm-section">
        <div className="cm-card" style={{ padding: 12, marginBottom: 12 }}>
          <div className="cm-sectionHeader">
            <Title heading={6} style={{ margin: 0 }}>
              类型提醒
            </Title>
            <Text className="cm-muted">whale_wall_far / whale_wall_filled / whale_wall_canceled</Text>
          </div>
          <div className="cm-table" style={{ marginBottom: 12 }}>
            <Table
              rowKey={(r) => (r as WhaleWallGroupedItem).key}
              loading={loading}
              pagination={false}
              size="small"
              columns={whaleWallColumns as any}
              data={whaleWallGroupedItems}
              scroll={{ x: "max-content", y: 240 }}
            />
          </div>
        </div>

        <div className="cm-card" style={{ padding: 12, marginBottom: 12 }}>
          <div className="cm-sectionHeader">
            <Title heading={6} style={{ margin: 0 }}>
              市场异动
            </Title>
            <Text className="cm-muted">来源：/api/aggregate/hotMarkets</Text>
          </div>
          <div className="cm-table" style={{ marginBottom: 12 }}>
            <Table
              rowKey={(r) => `${(r as HotItem).symbol}-${(r as HotItem).eventType}-${(r as HotItem).eventTimeMs}`}
              loading={loading}
              pagination={false}
              size="small"
              columns={hotColumns as any}
              data={hotItems}
              scroll={{ x: "max-content", y: 360 }}
            />
          </div>
        </div>

        <div className="cm-card" style={{ padding: 12 }}>
          <div className="cm-sectionHeader">
            <Title heading={6} style={{ margin: 0 }}>
              吸筹信号扫描（全币）
            </Title>
            <Space>
              <Checkbox checked={includeShortBias} onChange={setIncludeShortBias}>
                包含 SHORT_BIAS（派发）
              </Checkbox>
              <Text className="cm-muted">默认仅 LONG_BIAS（已触发信号）</Text>
            </Space>
          </div>
          <div className="cm-table" style={{ marginBottom: 12 }}>
            <Table
              rowKey={(r) => `${(r as AbsorptionSignalItem).symbol}-${(r as AbsorptionSignalItem).ts}`}
              loading={loading}
              pagination={false}
              size="small"
              columns={absorptionColumns as any}
              data={absorptionItems}
              scroll={{ x: "max-content" }}
            />
          </div>
        </div>
      </div>
    </div>
  );
}

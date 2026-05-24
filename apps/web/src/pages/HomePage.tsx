import { useEffect, useMemo, useRef, useState } from "react";
import {
  AutoComplete,
  Button,
  Input,
  Message,
  Select,
  Skeleton,
  Space,
  Table,
  Tag,
  Typography,
} from "@arco-design/web-react";
import { Link } from "react-router-dom";
import EChart from "../components/EChart";
import QuantHelp from "../components/QuantHelp";

type Market = "spot" | "swap";

type BasicItem = {
  symbol: string;
  lastPrice: number;
  priceChangePercent: number;
  quoteVolume: number;
};

type BasicInfo = {
  market: Market;
  gainers: BasicItem[];
  losers: BasicItem[];
};

type ReturnItem = {
  symbol: string;
  open: number;
  close: number;
  high: number | null;
  low: number | null;
  returnPct: number;
  amplitudePct: number | null;
  quoteNotional: number;
  tradeCount: number;
};

type ReturnsResp = {
  market: Market;
  bucket: "15m" | "1h" | "4h" | "1d";
  bucketStartMs: number | null;
  bucketEndMs: number | null;
  gainers: ReturnItem[];
  losers: ReturnItem[];
};

type HotItem = {
  id: number;
  symbol: string;
  eventType: string;
  tfSignal: string;
  tfLevel: string | null;
  eventTimeMs: number;
  title: string;
  details: any;
};

type FavoritesResp = {
  clientId: string;
  items: Array<{ market: Market; symbol: string; createdAt: string }>;
};

type CoinBasicResp = {
  basic: {
    symbol: string;
    lastPrice: number | null;
    priceChangePercent24h: number | null;
  };
};

type PairResp = {
  market: Market;
  pairs: string[];
};

type RankInterval = "24h" | "4h" | "1h" | "15m";
type RankKind = "openinterest" | "oicapratio" | "fi1d" | "fo1d" | "si1d" | "so1d";

type RankItem = {
  rank: number;
  symbol: string;
  changePct: number;
  oiNotionalUsd: number;
  netUsd: number;
  biasPct: number;
  ratioPct: number;
  marketCapUsd: number;
};

type RankResp = {
  kind: RankKind;
  title: string;
  interval?: RankInterval;
  limit: number;
  page: number;
  pageSize: number;
  total: number;
  totalPages: number;
  asOfMs: number;
  items: RankItem[];
};

type HomeTabKey = "overview" | "quant";
type OverviewSectionKey = "movers24h" | "ret15m" | "ret1h";

type QuantItem = {
  ts: number;
  o: number;
  h: number;
  l: number;
  c: number;
  vol_buy: number;
  vol_sell: number;
  vol_total: number;
  delta: number;
  cvd: number;
  vwap: number | null;
  count: number;
};

type QuantDashboardResp = {
  symbol: string;
  market: Market;
  requestedMarket: Market;
  marketFallback: boolean;
  bucket?: string;
  items: QuantItem[];
};

type QuantBucket = "15m" | "1h" | "4h" | "1d";
type HeatmapBucket = "1m" | "5m" | "15m";
type CvdMode = "visible" | "rolling24h" | "session";
type QuantWsBucket = QuantBucket;

type SpotHeatmapResp = {
  symbol: string;
  market: "spot";
  interval: QuantBucket;
  heatmapInterval?: HeatmapBucket;
  limit: number;
  step: number | null;
  minPrice: number | null;
  maxPrice: number | null;
  yCount: number;
  data: Array<[number, number, number]>;
  maxIntensity: number;
};

type QuantMlfSideMetric = {
  market: Market;
  available: boolean;
  closePrice: number | null;
  wallStrengthRatio: number | null;
  buyWallUsd: number;
  sellWallUsd: number;
  cvdDelta: number;
  cvdNorm: number;
  bucketCount: number;
};

type QuantMlfCompareResp = {
  symbol: string;
  bucket: QuantBucket;
  lookbackHours: number;
  dominanceThreshold: number;
  spot: QuantMlfSideMetric;
  swap: QuantMlfSideMetric;
  spotDominanceFactor: number | null;
  wallStrengthRatioDiff: number | null;
  dominanceState: "SPOT_DOMINANT" | "SWAP_DOMINANT" | "NEUTRAL" | "UNKNOWN";
  ts: number;
};

const QUANT_BUCKET_OPTIONS: Array<{ value: QuantBucket; label: string }> = [
  { value: "15m", label: "15m" },
  { value: "1h", label: "1h" },
  { value: "4h", label: "4h" },
  { value: "1d", label: "1D" },
];

const CVD_MODE_OPTIONS: Array<{ value: CvdMode; label: string }> = [
  { value: "visible", label: "Visible" },
  { value: "rolling24h", label: "Rolling 24H" },
  { value: "session", label: "Session" },
];

const RANK_KIND_OPTIONS: Array<{ value: RankKind; label: string }> = [
  { value: "openinterest", label: "OI变化排行" },
  { value: "oicapratio", label: "持仓/市值变化排行" },
  { value: "fi1d", label: "合约净流入" },
  { value: "fo1d", label: "合约净流出" },
  { value: "si1d", label: "现货净流入" },
  { value: "so1d", label: "现货净流出" },
];

const RANK_INTERVAL_OPTIONS: Array<{ value: RankInterval; label: string }> = [
  { value: "24h", label: "24h" },
  { value: "4h", label: "4h" },
  { value: "1h", label: "1h" },
  { value: "15m", label: "15m" },
];

const QUANT_MAX_POINTS = 240;

function resolveHeatmapInterval(bucket: QuantBucket): HeatmapBucket {
  if (bucket === "15m" || bucket === "1h") return "1m";
  if (bucket === "4h") return "5m";
  return "15m";
}

function getSessionAnchorUtcMs(ts: number): number {
  const d = new Date(ts);
  const day = d.getUTCDay();
  const diff = (day + 6) % 7;
  d.setUTCHours(0, 0, 0, 0);
  d.setUTCDate(d.getUTCDate() - diff);
  return d.getTime();
}

function quantBucketMs(bucket: QuantWsBucket): number {
  if (bucket === "15m") return 15 * 60 * 1000;
  if (bucket === "1h") return 60 * 60 * 1000;
  if (bucket === "4h") return 4 * 60 * 60 * 1000;
  return 24 * 60 * 60 * 1000;
}

function normalizeWsSymbol(symbol: string) {
  return symbol.trim().toLowerCase();
}

function getBinanceKlineWsUrl(market: Market, symbol: string, bucket: QuantWsBucket) {
  const stream = `${normalizeWsSymbol(symbol)}@kline_${bucket}`;
  if (market === "spot") {
    return `wss://stream.binance.com:9443/ws/${stream}`;
  }
  return `wss://fstream.binance.com/ws/${stream}`;
}

function getVwapLookback(bucket: QuantWsBucket): number {
  const minutes = Math.max(1, Math.floor(quantBucketMs(bucket) / 60_000));
  return Math.max(6, Math.min(240, Math.floor((24 * 60) / minutes)));
}

function rebuildQuantDerived(items: QuantItem[], bucket: QuantWsBucket): QuantItem[] {
  if (!items.length) return [];
  const lookback = getVwapLookback(bucket);
  let cvdAcc = 0;

  return items.map((item, idx) => {
    const startIdx = Math.max(0, idx - lookback + 1);
    let sumCv = 0;
    let sumV = 0;
    for (let i = startIdx; i <= idx; i += 1) {
      const c = items[i].c;
      const v = items[i].vol_total;
      if (Number.isFinite(c) && Number.isFinite(v) && v > 0) {
        sumCv += c * v;
        sumV += v;
      }
    }

    cvdAcc += item.delta;
    return {
      ...item,
      vwap: sumV > 0 ? sumCv / sumV : null,
      cvd: cvdAcc,
    };
  });
}

function buildQuantItemFromKline(kline: any): QuantItem | null {
  if (!kline || typeof kline !== "object") return null;
  const ts = Number(kline.t);
  const o = Number(kline.o);
  const h = Number(kline.h);
  const l = Number(kline.l);
  const c = Number(kline.c);
  const count = Number(kline.n || 0);
  if (![ts, o, h, l, c].every((n) => Number.isFinite(n))) return null;

  const baseVolRaw = Number(kline.v);
  const quoteVolRaw = Number(kline.q);
  const takerBuyQuoteRaw = Number(kline.Q);
  const baseVol = Number.isFinite(baseVolRaw) ? Math.max(0, baseVolRaw) : 0;
  const volTotal = Number.isFinite(quoteVolRaw) && quoteVolRaw > 0 ? quoteVolRaw : baseVol * c;
  const volBuy = Number.isFinite(takerBuyQuoteRaw) && takerBuyQuoteRaw >= 0 ? takerBuyQuoteRaw : 0;
  const volSell = Math.max(0, volTotal - volBuy);

  return {
    ts,
    o,
    h,
    l,
    c,
    vol_buy: volBuy,
    vol_sell: volSell,
    vol_total: volTotal,
    delta: volBuy - volSell,
    cvd: 0,
    vwap: null,
    count: Number.isFinite(count) ? count : 0,
  };
}

const API_BASE = (import.meta as any).env?.VITE_API_BASE || "";

async function getJson<T>(path: string): Promise<T> {
  const r = await fetch(`${API_BASE}${path}`);
  if (!r.ok) throw new Error(`HTTP ${r.status}`);
  return (await r.json()) as T;
}

async function postJson<T>(path: string, body: any): Promise<T> {
  const r = await fetch(`${API_BASE}${path}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!r.ok) throw new Error(`HTTP ${r.status}`);
  return (await r.json()) as T;
}

async function deleteReq(path: string): Promise<void> {
  const r = await fetch(`${API_BASE}${path}`, { method: "DELETE" });
  if (!r.ok) throw new Error(`HTTP ${r.status}`);
}

const usdCompact = new Intl.NumberFormat("en-US", {
  notation: "compact",
  maximumFractionDigits: 2,
});

function formatPrice(v: number | null) {
  if (!Number.isFinite(v as number)) return "-";
  const value = v as number;
  const abs = Math.abs(value);
  if (abs === 0) return "0";
  if (abs < 0.01) return value.toFixed(8);
  if (abs < 1) return value.toFixed(6);
  if (abs < 1000) return value.toFixed(2);
  return value.toFixed(0);
}

function formatPct(v: number | null, digits = 2) {
  if (v == null || !Number.isFinite(v)) return "-";
  const s = (v * 100).toFixed(digits);
  return `${v >= 0 ? "+" : ""}${s}%`;
}

function formatNumber(v: number | null | undefined, digits = 2) {
  if (v == null || !Number.isFinite(v)) return "-";
  return Number(v).toLocaleString("en-US", { maximumFractionDigits: digits });
}

function formatSigned(v: number | null | undefined, digits = 2) {
  if (v == null || !Number.isFinite(v)) return "-";
  const n = Number(v);
  return `${n >= 0 ? "+" : ""}${n.toLocaleString("en-US", { maximumFractionDigits: digits })}`;
}

function formatCompact(v: number | null | undefined, digits = 2) {
  if (v == null || !Number.isFinite(v)) return "-";
  return new Intl.NumberFormat("en-US", {
    notation: "compact",
    maximumFractionDigits: digits,
  }).format(Number(v));
}

function formatCompactSigned(v: number | null | undefined, digits = 2) {
  if (v == null || !Number.isFinite(v)) return "-";
  const n = Number(v);
  return `${n >= 0 ? "+" : ""}${formatCompact(n, digits)}`;
}

function formatRatio(v: number | null | undefined, digits = 2) {
  if (v == null || !Number.isFinite(v)) return "-";
  return Number(v).toFixed(digits);
}

function getClientId(): string {
  const key = "coinmark_client_id";
  const existed = localStorage.getItem(key);
  if (existed) return existed;
  const gen =
    (globalThis.crypto?.randomUUID?.() as string | undefined) ||
    `cm_${Date.now()}_${Math.random().toString(16).slice(2, 10)}`;
  localStorage.setItem(key, gen);
  return gen;
}

export default function HomePage() {
  const { Title, Text } = Typography;
  const [market, setMarket] = useState<Market>("swap");
  const [activeHomeTab, setActiveHomeTab] = useState<HomeTabKey>("overview");
  const [quantBucket, setQuantBucket] = useState<QuantBucket>("1h");
  const [cvdMode, setCvdMode] = useState<CvdMode>("visible");
  const [quantVisibleAnchorTs, setQuantVisibleAnchorTs] = useState<number | null>(null);
  const [quantSymbol, setQuantSymbol] = useState("BTCUSDT");
  const [quantData, setQuantData] = useState<QuantDashboardResp | null>(null);
  const [quantRealtimeItems, setQuantRealtimeItems] = useState<QuantItem[]>([]);
  const [spotHeatmap, setSpotHeatmap] = useState<SpotHeatmapResp | null>(null);
  const [quantMlfCompare, setQuantMlfCompare] = useState<QuantMlfCompareResp | null>(null);
  const [quantLoading, setQuantLoading] = useState(false);
  const [loading, setLoading] = useState(false);
  const [basic, setBasic] = useState<BasicInfo | null>(null);
  const [ret15m, setRet15m] = useState<ReturnsResp | null>(null);
  const [ret1h, setRet1h] = useState<ReturnsResp | null>(null);
  const [hot, setHot] = useState<HotItem[]>([]);
  const [favorites, setFavorites] = useState<FavoritesResp | null>(null);
  const [favDetails, setFavDetails] = useState<Record<string, CoinBasicResp>>({});
  const [rankKind, setRankKind] = useState<RankKind>("openinterest");
  const [rankInterval, setRankInterval] = useState<RankInterval>("24h");
  const [rankResp, setRankResp] = useState<RankResp | null>(null);
  const [rankLoading, setRankLoading] = useState(false);
  const [rankUpdatedMs, setRankUpdatedMs] = useState<number | null>(null);
  const [lastUpdatedMs, setLastUpdatedMs] = useState<number | null>(null);
  const [quantUpdatedMs, setQuantUpdatedMs] = useState<number | null>(null);
  const [favInput, setFavInput] = useState("");
  const [pairPool, setPairPool] = useState<string[]>([]);
  const [pairPoolByMarket, setPairPoolByMarket] = useState<Record<Market, string[]>>({ spot: [], swap: [] });
  const [pairPoolLoading, setPairPoolLoading] = useState(false);
  const [overviewCollapsed, setOverviewCollapsed] = useState<Record<OverviewSectionKey, boolean>>({
    movers24h: false,
    ret15m: true,
    ret1h: false,
  });
  const refreshSeq = useRef(0);
  const quantMainDataZoomRef = useRef<{ startValue?: number; endValue?: number } | null>(null);
  const quantRealtimeItemsRef = useRef<QuantItem[]>([]);

  const loadQuant = async (symbolOverride?: string) => {
    const targetSymbol = (symbolOverride ?? quantSymbol).trim().toUpperCase();
    if (!targetSymbol) return;

    setQuantLoading(true);
    try {
      const heatmapInterval = resolveHeatmapInterval(quantBucket);
      const [d, heat, compare] = await Promise.all([
        getJson<QuantDashboardResp>(
          `/api/coin/detail/quant-dashboard?symbol=${targetSymbol}&market=${market}&bucket=${quantBucket}&limit=240`
        ),
        getJson<SpotHeatmapResp>(
          `/api/coin/detail/orderbook/spot-heatmap?symbol=${targetSymbol}&interval=${quantBucket}&heatmap_interval=${heatmapInterval}&limit=240`
        ),
        getJson<QuantMlfCompareResp>(
          `/api/coin/detail/quant-mlf-compare?symbol=${targetSymbol}&bucket=${quantBucket}&lookbackHours=24&dominanceThreshold=0`
        ),
      ]);
      setQuantSymbol(targetSymbol);
      setQuantData(d);
      const derived = rebuildQuantDerived(d.items || [], quantBucket);
      quantRealtimeItemsRef.current = derived;
      setQuantRealtimeItems(derived);
      setSpotHeatmap(heat);
      setQuantMlfCompare(compare);
      setQuantUpdatedMs(Date.now());
    } catch (e: any) {
      Message.error(`量化数据拉取失败：${e?.message || e}`);
    } finally {
      setQuantLoading(false);
    }
  };

  const refreshOverview = async () => {
    const seq = ++refreshSeq.current;
    setLoading(true);
    try {
      const cid = getClientId();
      const [bi, r15, r1, hm, fav] = await Promise.all([
        getJson<BasicInfo>(`/api/aggregate/basicinfo?market=${market}&limit=30`),
        getJson<ReturnsResp>(`/api/aggregate/returns?market=${market}&bucket=15m&limit=20`),
        getJson<ReturnsResp>(`/api/aggregate/returns?market=${market}&bucket=1h&limit=20`),
        getJson<{ items: HotItem[] }>(`/api/aggregate/hotMarkets?market=${market}&limit=20`),
        getJson<FavoritesResp>(`/api/user/favorites?clientId=${cid}&market=${market}`),
      ]);
      if (seq !== refreshSeq.current) return;
      setBasic(bi);
      setRet15m(r15);
      setRet1h(r1);
      const hotItems: HotItem[] = (hm.items || [])
        .map((e: any) => ({
          id: Number(e?.id ?? 0),
          symbol: String(e?.symbol ?? ""),
          eventType: String(e?.eventType ?? e?.event_type ?? ""),
          tfSignal: String(e?.tfSignal ?? e?.tf_signal ?? ""),
          tfLevel: e?.tfLevel ?? e?.tf_level ?? null,
          eventTimeMs: Number(e?.eventTimeMs ?? e?.event_time_ms ?? 0),
          title: String(e?.title ?? ""),
          details: e?.details,
        }))
        .filter((e) => e.symbol && Number.isFinite(e.eventTimeMs) && e.eventTimeMs > 0);
      setHot(hotItems);
      setFavorites(fav);
      setLastUpdatedMs(Date.now());

      const favSyms = (fav.items || []).map((x) => x.symbol).slice(0, 20);
      if (favSyms.length > 0) {
        const detailEntries = await Promise.all(
          favSyms.map(async (s) => {
            try {
              const d = await getJson<CoinBasicResp>(`/api/coin/detail/basic?market=${market}&symbol=${s}`);
              return [s, d] as const;
            } catch {
              return [s, { basic: { symbol: s, lastPrice: null, priceChangePercent24h: null } }] as const;
            }
          })
        );
        if (seq !== refreshSeq.current) return;
        setFavDetails(Object.fromEntries(detailEntries));
      } else {
        setFavDetails({});
      }
    } catch (e: any) {
      Message.error(`拉取失败：${e?.message || e}`);
    } finally {
      setLoading(false);
    }
  };

  const refreshRankCenter = async () => {
    setRankLoading(true);
    try {
      const d = await getJson<RankResp>(
        `/api/tg/rank?kind=${rankKind}&interval=${rankInterval}&limit=30&page=1`
      );
      setRankResp(d);
      setRankUpdatedMs(Date.now());
    } catch {
      setRankResp(null);
    } finally {
      setRankLoading(false);
    }
  };

  useEffect(() => {
    if (activeHomeTab !== "overview") return;
    refreshOverview();
    refreshRankCenter();
    const t = setInterval(() => {
      refreshOverview();
      refreshRankCenter();
    }, 15000);
    return () => clearInterval(t);
  }, [activeHomeTab, market, rankKind, rankInterval]);

  useEffect(() => {
    let stopped = false;
    const loadPairPool = async () => {
      setPairPoolLoading(true);
      try {
        const [spotResp, swapResp] = await Promise.all([
          getJson<PairResp>(`/api/symbol/getpairs?market=spot`),
          getJson<PairResp>(`/api/symbol/getpairs?market=swap`),
        ]);
        if (stopped) return;
        const spotPairs = (spotResp.pairs || []).map((s) => String(s || "").toUpperCase()).filter(Boolean);
        const swapPairs = (swapResp.pairs || []).map((s) => String(s || "").toUpperCase()).filter(Boolean);
        const merged = Array.from(new Set([...spotPairs, ...swapPairs])).sort((a, b) => a.localeCompare(b));
        setPairPoolByMarket({ spot: spotPairs, swap: swapPairs });
        setPairPool(merged);
      } catch {
        if (stopped) return;
        setPairPoolByMarket({ spot: [], swap: [] });
        setPairPool([]);
      } finally {
        if (!stopped) setPairPoolLoading(false);
      }
    };
    loadPairPool();
    return () => {
      stopped = true;
    };
  }, []);

  useEffect(() => {
    if (activeHomeTab !== "quant") return;
    loadQuant();
  }, [activeHomeTab, market, quantBucket]);

  useEffect(() => {
    if (activeHomeTab !== "quant") return;
    const sym = (quantSymbol || "").trim().toUpperCase();
    if (!sym) return;

    let ws: WebSocket | null = null;
    let reconnectTimer: number | null = null;
    let attempt = 0;
    let stopped = false;

    const connect = () => {
      if (stopped) return;
      const url = getBinanceKlineWsUrl(market, sym, quantBucket);
      ws = new WebSocket(url);

      ws.onopen = () => {
        attempt = 0;
      };

      ws.onmessage = (event) => {
        try {
          const payload = JSON.parse(String(event.data || "{}"));
          const kline = payload?.k || payload?.data?.k;
          const patch = buildQuantItemFromKline(kline);
          if (!patch) return;

          setQuantRealtimeItems((prev) => {
            const base = prev.length ? prev : quantRealtimeItemsRef.current;
            if (!base.length) return prev;

            const last = base[base.length - 1];
            if (patch.ts < last.ts) return prev;

            let next: QuantItem[];
            if (patch.ts === last.ts) {
              next = [...base.slice(0, -1), { ...last, ...patch }];
            } else {
              next = [...base, patch];
              if (next.length > QUANT_MAX_POINTS) {
                next = next.slice(next.length - QUANT_MAX_POINTS);
              }
            }

            const derived = rebuildQuantDerived(next, quantBucket);
            quantRealtimeItemsRef.current = derived;
            return derived;
          });
        } catch {
          // ignore malformed payload
        }
      };

      ws.onclose = () => {
        if (stopped) return;
        attempt += 1;
        const delay = Math.min(10_000, 1000 * 2 ** Math.min(attempt, 4)) + Math.floor(Math.random() * 300);
        reconnectTimer = window.setTimeout(connect, delay);
      };

      ws.onerror = () => {
        try {
          ws?.close();
        } catch {
          // no-op
        }
      };
    };

    connect();

    return () => {
      stopped = true;
      if (reconnectTimer != null) {
        window.clearTimeout(reconnectTimer);
      }
      try {
        ws?.close();
      } catch {
        // no-op
      }
    };
  }, [activeHomeTab, market, quantSymbol, quantBucket]);

  const basicColumns = useMemo(
    () => [
      {
        title: "Symbol",
        dataIndex: "symbol",
        render: (_: any, r: BasicItem) => <Link to={`/coin/${r.symbol}`}>{r.symbol}</Link>,
      },
      {
        title: "价格",
        render: (_: any, r: BasicItem) => <span className="cm-number--mono">{formatPrice(r.lastPrice)}</span>,
      },
      {
        title: "24h",
        render: (_: any, r: BasicItem) => {
          const v = r.priceChangePercent / 100;
          const cls = v >= 0 ? "cm-number--pos" : "cm-number--neg";
          return <span className={cls}>{formatPct(v, 2)}</span>;
        },
      },
      {
        title: "成交额",
        render: (_: any, r: BasicItem) => <span className="cm-number--mono">{usdCompact.format(r.quoteVolume)}</span>,
      },
    ],
    []
  );

  const returnColumns = useMemo(
    () => [
      {
        title: "Symbol",
        dataIndex: "symbol",
        render: (_: any, r: ReturnItem) => <Link to={`/coin/${r.symbol}`}>{r.symbol}</Link>,
      },
      {
        title: "涨跌幅",
        render: (_: any, r: ReturnItem) => {
          const ret = (r as any).returnPct ?? ((r as any).retPct != null ? (r as any).retPct / 100 : null);
          const cls = (ret ?? 0) >= 0 ? "cm-number--pos" : "cm-number--neg";
          return <span className={cls}>{formatPct(ret, 2)}</span>;
        },
      },
      {
        title: "振幅",
        render: (_: any, r: ReturnItem) => <span className="cm-number--mono">{formatPct(r.amplitudePct, 2)}</span>,
      },
      {
        title: "成交额",
        render: (_: any, r: ReturnItem) => <span className="cm-number--mono">{usdCompact.format(r.quoteNotional)}</span>,
      },
    ],
    []
  );

  const favoriteSearchCandidates = useMemo(() => {
    const keyword = favInput.trim().toUpperCase();
    if (!keyword) return pairPool.slice(0, 30);
    const inMarket = pairPoolByMarket[market] || [];
    const startsInMarket = inMarket.filter((s) => s.startsWith(keyword));
    const containsInMarket = inMarket.filter((s) => !s.startsWith(keyword) && s.includes(keyword));
    const startsAll = pairPool.filter((s) => s.startsWith(keyword) && !startsInMarket.includes(s));
    const containsAll = pairPool.filter(
      (s) => !s.startsWith(keyword) && s.includes(keyword) && !containsInMarket.includes(s)
    );
    return [...startsInMarket, ...containsInMarket, ...startsAll, ...containsAll].slice(0, 40);
  }, [favInput, market, pairPool, pairPoolByMarket]);

  const resolveFavoriteSymbol = (raw: string) => {
    const keyword = (raw || "").trim().toUpperCase();
    if (!keyword) return "";
    if (pairPool.includes(keyword)) return keyword;
    const options = favoriteSearchCandidates;
    if (!options.length) return keyword;
    return options[0];
  };

  const handleAddFavorite = async (symbolOverride?: string) => {
    const sym = resolveFavoriteSymbol(symbolOverride ?? favInput);
    if (!sym) return;
    try {
      const cid = getClientId();
      await postJson(`/api/user/favorites?clientId=${cid}`, { market, symbols: [sym] });
      setFavInput("");
      refreshOverview();
    } catch (e: any) {
      Message.error(`收藏失败：${e?.message || e}`);
    }
  };

  const handleRemoveFavorite = async (sym: string) => {
    try {
      const cid = getClientId();
      await deleteReq(`/api/user/favorites/${sym}?clientId=${cid}&market=${market}`);
      refreshOverview();
    } catch (e: any) {
      Message.error(`取消收藏失败：${e?.message || e}`);
    }
  };

  const quantLatest = useMemo(() => {
    const items = quantRealtimeItems;
    if (!items.length) return null;
    return items[items.length - 1];
  }, [quantRealtimeItems]);

  const quantCvdSeriesData = useMemo(() => {
    const items = quantRealtimeItems;
    if (!items.length) return [] as Array<[number, number]>;

    const lastTs = items[items.length - 1]?.ts ?? 0;
    const fallbackVisibleTs = items[Math.max(0, Math.floor((items.length - 1) * 0.4))]?.ts ?? items[0].ts;
    const anchorTs =
      cvdMode === "rolling24h"
        ? lastTs - 24 * 60 * 60 * 1000
        : cvdMode === "session"
          ? getSessionAnchorUtcMs(lastTs)
          : (quantVisibleAnchorTs ?? fallbackVisibleTs);

    let acc = 0;
    const out: Array<[number, number]> = [];
    for (const item of items) {
      if (item.ts < anchorTs) {
        continue;
      }
      acc += item.delta;
      out.push([item.ts, acc]);
    }
    return out;
  }, [quantRealtimeItems, cvdMode, quantVisibleAnchorTs]);

  const quantMainOption = useMemo(() => {
    const candles = quantRealtimeItems;
    if (!candles.length) return null;

    const x = candles.map((item) => item.ts);

    const candleData = candles.map((item) => [item.ts, item.o, item.c, item.l, item.h]);
    const deltaData = candles.map((item) => [item.ts, item.delta]);
    const cvdData = quantCvdSeriesData;

    const allPrices = candles.flatMap((item) => [item.l, item.h]);
    const yMin = Math.min(...allPrices) * 0.995;
    const yMax = Math.max(...allPrices) * 1.005;

    const heatRaw = spotHeatmap?.data || [];

    const heatPoints: Array<[number, number, number]> = [];
    const intensityValues: number[] = [];

    for (const item of heatRaw) {
      const ts = item[0];
      const price = item[1];
      const intensity = item[2];
      if (!Number.isFinite(ts) || !Number.isFinite(price) || !Number.isFinite(intensity)) continue;
      heatPoints.push([ts, price, intensity]);
      intensityValues.push(intensity);
    }

    intensityValues.sort((a, b) => a - b);
    const p95 = intensityValues.length > 0 ? intensityValues[Math.max(0, Math.floor(intensityValues.length * 0.95) - 1)] : 1;
    const visualMax = Math.max(1, p95);

    return {
      animation: false,
      grid: [
        { left: 60, right: 60, top: 30, height: "64%" },
        { left: 60, right: 60, top: "76%", height: "18%" },
      ],
      axisPointer: {
        link: [{ xAxisIndex: [0, 1] }],
        label: { backgroundColor: "#475569" },
      },
      tooltip: {
        trigger: "axis",
        axisPointer: { type: "cross" },
      },
      xAxis: [
        {
          type: "time",
          gridIndex: 0,
          boundaryGap: true,
          axisLabel: { color: "#94a3b8" },
        },
        {
          type: "time",
          gridIndex: 1,
          boundaryGap: true,
          axisLabel: { color: "#94a3b8" },
        },
      ],
      yAxis: [
        {
          type: "value",
          gridIndex: 0,
          scale: true,
          min: yMin,
          max: yMax,
          axisLabel: { color: "#94a3b8" },
          splitLine: { lineStyle: { color: "rgba(148,163,184,0.16)" } },
        },
        {
          type: "value",
          gridIndex: 1,
          position: "left",
          scale: true,
          axisLabel: { color: "#94a3b8" },
          splitLine: { lineStyle: { color: "rgba(148,163,184,0.12)" } },
        },
        {
          type: "value",
          gridIndex: 1,
          position: "right",
          scale: true,
          axisLabel: { color: "#94a3b8" },
          splitLine: { show: false },
        },
      ],
      dataZoom: [
        {
          type: "inside",
          xAxisIndex: [0, 1],
          startValue: quantMainDataZoomRef.current?.startValue,
          endValue: quantMainDataZoomRef.current?.endValue,
          start: quantMainDataZoomRef.current ? undefined : 40,
          end: quantMainDataZoomRef.current ? undefined : 100,
        },
        {
          type: "slider",
          xAxisIndex: [0, 1],
          top: "95%",
          startValue: quantMainDataZoomRef.current?.startValue,
          endValue: quantMainDataZoomRef.current?.endValue,
          start: quantMainDataZoomRef.current ? undefined : 40,
          end: quantMainDataZoomRef.current ? undefined : 100,
        },
      ],
      visualMap: {
        show: false,
        seriesIndex: 0,
        min: 0,
        max: visualMax,
        inRange: {
          color: [
            "rgba(0,0,0,0)",
            "rgba(191,219,254,0.45)",
            "rgba(59,130,246,0.68)",
            "rgba(245,158,11,0.85)",
          ],
        },
      },
      series: [
        {
          name: "Spot5000热力图",
          type: "heatmap",
          xAxisIndex: 0,
          yAxisIndex: 0,
          data: heatPoints,
          encode: { x: 0, y: 1, value: 2 },
          progressive: 5000,
          blurSize: 20,
          pointSize: 32,
          z: 0,
        },
        {
          name: "K线",
          type: "candlestick",
          xAxisIndex: 0,
          yAxisIndex: 0,
          data: candleData,
          encode: {
            x: 0,
            y: [1, 2, 3, 4],
            tooltip: [1, 2, 3, 4],
          },
          itemStyle: {
            color: "#22c55e",
            color0: "#ef4444",
            borderColor: "#22c55e",
            borderColor0: "#ef4444",
          },
          z: 10,
        },
        {
          name: "Delta",
          type: "bar",
          xAxisIndex: 1,
          yAxisIndex: 1,
          data: deltaData,
          encode: { x: 0, y: 1, tooltip: [1] },
          itemStyle: {
            color: (params: any) => (Number(params?.value?.[1] ?? 0) >= 0 ? "rgba(34,197,94,0.7)" : "rgba(239,68,68,0.7)"),
          },
          barMaxWidth: 10,
          z: 2,
        },
        {
          name: "CVD",
          type: "line",
          xAxisIndex: 1,
          yAxisIndex: 2,
          data: cvdData,
          encode: { x: 0, y: 1, tooltip: [1] },
          lineStyle: { color: "#3b82f6", width: 1.8 },
          showSymbol: false,
          smooth: false,
          z: 3,
        },
      ],
    } as any;
  }, [quantRealtimeItems, spotHeatmap, quantCvdSeriesData]);

  const quantDeltaClass =
    quantLatest == null ? "cm-muted" : quantLatest.delta >= 0 ? "cm-number--pos" : "cm-number--neg";

  const quantMainChartEvents = useMemo(() => {
    return {
      dataZoom: (params: any) => {
        const batch = Array.isArray(params?.batch) ? params.batch[0] : params;
        const startValue = batch?.startValue;
        const endValue = batch?.endValue;
        if (Number.isFinite(startValue) && Number.isFinite(endValue)) {
          quantMainDataZoomRef.current = { startValue, endValue };
          if (cvdMode === "visible") {
            setQuantVisibleAnchorTs(Number(startValue));
          }
        }
      },
    } as Record<string, (params: any) => void>;
  }, [cvdMode]);

  const spotDominanceClass = useMemo(() => {
    const value = quantMlfCompare?.spotDominanceFactor;
    if (value == null) return "cm-muted";
    if (value > 0) return "cm-number--pos";
    if (value < 0) return "cm-number--neg";
    return "cm-muted";
  }, [quantMlfCompare]);

  const rankKindLabel = useMemo(() => {
    return RANK_KIND_OPTIONS.find((x) => x.value === rankKind)?.label || rankKind;
  }, [rankKind]);

  const rankColumns = useMemo(() => {
    const isFlow = rankKind === "fi1d" || rankKind === "fo1d" || rankKind === "si1d" || rankKind === "so1d";
    const isOICap = rankKind === "oicapratio";
    const changeTitle = `${rankInterval}变化`;
    const mainTitle = isFlow ? `${rankInterval}净额` : changeTitle;
    const subTitle = isFlow ? "资金偏向" : isOICap ? "当前持仓/市值比" : "当前持仓(U)";
    return [
      {
        title: "#",
        width: 56,
        render: (_: any, r: RankItem) => <span className="cm-number--mono">{r.rank ?? "-"}</span>,
      },
      {
        title: "Symbol",
        dataIndex: "symbol",
        render: (_: any, r: RankItem) => <Link to={`/coin/${r.symbol}`}>{r.symbol}</Link>,
      },
      {
        title: mainTitle,
        render: (_: any, r: RankItem) => {
          if (isFlow) {
            const cls = (r.netUsd ?? 0) >= 0 ? "cm-number--pos" : "cm-number--neg";
            return <span className={cls}>{formatCompactSigned(r.netUsd, 2)}</span>;
          }
          const pct = (r.changePct ?? 0) / 100;
          const cls = pct >= 0 ? "cm-number--pos" : "cm-number--neg";
          return <span className={cls}>{formatPct(pct, 2)}</span>;
        },
      },
      {
        title: subTitle,
        render: (_: any, r: RankItem) => {
          if (isFlow) {
            const pct = (r.biasPct ?? 0) / 100;
            const cls = pct >= 0 ? "cm-number--pos" : "cm-number--neg";
            return <span className={cls}>{formatPct(pct, 2)}</span>;
          }
          if (isOICap) {
            return <span className="cm-number--mono">{Number(r.ratioPct ?? 0).toFixed(2)}%</span>;
          }
          return <span className="cm-number--mono">{formatCompact(r.oiNotionalUsd, 2)}</span>;
        },
      },
    ];
  }, [rankKind, rankInterval]);

  const toggleOverviewSection = (key: OverviewSectionKey) => {
    setOverviewCollapsed((prev) => ({ ...prev, [key]: !prev[key] }));
  };

  const overviewSummary = useMemo(() => {
    const gainers24h = basic?.gainers?.length || 0;
    const losers24h = basic?.losers?.length || 0;
    const gainers15m = ret15m?.gainers?.length || 0;
    const losers15m = ret15m?.losers?.length || 0;
    const gainers1h = ret1h?.gainers?.length || 0;
    const losers1h = ret1h?.losers?.length || 0;
    const hotCount = hot?.length || 0;
    const favCount = favorites?.items?.length || 0;
    return {
      gainers24h,
      losers24h,
      gainers15m,
      losers15m,
      gainers1h,
      losers1h,
      hotCount,
      favCount,
    };
  }, [basic, ret15m, ret1h, hot, favorites]);

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
    absorption_signal_long: "吸筹扫描看多",
    absorption_signal_short: "吸筹扫描看空",
    signal_lab_bid_wall: "买盘墙",
    signal_lab_ask_wall: "卖盘墙",
  };

  return (
    <div className="cm-page">
      <div className="cm-section">
        <div className="cm-sectionHeader">
          <Title heading={5} style={{ margin: 0 }}>
            首页（默认市场：合约）
          </Title>
          <Space>
            <span className="cm-pill">
              <span className="cm-muted">市场</span>
              <Select value={market} onChange={(v) => setMarket(v as Market)} style={{ width: 120 }}>
                <Select.Option value="swap">合约（swap）</Select.Option>
                <Select.Option value="spot">现货（spot）</Select.Option>
              </Select>
            </span>
            <span className="cm-pill">
              <span className="cm-muted">{activeHomeTab === "quant" ? "上次量化刷新" : "上次总览刷新"}</span>
              <span className="cm-number--mono">
                {activeHomeTab === "quant"
                  ? quantUpdatedMs
                    ? new Date(quantUpdatedMs).toLocaleString()
                    : "—"
                  : lastUpdatedMs
                    ? new Date(lastUpdatedMs).toLocaleString()
                    : "—"}
              </span>
            </span>
            <Button
              loading={activeHomeTab === "quant" ? quantLoading : loading || rankLoading}
              onClick={
                activeHomeTab === "quant"
                  ? () => loadQuant()
                  : () => {
                      refreshOverview();
                      refreshRankCenter();
                    }
              }
              type="primary"
            >
              {activeHomeTab === "quant" ? "刷新量化" : "刷新总览"}
            </Button>
          </Space>
        </div>

        <div className="cm-card" style={{ padding: 12, marginTop: 10 }}>
          <div className="cm-sectionHeader" style={{ marginBottom: 6 }}>
            <Title heading={6} style={{ margin: 0 }}>
              收藏入口
            </Title>
            <Text className="cm-muted">支持输入 BTC 自动联想（spot + swap 全部交易对）</Text>
          </div>
          <Space style={{ width: "100%" }}>
            <AutoComplete
              style={{ width: 260 }}
              value={favInput}
              data={favoriteSearchCandidates}
              loading={pairPoolLoading}
              placeholder="输入 btc 或 BTCUSDT"
              onChange={(v) => setFavInput((v || "").toUpperCase())}
              onSearch={(v) => setFavInput((v || "").toUpperCase())}
              onPressEnter={(_e, activeOption) => {
                const selected =
                  typeof activeOption?.value === "string" && activeOption.value
                    ? String(activeOption.value)
                    : favoriteSearchCandidates[0] || favInput;
                handleAddFavorite(selected);
              }}
            />
            <Button type="primary" onClick={() => handleAddFavorite()}>
              添加
            </Button>
          </Space>
          <div className="cm-favList">
            {(favorites?.items || []).length === 0 && <Text className="cm-muted">暂无收藏</Text>}
            {(favorites?.items || []).map((f) => {
              const d = favDetails[f.symbol]?.basic;
              const pct = d?.priceChangePercent24h == null ? null : d.priceChangePercent24h / 100;
              const cls = pct == null ? "cm-muted" : pct >= 0 ? "cm-number--pos" : "cm-number--neg";
              return (
                <div key={f.symbol} className="cm-favItem">
                  <Link to={`/coin/${f.symbol}`}>{f.symbol}</Link>
                  <span className="cm-number--mono">{formatPrice(d?.lastPrice ?? null)}</span>
                  <span className={cls}>{formatPct(pct, 2)}</span>
                  <Button size="mini" onClick={() => handleRemoveFavorite(f.symbol)}>
                    移除
                  </Button>
                </div>
              );
            })}
          </div>
        </div>

        <div className="cm-coinTabs" style={{ justifyContent: "flex-start", marginTop: 10 }}>
          {(
            [
              { key: "overview", label: "首页总览" },
              { key: "quant", label: "量化仪表盘" },
            ] as Array<{ key: HomeTabKey; label: string }>
          ).map((tab) => (
            <button
              key={tab.key}
              type="button"
              className={`cm-tab ${activeHomeTab === tab.key ? "cm-tab--active" : ""}`}
              onClick={() => setActiveHomeTab(tab.key)}
            >
              {tab.label}
            </button>
          ))}
        </div>
      </div>

      {activeHomeTab === "quant" && (
        <div className="cm-section">
          <div className="cm-card" style={{ padding: 12 }}>
            <div className="cm-sectionHeader">
              <Title heading={6} style={{ margin: 0 }}>
                中低频结构图（K线 + Spot5000热力 + Delta/CVD）
              </Title>
              <Space>
                <QuantHelp title="量化指标说明" />
                <Input
                  style={{ width: 160 }}
                  value={quantSymbol}
                  onChange={setQuantSymbol}
                  placeholder="BTCUSDT"
                  onPressEnter={() => loadQuant()}
                />
                <Select value={quantBucket} onChange={(v) => setQuantBucket(v as QuantBucket)} style={{ width: 100 }}>
                  {QUANT_BUCKET_OPTIONS.map((option) => (
                    <Select.Option key={option.value} value={option.value}>
                      {option.label}
                    </Select.Option>
                  ))}
                </Select>
                <Select value={cvdMode} onChange={(v) => setCvdMode(v as CvdMode)} style={{ width: 130 }}>
                  {CVD_MODE_OPTIONS.map((option) => (
                    <Select.Option key={option.value} value={option.value}>
                      {option.label}
                    </Select.Option>
                  ))}
                </Select>
                <Button loading={quantLoading} type="primary" onClick={() => loadQuant()}>
                  加载
                </Button>
              </Space>
            </div>
            <div className="cm-muted" style={{ marginBottom: 8 }}>
              上次量化刷新：{quantUpdatedMs ? new Date(quantUpdatedMs).toLocaleString() : "—"}
            </div>
            {quantMainOption ? (
              <EChart
                option={quantMainOption}
                height={980}
                onEvents={quantMainChartEvents}
                setOptionOpts={{
                  notMerge: false,
                  lazyUpdate: true,
                  replaceMerge: ["series"],
                }}
              />
            ) : quantLoading ? (
              <div style={{ padding: 12 }}>
                <Skeleton animation text={{ rows: 8 }} />
              </div>
            ) : (
              <Text className="cm-muted">暂无可视化数据</Text>
            )}
          </div>

          <div className="cm-grid-2" style={{ marginTop: 12 }}>
            <div className="cm-card" style={{ padding: 12 }}>
              <Text className="cm-muted">最新 K 线时间</Text>
              <div className="cm-homeQuantValue">{quantLatest ? new Date(quantLatest.ts).toLocaleString() : "-"}</div>
            </div>
            <div className="cm-card" style={{ padding: 12 }}>
              <Text className="cm-muted">最新收盘价</Text>
              <div className="cm-homeQuantValue cm-number--mono">{formatNumber(quantLatest?.c ?? null, 0)}</div>
            </div>
            <div className="cm-card" style={{ padding: 12 }}>
              <Text className="cm-muted">Delta</Text>
              <div className={`cm-homeQuantValue cm-number--mono ${quantDeltaClass}`}>{formatCompactSigned(quantLatest?.delta ?? null, 2)}</div>
            </div>
            <div className="cm-card" style={{ padding: 12 }}>
              <Text className="cm-muted">CVD</Text>
              <div className="cm-homeQuantValue cm-number--mono">{formatCompact(quantLatest?.cvd ?? null, 2)}</div>
            </div>
            <div className="cm-card" style={{ padding: 12 }}>
              <Text className="cm-muted">VWAP</Text>
              <div className="cm-homeQuantValue cm-number--mono">{formatPrice(quantLatest?.vwap ?? null)}</div>
            </div>
            <div className="cm-card" style={{ padding: 12 }}>
              <Text className="cm-muted">成交量</Text>
              <div className="cm-homeQuantValue cm-number--mono">{formatNumber(quantLatest?.vol_total ?? null)}</div>
            </div>
          </div>

          <div className="cm-grid-2" style={{ marginTop: 12 }}>
            <div className="cm-card" style={{ padding: 12 }}>
              <Text className="cm-muted">Spot Wall Strength Ratio</Text>
              <div className="cm-homeQuantValue cm-number--mono">{formatRatio(quantMlfCompare?.spot.wallStrengthRatio ?? null, 2)}</div>
              <div className="cm-muted">买墙: {formatCompact(quantMlfCompare?.spot.buyWallUsd ?? null, 2)} / 卖墙: {formatCompact(quantMlfCompare?.spot.sellWallUsd ?? null, 2)}</div>
            </div>
            <div className="cm-card" style={{ padding: 12 }}>
              <Text className="cm-muted">Swap Wall Strength Ratio</Text>
              <div className="cm-homeQuantValue cm-number--mono">{formatRatio(quantMlfCompare?.swap.wallStrengthRatio ?? null, 2)}</div>
              <div className="cm-muted">买墙: {formatCompact(quantMlfCompare?.swap.buyWallUsd ?? null, 2)} / 卖墙: {formatCompact(quantMlfCompare?.swap.sellWallUsd ?? null, 2)}</div>
            </div>
            <div className="cm-card" style={{ padding: 12 }}>
              <Text className="cm-muted">Spot Dominance Factor（24h）</Text>
              <div className={`cm-homeQuantValue cm-number--mono ${spotDominanceClass}`}>
                {formatSigned(quantMlfCompare?.spotDominanceFactor ?? null, 3)}
              </div>
              <div className="cm-muted">状态: {quantMlfCompare?.dominanceState || "UNKNOWN"}</div>
            </div>
            <div className="cm-card" style={{ padding: 12 }}>
              <Text className="cm-muted">Wall Strength Ratio Diff（Spot - Swap）</Text>
              <div className="cm-homeQuantValue cm-number--mono">
                {formatSigned(quantMlfCompare?.wallStrengthRatioDiff ?? null, 2)}
              </div>
              <div className="cm-muted">阈值: {formatNumber(quantMlfCompare?.dominanceThreshold ?? null, 2)}</div>
            </div>
          </div>
        </div>
      )}

      {activeHomeTab === "overview" && (
        <>
          <div className="cm-section">
            <div className="cm-card cm-homeSummary">
              <div className="cm-homeSummaryItem">
                <div className="cm-homeSummaryLabel">24h 上涨币种数 / 下跌币种数</div>
                <div className="cm-homeSummaryValue cm-number--mono">
                  {overviewSummary.gainers24h} / {overviewSummary.losers24h}
                </div>
              </div>
              <div className="cm-homeSummaryItem">
                <div className="cm-homeSummaryLabel">15m 上涨币种数 / 下跌币种数</div>
                <div className="cm-homeSummaryValue cm-number--mono">
                  {overviewSummary.gainers15m} / {overviewSummary.losers15m}
                </div>
              </div>
              <div className="cm-homeSummaryItem">
                <div className="cm-homeSummaryLabel">1h 上涨币种数 / 下跌币种数</div>
                <div className="cm-homeSummaryValue cm-number--mono">
                  {overviewSummary.gainers1h} / {overviewSummary.losers1h}
                </div>
              </div>
              <div className="cm-homeSummaryItem">
                <div className="cm-homeSummaryLabel">异动 / 收藏</div>
                <div className="cm-homeSummaryValue cm-number--mono">
                  {overviewSummary.hotCount} / {overviewSummary.favCount}
                </div>
              </div>
            </div>
          </div>

          <div className="cm-section">
            <div className="cm-sectionHeader">
              <Title heading={6} style={{ margin: 0 }}>
                排行中心
              </Title>
              <Space>
                <Select value={rankKind} onChange={(v) => setRankKind(v as RankKind)} style={{ width: 220 }}>
                  {RANK_KIND_OPTIONS.map((option) => (
                    <Select.Option key={option.value} value={option.value}>
                      {option.label}
                    </Select.Option>
                  ))}
                </Select>
                <Select value={rankInterval} onChange={(v) => setRankInterval(v as RankInterval)} style={{ width: 100 }}>
                  {RANK_INTERVAL_OPTIONS.map((option) => (
                    <Select.Option key={option.value} value={option.value}>
                      {option.label}
                    </Select.Option>
                  ))}
                </Select>
                <Text className="cm-muted">
                  {rankUpdatedMs ? `更新于 ${new Date(rankUpdatedMs).toLocaleTimeString()}` : "—"}
                </Text>
              </Space>
            </div>
            <div className="cm-card" style={{ padding: 12 }}>
              <Text className="cm-muted">{rankKindLabel}（按最近已收盘周期）</Text>
              <div className="cm-table" style={{ marginTop: 8 }}>
                <Table
                  loading={rankLoading}
                  rowKey="symbol"
                  pagination={false}
                  size="small"
                  columns={rankColumns as any}
                  data={rankResp?.items || []}
                  scroll={{ x: "max-content", y: 380 }}
                />
              </div>
            </div>
          </div>

          <div className="cm-section">
            <div className="cm-sectionHeader">
              <Title heading={6} style={{ margin: 0 }}>
                24h 涨跌榜
              </Title>
              <Space>
                <Text className="cm-muted">来自 Binance 24h ticker</Text>
                <Button size="mini" type="text" onClick={() => toggleOverviewSection("movers24h")}>
                  {overviewCollapsed.movers24h ? "展开" : "收起"}
                </Button>
              </Space>
            </div>
            {!overviewCollapsed.movers24h && (
              <div className="cm-grid-2">
                <div className="cm-card" style={{ padding: 12 }}>
                  <Text className="cm-muted">上涨榜</Text>
                  <div className="cm-table">
                    <Table
                      loading={loading}
                      rowKey="symbol"
                      pagination={false}
                      size="small"
                      columns={basicColumns as any}
                      data={basic?.gainers || []}
                      scroll={{ x: "max-content", y: 340 }}
                    />
                  </div>
                </div>
                <div className="cm-card" style={{ padding: 12 }}>
                  <Text className="cm-muted">下跌榜</Text>
                  <div className="cm-table">
                    <Table
                      loading={loading}
                      rowKey="symbol"
                      pagination={false}
                      size="small"
                      columns={basicColumns as any}
                      data={basic?.losers || []}
                      scroll={{ x: "max-content", y: 340 }}
                    />
                  </div>
                </div>
              </div>
            )}
          </div>

          <div className="cm-section">
            <div className="cm-sectionHeader">
              <Title heading={6} style={{ margin: 0 }}>
                最近 15m（已收盘）
              </Title>
              <Space>
                <Text className="cm-muted">
                  {ret15m?.bucketStartMs && ret15m.bucketEndMs
                    ? `${new Date(ret15m.bucketStartMs).toLocaleTimeString()} ~ ${new Date(ret15m.bucketEndMs).toLocaleTimeString()}`
                    : "—"}
                </Text>
                <Button size="mini" type="text" onClick={() => toggleOverviewSection("ret15m")}>
                  {overviewCollapsed.ret15m ? "展开" : "收起"}
                </Button>
              </Space>
            </div>
            {!overviewCollapsed.ret15m && (
              <div className="cm-grid-2">
                <div className="cm-card" style={{ padding: 12 }}>
                  <Text className="cm-muted">涨幅榜</Text>
                  <div className="cm-table">
                    <Table
                      loading={loading}
                      rowKey="symbol"
                      pagination={false}
                      size="small"
                      columns={returnColumns as any}
                      data={ret15m?.gainers || []}
                      scroll={{ x: "max-content", y: 340 }}
                    />
                  </div>
                </div>
                <div className="cm-card" style={{ padding: 12 }}>
                  <Text className="cm-muted">跌幅榜</Text>
                  <div className="cm-table">
                    <Table
                      loading={loading}
                      rowKey="symbol"
                      pagination={false}
                      size="small"
                      columns={returnColumns as any}
                      data={ret15m?.losers || []}
                      scroll={{ x: "max-content", y: 340 }}
                    />
                  </div>
                </div>
              </div>
            )}
          </div>

          <div className="cm-section">
            <div className="cm-sectionHeader">
              <Title heading={6} style={{ margin: 0 }}>
                最近 1h（已收盘）
              </Title>
              <Space>
                <Text className="cm-muted">
                  {ret1h?.bucketStartMs && ret1h.bucketEndMs
                    ? `${new Date(ret1h.bucketStartMs).toLocaleTimeString()} ~ ${new Date(ret1h.bucketEndMs).toLocaleTimeString()}`
                    : "—"}
                </Text>
                <Button size="mini" type="text" onClick={() => toggleOverviewSection("ret1h")}>
                  {overviewCollapsed.ret1h ? "展开" : "收起"}
                </Button>
              </Space>
            </div>
            {!overviewCollapsed.ret1h && (
              <div className="cm-grid-2">
                <div className="cm-card" style={{ padding: 12 }}>
                  <Text className="cm-muted">涨幅榜</Text>
                  <div className="cm-table">
                    <Table
                      loading={loading}
                      rowKey="symbol"
                      pagination={false}
                      size="small"
                      columns={returnColumns as any}
                      data={ret1h?.gainers || []}
                      scroll={{ x: "max-content", y: 340 }}
                    />
                  </div>
                </div>
                <div className="cm-card" style={{ padding: 12 }}>
                  <Text className="cm-muted">跌幅榜</Text>
                  <div className="cm-table">
                    <Table
                      loading={loading}
                      rowKey="symbol"
                      pagination={false}
                      size="small"
                      columns={returnColumns as any}
                      data={ret1h?.losers || []}
                      scroll={{ x: "max-content", y: 340 }}
                    />
                  </div>
                </div>
              </div>
            )}
          </div>
      <div className="cm-section">
        <div className="cm-grid-2">
          <div className="cm-card" style={{ padding: 12 }}>
            <div className="cm-sectionHeader">
              <Title heading={6} style={{ margin: 0 }}>
                热点 / 异动快讯
              </Title>
              <Tag color="arcoblue">可追溯事件流</Tag>
            </div>
            <div className="cm-eventList">
              {(hot || []).length === 0 && <Text className="cm-muted">暂无数据</Text>}
              {(hot || []).map((e) => (
                <div key={e.id} className="cm-eventItem">
                  <div className="cm-eventTitle">
                    <Tag color="orange">{eventTypeLabel[e.eventType] || e.eventType}</Tag>
                    <Link to={`/coin/${e.symbol}`}>{e.symbol}</Link>
                    <span className="cm-muted">{e.title}</span>
                  </div>
                  <div className="cm-eventMeta">
                    <span className="cm-number--mono">{new Date(e.eventTimeMs).toLocaleString()}</span>
                    <span className="cm-muted">信号: {e.tfSignal}</span>
                    {e.tfLevel && <span className="cm-muted">水平: {e.tfLevel}</span>}
                  </div>
                </div>
              ))}
            </div>
          </div>

        </div>
      </div>

        </>
      )}
    </div>
  );
}

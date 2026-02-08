import { useEffect, useMemo, useRef, useState } from "react";
import {
  Button,
  Input,
  Message,
  Select,
  Space,
  Table,
  Tag,
  Typography,
} from "@arco-design/web-react";
import { Link } from "react-router-dom";

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
  const [loading, setLoading] = useState(false);
  const [basic, setBasic] = useState<BasicInfo | null>(null);
  const [ret15m, setRet15m] = useState<ReturnsResp | null>(null);
  const [ret1h, setRet1h] = useState<ReturnsResp | null>(null);
  const [hot, setHot] = useState<HotItem[]>([]);
  const [favorites, setFavorites] = useState<FavoritesResp | null>(null);
  const [favDetails, setFavDetails] = useState<Record<string, CoinBasicResp>>({});
  const [lastUpdatedMs, setLastUpdatedMs] = useState<number | null>(null);
  const [favInput, setFavInput] = useState("");
  const refreshSeq = useRef(0);

  const refresh = async () => {
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
      setHot(hm.items || []);
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

  useEffect(() => {
    refresh();
    const t = setInterval(refresh, 15000);
    return () => clearInterval(t);
  }, [market]);

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
          const cls = r.returnPct >= 0 ? "cm-number--pos" : "cm-number--neg";
          return <span className={cls}>{formatPct(r.returnPct, 2)}</span>;
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

  const handleAddFavorite = async () => {
    const sym = favInput.trim().toUpperCase();
    if (!sym) return;
    try {
      const cid = getClientId();
      await postJson(`/api/user/favorites?clientId=${cid}`, { market, symbols: [sym] });
      setFavInput("");
      refresh();
    } catch (e: any) {
      Message.error(`收藏失败：${e?.message || e}`);
    }
  };

  const handleRemoveFavorite = async (sym: string) => {
    try {
      const cid = getClientId();
      await deleteReq(`/api/user/favorites/${sym}?clientId=${cid}&market=${market}`);
      refresh();
    } catch (e: any) {
      Message.error(`取消收藏失败：${e?.message || e}`);
    }
  };

  const eventTypeLabel: Record<string, string> = {
    breakout_up: "突破",
    breakout_down: "跌破",
    volume_spike: "量能",
    amplitude_spike: "振幅",
    signal_lab_persistent_buy: "持续吸筹",
    signal_lab_bid_wall: "买盘墙",
    signal_lab_ask_wall: "卖盘墙",
  };

  return (
    <div className="cm-page">
      <div className="cm-section">
        <div className="cm-sectionHeader">
          <Title heading={5} style={{ margin: 0 }}>
            主页面（默认市场：合约）
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
              <span className="cm-muted">上次刷新</span>
              <span className="cm-number--mono">{lastUpdatedMs ? new Date(lastUpdatedMs).toLocaleString() : "—"}</span>
            </span>
            <Button loading={loading} onClick={refresh} type="primary">
              立即刷新
            </Button>
          </Space>
        </div>
      </div>

      <div className="cm-section">
        <div className="cm-sectionHeader">
          <Title heading={6} style={{ margin: 0 }}>
            24h 涨跌榜
          </Title>
          <Text className="cm-muted">来自 Binance 24h ticker，口径清晰可复算</Text>
        </div>
        <div className="cm-grid-2">
          <div className="cm-card" style={{ padding: 12 }}>
            <Text className="cm-muted">上涨榜</Text>
            <div className="cm-table">
              <Table loading={loading} rowKey="symbol" pagination={false} size="small" columns={basicColumns as any} data={basic?.gainers || []} />
            </div>
          </div>
          <div className="cm-card" style={{ padding: 12 }}>
            <Text className="cm-muted">下跌榜</Text>
            <div className="cm-table">
              <Table loading={loading} rowKey="symbol" pagination={false} size="small" columns={basicColumns as any} data={basic?.losers || []} />
            </div>
          </div>
        </div>
      </div>

      <div className="cm-section">
        <div className="cm-sectionHeader">
          <Title heading={6} style={{ margin: 0 }}>
            最近 15m（已收盘）
          </Title>
          <Text className="cm-muted">
            {ret15m?.bucketStartMs && ret15m.bucketEndMs
              ? `${new Date(ret15m.bucketStartMs).toLocaleTimeString()} ~ ${new Date(ret15m.bucketEndMs).toLocaleTimeString()}`
              : "—"}
          </Text>
        </div>
        <div className="cm-grid-2">
          <div className="cm-card" style={{ padding: 12 }}>
            <Text className="cm-muted">涨幅榜</Text>
            <div className="cm-table">
              <Table loading={loading} rowKey="symbol" pagination={false} size="small" columns={returnColumns as any} data={ret15m?.gainers || []} />
            </div>
          </div>
          <div className="cm-card" style={{ padding: 12 }}>
            <Text className="cm-muted">跌幅榜</Text>
            <div className="cm-table">
              <Table loading={loading} rowKey="symbol" pagination={false} size="small" columns={returnColumns as any} data={ret15m?.losers || []} />
            </div>
          </div>
        </div>
      </div>

      <div className="cm-section">
        <div className="cm-sectionHeader">
          <Title heading={6} style={{ margin: 0 }}>
            最近 1h（已收盘）
          </Title>
          <Text className="cm-muted">
            {ret1h?.bucketStartMs && ret1h.bucketEndMs
              ? `${new Date(ret1h.bucketStartMs).toLocaleTimeString()} ~ ${new Date(ret1h.bucketEndMs).toLocaleTimeString()}`
              : "—"}
          </Text>
        </div>
        <div className="cm-grid-2">
          <div className="cm-card" style={{ padding: 12 }}>
            <Text className="cm-muted">涨幅榜</Text>
            <div className="cm-table">
              <Table loading={loading} rowKey="symbol" pagination={false} size="small" columns={returnColumns as any} data={ret1h?.gainers || []} />
            </div>
          </div>
          <div className="cm-card" style={{ padding: 12 }}>
            <Text className="cm-muted">跌幅榜</Text>
            <div className="cm-table">
              <Table loading={loading} rowKey="symbol" pagination={false} size="small" columns={returnColumns as any} data={ret1h?.losers || []} />
            </div>
          </div>
        </div>
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
                    <span className="cm-muted">信号：{e.tfSignal}</span>
                    {e.tfLevel && <span className="cm-muted">水平：{e.tfLevel}</span>}
                  </div>
                </div>
              ))}
            </div>
          </div>

          <div className="cm-card" style={{ padding: 12 }}>
            <div className="cm-sectionHeader">
              <Title heading={6} style={{ margin: 0 }}>
                收藏入口
              </Title>
              <Text className="cm-muted">匿名 client_id 存储（本地浏览器）</Text>
            </div>
            <Space style={{ width: "100%" }}>
              <Input
                placeholder="输入 symbol，例如 BTCUSDT"
                value={favInput}
                onChange={setFavInput}
                onPressEnter={handleAddFavorite}
              />
              <Button type="primary" onClick={handleAddFavorite}>
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
        </div>
      </div>
    </div>
  );
}

import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Table, Tag, Typography } from "@arco-design/web-react";
import { fmtUsdCn } from "./etfFlows";

const { Title, Text } = Typography;
const API_BASE = (import.meta as any).env?.VITE_API_BASE || "";
const REFRESH_MS = 60 * 1000; // 后端每 5 分钟扫一次

type Item = {
  symbol: string;
  decision_ms: number;
  price: number;
  acc_3d: number;
  rise_from_low30: number;
  ret_4h: number;
  vol_24h: number;
  oi_usd: number;
  market_cap: number;
  ema_cross: boolean;
};
type Pick = {
  id: number;
  list: "watch" | "avoid";
  symbol: string;
  entered_ms: number;
  entry_price: number;
  ema_cross: boolean;
  ret_4h: number | null;
  ret_24h: number | null;
  first_touch_5: number | null;
};
type Stats = {
  list: "watch" | "avoid";
  done: number;
  up_4h_ratio: number;
  median_24h: number;
  touch_up_5: number;
  touch_down_5: number;
  since_ms: number;
};

const pct = (v: number | null | undefined, digits = 1) =>
  v == null || !Number.isFinite(v) ? "—" : `${v > 0 ? "+" : ""}${(v * 100).toFixed(digits)}%`;
const tone = (v: number | null | undefined) =>
  v == null ? "var(--cm-muted)" : v > 0 ? "var(--cm-success)" : v < 0 ? "var(--cm-danger)" : "var(--cm-muted)";
const fmtTime = (ms: number) =>
  new Date(ms).toLocaleString(undefined, { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" });

function Pct({ v }: { v: number | null | undefined }) {
  return <span style={{ color: tone(v), fontVariantNumeric: "tabular-nums" }}>{pct(v)}</span>;
}

function StatsLine({ s, backtest }: { s?: Stats; backtest: string }) {
  return (
    <Text className="cm-muted" style={{ display: "block", marginBottom: 8 }}>
      回测（2026 年 7~9 月）：{backtest}
      <br />
      实盘：
      {!s || s.done === 0
        ? "还没有满 24 小时的记录"
        : `自 ${fmtTime(s.since_ms)} 起，已满 24 小时 ${s.done} 次：4h 后上涨 ${(s.up_4h_ratio * 100).toFixed(0)}%，先涨 5% ${(s.touch_up_5 * 100).toFixed(0)}%，先跌 5% ${(s.touch_down_5 * 100).toFixed(0)}%，24h 中位 ${pct(s.median_24h)}`}
    </Text>
  );
}

// PotentialPage 潜力区：观察名单（低位高积累）与别追名单（没积累的暴涨），附上榜记录的实盘表现。
export default function PotentialPage() {
  const [watch, setWatch] = useState<Item[]>([]);
  const [avoid, setAvoid] = useState<Item[]>([]);
  const [history, setHistory] = useState<Pick[]>([]);
  const [stats, setStats] = useState<Stats[]>([]);
  const [updatedMs, setUpdatedMs] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  useEffect(() => {
    let alive = true;
    const load = async () => {
      try {
        const r = await fetch(`${API_BASE}/api/potential?history=100`);
        if (!r.ok) throw new Error(`HTTP ${r.status}`);
        const d = await r.json();
        if (alive) {
          setWatch(d.watch || []);
          setAvoid(d.avoid || []);
          setHistory(d.history || []);
          setStats(d.stats || []);
          setUpdatedMs(d.updated_ms || 0);
          setError("");
        }
      } catch (e: any) {
        if (alive) setError(e?.message || "加载失败");
      } finally {
        if (alive) setLoading(false);
      }
    };
    load();
    const t = setInterval(load, REFRESH_MS);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, []);

  const itemColumns = [
    {
      title: "币",
      fixed: "left" as const,
      width: 150,
      render: (_: any, r: Item) => (
        <span style={{ whiteSpace: "nowrap" }}>
          <Link to={`/coin/${r.symbol}`}>{r.symbol}</Link>
          {r.ema_cross && (
            <Tag size="small" color="green" style={{ marginLeft: 6 }} title="最近 4 小时内 1h 或 15m 收盘价上穿 EMA100/200，且 EMA100 在 EMA200 上方">
              刚上穿 EMA
            </Tag>
          )}
        </span>
      ),
    },
    { title: "价格", render: (_: any, r: Item) => <span style={{ fontVariantNumeric: "tabular-nums" }}>{r.price}</span> },
    { title: "3 天积累", sorter: (a: Item, b: Item) => a.acc_3d - b.acc_3d, render: (_: any, r: Item) => <span style={{ color: tone(r.acc_3d) }}>{fmtUsdCn(r.acc_3d)}</span> },
    { title: "离 30 天低点", render: (_: any, r: Item) => (r.rise_from_low30 ? pct(r.rise_from_low30) : "—") },
    { title: "4h 涨幅", sorter: (a: Item, b: Item) => a.ret_4h - b.ret_4h, render: (_: any, r: Item) => <Pct v={r.ret_4h} /> },
    { title: "24h 成交额", render: (_: any, r: Item) => fmtUsdCn(r.vol_24h, false) },
    { title: "持仓", render: (_: any, r: Item) => fmtUsdCn(r.oi_usd, false) },
    { title: "市值", render: (_: any, r: Item) => (r.market_cap ? fmtUsdCn(r.market_cap, false) : "—") },
  ];

  const historyColumns = [
    { title: "上榜时间", render: (_: any, r: Pick) => <span style={{ whiteSpace: "nowrap" }}>{fmtTime(r.entered_ms)}</span> },
    { title: "名单", render: (_: any, r: Pick) => (r.list === "watch" ? <Tag color="green">观察</Tag> : <Tag color="red">别追</Tag>) },
    {
      title: "币",
      render: (_: any, r: Pick) => (
        <span style={{ whiteSpace: "nowrap" }}>
          <Link to={`/coin/${r.symbol}`}>{r.symbol}</Link>
          {r.ema_cross && <Tag size="small" color="green" style={{ marginLeft: 6 }}>EMA</Tag>}
        </span>
      ),
    },
    { title: "上榜价", render: (_: any, r: Pick) => r.entry_price },
    { title: "4h 后", render: (_: any, r: Pick) => <Pct v={r.ret_4h} /> },
    { title: "24h 后", render: (_: any, r: Pick) => <Pct v={r.ret_24h} /> },
    {
      title: "24h 内先碰到",
      render: (_: any, r: Pick) =>
        r.first_touch_5 == null ? <Text className="cm-muted">—</Text> : r.first_touch_5 > 0 ? <span style={{ color: tone(1) }}>+5%</span> : r.first_touch_5 < 0 ? <span style={{ color: tone(-1) }}>-5%</span> : <Text className="cm-muted">都没碰到</Text>,
    },
  ];

  const statOf = (list: string) => stats.find((s) => s.list === list);
  const table = (data: Item[]) => <Table rowKey="symbol" loading={loading} columns={itemColumns} data={data} pagination={false} scroll={{ x: true }} border={false} />;

  return (
    <>
      <div className="cm-section">
        <div className="cm-sectionHeader">
          <Title heading={6} style={{ margin: 0 }}>
            潜力区 · 观察名单（低位高积累）
          </Title>
          <Text className="cm-muted">
            {updatedMs ? `更新于 ${new Date(updatedMs).toLocaleTimeString()}` : ""}
            {error ? ` · ${error}` : ""}
          </Text>
        </div>
        <Text className="cm-muted" style={{ display: "block", marginBottom: 4 }}>
          条件：只看加密币小币（市值 &lt; 10 亿，没有市值数据时持仓 &lt; 5000 万），24h 成交额 ≥ 200 万、持仓 ≥ 200 万；近 3 天合约净流入（主动买 − 主动卖）≥ 500 万，价格离 30 天低点 &lt; 30%。
        </Text>
        <StatsLine s={statOf("watch")} backtest="24h 内先涨 5% 的 42%、先跌 5% 的 24%（随便买为 33% / 26%）；只有 20 个币，样本少，仅供观察。" />
        {table(watch)}
      </div>

      <div className="cm-section">
        <div className="cm-sectionHeader">
          <Title heading={6} style={{ margin: 0 }}>
            别追名单（没积累的暴涨）
          </Title>
        </div>
        <Text className="cm-muted" style={{ display: "block", marginBottom: 4 }}>
          条件：同样的小币范围；近 3 天净流入 &lt; 50 万，4 小时涨幅 ≥ 10%。
        </Text>
        <StatsLine s={statOf("avoid")} backtest="4h 后 58% 下跌，24h 后中位跌 4.8%（1452 次、363 个币，前后两段一致）；偶尔会被继续暴拉，不建议直接做空。" />
        {table(avoid)}
      </div>

      <div className="cm-section">
        <div className="cm-sectionHeader">
          <Title heading={6} style={{ margin: 0 }}>
            上榜记录
          </Title>
          <Text className="cm-muted">同一个币同一个名单 24 小时内只记一次，按上榜那一小时的收盘价计算</Text>
        </div>
        <Table rowKey="id" loading={loading} columns={historyColumns} data={history} pagination={{ pageSize: 20 }} scroll={{ x: true }} border={false} />
      </div>
    </>
  );
}

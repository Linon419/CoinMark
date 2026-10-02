import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Table, Typography } from "@arco-design/web-react";
import { fmtUsdCn } from "./etfFlows";
import { FavStar, NotifyPanel, PULLBACK_TIMEFRAMES, usePullbackNotify } from "../components/PullbackNotify";

const { Title, Text } = Typography;
const API_BASE = (import.meta as any).env?.VITE_API_BASE || "";
const REFRESH_MS = 60 * 1000; // 后端每 15 分钟扫一次（整刻钟后 30 秒），页面每分钟取一次最新结果

type Hit = {
  timeframe: string;
  candle_start_ms: number;
  line: "EMA100" | "EMA200";
  close: number;
  low: number;
  ema100: number;
  ema200: number;
  distance_pct: number;
};
type Row = { symbol: string; quote_volume_24h: number; hits: Hit[] };

function HitCell({ hit }: { hit?: Hit }) {
  if (!hit) return <Text className="cm-muted">—</Text>;
  const title = `收盘 ${hit.close}｜最低 ${hit.low}｜EMA100 ${hit.ema100.toPrecision(6)}｜EMA200 ${hit.ema200.toPrecision(6)}`;
  return (
    <span title={title} style={{ whiteSpace: "nowrap", fontVariantNumeric: "tabular-nums", color: hit.line === "EMA200" ? "var(--cm-warning)" : "var(--cm-success)" }}>
      {hit.line} +{(hit.distance_pct * 100).toFixed(2)}%
    </span>
  );
}

// EmaPullbackPage EMA 回踩：上涨趋势中回落到 EMA100 / EMA200 并收在线上方的合约。
export default function EmaPullbackPage() {
  const [items, setItems] = useState<Row[]>([]);
  const [updatedMs, setUpdatedMs] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const { settings: notify, save: saveNotify, isFav, toggleFav } = usePullbackNotify("/api/ema-pullback/notify");

  useEffect(() => {
    let alive = true;
    const load = async () => {
      try {
        const r = await fetch(`${API_BASE}/api/ema-pullback`);
        if (!r.ok) throw new Error(`HTTP ${r.status}`);
        const d = await r.json();
        if (alive) {
          setItems(d.items || []);
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

  const columns = [
    {
      title: "币",
      dataIndex: "symbol",
      fixed: "left" as const,
      width: 150,
      render: (v: string) => (
        <span style={{ whiteSpace: "nowrap" }}>
          <FavStar active={isFav(v)} onClick={() => toggleFav(v)} />
          <Link to={`/coin/${v}`}>{v}</Link>
        </span>
      ),
    },
    ...PULLBACK_TIMEFRAMES.map((tf) => ({
      title: tf,
      render: (_: any, r: Row) => <HitCell hit={r.hits.find((h) => h.timeframe === tf)} />,
    })),
    {
      title: "24h 成交额",
      sorter: (a: Row, b: Row) => a.quote_volume_24h - b.quote_volume_24h,
      render: (_: any, r: Row) => fmtUsdCn(r.quote_volume_24h, false),
    },
  ];

  return (
    <div className="cm-section">
      <div className="cm-sectionHeader">
        <Title heading={6} style={{ margin: 0 }}>
          EMA 回踩
        </Title>
        <Text className="cm-muted">
          {updatedMs ? `更新于 ${new Date(updatedMs).toLocaleTimeString()}` : ""}
          {error ? ` · ${error}` : ""}
        </Text>
      </div>
      <Text className="cm-muted" style={{ display: "block", marginBottom: 8 }}>
        条件（已收盘 K 线）：EMA100 &gt; EMA200、EMA200 比 10 根前高；K 线最低点碰到 EMA100 或 EMA200（离线 0.3% 以内或插到线下），收盘仍在线上方，且之前 10 根收盘都在这条线上方。两条线都碰到时显示更深的 EMA200（黄色）。百分比是收盘价离这条线的距离。命中周期多的排前面。
      </Text>
      <NotifyPanel settings={notify} onSave={saveNotify} />
      <Table rowKey="symbol" loading={loading} columns={columns} data={items} pagination={false} scroll={{ x: true }} border={false} />
    </div>
  );
}

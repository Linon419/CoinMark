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
  close: number;
  lower: number;
  upper: number;
  ema100: number;
  ema200: number;
  percent_b: number;
  bandwidth: number;
  bandwidth_max20: number;
};
type Row = { symbol: string; quote_volume_24h: number; hits: Hit[] };

function HitCell({ hit }: { hit?: Hit }) {
  if (!hit) return <Text className="cm-muted">—</Text>;
  const title = `收盘 ${hit.close}｜下轨 ${hit.lower.toPrecision(6)}｜EMA200 ${hit.ema200.toPrecision(6)}｜带宽 ${(hit.bandwidth * 100).toFixed(1)}%（近20根最大 ${(hit.bandwidth_max20 * 100).toFixed(1)}%）`;
  return (
    <span title={title} style={{ color: "var(--cm-success)", fontVariantNumeric: "tabular-nums" }}>
      %B {hit.percent_b.toFixed(2)}
    </span>
  );
}

// BollSqueezePage 布林回踩：上涨趋势中 BOLL 缩口、价格接近下轨的合约。
export default function BollSqueezePage() {
  const [items, setItems] = useState<Row[]>([]);
  const [updatedMs, setUpdatedMs] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const { settings: notify, save: saveNotify, isFav, toggleFav } = usePullbackNotify("/api/boll-squeeze/notify");

  useEffect(() => {
    let alive = true;
    const load = async () => {
      try {
        const r = await fetch(`${API_BASE}/api/boll-squeeze`);
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
          布林回踩
        </Title>
        <Text className="cm-muted">
          {updatedMs ? `更新于 ${new Date(updatedMs).toLocaleTimeString()}` : ""}
          {error ? ` · ${error}` : ""}
        </Text>
      </div>
      <Text className="cm-muted" style={{ display: "block", marginBottom: 8 }}>
        条件（已收盘 K 线）：EMA100 &gt; EMA200、EMA200 比 10 根前高、收盘在 EMA200 上方；BOLL(20,2) 带宽比 3 根前窄且 ≤ 近 20 根最大带宽的 80%；收盘 %B 在 -0.2~0.35（0 = 下轨，1 = 上轨）。命中周期多的排前面。
      </Text>
      <NotifyPanel settings={notify} onSave={saveNotify} />
      <Table rowKey="symbol" loading={loading} columns={columns} data={items} pagination={false} scroll={{ x: true }} border={false} />
    </div>
  );
}

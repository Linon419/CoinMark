import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Button, Checkbox, Input, Message, Switch, Table, Tag, Typography } from "@arco-design/web-react";
import { fmtUsdCn } from "./etfFlows";

const { Title, Text } = Typography;
const API_BASE = (import.meta as any).env?.VITE_API_BASE || "";
const REFRESH_MS = 60 * 1000; // 后端每分钟扫一次
const TIMEFRAMES = ["15m", "30m", "1h", "4h"] as const;

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
type NotifySettings = { enabled: boolean; timeframes: string[]; favorites: string[]; tg_configured?: boolean };

function HitCell({ hit }: { hit?: Hit }) {
  if (!hit) return <Text className="cm-muted">—</Text>;
  const title = `收盘 ${hit.close}｜下轨 ${hit.lower.toPrecision(6)}｜EMA200 ${hit.ema200.toPrecision(6)}｜带宽 ${(hit.bandwidth * 100).toFixed(1)}%（近20根最大 ${(hit.bandwidth_max20 * 100).toFixed(1)}%）`;
  return (
    <span title={title} style={{ color: "var(--cm-success)", fontVariantNumeric: "tabular-nums" }}>
      %B {hit.percent_b.toFixed(2)}
    </span>
  );
}

// NotifyPanel 收藏币 + 通知周期 + Telegram 开关；只通知收藏的币，同一币同一周期刚进入条件时推一次。
function NotifyPanel({ settings, onSave }: { settings: NotifySettings | null; onSave: (next: NotifySettings) => void }) {
  const [input, setInput] = useState("");
  if (!settings) return null;
  const add = () => {
    const sym = input.trim().toUpperCase();
    if (!sym) return;
    onSave({ ...settings, favorites: [...settings.favorites, sym] });
    setInput("");
  };
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 8, marginBottom: 12 }}>
      <div style={{ display: "flex", flexWrap: "wrap", alignItems: "center", gap: 12 }}>
        <span style={{ display: "inline-flex", alignItems: "center", gap: 6 }}>
          <Switch size="small" checked={settings.enabled} onChange={(v) => onSave({ ...settings, enabled: v })} />
          <Text>Telegram 通知</Text>
        </span>
        <span style={{ display: "inline-flex", alignItems: "center", gap: 6 }}>
          <Text className="cm-muted">周期</Text>
          <Checkbox.Group options={[...TIMEFRAMES]} value={settings.timeframes} onChange={(v) => onSave({ ...settings, timeframes: v as string[] })} />
        </span>
        {settings.tg_configured === false && <Text style={{ color: "var(--cm-danger)" }}>服务器未配置 Telegram 通知群，暂时发不出去</Text>}
      </div>
      <div style={{ display: "flex", flexWrap: "wrap", alignItems: "center", gap: 6 }}>
        <Text className="cm-muted">收藏</Text>
        {settings.favorites.length === 0 && <Text className="cm-muted">（还没有，点表格里的 ☆ 或在右边输入）</Text>}
        {settings.favorites.map((sym) => (
          <Tag key={sym} closable onClose={() => onSave({ ...settings, favorites: settings.favorites.filter((f) => f !== sym) })}>
            {sym.replace(/USDT$/, "")}
          </Tag>
        ))}
        <Input size="small" style={{ width: 120 }} placeholder="如 ZEC" value={input} onChange={setInput} onPressEnter={add} />
        <Button size="small" onClick={add}>
          添加
        </Button>
      </div>
      <Text className="cm-muted">只通知收藏的币：在勾选的周期上，最新一根收盘 K 线刚满足条件（前一根不满足）时推送一次。</Text>
    </div>
  );
}

// BollSqueezePage 布林回踩：上涨趋势中 BOLL 缩口、价格接近下轨的合约。
export default function BollSqueezePage() {
  const [items, setItems] = useState<Row[]>([]);
  const [updatedMs, setUpdatedMs] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [notify, setNotify] = useState<NotifySettings | null>(null);

  useEffect(() => {
    fetch(`${API_BASE}/api/boll-squeeze/notify`)
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then(setNotify)
      .catch(() => setNotify(null));
  }, []);

  const saveNotify = async (next: NotifySettings) => {
    try {
      const r = await fetch(`${API_BASE}/api/boll-squeeze/notify`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled: next.enabled, timeframes: next.timeframes, favorites: next.favorites }),
      });
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      setNotify(await r.json());
    } catch (e: any) {
      Message.error(`保存失败：${e?.message || e}`);
    }
  };
  const isFav = (sym: string) => !!notify?.favorites.includes(sym);
  const toggleFav = (sym: string) => {
    if (!notify) return;
    saveNotify({ ...notify, favorites: isFav(sym) ? notify.favorites.filter((f) => f !== sym) : [...notify.favorites, sym] });
  };

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
          <span
            role="button"
            title={isFav(v) ? "取消收藏" : "收藏（收藏的币会推送 Telegram 通知）"}
            onClick={() => toggleFav(v)}
            style={{ cursor: "pointer", marginRight: 6, color: isFav(v) ? "var(--cm-warning)" : "var(--cm-muted)" }}
          >
            {isFav(v) ? "★" : "☆"}
          </span>
          <Link to={`/coin/${v}`}>{v}</Link>
        </span>
      ),
    },
    ...TIMEFRAMES.map((tf) => ({
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

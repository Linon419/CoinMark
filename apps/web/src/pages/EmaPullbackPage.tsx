import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { Pagination, Radio, Table, Tag, Typography } from "@arco-design/web-react";
import { fmtUsdCn } from "./etfFlows";
import { FavStar, NotifyPanel, PULLBACK_TIMEFRAMES, usePullbackNotify } from "../components/PullbackNotify";

const { Title, Text } = Typography;
const API_BASE = (import.meta as any).env?.VITE_API_BASE || "";
const REFRESH_MS = 60 * 1000; // 后端每 15 分钟扫一次（整刻钟后 30 秒），页面每分钟取一次最新结果
const CANDIDATE_PAGE_SIZE = 12;

type Side = "long" | "short";
type Hit = {
  side: Side;
  timeframe: string;
  candle_start_ms: number;
  line: "EMA100" | "EMA200";
  close: number;
  low: number;
  high: number;
  ema100: number;
  ema200: number;
  distance_pct: number;
  wick_ratio: number;
  score: number;
  reasons: string[] | null;
};
type Row = { symbol: string; quote_volume_24h: number; hits: Hit[]; score: number };

const pctText = (v: number) => `${v >= 0 ? "+" : ""}${(v * 100).toFixed(2)}%`;

function HitCell({ hit }: { hit?: Hit }) {
  if (!hit) return <Text className="cm-muted">—</Text>;
  const extreme = hit.side === "short" ? `最高 ${hit.high}` : `最低 ${hit.low}`;
  const title = `收盘 ${hit.close}｜${extreme}｜EMA100 ${hit.ema100.toPrecision(6)}｜EMA200 ${hit.ema200.toPrecision(6)}｜${hit.score} 分 ${(hit.reasons || []).join("、")}`;
  return (
    <span title={title} style={{ whiteSpace: "nowrap", fontVariantNumeric: "tabular-nums", color: hit.line === "EMA200" ? "var(--cm-warning)" : "var(--cm-success)" }}>
      {hit.line} {pctText(hit.distance_pct)} · {hit.score}
    </span>
  );
}

// EmaPullbackPage EMA 回踩：上涨趋势中回落到 EMA100 / EMA200 并收在线上方（做多）；
// 下跌趋势中反抽到 EMA100 / EMA200 并收在线下方（做空）。上方是按分数排的当前可交易候选。
export default function EmaPullbackPage() {
  const [items, setItems] = useState<Row[]>([]);
  const [updatedMs, setUpdatedMs] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [side, setSide] = useState<Side>("long");
  const [page, setPage] = useState(1);
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

  // 只保留当前方向的命中，按最高分排
  const rows = useMemo(
    () =>
      items
        .map((r) => {
          const hits = r.hits.filter((h) => h.side === side);
          return { ...r, hits, score: Math.max(0, ...hits.map((h) => h.score)) };
        })
        .filter((r) => r.hits.length > 0)
        .sort((a, b) => b.score - a.score || b.hits.length - a.hits.length || b.quote_volume_24h - a.quote_volume_24h),
    [items, side],
  );
  // 候选：每个币每个周期一张卡片，按分数排
  const candidates = useMemo(
    () => rows.flatMap((r) => r.hits.map((h) => ({ symbol: r.symbol, hit: h }))).sort((a, b) => b.hit.score - a.hit.score || a.symbol.localeCompare(b.symbol)),
    [rows],
  );
  const pageCount = Math.max(1, Math.ceil(candidates.length / CANDIDATE_PAGE_SIZE));
  const curPage = Math.min(page, pageCount);
  const pagedCandidates = candidates.slice((curPage - 1) * CANDIDATE_PAGE_SIZE, curPage * CANDIDATE_PAGE_SIZE);

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
    { title: "分数", sorter: (a: Row, b: Row) => a.score - b.score, render: (_: any, r: Row) => <strong className="cm-mono">{r.score}</strong> },
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
        <span style={{ display: "inline-flex", alignItems: "center", gap: 8 }}>
          <Radio.Group
            type="button"
            size="small"
            value={side}
            onChange={(v) => {
              setSide(v);
              setPage(1);
            }}
          >
            <Radio value="long">做多</Radio>
            <Radio value="short">做空</Radio>
          </Radio.Group>
          <Text className="cm-muted">
            {updatedMs ? `更新于 ${new Date(updatedMs).toLocaleTimeString()}` : ""}
            {error ? ` · ${error}` : ""}
          </Text>
        </span>
      </div>
      <Text className="cm-muted" style={{ display: "block", marginBottom: 8 }}>
        {side === "long"
          ? "条件（已收盘 K 线）：EMA100 > EMA200、EMA200 比 10 根前高；K 线最低点碰到 EMA100 或 EMA200（离线 0.3% 以内或插到线下），收盘仍在线上方，且之前 10 根收盘都在这条线上方。"
          : "条件（已收盘 K 线）：EMA100 < EMA200、EMA200 比 10 根前低；K 线最高点碰到 EMA100 或 EMA200（离线 0.3% 以内或冲到线上），收盘仍在线下方，且之前 10 根收盘都在这条线下方。"}
        两条线都碰到时显示更深的 EMA200（黄色）。百分比是收盘价离这条线的距离，后面是分数。
        <br />
        分数：基础 60；同方向每多一个周期命中 +10（最多 +20）；上一级周期（15m→30m→1h→4h）同方向趋势 +10；触线那根{side === "long" ? "下" : "上"}影线 ≥ 整根一半 +10。只看当前这根收盘 K 线，下一根不再触线就会消失。
      </Text>
      <div className="cm-bollTradeBand">
        <div className="cm-sectionHeader">
          <Title heading={6} style={{ margin: 0 }}>
            当前可交易候选
          </Title>
          <Text className="cm-muted">{candidates.length} 个 · 85 分以上为重点</Text>
        </div>
        {candidates.length > 0 ? (
          <>
            <div className="cm-bollCandidateGrid">
              {pagedCandidates.map(({ symbol, hit }) => (
                <div className="cm-bollCandidateCard" key={`${symbol}-${hit.timeframe}`}>
                  <div className="cm-bollCandidateTop">
                    <Link to={`/coin/${symbol}`}>
                      <strong className="cm-symbol">{symbol}</strong>
                    </Link>
                    <Tag color={hit.score >= 85 ? "red" : "orange"}>{hit.score >= 85 ? "重点" : "可参与"}</Tag>
                  </div>
                  <div className="cm-bollCandidateMeta">
                    <span>
                      周期 <b>{hit.timeframe}</b>
                    </span>
                    <span>
                      {side === "long" ? "回踩" : "反抽"} <b>{hit.line}</b>
                    </span>
                    <span>
                      离线 <b>{pctText(hit.distance_pct)}</b>
                    </span>
                  </div>
                  <div className="cm-bollCandidateBottom">
                    <Text className="cm-muted" style={{ fontSize: 12 }}>
                      {(hit.reasons || []).join("、") || "—"}
                    </Text>
                    <strong className="cm-mono cm-bollCandidateScore">{hit.score}</strong>
                  </div>
                </div>
              ))}
            </div>
            {candidates.length > CANDIDATE_PAGE_SIZE ? (
              <div className="cm-bollCandidatePager">
                <Pagination size="small" current={curPage} pageSize={CANDIDATE_PAGE_SIZE} total={candidates.length} onChange={setPage} />
              </div>
            ) : null}
          </>
        ) : (
          <div className="cm-bollCandidateEmpty">{loading ? "加载中…" : "暂无候选"}</div>
        )}
      </div>
      <NotifyPanel settings={notify} onSave={saveNotify} />
      <Table rowKey="symbol" loading={loading} columns={columns} data={rows} pagination={false} scroll={{ x: true }} border={false} />
    </div>
  );
}

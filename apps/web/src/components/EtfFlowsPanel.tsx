import { useEffect, useState } from "react";
import { Table, Typography } from "@arco-design/web-react";
import { EtfFlowSummary, flowBars, flowTone, fmtUsdCn } from "../pages/etfFlows";

const { Title, Text } = Typography;
const API_BASE = (import.meta as any).env?.VITE_API_BASE || "";
const REFRESH_MS = 10 * 60 * 1000; // SoSoValue 在美股收盘后约 1 小时更新，无需频繁刷新

const toneColor = { up: "var(--cm-success)", down: "var(--cm-danger)", flat: "var(--cm-muted)" } as const;

function Flow({ v }: { v: number }) {
  return <span style={{ color: toneColor[flowTone(v)], fontVariantNumeric: "tabular-nums" }}>{fmtUsdCn(v)}</span>;
}

function Bars({ series }: { series: EtfFlowSummary["series"] }) {
  return (
    <div style={{ display: "flex", alignItems: "center", gap: 2, height: 32 }}>
      {flowBars(series, 14).map((b) => (
        <div key={b.date} title={`${b.date} ${fmtUsdCn(b.value)}`} style={{ display: "flex", flexDirection: "column", height: 32, width: 4 }}>
          <div style={{ flex: 1, display: "flex", alignItems: "flex-end" }}>
            {b.tone === "up" && <div style={{ width: 4, height: b.height, background: toneColor.up }} />}
          </div>
          <div style={{ flex: 1 }}>
            {b.tone !== "up" && <div style={{ width: 4, height: b.height, background: toneColor[b.tone] }} />}
          </div>
        </div>
      ))}
    </div>
  );
}

// EtfFlowsPanel 美国现货加密 ETF 每日资金净流入/流出（按币汇总全部 ETF）。
export default function EtfFlowsPanel() {
  const [items, setItems] = useState<EtfFlowSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  useEffect(() => {
    let alive = true;
    const load = async () => {
      try {
        const r = await fetch(`${API_BASE}/api/etf/flows?days=20`);
        if (!r.ok) throw new Error(`HTTP ${r.status}`);
        const d = await r.json();
        if (alive) {
          setItems(d.items || []);
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

  const latestDate = items.reduce((m, it) => (it.latest.date > m ? it.latest.date : m), "");
  const columns = [
    { title: "币", dataIndex: "asset", fixed: "left" as const, width: 80, render: (v: string) => <span style={{ whiteSpace: "nowrap", fontWeight: 600 }}>{v}</span> },
    { title: "日期", render: (_: any, r: EtfFlowSummary) => <Text className="cm-muted">{r.latest.date.slice(5)}</Text> },
    { title: "当日净流入", render: (_: any, r: EtfFlowSummary) => <Flow v={r.latest.netInflow} /> },
    { title: "近5日", render: (_: any, r: EtfFlowSummary) => <Flow v={r.sum5} /> },
    { title: "近20日", render: (_: any, r: EtfFlowSummary) => <Flow v={r.sum20} /> },
    { title: "总资产", render: (_: any, r: EtfFlowSummary) => fmtUsdCn(r.latest.netAssets, false) },
    { title: "近20日走势", render: (_: any, r: EtfFlowSummary) => <Bars series={r.series} /> },
  ];

  return (
    <div className="cm-section">
      <div className="cm-sectionHeader">
        <Title heading={6} style={{ margin: 0 }}>
          美国现货 ETF 资金流
        </Title>
        <Text className="cm-muted">
          {error ? `加载失败：${error}` : latestDate ? `最新交易日 ${latestDate}（美东），数据来源 SoSoValue` : "—"}
        </Text>
      </div>
      <div className="cm-card" style={{ padding: 12 }}>
        <div className="cm-table">
          <Table
            loading={loading}
            rowKey="asset"
            pagination={false}
            size="small"
            columns={columns as any}
            data={items}
            scroll={{ x: "max-content" }}
          />
        </div>
      </div>
    </div>
  );
}

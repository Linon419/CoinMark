import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { Button, Checkbox, Select, Space, Table, Tag, Typography } from "@arco-design/web-react";

type Market = "spot" | "swap";

type HotItem = {
  id: number;
  symbol: string;
  eventType: string;
  tfSignal: string;
  tfLevel: string | null;
  eventTimeMs: number;
  title: string;
  details: any;
  severityScore?: number;
  severityLevel?: "info" | "warning" | "critical";
  narrative?: string;
  firstSeenInWindow?: boolean;
};

type AbsorptionSignalItem = {
  symbol: string;
  signalState: "NONE" | "WATCH" | "CONFIRM" | "STRONG";
  direction: "LONG_BIAS";
  score: number;
  windows: Record<string, { passed: boolean; score: number }>;
  reasons: string[];
  ts: number;
};

type InstitutionalLevelItem = {
  symbol: string;
  market: "spot" | "swap";
  zoneType: "bid" | "ask";
  zoneLow: number | null;
  zoneHigh: number | null;
  realScore: number;
  state: "NONE" | "WATCH" | "CONFIRM" | "STRONG";
  reasons: string[];
  ts: number;
};

const API_BASE = (import.meta as any).env?.VITE_API_BASE || "";

async function getJson<T>(path: string): Promise<T> {
  const r = await fetch(`${API_BASE}${path}`);
  if (!r.ok) throw new Error(`HTTP ${r.status}`);
  return (await r.json()) as T;
}

const EVENT_TYPES = [
  { label: "全部", value: "" },
  { label: "突破", value: "breakout_up" },
  { label: "跌破", value: "breakout_down" },
  { label: "量能异常", value: "volume_spike" },
  { label: "振幅异常", value: "amplitude_spike" },
];

const SINCE_OPTIONS = [
  { label: "最近 6 小时", value: 360 },
  { label: "最近 24 小时", value: 1440 },
  { label: "最近 3 天", value: 4320 },
  { label: "最近 7 天", value: 10080 },
];

function levelTagColor(level?: "info" | "warning" | "critical") {
  if (level === "critical") return "red";
  if (level === "warning") return "orange";
  return "arcoblue";
}

function levelLabel(level?: "info" | "warning" | "critical") {
  if (level === "critical") return "高危";
  if (level === "warning") return "关注";
  return "提示";
}

export default function AnomaliesPage() {
  const { Title, Text } = Typography;
  const [market, setMarket] = useState<Market>("swap");
  const [sinceMinutes, setSinceMinutes] = useState<number>(360);
  const [eventType, setEventType] = useState<string>("");
  const [items, setItems] = useState<HotItem[]>([]);
  const [absorptionItems, setAbsorptionItems] = useState<AbsorptionSignalItem[]>([]);
  const [institutionalItems, setInstitutionalItems] = useState<InstitutionalLevelItem[]>([]);
  const [includeShortBias, setIncludeShortBias] = useState(false);
  const [stats, setStats] = useState<Record<string, number>>({});
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
      const listPath =
        `/api/aggregate/hotMarkets?market=${market}&limit=100&sinceMinutes=${sinceMinutes}` +
        (eventType ? `&eventType=${eventType}` : "");
      const direction = includeShortBias ? "all" : "long";
      const absorptionLookbackMinutes = Math.max(sinceMinutes, 3 * 24 * 60);
      const [list, stat, absorption, institutional] = await Promise.all([
        safeGet<{ items: HotItem[] }>(listPath, { items: [] }),
        safeGet<{ counts: Record<string, number> }>(
          `/api/aggregate/anomalyStats?market=${market}&sinceMinutes=${sinceMinutes}`,
          { counts: {} }
        ),
        safeGet<{ items: AbsorptionSignalItem[] }>(
          `/api/aggregate/orderbookAbsorptionSignals?market=${market}&limit=100&onlySignals=1&signalLookbackMinutes=${absorptionLookbackMinutes}&direction=${direction}`,
          { items: [] }
        ),
        safeGet<{ items: InstitutionalLevelItem[] }>(
          `/api/aggregate/orderbookInstitutionalLevels?market=both&limit=100&state=WATCH&lookbackMinutes=360`,
          { items: [] }
        ),
      ]);
      const ordered = [...(list.items || [])].sort((a, b) => {
        const scoreDiff = Number(b.severityScore || 0) - Number(a.severityScore || 0);
        if (scoreDiff !== 0) return scoreDiff;
        return Number(b.eventTimeMs || 0) - Number(a.eventTimeMs || 0);
      });
      setItems(ordered);
      setStats(stat.counts || {});
      setAbsorptionItems(absorption.items || []);
      setInstitutionalItems(institutional.items || []);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    refresh();
  }, [market, sinceMinutes, eventType, includeShortBias]);

  useEffect(() => {
    const timer = setInterval(() => {
      if (document.visibilityState === "visible") {
        void refresh();
      }
    }, 15_000);
    return () => clearInterval(timer);
  }, [market, sinceMinutes, eventType, includeShortBias]);

  const columns = useMemo(
    () => [
      {
        title: "时间",
        render: (_: any, r: HotItem) => new Date(r.eventTimeMs).toLocaleString(),
      },
      {
        title: "等级",
        render: (_: any, r: HotItem) => (
          <Space>
            <Tag color={levelTagColor(r.severityLevel)}>{levelLabel(r.severityLevel)}</Tag>
            <span style={{ fontWeight: 700 }}>{Number(r.severityScore || 0).toFixed(1)}</span>
          </Space>
        ),
      },
      {
        title: "Symbol",
        render: (_: any, r: HotItem) => <Link to={`/coin/${r.symbol}`}>{r.symbol}</Link>,
      },
      {
        title: "类型",
        render: (_: any, r: HotItem) => <Tag color="orange">{r.eventType}</Tag>,
      },
      {
        title: "叙事",
        render: (_: any, r: HotItem) => (
          <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
            <span>{r.narrative || r.title}</span>
            <span className="cm-muted">
              信号：{r.tfSignal}
              {r.tfLevel ? ` | 水平：${r.tfLevel}` : ""}
              {r.firstSeenInWindow ? " | 窗口首次" : ""}
            </span>
          </div>
        ),
      },
      {
        title: "细节",
        render: (_: any, r: HotItem) => {
          const d = r.details || {};
          const tags: string[] = [];
          if (d.levelPrice) tags.push(`水平 ${d.levelPrice}`);
          if (d.volumeFactor) tags.push(`量能x${Number(d.volumeFactor).toFixed(2)}`);
          if (d.amplitude) tags.push(`振幅 ${(Number(d.amplitude) * 100).toFixed(2)}%`);
          if (d.marginPct) tags.push(`margin ${(Number(d.marginPct) * 100).toFixed(2)}%`);
          return (
            <Space wrap>
              {tags.length === 0 && <span className="cm-muted">-</span>}
              {tags.map((t) => (
                <Tag key={t} color="cyan">
                  {t}
                </Tag>
              ))}
            </Space>
          );
        },
      },
    ],
    []
  );

  const absorptionColumns = useMemo(
    () => [
      {
        title: "时间",
        render: (_: any, r: AbsorptionSignalItem) => new Date(r.ts).toLocaleString(),
      },
      {
        title: "Symbol",
        render: (_: any, r: AbsorptionSignalItem) => <Link to={`/coin/${r.symbol}`}>{r.symbol}</Link>,
      },
      {
        title: "状态",
        render: (_: any, r: AbsorptionSignalItem) => {
          const color = r.signalState === "STRONG" ? "red" : r.signalState === "CONFIRM" ? "orange" : r.signalState === "WATCH" ? "blue" : "gray";
          return <Tag color={color}>{r.signalState}</Tag>;
        },
      },
      {
        title: "方向",
        render: (_: any, r: AbsorptionSignalItem) => (
          <Tag color={r.direction === "LONG_BIAS" ? "green" : "red"}>{r.direction}</Tag>
        ),
      },
      {
        title: "分数",
        render: (_: any, r: AbsorptionSignalItem) => <span style={{ fontWeight: 700 }}>{r.score}</span>,
      },
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
      {
        title: "依据",
        render: (_: any, r: AbsorptionSignalItem) => <span className="cm-muted">{(r.reasons || []).join("；")}</span>,
      },
    ],
    []
  );

  const institutionalColumns = useMemo(
    () => [
      {
        title: "时间",
        render: (_: any, r: InstitutionalLevelItem) => new Date(r.ts).toLocaleString(),
      },
      {
        title: "Symbol",
        render: (_: any, r: InstitutionalLevelItem) => <Link to={`/coin/${r.symbol}`}>{r.symbol}</Link>,
      },
      {
        title: "市场",
        render: (_: any, r: InstitutionalLevelItem) => <Tag color={r.market === "swap" ? "purple" : "green"}>{r.market}</Tag>,
      },
      {
        title: "状态",
        render: (_: any, r: InstitutionalLevelItem) => {
          const color = r.state === "STRONG" ? "red" : r.state === "CONFIRM" ? "orange" : r.state === "WATCH" ? "blue" : "gray";
          return <Tag color={color}>{r.state}</Tag>;
        },
      },
      {
        title: "分数",
        render: (_: any, r: InstitutionalLevelItem) => <span style={{ fontWeight: 700 }}>{Math.round(r.realScore)}</span>,
      },
      {
        title: "区间",
        render: (_: any, r: InstitutionalLevelItem) => {
          const left = typeof r.zoneLow === "number" ? r.zoneLow.toFixed(6) : "-";
          const right = typeof r.zoneHigh === "number" ? r.zoneHigh.toFixed(6) : "-";
          return (
            <span className="cm-muted">
              {r.zoneType.toUpperCase()} [{left}, {right}]
            </span>
          );
        },
      },
      {
        title: "依据",
        render: (_: any, r: InstitutionalLevelItem) => <span className="cm-muted">{(r.reasons || []).join("，")}</span>,
      },
    ],
    []
  );

  return (
    <div className="cm-page">
      <div className="cm-section">
        <div className="cm-sectionHeader">
          <Title heading={5} style={{ margin: 0 }}>
            异动统计
          </Title>
          <Space>
            <Select value={market} onChange={(v) => setMarket(v as Market)} style={{ width: 120 }}>
              <Select.Option value="swap">合约（swap）</Select.Option>
              <Select.Option value="spot">现货（spot）</Select.Option>
            </Select>
            <Select value={sinceMinutes} onChange={(v) => setSinceMinutes(v as number)} style={{ width: 140 }}>
              {SINCE_OPTIONS.map((o) => (
                <Select.Option key={o.value} value={o.value}>
                  {o.label}
                </Select.Option>
              ))}
            </Select>
            <Select value={eventType} onChange={(v) => setEventType(v as string)} style={{ width: 140 }}>
              {EVENT_TYPES.map((o) => (
                <Select.Option key={o.value || "all"} value={o.value}>
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
        <div className="cm-card" style={{ padding: 12 }}>
          <div className="cm-sectionHeader">
            <Title heading={6} style={{ margin: 0 }}>
              事件统计
            </Title>
            <Text className="cm-muted">时间窗：{sinceMinutes} 分钟</Text>
          </div>
          <Space wrap>
            {Object.entries(stats).length === 0 && <Text className="cm-muted">暂无统计</Text>}
            {Object.entries(stats).map(([k, v]) => (
              <Tag key={k} color="arcoblue">
                {k}: {v}
              </Tag>
            ))}
          </Space>
        </div>
      </div>

      <div className="cm-section">
        <div className="cm-card" style={{ padding: 12 }}>
          <div className="cm-sectionHeader">
            <Title heading={6} style={{ margin: 0 }}>
              吸筹信号扫描（全币）
            </Title>
            <Space>
              <Checkbox checked={includeShortBias} onChange={setIncludeShortBias}>
                包含 SHORT_BIAS（派发）
              </Checkbox>
              <Text className="cm-muted">默认仅 LONG_BIAS（WATCH/CONFIRM/STRONG）</Text>
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
            />
          </div>

          <div className="cm-sectionHeader">
            <Title heading={6} style={{ margin: 0 }}>
              真实挂单位置扫描（全币）
            </Title>
            <Text className="cm-muted">市场固定 both；仅显示 WATCH/CONFIRM/STRONG</Text>
          </div>
          <div className="cm-table" style={{ marginBottom: 12 }}>
            <Table
              rowKey={(r) => `${(r as InstitutionalLevelItem).market}-${(r as InstitutionalLevelItem).symbol}-${(r as InstitutionalLevelItem).zoneType}-${(r as InstitutionalLevelItem).ts}`}
              loading={loading}
              pagination={false}
              size="small"
              columns={institutionalColumns as any}
              data={institutionalItems}
            />
          </div>

          <div className="cm-sectionHeader">
            <Title heading={6} style={{ margin: 0 }}>
              事件列表
            </Title>
            <Text className="cm-muted">Top 100（按时间倒序）</Text>
          </div>
          <div className="cm-table">
            <Table rowKey="id" loading={loading} pagination={false} size="small" columns={columns as any} data={items} />
          </div>
        </div>
      </div>
    </div>
  );
}

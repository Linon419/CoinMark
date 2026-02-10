import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { Button, Checkbox, Select, Space, Table, Tag, Typography } from "@arco-design/web-react";

type Market = "spot" | "swap";

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

const SINCE_OPTIONS = [
  { label: "最近 6 小时", value: 360 },
  { label: "最近 24 小时", value: 1440 },
  { label: "最近 3 天", value: 4320 },
  { label: "最近 7 天", value: 10080 },
];

export default function AnomaliesPage() {
  const { Title, Text } = Typography;
  const [market, setMarket] = useState<Market>("swap");
  const [sinceMinutes, setSinceMinutes] = useState<number>(360);
  const [absorptionItems, setAbsorptionItems] = useState<AbsorptionSignalItem[]>([]);
  const [institutionalItems, setInstitutionalItems] = useState<InstitutionalLevelItem[]>([]);
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
      const [absorption, institutional] = await Promise.all([
        safeGet<{ items: AbsorptionSignalItem[] }>(
          `/api/aggregate/orderbookAbsorptionSignals?market=${market}&limit=100&onlySignals=1&signalLookbackMinutes=${absorptionLookbackMinutes}&direction=${direction}`,
          { items: [] }
        ),
        safeGet<{ items: InstitutionalLevelItem[] }>(
          `/api/aggregate/orderbookInstitutionalLevels?market=both&limit=100&state=CONFIRM&lookbackMinutes=360`,
          { items: [] }
        ),
      ]);
      setAbsorptionItems(absorption.items || []);
      setInstitutionalItems(institutional.items || []);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    refresh();
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
            信号扫描
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
            />
          </div>

          <div className="cm-sectionHeader">
            <Title heading={6} style={{ margin: 0 }}>
              真实挂单位置扫描（全币）
            </Title>
            <Text className="cm-muted">市场固定 both；仅显示 CONFIRM/STRONG（已告警）</Text>
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

        </div>
      </div>
    </div>
  );
}

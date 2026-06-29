import { useEffect, useMemo, useState } from "react";
import { Button, Drawer, Space, Table, Tag, Typography } from "@arco-design/web-react";
import BollPumpChart from "../components/BollPumpChart";
import {
  fetchBollPumpDetail,
  fetchBollPumpSignals,
  fetchBollPumpStates,
  fetchBollPumpStats,
  type BollPumpDetail,
  type BollPumpSignal,
  type BollPumpState,
} from "../services/bollPump";

function levelColor(level: string) {
  if (level === "CONFIRM_2") return "red";
  if (level === "CONFIRM_1") return "orange";
  return "arcoblue";
}

function fmtNum(v: number, digits = 2) {
  if (!Number.isFinite(v)) return "-";
  return v.toFixed(digits);
}

export default function BollPumpPage() {
  const { Title, Text } = Typography;
  const [signals, setSignals] = useState<BollPumpSignal[]>([]);
  const [states, setStates] = useState<BollPumpState[]>([]);
  const [stats, setStats] = useState<any>(null);
  const [detail, setDetail] = useState<BollPumpDetail | null>(null);
  const [open, setOpen] = useState(false);
  const [loading, setLoading] = useState(false);

  const refresh = async () => {
    setLoading(true);
    try {
      const [sig, st, stat] = await Promise.all([
        fetchBollPumpSignals(),
        fetchBollPumpStates("market=swap&limit=100&min_priority_score=60"),
        fetchBollPumpStats(),
      ]);
      setSignals(sig.items || []);
      setStates(st.items || []);
      setStats(stat);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void refresh();
    const timer = setInterval(() => {
      if (document.visibilityState === "visible") void refresh();
    }, 15000);
    return () => clearInterval(timer);
  }, []);

  const signalColumns = useMemo(
    () => [
      { title: "时间", render: (_: any, r: BollPumpSignal) => new Date(r.signal_time_ms).toLocaleString() },
      { title: "Symbol", dataIndex: "symbol" },
      { title: "周期", dataIndex: "timeframe" },
      { title: "等级", render: (_: any, r: BollPumpSignal) => <Tag color={levelColor(r.signal_level)}>{r.signal_level}</Tag> },
      { title: "价格", render: (_: any, r: BollPumpSignal) => Number(r.price).toPrecision(6) },
      { title: "量能", render: (_: any, r: BollPumpSignal) => `${fmtNum(Number(r.volume_ratio || 0))}x` },
      { title: "带宽", render: (_: any, r: BollPumpSignal) => fmtNum(Number(r.boll_bandwidth || 0), 4) },
      { title: "分数", render: (_: any, r: BollPumpSignal) => fmtNum(Number(r.priority_score || r.score || 0), 0) },
      {
        title: "操作",
        render: (_: any, r: BollPumpSignal) => (
          <Button
            size="small"
            onClick={async () => {
              setDetail(await fetchBollPumpDetail(r.id));
              setOpen(true);
            }}
          >
            详情
          </Button>
        ),
      },
    ],
    []
  );

  const stateColumns = useMemo(
    () => [
      { title: "Symbol", dataIndex: "symbol" },
      { title: "周期", dataIndex: "timeframe" },
      { title: "状态", render: (_: any, r: BollPumpState) => <Tag color={levelColor(r.status)}>{r.status}</Tag> },
      { title: "反弹", dataIndex: "bounce_count" },
      { title: "共振", render: (_: any, r: BollPumpState) => `+${fmtNum(Number(r.confluence_score || 0), 0)}` },
      { title: "优先级", render: (_: any, r: BollPumpState) => fmtNum(Number(r.priority_score || 0), 0) },
    ],
    []
  );

  const countsByLevel = stats?.countsByLevel || {};
  const countsByTimeframe = stats?.countsByTimeframe || {};

  return (
    <div className="cm-page">
      <div className="cm-section">
        <div className="cm-sectionHeader">
          <div>
            <Title heading={5} style={{ margin: 0 }}>
              BOLL 泵盘扫描器
            </Title>
            <Text className="cm-muted">swap | 1m / 3m / 5m / 15m / 30m / 1h</Text>
          </div>
          <Button loading={loading} onClick={refresh}>
            刷新
          </Button>
        </div>
        <Space wrap>
          <span className="cm-pill">信号 {signals.length}</span>
          <span className="cm-pill">活跃 {states.length}</span>
          <span className="cm-pill">WATCH {countsByLevel.WATCH || 0}</span>
          <span className="cm-pill">CONFIRM_1 {countsByLevel.CONFIRM_1 || 0}</span>
          <span className="cm-pill">CONFIRM_2 {countsByLevel.CONFIRM_2 || 0}</span>
          <span className="cm-pill">更新时间 {stats?.generatedAtMs ? new Date(stats.generatedAtMs).toLocaleTimeString() : "-"}</span>
        </Space>
      </div>

      <div className="cm-card cm-bollStats">
        {["1m", "3m", "5m", "15m", "30m", "1h"].map((tf) => (
          <div key={tf} className="cm-bollStatItem">
            <span className="cm-muted">{tf}</span>
            <strong>{countsByTimeframe[tf] || 0}</strong>
          </div>
        ))}
      </div>

      <div className="cm-grid-2">
        <div className="cm-card cm-bollPanel">
          <div className="cm-sectionHeader">
            <Title heading={6} style={{ margin: 0 }}>
              最近信号
            </Title>
          </div>
          <Table rowKey="id" size="small" columns={signalColumns as any} data={signals as any} pagination={{ pageSize: 20 }} />
        </div>
        <div className="cm-card cm-bollPanel">
          <div className="cm-sectionHeader">
            <Title heading={6} style={{ margin: 0 }}>
              活跃状态
            </Title>
          </div>
          <Table rowKey="id" size="small" columns={stateColumns as any} data={states as any} pagination={{ pageSize: 20 }} />
        </div>
      </div>

      <Drawer width="80%" visible={open} onCancel={() => setOpen(false)} footer={null} title={detail?.signal ? `${detail.signal.symbol} ${detail.signal.timeframe}` : "BOLL detail"}>
        <Text className="cm-muted">{detail?.signal?.reason || ""}</Text>
        <BollPumpChart detail={detail} />
      </Drawer>
    </div>
  );
}

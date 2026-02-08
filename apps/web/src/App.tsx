import { useEffect, useState } from "react";
import { NavLink, Outlet } from "react-router-dom";
import { Space, Switch, Typography } from "@arco-design/web-react";
import NotificationCenter from "./components/NotificationCenter";
import { useNotificationCenter } from "./stores/notificationCenter";

type ThemeMode = "light" | "dark";

const API_BASE = (import.meta as any).env?.VITE_API_BASE || "";

function getInitialTheme(): ThemeMode {
  const saved = localStorage.getItem("coinmark_theme");
  if (saved === "light" || saved === "dark") return saved;
  return window.matchMedia?.("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

function applyTheme(theme: ThemeMode) {
  document.documentElement.dataset.theme = theme;
}

export default function App() {
  const { Title, Text } = Typography;
  const [apiOk, setApiOk] = useState<boolean | null>(null);
  const [theme, setTheme] = useState<ThemeMode>(() => getInitialTheme());
  const { hubStatus } = useNotificationCenter();

  useEffect(() => {
    applyTheme(theme);
    localStorage.setItem("coinmark_theme", theme);
  }, [theme]);

  useEffect(() => {
    const ping = async () => {
      try {
        const r = await fetch(`${API_BASE}/healthz`);
        setApiOk(r.ok);
      } catch {
        setApiOk(false);
      }
    };
    ping();
    const t = setInterval(ping, 15000);
    return () => clearInterval(t);
  }, []);

  return (
    <div className="cm-shell">
      <div className="cm-topbar">
        <div className="cm-topbarRow">
          <div className="cm-brand">
            <div className="cm-logo" aria-hidden="true" />
            <div>
              <Title heading={4} style={{ margin: 0 }}>
                CoinMark（CoinArch）
              </Title>
              <Text className="cm-muted">数据源：Binance Spot + USDT-M Futures（指标严格可追溯）</Text>
            </div>
          </div>

          <Space size="medium" className="cm-nav">
            <NavLink to="/" end className={({ isActive }) => `cm-navLink ${isActive ? "cm-navLink--active" : ""}`}>
              主页
            </NavLink>
            <NavLink to="/intraday/BTCUSDT" className={({ isActive }) => `cm-navLink ${isActive ? "cm-navLink--active" : ""}`}>
              日内看盘
            </NavLink>
            <NavLink to="/anomalies" className={({ isActive }) => `cm-navLink ${isActive ? "cm-navLink--active" : ""}`}>
              异动统计
            </NavLink>
            <NavLink to="/signal-lab" className={({ isActive }) => `cm-navLink ${isActive ? "cm-navLink--active" : ""}`}>
              SignalLab
            </NavLink>
          </Space>

          <Space>
            <span className="cm-pill" title="API 连通状态">
              <span className={`cm-dot ${apiOk === true ? "cm-dot--ok" : apiOk === false ? "cm-dot--bad" : ""}`} />
              <span className="cm-muted">API</span>
              <span className="cm-number--mono">{apiOk === null ? "检测中" : apiOk ? "在线" : "离线"}</span>
            </span>
            <span className="cm-pill" title="Hub 连接状态">
              <span className={`cm-dot ${hubStatus === "connected" ? "cm-dot--ok" : "cm-dot--bad"}`} />
              <span className="cm-muted">Hub</span>
              <span className="cm-number--mono">{hubStatus === "connected" ? "在线" : hubStatus === "reconnecting" ? "重连" : hubStatus === "connecting" ? "连接" : "离线"}</span>
            </span>
            <span className="cm-pill" title="主题">
              <span className="cm-muted">夜间</span>
              <Switch checked={theme === "dark"} onChange={(v) => setTheme(v ? "dark" : "light")} />
            </span>
            <NotificationCenter />
          </Space>
        </div>
      </div>

      <div className="cm-content">
        <Outlet />
      </div>
    </div>
  );
}

import { useEffect, useState } from "react";
import { Button, Checkbox, Input, Message, Switch, Tag, Typography } from "@arco-design/web-react";

const { Text } = Typography;
const API_BASE = (import.meta as any).env?.VITE_API_BASE || "";
export const PULLBACK_TIMEFRAMES = ["15m", "30m", "1h", "4h"] as const;

export type NotifySettings = { enabled: boolean; timeframes: string[]; favorites: string[]; tg_configured?: boolean };

// usePullbackNotify 回踩类页面的通知设置（开关、周期、收藏），apiPath 如 /api/boll-squeeze/notify。
export function usePullbackNotify(apiPath: string) {
  const [settings, setSettings] = useState<NotifySettings | null>(null);

  useEffect(() => {
    fetch(`${API_BASE}${apiPath}`)
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then(setSettings)
      .catch(() => setSettings(null));
  }, [apiPath]);

  const save = async (next: NotifySettings) => {
    try {
      const r = await fetch(`${API_BASE}${apiPath}`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled: next.enabled, timeframes: next.timeframes, favorites: next.favorites }),
      });
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      setSettings(await r.json());
    } catch (e: any) {
      Message.error(`保存失败：${e?.message || e}`);
    }
  };
  const isFav = (sym: string) => !!settings?.favorites.includes(sym);
  const toggleFav = (sym: string) => {
    if (!settings) return;
    save({ ...settings, favorites: isFav(sym) ? settings.favorites.filter((f) => f !== sym) : [...settings.favorites, sym] });
  };
  return { settings, save, isFav, toggleFav };
}

// FavStar 表格里的收藏星标。
export function FavStar({ active, onClick }: { active: boolean; onClick: () => void }) {
  return (
    <span
      role="button"
      title={active ? "取消收藏" : "收藏（收藏的币会推送 Telegram 通知）"}
      onClick={onClick}
      style={{ cursor: "pointer", marginRight: 6, color: active ? "var(--cm-warning)" : "var(--cm-muted)" }}
    >
      {active ? "★" : "☆"}
    </span>
  );
}

// NotifyPanel 收藏币 + 通知周期 + Telegram 开关；只通知收藏的币，同一币同一周期刚进入条件时推一次。
export function NotifyPanel({ settings, onSave }: { settings: NotifySettings | null; onSave: (next: NotifySettings) => void }) {
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
          <Checkbox.Group options={[...PULLBACK_TIMEFRAMES]} value={settings.timeframes} onChange={(v) => onSave({ ...settings, timeframes: v as string[] })} />
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

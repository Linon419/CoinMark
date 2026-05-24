import { useEffect, useState } from "react";
import { Badge, Button, Drawer, Empty, Message, Space, Switch, Tag, Typography } from "@arco-design/web-react";
import { IconNotification } from "@arco-design/web-react/icon";
import { useNotificationCenter } from "../stores/notificationCenter";

const API_BASE = (import.meta as any).env?.VITE_API_BASE || "";

type TelegramPrefs = {
  configured: boolean;
  enabled: boolean;
  market: string;
  abnormalEventsEnabled: boolean;
  whaleWallEnabled: boolean;
  absorptionEnabled: boolean;
  muteAll: boolean;
};

type TelegramPrefsPatch = Partial<Pick<TelegramPrefs, "abnormalEventsEnabled" | "whaleWallEnabled" | "absorptionEnabled" | "muteAll">>;

async function requestTelegramPrefs(method: "GET" | "PATCH", patch?: TelegramPrefsPatch): Promise<TelegramPrefs> {
  const resp = await fetch(`${API_BASE}/api/telegram/notify-prefs`, {
    method,
    headers: method === "PATCH" ? { "Content-Type": "application/json" } : undefined,
    body: method === "PATCH" ? JSON.stringify(patch || {}) : undefined,
  });
  if (!resp.ok) {
    throw new Error(`HTTP ${resp.status}`);
  }
  return (await resp.json()) as TelegramPrefs;
}

function levelColor(level: "info" | "warning" | "critical"): "arcoblue" | "orange" | "red" {
  if (level === "critical") {
    return "red";
  }
  if (level === "warning") {
    return "orange";
  }
  return "arcoblue";
}

function statusLabel(status: string): string {
  if (status === "connected") {
    return "已连接";
  }
  if (status === "connecting") {
    return "连接中";
  }
  if (status === "reconnecting") {
    return "重连中";
  }
  return "离线";
}

export default function NotificationCenter() {
  const { Text } = Typography;
  const [telegramPrefs, setTelegramPrefs] = useState<TelegramPrefs | null>(null);
  const [telegramLoading, setTelegramLoading] = useState(false);
  const {
    items,
    unread,
    open,
    showAllTypes,
    muted,
    muteTypes,
    muteSymbols,
    hubStatus,
    openPanel,
    closePanel,
    markAllRead,
    clearAll,
    toggleShowAllTypes,
    toggleMute,
    toggleTypeMute,
    toggleSymbolMute,
  } = useNotificationCenter();

  const loadTelegramPrefs = async () => {
    setTelegramLoading(true);
    try {
      setTelegramPrefs(await requestTelegramPrefs("GET"));
    } catch {
      Message.error("读取 Telegram 设置失败");
    } finally {
      setTelegramLoading(false);
    }
  };

  const updateTelegramPrefs = async (patch: TelegramPrefsPatch) => {
    setTelegramLoading(true);
    try {
      setTelegramPrefs(await requestTelegramPrefs("PATCH", patch));
    } catch {
      Message.error("更新 Telegram 设置失败");
    } finally {
      setTelegramLoading(false);
    }
  };

  useEffect(() => {
    if (open) {
      void loadTelegramPrefs();
    }
  }, [open]);

  return (
    <>
      <Badge count={unread} maxCount={99}>
        <Button shape="circle" icon={<IconNotification />} onClick={openPanel} />
      </Badge>

      <Drawer
        width={420}
        title="通知中心"
        visible={open}
        onCancel={closePanel}
        footer={null}
      >
        <div className="cm-notifyToolbar">
          <Space>
            <Tag color={hubStatus === "connected" ? "green" : "orangered"}>Hub：{statusLabel(hubStatus)}</Tag>
            <span className="cm-muted">全部类型</span>
            <Switch checked={showAllTypes} onChange={toggleShowAllTypes} />
            <span className="cm-muted">静音</span>
            <Switch checked={muted} onChange={toggleMute} />
          </Space>
          <Space>
            <Button size="mini" onClick={markAllRead}>全部已读</Button>
            <Button size="mini" status="danger" onClick={clearAll}>清空</Button>
          </Space>
        </div>

        <div className="cm-notifyTelegramPrefs">
          <div className="cm-notifyTelegramHeader">
            <Text>Telegram 推送</Text>
            <Space size="mini">
              <Tag color={telegramPrefs?.enabled ? "green" : "gray"}>{telegramPrefs?.enabled ? "已启用" : "已关闭"}</Tag>
              <Tag color={telegramPrefs?.configured ? "arcoblue" : "orangered"}>{telegramPrefs?.configured ? telegramPrefs.market.toUpperCase() : "未配置"}</Tag>
            </Space>
          </div>
          <div className="cm-notifyTelegramSwitches">
            <label className="cm-notifySwitchRow">
              <span>大户挂单</span>
              <Switch
                size="small"
                checked={!!telegramPrefs?.whaleWallEnabled}
                disabled={telegramLoading || !telegramPrefs?.configured}
                onChange={(v) => void updateTelegramPrefs({ whaleWallEnabled: v })}
              />
            </label>
            <label className="cm-notifySwitchRow">
              <span>吸筹</span>
              <Switch
                size="small"
                checked={!!telegramPrefs?.absorptionEnabled}
                disabled={telegramLoading || !telegramPrefs?.configured}
                onChange={(v) => void updateTelegramPrefs({ absorptionEnabled: v })}
              />
            </label>
            <label className="cm-notifySwitchRow">
              <span>Abnormal Events</span>
              <Switch
                size="small"
                checked={!!telegramPrefs?.abnormalEventsEnabled}
                disabled={telegramLoading || !telegramPrefs?.configured}
                onChange={(v) => void updateTelegramPrefs({ abnormalEventsEnabled: v })}
              />
            </label>
            <label className="cm-notifySwitchRow">
              <span>Telegram 静音</span>
              <Switch
                size="small"
                checked={!!telegramPrefs?.muteAll}
                disabled={telegramLoading || !telegramPrefs?.configured}
                onChange={(v) => void updateTelegramPrefs({ muteAll: v })}
              />
            </label>
          </div>
        </div>

        <div className="cm-notifyList">
          {items.length === 0 && <Empty description="暂无通知" />}
          {items.map((item) => (
            <div key={`${item.id}_${item.ts}`} className={`cm-notifyItem ${item.unread ? "cm-notifyItem--unread" : ""}`}>
              <div className="cm-notifyTitle">
                <Tag color={levelColor(item.level)}>{item.type}</Tag>
                {item.count > 1 && <Tag color="purple">x{item.count}</Tag>}
                {item.symbol && (
                  <Tag
                    checkable
                    checked={!muteSymbols.includes(item.symbol)}
                    onCheck={() => toggleSymbolMute(item.symbol!)}
                  >
                    {item.symbol}
                  </Tag>
                )}
                <Tag
                  checkable
                  checked={!muteTypes.includes(item.type)}
                  onCheck={() => toggleTypeMute(item.type)}
                >
                  类型提醒
                </Tag>
              </div>
              <div className="cm-notifyContent">{item.content || item.title}</div>
              <div className="cm-notifyMeta">
                <Text className="cm-muted">{new Date(item.ts).toLocaleString()}</Text>
                {item.market && <Text className="cm-muted">{item.market.toUpperCase()}</Text>}
              </div>
            </div>
          ))}
        </div>
      </Drawer>
    </>
  );
}

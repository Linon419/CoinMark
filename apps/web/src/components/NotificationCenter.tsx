import { Badge, Button, Drawer, Empty, Space, Switch, Tag, Typography } from "@arco-design/web-react";
import { IconNotification } from "@arco-design/web-react/icon";
import { useNotificationCenter } from "../stores/notificationCenter";

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

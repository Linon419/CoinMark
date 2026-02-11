import { Tooltip } from "@arco-design/web-react";

type Props = {
  title?: string;
};

export default function QuantHelp({ title = "指标说明" }: Props) {
  return (
    <Tooltip
      position="bl"
      mini
      content={
        <div style={{ maxWidth: 380, lineHeight: 1.55 }}>
          <div style={{ fontWeight: 600, marginBottom: 6 }}>{title}</div>
          <div>
            <b>主图：</b>K线 + Spot5000 热力。颜色越深说明该价格区挂单越厚；颜色浅的“真空带”更容易被快速穿越。
          </div>
          <div style={{ marginTop: 4 }}>
            <b>Delta：</b>当前K线主动买入 - 主动卖出。正值偏多，负值偏空。
          </div>
          <div style={{ marginTop: 4 }}>
            <b>CVD（累积Delta）：</b>
            Visible=从当前可视区左边开始累积；Rolling 24H=过去24小时；Session=本周一00:00(UTC)开始。
          </div>
          <div style={{ marginTop: 4 }}>
            <b>Wall Ratio：</b>下方买墙/上方卖墙。&gt;1 支撑偏强；&lt;1 压力偏强。
          </div>
          <div style={{ marginTop: 4 }}>
            <b>Spot Dominance：</b>现货主导因子。&gt;0 更像现货推动的真突破；&lt;0 更像合约主导。
          </div>
          <div style={{ marginTop: 4 }}>
            <b>用法：</b>先看热力找墙和真空带，再看CVD背离确认资金方向，最后用Wall Ratio和Spot Dominance过滤假突破。
          </div>
        </div>
      }
    >
      <span
        style={{
          display: "inline-flex",
          width: 20,
          height: 20,
          borderRadius: "50%",
          alignItems: "center",
          justifyContent: "center",
          cursor: "help",
          border: "1px solid rgba(148,163,184,0.6)",
          color: "#64748b",
          fontWeight: 700,
          userSelect: "none",
        }}
      >
        ?
      </span>
    </Tooltip>
  );
}


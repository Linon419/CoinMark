import { useEffect, useRef } from "react";
import * as echarts from "echarts";

type Props = {
  option: echarts.EChartsOption;
  height?: number;
};

export default function EChart({ option, height = 320 }: Props) {
  const ref = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    if (!ref.current) return;
    const chart = echarts.init(ref.current);
    chart.setOption(option, { notMerge: true, lazyUpdate: true });
    const onResize = () => chart.resize();
    window.addEventListener("resize", onResize);
    return () => {
      window.removeEventListener("resize", onResize);
      chart.dispose();
    };
  }, [option]);

  return <div style={{ width: "100%", height }} ref={ref} />;
}

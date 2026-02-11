import { useEffect, useRef } from "react";
import * as echarts from "echarts";

type Props = {
  option: echarts.EChartsOption;
  height?: number;
  onEvents?: Record<string, (params: any) => void>;
  setOptionOpts?: {
    notMerge?: boolean;
    lazyUpdate?: boolean;
    replaceMerge?: Array<"series" | "xAxis" | "yAxis" | "legend" | "grid" | "visualMap" | "dataZoom">;
  };
};

export default function EChart({ option, height = 320, onEvents, setOptionOpts }: Props) {
  const ref = useRef<HTMLDivElement | null>(null);
  const chartRef = useRef<echarts.EChartsType | null>(null);
  const handlersRef = useRef<Array<{ eventName: string; handler: (params: any) => void }>>([]);

  useEffect(() => {
    if (!ref.current) return;
    const chart = echarts.init(ref.current);
    chartRef.current = chart;

    const onResize = () => chart.resize();
    window.addEventListener("resize", onResize);
    return () => {
      window.removeEventListener("resize", onResize);
      for (const item of handlersRef.current) {
        chart.off(item.eventName, item.handler);
      }
      handlersRef.current = [];
      chart.dispose();
      chartRef.current = null;
    };
  }, []);

  useEffect(() => {
    const chart = chartRef.current;
    if (!chart) return;
    chart.setOption(option, {
      notMerge: setOptionOpts?.notMerge ?? true,
      lazyUpdate: setOptionOpts?.lazyUpdate ?? true,
      replaceMerge: setOptionOpts?.replaceMerge,
    });
  }, [option, setOptionOpts]);

  useEffect(() => {
    const chart = chartRef.current;
    if (!chart) return;

    for (const item of handlersRef.current) {
      chart.off(item.eventName, item.handler);
    }
    handlersRef.current = [];

    if (!onEvents) return;
    for (const [eventName, handler] of Object.entries(onEvents)) {
      if (typeof handler !== "function") continue;
      chart.on(eventName, handler);
      handlersRef.current.push({ eventName, handler });
    }
  }, [onEvents]);

  return <div style={{ width: "100%", height }} ref={ref} />;
}

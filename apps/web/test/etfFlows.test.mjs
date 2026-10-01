import assert from "node:assert/strict";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";
import { pathToFileURL } from "node:url";
import ts from "typescript";

async function importTs(relativePath) {
  const source = await readFile(new URL(relativePath, import.meta.url), "utf8");
  const compiled = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.ES2022, target: ts.ScriptTarget.ES2022, strict: true },
  });
  const dir = await mkdtemp(path.join(tmpdir(), "coinmark-etf-flows-"));
  const file = path.join(dir, "module.mjs");
  await writeFile(file, compiled.outputText, "utf8");
  try {
    return await import(pathToFileURL(file).href);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
}

const { fmtUsdCn, flowTone, flowBars } = await importTs("../src/pages/etfFlows.ts");

test("金额按亿/万格式化并带正负号", () => {
  assert.equal(fmtUsdCn(-148687762.2), "-1.49亿");
  assert.equal(fmtUsdCn(107981652503), "+1079.82亿");
  assert.equal(fmtUsdCn(1450000), "+145万");
  assert.equal(fmtUsdCn(-55000), "-5.5万");
  assert.equal(fmtUsdCn(0), "0");
  assert.equal(fmtUsdCn(1450000, false), "145万");
});

test("流入流出语气", () => {
  assert.equal(flowTone(1), "up");
  assert.equal(flowTone(-1), "down");
  assert.equal(flowTone(0), "flat");
});

test("柱状图按本行最大绝对值缩放，0 也保留最小高度", () => {
  const bars = flowBars([{ date: "a", netInflow: 100 }, { date: "b", netInflow: -50 }, { date: "c", netInflow: 0 }], 20);
  assert.deepEqual(bars.map((b) => b.height), [20, 10, 1]);
  assert.deepEqual(bars.map((b) => b.tone), ["up", "down", "flat"]);
  assert.equal(flowBars([], 20).length, 0);
});

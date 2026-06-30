import assert from "node:assert/strict";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";
import { pathToFileURL } from "node:url";
import ts from "typescript";

async function importTs(relativePath) {
  const sourceUrl = new URL(relativePath, import.meta.url);
  const source = await readFile(sourceUrl, "utf8");
  const compiled = ts.transpileModule(source, {
    compilerOptions: {
      module: ts.ModuleKind.ES2022,
      target: ts.ScriptTarget.ES2022,
      strict: true,
    },
  });
  const dir = await mkdtemp(path.join(tmpdir(), "coinmark-boll-candidates-"));
  const file = path.join(dir, "module.mjs");
  await writeFile(file, compiled.outputText, "utf8");
  try {
    return await import(pathToFileURL(file).href);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
}

const candidates = await importTs("../src/pages/bollPumpCandidates.ts");

test("filters inactive BOLL states from the active view", () => {
  const rows = [
    { symbol: "AAAUSDT", status: "CONFIRM_1", priority_score: 88 },
    { symbol: "BBBUSDT", status: "EXPIRED", priority_score: 99 },
    { symbol: "CCCUSDT", status: "INVALIDATED", priority_score: 120 },
    { symbol: "DDDUSDT", status: "IDLE", priority_score: 0 },
  ];

  assert.deepEqual(
    candidates.filterActiveBollPumpStates(rows).map((row) => row.symbol),
    ["AAAUSDT"],
  );
});

test("builds current trade candidates from confirmed states only", () => {
  const states = [
    { symbol: "WATCHUSDT", timeframe: "1m", status: "WATCH", priority_score: 140, bounce_count: 0 },
    { symbol: "ONEUSDT", timeframe: "3m", dominant_timeframe: "15m", status: "CONFIRM_1", priority_score: 92, bounce_count: 1 },
    { symbol: "TWOUSDT", timeframe: "5m", status: "COMPLETED", priority_score: 108, bounce_count: 2 },
    { symbol: "BADUSDT", timeframe: "15m", status: "INVALIDATED", priority_score: 150, bounce_count: 2 },
  ];
  const signals = [
    { id: 7, symbol: "TWOUSDT", timeframe: "5m", reason: "4h resistance breakout", signal_time_ms: 2000 },
    { id: 3, symbol: "ONEUSDT", timeframe: "3m", reason: "first lower-band confirm", signal_time_ms: 1000 },
  ];

  const out = candidates.buildBollPumpTradeCandidates(states, signals);

  assert.deepEqual(
    out.map((row) => [row.symbol, row.status, row.trade_label, row.latest_signal_id, row.has_4h_breakout]),
    [
      ["TWOUSDT", "COMPLETED", "重点", 7, true],
      ["ONEUSDT", "CONFIRM_1", "可参与", 3, false],
    ],
  );
});

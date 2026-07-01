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
    { symbol: "EEEUSDT", status: "COMPLETED", priority_score: 120, expires_at_candle_ms: 1_000 },
  ];

  assert.deepEqual(
    candidates.filterActiveBollPumpStates(rows, 2_000).map((row) => row.symbol),
    ["AAAUSDT"],
  );
});

test("builds current trade candidates from breakout watch and confirmed states", () => {
  const nowMs = 10_000;
  const states = [
    { symbol: "WATCHUSDT", timeframe: "1m", status: "WATCH", priority_score: 140, bounce_count: 0, expires_at_candle_ms: 20_000 },
    { symbol: "ONEUSDT", timeframe: "3m", dominant_timeframe: "15m", status: "CONFIRM_1", priority_score: 92, bounce_count: 1, expires_at_candle_ms: 20_000 },
    { symbol: "TWOUSDT", timeframe: "5m", status: "COMPLETED", priority_score: 108, bounce_count: 2, expires_at_candle_ms: 20_000 },
    { symbol: "BADUSDT", timeframe: "15m", status: "INVALIDATED", priority_score: 150, bounce_count: 2, expires_at_candle_ms: 20_000 },
    { symbol: "OLDUSDT", timeframe: "1m", status: "COMPLETED", priority_score: 160, bounce_count: 2, expires_at_candle_ms: 9_000 },
    { symbol: "NOSIGUSDT", timeframe: "30m", status: "CONFIRM_1", priority_score: 150, bounce_count: 1, expires_at_candle_ms: 20_000 },
  ];
  const signals = [
    { id: 7, symbol: "TWOUSDT", timeframe: "5m", signal_level: "CONFIRM_2", reason: "4h resistance breakout", signal_time_ms: 2_000 },
    { id: 3, symbol: "ONEUSDT", timeframe: "3m", signal_level: "CONFIRM_1", reason: "first lower-band confirm", signal_time_ms: 1_000 },
    { id: 1, symbol: "WATCHUSDT", timeframe: "1m", signal_level: "WATCH", reason: "volume-backed pump", signal_time_ms: 3_000 },
    { id: 9, symbol: "OLDUSDT", timeframe: "1m", signal_level: "CONFIRM_2", reason: "second lower-band confirm", signal_time_ms: 4_000 },
  ];

  const out = candidates.buildBollPumpTradeCandidates(states, signals, 12, nowMs);

  assert.deepEqual(
    out.map((row) => [row.symbol, row.status, row.trade_label, row.latest_signal_id, row.has_4h_breakout]),
    [
      ["WATCHUSDT", "WATCH", "突破", 1, false],
      ["TWOUSDT", "COMPLETED", "重点", 7, true],
      ["ONEUSDT", "CONFIRM_1", "可参与", 3, false],
    ],
  );
});

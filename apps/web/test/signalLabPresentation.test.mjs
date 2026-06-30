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
  const dir = await mkdtemp(path.join(tmpdir(), "coinmark-signal-lab-"));
  const file = path.join(dir, "module.mjs");
  await writeFile(file, compiled.outputText, "utf8");
  try {
    return await import(pathToFileURL(file).href);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
}

const presentation = await importTs("../src/pages/signalLabPresentation.ts");

test("formats persistent buy ratio as percentage", () => {
  assert.equal(
    presentation.formatSignalLabBuyRatio({
      eventType: "signal_lab_persistent_buy",
      buyRatio: 0.734,
    }),
    "73.4%",
  );
});

test("formats missing buy ratio as a placeholder", () => {
  assert.equal(
    presentation.formatSignalLabBuyRatio({
      eventType: "signal_lab_single_large",
    }),
    "-",
  );
});

test("labels realtime signal event types", () => {
  assert.equal(presentation.signalLabEventTypeLabel("signal_lab_persistent_buy"), "持续买入");
  assert.equal(presentation.signalLabEventTypeLabel("signal_lab_single_large"), "单笔大额");
});

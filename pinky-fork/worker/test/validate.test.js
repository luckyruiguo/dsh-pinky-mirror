import assert from "node:assert/strict";
import { test } from "node:test";

import { TEST_INSTALL_ID, checkCrash, checkUsage } from "../src/validate.js";

const now = Date.parse("2026-11-08T10:00:00Z");

const usage = () => ({
  install_id: "8c1f0e6b2a7d4e5f9a0b1c2d3e4f5a6b",
  version: "0.2.0",
  os: "macos",
  os_version: "27.2",
  arch: "arm64",
  period_start: "2026-11-07T09:00:00.123456+08:00",
  period_end: "2026-11-08T09:12:00Z",
  counts: { "app.starts": 2, "phones.android.wifi": 1, "runs.agent.claude_code": 5, "results.error.lease_held": 1 },
});

const crash = () => ({
  id: "app-1794041672286-76939",
  process: "app",
  version: "0.2.0",
  os: "macos",
  os_version: "27.2",
  arch: "arm64",
  go_version: "go1.27.3",
  started: "2026-11-07T16:54:32.286803+08:00",
  crashed: "2026-11-07T16:54:33.603537125+08:00",
  reason: "panic: runtime error: index out of range [3] with length 3",
  stack: "goroutine 1 [running]:\nmain.main()\n\t~/pinky/cmd/pinky/main.go:44 +0x2b8",
});

test("a usage report is stored as sent", () => {
  const r = checkUsage(usage(), now);
  assert.equal(r.error, undefined);
  assert.equal(r.value.test, false);
  assert.equal(r.value.period_start, "2026-11-07T01:00:00.123Z");
  assert.deepEqual(r.value.counts, usage().counts);
});

test("a test usage report is not stored", () => {
  assert.equal(checkUsage({ ...usage(), install_id: TEST_INSTALL_ID }, now).value.test, true);
});

test("usage reports that are not Pinky's are refused", () => {
  const bad = [
    null,
    [],
    "x",
    { ...usage(), install_id: "8C1F0E6B2A7D4E5F9A0B1C2D3E4F5A6B" },
    { ...usage(), install_id: undefined },
    { ...usage(), version: "0.2.0; DROP TABLE" },
    { ...usage(), os: "Mac OS" },
    { ...usage(), arch: "" },
    { ...usage(), os_version: "27\u0000" },
    { ...usage(), period_start: "yesterday" },
    { ...usage(), period_end: "2026-11-01T00:00:00Z" },
    { ...usage(), period_end: "2026-11-20T00:00:00Z" },
    { ...usage(), period_start: "2020-01-01T00:00:00Z" },
    { ...usage(), counts: [] },
    { ...usage(), counts: { "Tools.Phone_Tap": 1 } },
    { ...usage(), counts: { "task.Pay Alice": 1 } },
    { ...usage(), counts: { "a..b": 1 } },
    { ...usage(), counts: { "x": -1 } },
    { ...usage(), counts: { "x": 1.5 } },
    { ...usage(), counts: { "x": "1" } },
    { ...usage(), counts: { "x": 2e9 } },
    { ...usage(), counts: Object.fromEntries(Array.from({ length: 501 }, (_, i) => [`k${i}`, 1])) },
  ];
  for (const b of bad) {
    assert.ok(checkUsage(b, now).error, JSON.stringify(b)?.slice(0, 120));
  }
});

test("a crash report is stored as sent", () => {
  const r = checkCrash(crash(), now);
  assert.equal(r.error, undefined);
  assert.equal(r.value.report_id, "app-1794041672286-76939");
  assert.equal(r.value.test, false);
  assert.equal(r.value.stack, crash().stack);
});

test("a test crash report is not stored", () => {
  assert.equal(checkCrash({ ...crash(), id: "test-smoke" }, now).value.test, true);
});

test("crash reports that are not Pinky's are refused", () => {
  const bad = [
    { ...crash(), id: "../x" },
    { ...crash(), id: "app-1-2; drop" },
    { ...crash(), process: "App" },
    { ...crash(), reason: "" },
    { ...crash(), reason: "x".repeat(1001) },
    { ...crash(), stack: "" },
    { ...crash(), stack: "a\u0001b" },
    { ...crash(), stack: "x".repeat(512 * 1024 + 1) },
    { ...crash(), crashed: "never" },
    { ...crash(), go_version: 7 },
  ];
  for (const b of bad) {
    assert.ok(checkCrash(b, now).error, JSON.stringify(b).slice(0, 120));
  }
});

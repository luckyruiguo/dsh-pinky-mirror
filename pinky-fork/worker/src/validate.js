// Checks the reports Pinky sends (internal/telemetry): their shape, the
// names they may hold and their sizes. Anything else is refused, so the
// database only ever holds what docs/TELEMETRY.md lists.

// A usage report with this install id, or a crash report whose id starts
// with "test-", is checked and answered but not stored: for smoke tests
// against the deployed Worker.
export const TEST_INSTALL_ID = "00000000000000000000000000000000";

export const MAX_USAGE_BYTES = 64 * 1024;
export const MAX_CRASH_BYTES = 1024 * 1024;
const MAX_COUNTS = 500;
const MAX_COUNT = 1e9;
const MAX_STACK = 512 * 1024;

const reInstall = /^[0-9a-f]{32}$/;
const reVersion = /^[0-9A-Za-z][0-9A-Za-z.+_-]{0,39}$/;
const reWord = /^[a-z0-9_]{1,20}$/;
const reCounter = /^[a-z0-9_]{1,40}(\.[a-z0-9_]{1,40}){0,7}$/;
const reCrashID = /^[a-z0-9_]{1,20}-\d{1,16}-\d{1,10}$/;
const reTestCrashID = /^test-[a-z0-9_-]{1,40}$/;

// Reports older than this, or later than now plus a day, are refused.
const EARLIEST = Date.parse("2026-01-01T00:00:00Z");
const DAY = 24 * 60 * 60 * 1000;

function fail(error) {
  return { error };
}

function isObject(v) {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

// text checks an optional string: printable, at most max characters.
function text(v, max) {
  if (v === undefined || v === null) return "";
  if (typeof v !== "string" || v.length > max || /[\u0000-\u0008\u000b-\u001f\u007f]/.test(v)) return null;
  return v;
}

// time checks an RFC 3339 time and returns it as an ISO string.
function time(v, now) {
  if (typeof v !== "string" || v.length > 40) return null;
  const t = Date.parse(v);
  if (Number.isNaN(t) || t < EARLIEST || t > now + DAY) return null;
  return new Date(t).toISOString();
}

// system checks the fields both reports share.
function system(body) {
  const version = body.version;
  const os = body.os;
  const arch = body.arch;
  const osVersion = text(body.os_version, 40);
  if (typeof version !== "string" || !reVersion.test(version)) return fail("bad version");
  if (typeof os !== "string" || !reWord.test(os)) return fail("bad os");
  if (typeof arch !== "string" || !reWord.test(arch)) return fail("bad arch");
  if (osVersion === null) return fail("bad os_version");
  return { value: { version, os, os_version: osVersion, arch } };
}

// checkUsage checks a usage report and returns what to store.
export function checkUsage(body, now = Date.now()) {
  if (!isObject(body)) return fail("not an object");
  if (typeof body.install_id !== "string" || !reInstall.test(body.install_id)) return fail("bad install_id");
  const sys = system(body);
  if (sys.error) return sys;
  const start = time(body.period_start, now);
  const end = time(body.period_end, now);
  if (!start || !end || Date.parse(end) < Date.parse(start)) return fail("bad period");
  if (!isObject(body.counts)) return fail("bad counts");
  const keys = Object.keys(body.counts);
  if (keys.length > MAX_COUNTS) return fail("too many counts");
  const counts = {};
  for (const k of keys) {
    const n = body.counts[k];
    if (!reCounter.test(k)) return fail("bad counter name");
    if (!Number.isInteger(n) || n < 0 || n > MAX_COUNT) return fail("bad count");
    counts[k] = n;
  }
  return {
    value: {
      test: body.install_id === TEST_INSTALL_ID,
      install_id: body.install_id,
      ...sys.value,
      period_start: start,
      period_end: end,
      counts,
    },
  };
}

// checkCrash checks a crash report and returns what to store.
export function checkCrash(body, now = Date.now()) {
  if (!isObject(body)) return fail("not an object");
  const id = body.id;
  const test = typeof id === "string" && reTestCrashID.test(id);
  if (typeof id !== "string" || (!test && !reCrashID.test(id))) return fail("bad id");
  if (typeof body.process !== "string" || !reWord.test(body.process)) return fail("bad process");
  const sys = system(body);
  if (sys.error) return sys;
  const goVersion = text(body.go_version, 40);
  if (goVersion === null) return fail("bad go_version");
  const started = time(body.started, now);
  const crashed = time(body.crashed, now);
  if (!started || !crashed) return fail("bad times");
  const reason = text(body.reason, 1000);
  if (!reason) return fail("bad reason");
  // The stack has newlines and tabs; nothing else below a space.
  const stack = body.stack;
  if (typeof stack !== "string" || stack.length === 0 || stack.length > MAX_STACK || /[\u0000-\u0008\u000b-\u001f\u007f]/.test(stack.replace(/\r/g, ""))) {
    return fail("bad stack");
  }
  return {
    value: {
      test,
      report_id: id,
      process: body.process,
      ...sys.value,
      go_version: goVersion,
      started,
      crashed,
      reason,
      stack,
    },
  };
}

// Pinky's telemetry backend: it takes the usage reports and crash reports
// that internal/telemetry posts, checks them (validate.js) and stores them
// in D1. It keeps no IP address or request header, and a daily cron
// deletes usage reports after 400 days and crash reports after 180.
// docs/TELEMETRY.md describes what the reports hold.

import { MAX_CRASH_BYTES, MAX_USAGE_BYTES, checkCrash, checkUsage } from "./validate.js";

const KEEP_USAGE_DAYS = 400;
const KEEP_CRASH_DAYS = 180;

const routes = {
  "/v1/usage": { max: MAX_USAGE_BYTES, check: checkUsage, store: storeUsage },
  "/v1/crash": { max: MAX_CRASH_BYTES, check: checkCrash, store: storeCrash },
};

function reply(status, body) {
  return new Response(body === undefined ? null : body + "\n", {
    status,
    headers: body === undefined ? {} : { "content-type": "text/plain; charset=utf-8" },
  });
}

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    if (url.pathname === "/" && (request.method === "GET" || request.method === "HEAD")) {
      return reply(200, "Pinky telemetry. See docs/TELEMETRY.md in github.com/DIYgod/pinky.");
    }
    const route = routes[url.pathname];
    if (!route) return reply(404, "not found");
    if (request.method !== "POST") return reply(405, "POST only");

    // A rough limit per address, kept by Cloudflare for a minute and never
    // stored here.
    if (env.LIMITER) {
      const { success } = await env.LIMITER.limit({ key: request.headers.get("cf-connecting-ip") || "-" });
      if (!success) return reply(429, "too many reports; try later");
    }
    const declared = Number(request.headers.get("content-length") || 0);
    if (declared > route.max) return reply(413, "too large");
    const raw = await request.text();
    if (raw.length > route.max) return reply(413, "too large");
    let body;
    try {
      body = JSON.parse(raw);
    } catch {
      return reply(400, "not JSON");
    }
    const result = route.check(body);
    if (result.error) return reply(400, result.error);
    if (!result.value.test) {
      await route.store(env.DB, result.value, new Date().toISOString());
    }
    return reply(204);
  },

  async scheduled(_event, env) {
    const ago = (days) => new Date(Date.now() - days * 24 * 60 * 60 * 1000).toISOString();
    await env.DB.batch([
      env.DB.prepare("DELETE FROM usage_reports WHERE received_at < ?1").bind(ago(KEEP_USAGE_DAYS)),
      env.DB.prepare("DELETE FROM crash_reports WHERE received_at < ?1").bind(ago(KEEP_CRASH_DAYS)),
    ]);
  },
};

// storeUsage keeps one row per install and period: a report sent again
// after a failure (with what was counted meanwhile) replaces the first.
async function storeUsage(db, u, now) {
  await db
    .prepare(
      `INSERT INTO usage_reports (install_id, period_start, period_end, received_at, version, os, os_version, arch, counts)
       VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9)
       ON CONFLICT (install_id, period_start) DO UPDATE SET
         period_end = excluded.period_end, received_at = excluded.received_at, version = excluded.version,
         os = excluded.os, os_version = excluded.os_version, arch = excluded.arch, counts = excluded.counts`,
    )
    .bind(u.install_id, u.period_start, u.period_end, now, u.version, u.os, u.os_version, u.arch, JSON.stringify(u.counts))
    .run();
}

// storeCrash keeps a crash once, however often it is sent.
async function storeCrash(db, c, now) {
  await db
    .prepare(
      `INSERT OR IGNORE INTO crash_reports
         (report_id, received_at, process, version, os, os_version, arch, go_version, started, crashed, reason, stack)
       VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12)`,
    )
    .bind(c.report_id, now, c.process, c.version, c.os, c.os_version, c.arch, c.go_version, c.started, c.crashed, c.reason, c.stack)
    .run();
}

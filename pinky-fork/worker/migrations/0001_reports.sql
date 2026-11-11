-- Usage reports: one row per install and period (a report sent again after
-- a failure replaces the first). counts is the report's JSON object of
-- counters; query it with json_each.
CREATE TABLE usage_reports (
  id INTEGER PRIMARY KEY,
  install_id TEXT NOT NULL,
  period_start TEXT NOT NULL,
  period_end TEXT NOT NULL,
  received_at TEXT NOT NULL,
  version TEXT NOT NULL,
  os TEXT NOT NULL,
  os_version TEXT NOT NULL,
  arch TEXT NOT NULL,
  counts TEXT NOT NULL,
  UNIQUE (install_id, period_start)
);
CREATE INDEX usage_reports_received_at ON usage_reports (received_at);

-- Crash reports, once each. They carry no install id.
CREATE TABLE crash_reports (
  id INTEGER PRIMARY KEY,
  report_id TEXT NOT NULL UNIQUE,
  received_at TEXT NOT NULL,
  process TEXT NOT NULL,
  version TEXT NOT NULL,
  os TEXT NOT NULL,
  os_version TEXT NOT NULL,
  arch TEXT NOT NULL,
  go_version TEXT NOT NULL,
  started TEXT NOT NULL,
  crashed TEXT NOT NULL,
  reason TEXT NOT NULL,
  stack TEXT NOT NULL
);
CREATE INDEX crash_reports_received_at ON crash_reports (received_at);

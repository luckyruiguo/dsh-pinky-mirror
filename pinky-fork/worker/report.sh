#!/bin/sh
# Summarizes what Pinky's telemetry Worker has stored, from its D1
# database, with the global wrangler (through the 1Password plugin when
# `op` is there; WRANGLER overrides the command).
#
#   worker/report.sh [--local] [days]     installs, versions, systems, top counters, crashes
#   worker/report.sh [--local] crashes    the latest crash reports, one line each
#   worker/report.sh [--local] crash <id> one crash report with its stack
#   worker/report.sh [--local] sql "<SQL>"
set -eu
cd "$(dirname "$0")"
if [ -z "${WRANGLER:-}" ]; then
	if command -v op >/dev/null 2>&1; then
		WRANGLER="op plugin run -- wrangler"
	else
		WRANGLER=wrangler
	fi
fi
where=--remote
if [ "${1:-}" = "--local" ]; then
	where=--local
	shift
fi

# show prints wrangler's JSON results as a table, or one field a line with
# "record" (for a crash's stack).
show() {
	node -e '
let s = "";
process.stdin.on("data", (d) => (s += d)).on("end", () => {
  let rows;
  try { rows = JSON.parse(s)[0].results; } catch { process.stdout.write(s); return; }
  if (!rows.length) return console.log("(none)");
  const cols = Object.keys(rows[0]);
  if (process.argv[1] === "record") {
    for (const r of rows) for (const c of cols) console.log(c + ": " + r[c]);
    return;
  }
  const w = cols.map((c) => Math.max(c.length, ...rows.map((r) => String(r[c] ?? "").length)));
  const line = (vals) => vals.map((v, i) => String(v ?? "").padEnd(w[i])).join("  ").trimEnd();
  console.log(line(cols));
  for (const r of rows) console.log(line(cols.map((c) => r[c])));
});' "${1:-table}"
}

q() { # q <title> <sql> [record]
	printf '\n== %s\n' "$1"
	$WRANGLER d1 execute pinky-telemetry "$where" --json --command "$2" 2>/dev/null | show "${3:-table}"
}

case "${1:-}" in
crashes)
	q "Latest crash reports" "SELECT report_id, crashed, version, process, os, os_version, reason FROM crash_reports ORDER BY crashed DESC LIMIT 30"
	;;
crash)
	id=$(printf '%s' "${2:?crash <id>}" | tr -cd 'a-z0-9_-')
	q "Crash $id" "SELECT report_id, version, process, os, os_version, arch, go_version, started, crashed, reason, stack FROM crash_reports WHERE report_id = '$id'" record
	;;
sql)
	q "Query" "${2:?sql \"<SQL>\"}"
	;;
*)
	days=$(printf '%s' "${1:-7}" | tr -cd '0-9')
	since="datetime('now', '-${days:-7} days')"
	q "Installs and reports in the last ${days:-7} days" "SELECT COUNT(DISTINCT install_id) AS installs, COUNT(*) AS reports FROM usage_reports WHERE received_at >= $since"
	q "Versions" "SELECT version, COUNT(DISTINCT install_id) AS installs FROM usage_reports WHERE received_at >= $since GROUP BY version ORDER BY installs DESC"
	q "Systems" "SELECT os, os_version, arch, COUNT(DISTINCT install_id) AS installs FROM usage_reports WHERE received_at >= $since GROUP BY os, os_version, arch ORDER BY installs DESC LIMIT 20"
	q "Counters" "SELECT j.key AS counter, SUM(j.value) AS total, COUNT(DISTINCT u.install_id) AS installs FROM usage_reports u, json_each(u.counts) j WHERE u.received_at >= $since GROUP BY j.key ORDER BY total DESC LIMIT 80"
	q "Crashes" "SELECT version, process, reason, COUNT(*) AS crashes, MAX(crashed) AS last FROM crash_reports WHERE received_at >= $since GROUP BY version, process, reason ORDER BY crashes DESC LIMIT 20"
	;;
esac

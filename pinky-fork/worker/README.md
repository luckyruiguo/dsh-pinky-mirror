# Pinky's telemetry Worker

A Cloudflare Worker that takes the usage reports and crash reports Pinky sends ([docs/TELEMETRY.md](../docs/TELEMETRY.md), [internal/telemetry](../internal/telemetry)) and keeps them in a D1 database. Feedback does not come here: it goes to GitHub issues.

- `POST /v1/usage` and `POST /v1/crash`: JSON, checked by [src/validate.js](src/validate.js) (shape, counter names, sizes: 64 KB for usage, 1 MB for a crash); 204 when stored, 400 when refused, 413 when too large, 429 after 30 reports a minute from one address.
- A usage report with the install id `00000000000000000000000000000000`, or a crash report whose id starts with `test-`, is checked but not stored.
- Usage reports are one row per install and period: a report sent again after a failure replaces the first. Crash reports are stored once each.
- A cron at 03:23 UTC deletes usage reports older than 400 days and crash reports older than 180.
- No IP address, request header or Workers Logs are kept.

Deployed in the RSS3 account as `pinky-telemetry` at https://pinky-telemetry.rss3.workers.dev, with the D1 database `pinky-telemetry` (APAC). The app's address is `telemetry.Endpoint` in [internal/telemetry/telemetry.go](../internal/telemetry/telemetry.go).

It has no dependencies. Use the global `wrangler` (not `npx`), from this folder:

```sh
node --test                                        # the validator's tests
wrangler d1 migrations apply pinky-telemetry --local
wrangler dev                                       # http://127.0.0.1:8787, with a local database
./smoke.sh                                         # what it takes and refuses (test reports only)
PINKY_TELEMETRY_TEST_URL=http://127.0.0.1:8787 go test -run TestAgainstTheWorker ../internal/telemetry   # real reports from the Go client
```

Deploying:

```sh
wrangler d1 migrations apply pinky-telemetry --remote   # new files in migrations/ first
wrangler deploy
./smoke.sh https://pinky-telemetry.rss3.workers.dev
```

Reading what came in (`--local` reads the local database; `WRANGLER=…` picks the command, which is `op plugin run -- wrangler` when `op` is installed):

```sh
./report.sh            # the last 7 days: installs, versions, systems, counters, crashes
./report.sh 30         # the last 30 days
./report.sh crashes    # the latest crash reports
./report.sh crash <id> # one crash with its stack
./report.sh sql "SELECT …"
```

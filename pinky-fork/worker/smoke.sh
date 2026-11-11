#!/bin/sh
# Checks a running telemetry Worker from outside: what it takes and what
# it refuses. It sends only test reports, which the Worker checks but does
# not store, so it is safe against the deployed Worker:
#
#   worker/smoke.sh                       # wrangler dev on 127.0.0.1:8787
#   worker/smoke.sh https://pinky-telemetry.example.workers.dev
set -eu
url=${1:-http://127.0.0.1:8787}
url=${url%/}
fail=0

check() { # check <want status> <what> <curl args...>
	want=$1 what=$2
	shift 2
	got=$(curl -sS -o /dev/null -w '%{http_code}' "$@" || echo 000)
	if [ "$got" = "$want" ]; then
		echo "ok   $got $what"
	else
		echo "FAIL $got (want $want) $what"
		fail=1
	fi
}

now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
usage='{"install_id":"00000000000000000000000000000000","version":"0.0.0-dev","os":"macos","os_version":"27.2","arch":"arm64","period_start":"'$now'","period_end":"'$now'","counts":{"app.starts":1,"tools.phone_tap":3}}'
crash='{"id":"test-smoke","process":"app","version":"0.0.0-dev","os":"macos","os_version":"27.2","arch":"arm64","go_version":"go1.27.3","started":"'$now'","crashed":"'$now'","reason":"panic: smoke test","stack":"goroutine 1 [running]:\nmain.main()"}'
json='-H content-type:application/json'

check 200 "home page" "$url/"
check 204 "a test usage report" -X POST $json --data "$usage" "$url/v1/usage"
check 204 "a test crash report" -X POST $json --data "$crash" "$url/v1/crash"
check 400 "not JSON" -X POST $json --data "{nope" "$url/v1/usage"
check 400 "a counter name that is not Pinky's" -X POST $json --data "$(echo "$usage" | sed 's/"app.starts"/"Pay Alice"/')" "$url/v1/usage"
check 400 "a crash without a stack" -X POST $json --data "$(echo "$crash" | sed 's/"stack":"[^"]*"/"stack":""/')" "$url/v1/crash"
check 405 "GET on a report path" "$url/v1/usage"
check 404 "an unknown path" -X POST $json --data "$usage" "$url/v1/feedback"
big=$(head -c 70000 /dev/zero | tr '\0' 'x')
check 413 "a usage report over 64 KB" -X POST $json --data "{\"pad\":\"$big\"}" "$url/v1/usage"
exit $fail

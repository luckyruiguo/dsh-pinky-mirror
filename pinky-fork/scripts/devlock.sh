#!/bin/sh
# Run a command while holding one phone's lock, so parallel sessions never
# drive the same phone at the same time.
#
# Usage: scripts/devlock.sh <device-id> <command...>
set -eu
if [ "$#" -lt 2 ]; then
	echo "usage: $0 <device-id> <command...>" >&2
	exit 2
fi
id="$1"
shift
exec lockf -k -t 1800 "/tmp/pinky-device-$id.lock" "$@"

#!/bin/sh
# Reads the kernel ring buffer (dmesg). READ-ONLY: never writes anything.
# $1 = number of lines to keep (100 | 300 | 1000)
# $2 = level filter (all | warn)   warn = emerg, alert, crit, err, warn
# Exit codes: 2 invalid argument, 3 dmesg could not be read.
# ASCII only: guest-exec arguments (and this text, when run inline) must not contain
# non-ASCII characters.
set -eu
case "${1:-}" in
  100|300|1000) n="$1" ;;
  *) echo "invalid line count" >&2; exit 2 ;;
esac
case "${2:-}" in
  all) lvl="" ;;
  warn) lvl="--level=emerg,alert,crit,err,warn" ;;
  *) echo "invalid level" >&2; exit 2 ;;
esac
# Readable timestamps when util-linux supports them; plain uptime stamps otherwise.
if dmesg --time-format=iso >/dev/null 2>&1; then
  tf="--time-format=iso"
elif dmesg -T >/dev/null 2>&1; then
  tf="-T"
else
  tf=""
fi
# A level filter the installed dmesg does not know is dropped, not turned into an error;
# the marker on stderr lets the caller tell the operator the log is NOT filtered.
if [ -n "$lvl" ] && ! dmesg "$lvl" >/dev/null 2>&1; then
  lvl=""
  echo "level filter not supported" >&2
fi
# $tf and $lvl are fixed words chosen above, never request data: no quoting needed.
out=$(dmesg $tf $lvl 2>&1) && rc=0 || rc=$?
if [ "$rc" -ne 0 ]; then
  # Only the first line, cut short: the buffer content must not travel in the error channel.
  first=$(printf '%s\n' "$out" | head -n 1 | cut -c1-200)
  echo "dmesg not readable (exit $rc): $first" >&2
  exit 3
fi
printf '%s\n' "$out" | tail -n "$n"

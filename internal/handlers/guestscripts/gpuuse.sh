#!/bin/sh
# Reads GPU metrics with nvidia-smi. READ-ONLY: never writes files, never changes GPU state.
# Takes no arguments. Exit codes: 2 argument given, 3 nvidia-smi not installed,
# 4 nvidia-smi failed (driver not loaded, library/driver mismatch, timeout).
# ASCII only: when run inline this text travels as a guest-exec argument.
set -eu
if [ "$#" -ne 0 ]; then
  echo "no arguments allowed" >&2
  exit 2
fi
if ! command -v nvidia-smi >/dev/null 2>&1; then
  echo "nvidia-smi not found" >&2
  exit 3
fi
# A hung driver must not keep the agent busy: 15 s limit when coreutils timeout exists.
to=""
if command -v timeout >/dev/null 2>&1; then
  to="timeout 15"
fi
# $to is a fixed word chosen above, never request data: no quoting needed.
out=$($to nvidia-smi --query-gpu=pci.bus_id,name,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw --format=csv,noheader,nounits 2>&1) && rc=0 || rc=$?
if [ "$rc" -ne 0 ]; then
  # nvidia-smi often prints its error on stdout: only the first line, cut short.
  first=$(printf '%s\n' "$out" | head -n 1 | cut -c1-200)
  echo "nvidia-smi failed (exit $rc): $first" >&2
  exit 4
fi
# Unchanged: the server parses strictly and drops any line that is not a metrics row.
printf '%s\n' "$out"

#!/bin/bash
# Aggiorna i template di tutte le release Ubuntu abilitate (os.d/ubuntu-*.conf).
exec "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/bin/pmgr-os.sh" --family ubuntu "$@"

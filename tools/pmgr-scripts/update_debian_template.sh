#!/bin/bash
# Aggiorna i template di tutte le release Debian abilitate (os.d/debian-*.conf).
exec "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/bin/pmgr-os.sh" --family debian "$@"

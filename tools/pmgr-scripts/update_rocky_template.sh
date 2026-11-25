#!/bin/bash
# Aggiorna i template di tutte le release Rocky Linux abilitate (os.d/rocky-*.conf).
exec "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/bin/pmgr-os.sh" --family rocky "$@"

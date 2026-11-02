# mockpve — Proxmox VE finto per le anteprime grafiche

Server HTTP di **sola lettura** con dati sintetici (4 nodi, ~50 VM, un nodo offline, task di esempio). Serve a vedere l'interfaccia senza un cluster reale e senza token veri. Solo libreria standard di Python 3, nessuna dipendenza.

- Ascolta **solo su 127.0.0.1** (porta 18006 di default, o il primo argomento).
- Risponde 405 a ogni scrittura (POST/PUT/DELETE): nessuna azione puo' modificare nulla.
- I dati non rappresentano nessun cluster reale.

## Uso

```bash
python3 devtools/mockpve/mockpve.py 18006 &

# directory di prova fuori dal repo, con config privata
mkdir -p /tmp/pmgr-preview && cd /tmp/pmgr-preview && umask 077
cat > config.json <<'JSON'
{
  "clusters": [{"label": "Anteprima (dati finti)", "url": "http://127.0.0.1:18006",
                "api_token": "mock@pve!preview=00000000-0000-0000-0000-000000000000", "default": true}],
  "theme": "", "density": "", "cache_seconds": 30,
  "db_path": "preview.db", "listen_addr": "127.0.0.1:8090"
}
JSON
CGO_ENABLED=1 go build -o /tmp/pmgr-preview/proxmox-manager /path/al/repo/cmd/server/
./proxmox-manager        # poi apri http://127.0.0.1:8090/
```

Il token e' finto: non usarlo contro un Proxmox vero e non copiare qui il `config.json` di produzione.

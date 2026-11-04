# mockpve — Proxmox VE finto per le anteprime grafiche

Server HTTP di **sola lettura** con dati sintetici (4 nodi, ~50 VM, un nodo offline, task di esempio). Serve a vedere l'interfaccia senza un cluster reale e senza token veri. Solo libreria standard di Python 3, nessuna dipendenza.

- Ascolta **solo su 127.0.0.1** (porta 18006 di default, o il primo argomento).
- Risponde 405 a ogni scrittura (POST/PUT/DELETE) tranne `agent/exec` (finto: restituisce un dmesg e un sistema operativo inventati e non esegue nulla), la modifica della configurazione QEMU/LXC (`PUT .../{qemu|lxc}/{id}/config`, anche del campo `tags`, con controllo del `digest` come Proxmox) e lo spegnimento (`status/shutdown|stop`): servono a provare l'assegnazione delle GPU, vivono solo in memoria e si perdono al riavvio del mock.
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

## Ceph finto

`CEPH_SCENARIO=healthy|warn|error|recovery|down|none python3 devtools/mockpve/mockpve.py` (default `recovery`): 36 OSD su 6 nodi (4 ssd + 2 hdd per nodo), 4 pool (`archivio` con `replicated_rule` su tutte le classi), mappa CRUSH con 3 regole, 3 MON. `error` = capacita' 97% + `OSD_FULL`; `recovery` = OSD giu', fronte di recupero e flag `noout`; `none` = come un cluster senza Ceph (500 "binary not installed").

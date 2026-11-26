# mockpve — Proxmox VE finto per le anteprime grafiche

Server HTTP di **sola lettura** con dati sintetici (4 nodi online, ~50 VM, task di esempio). Serve a vedere l'interfaccia senza un cluster reale e senza token veri. Solo libreria standard di Python 3, nessuna dipendenza.

- Ascolta **solo su 127.0.0.1** (porta 18006 di default, o il primo argomento).
- Risponde 405 a ogni scrittura (POST/PUT/DELETE) tranne `agent/exec` (finto: restituisce un dmesg e un sistema operativo inventati e non esegue nulla), la modifica della configurazione QEMU/LXC (`PUT .../{qemu|lxc}/{id}/config`, anche del campo `tags`, con controllo del `digest` come Proxmox) lo spegnimento (`status/shutdown|stop`) e la migrazione (`POST .../migrate`, sposta la VM solo in memoria; il task risulta "running" per due letture e poi OK): servono a provare l'assegnazione delle GPU, vivono solo in memoria e si perdono al riavvio del mock.
- Il mock simula anche l’uso GPU; `"gpu_usage": true` nel `config.json` di anteprima abilita il collector (primo campione dopo 90 s).
- `GET /storage` elenca gli storage del cluster (`datacloud-ssd`, `datacloud-hdd`, `local`...): serve alla tabella delle classi degli storage in Impostazioni → Report e listini. Le impostazioni dei report si salvano nel `config.json` di anteprima, non nel mock.
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
  "theme": "", "density": "",
  "db_path": "preview.db", "listen_addr": "127.0.0.1:8090"
}
JSON
CGO_ENABLED=1 go build -o /tmp/pmgr-preview/proxmox-manager /path/al/repo/cmd/server/
# nella directory di anteprima: stampa password e chiave TOTP una sola volta
./proxmox-manager user add dev --role admin
./proxmox-manager        # poi apri http://127.0.0.1:8090/
```

L’anteprima richiede sempre un account: inserisci la chiave TOTP nell’app di autenticazione e accedi con utente `dev`, password e codice a 6 cifre. In alternativa, avvia senza creare utenti e usa la configurazione web con il codice monouso stampato nel log (nel servizio: `journalctl -u proxmox-manager`). La directory di anteprima conserva account e chiave del server separati dalla produzione.

Il token e' finto: non usarlo contro un Proxmox vero e non copiare qui il `config.json` di produzione.

## Ceph finto

`CEPH_SCENARIO=healthy|warn|error|recovery|down|none python3 devtools/mockpve/mockpve.py` (default `recovery`): 36 OSD su 6 nodi (4 ssd + 2 hdd per nodo), 4 pool (`archivio` con `replicated_rule` su tutte le classi), mappa CRUSH con 3 regole, 3 MON. `error` = capacita' 97% + `OSD_FULL`; `recovery` = OSD giu', fronte di recupero e flag `noout`; `none` = come un cluster senza Ceph (500 "binary not installed").
- Spegnimento, stop e accensione delle VM QEMU (`POST .../status/shutdown|stop|start`) cambiano lo stato solo in memoria e restituiscono un UPID valido. `NOSHUTDOWN_VMIDS=155,160` (variabile d'ambiente) fa ignorare lo spegnimento ordinato a quelle VM: serve a provare la domanda sullo stop forzato del Bilanciamento.
- Processori: `datacloud-node-1` e `-3` AMD EPYC 7313, `-2` Intel Xeon E5-2640 v4; le VM QEMU con VMID pari hanno `cpu: host` (a caldo vanno solo verso un nodo che ha tutti i flag CPU del nodo di partenza: `cpuinfo.flags`, accorciati nel mock).

## Preparazione nodi

`MOCK_ARCH=x86_64|aarch64` sceglie l'architettura dei nodi (predefinita `x86_64`); `MOCK_ARCH=aarch64 python3 devtools/mockpve/mockpve.py 18006` simula un cluster ARM. Aggiungi `MOCK_ARCH_MIXED=1` per assegnare all'ultimo nodo l'altra architettura e provare gli avvisi di cluster misto.

La pagina `#/nodeprep` legge anche i permessi effettivi (`GET /access/permissions?path=...`) e gli orologi (`GET /nodes/{n}/time`). Per impostazione predefinita tutti i controlli di preparazione sono a posto: nodi online, storage e snippet disponibili, bridge `vmbr0`, versioni uguali, permessi completi e orologi allineati.

Avvia `READINESS_SCENARIO=problems python3 devtools/mockpve/mockpve.py 18006` per simulare: contenuto `snippets` assente da `local` sul nodo 1, file `vendor.yaml` mancante sul nodo 2, bridge `vmbr0` assente e orologio avanti di 45 secondi sul nodo 3, privilegio `Datastore.AllocateSpace` mancante. Riavvia il mock e premi **Ricontrolla** per cambiare scenario senza attendere la cache. Il mock non esegue i comandi mostrati dalla pagina.

La sezione **Accesso per la sincronizzazione** mostra **impronta da confermare** in anteprima. Gli indirizzi dei nodi in `/cluster/status` sono `192.0.2.11` e successivi (TEST-NET-1, indirizzi riservati alla documentazione), così l'anteprima non tenta connessioni SSH a macchine reali. **Leggi impronta** termina con **non raggiungibile**: il mock non implementa un server SSH. Non confermare impronte né usare indirizzi di produzione nell'anteprima.

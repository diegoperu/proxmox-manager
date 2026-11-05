# pmgr-sync - preparazione dei nodi per la sincronizzazione di script e snippet

Parte della ROADMAP 13. Questa cartella contiene solo strumenti **senza segreti**; gli script dei
template (`/root/cloud-images`, `SUPPORT-FILES/`) non stanno nel repository.

| File | A cosa serve |
|---|---|
| `pmgr-sync-setup.sh` | Si lancia come root su ogni nodo, una volta: crea l'utente `pmgr-sync` (non root, senza password), le cartelle con permessi di gruppo e il comando ristretto per la chiave del nodo sorgente. Idempotente, `--check` non scrive. |
| `test_pmgr_sync.sh` | Test senza root: validazione degli argomenti e comportamento del comando ristretto (`pmgr-sync-gate`). |
| `test_pmgr_sync_container.sh` | Test di integrazione in un container Debian 13 usa e getta (podman): utente, permessi, idempotenza, sshd e rsync veri. |

## Come e' fatto

```
nodo sorgente (pmgr-sync, chiave ed25519)
   --rsync su ssh-->  nodo destinazione, utente pmgr-sync
                      authorized_keys:  restrict,from="<ip sorgente>",command="pmgr-sync-gate"
```

`pmgr-sync-gate` accetta **solo**:
- `pmgr-list`: elenco `hash  dimensione  data  percorso` dei file nelle due cartelle (nessun link simbolico, nomi fuori whitelist segnalati come `INVALID`);
- `rsync --server ... . snippets/` e `... . pmgr-scripts/`: scrittura (`rrsync -wo -no-del`), mai lettura, mai cancellazione, nessun altro percorso.

Tutto il resto esce con 126. La chiave del nodo sorgente non apre una shell.

Cartelle sui nodi: `/var/lib/vz/snippets` (dove Proxmox cerca gli snippet) e `/var/lib/vz/pmgr-scripts`,
entrambe `root:pmgr-sync` `2775`. I file creati da `pmgr-sync` sono suoi e leggibili da tutti (`644`).

**Promemoria per lo strumento di sincronizzazione** (verificato dal test di integrazione): usare
`rsync -rltO --no-owner --no-group --chmod=F644,D775` (con `-O`, altrimenti rsync prova a impostare la
data della cartella di destinazione, che e' di root, e fallisce) e **mai** `--delete`.

## Prova su Magi (cluster di test)

Nodi: `magi-node-1` 10.75.132.201 (sorgente di prova), `magi-node-2` .202 e `magi-node-3` .203 (destinazioni).
La chiave `~/.ssh/pmgr_sync_magi_dev` (senza passphrase) e' per l'accesso con shell dell'assistente a `pmgr-sync`
e **solo su Magi**, vincolata all'indirizzo della postazione. Non va usata su CALCOLO o HOSTING.

```bash
cd ~/ai-workspace/proxmox-manager/tools/pmgr-sync
DEV=~/.ssh/pmgr_sync_magi_dev.pub

# 0. indirizzo con cui la postazione si presenta a Magi (usalo in --dev-from al posto di MIOIP)
ssh root@10.75.132.201 'echo ${SSH_CLIENT%% *}'

# 1. nodo sorgente di prova: utente, cartelle, chiave ssh
scp pmgr-sync-setup.sh "$DEV" root@10.75.132.201:/root/
ssh root@10.75.132.201 'bash /root/pmgr-sync-setup.sh --role source --check; echo "(check: uscita $?)"'
ssh root@10.75.132.201 'bash /root/pmgr-sync-setup.sh --role source --dev-pubkey /root/pmgr_sync_magi_dev.pub --dev-from MIOIP'
ssh root@10.75.132.201 'cat /var/lib/pmgr-sync/.ssh/id_ed25519.pub' > /tmp/magi1.pub

# 2. destinazioni (nodo 2 e 3)
for h in 10.75.132.202 10.75.132.203; do
  scp pmgr-sync-setup.sh "$DEV" /tmp/magi1.pub root@$h:/root/
  ssh root@$h 'bash /root/pmgr-sync-setup.sh --role dest --source-pubkey /root/magi1.pub --source-from 10.75.132.201 --dev-pubkey /root/pmgr_sync_magi_dev.pub --dev-from MIOIP'
done
```

Il secondo giro dello stesso comando deve mostrare solo righe `[ok]` e nessun `[fatto]`.
Avvisi `[avviso]` su `AllowUsers` di sshd vanno risolti prima di proseguire.

## Cosa NON c'e' ancora

- `pmgr-snippets-sync` (lo strumento che confronta, copia e verifica) e l'accesso del manager con il suo comando ristretto: prossimo passo, per ora il setup non installa nessuna chiave del manager.
- La sincronizzazione dall'interfaccia del manager (pagina "Script e snippet").

# Aggiungere un nodo o un cluster

Procedura **manuale** valida oggi. Il manager la sostituira' a passi (pagine "Preparazione dei nodi", "Aggiungi nodo",
"Rimuovi nodo": ROADMAP 13, decisioni D148-D157 in `DOMANDE.md`); questo documento resta il riferimento per chi lavora
senza il manager. Gli strumenti sono in `tools/pmgr-scripts/` (README della cartella per i dettagli) e `tools/pmgr-sync/`.

Regole: sui nodi si lavora da root solo per i passi indicati; sulle VM sempre l'utente `tecnico`; nessuna password o chiave
privata finisce nel repository; ogni modifica ai cluster si fa con un'anteprima e una verifica dopo.

## A. Cluster nuovo

1. **Account del manager su Proxmox** (ruolo `PMGRManager` con i 24 privilegi, utente `pmgr@pve`, token `manager`):
   comandi e motivo di ogni privilegio nel `README.md`, sezione "Proxmox service account". Il privilegio
   `Datastore.AllocateSpace` e' necessario anche per **vedere gli snippet e le ISO** negli elenchi dello storage: senza,
   il controllo della Nuova VM li trova solo su alcuni nodi.
2. **Aggiungere il cluster al manager**: Impostazioni, Generale, cluster (URL e token `utente@realm!nome=valore`).
   Provare con "Test".
3. **Rete delle VM nuove**: Impostazioni, Infrastruttura (reti con gateway, DNS, dominio di ricerca).
   Nella stessa scheda, **Verifica TLS del cluster**: incolla l'output di `cat /etc/pve/pve-root-ca.pem` (da un nodo qualsiasi,
   certificato pubblico) e conferma con TOTP; da li' il manager verifica il certificato del cluster (dettagli nel README).
4. **Contenuto Snippets** sullo storage che li contiene (di solito `local`), da root su un nodo, **conservando i contenuti
   esistenti**:
   `pvesm status --storage local` per leggere i contenuti, poi `pvesm set local --content <attuali>,snippets`.
5. **Strumenti dei template** sul nodo che tiene gli snippet (da root): estrarre il pacchetto di `tools/pmgr-scripts/` in
   `/root/pmgr-scripts`; creare `/etc/pmgr/cluster.conf` (da `cluster.conf.example`, root, 0600), `/etc/pmgr/ssh_keys.pub`
   (chiavi pubbliche dei template) e `/etc/pmgr/template-password` (una riga, root, 0600); poi
   `bin/pmgr-node-install.sh` (sola lettura) e, se corretto, `--apply`: genera e installa gli snippet.
6. **Template**: `bin/pmgr-os.sh --all --dry-run`, poi senza `--dry-run` (uno per release, VMID = `VMID_BASE` + slot).
7. **Copia degli snippet sugli altri nodi**: passo C qui sotto.
8. **Prova**: creare una VM di prova dalla pagina Nuova VM (con utente aggiuntivo e disco aggiuntivo), controllarla con
   `bin/pmgr-vm-apply.sh --verify`, cancellarla.

## B. Nodo nuovo in un cluster esistente

1. Installare Proxmox VE alla **stessa versione maggiore** del cluster; ora sincronizzata; nome risolvibile; reti di
   management, cluster e (se serve) Ceph raggiungibili.
2. Preparare i repository (da root; oggi a mano, in futuro `pmgr-node-prepare.sh`, D156): repository `enterprise` disattivato,
   `pve-no-subscription` attivo e, se il cluster usa Ceph, il repository Ceph **della stessa release del cluster**
   (non quella piu' recente: due release di Ceph nello stesso cluster non vanno bene). Non toccare HA e corosync prima
   dell'ingresso nel cluster.
3. Aggiornare il nodo e riavviarlo se il kernel e' cambiato.
4. Ingresso nel cluster: `pvecm add <nodo-del-cluster>` sul nodo nuovo, con il nodo ancora **senza VM**.
5. Ceph, se il cluster lo usa: `pveceph install` con la stessa release, poi MON/MGR/OSD secondo il piano del cluster.
6. Storage e contenuto Snippets come al passo A.4; i bridge delle VM devono avere lo stesso nome degli altri nodi.
7. Accesso per la sincronizzazione (da root, una volta): `tools/pmgr-sync/pmgr-sync-setup.sh` (ruolo destinazione, README
   della cartella); la chiave del nodo sorgente resta limitata al comando `pmgr-sync-gate`.
8. Copia di script e snippet: passo C.
9. GPU, se ci sono: creare i mapping PCI del nodo (uno per scheda fisica) in Datacenter, Resource Mappings.
10. Controllo finale nel manager: il nodo compare in linea, la Nuova VM lo propone (RAM, storage, snippet), il
    Bilanciamento lo conta.

## C. Copiare script e snippet sui nodi

Dal nodo sorgente, come utente `pmgr-sync` (`/etc/pmgr/sync.conf`, righe `dest NOME HOST [profile=standard|hosting]`):
`pmgr-snippets-sync.sh status`, poi `run` (anteprima) e `run --apply`. Niente cancellazioni, hash verificati dopo la copia.
Dopo aver rigenerato gli snippet **da root**, riportare i file a `pmgr-sync:pmgr-sync`
(`chown pmgr-sync:pmgr-sync /var/lib/vz/snippets/*.yaml`), altrimenti rsync non riesce a impostarne la data.

## D. Rimuovere un nodo

Ordine richiesto da Proxmox; ogni passo si fa solo se il precedente e' andato bene.

1. Svuotare il nodo dalle VM (pagina Bilanciamento, "Simula lo spegnimento di <nodo>", poi un passo alla volta).
2. Togliere il nodo da HA (gruppi e risorse), replica, restrizioni degli storage e mapping PCI.
3. Ceph: OSD del nodo `out`, attendere `HEALTH_OK`, fermare e distruggere gli OSD, rimuovere MON/MGR/MDS del nodo
   controllando che ne restino abbastanza (quorum), togliere l'host dalla mappa CRUSH.
4. Spegnere il nodo. **Non deve tornare in rete con la stessa identita'** senza una reinstallazione.
5. Da un altro nodo: `pvecm delnode <nodo>`.
6. Pulizia: `/etc/pve/nodes/<nodo>` se rimasto, `pvecm updatecerts` sugli altri nodi.
7. Nel manager: rimuovere il nodo dalla configurazione di sincronizzazione (`sync.conf`) e dimenticarne l'impronta SSH.

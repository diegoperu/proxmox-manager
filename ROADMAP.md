# ProxmoxManager — Roadmap delle estensioni

Elenco unico dei lavori da fare, per poterli priorizzare e gestire. Le decisioni e la storia stanno in `CLAUDE.md` (sezioni "Lavori da fare" e "Registro modifiche") e `DOMANDE.md`; qui c'è solo cosa resta, in che ordine e cosa serve per partire.

**Come usarlo:** una riga per lavoro; lo stato si aggiorna qui e nel registro di `CLAUDE.md`. Un lavoro grosso parte sempre con la discussione (domande in `DOMANDE.md`) prima del codice. I modelli indicati sono suggerimenti, come da regola: `Opus` per design/sicurezza, `Sonnet` per l'implementazione.

Legenda stato: `da fare` · `in discussione` · `in corso` · `fatto`. Dimensione: S (≤1 giorno), M (2-4 giorni), L (oltre).

## Priorità consigliata

| # | Estensione | Valore | Dim. | Dipende da | Stato |
|---|---|---|---|---|---|
| 1 | Gruppi dai tag di Proxmox | report corretti (niente "gruppi" finti come `ubuntu`) | M | — | fatto e rilasciato (2026-11-04) |
| 2 | Convertire le 22 assegnazioni GPU dirette ai mapping | l'app può gestire tutte le GPU | S (lavoro in Proxmox) | — | da fare |
| 3 | Bilanciamento del cluster (proposte di spostamento VM) | nodi caricati in modo uguale (pareggio) | L | 1 (gruppi) utile, non bloccante | fatto (2026-11-04): algoritmo, pagina Bilanciamento, esecuzione passo-passo a caldo/a freddo (D50-D53), riserva minima per eseguire, filtro "solo queste VM" (D54), vincolo CPU per `cpu: host` (D55, poi flag CPU del nodo D63 dopo gli incidenti delle VM 109, 133, 198), revisione di sicurezza e prove reali (VM 109, 174) |
| 3b | Nodo consigliato alla creazione VM + GPU assegnata nel deploy | VM create nel posto giusto, GPU subito pronta | M-L | 3 (modello di carico e vincoli), pagina GPU | fatto e rilasciato (2026-11-05): consiglio del nodo e delle schede, creazione guidata dal server, utente e disco aggiuntivi (D56-D67), nome e memoria delle schede modificabili (D62); da provare a mano l'utente con rete |
| 4 | Uso reale delle GPU (`nvidia-smi`) | report su GPU assegnate ma inutilizzate o riservate | M | helper nei guest | **fatto e in servizio** (2026-11-19, PR #41): collector ogni 5 minuti, pagina GPU, pannello VM, report tecnico e manageriale; vedi CLAUDE.md "Monitoraggio delle GPU" |
| 5 | Coda richieste dell'intranet per i report | domanda non soddisfatta nei report manageriali | M | accesso/API intranet | da fare |
| 6 | Correggere `setup-disk` (usa il disco richiesto) | evita formattazioni/riavvii inutili | S | — | fatto (2026-11-05), D68 A: slot controllato, riavvio solo con conferma |
| 7 | Rifare la prova del fallback inline su Rocky SELinux | capire se gli helper sono davvero necessari | S | VM di prova | da fare |
| 8 | `allowed_hosts` (anti DNS rebinding) | sicurezza dietro reverse proxy | S | decisione D14 | in discussione |
| 9 | Errore JS aprendo direttamente `#/gpu` (`Gpu.summary`, gia' presente prima del 3b) | console pulita, nessun rischio di pagina vuota | S | — | fatto (2026-11-05): non piu' riproducibile; la pagina ora normalizza la risposta (vedi CLAUDE.md) |
| 10 | Controllo preventivo alla creazione VM: snippet `cicustom` presente sul nodo di destinazione; spazio anche per il volume clonato (non solo per il disco aggiuntivo) | niente VM lasciate spente a meta' | S | — | fatto (2026-11-05): file del template sul nodo (verificabile solo dove lo storage elenca quel tipo di contenuto) e spazio del clone |
| 11 | Colonna "disco" della lista VM: oggi solo `/`, estendibile al disco piu' pieno | stessa informazione del pannello | S | — | chiuso (2026-11-05): resta solo `/`, decisione utente |
| 13 | **Sincronizzazione di script e snippet dal nodo-1** (rsync generico + sezione di amministrazione) | il nodo-1 non e' piu' l'unica copia degli script usati dalle VM nuove e dal manager | L | decisioni D69-D99 | **in corso**: fatti `tools/pmgr-sync/` (accesso non root e comando forzato), nuovo tooling dei template e `pmgr-snippets-sync.sh` (locali, provati su Magi e installati su CALCOLO); **manca l'integrazione del manager** (D94 A, D97 A, D98 A: comando forzato `status\|diff\|run`, `snippet_sync` in `config.json`, endpoint, pagina "Script e snippet"; D95 aperta) |
| 14 | **Aggiornamento dei template dal manager** (pulsante che lancia la procedura di `update_*_template.sh`: scarica l'immagine, ricrea il template, rigenera lo snippet, sincronizza) | oggi si lancia a mano sul nodo-1 (~40 min, 3 esecuzioni registrate); con piu' copie degli script va deciso chi aggiorna cosa | L | roadmap 13 chiusa; decisioni su template per cluster (VMID e storage diversi), un solo aggiornamento alla volta, protezione contro la distruzione del template se il download fallisce (D84.2) | da progettare dopo la 13 |
| 15 | **Migrazione di una VM su un altro cluster** | richiesta utente 2026-11-05; Proxmox ha una migrazione remota (`remote_migrate`, token del cluster di destinazione) | L | roadmap 13 (cluster di prova Magi) | da progettare dopo la 13 |
| 12 | Prima di avviare una VM spenta, controllare che il nodo abbia la RAM per avviarla | niente avvii che sovraccaricano il nodo | S | modello di carico del bilanciamento | fatto (2026-11-05): avvio singolo, container e Batch; rifiuto 409 con i GB mancanti |
| 16 | **Rollout degli script sulle VM esistenti: CALCOLO** (fail2ban, aggiornamenti di sicurezza, SSH, agent-exec, GPU-aware) | VM allineate agli snippet nuovi | L | strumenti locali (`pmgr-vm-rollout.sh`, `pmgr-rollout-run.sh`) | **fatto e chiuso** (2026-11-10/12, "calcolo e' a posto"); 8 VM GPU con job saltate per scelta |
| 17 | **Rollout sulle VM esistenti: HOSTING** (firewall, fail2ban, hardening SSH, aggiornamenti, riavvii) | VM esposte a Internet messe in sicurezza | L | stessi strumenti, profilo hosting | **fatto in gran parte** (2026-11-12); restano 108 (in pausa), VM con sola password, porte pubblicate da Docker (D103), Ubuntu 20.04; report per i proprietari generato. Vedi il dettaglio sotto |
| 18 | Installare il nuovo tooling dei template sul cluster HOSTING (oggi i vecchi script in `/root/cloud-images`) e ricreare i template | le VM nuove di HOSTING nascono gia' conformi (firewall 22/80/443, SSH a sola chiave, hardening) | M | `pmgr-calcolo-install.sh` (profilo hosting con `PROFILE=hosting`), password dei template | da fare |
| 19 | `disk.sh`: controllare i comandi necessari e dire quale manca (oggi `parted` assente = exit 127) e aggiornare gli helper dei template Rocky | "Aggiungi disco" robusto sulle VM Debian esistenti (senza `parted`) | S | helper nei template | da fare |
| 20 | **Deploy non locale e manager multiutente con livelli** (macchina dedicata con IP proprio, login, ruoli diversi; richiesta ribadita dall'utente il 2026-11-19) | login e ruoli al posto della VPN come unico controllo; il manager non gira piu' sulla postazione; sblocca D95/D96 | L | decisione sull'architettura (Opus) | **prossimo**, da discutere |
| 21 | **Report: uso medio per VM su 1-12 mesi a scelta** (richiesta utente 2026-11-19) | capire se una VM e' sovradimensionata o abbandonata guardando l'andamento mese per mese | M | Report v2, storico giornaliero | **fatto e in servizio** (2026-11-19, PR #44 e #45): storico giornaliero per VM e istanza, report "Andamento delle VM", D115-D122 |
| 22 | **Report manageriale v3** (anonimo, sintesi + allegati, valore economico, disponibilita' al netto delle interruzioni, backup, GPU per taglia) con impostazioni per cluster e controllo dei listini esterni | documento per direzione e consiglio piu' leggibile e difendibile | L | voci 4 e 21 | **fatto e in servizio** (2026-11-19, PR #47); D123-D132 |
| 23 | **Impostazioni: revisione di usabilita'** (schede, riga di stato, anteprima del listino, barra di salvataggio, tabella storage, menu cluster) | pagina piu' corta e meno errori nei parametri | M | 22 | **fatto e in servizio** (2026-11-19, PR #49, #50) |
| 24 | **Controllo degli aggiornamenti dal repository** (richiesta utente 2026-11-19): il manager confronta il commit che sta girando con `main` su GitHub e lo dice nella pagina (Impostazioni → Informazioni e un avviso discreto), con le istruzioni per aggiornare; pulsante "Aggiorna e riavvia" **solo dopo la 20** | sapere subito se il servizio e' indietro; aggiornare senza terminale | S (controllo) / M (pulsante) | controllo: nessuna; pulsante: 20 (ruolo amministratore), servizio systemd | parte 1 (controllo) **fatta** 2026-11-20; parte 2 (binari su GitHub): workflow scritto e provato in locale, **da provare con il primo tag**; parte 3 con la 20; vedi dettaglio |
| 25 | **Valori di rete per cluster** nella creazione VM (reti con gateway, DNS, dominio di ricerca) | VM nuove con la rete giusta su ogni cluster | S | — | **fatto** (2026-11-20, D133 A): scheda Impostazioni → Infrastruttura |

## Dettaglio

### 1. Gruppi dai tag di Proxmox
- **Perché:** oggi il gruppo è il prefisso del nome VM (D27) e i report contano prefissi che non sono gruppi (`ubuntu`, `poli`, `blm`, `carlo`…).
- **Cosa:** campo "gruppo" in **Nuova VM** che scrive il tag (formato da definire, es. `gruppo-rossi`; validazione `[a-z0-9]{1,32}`); `vmGroup` e `BuildSnapshot` leggono il tag con ripiego sul nome; azione in blocco per etichettare le 107 VM esistenti; nota nei report sull'origine del gruppo.
- **Attenzione:** le fotografie già accumulate restano con il gruppo da nome (dichiararlo).
- **Prima del codice:** domande D35-D41 in `DOMANDE.md` (formato tag, VM senza tag, doppi tag, elenco gruppi, etichettatura delle esistenti, cambio dal pannello, storico). Da leggere in sola lettura: `user-tag-access`/`registered-tags` del datacenter.
- **Modelli:** progetto breve Opus medio, implementazione Sonnet alto.

### 13/16/17. Script e snippet, rollout sulle VM esistenti (stato al 2026-11-12)
- **Strumenti** (in `SUPPORT-FILES/pmgr-scripts/`, **fuori da git**; solo `tools/pmgr-sync/` e' nel repo): `pmgr-os.sh` (template guidati da dati, 8 release), snippet con fail2ban/aggiornamenti/SSH/firewall per profilo standard e hosting, `pmgr-vm-apply.sh` + `pmgr-vm-rollout.sh` (moduli `fail2ban`, `updates` GPU-aware e con esclusione Docker, `ssh-standard`, `ssh-hosting`, `firewall-hosting`, `hardening-hosting`, `agent-exec`, `baseline`, `--verify`), `pmgr-rollout-run.sh` (ondate con ripresa), `pmgr-snippets-sync.sh`, `pmgr-calcolo-install.sh`, `pmgr-push-key-guest.sh`. Test in container su 8 distribuzioni.
- **CALCOLO**: chiuso. **Magi**: cluster di prova allineato. **HOSTING**: fatto il 2026-11-12 (firewall su 104, 105, 108, 118; fail2ban su 20 VM; hardening SSH + sysctl + recidive su 13; aggiornamenti automatici su 19; riavvii su 10). Report per i proprietari: `~/rollout-hosting/REPORT-INTERVENTI-HOSTING.md` (fuori repo).
- **Resta**: 108 (D104), VM con sola password (102, 103, 112, 114, 117, 118: completare se i proprietari aggiungono una chiave), porte pubblicate da Docker (D103), Ubuntu 20.04 (117, 118), porta 25 in uscita (D93.2, aperta), integrazione del manager (voce 13), tooling sul cluster HOSTING (voce 18), snapshot `pre-*` da cancellare. Il promemoria resta aperto per HOSTING.

### 2. Convertire le assegnazioni GPU dirette ai mapping
- **Perché:** le 22 assegnazioni dirette sono mostrate ma non modificabili dall'app (D25).
- **Cosa:** in Proxmox, a VM spenta, sostituire `hostpciN: 0000:xx:yy` con `mapping=<NOME>` (i 27 mapping esistono già). Nessun codice; si può valutare uno strumento che prepara l'elenco "VM → mapping da usare".
- **Modelli:** nessuno (operazione manuale) o Sonnet basso per l'elenco.

### 3. Bilanciamento del cluster (proposte di spostamento VM)
- **Richiesta (2026-11-03):** dopo aver analizzato le VM in essere, proporre spostamenti per ottimizzare l'uso del cluster ed evitare nodi scarichi. **RAM prima, poi CPU. Nessun overbooking di RAM.** Le VM con GPU non si spostano.
- **Principi:** il sistema **propone, non esegue**: un piano ordinato di migrazioni con lo stato prima/dopo per nodo, da confermare passo per passo (come la pagina GPU); numero di migrazioni minimo; ogni VM bloccata mostra il motivo.
- **Non spostabili:** VM con GPU (diretta o mapping), altri dispositivi PCI/USB legati al nodo, dischi locali non condivisi, VM con `lock`, template, VM con HA vincolata.
- **Dati disponibili:** risorse assegnate (`/api/vms`), uso reale (RRD, p95 in `/api/reports/nodes` e `/vms`), inventario GPU, azione `migrate` già presente.
- **Obiettivo (deciso 2026-11-03): pareggiare**, cioe' nodi caricati il piu' possibile in modo uguale (non svuotare nodi). Misura: % di RAM assegnata per nodo (priorita'), poi CPU; il piano minimizza lo scarto tra nodi con il minor numero di migrazioni. I nodi con VM bloccate (GPU) hanno un carico fisso intorno a cui si distribuisce il resto.
- **Decisioni (2026-11-03):** migrazione **a caldo o a freddo a scelta** per ogni piano; **simulazione "e se spengo il nodo X"** approvata.
- **Da fissare (proposte):**
  - **Riserva di RAM per nodo** = la memoria che non si puo' assegnare alle VM perche' serve all'host: sistema operativo e Proxmox (proposta 8 GB), **piu' i demoni Ceph** che girano sul nodo (ogni OSD usa circa 4 GB di memoria di default, piu' MON/MGR se presenti). Proposta: **calcolo automatico** = 8 GB + 4 GB per ogni OSD del nodo (il numero di OSD si legge da Ceph, gia' disponibile), con **valore modificabile** nella pagina (e per nodo, se servisse). RAM assegnabile = RAM fisica - riserva; **nessun overbooking** sopra questo valore.
  - **Tetto di CPU** = quanto si accetta di caricare i processori. Due misure: (a) **rapporto vCPU assegnate / core fisici** (overcommit di CPU, e' ammesso a differenza della RAM: proposta massimo **4 vCPU per core**), (b) **uso reale** (p95 sulle ultime 4 settimane): proposta **non portare un nodo oltre l'80% di CPU p95** dopo lo spostamento. La CPU e' secondaria alla RAM: serve solo a non creare un nodo saturo mentre si pareggia la RAM.
  - Da confermare o cambiare i tre numeri: **8 GB** per l'host, **4 GB per OSD**, **4 vCPU per core** e **80% di CPU p95**.
- **Approvato (2026-11-03):** riserva RAM = 8 GB + 4 GB per OSD (automatica, modificabile) come **blocco duro**; CPU 4 vCPU per core e 80% p95 come **avviso non bloccante** (il piano e il nodo consigliato possono superarli ma segnalano "attenzione CPU" con il motivo).
- **Domande aperte:** nessuna sui parametri; resta il progetto dell'algoritmo e del simulatore (Opus) prima del codice. di RAM per host e Ceph; tetto di CPU; migrazione a caldo o a freddo e finestre di manutenzione; uso del reale (p95) oltre all'assegnato; simulazione "e se spengo il nodo X".
- **Passi proposti:** (1) domande e specifiche, (2) algoritmo e simulatore con dati di prova e test dei casi limite (Opus alto), (3) endpoint del piano in sola lettura, (4) pagina con piano e conferma passo-passo (Sonnet alto), (5) revisione di sicurezza e prova reale su VM indicate dall'utente (Opus medio).
- **Rischi:** una migrazione a caldo carica rete e storage; il piano non deve mai superare la RAM fisica; il piano si ricalcola subito prima di ogni passo (lo stato cambia).

### 3b. Nodo consigliato alla creazione VM + GPU nel deploy
- **Richiesta (2026-11-03), in cascata al bilanciamento:** in **Nuova VM**, dopo aver compilato i dati e scelto se la VM avra' o no una GPU (e quale modello), l'app propone il **nodo consigliato** (carico prima/dopo, motivazione, alternative); l'utente puo' cambiarlo.
- **Regole:** stessi vincoli del bilanciamento (niente overbooking di RAM, riserva host/Ceph, RAM poi CPU, pareggio). Con GPU i candidati sono solo i nodi con una scheda libera del modello richiesto (da `/api/gpus`, con mapping).
- **GPU nel deploy:** dopo il clone, a VM spenta, scrivere `hostpciN=mapping=<id>` con la logica di piano/conferma/lucchetto/digest gia' scritta per la pagina GPU. Se l'assegnazione fallisce dopo il clone: messaggio chiaro, la VM resta senza GPU (assegnabile dalla pagina GPU), nessuna cancellazione automatica.
- **Decisioni (2026-11-03):**
  - **Clone sul nodo destinazione:** da testare se Proxmox lo permette (`clone` con `target`); se no, logica attuale (clone sul nodo del template, poi migrazione) e **GPU assegnata dopo la migrazione**.
  - **Soft lock della scheda** durante la creazione: prenotazione con scadenza in memoria del server; la verifica vera resta quella a freddo prima di scrivere.
  - **Avvio automatico** dopo l'assegnazione della GPU e in generale dopo la creazione (opzione, attiva di default; se la GPU fallisce la VM non parte e lo dice).
  - **Elenco delle schede disponibili** mostrato nel modulo (per nodo: modello, mapping, libera/prenotata).
  - **Utente aggiuntivo** nel modulo (nome + chiave SSH o password fissa, stessa logica del pulsante "Aggiungi utente" via guest agent e `adduser.sh`): fino a **due utenti al primo avvio** (uno da cloud-init, uno aggiunto dopo); serve attendere l'agent dopo l'avvio; se fallisce la VM resta creata e l'utente si aggiunge dal pulsante.
- **Scheda precisa:** l'utente sceglie la scheda esatta; l'elenco e' **raggruppato per memoria della scheda** (24/40/80 GB…; da ricavare dal modello, tabella da verificare sulle schede reali), poi per modello e nodo.
- **Schede in uso:** restano visibili **in grigio, non selezionabili, con il nome della VM** assegnata (anche se diretta); una scheda prenotata dal soft lock compare come "in creazione".
- **Nessun nodo idoneo:** il modulo spiega **perche'**, nodo per nodo: RAM (richiesta vs. disponibile dopo la riserva), core, scheda non libera, storage incompatibile, nodo fuori linea; con la quantita' che manca ("mancano 12 GB di RAM") e il nodo piu' vicino a essere idoneo.
- **Decisioni complete:** non restano punti aperti per 3b a parte quelli del bilanciamento (riserva di RAM, tetto CPU).
- **Primo passo utile:** prova in sola lettura/su VM di test del clone con `target` verso un altro nodo.
- **Dipende da:** 3 (modello di carico) e dalla pagina GPU (gia' fatta). Si puo' partire con una versione senza GPU nel deploy (solo nodo consigliato) e aggiungere la GPU dopo.
- **Modelli:** design Opus medio, implementazione Sonnet alto, revisione Opus medio (scrive configurazione hardware).

### 4. Uso reale delle GPU (`nvidia-smi`)
- **Perché:** Proxmox sa quale VM ha quale scheda, non quanto la usa (D34).
- **Cosa:** helper `nvidia-smi` in `/usr/local/libexec/pmgr/` (regole degli altri helper: solo ASCII, argomenti posizionali, sola lettura) + raccolta e salvataggio dei campioni; poi estendere i report (utilizzo per VM e gruppo, schede assegnate ma inutilizzate).
- **Attenzione:** richiede aggiornare i template/snippet cloud-init (fuori repo) e gli helper nei guest già esistenti; nuova scelta di architettura sulla raccolta (un collector in più: nuova eccezione alla regola "niente collector").
- **Modelli:** design Opus medio, implementazione Sonnet alto.

### 5. Coda richieste dell'intranet (solo reportistica)
- **Cosa:** leggere dall'intranet le richieste di risorse in attesa per mostrare la domanda non soddisfatta nei report manageriali ("N gruppi in attesa di GPU").
- **Da definire:** API o export dell'intranet, autenticazione (credenziali fuori dal repo), sola lettura, nessun dato personale nei PDF.
- **Fino ad allora:** i report non mostrano gruppi in attesa.

### 6. `setup-disk` usa il disco richiesto
- **Limite noto:** `setup-disk` valida `disk` ma sceglie l'unico disco vuoto; se non lo trova forza uno stop+start e risponde 502 anche quando il disco è già configurato.
- **Cosa:** usare il device indicato (mappando lo slot Proxmox al device del guest) e rendere idempotente il caso "già configurato".

### 7. Prova del fallback inline su Rocky con SELinux enforcing
- **Perché:** il timeout (`got timeout`) era stato attribuito a SELinux ma la causa probabile erano i caratteri non ASCII negli script inline (ora bloccati dai test). Va rifatta la prova con gli script ASCII per sapere se gli helper etichettati restano necessari.
- **Serve:** una VM Rocky con SELinux enforcing, agent in salute.

### 8. `allowed_hosts` (D14)
- **Perché:** la guardia `/api` controlla già origine e `Sec-Fetch-Site`; `allowed_hosts` aggiunge la difesa dal DNS rebinding.
- **Stato:** "per ora no, dobbiamo valutare" (2026-11-02). Da riprendere se l'app viene esposta dietro reverse proxy.

## Idee non ancora discusse (da valutare)

- **Manager multiutente con livelli diversi** (richiesta utente 2026-11-09): login e ruoli al posto della VPN come unico controllo; riprendere il punto unico di autorizzazione previsto da D95.
- **Manager su macchina dedicata con IP proprio** (richiesta utente 2026-11-09): sblocca D96 (`from=` esatto sulla chiave del manager).

- **Docker e firewall**: regole `DOCKER-USER` per le porte pubblicate (D103); documentare ai clienti che Docker aggira ufw.
- **Valori di rete per cluster** (2026-11-09): gateway, prefisso IP, DNS e dominio di ricerca come impostazioni di ogni cluster, chiesti quando lo si aggiunge; oggi la pagina Nuova VM usa le costanti di CALCOLO (gateway 10.75.4.254). Vedi `CLAUDE.md`, Lavori da fare.
- Esportare il report manageriale anche come pagina HTML autonoma.
- Report programmati (oggi esclusi: si generano a richiesta).
- Confronto tra due periodi nello stesso report.
- Avvisi (mail/chat) su soglie del Bollettino o su Ceph.

## Come aggiornare
1. Cambia lo stato nella tabella e aggiungi una riga al registro modifiche di `CLAUDE.md`.
2. Quando un lavoro parte, crea il branch e le domande in `DOMANDE.md`.
3. A lavoro finito sposta la riga in "fatto" (o rimuovila) e tieni le decisioni in `CLAUDE.md`.

### 20. Deploy non locale e manager multiutente con livelli
- **Richiesta (ribadita 2026-11-19):** portare il manager fuori dalla postazione (macchina dedicata, IP proprio) con utenti a diversi livelli.
- **Da discutere prima del codice (Opus):** autenticazione (account locali con argon2id, oppure SSO/LDAP di ateneo), ruoli (es. sola lettura, operatore VM, amministratore cluster) e quali azioni ciascuno puo' fare, sessioni e CSRF con login, audit log di chi fa cosa, TLS e certificato, dove stanno token Proxmox e segreti, rete (chi raggiunge la macchina), aggiornamento e backup del servizio, collector (fotografie, uso GPU) che girano lato server. Sblocca D95 (chi puo' sincronizzare gli snippet) e D96 (`from=` della chiave SSH).
- **Rischi:** oggi la difesa e' solo la rete fidata + guardia `/api`; esporre il servizio senza login renderebbe raggiungibili scritture su VM e cluster.

### 21. Report: uso medio per VM su 1-12 mesi
- **Richiesta (2026-11-19):** report dell'uso medio di ogni VM su un periodo scelto da 1 a 12 mesi, per capire se e' sovradimensionata o abbandonata.
- **Gia' esistente da cui partire:** il report tecnico "VM e container" (periodi 30 giorni / trimestre / semestre / 12 mesi / personalizzato, CPU e RAM medie e p95, segnalazioni D32 "inattiva" e "sovradimensionata").
- **Cosa aggiungerebbe (da confermare):** scelta del periodo in mesi (1-12), andamento **mese per mese** per VM (media di CPU, RAM, rete, disco per ogni mese) per vedere quando una VM ha smesso di lavorare, ultimo mese con attivita', proposta di ridimensionamento (RAM/core suggeriti dal p95). Vincolo dell'RRD: oltre ~90 giorni solo dati settimanali, quindi niente p95 vero sui mesi vecchi.
- **Da valutare (richiesta utente 2026-11-19): pro e contro di un salvataggio proprio dei dati di consumo**, finora escluso ("niente collector": lo storico viene dall'RRD di Proxmox; eccezioni solo D28 per l'inventario e ROADMAP 4 per le GPU).
  - *Pro:* risoluzione fine anche sui mesi vecchi (p95 vero su 12 mesi, oggi impossibile oltre ~90 giorni); i dati delle VM cancellate restano (l'RRD sparisce con la VM); storico indipendente da Proxmox (reinstallazioni, migrazioni tra cluster, ROADMAP 15); aggregati gia' pronti (report piu' veloci, meno chiamate a Proxmox); stesso meccanismo del collector GPU gia' scritto.
  - *Contro:* un servizio che deve girare sempre (oggi il manager sta sulla postazione: buchi nei dati quando e' spento, si risolve solo con la macchina dedicata della ROADMAP 20); crescita del database (stima da fare: ~110 VM x campioni x 12 mesi; con aggregati giornalieri e' piccola, con campioni a 5 minuti no); carico costante su Proxmox (una lettura RRD o di stato per VM a ogni giro); retention, backup e privacy da decidere (consumi per VM = attivita' dei gruppi); il giorno di attivazione e' il primo dato (nessuno storico all'indietro, a differenza dell'RRD).
  - *Via di mezzo da considerare:* nessun campionamento proprio, ma ogni notte si salvano gli aggregati giornalieri per VM ricavati dall'RRD del giorno (risoluzione a 1 minuto disponibile per le ultime 24 ore): p95 giornaliero vero per sempre, poche righe, nessun carico continuo, buchi recuperabili finche' l'RRD ha il giorno.
  - Decisione da prendere con una domanda in `DOMANDE.md` all'avvio del lavoro; il "niente collector" in `CLAUDE.md` va aggiornato solo dopo la risposta.

### 24. Controllo degli aggiornamenti dal repository e binari su GitHub (design 2026-11-20, D134)

**Fatti:** repository pubblico, l'API di GitHub risponde senza token (60 richieste/ora per IP); nessun tag ne' release; la versione `v0.N.0` della pagina e' scritta dall'hook sul branch e **non coincide con i commit di `main`** (squash: 194 contro 192), quindi il binario deve portare il **commit** con cui e' stato compilato; build attuale dinamica (glibc) e quindi legata alla distribuzione. **Provato:** build **statica musl** in un contenitore Alpine (`-tags "netgo osusergo sqlite_omit_load_extension"`, `-linkmode external -extldflags -static`): 12 MB, non dinamica, parte su Rocky 8.9 e crea il database SQLite.

**Parte 1, controllo (S, prima della 20):**
- *Versione incisa* con `-ldflags -X` (commit, data, "dirty"): in `build.sh`, nel workflow e nella procedura di deploy; `GET /api/version` (solo locale, niente rete).
- *Controllo* (`internal/handlers/updatecheck.go`): `GET https://api.github.com/repos/diegoperu/proxmox-manager/compare/<commit>...main` (indirizzo e repository fissi nel codice, il commit e' una stringa esadecimale validata; TLS verificato, timeout 15 s, corpo massimo 1 MB, redirect solo stesso host, `User-Agent` generico) -> `ahead_by` e i titoli (prima riga, 120 caratteri, massimo 20) dei commit mancanti; 404 = commit sconosciuto (build locale con modifiche) -> "versione non confrontabile". Cache in memoria 6 ore, "Controlla ora" al massimo ogni 5 minuti, un controllo alla volta, `update_check: false` lo spegne, rete assente = lo dice e basta. Quando esisteranno le release: confronto con l'ultima release (`/releases/latest`) invece di `main`.
- *Pagina:* Impostazioni → Generale → Informazioni: versione, commit, data, esito ("aggiornato", "N commit indietro" con i titoli come testo, "non raggiungibile"), pulsante "Controlla ora" e "Come aggiornare" (da sorgenti: `git pull --ff-only`, `./build.sh`, riavvio; da binario: scarica la release, verifica `SHA256SUMS`, sostituisci, riavvia). Un puntino discreto accanto alla versione nella barra laterale quando e' indietro. Testi di GitHub solo come testo (`esc()`), mai come link cliccabili.
- *Sicurezza:* nessun dato nostro viaggia (solo il commit, pubblico); risposta non fidata (parsing stretto, tetti, esadecimali validati); il controllo non esegue ne' scarica niente.

**Parte 2, binari compilati su GitHub (S-M):**
- Workflow `.github/workflows/release.yml` su tag `vAAAA.MM.GG[.N]` (o avvio manuale): job in contenitore Alpine, `go vet` + `go test`, build statica linux/amd64 con versione incisa, `SHA256SUMS`, **attestazione di provenienza** (`actions/attest-build-provenance`, verificabile con `gh attestation verify`), creazione della release con `gh release create` e note dai commit. Permessi minimi (`contents: write` solo nel job di pubblicazione), azioni fissate per commit SHA, nessun segreto (con C1 la firma e' fuori dal workflow).
- `build.sh` resta per chi compila dai sorgenti (macOS compreso).
- Prima release: tag sul commit attuale di `main`, binario di prova scaricato e provato su una macchina pulita.

**Parte 3, pulsante "Aggiorna e riavvia" (con la voce 20, solo ruolo amministratore):** scarica la release, verifica **firma ed25519** (C1) e checksum, tiene il binario precedente, sostituisce e termina; systemd (`Restart=always`) lo rilancia; ritorno indietro con un clic. Prerequisiti: login e ruoli, servizio systemd, chiave pubblica incorporata.

**Piano a step:** (1) design di sicurezza [FATTO, questo testo, Opus medio]; (2) versione incisa + controllo + pagina + test con GitHub finto [Sonnet alto, via Codex]; (3) workflow di release + prima release provata [Sonnet medio, via Codex, revisione mia]; (4) firma e pulsante dentro la 20 [Opus medio].

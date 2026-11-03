# ProxmoxManager — Roadmap delle estensioni

Elenco unico dei lavori da fare, per poterli priorizzare e gestire. Le decisioni e la storia stanno in `CLAUDE.md` (sezioni "Lavori da fare" e "Registro modifiche") e `DOMANDE.md`; qui c'è solo cosa resta, in che ordine e cosa serve per partire.

**Come usarlo:** una riga per lavoro; lo stato si aggiorna qui e nel registro di `CLAUDE.md`. Un lavoro grosso parte sempre con la discussione (domande in `DOMANDE.md`) prima del codice. I modelli indicati sono suggerimenti, come da regola: `Opus` per design/sicurezza, `Sonnet` per l'implementazione.

Legenda stato: `da fare` · `in discussione` · `in corso` · `fatto`. Dimensione: S (≤1 giorno), M (2-4 giorni), L (oltre).

## Priorità consigliata

| # | Estensione | Valore | Dim. | Dipende da | Stato |
|---|---|---|---|---|---|
| 1 | Gruppi dai tag di Proxmox | report corretti (niente "gruppi" finti come `ubuntu`) | M | — | da fare |
| 2 | Convertire le 22 assegnazioni GPU dirette ai mapping | l'app può gestire tutte le GPU | S (lavoro in Proxmox) | — | da fare |
| 3 | Bilanciamento del cluster (proposte di spostamento VM) | nodi caricati in modo uguale (pareggio) | L | 1 (gruppi) utile, non bloccante | in discussione |
| 3b | Nodo consigliato alla creazione VM + GPU assegnata nel deploy | VM create nel posto giusto, GPU subito pronta | M-L | 3 (modello di carico e vincoli), pagina GPU | da fare |
| 4 | Uso reale delle GPU (`nvidia-smi`) | report su GPU assegnate ma inutilizzate | M | helper nei guest | da fare |
| 5 | Coda richieste dell'intranet per i report | domanda non soddisfatta nei report manageriali | M | accesso/API intranet | da fare |
| 6 | Correggere `setup-disk` (usa il disco richiesto) | evita formattazioni/riavvii inutili | S | — | da fare |
| 7 | Rifare la prova del fallback inline su Rocky SELinux | capire se gli helper sono davvero necessari | S | VM di prova | da fare |
| 8 | `allowed_hosts` (anti DNS rebinding) | sicurezza dietro reverse proxy | S | decisione D14 | in discussione |

## Dettaglio

### 1. Gruppi dai tag di Proxmox
- **Perché:** oggi il gruppo è il prefisso del nome VM (D27) e i report contano prefissi che non sono gruppi (`ubuntu`, `poli`, `blm`, `carlo`…).
- **Cosa:** campo "gruppo" in **Nuova VM** che scrive il tag (formato da definire, es. `gruppo-rossi`; validazione `[a-z0-9]{1,32}`); `vmGroup` e `BuildSnapshot` leggono il tag con ripiego sul nome; azione in blocco per etichettare le 107 VM esistenti; nota nei report sull'origine del gruppo.
- **Attenzione:** le fotografie già accumulate restano con il gruppo da nome (dichiararlo).
- **Prima del codice:** formato del tag, chi può assegnarlo, cosa fare delle VM senza tag.
- **Modelli:** progetto breve Opus medio, implementazione Sonnet alto.

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
- **Domande aperte:** riserva di RAM per host e Ceph; tetto di CPU; migrazione a caldo o a freddo e finestre di manutenzione; uso del reale (p95) oltre all'assegnato; simulazione "e se spengo il nodo X".
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
- Esportare il report manageriale anche come pagina HTML autonoma.
- Report programmati (oggi esclusi: si generano a richiesta).
- Confronto tra due periodi nello stesso report.
- Avvisi (mail/chat) su soglie del Bollettino o su Ceph.

## Come aggiornare
1. Cambia lo stato nella tabella e aggiungi una riga al registro modifiche di `CLAUDE.md`.
2. Quando un lavoro parte, crea il branch e le domande in `DOMANDE.md`.
3. A lavoro finito sposta la riga in "fatto" (o rimuovila) e tieni le decisioni in `CLAUDE.md`.

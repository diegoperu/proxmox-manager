# DOMANDE — branch `experiment/ui-redesign`

Domande a cui rispondere prima di scrivere codice per il redesign grafico/UX.

### D1 — Ambito: restyle o ripensamento UX
**Stato**: RISPOSTA
**Bloccante**: sì
**Domanda**: Il branch tocca solo l'aspetto (token, tipografia, materiali, layout) o anche i flussi (navigazione, pannello dettagli, command palette, azioni di massa)?
**Perché conta**: cambia di molto il costo e il rischio di regressioni su un unico `index.html` da ~2600 righe.
**Opzioni**: A) solo pelle (CSS + markup, stessi flussi) B) pelle + UX (palette ⌘K, pannello laterale al posto dei modal, centro attività, breadcrumb, hash-routing) C) B + ridisegno di dashboard/nodi con visualizzazioni nuove (topologia, heatmap)
**Suggerimento**: B a fasi: prima il sistema visivo (fase 1), poi i flussi uno alla volta (fasi 2+), ognuna in commit separati e rivedibile.
**Risposta utente**: 2026-11-02 — TUTTO (opzione C): pelle + flussi + dashboard ridisegnata, a fasi.

### D2 — Sorte dei 5 temi esistenti
**Stato**: RISPOSTA
**Bloccante**: sì
**Domanda**: PRODUCT.md dice che auto/dark/light/eva01/catppuccino sono identità del prodotto. Nel redesign: A) restano tutti e il nuovo look è un livello comune sopra i token B) nuovo tema di default (coppia chiaro/scuro che segue l'OS) + gli altri mantenuti come varianti di colore C) gli altri temi sono rimossi.
**Perché conta**: A/B implicano design a token (ruoli semantici) e verifica contrasto su 5+ temi; C è molto più semplice ma cancella un impegno di brand.
**Opzioni**: A) tutti, stesso look B) nuovo default + varianti C) rimozione
**Suggerimento**: B. Il nuovo look vive nei token; eva01/catppuccino diventano solo palette sopra la stessa struttura.
**Risposta utente**: 2026-11-02 — campo libero a Claude. Decisione: B. I nomi tema restano (auto, dark, light, eva01, catppuccino); auto/dark/light adottano il nuovo look "Horizon", eva01 e catppuccino diventano palette sopra la stessa struttura.

### D3 — Densità: spazioso stile Apple vs denso da operatore
**Stato**: RISPOSTA
**Bloccante**: sì
**Domanda**: Principio 1 di PRODUCT.md: densità e scansione rapida vincono sulla decorazione. Lo stile Apple è più arioso. Come si risolve?
**Perché conta**: tabelle VM con decine di righe e azioni di massa non devono rallentare.
**Opzioni**: A) arioso ovunque B) denso ovunque (cambia solo la pelle) C) due densità (comoda/compatta) scelte in impostazioni, default comoda per dashboard e dettagli, compatta per tabelle e batch
**Suggerimento**: C, preferenza salvata in `config.json` (niente localStorage).
**Risposta utente**: 2026-11-02 — concordo (C): due densita', comoda/compatta, preferenza in config.json.

### D4 — Font
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: SF Pro non è ridistribuibile. Per il look Apple propongo Inter (variable, licenza OFL) vendorizzato in `web/static/` + JetBrains Mono mantenuto solo per dati/ID/log. Barlow Condensed uscirebbe dal nuovo default. Accetti la nuova dipendenza (font, nessun codice)?
**Perché conta**: nuovo asset nel binario, aggiornamento di `THIRD_PARTY.md` con hash SHA-256, verifica del vincolo "nessun CDN".
**Opzioni**: A) Inter + JetBrains Mono B) stack di sistema (`system-ui`: SF su Mac, Segoe su Windows, Cantarell su Linux, nessun asset nuovo ma resa diversa per OS) C) tenere i font attuali
**Suggerimento**: A, resa identica su tutti i dispositivi del team.
**Risposta utente**: 2026-11-02 — A: Inter vendorizzato + JetBrains Mono per dati.

### D5 — Come provare il branch senza toccare il servizio in uso
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Il servizio vivo gira da `proxmox-manager-use/`. Per l'anteprima avvio un binario del branch su un'altra porta (es. `:8081`) con una copia di `config.json`, senza toccare la directory in uso. Va bene?
**Perché conta**: evita di sostituire l'app che il team usa; la copia contiene il token API, quindi 0600 e fuori dal repo.
**Opzioni**: A) porta 8081 con config copiata (token reale, sola lettura nei miei test) B) porta 8081 con cluster finto/mock (nessun token, ma dati inventati per vedere il layout) C) solo build, anteprima la fai tu
**Suggerimento**: B per le prime fasi (grafica), A solo quando serve verificare dati reali.
**Risposta utente**: 2026-11-02 — ok: binario del branch su :8081 con dati finti.

### D6 — Barra azioni flottante per selezione multipla
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Oggi la selezione a checkbox nella pagina VM è stata rimossa e le azioni di massa stanno solo in `batch`. In stile cloud moderno si può avere una barra flottante in basso quando si selezionano righe. La reintroduciamo?
**Perché conta**: decisione già presa il 2026-10-13 di toglierla; la riapro solo se vuoi.
**Opzioni**: A) no, batch resta separata B) sì, barra flottante che porta alla pagina batch con la selezione precompilata C) sì, azioni dirette dalla barra
**Suggerimento**: B.
**Risposta utente**: 2026-11-02 — costruire, opzione B (la barra apre Batch con selezione e azione preselezionate). 2026-11-02 — campo libero, l'utente valuta alla fine. Assunzione provvisoria: B (barra flottante che porta a batch con selezione precompilata).

### D7 — Server Proxmox finto per le anteprime: tenerlo nel repo?
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Per l'anteprima uso `mockpve.py` (Python stdlib, sola lettura, solo 127.0.0.1, dati sintetici), che oggi sta fuori dal repo. Lo aggiungo al repo (es. `devtools/mockpve/`) o resta locale?
**Perché conta**: aggiunge un file Python a un repo solo Go; utile per vedere il layout senza token reali e per i test visivi.
**Opzioni**: A) resta fuori dal repo B) `devtools/mockpve/mockpve.py` con README breve
**Suggerimento**: B, cosi' chiunque puo' riprodurre l'anteprima.
**Risposta utente**: 2026-11-02 — ok al suggerimento: opzione B, `devtools/mockpve/` con README breve.

### D8 — Porta di anteprima
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: La 8081 risulta occupata da un altro servizio (podman/pasta), quindi l'anteprima gira su `127.0.0.1:8090`.
**Risposta utente**: informativa, nessuna azione richiesta (D5 autorizzava una porta di anteprima con dati finti). L'utente ne ha preso atto il 2026-11-02.

## Funzione "dmesg" nel pannello VM (branch `feat/vm-dmesg`)

### D9 — Dove si vede il dmesg
**Stato**: RISPOSTA
**Bloccante**: sì
**Domanda**: Il dmesg e' lungo (righe da 100-200 caratteri) e il pannello VM e' largo 460px. Dove lo mostriamo?
**Perché conta**: leggibilita' di un log; cambia il markup e lo spazio.
**Opzioni**: A) sezione "Log del kernel" dentro il pannello, chiusa; si apre e legge al clic, con scorrimento orizzontale B) pulsante nel pannello che apre una finestra larga (90% dello schermo) con il log e i filtri C) A con pulsante "Espandi" che porta alla vista larga di B
**Suggerimento**: C.
**Risposta utente**: 2026-11-02 — opzione C: sezione nel pannello (si legge al clic) con pulsante "Espandi" per la vista larga.

### D10 — Quali VM
**Stato**: RISPOSTA
**Bloccante**: sì
**Domanda**: Solo QEMU con guest agent attivo e Linux (il dmesg arriva dall'agent)? I container LXC non hanno un comando equivalente nell'API Proxmox (non esiste un exec); le VM Windows non hanno dmesg.
**Opzioni**: A) solo QEMU Linux, pulsante spento con il motivo per LXC, VM ferme e Windows/senza agent B) anche altro (es. LXC via console seriale: fuori scopo)
**Suggerimento**: A.
**Risposta utente**: 2026-11-02 — come suggerito: solo QEMU Linux con guest agent; spento (con il motivo) per LXC, VM ferme e Windows.

### D11 — Contenuto e filtri
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Quante righe e quali filtri? Proposta: ultime 300 righe (scelta 100/300/1000), filtro livello "tutto" oppure "avvisi ed errori", orario leggibile; nel browser ricerca testuale, evidenziazione di errori/avvisi, copia e scarica .txt.
**Suggerimento**: la proposta cosi' com'e'.
**Risposta utente**: 2026-11-02 — come suggerito: 300 righe (100/300/1000), livello tutto o avvisi ed errori, ricerca, evidenziazione, copia, scarica .txt.

### D12 — Guest con SELinux (Rocky/RHEL)
**Stato**: RISPOSTA
**Bloccante**: sì
**Domanda**: Come per add-user e dischi, sui guest con SELinux enforcing la shell dell'agent e' confinata: per leggere il dmesg serve un helper etichettato `dmesg.sh` in `/usr/local/libexec/pmgr/` (a sola lettura, senza argomenti liberi), da aggiungere ai template cloud-init. Su Debian/Ubuntu basta l'esecuzione inline. Va bene aggiungere l'helper (tu aggiorni i template) e, dove manca, mostrare un messaggio chiaro?
**Suggerimento**: si'.
**Risposta utente**: 2026-11-02 — si': helper `dmesg.sh` a sola lettura; i template cloud-init vanno rigenerati e risincronizzati.

### D13 — VM di prova
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Su quale VM posso provare la funzione end to end (lettura in sola lettura, nessuna modifica)? Una Ubuntu/Debian e una Rocky con SELinux, se ce ne sono.
**Risposta utente**: 2026-11-02 — VM di prova: `ai-testing-diego` (Ubuntu 24.04, nodo 5, VMID 202) e `tosi-vm-8` (Rocky 8.10 con SELinux, nodo 3, VMID 219). Richiesta aggiuntiva: colonna Sistema operativo (nome e versione) nella lista VM & Container.

### D14 — Elenco degli host ammessi (`allowed_hosts`)
**Stato**: APERTA
**Bloccante**: no
**Domanda**: La guardia su `/api` blocca CSRF e richieste da altre origini senza configurazione. Il controllo sull'intestazione `Host` (contro il DNS rebinding) e' pero' opzionale (`allowed_hosts` in config.json, vuoto = spento) perche' dipende dai nomi con cui raggiungete il servizio. Vuoi attivarlo sul servizio vero? Servono i nomi che si digitano nel browser (es. localhost, il nome del server).
**Suggerimento**: si', con i nomi che usate davvero.
**Risposta utente**:

## Pagina Ceph e riepilogo nella dashboard (branch `feat/ceph`)

Stato verificato (2026-11-02, sola lettura): il cluster CALCOLO ha 2 storage `rbd` (Ceph); HOSTING no.

### D15 — Solo lettura o anche azioni
**Stato**: RISPOSTA
**Bloccante**: sì
**Domanda**: La pagina Ceph mostra soltanto lo stato, oppure deve anche permettere azioni (flag `noout`/`norebalance`, mettere un OSD out/in, riavviare un OSD)?
**Perché conta**: le azioni cambiano lo stato del cluster di storage, richiedono privilegi piu' alti sul token e conferme tipo reboot nodo; la sola lettura e' molto piu' sicura.
**Opzioni**: A) solo lettura B) lettura + flag di manutenzione C) lettura + azioni sugli OSD
**Suggerimento**: A ora; le azioni, se servono, in una fase separata con conferma scritta.
**Risposta utente**: 2026-11-02 — solo lettura (A).

### D16 — Presenza nel menu
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: La voce "Ceph" compare nel menu solo per i cluster che hanno Ceph (verifica automatica, nascosta per HOSTING) oppure sempre?
**Suggerimento**: solo dove Ceph e' presente; la dashboard mostra il riepilogo Ceph solo in quel caso.
**Risposta utente**: 2026-11-02 — come suggerito: voce e riquadro solo dove Ceph e' presente.

### D17 — Storico e aggiornamento
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: L'API Proxmox da' i valori istantanei di I/O e recupero, non uno storico. Va bene aggiornare ogni 5 secondi con la pagina aperta e tenere in memoria del browser gli ultimi 10 minuti (nessun salvataggio, niente cache sul disco)? Con la pagina chiusa non si accumula storia.
**Suggerimento**: si'.
**Risposta utente**: 2026-11-02 — come suggerito: ogni 5 s con pagina aperta, 10 minuti in memoria del browser.

### D18 — Regole della "perturbazione"
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Come entra Ceph nel calcolo generale? Proposta: HEALTH_ERR = perturbazione (attenzione, rosso); HEALTH_WARN = quasi sereno (ambra); capacita' >= 85% (nearfull) ambra, >= 95% (full) rosso; OSD down o recupero in corso entrano come voci nella frase sotto il titolo. Le soglie 85/90/95 sono quelle predefinite di Ceph (Proxmox non le espone).
**Suggerimento**: la proposta cosi' com'e'.
**Risposta utente**: 2026-11-02 — come suggerito.

### D19 — Dati di prova
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Per sviluppare aggiungo a `mockpve` uno stato Ceph finto con piu' casi (sano, avviso, errore, recupero in corso, OSD fermi); poi verifico in sola lettura sul cluster CALCOLO. Il token di CALCOLO deve avere il privilegio `Sys.Audit` sui nodi: se manca la pagina mostrera' "permessi insufficienti". Va bene?
**Suggerimento**: si'.
**Risposta utente**: 2026-11-02 — si'.

## Assegnazione GPU alle VM (2026-11-02, branch `feat/gpu-assign`)

Decisioni gia' prese dall'utente (2026-11-02): le assegnazioni dall'app passano **solo** dai resource mapping PCI di Proxmox (li crea l'utente); le assegnazioni dirette (`hostpciN: 0000:xx:yy`) si leggono e si segnalano ma non si modificano dall'app; le opzioni di una nuova assegnazione si copiano da quelle gia' attive; una VM accesa non si modifica (avviso + pulsante per spegnerla); solo schede Nvidia di classe display, con tutte le funzioni della scheda (audio compreso).

Dati letti sul cluster CALCOLO (sola lettura, 2026-11-02): 22 assegnazioni, tutte dirette; 20 senza opzioni (`hostpci0: 0000:xx:yy`, tutte le funzioni), 2 con `rombar=0`; tutte le VM con GPU sono `q35` (21 con OVMF). Esistono 4 mapping PCI, ognuno con una sola voce su un solo nodo e con il percorso **di una sola funzione** (`...:00.0`).

### D20 — Un mapping per scheda o mapping "a gruppo"
**Stato**: RISPOSTA
**Bloccante**: si'
**Domanda**: In Proxmox un mapping puo' contenere una scheda sola oppure piu' schede (anche su piu' nodi): con piu' schede, all'avvio della VM Proxmox ne sceglie una libera, quindi non si sa in anticipo quale. Come vuoi creare i mapping?
**Perche' conta**: con un mapping per scheda l'app sa esattamente quale scheda ha ogni VM e puo' impedire i doppioni prima di scrivere. Con i gruppi la "doppia assegnazione" diventa "piu' VM che schede nel gruppo su quel nodo" e il conflitto si vede solo come sovrapprenotazione.
**Opzioni**: A) un mapping per scheda fisica (es. `n10-a100-1`), una voce per nodo; B) mapping a gruppo per modello; C) entrambi: l'app gestisce A e per B mostra solo il conteggio (sola lettura).
**Suggerimento**: A (eventualmente C se i gruppi esistono gia'): e' l'unico modo in cui "stessa GPU a due VM" e' verificabile prima della scrittura.
**Risposta utente**: 2026-11-02 — come suggerito.

### D21 — Percorso del mapping: una funzione o tutte
**Stato**: RISPOSTA
**Bloccante**: si'
**Domanda**: I 4 mapping esistenti puntano alla sola funzione `.0` (solo la GPU, senza l'audio della scheda), mentre le assegnazioni dirette passano tutte le funzioni. Per i nuovi mapping vuoi il percorso senza funzione (`0000:xx:yy`, tutte le funzioni, come oggi) o con `.0`?
**Perche' conta**: hai scelto "con l'audio": serve il percorso senza funzione. Va verificato sul vostro Proxmox che l'editor dei mapping lo accetti (in alternativa si spuntano tutte le funzioni nel mapping). L'app considera occupata l'intera scheda (bus:device) in ogni caso.
**Opzioni**: A) percorso senza funzione; B) solo `.0`; C) indifferente, decide chi crea il mapping.
**Suggerimento**: A, coerente con le assegnazioni attuali.
**Risposta utente**: 2026-11-02 — come suggerito.

### D22 — Opzioni della nuova assegnazione
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: "Copiare le opzioni da un'assegnazione attiva": oggi 20 su 22 non hanno opzioni, 2 hanno `rombar=0`. Proposta: l'app scrive `hostpciN: mapping=<id>` piu' le opzioni dell'assegnazione piu' frequente **dello stesso modello** di scheda (oggi: nessuna opzione), mostrandole prima della conferma; se non ce n'e' nessuna dello stesso modello, nessuna opzione. Nessun campo libero per le opzioni.
**Perche' conta**: `rombar`, `pcie`, `x-vga` cambiano l'avvio della VM; un campo libero e' una superficie di errore. Le opzioni ammesse sarebbero una whitelist (`pcie`, `rombar`, `x-vga`, `romfile` escluso).
**Opzioni**: A) piu' frequente dello stesso modello; B) piu' frequente in tutto il cluster; C) sempre nessuna opzione.
**Suggerimento**: A.
**Risposta utente**: 2026-11-02 — come suggerito.

### D23 — Dove si assegna
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Proposta: nuova pagina **GPU** (sotto Risorse): per ogni nodo le schede Nvidia con stato (libera / assegnata a VM X / in conflitto / assegnazione diretta, sola lettura), sezione "Da correggere" in cima, azioni Assegna (scegli la VM tra quelle **dello stesso nodo**, spente) e Togli. Nel pannello della VM, sotto "Scheda video", un collegamento alla stessa azione. In panoramica le assegnazioni da correggere portano la condizione a "Quasi sereno" con la voce "N GPU da correggere".
**Opzioni**: A) pagina GPU + pannello VM + panoramica; B) solo pagina GPU; C) solo pannello VM.
**Suggerimento**: A.
**Risposta utente**: 2026-11-02 — come suggerito.

### D24 — Pulsante di spegnimento
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Se la VM e' accesa l'app mostra "Per cambiare la scheda video la VM deve essere spenta" con un pulsante. Che spegnimento?
**Perche' conta**: lo spegnimento pulito (ACPI) puo' non riuscire (guest bloccato, Windows con aggiornamenti); lo stop forzato perde i dati non salvati.
**Opzioni**: A) solo spegnimento pulito (conferma "attenzione"), con attesa e stato; se dopo 2 minuti e' ancora accesa lo dice e propone lo stop forzato con la conferma "pericolo" esistente; B) solo spegnimento pulito; C) stop forzato diretto.
**Suggerimento**: A.
**Risposta utente**: 2026-11-02 — come suggerito.

### D25 — Correzione dei conflitti
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Conflitti segnalati: (1) stessa scheda in piu' VM (diretta o mapping, anche VM spente); (2) mapping assegnato a piu' VM; (3) riferimento a una scheda che non esiste sul nodo della VM (es. VM spostata); (4) mapping che sul nodo della VM non ha voci. Se il conflitto coinvolge solo mapping, l'app propone "Togli da VM X" (VM spenta, conferma). Se coinvolge un'assegnazione diretta, l'app spiega cosa correggere in Proxmox ma non modifica (coerente con la tua scelta). Va bene?
**Suggerimento**: si'.
**Risposta utente**: 2026-11-02 — come suggerito.

### D26 — Prova sul cluster reale
**Stato**: RISPOSTA
**Bloccante**: si' (prima dello step 4)
**Domanda**: Per provare assegna/togli serve una VM di prova spenta e un mapping di prova su una scheda libera dello stesso nodo. Quali? (`ai-testing-diego` ha gia' una A100 in assegnazione diretta: l'app non la tocchera'.)
**Perche' conta**: e' la prima funzione che scrive la configurazione hardware; la prova reale va fatta su oggetti indicati da te, mai su VM di altri.
**Suggerimento**: una VM nuova vuota sullo stesso nodo di una scheda libera, e un mapping creato apposta.
**Risposta utente**: 2026-11-02 — come suggerito.

## Report v2 (2026-11-03, branch `feat/reports-v2`)

Gia' deciso dall'utente (2026-11-03): la pagina Report attuale si scarta e si rifa'; due famiglie di report, **tecnici** (dettaglio per chi gestisce il servizio) e **manageriali** (per direttore e consiglio di dipartimento: valutazione del servizio ed eventuali finanziamenti aggiuntivi); come formato manageriale basta il **PDF**; nessuna programmazione (si generano a richiesta); storico = il massimo che Proxmox conserva, alle sue risoluzioni.

### D27 — A chi appartengono le VM
**Stato**: APERTA
**Bloccante**: si' (per i KPI "gruppi serviti" e "risorse per gruppo")
**Domanda**: Per il consiglio conta "quanti gruppi di ricerca/progetti usano il servizio e quanto". Oggi l'app non sa a chi appartiene una VM. Da dove lo ricavo?
**Perche' conta**: e' probabilmente il KPI piu' forte per una richiesta di fondi; se la regola e' sbagliata i numeri sono sbagliati davanti al consiglio.
**Opzioni**: A) pool di Proxmox; B) tag di Proxmox (es. `gruppo-rossi`); C) prefisso del nome (es. `dinitto-vm-gpu-1` -> `dinitto`), con una tabella di correzione; D) campo note della VM; E) niente gruppi, solo totali.
**Suggerimento**: B se i tag sono usati o li potete mettere (esplicito, modificabile da Proxmox); altrimenti C come ripiego dichiarato nel report ("attribuzione dal nome della VM").
**Risposta utente**:

### D28 — Storico dell'inventario
**Stato**: APERTA
**Bloccante**: si' (architettura)
**Domanda**: Proxmox conserva lo storico dei **consumi** (CPU, RAM, rete, disco, storage) fino a un anno, ma non lo storico dell'**inventario**: quante VM c'erano sei mesi fa, quante GPU erano assegnate, chi le usava. Le VM cancellate perdono anche i consumi. Per mostrare la crescita del servizio serve salvare una fotografia giornaliera (pochi numeri) nel database SQLite dell'app. `CLAUDE.md` oggi vieta di reintrodurre un "collector": va eccezione?
**Perche' conta**: "siamo passati da 60 a 115 VM in un anno, le GPU sono tutte assegnate da marzo" e' l'argomento piu' convincente per i finanziamenti; senza fotografia si puo' mostrare solo lo stato di oggi piu' l'andamento dei consumi.
**Opzioni**: A) fotografia giornaliera minima (conteggi e assegnazioni per nodo/gruppo, niente IP, niente dati dei guest), tenuta 5 anni, file SQLite gia' esistente, scritta dal server una volta al giorno; B) niente fotografia, solo RRD (la crescita partira' da zero il giorno in cui la attiviamo comunque in futuro); C) fotografia solo quando si genera un report (buchi se non se ne generano).
**Suggerimento**: A: e' piccola (qualche KB al giorno), non tocca i guest e da' valore che cresce col tempo; va attivata presto perche' lo storico parte dal primo giorno.
**Risposta utente**:

### D29 — Copertina e intestazione del PDF
**Stato**: APERTA
**Bloccante**: no
**Domanda**: Il PDF manageriale ha una copertina. Cosa ci va: nome del servizio, struttura (es. "DEIB — Politecnico di Milano"), autore, logo?
**Perche' conta**: il logo va vendorizzato (file nel binario, nessun CDN); nome e struttura possono stare in `config.json` (campi validati).
**Opzioni**: A) testi in `config.json` + logo SVG/PNG che mi dai tu; B) solo testi; C) niente copertina, titolo in cima alla prima pagina.
**Suggerimento**: A se hai un logo utilizzabile, altrimenti B.
**Risposta utente**:

### D30 — Cosa compare nel PDF per il consiglio
**Stato**: APERTA
**Bloccante**: no
**Domanda**: Nel report manageriale mostro solo aggregati (per servizio, nodo, gruppo) o anche nomi di VM, IP e nomi di persona contenuti nei nomi delle VM?
**Perche' conta**: il PDF esce dal gruppo tecnico e puo' essere inoltrato; IP e nomi interni non servono a un consiglio.
**Opzioni**: A) solo aggregati e gruppi (niente IP, niente nomi di VM); B) anche le prime N VM per consumo; C) tutto.
**Suggerimento**: A per il manageriale; il tecnico mostra tutto ed e' marcato "uso interno".
**Risposta utente**:

### D31 — Generazione del PDF
**Stato**: APERTA
**Bloccante**: no
**Domanda**: Come generare il PDF?
**Perche' conta**: dipendenze, qualita' e peso.
**Opzioni**: A) vista "documento" A4 dedicata nella pagina e stampa del browser ("Salva come PDF"): nessuna libreria nuova, testo vettoriale e selezionabile, grafici ridisegnati ad alta risoluzione per la stampa, intestazioni/pie' di pagina via CSS `@page`; B) libreria vendorizzata (jsPDF + html2canvas, ~600 KB): un clic produce il file, ma le pagine diventano immagini; C) PDF generato dal server Go (libreria nuova e Chrome headless o simili): pesante.
**Suggerimento**: A. Unico compromesso: un passaggio in piu' (dialogo di stampa).
**Risposta utente**:

### D32 — Soglie dei report tecnici
**Stato**: APERTA
**Bloccante**: no
**Domanda**: Proposta: VM **inattiva** = accesa con CPU media < 2% e rete < 1 KB/s per tutto il periodo (minimo 7 giorni); VM **sovradimensionata** = RAM usata (p95) < 25% di quella assegnata o CPU p95 < 10% dei core assegnati, per almeno 14 giorni; nodo **saturo** = RAM p95 >= 85% o CPU p95 >= 85% (soglia gia' usata dal Bollettino). Vanno bene?
**Perche' conta**: sono gli elenchi "cosa recuperare" che giustificano o evitano acquisti.
**Suggerimento**: queste, modificabili nella pagina per il singolo report (non salvate).
**Risposta utente**:

### D33 — Lingua
**Stato**: APERTA
**Bloccante**: no
**Domanda**: Report solo in italiano o anche in inglese (es. per valutazioni esterne)?
**Suggerimento**: solo italiano ora; testi raccolti in un punto per aggiungere l'inglese dopo.
**Risposta utente**:

### D34 — Utilizzo delle GPU nei guest
**Stato**: APERTA
**Bloccante**: no
**Domanda**: Proxmox sa quale VM ha quale GPU, non quanto la usa. Misurare l'uso reale (es. `nvidia-smi` via guest agent) richiede un nuovo helper nei guest e un collector. Fuori scope per ora?
**Suggerimento**: fuori scope; il report mostra GPU assegnate/libere (e, con D28 A, da quanto tempo), che e' gia' l'indicatore di domanda.
**Risposta utente**:

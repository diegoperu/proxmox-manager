exct# DOMANDE — branch `experiment/ui-redesign`

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
**Stato**: RISPOSTA
**Bloccante**: si' (per i KPI "gruppi serviti" e "risorse per gruppo")
**Domanda**: Per il consiglio conta "quanti gruppi di ricerca/progetti usano il servizio e quanto". Oggi l'app non sa a chi appartiene una VM. Da dove lo ricavo?
**Perche' conta**: e' probabilmente il KPI piu' forte per una richiesta di fondi; se la regola e' sbagliata i numeri sono sbagliati davanti al consiglio.
**Opzioni**: A) pool di Proxmox; B) tag di Proxmox (es. `gruppo-rossi`); C) prefisso del nome (es. `dinitto-vm-gpu-1` -> `dinitto`), con una tabella di correzione; D) campo note della VM; E) niente gruppi, solo totali.
**Suggerimento**: B se i tag sono usati o li potete mettere (esplicito, modificabile da Proxmox); altrimenti C come ripiego dichiarato nel report ("attribuzione dal nome della VM").
**Risposta utente**: 2026-11-03 — dal nome della VM (convenzione interna): il gruppo e' il prefisso prima del primo `-` (es. `dinitto-vm-gpu-1` -> `dinitto`); il report lo dichiara.

### D28 — Storico dell'inventario
**Stato**: RISPOSTA
**Bloccante**: si' (architettura)
**Domanda**: Proxmox conserva lo storico dei **consumi** (CPU, RAM, rete, disco, storage) fino a un anno, ma non lo storico dell'**inventario**: quante VM c'erano sei mesi fa, quante GPU erano assegnate, chi le usava. Le VM cancellate perdono anche i consumi. Per mostrare la crescita del servizio serve salvare una fotografia giornaliera (pochi numeri) nel database SQLite dell'app. `CLAUDE.md` oggi vieta di reintrodurre un "collector": va eccezione?
**Perche' conta**: "siamo passati da 60 a 115 VM in un anno, le GPU sono tutte assegnate da marzo" e' l'argomento piu' convincente per i finanziamenti; senza fotografia si puo' mostrare solo lo stato di oggi piu' l'andamento dei consumi.
**Opzioni**: A) fotografia giornaliera minima (conteggi e assegnazioni per nodo/gruppo, niente IP, niente dati dei guest), tenuta 5 anni, file SQLite gia' esistente, scritta dal server una volta al giorno; B) niente fotografia, solo RRD (la crescita partira' da zero il giorno in cui la attiviamo comunque in futuro); C) fotografia solo quando si genera un report (buchi se non se ne generano).
**Suggerimento**: A: e' piccola (qualche KB al giorno), non tocca i guest e da' valore che cresce col tempo; va attivata presto perche' lo storico parte dal primo giorno.
**Risposta utente**: 2026-11-03 — ok al collector (opzione A): fotografia giornaliera minima nel SQLite dell'app; `CLAUDE.md` aggiornato (eccezione alla regola "niente collector").

### D29 — Copertina e intestazione del PDF
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Il PDF manageriale ha una copertina. Cosa ci va: nome del servizio, struttura (es. "DEIB — Politecnico di Milano"), autore, logo?
**Perche' conta**: il logo va vendorizzato (file nel binario, nessun CDN); nome e struttura possono stare in `config.json` (campi validati).
**Opzioni**: A) testi in `config.json` + logo SVG/PNG che mi dai tu; B) solo testi; C) niente copertina, titolo in cima alla prima pagina.
**Suggerimento**: A se hai un logo utilizzabile, altrimenti B.
**Risposta utente**: 2026-11-03 — dati e loghi dal sito del progetto https://datacloud.polimi.it/ : servizio **DataCloud**, laboratorio interdipartimentale del **Politecnico di Milano** (DEIB, DABC, DASTU, Matematica), servizi cloud IaaS per il personale di ricerca (data science, AI, machine learning, simulazione numerica); contatto datacloud-lab@polimi.it; Via Giuseppe Ponzio 34/5, Edificio 20, 20133 Milano. Loghi scaricati (non ancora nel repo): `DataCloud-01-1.png` (2191x1246, logo blu Polimi+DataCloud, sha256 f9bd4f62de66f6597b18afbba5be2aac89c59137931429ab7109a407aa44d9c9) e `Bandiera_Bianco.png` (1425x622, bandiera Polimi bianca per fondi scuri, sha256 ac038910d3f19b445d05f9acdd3a162e1114dc9f818c04d996515e04a025a887).

### D30 — Cosa compare nel PDF per il consiglio
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Nel report manageriale mostro solo aggregati (per servizio, nodo, gruppo) o anche nomi di VM, IP e nomi di persona contenuti nei nomi delle VM?
**Perche' conta**: il PDF esce dal gruppo tecnico e puo' essere inoltrato; IP e nomi interni non servono a un consiglio.
**Opzioni**: A) solo aggregati e gruppi (niente IP, niente nomi di VM); B) anche le prime N VM per consumo; C) tutto.
**Suggerimento**: A per il manageriale; il tecnico mostra tutto ed e' marcato "uso interno".
**Risposta utente**: 2026-11-03 — come suggerito: manageriale solo aggregati e gruppi (niente IP, niente nomi di VM); tecnico completo, marcato "uso interno".

### D31 — Generazione del PDF
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Come generare il PDF?
**Perche' conta**: dipendenze, qualita' e peso.
**Opzioni**: A) vista "documento" A4 dedicata nella pagina e stampa del browser ("Salva come PDF"): nessuna libreria nuova, testo vettoriale e selezionabile, grafici ridisegnati ad alta risoluzione per la stampa, intestazioni/pie' di pagina via CSS `@page`; B) libreria vendorizzata (jsPDF + html2canvas, ~600 KB): un clic produce il file, ma le pagine diventano immagini; C) PDF generato dal server Go (libreria nuova e Chrome headless o simili): pesante.
**Suggerimento**: A. Unico compromesso: un passaggio in piu' (dialogo di stampa).
**Risposta utente**: 2026-11-03 — si': vista documento A4 + stampa del browser.

### D32 — Soglie dei report tecnici
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Proposta: VM **inattiva** = accesa con CPU media < 2% e rete < 1 KB/s per tutto il periodo (minimo 7 giorni); VM **sovradimensionata** = RAM usata (p95) < 25% di quella assegnata o CPU p95 < 10% dei core assegnati, per almeno 14 giorni; nodo **saturo** = RAM p95 >= 85% o CPU p95 >= 85% (soglia gia' usata dal Bollettino). Vanno bene?
**Perche' conta**: sono gli elenchi "cosa recuperare" che giustificano o evitano acquisti.
**Suggerimento**: queste, modificabili nella pagina per il singolo report (non salvate).
**Risposta utente**: 2026-11-03 — come suggerito.

### D33 — Lingua
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Report solo in italiano o anche in inglese (es. per valutazioni esterne)?
**Suggerimento**: solo italiano ora; testi raccolti in un punto per aggiungere l'inglese dopo.
**Risposta utente**: 2026-11-03 — il manageriale e' **bilingue**: prima tutto in italiano, poi lo stesso documento replicato uguale in inglese (stesso PDF). Il tecnico resta in italiano.

### D34 — Utilizzo delle GPU nei guest
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Proxmox sa quale VM ha quale GPU, non quanto la usa. Misurare l'uso reale (es. `nvidia-smi` via guest agent) richiede un nuovo helper nei guest e un collector. Fuori scope per ora?
**Suggerimento**: fuori scope; il report mostra GPU assegnate/libere (e, con D28 A, da quanto tempo), che e' gia' l'indicatore di domanda.
**Risposta utente**: 2026-11-03 — fuori scope per ora; da riprendere quando si aggiunge l'helper `nvidia-smi` (segnato tra i lavori da fare in `CLAUDE.md`).

## Gruppi dai tag di Proxmox (ROADMAP 1, branch `feat/group-tags`, 2026-11-04)

Contesto: oggi il gruppo e' il prefisso del nome VM (D27) e i report contano "gruppi" che non lo sono (`ubuntu`, `poli`, `blm`, `carlo`...). I tag di Proxmox arrivano gia' in `/cluster/resources` (campo `tags`, separati da `;`): leggerli non costa chiamate in piu'. Oggi sulle VM ci sono solo tag di sistema (`cloudinit`, `gpu`, `24.04`...). Proxmox ammette nei tag `[a-z0-9_][a-z0-9_.+-]*`; il datacenter puo' limitare chi crea tag nuovi (`user-tag-access`, `registered-tags`): da leggere in sola lettura prima del codice.

### D35 — Formato del tag di gruppo
**Stato**: RISPOSTA
**Bloccante**: si'
**Domanda**: Come si scrive il gruppo nel tag, per distinguerlo dai tag di sistema?
**Perche' conta**: senza un prefisso riconoscibile `ubuntu` o `gpu` diventerebbero gruppi, cioe' lo stesso errore di oggi. Il formato e' un contratto: cambiarlo dopo significa rietichettare tutto.
**Opzioni**: A) `gruppo-<nome>` (es. `gruppo-rossi`): leggibile anche nella UI di Proxmox; B) `g-<nome>`: corto, meno chiaro a chi non conosce la convenzione; C) `<nome>` nudo: ambiguo con i tag di sistema.
**Suggerimento**: A. `<nome>` validato `[a-z0-9]{1,32}` come oggi (D27), quindi tag lungo al massimo 39 caratteri, compatibile con Proxmox.
**Risposta utente**: 2026-11-04 — A: `gruppo-<nome>`

### D36 — VM senza tag di gruppo
**Stato**: RISPOSTA
**Bloccante**: si'
**Domanda**: A quale gruppo va una VM senza tag `gruppo-*`?
**Perche' conta**: finche' le 107 VM non sono etichettate i report dipendono da questa regola; e' anche la regola per le VM create fuori dall'app (da Proxmox).
**Opzioni**: A) ripiego sul prefisso del nome, come oggi (restano i gruppi finti finche' non si etichetta); B) "senza gruppo" (report onesti ma vuoti all'inizio); C) ripiego sul prefisso **solo se** e' un gruppo che esiste gia' come tag su almeno un'altra VM, altrimenti "senza gruppo".
**Suggerimento**: C: `rossi-vm2` senza tag finisce in `rossi` se `gruppo-rossi` esiste, `ubuntu-test` finisce in "senza gruppo". Nei report tecnici si vede l'origine (tag / nome / nessuno) per riga.
**Risposta utente**: 2026-11-04 — C: ripiego sul prefisso del nome solo se il gruppo esiste gia' come tag, altrimenti "senza gruppo"

### D37 — Piu' tag di gruppo sulla stessa VM
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Se una VM ha due tag `gruppo-*` (errore o VM condivisa), come la conto?
**Opzioni**: A) gruppo "da verificare" + segnalazione nel report tecnico; B) il primo in ordine alfabetico; C) ripartita a meta' tra i gruppi.
**Suggerimento**: A: nessuna scelta silenziosa; l'app non scrive mai due tag di gruppo (sostituisce).
**Risposta utente**: 2026-11-04 — C: ripartita in parti uguali tra i gruppi (diverso dal suggerimento; dettagli in D42)

### D38 — Elenco dei gruppi validi
**Stato**: RISPOSTA
**Bloccante**: si'
**Domanda**: Da dove viene l'elenco dei gruppi proposto nella creazione VM?
**Perche' conta**: un refuso (`gruppo-rosi`) crea un gruppo finto nei report del consiglio.
**Opzioni**: A) dai tag gia' presenti sulle VM; un gruppo nuovo si crea scrivendolo, con conferma esplicita "nuovo gruppo"; B) lista in `config.json` gestita da Impostazioni (solo quelli ammessi); C) tag registrati di Proxmox (`registered-tags` del datacenter).
**Suggerimento**: A: nessuna configurazione in piu', e la conferma "stai creando il gruppo X" ferma i refusi. B se volete un controllo rigido.
**Risposta utente**: 2026-11-04 — A: elenco dai tag presenti, gruppo nuovo con conferma esplicita

### D39 — Etichettare le VM esistenti
**Stato**: RISPOSTA
**Bloccante**: si'
**Domanda**: Come si mettono i tag alle ~107 VM gia' presenti?
**Perche' conta**: e' una scrittura in blocco sulla configurazione delle VM (solo il campo `tags`, a VM accesa va bene: non richiede riavvio).
**Opzioni**: A) nuova azione in Batch "Imposta gruppo": selezioni le VM (anche filtrando per prefisso del nome), scegli il gruppo, conferma; B) pagina "Gruppi" con una proposta automatica per ogni VM (dal prefisso del nome) da accettare o correggere riga per riga e poi applicare; C) a mano in Proxmox / script esterno.
**Suggerimento**: B: con 107 VM la proposta dal nome fa il grosso del lavoro e la revisione riga per riga toglie i finti (`ubuntu`). La scrittura preserva gli altri tag e usa il `digest` (come la pagina GPU). A si puo' aggiungere dopo.
**Risposta utente**: 2026-11-04 — B: pagina "Gruppi" con proposta dal nome, revisione riga per riga

### D40 — Cambiare il gruppo di una VM
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Oltre alla creazione, si puo' cambiare o togliere il gruppo dal pannello VM?
**Suggerimento**: si': riga "Gruppo: rossi (tag) · Cambia" nel pannello VM, con conferma; stessa scrittura di D39.
**Risposta utente**: 2026-11-04 — si': "Cambia" nel pannello VM, con conferma

### D41 — Storico delle fotografie
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Le fotografie giornaliere dal 2026-11-03 usano il gruppo dal nome. Cosa ne facciamo dopo il passaggio ai tag?
**Opzioni**: A) restano come sono; i report mostrano la data del cambio di regola e non confrontano gruppi prima/dopo; B) si ricalcolano (impossibile in modo esatto: le fotografie non contengono i nomi delle VM, per scelta D28).
**Suggerimento**: A, con una colonna `group_source` nelle nuove fotografie per sapere quale regola era in vigore.
**Risposta utente**: 2026-11-04 — A: fotografie vecchie invariate, data del cambio dichiarata, `group_source` nelle nuove

### D42 — Come si ripartisce una VM condivisa (segue D37 C)
**Stato**: RISPOSTA
**Bloccante**: si'
**Domanda**: Con D37 C una VM con tag `gruppo-a` e `gruppo-b` vale meta' per ciascuno. Cosa si divide esattamente?
**Perche' conta**: la fotografia giornaliera oggi salva numeri interi (VM, core, RAM, disco, schede GPU per gruppo): va cambiato lo schema (`internal/history`). E i totali dei report devono tornare: la somma dei gruppi deve dare il totale del cluster.
**Opzioni**: A) tutto in parti uguali, anche il conteggio (2 gruppi = 0,5 VM ciascuno; 3 gruppi = 0,33): i totali tornano sempre, ma compaiono "VM" con decimali; B) risorse (RAM, disco, core, GPU) in parti uguali, conteggio VM pieno per ogni gruppo (la VM compare in entrambi) + numero di VM condivise accanto: leggibile, ma la somma delle VM per gruppo supera il totale (il totale si prende dal cluster, non dalla somma); C) come B, ma la GPU (scheda intera, non divisibile) va a un "condiviso" invece che a meta'.
**Suggerimento**: B. Schema: RAM e disco restano in byte (divisione intera, il resto al primo gruppo in ordine alfabetico per non perdere byte), core e schede in millesimi (colonne nuove), colonna `shared_vms`; le righe vecchie si leggono come prima. Il report tecnico mostra la VM con tutti i suoi gruppi; il manageriale scrive "di cui N condivise".
**Risposta utente**: 2026-11-04 — B: risorse in parti uguali (byte con resto al primo gruppo alfabetico, core e schede in millesimi), conteggio VM pieno per gruppo + `shared_vms`; totale VM dal cluster.

**Verifica in sola lettura (2026-11-04, entrambi i cluster):** `user-tag-access` e `registered-tags` non impostati = default Proxmox `free` (chi ha `VM.Config.Options` puo' creare tag nuovi); il token ha `VM.Config.Options` su `/vms` (e `Sys.Modify`): le scritture dei tag sono permesse. Nessun tag `gruppo-*` presente (CALCOLO 115 VM/25 tag distinti, HOSTING 29/7). Tag esistenti tutti di sistema (`cloudinit`, `ubuntu-template`, `24.04`, `gpu`, `a100`...), uno maiuscolo (`LTSC`): la lettura deve confrontare `gruppo-` senza distinzione di maiuscole e preservare gli altri tag cosi' come sono. Attenzione: il clone copia i tag del template/VM sorgente (un template non deve mai avere un tag di gruppo).

## Bilanciamento del cluster (ROADMAP 3) — domande del 2026-11-04

**Misure in sola lettura su CALCOLO (2026-11-04):**
- 11 nodi molto diversi: RAM da 252 GB a 2 TB, da 40 a 256 core. Il pareggio va fatto in **percentuale**, non in GB.
- **Un nodo supera gia' il limite di RAM assegnata:** `datacloud-node-10` ha 1216 GB assegnati alle VM su 1007 GB fisici (tutte accese, RAM usata realmente 960 GB). Altri nodi sono vicini (node-5: 1281 su 2004; node-11: 1200 su 1511). Il blocco duro "mai oltre fisica - riserva" e' quindi **gia' violato** all'inizio.
- Molte VM hanno RAM assegnata molto piu' alta di quella usata (es. 128 GB assegnati, 2 GB usati).
- **Nessuna risorsa HA, nessun `lock`.** 22 VM hanno GPU (`hostpci`), 1 ha un ISO locale. Tutti gli storage delle VM (ssd, hdd, backup) sono condivisi (Ceph/PBS).
- L'API di Proxmox `GET /nodes/{n}/qemu/{id}/migrate` dice per ogni VM i **dischi locali**, le **risorse locali** (es. `hostpci0`) e i **nodi consentiti** (`allowed_nodes`); `not_allowed_nodes` non da' i motivi (oggetti vuoti), quindi i motivi li ricava l'app.

### D43 — Nodi che partono gia' oltre il limite di RAM
**Stato**: RISPOSTA
**Bloccante**: si'
**Domanda**: Come tratta il piano un nodo gia' oltre RAM fisica - riserva (oggi `node-10`)?
**Perche' conta**: la regola approvata dice "mai superare"; se vale anche per i nodi di partenza, nessun piano sarebbe valido finche' quel nodo esiste.
**Opzioni**: A) il limite e' un blocco duro **solo per i nodi di destinazione**: una migrazione non puo' portare un nodo oltre il limite ne' peggiorare uno che lo supera gia'; i nodi che lo superano diventano le **sorgenti prioritarie** e il piano li segnala in rosso come "da alleggerire"; B) il piano non si genera finche' un nodo viola il limite (si risolve prima a mano); C) il limite vale solo come avviso.
**Suggerimento**: A: tiene il blocco duro dove protegge davvero (destinazioni) e rende utile il piano proprio sul caso che c'e' oggi.
**Risposta utente**: 2026-11-04 — A: blocco duro solo sui nodi di destinazione; i nodi gia' oltre il limite sono sorgenti prioritarie, segnalati in rosso

### D44 — Misura della RAM per il pareggio
**Stato**: RISPOSTA
**Bloccante**: si'
**Domanda**: Il pareggio si calcola sulla RAM **assegnata** alle VM o sulla RAM **realmente usata**?
**Perche' conta**: assegnata e usata divergono molto (128 GB assegnati, 2 GB usati). Sull'assegnata il piano e' stabile e prudente (nessun overbooking); sull'usata sfrutta meglio il cluster ma una VM puo' crescere e saturare un nodo.
**Opzioni**: A) pareggio sulla **% di RAM assegnata** sul totale assegnabile (fisica - riserva), come gia' deciso; l'uso reale (p95 delle 4 settimane) resta come **avviso** sul nodo di destinazione ("attenzione RAM reale"); B) pareggio sull'uso reale (p95), con l'assegnata solo come tetto; C) misura mista (media pesata).
**Suggerimento**: A, coerente con la regola "niente overbooking" e con le decisioni del 2026-11-03.
**Risposta utente**: 2026-11-04 — A: pareggio sulla % di RAM assegnata su (fisica - riserva); uso reale come avviso

### D45 — VM spente: contano nel carico?
**Stato**: RISPOSTA
**Bloccante**: si'
**Domanda**: Le VM spente occupano RAM assegnata nel calcolo del nodo?
**Perche' conta**: se non contano, un nodo sembra libero ma all'avvio di quelle VM puo' superare il limite; se contano, il carico e' sovrastimato.
**Opzioni**: A) contano per intero (potrebbero accendersi in qualsiasi momento); B) non contano ma il piano le elenca e avvisa; C) a scelta con un interruttore nella pagina (default: contano).
**Suggerimento**: C con default A: prudente, e l'utente puo' vedere cosa cambia.
**Risposta utente**: 2026-11-04 — C: interruttore nella pagina, default: le VM spente contano

### D46 — Container LXC
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: I container LXC rientrano nel piano?
**Perche' conta**: Proxmox non migra gli LXC a caldo (serve un riavvio breve del container): "a caldo" per un LXC non esiste.
**Opzioni**: A) solo VM QEMU; gli LXC contano nel carico ma sono "non spostabili: richiede riavvio"; B) LXC spostabili solo quando l'utente sceglie la migrazione "a freddo/riavvio" per il piano; C) LXC sempre spostabili con riavvio, con avviso.
**Suggerimento**: B (a CALCOLO ci sono pochi LXC, 1 di 107).
**Risposta utente**: 2026-11-04 — B: LXC spostabili solo con migrazione a freddo/riavvio scelta per il piano

### D47 — Limiti del piano e ordine dei passi
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Quanto grande puo' essere un piano e come si ordinano i passi?
**Perche' conta**: ogni migrazione a caldo carica rete e storage Ceph; una VM da 128 GB si sposta piu' lentamente di una da 4 GB. Un piano lungo dopo un po' non e' piu' valido (lo stato cambia).
**Opzioni**: A) massimo **10 passi** per piano (modificabile), a parita' di risultato si preferisce spostare **meno GB**; si fa un passo alla volta con ricalcolo prima di ciascuno e conferma; B) piano completo senza tetto con tutti i passi; C) tetto in GB totali spostati (es. 500 GB) invece che in numero di passi.
**Suggerimento**: A.
**Risposta utente**: 2026-11-04 — A: massimo 10 passi (modificabile), a parita' meno GB spostati, un passo alla volta con ricalcolo e conferma

### D48 — Dove vive la funzione
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Dove compare il bilanciamento nell'interfaccia?
**Perche' conta**: UX e navigazione.
**Opzioni**: A) nuova pagina "Bilanciamento" nel menu (sotto Operazioni, accanto a Cluster): stato dei nodi prima/dopo, piano, simulazione "e se spengo il nodo X", esecuzione passo per passo; B) sezione dentro la pagina Cluster.
**Suggerimento**: A: la pagina Cluster contiene gia' le azioni distruttive sui nodi e il piano ha una interfaccia propria.
**Risposta utente**: 2026-11-04 — A: nuova pagina Bilanciamento sotto Operazioni

### D49 — Peso dei gruppi
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Il piano deve tenere conto dei gruppi (es. non concentrare tutte le VM di uno stesso gruppo su un nodo)?
**Perche' conta**: un nodo che si ferma porterebbe giu' tutte le VM di un gruppo; ma complica l'algoritmo.
**Opzioni**: A) per ora no (rinviato); B) preferenza morbida: a parita' di risultato si preferisce distribuire le VM dello stesso gruppo; C) vincolo: al massimo N VM dello stesso gruppo per nodo.
**Suggerimento**: A, da riprendere dopo la prima versione.
**Risposta utente**: 2026-11-04 — B: preferenza morbida, a parita' di risultato si distribuiscono le VM dello stesso gruppo

### D50 — Migrazione a freddo delle VM QEMU accese: dove si sceglie
**Stato**: RISPOSTA
**Bloccante**: si' (per la migrazione a freddo)
**Domanda**: L'utente vuole poter spostare a freddo anche le VM QEMU accese (spegni, sposta, riaccendi). Dove si sceglie a caldo o a freddo?
**Perche' conta**: a freddo la VM e' ferma per tutta la migrazione (minuti); a caldo resta accesa ma carica rete e storage. Oggi l'interruttore "Includi i container (riavvio breve)" vale solo per gli LXC (D46).
**Opzioni**: A) un solo interruttore per il piano, "Migrazione a freddo", che vale per VM e container (decisione del 2026-11-03: "a caldo o a freddo a scelta per ogni piano"); B) due interruttori separati, uno per i container (riavvio breve) e uno per le VM QEMU (spegnimento); C) scelta nel passo: nella conferma di ogni passo "a caldo" (predefinito) o "a freddo".
**Suggerimento**: B: un container si riavvia in pochi secondi, una VM spenta resta ferma per minuti; tenerli separati evita di spegnere VM quando si voleva solo includere i container. La conferma del passo dice sempre in parole cosa succedera'.
**Risposta utente**: 2026-11-04 — Posso scegliere con una spunta quali migrare a caldo e quali a freddo (container a freddo senza possibilitá di scelta)
**Interpretazione (dichiarata)**: spunta "a freddo" per ogni VM QEMU accesa nella tabella del piano (predefinito: a caldo), la scelta vale al momento dell'esecuzione del passo e non cambia il piano; i container accesi si spostano sempre con riavvio, senza spunta; l'interruttore "Includi i container" (D46) resta com'e' (decide solo se i container entrano nel piano)

### D51 — Migrazione a freddo: se la VM non si spegne
**Stato**: RISPOSTA
**Bloccante**: si' (per la migrazione a freddo)
**Domanda**: Lo spegnimento ordinato (ACPI o guest agent) puo' non completarsi. Cosa si fa dopo l'attesa?
**Perche' conta**: uno stop forzato e' come staccare la corrente: rischio di dati persi nel guest.
**Opzioni**: A) attesa di 3 minuti, poi si rinuncia: la VM resta accesa sul suo nodo e nulla si sposta; B) attesa di 3 minuti, poi la pagina chiede conferma (livello pericolo) per uno stop forzato, come nella pagina GPU (D24); C) stop forzato automatico dopo l'attesa.
**Suggerimento**: B, coerente con D24; senza conferma entro qualche minuto la procedura si annulla e la VM resta accesa.
**Risposta utente**: 2026-11-04 — B

### D52 — Migrazione a freddo: errori a meta'
**Stato**: RISPOSTA
**Bloccante**: si' (per la migrazione a freddo)
**Domanda**: Se la migrazione fallisce quando la VM e' gia' spenta, o se la VM non riparte sul nodo di arrivo, cosa si fa?
**Perche' conta**: una VM lasciata spenta per errore e' un disservizio per chi la usa.
**Opzioni**: A) migrazione fallita = la VM viene riaccesa sul nodo d'origine e la pagina lo dice; avvio fallito sul nodo di arrivo = un solo nuovo tentativo, poi la VM resta spenta li' con un messaggio chiaro (niente ritorno automatico al nodo d'origine); B) in entrambi i casi la VM resta spenta e la pagina lo dice, l'utente decide; C) come A, ma se l'avvio fallisce sul nodo d'arrivo la si riporta al nodo d'origine e la si riaccende.
**Suggerimento**: A: riporta subito in servizio la VM nel caso piu' comune (migrazione rifiutata) senza nascondere un problema del nodo di arrivo dietro una seconda migrazione automatica.
**Risposta utente**: 2026-11-04 — A

### D53 — Migrazione a freddo: chi guida la procedura
**Stato**: RISPOSTA
**Bloccante**: si' (per la migrazione a freddo)
**Domanda**: La sequenza spegni -> migra -> riaccendi la guida il server o la pagina?
**Perche' conta**: la procedura dura minuti; se si interrompe a meta' la VM puo' restare spenta.
**Opzioni**: A) il server: una richiesta avvia la procedura (con lo stesso ricontrollo del piano di oggi), il server la porta avanti da solo e la pagina ne legge lo stato (spegnimento, migrazione, avvio, finita/errore); una procedura alla volta per cluster, stato in memoria; se il server si riavvia a meta' la procedura si ferma e la VM puo' restare spenta (lo dice il log del server); B) la pagina: chiama in sequenza le azioni gia' esistenti (shutdown, migrate, start); se si chiude la scheda a meta' la VM resta spenta.
**Suggerimento**: A: non dipende dalla scheda del browser aperta e tiene in un solo punto i controlli (lucchetto per cluster, ricontrollo del piano, conferma dello stop forzato di D51 come domanda che la procedura aspetta).
**Risposta utente**: 2026-11-04 — A

### D54 — Spostare una VM scelta (prova reale a freddo sulla VM 174)
**Stato**: RISPOSTA
**Bloccante**: si' (per provare la migrazione a freddo su una VM scelta)
**Domanda**: L'utente ha indicato la VM 174 (`santoro-vm-1`, 32 GB, `datacloud-node-6`) per la prova reale a freddo, ma il server esegue solo il primo passo del piano e la 174 non c'e' (neanche svuotando `node-6`: prima vengono 104 e, dopo la migrazione in corso, 109). Come si sposta una VM scelta?
**Perche' conta**: oggi il server non esegue niente che il piano non proponga: e' la garanzia che le regole (RAM, VM bloccate, nodi consentiti) valgano anche in scrittura.
**Opzioni**: A) parametro del piano "sposta solo queste VM" (es. `only=qemu:174`): tutte le altre restano ferme, il piano e il ricontrollo del server restano identici, la destinazione la sceglie il piano; se spostarla non migliora il pareggio il piano non propone nulla; B) pulsante "Sposta…" su una singola VM con nodo di arrivo scelto dall'utente: il server controlla i vincoli duri (RAM della destinazione, blocchi, nodi consentiti) ma non l'obiettivo del pareggio; C) nessuna aggiunta: la prima prova a freddo reale si fa su un passo del piano spuntando "a freddo".
**Suggerimento**: A: aggiunta piccola, nessuna nuova via di scrittura, utile anche dopo (es. "sposta solo le VM del gruppo X").
**Risposta utente**: 2026-11-04 — A

### D55 — Compatibilita' della CPU nella migrazione a caldo (incidente VM 109)
**Stato**: RISPOSTA
**Bloccante**: si' (nessun rilascio ne' altro passo reale prima della risposta)
**Domanda**: La migrazione a caldo della VM 109 (`cpu: host`) da `datacloud-node-10` (AMD EPYC 7713) a `datacloud-node-6` (Intel Xeon E5-2640 v4) ha trasferito tutta la memoria e poi e' fallita alla ripresa (`kvm: Restoring registers after init: Failed to set special registers: Invalid argument`): la VM e' rimasta SPENTA sul nodo di arrivo. Proxmox non lo impedisce e il precheck non lo segnala. Il cluster mescola 3 famiglie AMD EPYC e 5 Intel Xeon. Come deve tenerne conto il piano?
**Perche' conta**: con `cpu: host` (o `max`) la CPU vista dal guest e' quella del nodo: a caldo si puo' andare solo verso un nodo con la stessa CPU. A freddo (VM spenta e riaccesa) non c'e' il problema.
**Opzioni**: A) vincolo duro: una VM accesa con `cpu: host|max` si sposta a caldo solo verso nodi con lo **stesso modello di CPU** (letto da `/nodes/{n}/status`); verso gli altri solo a freddo. La spunta "a freddo" entra nel calcolo del piano (`cold_vms=...`), cosi' il piano propone destinazioni valide per il modo scelto. Le VM con modelli generici (`x86-64-v2-AES`, `kvm64`...) restano libere. B) come A ma "stessa marca" (AMD con AMD, Intel con Intel) invece dello stesso modello: piu' possibilita', rischio residuo tra generazioni diverse; C) tutte le VM accese con `cpu: host|max` solo a freddo.
**Suggerimento**: A: e' l'unica regola sicura con `cpu: host`; lo stesso modello esiste gia' a coppie/gruppi nel cluster (es. node-2/3/4 E5-2698 v4, node-1/8 Silver 4114).
**Risposta utente**: 2026-11-04 — A
**Modifica dell'utente (2026-11-04)**: il vincolo e' sul **produttore** (AMD/Intel), non sul modello (di fatto opzione B): l'utente ha migrato a mano a caldo la VM 174 (`cpu: host`) da `node-6` (Intel Xeon E5-2640 v4) a `node-1` (Intel Xeon Silver 4114) e ha funzionato. Rischio residuo segnalato all'utente: quella prova va da una generazione piu' vecchia (Broadwell) a una piu' nuova (Skylake); il verso opposto (es. `node-11` Gold 5120 -> `node-2` E5-2698 v4, o EPYC 7713 Milan -> EPYC 7302 Rome) toglie istruzioni al guest e non e' stato provato. **Provato poi (2026-11-04, VM 174):** Silver 4114 -> E5-2640 v4 a caldo ok; resta non provato solo tra generazioni AMD.

## ROADMAP 3b — Nodo consigliato alla creazione VM + GPU nel deploy (2026-11-04)

Fatti letti in sola lettura su CALCOLO: 8 template, tutti su `datacloud-node-1` tranne il Windows 130 (`node-11`); i template Linux hanno `cpu: host`, `q35`, dischi su `datacloud-ssd` (Ceph, condiviso). Schede: 27, 22 assegnate, 5 libere (A100 40GB su node-5 `HULK`; RTX PRO 6000 Blackwell e 2 H100L 94GB su node-10; A2/A16 su node-11), tutte con un mapping. Oggi `POST /api/provision` e' una richiesta sincrona che clona sul nodo del template.

### D56 — Clone direttamente sul nodo scelto: prova reale
**Stato**: RISPOSTA
**Bloccante**: si' (decide il flusso di creazione)
**Domanda**: Posso provare il clone con `target` (Proxmox lo ammette solo se il template e' su storage condiviso, come i vostri) creando una VM di prova e poi cancellandola?
**Perche' conta**: se funziona, la VM nasce gia' sul nodo giusto (niente migrazione, GPU assegnata subito); se no, si clona sul nodo del template e si migra a freddo prima di assegnare la GPU (decisione del 2026-11-03).
**Opzioni**: A) prova: clone completo del template 1003 (Ubuntu 24.04) come VM `test-clone-target` su un nodo diverso da `node-1` scelto da te, verifica (nodo, dischi su `datacloud-ssd`, config), poi cancellazione; la VM non viene mai avviata; B) niente prova: si usa subito clone + migrazione; C) prova su un altro template/nodo indicato da te.
**Suggerimento**: A, con nodo di arrivo `datacloud-node-6` (nessuna GPU, nessun impatto).
**Risposta utente**: 2026-11-04 — A
**Esito della prova (2026-11-04)**: clone completo del template 1003 (`node-1`) con `target=datacloud-node-6`: OK in 9 s, VM 108 `test-clone-target` creata direttamente su `node-6`, dischi (`virtio0`, `efidisk0`, cloud-init) su `datacloud-ssd` (Ceph condiviso), `cpu: host`; mai avviata; cancellata con `purge=1&destroy-unreferenced-disks=1` (OK, nessun volume rimasto). Quindi: **si clona direttamente sul nodo scelto**, niente migrazione. Nota: per DELETE i parametri vanno nell'indirizzo, non nel corpo (con il corpo Proxmox risponde 501).

### D57 — Chi guida la creazione
**Stato**: RISPOSTA
**Bloccante**: si'
**Domanda**: La creazione diventa una sequenza lunga (clone, risorse, disco, cloud-init, eventuale migrazione, GPU, avvio, attesa dell'agent, utente aggiuntivo). Chi la porta avanti?
**Perche' conta**: oggi e' una sola richiesta HTTP che resta aperta fino a 10 minuti; se la pagina si chiude a meta' non si sa cosa e' stato fatto.
**Opzioni**: A) il server, come la migrazione a freddo: una richiesta avvia la procedura, la pagina mostra le fasi (con esito per ognuna) e si puo' chiudere; procedura in memoria, piu' creazioni in parallelo ammesse (una per scheda GPU); B) resta una richiesta sincrona, allungata; C) la pagina chiama in sequenza le azioni esistenti.
**Suggerimento**: A: stessa struttura gia' provata sulla migrazione a freddo, errori a meta' gestiti in un solo punto.
**Risposta utente**: 2026-11-04 — A

### D58 — Nodo scelto a mano oltre il limite di RAM
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: L'utente puo' cambiare il nodo consigliato. Se sceglie un nodo dove la VM supera la RAM assegnabile (fisica - riserva), cosa succede?
**Perche' conta**: nel bilanciamento il limite e' un blocco duro (D43); alla creazione un amministratore potrebbe volerlo superare consapevolmente.
**Opzioni**: A) blocco duro come nel bilanciamento: il nodo appare non idoneo con il motivo ("mancano 12 GB"); B) consentito con conferma di pericolo che dice di quanto si supera; C) consentito con un avviso semplice.
**Suggerimento**: A: coerente con il bilanciamento, che altrimenti proporrebbe subito di spostarla.
**Risposta utente**: 2026-11-04 — A

### D59 — Nodo consigliato con GPU
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Con una GPU l'utente sceglie la scheda precisa (decisione 2026-11-03), quindi il nodo e' quello della scheda. Come entra il "consigliato"?
**Perche' conta**: senza regola il consiglio sul nodo e quello sulla scheda possono contraddirsi.
**Opzioni**: A) nell'elenco delle schede libere (raggruppate per memoria) la scheda il cui nodo e' piu' adatto (RAM dopo, pareggio) e' marcata "consigliata"; le schede su nodi dove la VM non entra sono in grigio con il motivo; B) prima si sceglie il nodo consigliato, poi solo le schede di quel nodo.
**Suggerimento**: A.
**Risposta utente**: 2026-11-04 — A

### D60 — Memoria delle schede (raggruppamento)
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Proxmox non dice la memoria della scheda. Va bene questa tabella, ricavata dai nomi delle schede del cluster?
**Perche' conta**: l'elenco delle schede e' raggruppato per memoria (decisione 2026-11-03); un valore sbagliato porta a scegliere la scheda sbagliata.
**Opzioni**: A) se il nome contiene "NNGB" si usa quello, altrimenti tabella: A2/A16 16 GB (A16 = 4 GPU da 16 GB, ogni funzione PCI e' una GPU da 16), T4 16 GB, A10 24 GB, L4 24 GB, A100 SXM4 40GB 40 GB, H100L 94GB 94 GB, RTX PRO 6000 Blackwell Max-Q 96 GB; modello non in tabella = gruppo "memoria non nota"; B) tabella da correggere (scrivere i valori giusti).
**Suggerimento**: A, da confermare in particolare per A2/A16 (le schede di node-1/7/11 sono A2 o A16?).
**Risposta utente**: 2026-11-04 — A (le schede A2/A16 sono A2); tabella confermata. In piu': nella pagina GPU aggiungere un modo per correggere o modificare i nomi e le dimensioni di RAM delle GPU identificate (vedi D62)

### D61 — Utente aggiuntivo e avvio
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Dopo l'avvio quanto si aspetta il guest agent prima di aggiungere l'utente, e cosa si fa se non risponde?
**Perche' conta**: al primo avvio cloud-init installa pacchetti e puo' metterci minuti; l'agent potrebbe non esserci (template senza agent).
**Opzioni**: A) attesa fino a 5 minuti; se l'agent non risponde la creazione termina "con avviso": VM creata e accesa, utente aggiuntivo non creato, da aggiungere col pulsante; B) attesa 10 minuti; C) come A ma con un secondo tentativo dopo altri 5 minuti.
**Suggerimento**: A.
**Risposta utente**: 2026-11-04 — C: attesa fino a 5 minuti, secondo tentativo dopo altri 5; se fallisce anche il secondo, la creazione termina con avviso (VM creata e accesa, utente aggiuntivo da aggiungere col pulsante)

### D62 — Correggere nome e memoria delle schede dalla pagina GPU
**Stato**: RISPOSTA
**Bloccante**: no (per il consiglio si parte dalla tabella confermata in D60)
**Domanda**: Nella pagina GPU si vogliono poter correggere il nome mostrato e la memoria (GB) di un modello di scheda. Dove si salvano queste correzioni e a chi valgono?
**Perche' conta**: oggi non si salva niente di questo tipo; la memoria serve a raggruppare le schede nella creazione VM e nei report. Le regole dicono niente `localStorage` e preferenze nel `config.json` via backend.
**Opzioni**: A) per **modello** (stringa che Proxmox riporta, es. `GA107GL [A2 / A16]`): una voce `gpu_models` in `config.json` con nome mostrato e GB, valida per tutte le schede di quel modello e per tutti i cluster; salvataggio da una finestra "Modifica modello" nella pagina GPU, validato (nome <= 64 caratteri, GB intero 1-512), la tabella di D60 resta il valore predefinito; B) come A ma separata per cluster; C) per singola scheda (indirizzo + nodo).
**Suggerimento**: A: la memoria e il nome sono proprieta' del modello, non della scheda; poche voci, nessun nuovo file.
**Risposta utente**: 2026-11-04 — A

### D63 — Migrazione a caldo con `cpu: host` tra generazioni dello stesso produttore
**Stato**: RISPOSTA
**Bloccante**: si' (il Bilanciamento oggi propone passi che falliscono)
**Domanda**: Le VM 133 e 198 (`cpu: host`) migrate a caldo da `node-10` (AMD EPYC 7713, Zen 3) a `node-5` (AMD EPYC 7302, Zen 2) hanno trasferito tutta la memoria e poi sono fallite alla ripresa (`resume failed`), come la 109 (AMD -> Intel). La regola "stesso produttore" (D55 corretta) non basta: verso una generazione piu' VECCHIA il guest perde istruzioni. La prova riuscita della 174 (Intel piu' nuovo -> piu' vecchio) e' stata fortunata. Che regola usare?
**Opzioni**: A) stesso **modello** di CPU (la D55 originale), verso gli altri solo a freddo; B) stesso produttore e generazione di destinazione **uguale o piu' nuova** della sorgente, con una tabella modello -> generazione per le CPU del cluster (EPYC 7302 Zen2 < 7453/7713/7763 Zen3; Xeon E5 v4 Broadwell < Silver 4114 / Gold 5120 Skylake) e modello non in tabella = solo stesso modello; C) a caldo solo per VM con tipo di CPU generico, `cpu: host` sempre a freddo.
**Suggerimento**: B: e' la regola che segue cosa vede il guest; A e' la piu' semplice e sicura se la tabella sembra fragile.
**Risposta utente**: 2026-11-05 — B (realizzata con i flag CPU di Proxmox: la destinazione deve avere tutte le istruzioni della sorgente; verificata con la prova di tutte le coppie sulla VM 108 `moving-test`)

## Disco aggiuntivo alla creazione della VM (richiesta utente 2026-11-05)

Richiesta: sotto "Utente aggiuntivo" della pagina Nuova VM, una sezione "Disco aggiuntivo" per creare direttamente un secondo disco. Oggi il pannello VM ha gia' "Aggiungi disco" in due fasi: (1) collega un volume nuovo alla VM (`adddisk`: storage, GB, bus, primo slot libero), (2) lo formatta ext4 e lo monta nel guest (`setup-disk`: guest agent, mount point validato, sceglie l'unico disco vuoto e rifiuta se ce ne sono piu' di uno).

### D64 — Fino a dove arriva il disco aggiuntivo
**Stato**: RISPOSTA
**Bloccante**: si'
**Domanda**: Il disco aggiuntivo deve solo essere collegato alla VM o anche formattato e montato?
**Perche' conta**: collegarlo e' una sola scrittura su Proxmox; formattarlo e montarlo scrive dentro il guest (serve la VM accesa e il guest agent) e, sbagliando disco, distrugge dati. Su una VM appena clonata l'unico disco vuoto e' il nuovo, quindi la scelta del disco e' sicura.
**Opzioni**: A) solo collegato: il disco compare nella VM vuoto, si formatta dopo con il pulsante del pannello; B) collegato, formattato ext4 e montato in una cartella scelta nel modulo (come "Aggiungi disco" del pannello), dopo l'avvio e l'attesa dell'agent come per l'utente aggiuntivo; se l'agent non risponde la VM resta creata con il disco collegato e un avviso; nessun riavvio forzato per far comparire il disco (diverso dal pulsante del pannello); con "Avvia al termine" spento resta collegato e basta.
**Suggerimento**: B: "creare direttamente un secondo disco" vuol dire averlo utilizzabile; il ripiego sull'avviso e' lo stesso dell'utente aggiuntivo.
**Risposta utente**: 2026-11-05 — B

### D65 — Su quale storage
**Stato**: RISPOSTA
**Bloccante**: si'
**Domanda**: Dove si crea il volume del secondo disco?
**Perche' conta**: lo storage deve essere raggiungibile dal nodo scelto e avere spazio; la scelta del nodo consigliato deve tenerne conto.
**Opzioni**: A) predefinito lo stesso storage del disco del template, ma si puo' scegliere tra gli storage del nodo scelto che accettano dischi di VM e hanno spazio libero per la dimensione richiesta (il consiglio dei nodi esclude quelli che non lo hanno, con il motivo e i GB mancanti); B) sempre e solo lo storage del template; C) campo libero.
**Suggerimento**: A: coerente con il resto del consiglio (motivi in parole, quantita' mancanti).
**Risposta utente**: 2026-11-05 — A

### D66 — Bus, slot e quanti dischi
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Bus (virtio/scsi/sata) e slot li sceglie l'utente? Si puo' aggiungere piu' di un disco?
**Perche' conta**: semplicita' del modulo contro flessibilita'.
**Opzioni**: A) un solo disco aggiuntivo, stesso bus del disco principale del template, primo slot libero (nessuna scelta nel modulo); B) un solo disco, bus scelto nel modulo; C) piu' dischi (fino a 4) con "Aggiungi un altro disco", ognuno con storage, GB, bus e cartella.
**Suggerimento**: A per ora: e' quello che hai chiesto ("un secondo disco"); C si puo' aggiungere dopo senza rifare il resto.
**Risposta utente**: 2026-11-05 — A (con il default "Disco device" della pagina Nuova VM portato a VirtIO: `virtio0` al posto di `scsi0`, lato pagina e lato server)

### D67 — Cartella di montaggio e permessi
**Stato**: RISPOSTA
**Bloccante**: no (solo con D64 B)
**Domanda**: Quale cartella e quali permessi nel guest?
**Perche' conta**: il pulsante del pannello rifiuta cartelle di sistema (`/etc`, `/var`, `/home`, `/tmp`...) e crea la cartella con permessi 1777 (sticky) o 0777.
**Opzioni**: A) campo "Cartella di montaggio" con predefinito `/data`, stesse regole e stessi controlli del pulsante, e la stessa casella "permessi aperti a tutti (1777)" attiva di default; B) cartella fissa `/data` senza campo; C) solo il campo, senza valore predefinito.
**Suggerimento**: A.
**Risposta utente**: 2026-11-05 — A

**Interpretazione dichiarata (D66)**: "cambia il default nel crea vm in VirtIO" letto come il campo "Disco device" della pagina Nuova VM (prima `scsi0`); il disco aggiuntivo usa lo stesso bus del disco principale (un disco principale `ide` ripiega su virtio, che ha abbastanza slot).
**Richiesta in piu' (2026-11-05)**: attesa del guest agent portata a **10 minuti per attesa** (due attese, fino a 20 minuti in tutto): in una creazione reale l'avviso `agent_timeout` e' arrivato pochi secondi prima che il primo avvio (con gli aggiornamenti automatici) finisse e comparisse il login.

## ROADMAP 6 — `setup-disk` (finestra "Aggiungi disco" della pagina VM)

Verifica del 2026-11-05 dopo l'aggiunta del disco alla creazione VM: la creazione usa una funzione propria (`formatAndMountBlank`, mai un riavvio) e non ha cambiato `SetupVMDisk`. Nel pulsante del pannello restano due cose: (1) il disco scelto nel menu e' validato (`validateDiskSlot`) ma **non usato**: il server formatta l'unico disco vuoto che trova (`candidates[0]`); (2) se non ne trova, **spegne e riaccende la VM** (`forceStop`, fino a 2 minuti) senza una conferma esplicita: c'e' solo la riga di avviso sotto il pulsante. Il rischio di perdere dati e' basso (si tocca solo un disco senza partizioni ne' filesystem, e con piu' di un candidato non scrive), ma il riavvio puo' essere inutile (disco gia' configurato) e brusco su una VM di produzione. Il collegamento "slot Proxmox -> dispositivo nel guest" richiede la dimensione del disco: oggi `find-blank` stampa solo i nomi, e cambiarlo vuol dire reinstallare l'helper `disk.sh` nei template.

### D68 — Come sistemare `setup-disk`
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Quanto si vuole correggere?
**Opzioni**: A) piccola: il server rifiuta uno slot che non esiste nella configurazione della VM (400); il riavvio non parte piu' da solo: il server risponde 409 `restart_needed` e la pagina chiede una conferma di pericolo ("la VM viene spenta e riaccesa") prima di ripetere la richiesta con il permesso; il testo sotto il pulsante dice che "disco scelto" e' quello vuoto trovato nel guest; B) completa: come A, piu' collegamento slot -> dispositivo con la dimensione (nuovo sotto-comando o output di `find-blank` con la dimensione, con helper vecchio che continua a funzionare ripiegando su A) e rifiuto se la dimensione non corrisponde; richiede di aggiornare gli helper nei template; C) lasciare com'e' e togliere solo il punto dalla roadmap.
**Suggerimento**: A: toglie il riavvio a sorpresa, che e' il vero rischio, senza toccare gli helper nei guest; B solo se capita davvero di avere piu' dischi vuoti insieme.
**Risposta utente**: 2026-11-05 — A

## ROADMAP 12 — Controllo della RAM prima di avviare una VM (richiesta utente 2026-11-05: "semplice, affrontiamolo")

Assunzioni dichiarate (provvisorie, non bloccanti: l'utente ha chiesto di procedere senza discussione):
- **Misura:** lo stesso modello del bilanciamento e del consiglio per le VM nuove: RAM assegnata alle VM **accese** del nodo + la RAM della VM da avviare deve stare entro la RAM assegnabile (fisica meno riserva di 8 GB + 4 GB per OSD Ceph). Le VM spente non contano (non occupano RAM).
- **Cosa succede se non entra:** rifiuto (409 `not_enough_ram`) con i GB mancanti, senza avviare niente e senza possibilita' di forzare dalla pagina; se serve davvero si avvia da Proxmox.
- **Dove:** avvio singolo di VM e di container e azione `start` del Batch; non nei percorsi che hanno gia' deciso il nodo con lo stesso modello (migrazione a freddo del bilanciamento, creazione guidata) ne' nel riavvio di `setup-disk` (la VM era gia' accesa e la sua RAM e' gia' contata).
- **Quando non si riesce a leggere il cluster:** si lascia partire l'avvio (decide Proxmox) e si scrive nel log; il controllo e' una protezione, non una condizione per poter avviare.
- **Avvii ravvicinati:** controllo e avvio sono serializzati per nodo, e una VM avviata da meno di 90 secondi che Proxmox mostra ancora ferma conta come accesa (un Batch di avvii non puo' superare il limite).

**D45 rivista (2026-11-05, richiesta utente):** il predefinito dell'interruttore "Le VM spente contano" passa da **si' a no**: una VM spenta non occupa RAM, e il consiglio per le VM nuove e il controllo prima dell'avvio gia' non le contano. L'interruttore resta nella pagina Bilanciamento per chi vuole prevedere cosa succede se le spente partono.

## ROADMAP 10 — Controlli preventivi alla creazione VM (richiesta utente 2026-11-05)

Assunzioni dichiarate (provvisorie, non bloccanti):
- **File che stanno sul nodo e non vengono copiati dal clone:** gli snippet di `cicustom` (`local:snippets/...`) e le ISO collegate al template (`local:iso/...`, per esempio il template Windows). Se lo storage e' condiviso non si controlla; altrimenti il file deve comparire nell'elenco dello storage del **nodo di destinazione** (`/nodes/{n}/storage/{s}/content`). Se manca: il nodo non e' idoneo (motivo `file_missing` con il nome del file) e la creazione e' rifiutata prima del clone. Se l'elenco non si legge non si blocca (si segnala nelle parti non lette). Il nodo del template non si controlla (il file c'e').
- **Spazio del clone:** si somma, per ogni storage, la dimensione dei volumi del template che il clone copia (dischi, EFI, TPM, unita' di cloud-init: 4 MB se non indicata; le ISO non si copiano), con il disco principale alla dimensione richiesta, piu' il disco aggiuntivo se sullo stesso storage. Si confronta con lo spazio libero che Proxmox riporta per lo storage (non riportato = non si controlla). Se manca: nodo non idoneo (`clone_no_space` con i GB mancanti).
- **Dove:** nel consiglio dei nodi (cosi' la pagina li esclude) e di nuovo nel controllo preliminare del server prima del clone. Il consiglio legge il template dal server (`template_vmid`), non si fida del browser.
- **Effetto collaterale voluto:** un template con una ISO su `local` (come il Windows 130) risulta ora "file mancante" sui nodi che non l'hanno, invece del generico "storage non condiviso".

**Correzione emersa dalla prova sul cluster vero (2026-11-05):** lo storage `local` ha come contenuti `vztmpl,backup,iso`, **senza `snippets`**, e Proxmox elenca solo i tipi di contenuto abilitati: per questo l'elenco degli snippet e' vuoto anche sul `node-1`, dove il file c'e'. Quindi un file e' **verificabile solo se lo storage ha abilitato quel tipo di contenuto**; altrimenti il controllo non e' possibile, non si blocca niente e la risposta lo dice (`files_unverified`, nella pagina: "gli snippet o le ISO del template non si possono verificare... abilitando il tipo di contenuto in Proxmox il controllo diventa possibile"). Se lo storage del file non esiste (o non e' attivo) sul nodo, il file e' irraggiungibile (`file_missing`). Le ISO invece si verificano (il `local` ha `iso`): il template Windows 130 ha le sue due ISO solo su `node-11`, quindi li' e' idoneo e altrove no. Per abilitare il controllo degli snippet basterebbe aggiungere `snippets` ai contenuti di `local` in Proxmox (Datacenter > Storage): lo decide l'utente, l'app non tocca la configurazione degli storage.

## ROADMAP 13 — Sincronizzazione di script e snippet (richiesta utente 2026-11-05)

**Obiettivo (dall'utente):** il `node-1` di CALCOLO contiene gli script aggiornati (creazione, aggiornamento e supporto) e gli snippet di cloud-init usati anche dal manager, e **non ha copie**. Serve poterli copiare su un altro nodo dello stesso cluster o di un altro cluster; `rsync_snippets.sh` oggi sincronizza solo verso i nodi diversi dal nodo 1 e va reso generico (sorgente spostabile); la copia deve avere una **sezione propria nella parte amministrativa del manager**, che puo' anche gestire il sync.

**Cosa ho verificato (sola lettura):** questa macchina e' la postazione (`diego-ict`), non un nodo: `rsync_snippets.sh` e `/var/lib/vz/snippets` non si vedono da qui. CALCOLO (11 nodi) **non ha CephFS** (nessun MDS) **ne' storage condiviso per file**: gli unici condivisi sono dischi Ceph e PBS, quindi gli snippet vanno copiati nodo per nodo. Lo storage `local` di CALCOLO ha contenuti `backup,iso,vztmpl`, **senza `snippets`**: l'API non li elenca (e il controllo preventivo del consiglio dei nodi non puo' verificarli). HOSTING (2 nodi) ha invece `local` con `iso,snippets,vztmpl,backup`. Gli snippet sono `vendor-data` di cloud-init: **vengono eseguiti come root in ogni VM nuova**, quindi l'integrita' dei file e' il requisito principale (vedi rischi in `CLAUDE.md`).

### D69 — Inventario: cosa c'e' davvero sul nodo-1
**Stato**: RISPOSTA (nodo-1 letto, resto in `SUPPORT-FILES/`) (risposta parziale: nodo-1 letto, restano gli altri file di `/root` e `/root/cloud-images`, vedi D81)
**Bloccante**: si'
**Domanda**: Posso avere, in sola lettura, `cat` di `rsync_snippets.sh`, `ls -la` della cartella degli snippet e l'elenco (percorsi) degli script di creazione, aggiornamento e supporto che stanno sul nodo-1 (e quali di questi usa il manager)? Senza non so cosa sincronizzare ne' dove stia ogni cosa.
**Opzioni**: A) li lanci tu e incolli l'output (comandi sotto); B) mi indichi un accesso SSH in sola lettura al nodo-1 (host, utente, chiave) e lo leggo io.
**Suggerimento**: A: nessun accesso nuovo per me. Comandi, da lanciare sul nodo-1: `cat <percorso>/rsync_snippets.sh` (e dove sta: `find / -xdev -name 'rsync_snippets*' 2>/dev/null`), `ls -la --time-style=long-iso /var/lib/vz/snippets`, `ls -la ~ /root /usr/local/bin /usr/local/sbin 2>/dev/null | head -80`, `crontab -l; systemctl list-timers | grep -i snip`.
**Risposta utente**: 
root@datacloud-node-1:~# cat /root/cloud-images/rsync_snippets.sh
#!/bin/bash
#script per sincronizzare gli snippet
echo "[$(date -Iseconds)] Start snippets synching..."
logger -t UPDATE-CLOUD-INIT-DATACLOUD 'Inizio rsync degli snippet'
rsync -auv /var/lib/vz/snippets root@10.75.132.2:/var/lib/vz/
rsync -auv /var/lib/vz/snippets root@10.75.132.3:/var/lib/vz/
rsync -auv /var/lib/vz/snippets root@10.75.132.4:/var/lib/vz/
rsync -auv /var/lib/vz/snippets root@10.75.132.5:/var/lib/vz/
rsync -auv /var/lib/vz/snippets root@10.75.132.6:/var/lib/vz/
rsync -auv /var/lib/vz/snippets root@10.75.132.7:/var/lib/vz/
rsync -auv /var/lib/vz/snippets root@10.75.132.8:/var/lib/vz/
rsync -auv /var/lib/vz/snippets root@10.75.132.9:/var/lib/vz/
rsync -auv /var/lib/vz/snippets root@10.75.132.10:/var/lib/vz/
rsync -auv /var/lib/vz/snippets root@10.75.132.11:/var/lib/vz/

echo "[$(date -Iseconds)] Snippets synched."
logger -t UPDATE-CLOUD-INIT-DATACLOUD 'Fine rsync degli snippet'


root@datacloud-node-1:~# find / -xdev -name 'rsync_snippets*' 2>/dev/null
/root/cloud-images/rsync_snippets.sh
root@datacloud-node-1:~# ls -la --time-style=long-iso /var/lib/vz/snippets
total 65
drwxr-xr-x 2 root root     9 2026-11-02 14:40 .
drwxr-xr-x 6 root root     6 2023-12-20 12:27 ..
-rw-r--r-- 1 root root   971 2026-04-28 09:29 vendor_debian_13.yaml
-rw-r--r-- 1 root root   971 2026-04-28 09:27 vendor_debian.yaml
-rw-r--r-- 1 root root 12359 2026-11-02 14:40 vendor_rocky_8.yaml
-rw-r--r-- 1 root root 12892 2026-11-02 14:40 vendor_rocky_9.yaml
-rw-r--r-- 1 root root  1738 2026-09-14 09:46 vendor_ubuntu_24.04.yaml
-rw-r--r-- 1 root root  1738 2026-09-14 09:47 vendor_ubuntu_26.04.yaml
-rw-r--r-- 1 root root  1520 2026-09-14 09:45 vendor_ubuntu.yaml

root@datacloud-node-1:~# ls -la ~ /root /usr/local/bin /usr/local/sbin 2>/dev/null | head -80
/root:
total 104
drwx------  9 root root    23 Nov  4 13:29 .
drwxr-xr-x 18 root root    22 Feb 12  2024 ..
drwx------  3 root root     3 Dec 20  2023 .ansible
-rw-------  1 root root 13243 Nov  4 13:29 .bash_history
-rw-r--r--  1 root root   571 Apr 10  2021 .bashrc
drwx------  4 root root     4 Apr 19  2024 .cache
drwxr-xr-x 10 root root    19 Oct 14 11:15 cloud-images
-rw-r--r--  1 root root 50378 Dec 16  2024 cluster_state.json
drwx------  4 root root     4 Dec 22  2023 .config
-r--r--r--  1 root root  8390 Nov  2 14:37 .fishsrv.pl
-rw-r--r--  1 root root    35 Aug  5 11:03 .forward
-rw-------  1 root root    33 Aug  5 08:49 .lesshst
drwx------  2 root root     5 Sep 13  2024 .links2
drwxr-xr-x  3 root root     3 Sep 20  2023 .local
-rw-r--r--  1 root root   132 Jul  4 11:05 .profile
-rw-r--r--  1 root root  1507 Aug 12 10:24 proxmox_shutdown.log
-rw-------  1 root root  1024 Sep  5  2025 .rnd
-rw-r--r--  1 root root    66 May  2  2024 .selected_editor
-rwxr-xr-x  1 root root  1387 Aug  7 13:41 set-vm-startup-order.sh
drwx------  2 root root     8 Jun 29 13:41 .ssh
-rwx------  1 root root  2298 Aug  5 09:58 stop_all_vm_lxcs.sh
-rw-------  1 root root 12467 May 13  2024 .viminfo
-rw-r--r--  1 root root   261 Apr  4  2025 .wget-hsts

/root:
total 104
drwx------  9 root root    23 Nov  4 13:29 .
drwxr-xr-x 18 root root    22 Feb 12  2024 ..
drwx------  3 root root     3 Dec 20  2023 .ansible
-rw-------  1 root root 13243 Nov  4 13:29 .bash_history
-rw-r--r--  1 root root   571 Apr 10  2021 .bashrc
drwx------  4 root root     4 Apr 19  2024 .cache
drwxr-xr-x 10 root root    19 Oct 14 11:15 cloud-images
-rw-r--r--  1 root root 50378 Dec 16  2024 cluster_state.json
drwx------  4 root root     4 Dec 22  2023 .config
-r--r--r--  1 root root  8390 Nov  2 14:37 .fishsrv.pl
-rw-r--r--  1 root root    35 Aug  5 11:03 .forward
-rw-------  1 root root    33 Aug  5 08:49 .lesshst
drwx------  2 root root     5 Sep 13  2024 .links2
drwxr-xr-x  3 root root     3 Sep 20  2023 .local
-rw-r--r--  1 root root   132 Jul  4 11:05 .profile
-rw-r--r--  1 root root  1507 Aug 12 10:24 proxmox_shutdown.log
-rw-------  1 root root  1024 Sep  5  2025 .rnd
-rw-r--r--  1 root root    66 May  2  2024 .selected_editor
-rwxr-xr-x  1 root root  1387 Aug  7 13:41 set-vm-startup-order.sh
drwx------  2 root root     8 Jun 29 13:41 .ssh
-rwx------  1 root root  2298 Aug  5 09:58 stop_all_vm_lxcs.sh
-rw-------  1 root root 12467 May 13  2024 .viminfo
-rw-r--r--  1 root root   261 Apr  4  2025 .wget-hsts

/usr/local/bin:
total 14
drwxr-xr-x  2 root root   3 Aug  5 08:34 .
drwxr-xr-x 10 root root  11 Nov 23  2023 ..
-rwxr-xr-x  1 root root 232 Aug  5 08:34 pve-startall-guarded.sh

/usr/local/sbin:
total 9
drwxr-xr-x  2 root root  2 Nov 23  2023 .
drwxr-xr-x 10 root root 11 Nov 23  2023 ..

Ci sono anche altri file di supporto in /root e in /root/cloud-image. Forse conviene che creo un'accesso per farti leggere la cartella.

### D70 — Architettura
**Stato**: RISPOSTA
**Bloccante**: si'
**Domanda**: Chi copia i file?
**Perche' conta**: i file diventano codice eseguito come root in ogni VM nuova. Il manager non ha login: se accettasse contenuti dal browser, chiunque raggiunga la pagina potrebbe infilare codice nelle VM future.
**Opzioni**:
A) **Script generico + pagina di sola lettura.** Il nuovo `pmgr-snippets-sync` gira sul nodo sorgente (a mano o a timer); la pagina mostra solo lo stato (uguale / diverso / mancante / in piu' per nodo e file) e non copia niente. Rischio minimo, ma la copia resta un passo manuale fuori dall'app.
B) **Script generico + il manager lo lancia (consigliata).** Come A, ma la pagina ha i pulsanti "Controlla" e "Sincronizza", eseguiti dal manager via SSH verso il nodo sorgente con una chiave ristretta a **un solo comando** (`command="..."` in `authorized_keys`: il manager non ha una shell, puo' solo chiedere `status`, `diff` o `run`). I contenuti arrivano **solo dalla sorgente**: dal browser non entra nessun file.
C) **Il manager copia tramite le API di Proxmox** (upload nello storage). Nessuna chiave SSH, ma non e' certo che Proxmox accetti l'upload di snippet, non c'e' modo di leggere i file dalla sorgente tramite API, e servirebbe `snippets` abilitato ovunque: da provare prima, non lo userei come base.
D) **Uno storage condiviso** (CephFS) montato su tutti i nodi: nessuna copia dentro CALCOLO. Non c'e' CephFS e crearlo (MDS, pool) e' un lavoro grosso; non risolve comunque il passaggio verso un altro cluster.
**Suggerimento**: B. A e' il ripiego se non vuoi dare al manager una chiave SSH.
**Risposta utente**: B, da capire come gestire lo spostamento tra cluster diversi. Come nota al momento il mio utente ha questi permessi associati al suo token (Datastore.Allocate Datastore.AllocateSpace Datastore.AllocateTemplate Datastore.Audit Group.Allocate Mapping.Audit Mapping.Modify Mapping.Use Permissions.Modify Pool.Allocate Pool.Audit Realm.Allocate Realm.AllocateUser SDN.Allocate SDN.Audit SDN.Use Sys.AccessNetwork Sys.Audit Sys.Console Sys.Incoming Sys.Modify Sys.PowerMgmt Sys.Syslog User.Modify VM.Allocate VM.Audit VM.Backup VM.Clone VM.Config.CDROM VM.Config.CPU VM.Config.Cloudinit VM.Config.Disk VM.Config.HWType VM.Config.Memory VM.Config.Network VM.Config.Options VM.Console VM.GuestAgent.Audit VM.GuestAgent.FileRead VM.GuestAgent.FileSystemMgmt VM.GuestAgent.FileWrite VM.GuestAgent.Unrestricted VM.Migrate VM.PowerMgmt VM.Replicate VM.Snapshot VM.Snapshot.Rollback) su tutti i cluster. Sarebbe ideale creare un utente non root sui vari nodi di ogni cluster, mettere li gli script di generazione degli snippet in modo da non usare root per lo spostamento tra i cluster. Verifica se i permessi permettono di creare utenti. Ho aggiunto un cluster "Magi Neo Tokyo 3" che é il nostro cluster di test creato appositamente per testare queste estensioni pericolose. Non ha tutti gli script del cluster di calcolo, il piú aggiornato, quindi puó essere un ottimo candidato target per il test di creazione utente tramite API, copia etc...

### D71 — Fonte di verita' e "spostare il nodo sorgente"
**Stato**: RISPOSTA
**Bloccante**: si'
**Domanda**: Il nodo sorgente e' sempre un nodo con i file "veri" (oggi il nodo-1), oppure i file vivono in un archivio a parte?
**Opzioni**: A) la sorgente e' un nodo configurato (`source`): per cambiarla si installa lo script sul nuovo nodo, si sincronizza, si verifica e si cambia una riga di configurazione (nel manager, con la procedura guidata "Sposta sorgente"); B) un archivio versionato (git privato **fuori da questo repository**, che non deve mai contenere gli script) da cui si distribuisce a tutti i nodi, nodo-1 compreso; C) come A ma con una copia "di riserva" sempre tenuta aggiornata su un secondo nodo.
**Suggerimento**: A con C: e' quello che hai chiesto e toglie subito il punto debole (nodo-1 unica copia). B e' piu' robusto ma cambia il tuo modo di lavorare.
**Risposta utente**: A con C, anche su piú nodi

### D72 — Destinazioni
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Verso quali nodi si copia?
**Opzioni**: A) una **lista esplicita** nel file di configurazione (nodi di CALCOLO, di HOSTING o altri cluster, ognuno con host e cartella), controllata dal manager per nome; B) tutti i nodi del cluster in automatico (da `/etc/pve/.members`) piu' la lista per gli altri cluster; C) a gruppi (per esempio "tutti CALCOLO", "tutti HOSTING").
**Suggerimento**: A con la comodita' di B per il cluster della sorgente.
**Risposta utente**: A con la comodita' di B per il cluster della sorgente.

### D73 — File spariti dalla sorgente
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Se un file non c'e' piu' sulla sorgente, si cancella dalle destinazioni?
**Opzioni**: A) mai in automatico: la pagina li mostra come "in piu'" e si cancellano solo uno per uno con conferma di pericolo; B) si cancellano con la sincronizzazione (come `rsync --delete`).
**Suggerimento**: A: un errore sulla sorgente non deve cancellare le copie, che sono proprio la protezione.
**Risposta utente**: A: un errore sulla sorgente non deve cancellare le copie, che sono proprio la protezione.

### D74 — Cosa si sincronizza
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Gli snippet e gli script di supporto stanno nella stessa cartella?
**Opzioni**: A) due cartelle con elenchi di nomi ammessi: `snippets` (file `.yaml`, nella cartella degli snippet) e `scripts` (script di creazione, aggiornamento, supporto, es. `/var/lib/vz/pmgr-scripts`); B) una cartella sola; C) una lista di percorsi scelta da te nel file di configurazione.
**Suggerimento**: dipende dall'inventario (D69); di base A con una lista di estensioni ammesse (`.yaml`, `.sh`, `.sha256`) e nessun altro file.
**Risposta utente**: A, escludere img, iso etc...

### D75 — Verifica dell'integrita'
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Come si sa che la copia e' identica alla sorgente?
**Opzioni**: A) un file `MANIFEST.sha256` generato sulla sorgente (hash di ogni file) e verificato su ogni destinazione dopo la copia e a ogni controllo; la pagina mostra uguale/diverso/mancante/in piu' per file; B) solo dimensione e data (quello che elenca Proxmox), senza hash; C) A piu' firma del manifest con una chiave tua (protegge anche da una sorgente manomessa).
**Suggerimento**: A. C solo se vuoi proteggerti da chi riesce a scrivere sul nodo-1.
**Risposta utente**: A

### D76 — Quando si sincronizza
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Solo a mano o anche in automatico?
**Opzioni**: A) solo a mano dalla pagina (con anteprima delle differenze e conferma); B) a timer sul nodo sorgente; C) a mano, piu' un avviso nella panoramica quando le copie sono diverse dalla sorgente da piu' di N giorni.
**Suggerimento**: C: niente copie automatiche di codice eseguito come root, ma nessuna differenza che passa inosservata.
**Risposta utente**: C

### D77 — Accesso SSH del manager (solo se D70 = B)
**Stato**: RISPOSTA
**Bloccante**: si' (per B)
**Domanda**: Con quale utente e quale chiave il manager parla col nodo sorgente?
**Opzioni**: A) una chiave dedicata, usata solo dal manager, in `authorized_keys` del nodo sorgente con `command=` (un solo script che accetta `status|diff|run`), `no-pty,no-port-forwarding,no-agent-forwarding`, ristretta all'indirizzo della postazione; lo script (installato da te, root) fa le copie verso le destinazioni con la chiave del nodo sorgente verso gli altri nodi; B) la tua chiave attuale (no: da non usare, ha una shell completa).
**Suggerimento**: A. La preparazione (creare la chiave, aggiungere la riga) e' un passo tuo, ti preparo i comandi.
**Risposta utente**: A, ma leggi la risposta a D70 per valutare un automatismo non root grazie alle API.

### D78 — Copia di sicurezza subito
**Stato**: RISPOSTA
**Bloccante**: no (ma consigliata prima di tutto)
**Domanda**: Facciamo subito una copia manuale, prima di sviluppare niente?
**Opzioni**: A) si': copia a mano del contenuto degli snippet e degli script su un secondo nodo (e uno di HOSTING), anche solo con `rsync -a` o `tar`, cosi' il rischio "unica copia" sparisce oggi; B) si aspetta lo strumento.
**Suggerimento**: A, adesso: l'app non c'entra, e' un comando tuo di un minuto una volta che sai la cartella.
**Risposta utente**: B

### D79 — Utente non root sui nodi: chi lo crea la prima volta
**Stato**: RISPOSTA
**Bloccante**: si'
**Domanda**: Le API di Proxmox non creano utenti del sistema operativo (verificato, vedi `CLAUDE.md`): `POST /access/users` crea solo un utente Proxmox (realm `pve`/`pam`), e per `pam` l'utente Linux deve gia' esistere. Come creiamo `pmgr-sync` sui nodi?
**Perche' conta**: serve root almeno una volta per nodo. Dopo, la sincronizzazione gira senza root.
**Opzioni**: A) ti preparo uno script idempotente (crea utente `pmgr-sync` senza password, gruppo `pmgr-sync`, cartelle con permessi, riga in `authorized_keys`) e lo lanci tu: a mano, o con Ansible (hai gia' `.ansible` su nodo-1) su tutti i nodi di un cluster; B) mi dai un accesso root temporaneo solo ai 3 nodi di Magi per la prova.
**Suggerimento**: A, provato prima su Magi (lo lanci tu). Su CALCOLO (10 nodi + nodo-1) e HOSTING lo stesso script, quando la prova e' andata bene.
**Risposta utente**: A

### D80 — Dove stanno gli script e gli snippet "veri"
**Stato**: RISPOSTA
**Bloccante**: si'
**Domanda**: `/root/cloud-images` e' sotto `/root` (0700): un utente non root non puo' leggerlo. Dove devono stare gli script da sincronizzare?
**Opzioni**: A) nuova cartella `/var/lib/vz/pmgr-scripts` (gruppo `pmgr-sync`, 2775): i tuoi generatori restano dove sono e scrivono li' (o un tuo passo li copia); B) restare in `/root/cloud-images` e sincronizzare quella cartella solo come root; C) cartella unica `/var/lib/vz/pmgr` con sottocartelle `snippets/` e `scripts/`.
**Suggerimento**: A: gli snippet restano dove Proxmox li cerca (`/var/lib/vz/snippets`, gruppo `pmgr-sync`), gli script in una cartella accanto, nessun file in `/root` coinvolto.
**Risposta utente**: A

### D81 — Cosa c'e' in `/root/cloud-images` e negli altri file di supporto
**Stato**: RISPOSTA
**Bloccante**: si'
**Domanda**: Non ti serve darmi un accesso: bastano elenchi di nomi. Sono file con segreti?
**Perche' conta**: la sincronizzazione ammette solo estensioni scelte (`.sh`, `.yaml`, `.sha256`); i segreti non devono finire su altri nodi ne' nel manifest.
**Opzioni**: A) incolli l'output di `ls -laR --time-style=long-iso /root/cloud-images | head -150` e di `grep -rIlE 'password|passwd|secret|token|BEGIN [A-Z ]*PRIVATE' /root/cloud-images /root/*.sh 2>/dev/null` (solo i nomi dei file che contengono quelle parole, non il contenuto); B) crei un utente di sola lettura sul nodo-1 (sconsigliato: serve a me solo per leggere nomi).
**Suggerimento**: A.
**Risposta utente**: ti ho messo una copia della cartella in modo che la puoi esaminare e leggeri gli script per correggerli in /SUPPORT-FILES da aggiungere a git ignore. In questo modo almeno all'inizio abbiamo una copia in root della cartella con gli script funzionanti. Quando avrai analizzato i file ti sarai reso conto che esistono script di aggiornamento delle immagini che creano i templates. Dobbiamo mettere un pulsante che esegua l'aggiornamento da proxmox manager. Da decidere come gestire l'upgrade di un template ora che avró multi copie. Questo lo metterei come task successivo dipendente dalla fine di questo. 

### D82 — Tra cluster: chi spinge i file
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Il nodo sorgente raggiunge in rete i nodi degli altri cluster (Magi 10.75.132.201-203, HOSTING 10.75.132.100...)?
**Opzioni**: A) spinge la sorgente (`rsync` su ssh con la chiave di `pmgr-sync` del nodo sorgente verso `pmgr-sync` delle destinazioni): il manager non vede mai i file; B) il manager fa da ponte (legge dalla sorgente e scrive sulla destinazione): serve solo se la sorgente non raggiunge l'altro cluster, e il manager avrebbe una chiave per ogni destinazione.
**Suggerimento**: A. Verifica dal nodo-1: `for h in 10.75.132.201 10.75.132.100; do timeout 3 bash -c "echo > /dev/tcp/$h/22" && echo "$h ok" || echo "$h NO"; done`.
**Risposta utente**: Ho verificato, A. Altra feat da aggiungere in sequenza sulla roadmap: migrazione di una vm su un cluster diverso.

### D83 — Password in chiaro negli script di finalizzazione
**Stato**: RISPOSTA
**Bloccante**: si' (prima di sincronizzare `finalize_image.sh`)
**Domanda**: I sette `finalize_image.sh` (`debian`, `debian_13`, `rocky_8`, `rocky_9`, `ubuntu`, `ubuntu_24.04`, `ubuntu_26.04`) contengono in chiaro la password che `qm set --cipassword` mette nei template (per l'utente `tecnico`). Come la togliamo?
**Perche' conta**: la sincronizzazione copierebbe la password su tutti i nodi e su altri cluster, e finirebbe in ogni backup e log. Nel repository non c'e' (`SUPPORT-FILES/` e' ignorata), ma e' comparsa nell'output di questa sessione di lavoro durante l'esame dei file.
**Opzioni**: A) gli script leggono la password da un file `0600` fuori dalla cartella sincronizzata (per esempio `/root/.pmgr-template-secrets`), presente solo sui nodi dove si eseguono gli aggiornamenti; `finalize_image.sh` resta sincronizzabile; B) `finalize_image.sh` esclusi dalla sincronizzazione e lasciati solo sul nodo che aggiorna i template; C) A piu' cambio della password (la nuova non e' mai passata di qui).
**Suggerimento**: A, e C a tua discrezione (la password attuale e' valida solo per l'utente `tecnico` dei template e le VM nuove la cambiano? da verificare con te).
**Risposta utente**: A, mi occuperó di cambiare la password in seguito

### D84 — Correzioni agli script di aggiornamento (prima di sincronizzarli)
**Stato**: RISPOSTA
**Bloccante**: si'
**Domanda**: Posso correggere le copie in `SUPPORT-FILES/` (e poi tu le rimetti sul nodo-1) con queste modifiche? Elenco dopo la lettura:
1. **Percorsi**: ovunque c'e' `/root/cloud-images` scritto a mano e in `ubuntu_24.04/*.sh` percorsi relativi (`./ubuntu_24.04/...`, funzionano solo lanciandoli da `/root/cloud-images`). Serve una cartella base unica (variabile `PMGR_BASE`, predefinita la cartella dello script) per poterli spostare.
2. **Template distrutto prima di sapere se l'immagine c'e'**: `update_image.sh` cancella l'immagine e poi scarica con `wget -q` senza controllare l'esito; `create_template.sh` fa `qm destroy 1003` comunque. Se il download fallisce (rete, mirror) il template sparisce e se ne crea uno vuoto. Proposta: `set -euo pipefail`, scarico in un file temporaneo, controllo di dimensione e hash (dove il mirror li pubblica), sostituzione solo a scarico riuscito, `qm destroy` solo dopo.
3. **VMID, storage, bridge, DNS, dominio di ricerca** scritti a mano (1001-1007, `datacloud-ssd`, `vmbr0`, `10.248.16.30 10.248.26.30`, `inside.dei.polimi.it`): in un file di configurazione per cluster (necessario per Magi e HOSTING, che hanno storage e DNS diversi).
4. **Password in chiaro**: vedi D83.
5. **`ubuntu_26.04/finalize_image.sh` usa `vendor_ubuntu_24.04.yaml`** (invece di `vendor_ubuntu_26.04.yaml`, che esiste ed e' identico per ora): probabile copia-incolla; da correggere?
6. **`update.sh`**: `#/bin/bash` senza `!` (parte comunque con la shell dell'utente, ma non e' un bash garantito).
7. **Helper dei guest**: le copie in `rocky_8/guestscripts` e `rocky_9/guestscripts` (`adduser.sh`, `disk.sh`) hanno stessa logica ma sono vecchie (accenti e `—` al posto di ASCII): i generatori dovrebbero prenderle dal repository (`internal/handlers/guestscripts/`) come dichiarano gia' nei commenti, cosi' si evita la deriva. `dmesg.sh` e' identico.
8. **Nessun controllo se un altro aggiornamento e' in corso** (lock) ne' log unico: 3 log in tutto, a mano.
**Opzioni**: A) tutte e otto; B) solo 1-3 e 5-6 ora, il resto con il pulsante di aggiornamento (task successivo); C) solo quelle che servono alla sincronizzazione (1, 3, 4).
**Suggerimento**: A, con un solo cambiamento per volta e prova su Magi prima di toccare il nodo-1.
**Risposta utente**: A, tutto deve funzionare sul magi prima di qualsiasi cosa. Va gestito anche il fatto che ogni cluster puó avere nomi pool diverse o target dischi diversi. La sincronizzazione multicluster deve gestire in automatico la correzione degli script. Mi serve il tuo script idempotente per crearti l'acesso sul magi (puoi usare i file di supporto come sorgente senza toccare calcolo.). Il template presente sul nodo 1 del magi é cancellabile o rinominabile con un altro ID.

### D85 — Script uguali ovunque, configurazione diversa per cluster
**Stato**: RISPOSTA
**Bloccante**: si' (prima di correggere gli script)
**Domanda**: Hai chiesto che la sincronizzazione tra cluster gestisca "in automatico la correzione degli script" (pool, storage, bridge, DNS, VMID diversi). Come?
**Perche' conta**: se la sincronizzazione riscrivesse il testo degli script per ogni cluster, i file sulle destinazioni non avrebbero piu' lo stesso hash della sorgente: il manifest (D75 A) non potrebbe piu' dire "uguale", e uno script modificato in automatico e' codice eseguito come root che nessuno ha riletto.
**Opzioni**:
A) **Script identici ovunque, tutto cio' che cambia in un file `cluster.conf` locale a ogni cluster** (storage dei template, storage del cloud-init, bridge, DNS, dominio di ricerca, VMID di partenza, nome dell'utente, percorso del file con la password). `cluster.conf` e' **escluso dalla sincronizzazione**. La "correzione automatica" e' la **creazione di `cluster.conf`**: al primo uso su un cluster nuovo il manager legge dalle API storage e rete (per Magi: `dirac-sea` al posto di `datacloud-ssd`), propone i valori, tu li confermi in pagina e li scrive. Se `cluster.conf` manca o e' incompleto gli script si fermano senza toccare niente.
B) Il manager riscrive gli script per ogni destinazione (sostituzione di nomi): sconsigliato per il motivo sopra.
C) Come A, ma `cluster.conf` generato solo a mano.
**Suggerimento**: A. Gli script restano uguali, controllabili con l'hash, e la parte "automatica" si limita a proporre i valori. Chi scrive `cluster.conf` sul nodo lo decidiamo allo step del backend (per ora lo crei tu o lo script di setup dal modello).
**Risposta utente**: Approvo la tua proposta (A).

## ROADMAP 13 — Profilo "hosting" e SSH (richiesta utente 2026-11-05)

Richiesta: (1) le Rocky del cluster di calcolo devono avere l'accesso SSH abilitato; (2) quando si fa il Sync verso un altro cluster si deve poter specificare se e' di hosting: se si', SSH con password disattivato e accesso con chiave imposto; (3) a ogni riavvio della VM l'accesso a sola chiave deve essere rimesso, perche' modifiche degli utenti non abbassino la sicurezza.

### D86 — Rocky del cluster di calcolo: SSH con password
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: "Abilita l'accesso con SSH" per le Rocky significa SSH con password, come gia' fanno Ubuntu e Debian?
**Perche' conta**: gli snippet Rocky non avevano nessuna impostazione SSH (Ubuntu/Debian impostano `PasswordAuthentication yes` e `PermitRootLogin no`).
**Opzioni**: A) si', password come Ubuntu/Debian; B) altro.
**Suggerimento**: A.
**Risposta utente**: A. Implementato come file `/etc/ssh/sshd_config.d/00-pmgr-ssh.conf` (`PasswordAuthentication yes`, `PermitRootLogin no`); su Rocky 8 lo snippet aggiunge anche la riga `Include` in testa a `sshd_config`, che li' manca.

### D87 — Dove si specifica che un cluster di destinazione e' di hosting
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Per destinazione e ricordato, oppure a ogni sync?
**Opzioni**: A) casella "hosting" sul cluster di destinazione, ricordata (configurazione del sync e pagina del manager); B) interruttore a ogni sync.
**Suggerimento**: A: meno rischio di dimenticarla e mandare snippet con password SSH su un cluster di hosting.
**Risposta utente**: A. La parte di rendering e' fatta (`pmgr-os.sh --render DIR --profile hosting`, e `PROFILE=hosting` in `cluster.conf` per i template generati nel cluster); la casella nel sync e nella pagina arriva con `pmgr-snippets-sync` (vedi D90).

### D88 — Quando si rimette l'SSH a sola chiave
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Solo al riavvio, oppure anche con un controllo periodico?
**Opzioni**: A) solo al riavvio; B) riavvio + controllo ogni 15 minuti.
**Suggerimento**: A (come richiesto).
**Risposta utente**: B: riavvio **e** ogni 15 minuti. Implementato con `pmgr-ssh-enforce` (servizio `Before=ssh.service` + timer).

### D89 — Password dell'utente del template nell'hosting
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Nel template di hosting resta la password per l'accesso da console, con il solo SSH a password disattivato?
**Opzioni**: A) si', resta; B) nessuna password.
**Suggerimento**: A.
**Risposta utente**: A. `finalize` non cambia: `cipassword` resta.

### D90 — Come il sync consegna le varianti di hosting
**Stato**: RISPOSTA
**Bloccante**: no (serve quando si scrive `pmgr-snippets-sync`)
**Domanda**: Le destinazioni di hosting devono ricevere snippet diversi dagli standard, con gli **stessi nomi di file** (i template li referenziano per nome). Come?
**Perche' conta**: il manifest con gli hash (D75 A) deve restare valido per ogni destinazione.
**Opzioni**: A) la sorgente genera **due alberi** (standard e hosting) con `pmgr-os.sh --render` e un manifest per profilo; il sync spinge a ogni destinazione l'albero del suo profilo; B) l'hosting genera i propri snippet localmente (`PROFILE=hosting` in `cluster.conf`) e il sync non li tocca; C) il sync trasforma il testo al volo (sconsigliato: gli hash non tornerebbero).
**Suggerimento**: A: una sola sorgente di verita', nessuna riscrittura al volo, il profilo e' una proprieta' della destinazione.
**Risposta utente**: Si (A): due alberi generati dalla sorgente con `pmgr-os.sh --render` (standard e hosting), un manifest per profilo, ogni destinazione riceve quello del suo profilo. Il rendering e' fatto; la consegna con la casella 'hosting' per destinazione arriva con `pmgr-snippets-sync`.

### D91 — VM di hosting gia' esistenti
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Le VM del cluster HOSTING (28) create con i vecchi snippet hanno SSH con password. Vuoi uno script di rollout, come per fail2ban, che installi `pmgr-ssh-enforce` anche su quelle?
**Perche' conta**: la regola vale solo per le VM nuove, finche' non si aggiornano quelle esistenti. Attenzione: chi oggi entra con la password viene escluso; servono le chiavi prima.
**Opzioni**: A) si', con `--check` e una VM di prova; B) no, solo VM nuove.
**Suggerimento**: A, dopo avere verificato che ogni VM abbia almeno una chiave in `authorized_keys`.
**Risposta utente**: Si (A): script di rollout con `--check` e una VM di prova, e con il controllo che ci sia almeno una chiave in `authorized_keys` prima di disattivare la password. Fatto: `pmgr-vm-rollout.sh --module ssh-hosting` (modulo `ssh-hosting` di `pmgr-vm-apply.sh`).

## ROADMAP 13 — Aggiornamenti di sicurezza automatici (richiesta utente 2026-11-05)

Richiesta: verificare che gli snippet abilitino correttamente gli aggiornamenti automatici **di sicurezza**: controllo e installazione **due volte al giorno**, **nessun riavvio automatico**, **avviso a ogni login** (console e SSH) quando serve un riavvio; **tutti i cluster** (standard e hosting).

### D92 — Esito della verifica e correzioni
**Stato**: RISPOSTA (nessuna scelta richiesta all'utente: sono correzioni di difetti)
**Bloccante**: no
**Cosa c'era**: Debian non installava nessun pacchetto per gli aggiornamenti automatici; Ubuntu aveva un cron di mezzanotte lanciato come utente `tecnico` (non root: non poteva installare) mentre i timer di apt controllano due volte al giorno ma **installano una volta sola** (06:00, con ritardo casuale fino a un'ora) e gli intervalli "1 giorno" saltano comunque il secondo giro; Rocky abilitava `dnf-automatic.timer` ma la configurazione di serie e' `apply_updates = no` (**scarica soltanto, non installa mai**) e `upgrade_type = default` (tutti gli aggiornamenti, non solo la sicurezza); nessuna delle tre mostrava un avviso di riavvio al login (Ubuntu solo se c'e' `update-notifier-common`, Debian e Rocky mai).
**Cosa e' stato fatto** (tutti i cluster e i profili): `unattended-upgrades` anche su Debian; origini **solo di sicurezza** (Debian: `-security` e `Debian-Security`; Ubuntu: `-security`, ESM apps e infra), `Automatic-Reboot "false"`, `APT::Periodic::* "always"` e `Enable "1"`; timer di apt con i due orari al giorno (liste 04:30 e 16:30, installazione 06:00 e 18:00, ritardo casuale 20-30 min); Rocky: `upgrade_type = security`, `apply_updates = yes`, `reboot = never` (dove l'opzione esiste), timer di `dnf-automatic` alle 06:00 e 18:00, `dnf-utils`; il vecchio cron tolto; `pmgr-reboot-check` (kernel installato piu' nuovo di quello in uso, `reboot-required`, `needs-restarting`, flag in `/run`) e `/etc/profile.d/pmgr-reboot-required.sh` che a ogni login interattivo (console e SSH) stampa l'avviso in italiano e in inglese e **non** sporca l'output di script e comandi via SSH.
**Risposta utente**: (presa d'atto)

## ROADMAP 13 — Hardening dei sistemi di hosting (richiesta utente 2026-11-06)

Verificato e corretto (vedi `CLAUDE.md` e `SUPPORT-FILES/pmgr-scripts/README.md`): firewall con 22/80/443 aperte e il resto chiuso, SSH solo con chiave, fail2ban, aggiornamenti di sicurezza. Regola dell'utente: **nei nuovi deploy sempre l'ultima LTS**, ogni tanto LTS precedenti; la matrice dei test comprende quindi Ubuntu 26.04/24.04/22.04, Debian 13/12, Rocky 10/9/8.

### D93 — Altre misure di hardening per l'hosting (proposte, nessuna applicata)
**Stato**: RISPOSTA
**Bloccante**: no
**Domanda**: Quali di queste vuoi aggiungere al profilo hosting? Per ciascuna: rischio e mio suggerimento.
**Perche' conta**: sono VM esposte a Internet con clienti dentro; ogni misura riduce la superficie ma puo' dare fastidio a chi ci lavora.
**Opzioni** (scegli quali, anche piu' di una):
1. **sysctl di rete e kernel** (syncookies, niente redirect ICMP e source routing, risposte ICMP false ignorate, `kptr_restrict=2`, `dmesg_restrict=1`, `unprivileged_bpf_disabled=1`). Rischio basso; il "Log del kernel" del manager gira come root e non cambia. **Suggerisco si'.**
2. **Blocco della porta 25 in uscita** (la VM non puo' mandare posta direttamente: protegge la reputazione dei tuoi indirizzi dallo spam; i clienti usano un relay). Dipende dalla tua politica. **Suggerisco si'.**
3. **443/udp** aperta (HTTP/3, QUIC). **Suggerisco no**, se non serve a nessuno.
4. **Elenco di pacchetti ridotto** sull'hosting: togliere `tcpdump`, `iptraf`, `nethogs`, `iftop`, `arpwatch`, `nfs-common`, `cifs-utils`, `acct`, `nmon` (i clienti li installano se servono). Meno codice su una macchina esposta. **Suggerisco si'.**
5. **Password di console**: oggi e' la stessa su tutte le VM dei template (D83: la cambi tu) e non vale via SSH. Opzioni: lasciarla (a rischio di console accessibile), una diversa per VM, oppure account senza password di console. **Suggerisco di lasciare come e' fino al cambio, poi valutare.**
6. **fail2ban**: jail `recidive` (una settimana di ban a chi viene bannato piu' volte) e jail web (nginx/apache) quando il server web e' installato. **Suggerisco `recidive` si', jail web a richiesta.**
7. **SSH**: `ClientAliveInterval 300` + `ClientAliveCountMax 2` (chiude le sessioni morte; **suggerisco si'**); `AllowGroups` per limitare chi puo' entrare (**suggerisco no** per ora: serve decidere il gruppo e la gestione dei clienti).
8. **Limite alle nuove connessioni SSH nel firewall** (`ufw limit 22/tcp`): complementa fail2ban ma puo' bloccare automazioni in parallelo. **Suggerisco no.**
9. **Registri verso un server remoto / auditd**: politica da definire. **Suggerisco di rinviare.**
10. **Docker sui sistemi dei clienti**: le porte pubblicate da Docker aggirano `ufw`. Non si risolve con uno snippet; **suggerisco di documentarlo ai clienti** (e, se serve, regole in `DOCKER-USER`).
**Suggerimento**: 1, 2, 4, 6 (`recidive`) e 7 (`ClientAlive`).
**Risposta utente**: Approvo le proposte (2026-11-06): quelle suggerite, cioe' 1 sysctl di rete e kernel, 2 blocco della porta 25 in uscita, 4 elenco di pacchetti ridotto sull'hosting, 6 jail `recidive` di fail2ban, 7 `ClientAliveInterval 300` e `ClientAliveCountMax 2`. Restano fuori: 3 (443/udp), 5 (password di console: la cambia l'utente), 7 `AllowGroups`, 8 (limite connessioni SSH), 9 (log remoti), 10 (Docker: da documentare ai clienti). **Da implementare** (non ancora fatto).


## ROADMAP 13 — Accesso del manager al nodo sorgente (2026-11-09)

### D94 — Come il manager esegue ssh
**Stato**: APERTA
**Bloccante**: si'
**Domanda**: Il manager parla col nodo sorgente con il client `ssh` di sistema o con una libreria Go?
**Perche' conta**: una dipendenza nuova va approvata; il client di sistema deve esistere dove gira il manager.
**Opzioni**: A) `os/exec` di `/usr/bin/ssh` con lista di argomenti fissa, senza shell, `-F /dev/null`, `BatchMode`, `IdentitiesOnly`, `StrictHostKeyChecking=yes` con `known_hosts` dedicato; B) `golang.org/x/crypto/ssh`, nessun binario esterno ma una dipendenza nuova e la verifica della chiave host da scrivere.
**Suggerimento**: A: nessuna dipendenza, nessun input della richiesta negli argomenti.
**Risposta utente**:

### D95 — Chi puo' premere "Sincronizza"
**Stato**: APERTA
**Bloccante**: si'
**Domanda**: Il manager non ha login: basta la protezione delle altre azioni per una copia che finisce in codice eseguito come root nelle VM future?
**Perche' conta**: chi raggiunge la pagina non puo' inserire contenuti, ma puo' propagare a tutti i nodi una sorgente modificata a meta'.
**Opzioni**: A) guardia `/api` + anteprima obbligatoria + conferma di pericolo + `--expect <digest>`: la copia parte solo se la sorgente e' ancora identica a quella vista nell'anteprima, altrimenti 409; B) come A piu' un codice segreto da digitare (in `config.json`, hash lento); C) solo stato, copia a mano sul nodo (torna a D70 A).
**Suggerimento**: A, coerente con le altre azioni distruttive del manager; B se la VPN e' condivisa con persone che non devono poter sincronizzare.
**Risposta utente**:

### D96 — Limite di indirizzo della chiave del manager
**Stato**: APERTA
**Bloccante**: no
**Domanda**: `from=` nella chiave del manager: quale indirizzo?
**Perche' conta**: su Magi la postazione arriva come 10.75.132.254 (router), quindi `from=` limita alla rete dietro il router e non a una macchina. La chiave resta comunque ristretta a `status|diff|run`.
**Opzioni**: A) accettare il router, sapendo che il limite vero e' la chiave (0600 sulla postazione); B) far girare il manager su una macchina della rete dei nodi, con `from=` esatto.
**Suggerimento**: A per Magi; per CALCOLO decidere prima della messa in produzione.
**Risposta utente**:

### D97 — Dove sta `pmgr-snippets-sync.sh`
**Stato**: APERTA
**Bloccante**: si'
**Domanda**: Lo strumento resta solo in `SUPPORT-FILES/` (fuori da git) o va in `tools/pmgr-sync/`?
**Perche' conta**: il manager dipende dalla sua uscita JSON: se non e' versionato, codice e contratto possono divergere senza traccia. Non contiene segreti; la regola "mai nel repo" riguarda i generatori degli snippet.
**Opzioni**: A) sorgente in `tools/pmgr-sync/` con i suoi test; il comando forzato (root) esegue una copia installata in `/usr/local/libexec/pmgr/`, non scrivibile da `pmgr-sync`; B) resta locale, consegnato nella cartella sincronizzata.
**Suggerimento**: A: la chiave del manager esegue solo un file di root, non uno modificabile dall'utente della sincronizzazione.
**Risposta utente**:

### D98 — Una sorgente sola nella configurazione
**Stato**: APERTA
**Bloccante**: no
**Domanda**: In questo passo il manager conosce una sola sorgente?
**Perche' conta**: D71 chiede una sorgente spostabile, anche su piu' nodi; la procedura "Sposta sorgente" tocca chiavi su tutte le destinazioni.
**Opzioni**: A) una sorgente attiva in `snippet_sync` (host, porta, percorsi di chiave e known_hosts); spostarla si progetta con la pagina; B) elenco di sorgenti da subito.
**Suggerimento**: A: meno superficie ora, e il formato si puo' estendere.
**Risposta utente**:

### D99 — Aggiornamenti automatici sulle VM con GPU (rollout su CALCOLO)
**Stato**: APERTA
**Bloccante**: no
**Domanda**: Alle 21 VM con scheda GPU del calcolo si applicano anche gli aggiornamenti di sicurezza automatici?
**Perche' conta**: includono i kernel nuovi; con i driver NVIDIA il modulo del driver si ricostruisce solo se il pacchetto lo prevede (DKMS) e serve comunque un riavvio. Un kernel aggiornato senza driver puo' lasciare la VM senza GPU al prossimo riavvio.
**Opzioni**: A) solo `fail2ban` e `ssh-standard` (nessun aggiornamento automatico sulle GPU), decidere poi caso per caso; B) tutto come le altre (`baseline ssh-standard`); C) tutto ma escludendo i pacchetti `nvidia*`/`linux-image*` dagli aggiornamenti automatici sulle GPU.
**Suggerimento**: A per partire, poi C dopo aver visto come sono installati i driver su alcune VM.
**Risposta utente**:


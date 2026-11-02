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

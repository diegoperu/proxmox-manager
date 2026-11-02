---
name: ProxmoxManager
description: Console di amministrazione Proxmox letta come un bollettino meteo notturno - una frase di condizione sopra i numeri, stato come colore + forma della linea + parola.
colors:
  sky-night: "#0b1424"
  sky-glow: "#1d3560"
  card-night: "#13213a"
  card-raised: "#182a48"
  track-night: "#213a60"
  text: "#e9eef7"
  text-secondary: "#a9b6cc"
  text-muted: "#8394b0"
  accent: "#7fb2ff"
  accent-bright: "#a6c8ff"
  on-accent: "#06142b"
  ok-green: "#34c27a"
  danger-red: "#ff6b6b"
  on-danger: "#1a0505"
  warn-amber: "#f2a93b"
  badge-indigo: "#a3a1ff"
  badge-cyan: "#4fd1e0"
  badge-violet: "#c58bff"
  dawn-bg: "#eef2f8"
  dawn-card: "#ffffff"
  dawn-text: "#0d1b33"
  dawn-accent: "#1d5fd1"
  dawn-accent-fill: "#2d6cdf"
typography:
  headline:
    fontFamily: "Inter, system-ui, -apple-system, 'Segoe UI', Roboto, sans-serif"
    fontSize: "30px"
    fontWeight: 600
    lineHeight: 1.12
    letterSpacing: "-0.02em"
  display-number:
    fontFamily: "Inter, system-ui, sans-serif"
    fontSize: "26px"
    fontWeight: 600
    lineHeight: 1.1
    letterSpacing: "-0.02em"
    fontFeature: "tabular-nums"
  title:
    fontFamily: "Inter, system-ui, sans-serif"
    fontSize: "17px"
    fontWeight: 600
    letterSpacing: "-0.01em"
  section:
    fontFamily: "Inter, system-ui, sans-serif"
    fontSize: "15px"
    fontWeight: 600
    letterSpacing: "-0.005em"
  body:
    fontFamily: "Inter, system-ui, sans-serif"
    fontSize: "13px"
    fontWeight: 400
    lineHeight: 1.45
  label:
    fontFamily: "Inter, system-ui, sans-serif"
    fontSize: "12px"
    fontWeight: 500
  badge:
    fontFamily: "Inter, system-ui, sans-serif"
    fontSize: "11px"
    fontWeight: 600
  mono:
    fontFamily: "'JetBrains Mono', ui-monospace, monospace"
    fontSize: "12px"
    fontWeight: 400
    lineHeight: 1.8
rounded:
  badge: "6px"
  control: "8px"
  inset: "10px"
  toast: "12px"
  card: "14px"
  modal: "16px"
  pill: "20px"
spacing:
  card-pad: "16px"
  card-pad-compact: "12px"
  cell-y: "9px"
  cell-y-compact: "5px"
  cell-x: "12px"
  gap: "14px"
  gap-compact: "10px"
  row-h: "62px"
  row-h-compact: "46px"
  rail: "212px"
  topbar: "52px"
components:
  button-primary:
    backgroundColor: "{colors.accent}"
    textColor: "{colors.on-accent}"
    typography: "{typography.label}"
    rounded: "{rounded.control}"
    height: "30px"
    padding: "0 14px"
  button-primary-hover:
    backgroundColor: "{colors.accent-bright}"
  button-ghost:
    backgroundColor: "transparent"
    textColor: "{colors.text-secondary}"
    rounded: "{rounded.control}"
    height: "30px"
  button-destructive:
    backgroundColor: "transparent"
    textColor: "{colors.danger-red}"
    rounded: "{rounded.control}"
    height: "30px"
  button-destructive-confirm:
    backgroundColor: "{colors.danger-red}"
    textColor: "{colors.on-danger}"
    rounded: "{rounded.control}"
  card:
    backgroundColor: "{colors.card-night}"
    textColor: "{colors.text}"
    rounded: "{rounded.card}"
    padding: "16px"
  input:
    backgroundColor: "{colors.card-raised}"
    textColor: "{colors.text}"
    rounded: "{rounded.control}"
    height: "32px"
    padding: "0 10px"
  badge:
    backgroundColor: "{colors.card-raised}"
    textColor: "{colors.text}"
    typography: "{typography.badge}"
    rounded: "{rounded.badge}"
    padding: "2px 8px"
  chip-step:
    backgroundColor: "{colors.card-night}"
    textColor: "{colors.text-muted}"
    rounded: "{rounded.pill}"
    height: "30px"
    padding: "0 12px"
  nav-item:
    textColor: "{colors.text-secondary}"
    rounded: "{rounded.control}"
    height: "32px"
    padding: "0 10px"
  nav-item-active:
    textColor: "{colors.accent}"
---

# Design System: ProxmoxManager

## Overview

**Creative North Star: "Il Bollettino"**

Il cluster e' letto come un bollettino meteo notturno. Una frase di condizione ("Quasi sereno - 3 nodi su 4 operativi", titolo 30px con icona 48px) sta sopra i numeri e non li sostituisce; sotto, una striscia di quattro KPI con storia recente e una tabella di nodi dove ogni riga mostra l'escursione min-max di CPU e RAM nelle ultime 24h con un punto "adesso"; in fondo la Top 10 delle VM che hanno lavorato di piu', ciascuna con il proprio andamento come una previsione oraria. Il sistema rifiuta la griglia di schede KPI piu' grafico ad area che ogni console cloud ripete.

Il mondo e' un cielo blu profondo con schede un gradino piu' chiare e filetti azzurri a bassa opacita'; il tema chiaro e' la stessa struttura in "alba" lattiginosa. La densita' e' da sala operativa, non da vetrina: due densita' (comoda, compatta) sulle stesse proporzioni, nessun effetto che faccia aspettare. Lo stato non e' mai solo colore: colore + forma della linea + parola.

**Key Characteristics:**
- Frase di condizione sopra i numeri; l'eccezione (il nodo che devia) in rosso nel sottotitolo.
- Stato = colore + forma della linea (piena ok, tratteggiata offline/in corso, spessa 3px errore) + parola.
- Inter per l'interfaccia con cifre tabellari, JetBrains Mono solo per identificatori, IP, log.
- Raggi 8-16px, filetti a 1px, un solo colore di marca per tema.
- Vetro smerigliato in cinque punti soltanto; niente ombre sulle schede a riposo.
- Cinque temi con nome fisso: auto (segue il sistema), dark, light, eva01, catppuccino - stessa struttura, palette diverse.

## Colors

Cielo notturno con un solo accento azzurro; verde, ambra e rosso sono riservati allo stato.

### Primary
- **Azzurro Quota** (accent): colore di marca - voce di menu attiva, azione primaria, selezione, focus ring, intervallo min-max normale, ordinamento attivo. In chiaro diventa un blu scuro (dawn-accent) per il testo e un blu piu' pieno (dawn-accent-fill) per il riempimento dei bottoni. `--amber*` nel CSS sono alias legacy di `--accent*` usati da JS (`tk('--amber')`); i nuovi usi scrivono `--accent`.
- **Azzurro Alto** (accent-bright): hover del bottone primario.

### Secondary (stato)
- **Verde Operativo** (ok-green): attivo, in esecuzione, task riuscito.
- **Ambra Attenzione** (warn-amber): soglie superate; distinto dal marchio. Nei temi eva01 e catppuccino e' ridefinito per restare leggibile accanto all'accento.
- **Rosso Allarme** (danger-red): offline, errore, azioni distruttive, eccezione nel sottotitolo.
- Badge di tipo: indaco (VM in pausa/pool), ciano (QEMU), viola (LXC/template), mai usati per lo stato di salute.

### Neutral
- **Cielo Notte** (sky-night) sfondo, con gradiente verso **Bagliore di Cielo** (sky-glow) nei primi 380px della pagina.
- **Scheda Notte** (card-night) superfici; **Scheda Rialzata** (card-raised) campi, intestazioni tabella, riquadri interni; **Binario** (track-night) piste delle barre.
- Testo in tre gradini: text (primario), text-secondary (celle, corpo), text-muted (etichette, meta).
- Filetti: bianco-azzurro al 16% (`--border`) e 30% (`--border2`, bordi di campi/modal).
- **Alba** (dawn-bg, dawn-card, dawn-text): tema chiaro con gli stessi ruoli; accenti e semantici ridefiniti piu' scuri.
- eva01 (viola scuro, accento lime #7cc443) e catppuccino (Mocha, accento pesca #fab387) ridefiniscono le stesse variabili; nomi dei temi invariabili.

### Named Rules
**The State Is Never Colour Alone Rule.** Ogni stato porta forma della linea e parola oltre al colore (`.st`, `.toast::before`, `.task .sl`, `.pv-step::before`). Un nuovo stato definisce prima la forma.
**The One Brand Rule.** Il marchio e' `--accent`; ambra/verde/rosso significano solo stato. Un grafico o un componente nuovo legge i token, mai esadecimali letterali.
**The Contrast Floor Rule.** Ogni colore usato come testo e' >=4.5:1 su bg/bg2/bg3 in tutti i temi, badge compresi (verificato per progetto, non rimisurato in questa passata).

## Typography

**Interface Font:** Inter variabile, vendorizzata, con system-ui di riserva
**Data Font:** JetBrains Mono variabile, vendorizzata (`.tm`, `.plog`, VMID, IP, log)

**Character:** Inter sobria con spaziatura negativa sui titoli e cifre tabellari ovunque ci siano numeri; il mono compare solo dove il carattere identifica un oggetto.

### Hierarchy
- **Headline** (600, 30px, 1.12, -0.02em): frase di condizione della dashboard (22px sotto 700px).
- **Number** (600, 26px, tabular): valori della striscia KPI (`.kv`, `.card-v`).
- **Title** (600, 17px, -0.01em): titolo di modal e pannello laterale.
- **Section** (600, 15px): titolo pagina in topbar, `.sec`, nome nodo, ricerca rapida.
- **Body** (400-500, 13px, 1.45): menu, tab, sottotitolo condizione (14px).
- **Label** (500, 12px): tabelle, campi, bottoni (600), meta, intestazioni di gruppo.
- **Badge** (600, 11px): solo badge e tasto `kbd`.

### Named Rules
**The Closed Scale Rule.** Scala chiusa 30/26/17/15/13/12/11 (piu' 14px per nome-nodo e sottotitolo condizione, 20px per i valori `.mv`, 16px solo input con `pointer:coarse`). Il 11 e' riservato ai badge. Nessun valore nuovo senza passare da questo file.
**The Mono For Identifiers Rule.** Il mono e' per VMID, IP, log, mai per titoli o numeri di metrica.

## Layout

Desktop-first, griglia body a due colonne: rail 212px + contenuto, riga topbar 52px; il contenuto scorre da solo (`overflow:hidden` sul body). Dashboard: colonna principale fluida + centro attivita' 320px sticky a destra (gap 28px); sotto 1180px il centro attivita' passa in colonna unica tra il pannello nodi e la Top 10. Padding contenuto 20px 28px.

Ritmo: padding scheda 16px, gap 14px, celle 9px/12px, righe nodo min 62px. La densita' compatta (`html.compact`, da Impostazioni, in config.json) porta 12px, 10px, 5px/10px e 46px senza cambiare proporzioni.

Responsive: sotto 850px la rail diventa cassetto (pulsante menu in topbar, scrim, vetro 20px); sotto 700px KPI a 2 colonne e righe nodo a due colonne; sotto 650px niente crumb/badge, toast a tutta larghezza, ricerca ridotta a icona, colonne secondarie della lista VM e della Top 10 nascoste (restano nome, stato o andamento, CPU media; la condizione diventa solo glifo). `pointer:coarse`: bersagli 44px e campi 16px (non verificato su dispositivo touch).

## Elevation & Depth

Tonale e piatto: la profondita' viene da gradini di superficie (bg, bg2, bg3, bg4) e filetti 1px, non da ombre. Le schede e le tabelle non hanno ombra. Un'unica ombra esiste, `--shadow-pop` (`0 24px 60px -12px rgba(0,0,0,.55), 0 2px 10px rgba(0,0,0,.3)`), per modal, pannello laterale, ricerca rapida e toast.

Il vetro smerigliato (backdrop-filter) e' ammesso in cinque punti, tutti dove una superficie ne copre un'altra: intestazione sticky delle tabelle (blur 10px), toast (14px), barra di selezione delle VM (14px), scrim dei modal (6px), cassetto mobile (20px). La barra superiore usa solo un velo traslucido (55% di bg2) senza blur.

### Named Rules
**The Five Glass Rule.** Vetro solo su intestazione tabella sticky, toast, barra di selezione, scrim modal, cassetto mobile: sempre superfici che si sovrappongono ad altro contenuto. Una scheda in vetro e' un errore.
**The Flat At Rest Rule.** A riposo nessuna ombra; l'ombra pop appartiene alle sole superfici sovrapposte.

## Shapes

Raggi dichiarati: 6 (badge), 8 (controlli, voci menu, righe palette), 10 (riquadri interni, log, lista conferma), 12 (toast), 14 (schede, tabelle, pannelli), 16 (modal; il pannello laterale lo arrotonda solo sul lato sinistro), pill 20 (chip nodo, passi del provisioning). Barre e tracce sono capsule da 5-6px; i punti VM e il punto "adesso" sono cerchi. Filetti 1px ovunque; la tab attiva e' l'unico bordo da 2px, in basso. Nessuna striscia colorata sul lato sinistro di schede o righe.

## Components

### Buttons
- **Shape:** angoli da 8px, altezza 30px (26px `.sm`), etichetta 12px/600.
- **Primary:** pieno accento con testo on-accent; hover accent-bright. Un solo primario per vista ("Nuova VM").
- **Ghost:** trasparente, bordo border2, hover bordo e testo accento.
- **Destructive:** a contorno (rosso al 45%) e testo rosso; riempie con velo rosso solo su hover o focus-visible. Il pieno (`.bx`, testo on-danger) esiste solo nel dialogo di conferma.
- **Success / purple:** velo tinto a bordo sottile per azioni di avvio e secondarie tipizzate.
- **Disabled:** opacita' .4.

### Chips (passi del provisioning, selettore nodi)
Pill 20px con filetto; il passo ha una linea a sinistra: tratteggiata (da fare), piena verde (fatto), bordo accento (corrente). Il chip nodo attivo ha velo accento, bordo e testo accento.

### Cards / Containers
Raggio 14px, fondo card-night, filetto 1px, padding 16px, nessuna ombra. La striscia KPI e' un unico contenitore con quattro celle separate da filetti verticali, non quattro schede; ogni cella ha un mini grafico (linea 1.5px accento, ambra se in soglia; barre rosse per gli errori).

### Inputs / Fields
Fondo card-raised, filetto 1px, raggio 8px, altezza 32px, testo 12px. Focus: bordo accento piu' outline 2px accento. Etichette sopra in 12px/500 text-muted.

### Navigation
Rail con gruppi in 12px/600 muted (Operazioni, Analisi), voci 32px con icona 16px; attiva = testo accento su velo accento. Topbar 52px: marchio, crumb (cluster / pagina), pulsante ricerca a 32px con `kbd` Ctrl/Cmd+K, indicatore di connessione (punto 8px), densita'. Le tab sono sottolineate in accento da 2px.

### Riga nodo (firma)
Griglia: nome + sottotitolo, stato (`.st`), pista CPU, pista RAM, punti VM. La pista mostra l'intervallo min-max 24h come capsula (accento; ambra o rossa oltre soglia), una tacca tratteggiata verticale all'85% e un punto "adesso" da 12px con bordo del colore della scheda che scorre al nuovo valore (0.6s, `--ease`, disattivato con reduced-motion). Nodo offline: riga al 62%, pista sostituita da una linea tratteggiata. I punti VM hanno diametro 6-14px su cinque gradini per RAM, verdi se attive, vuoti con bordo se spente.

### Top 10 VM & Container (andamento come previsione oraria)
Una riga per VM in un pannello a filetti, ordinate per lavoro svolto (CPU media x core assegnati) nella finestra scelta con il controllo segmentato 6/12/24 h. Colonne: posizione, nome (apre il pannello VM) con sotto IP in mono, nodo e tipo; **andamento**, **CPU media** (15px/600, sotto "≈ n core su N"), **picco**, **RAM** (barra), **condizione**. L'andamento e' una curva su asse fisso 0-100% con area tinta al 10-14% sotto la linea, soglia critica 85% tratteggiata e un punto bianco sul picco (nessun asse o etichetta: si legge come la previsione oraria di un'app meteo). Colore e glifo della curva seguono la condizione, con le stesse icone della frase in testa: **sereno** (verde, sole) picco < 65% e media < 50%; **variabile** (ambra, sole con nuvola) picco >= 65% o media >= 50%; **temporale** (rosso, nuvola con fulmine) picco >= 85%. Come per ogni stato, la parola accompagna il glifo (su mobile resta solo il glifo con nome accessibile). Titolo "Top 10 VM & Container" con sottotitolo "per lavoro svolto nelle ultime N ore" e pulsante "Tutte le VM".

### Barra di selezione (lista VM)
Barra sticky in basso al centro dell'area contenuto, raggio 14px, vetro e ombra pop: conteggio, avviso se ci sono VM selezionate nascoste dai filtri, poi Start, Shutdown, Reboot, "Altre azioni in Batch" e Deseleziona. Non esegue nulla: apre Batch con VM e azione preselezionate, dove restano conferma e log. Su mobile occupa tutta la larghezza.

### Pagine secondarie
Componenti condivisi: barra strumenti (`.toolbar`) con campo di ricerca con icona, controllo segmentato (`.seg`), riepilogo a destra; pannello (`.panel`, titolo 15px/600); scheda nodo (`.nc`) con tre misuratori CPU/RAM/Disco nella stessa lingua della riga nodo; tessere tema con anteprima dei colori; elenco a definizioni (`.dl`); stato vuoto (`.empty`). Lo stato del nodo ha una sola definizione (`nodeState`) per Panoramica, Nodi e Cluster.

### Centro attivita'
Elenco a filetti: ogni task ha una linea di stato (verde piena = riuscito, accento tratteggiata = in corso, rossa da 3px = errore), titolo, dettaglio (rosso per errori) e orario tabellare.

### Pannello laterale e ricerca rapida
Il dettaglio VM si apre da destra (460px, scorrimento 0.24s) sopra uno scrim, con intestazione, elenco dt/dd, azioni e una "zona pericolo" separata da filetto in fondo. La ricerca (Ctrl/Cmd+K) e' un modal 640px con campo da 52px, risultati raggruppati, riga selezionata con velo accento.

### Toast
Vetro, raggio 12px, ombra pop, in basso a destra; una linea a sinistra dice lo stato (accento info, verde ok, rossa da 3px errore) prima del testo.

### Badge
Raggio 6px, 11px/600, velo del colore al 12-15% con bordo al 32%: verde (attivo), rosso (fermo/fallito), indaco, ciano (QEMU), viola (LXC/template), accento (nuovo).

## Do's and Don'ts

### Do:
- **Do** aprire ogni vista di riepilogo con una frase che dice la condizione, poi i numeri.
- **Do** dare a ogni stato colore + forma della linea + parola.
- **Do** leggere colori da `--accent`, `--green`, `--warn`, `--red` e dai token di superficie; nei grafici usare i token del tema.
- **Do** tenere i distruttivi a contorno e separati dal primario; il pieno rosso solo nella conferma.
- **Do** usare cifre tabellari per ogni numero e il mono per ID, IP e log.
- **Do** rispettare `prefers-reduced-motion`: lo stato resta, il movimento no.

### Don't:
- **Don't** fare una griglia di schede KPI affiancate a un grafico ad area come primo schermo.
- **Don't** indicare lo stato con il solo colore.
- **Don't** usare bordi colorati sul lato sinistro di schede, righe o avvisi.
- **Don't** applicare il vetro a schede o pannelli fuori dai cinque punti ammessi.
- **Don't** introdurre dimensioni di testo fuori scala o usare l'11px fuori dai badge.
- **Don't** aggiungere ombre alle schede a riposo.
- **Don't** usare il marchio (azzurro) per significare salute o errore.
- **Don't** rinominare i temi (auto, dark, light, eva01, catppuccino) ne' aggiungere framework o font da CDN.

## Non canonizzato

- Alias legacy `--amber*` e `--cond` (= Inter): residui di compatibilita', non da usare in codice nuovo.
- Righe pre-esistenti con stili inline non ricondotti ai token non sono state riparate.
- Non verificato dal codice: indicazione "dato vecchio" a mezza altezza e "spento barrato" (nessuna regola CSS trovata); transizioni di pagina con View Transitions (nessun uso trovato nel file).

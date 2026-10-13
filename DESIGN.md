---
name: ProxmoxManager
description: Plancia di comando scura per sysadmin: monospace denso, accento ambra, stato sempre leggibile.
colors:
  deck-black: "#0a0b0d"
  panel: "#111318"
  panel-raised: "#181c23"
  panel-high: "#1e2330"
  hairline: "#2a3040"
  hairline-strong: "#3a4558"
  amber: "#f59e0b"
  amber-bright: "#fbbf24"
  amber-wash: "rgba(245,158,11,.12)"
  amber-glow: "rgba(245,158,11,.06)"
  alert-red: "#f05050"
  alert-red-wash: "rgba(239,68,68,.15)"
  ok-green: "#22c55e"
  ok-green-wash: "rgba(34,197,94,.15)"
  info-blue: "#478af7"
  info-blue-wash: "rgba(59,130,246,.15)"
  qemu-cyan: "#06b6d4"
  container-purple: "#ad5ff7"
  text-primary: "#e2e8f0"
  text-secondary: "#94a3b8"
  text-muted: "#76869d"
typography:
  display:
    fontFamily: "'Barlow Condensed', sans-serif"
    fontSize: "40px"
    fontWeight: 900
    lineHeight: 1
  headline:
    fontFamily: "'Barlow Condensed', sans-serif"
    fontSize: "22px"
    fontWeight: 700
    letterSpacing: "0.08em"
  title:
    fontFamily: "'Barlow Condensed', sans-serif"
    fontSize: "17px"
    fontWeight: 700
    letterSpacing: "0.05em"
  body:
    fontFamily: "'JetBrains Mono', monospace"
    fontSize: "13px"
    fontWeight: 400
  table:
    fontFamily: "'JetBrains Mono', monospace"
    fontSize: "12px"
    fontWeight: 400
  label:
    fontFamily: "'JetBrains Mono', monospace"
    fontSize: "9px"
    fontWeight: 500
    letterSpacing: "0.18em"
  button:
    fontFamily: "'JetBrains Mono', monospace"
    fontSize: "11px"
    fontWeight: 600
    letterSpacing: "0.04em"
  meta:
    fontFamily: "'JetBrains Mono', monospace"
    fontSize: "10px"
    fontWeight: 400
  input-touch:
    fontFamily: "'JetBrains Mono', monospace"
    fontSize: "16px"
    fontWeight: 400
rounded:
  badge: "2px"
  control: "3px"
  card: "4px"
  modal: "5px"
  chip: "20px"
spacing:
  xs: "4px"
  sm: "8px"
  md: "14px"
  lg: "22px"
components:
  button-primary:
    backgroundColor: "{colors.amber}"
    textColor: "#000000"
    typography: "{typography.button}"
    rounded: "{rounded.control}"
    padding: "6px 13px"
  button-primary-hover:
    backgroundColor: "{colors.amber-bright}"
  button-ghost:
    backgroundColor: "transparent"
    textColor: "{colors.text-secondary}"
    rounded: "{rounded.control}"
    padding: "6px 13px"
  button-danger:
    backgroundColor: "{colors.alert-red-wash}"
    textColor: "{colors.alert-red}"
    rounded: "{rounded.control}"
    padding: "6px 13px"
  input:
    backgroundColor: "{colors.panel-raised}"
    textColor: "{colors.text-primary}"
    rounded: "{rounded.control}"
    padding: "7px 9px"
  card:
    backgroundColor: "{colors.panel}"
    textColor: "{colors.text-primary}"
    rounded: "{rounded.card}"
    padding: "14px"
  badge-running:
    backgroundColor: "{colors.ok-green-wash}"
    textColor: "{colors.ok-green}"
    typography: "{typography.label}"
    rounded: "{rounded.badge}"
    padding: "2px 6px"
  node-chip:
    backgroundColor: "{colors.panel-raised}"
    textColor: "{colors.text-muted}"
    rounded: "{rounded.chip}"
    padding: "3px 10px"
  modal:
    backgroundColor: "{colors.panel}"
    rounded: "{rounded.modal}"
    padding: "22px"
---

# Design System: ProxmoxManager

## Overview

**Creative North Star: "The Flight Deck"**

Plancia di comando per chi legge lo stato di un'infrastruttura tutto il giorno. Fondo quasi nero, testo monospace, numeri grandi in condensed, un solo accento ambra che dice "qui puoi agire" o "qui sei". Tutto è denso e piccolo: 9-13px, tabelle compatte, etichette in maiuscolo spaziato come sigle di strumento. L'operatore scansiona, non legge.

Il colore ha un lavoro: stato. Verde, rosso, blu, ciano e viola indicano condizione della VM o tipo di risorsa, mai decorazione. L'ambra è l'unica voce di marca ed è anche selezione, hover, focus e azione primaria. La variabile CSS si chiama `--amber` in tutti i temi; negli altri temi cambia solo il valore (verde lime in eva01, pesca in catppuccino).

Il sistema rifiuta l'ornamento: niente ombre decorative, niente gradienti riempitivi, niente icone grandi. Cinque temi (auto, dark, light, eva01, catppuccino) sono parte dell'identità e usano gli stessi nomi di variabile.

**Key Characteristics:**
- Monospace 12-13px per dati, Barlow Condensed per titoli e numeri chiave.
- Etichette 9px in maiuscolo con spaziatura larga (0.13-0.18em).
- Profondità tonale a quattro gradini, bordi 1px, quasi nessuna ombra.
- Un accento (`--amber`) rimappato per tema.
- Angoli piccoli: 2-5px, tranne i chip nodo (pillola).

## Colors

Palette scura a tinta fredda (blu-grigio quasi neutro) con un accento caldo ambra. Il tema dark è il valore canonico in `:root`; gli altri temi sovrascrivono le stesse variabili.

### Primary
- **Amber Signal** (#f59e0b): azione primaria, voce attiva in sidebar, ordinamento tabella, hover di bordi e link, spinner, titolo logo. Unico accento di marca.
- **Amber Bright** (#fbbf24): solo stato hover del pulsante primario.
- **Amber Wash** (rgba(245,158,11,.12)) e **Amber Glow** (rgba(245,158,11,.06)): sfondo di badge/chip attivi e voce di nav attiva.

### Secondary (stato e tipo risorsa)
- **Alert Red** (#f05050): errore, stop, pulsanti distruttivi, barra critica, pallino connessione KO.
- **OK Green** (#22c55e): in esecuzione, successo, barra sana, pallino connessione OK.
- **Info Blue** (#478af7): badge VM prima/pausa e informazioni neutre.

### Tertiary
- **QEMU Cyan** (#06b6d4): badge delle VM QEMU.
- **Container Purple** (#ad5ff7): badge LXC e template, azioni speciali.

### Neutral
- **Deck Black** (#0a0b0d): sfondo pagina.
- **Panel** (#111318): sidebar, topbar, card, modal.
- **Panel Raised** (#181c23): intestazioni tabella, input, blocchi interni.
- **Panel High** (#1e2330): tracce di barre di avanzamento.
- **Hairline** (#2a3040) e **Hairline Strong** (#3a4558): bordi di card/tabelle e bordi di controlli.
- **Text Primary** (#e2e8f0), **Text Secondary** (#94a3b8), **Text Muted** (#76869d): testo, dati secondari, etichette.

### Altri temi (stesse variabili, valori diversi)
- **Light**: sfondo #f8fafc, pannelli #ffffff, testo #0f172a, testo muted #617087. Gli accenti sono scuriti per il testo (ambra #996206, verde #167f3d, rosso #d41212, blu #0b62f0, ciano #047a8f, viola #9530f5); il riempimento del bottone primario resta ambra #f59e0b con testo nero (`--amber-fill`).
- **Eva 01**: sfondo #0d0617, pannelli #1a0f2e; `--amber` diventa verde lime #7cc443; testo #f0e6ff; `--blue` e `--cyan` virano al viola.
- **Catppuccin Mocha**: sfondo #1e1e2e, pannelli #181825; `--amber` diventa pesca #fab387; testo #cdd6f4.
- **Auto** (nessuna classe): segue `prefers-color-scheme`, light o dark.

### Named Rules
**The 4.5 Floor Rule.** Ogni colore usato come testo raggiunge 4.5:1 su `--bg`, `--bg2` e `--bg3` in tutti e cinque i temi, compresi i badge sul loro velo. Un nuovo ruolo colore si verifica prima di entrare nel sistema.
**The Fill Is Not Text Rule.** Dove un colore è sia riempimento sia testo (ambra), i due ruoli hanno variabili separate (`--amber` testo, `--amber-fill` riempimento): così il tema chiaro scurisce il testo senza spegnere il bottone.
**The One Voice Rule.** L'ambra è l'unico accento di marca. Le altre tinte indicano stato o tipo di risorsa e non si usano come decorazione.
**The Semantic Variable Rule.** Il codice usa `--amber`, `--green` ecc. come ruoli. Un tema cambia il valore, non il nome: non scrivere colori letterali nei componenti.

## Typography

**Pesi caricati:** JetBrains Mono variabile 300-700; Barlow Condensed solo 700 e 900 (gli altri pesi non sono usati e non sono incorporati).

**Display / Titoli:** Barlow Condensed (con sans-serif)
**Body / Dati / Label:** JetBrains Mono (con monospace)

**Character:** coppia da strumentazione. La condensed dà peso ai numeri e ai titoli senza occupare spazio; la mono allinea colonne e rende leggibili ID, IP e nomi di nodo. Font incorporati nel binario (subset latin, `/static/fonts/`), con fallback di sistema.

### Hierarchy
- **Display** (900, 40px, 1): valore KPI delle card dashboard.
- **Headline** (700, 22px, 0.08em): titolo logo; valori metrica dei nodi (22px, 700).
- **Title** (700, 17px, 0.05em): titolo topbar, nome nodo (Nodi e Cluster), titolo modal, titolo della console.
- **Section** (700, 13px condensed, 0.1em, maiuscolo): intestazione di sezione con filetto che riempie la riga.
- **Body** (400, 13px): testo base; i controlli e le voci di navigazione usano 12px.
- **Table** (400, 12px): celle; le intestazioni colonna sono label 9px.
- **Label** (500, 9px, 0.13-0.18em, maiuscolo): etichette card, form, intestazioni di colonna, sezioni nav.
- **Button** (600, 11px, 0.04em, maiuscolo): pulsanti; le varianti `.sm` usano 10px.
- **Meta** (400, 10px): sotto-righe e suggerimenti (uptime, core, hint) in `--text3`.
- **Input touch** (400, 16px): solo con puntatore grosso, così iOS non zooma al focus.

### Named Rules
**The Number Is Condensed Rule.** Le metriche grandi sono sempre Barlow Condensed, con cifre tabulari (`tabular-nums`): i valori non ballano quando si aggiornano. Gli identificatori (VMID, IP, nodo) restano in mono.
**The Ramp Is Closed Rule.** Le dimensioni ammesse sono quelle dei ruoli sopra (40, 22, 17, 13, 12, 11, 10, 9). Un valore nuovo si aggiunge qui prima di comparire nel CSS.
**The Small Caps Label Rule.** Ogni etichetta di campo o colonna è 9px, maiuscola e spaziata. Nessuna etichetta in sentence case.

## Layout

Shell a due colonne a tutta altezza (`100vh`, nessuno scroll sul body): sidebar fissa da 220px e area principale con topbar da 48px e contenuto scrollabile (padding 20px 22px). Griglie CSS a 2, 3 o 4 colonne con gap 14px; sotto 1200px le 4 colonne diventano 2, sotto 850px le 3 diventano 2, sotto 650px tutto a 1 colonna. La pagina batch riempie l'altezza residua.

Ritmo compatto: gap 8-16px, padding celle 7px 11px, padding card 14px. La densità è intenzionale; non aumentare il padding per "respirare". Il target è desktop; il mobile è consultazione. Sotto 850px la sidebar diventa un menu a scomparsa e il Batch impila le colonne; sotto 650px le griglie vanno a una colonna. Con puntatore grosso (dito) i controlli salgono a 44px e i campi a 16px; con il mouse il minimo è 24px.

## Elevation & Depth

Tonale, non ombreggiato. La profondità viene da quattro gradini di sfondo (Deck Black → Panel → Panel Raised → Panel High) e da bordi 1px. Le card hanno un filetto in gradiente (1px, trasparente-hairline-trasparente) sul bordo superiore. L'unica ombra è il glow colorato dei pallini di stato connessione. Il modal si stacca con overlay nero al 78% e `backdrop-filter: blur(4px)`.

### Shadow Vocabulary
- **Status Glow** (`box-shadow: 0 0 6px var(--green)` / `var(--red)`): solo pallino connessione.
- **Modal Scrim** (`rgba(0,0,0,.78)` + `blur(4px)`): sfondo dietro i modal; 45% nero in light.

### Named Rules
**The Flat-By-Default Rule.** Le superfici non hanno ombre a riposo. Il glow serve solo a dire "stato vivo".

## Shapes

Angoli quasi vivi: 2px per badge, 3px per pulsanti e input, 4px per card e tabelle, 5px per modal. Eccezione unica: chip selezione nodo a pillola (20px). Bordi 1px sempre; la voce attiva di sidebar e le tab usano un filo 2px sul lato. Barre di avanzamento alte 4px con estremi arrotondati.

## Components

Tutti i controlli sono piccoli, in maiuscolo e senza ornamenti. L'accento ambra compare sull'azione primaria e sugli hover.

### Buttons
- **Shape:** angolo 3px, padding 6px 13px (`.sm`: 3px 9px), mono 11px 600 maiuscolo.
- **Primary (`.ba`):** sfondo ambra, testo nero; hover ambra brillante.
- **Ghost (`.bg2`):** trasparente, bordo hairline-strong; hover: bordo e testo ambra.
- **Danger (`.bd`), Success (`.bsuc`), Special (`.bpurp`):** sfondo wash della tinta, testo e bordo (30%) della tinta; hover wash più intenso.
- **Disabled:** opacità .35, cursore `not-allowed`.

### Badges
- **Style:** angolo 2px, mono 9px 700 maiuscolo, wash + bordo 30% della tinta di stato.
- **Stato:** running = verde, stopped = rosso (anche valore di default), paused = blu.
- **Tipo risorsa:** QEMU = ciano, LXC e template = viola, nodo = ambra.

### Chips
- **Node chip:** pillola 20px, bordo hairline, mono 11px; attivo = ambra wash, bordo e testo ambra, 600.

### Cards / Containers
- **Corner Style:** 4px.
- **Background:** Panel; blocchi interni (metriche, input) su Panel Raised.
- **Border:** 1px hairline, filetto in gradiente in alto.
- **Internal Padding:** 14px.
- **KPI card:** etichetta label, valore display, sotto-riga 10px muted; il valore prende ambra, verde o rosso secondo il significato.

### Inputs / Fields
- **Style:** sfondo Panel Raised, bordo hairline, angolo 3px, mono 12px, padding 7px 9px.
- **Focus:** il bordo diventa ambra, nessun anello esterno.
- **Select:** stesso stile, cursore pointer. Checkbox in selezione nodi usano `accent-color` ambra.

### Navigation
- **Sidebar:** voci 11.5px 500, icona 14px, padding 8px 16px; sezioni in label 9px.
- **Active:** testo ambra, filo sinistro 2px ambra, sfondo ambra glow.
- **Hover:** testo primario con velo bianco al 3% (nero al 4% in light).
- **Tabs:** maiuscolo 11px, sottolineatura 2px ambra per la tab attiva.

### Tables (componente firma)
Intestazione sticky su Panel Raised con label 9px; celle 12px, righe separate da bordo al 45%, hover con velo; colonne ordinabili con freccia ↑/↓ in ambra. Barre di utilizzo 4px verde/ambra/rosso per soglia. I dati restano su una riga (`nowrap`) e il contenitore scorre in orizzontale.

### Toasts e Modal
- **Toast:** Panel Raised, bordo, filo sinistro 3px (verde ok, rosso errore, ambra info); entra da destra in .18s.
- **Modal:** Panel, bordo hairline-strong, 5px, padding 22px, 360-620px; sale di 8px in .18s.

## Do's and Don'ts

### Do:
- **Do** usare le variabili semantiche (`--amber`, `--green`, `--bg2`) in ogni nuovo stile, per far funzionare tutti i temi.
- **Do** tenere etichette in 9px maiuscolo spaziato e numeri grandi in Barlow Condensed.
- **Do** codificare lo stato con la tinta più un testo (badge con parola), non con il solo colore.
- **Do** dare ai controlli distruttivi la variante danger rossa, separata dall'ambra.
- **Do** verificare ogni nuova superficie in tutti e cinque i temi, compresi eva01 e catppuccino.

### Don't:
- **Don't** usare l'ambra come decorazione o su più di una azione primaria per vista.
- **Don't** aggiungere ombre a riposo, gradienti riempitivi o angoli superiori a 5px (eccetto i chip pillola).
- **Don't** scrivere colori esadecimali nei componenti: passano da variabile.
- **Don't** aumentare il padding per rendere l'interfaccia "più ariosa": la densità serve all'operatore.
- **Don't** introdurre un framework JS o un secondo accento di marca.

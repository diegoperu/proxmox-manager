# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

Sysadmin e piccolo team infra (uso interno, Polimi) che gestiscono uno o più cluster Proxmox VE. Operatori esperti, uso frequente, su rete fidata/VPN. Lavorano soprattutto da schermo grande; il mobile serve al massimo per consultazione.

## Product Purpose

Interfaccia web di amministrazione per cluster Proxmox VE. Un binario Go fa da proxy tra browser e API Proxmox (nessun CORS, token solo lato server) e serve una SPA unica. Successo = gli operatori fanno le attività ripetitive (azioni di massa, provisioning, report, setup nel guest) più in fretta e con meno errori che dalla UI nativa Proxmox.

## Positioning

Rispetto alla UI nativa di Proxmox, confermato dall'utente:
- operazioni di massa su più nodi/VM con log in tempo reale;
- provisioning guidato: clone di template cloud-init con IP/DNS/SSH key e delta disco in un solo form;
- multi-cluster in un'unica interfaccia, con report CPU/RAM/rete ed export Excel;
- operazioni dentro il guest via QEMU Guest Agent (aggiunta utenti, aggiunta e montaggio dischi) senza SSH.

## Operating Context

- Browser → proxy Go (`localhost:8080` o host interno) → API Proxmox su HTTPS (certificati self-signed, TLS verify disabilitato per scelta documentata in `SECURITY.md`).
- Pensato per rete locale/VPN fidata, non per esposizione su reti non fidate.
- Autenticazione verso Proxmox solo con API Token; credenziali utente solo per la console seriale (opzionali).
- Storico metriche delegato a Proxmox RRD; SQLite solo come cache API.

## Capabilities and Constraints

- Pagine: dashboard, nodi, VM/LXC, provisioning, batch, cluster, report, impostazioni.
- Frontend: vanilla JS + Chart.js + SheetJS in un singolo `index.html` embeddato nel binario Go. Nessun framework, nessun `localStorage`/`sessionStorage`, nessuna chiamata diretta del browser a Proxmox.
- Dettagli tecnici e regole "cosa NON fare" restano in `CLAUDE.md` (fonte autorevole).
- Distribuzione pubblica: non è l'obiettivo dichiarato; il README e la licenza CC BY-NC 4.0 esistono, ma l'utente ha indicato uso interno/team piccolo come pubblico primario.

## Brand Commitments

Il nome è "ProxmoxManager" (titolo pagina "PROXMOX MANAGER"). Il set di temi è parte dell'identità e deve restare: automatico (segue l'OS), dark, light, eva01 (Evangelion Unit 01) e catppuccino (Catppuccin Mocha).

## Evidence on Hand

- `README.md`, `SECURITY.md`, `CLAUDE.md` nella root.
- SPA esistente in `cmd/server/web/templates/index.html` (~2100 righe).
- Nessun testimonial, cliente o metrica dichiarata: non inventarne.

## Product Principles

1. Velocità dell'operatore prima dell'espressività: densità, scansione rapida e azioni di massa vincono sulla decorazione.
2. Mai nascondere la realtà dell'infrastruttura: stato, errori e log di Proxmox/guest agent sono mostrati chiaramente, non edulcorati.
3. Le azioni distruttive o irreversibili (delete, shutdown, reboot, formattazione dischi) richiedono attenzione esplicita e ambito chiaro.
4. Un solo file, nessuna dipendenza di build: ogni miglioria deve rispettare il vincolo vanilla/single-file.
5. Tutti i temi sono cittadini di prima classe: nessuna feature è finita se rompe un tema.

## Accessibility & Inclusion

Nessuno standard dichiarato (es. WCAG AA) dall'utente. Da decidere: leggibilità e contrasto vanno comunque verificati nei temi scuri colorati (eva01, catppuccino).

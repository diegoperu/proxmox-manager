# Componenti di terze parti incorporati

Questi file sono serviti dal binario (`//go:embed`) sotto `/static/`. Sostituiscono i CDN usati in precedenza: la UI funziona senza accesso a internet.

Per aggiornare un file: scaricare la nuova versione, confrontarne l'hash con una seconda fonte, cambiare nome file (la versione è nel nome), aggiornare `index.html` e questa tabella.

| File | Componente | Licenza | Origine | Verifica | SHA-256 |
|---|---|---|---|---|---|
| `chart-4.4.0.umd.min.js` | Chart.js 4.4.0 | MIT | https://cdnjs.cloudflare.com/ajax/libs/Chart.js/4.4.0/chart.umd.min.js | hash verificato contro cdnjs (sha512 SRI) | `e9b0f875106021fb3d58120ad8ebdd3e7d32135a4452fd8918c72ac6475f2bd3` |
| `xlsx-0.20.3.full.min.js` | SheetJS Community Edition 0.20.3 | Apache-2.0 | https://cdn.sheetjs.com/xlsx-0.20.3/package/dist/xlsx.full.min.js | sito ufficiale SheetJS via TLS; nessuna seconda fonte indipendente | `cc015130aa8521e7f088f88898eba949ccdcbfb38df0bd129b44b7273c3a6f41` |
| `xterm-5.3.0.js` | xterm.js 5.3.0 | MIT | https://cdn.jsdelivr.net/npm/xterm@5.3.0/lib/xterm.js | hash verificato contro jsDelivr (sha256) | `f0aea0f75f48559013ae6643c2479dd737d26da42d5524e6d2b70915ae6523c7` |
| `xterm-5.3.0.css` | xterm.js 5.3.0 (CSS) | MIT | https://cdn.jsdelivr.net/npm/xterm@5.3.0/css/xterm.css | hash verificato contro jsDelivr (sha256) | `832f3f2c603b43ad4351ff04970150cc7a873014276db126a6065c6dd81e4872` |
| `xterm-addon-fit-0.8.0.js` | xterm-addon-fit 0.8.0 | MIT | https://cdn.jsdelivr.net/npm/xterm-addon-fit@0.8.0/lib/xterm-addon-fit.js | hash verificato contro jsDelivr (sha256) | `10f3194c5f17c1786fb7d5db865c1ec8539b6736a318063fd38bdaaf7c46848f` |
| `fonts/jetbrains-mono-latin-var.woff2` | JetBrains Mono (variabile 300-700, subset latin) | OFL-1.1 | https://fonts.googleapis.com/css2 (Google Fonts) | scaricato dal CSS ufficiale con User-Agent Chrome | `18be452724bfdc236c074ca94a249a7f41a86752c7d04ab258ce9ed5651f6a7e` |
| `fonts/barlow-condensed-latin-300.woff2` | Barlow Condensed 300 (subset latin) | OFL-1.1 | https://fonts.googleapis.com/css2 (Google Fonts) | scaricato dal CSS ufficiale con User-Agent Chrome | `8e0eaa0d5d8912e268261f0efc92dec3efb8811200ee35ac29ec757943d45d04` |
| `fonts/barlow-condensed-latin-400.woff2` | Barlow Condensed 400 (subset latin) | OFL-1.1 | https://fonts.googleapis.com/css2 (Google Fonts) | scaricato dal CSS ufficiale con User-Agent Chrome | `7fff1bb22e5773f0d1a55d3093068b6dac4539e8bb3ac23fb9f0a729df2c7bb4` |
| `fonts/barlow-condensed-latin-600.woff2` | Barlow Condensed 600 (subset latin) | OFL-1.1 | https://fonts.googleapis.com/css2 (Google Fonts) | scaricato dal CSS ufficiale con User-Agent Chrome | `215a93c696f442034a46fbb382958f753fda60e30490683aeea6b235fcbb2b66` |
| `fonts/barlow-condensed-latin-700.woff2` | Barlow Condensed 700 (subset latin) | OFL-1.1 | https://fonts.googleapis.com/css2 (Google Fonts) | scaricato dal CSS ufficiale con User-Agent Chrome | `3787a5a419171630e6890cfa47c4da067474d005cd0ff8dc11ec090fdc3ee2b8` |
| `fonts/barlow-condensed-latin-900.woff2` | Barlow Condensed 900 (subset latin) | OFL-1.1 | https://fonts.googleapis.com/css2 (Google Fonts) | scaricato dal CSS ufficiale con User-Agent Chrome | `aa01966de3f608a765a172237a3ffc6161fe4c7e3870b1a904dc6437e54c4e24` |

## Note
- Le intestazioni di copyright e licenza dentro i file `.js` e `.css` sono lasciate intatte.
- **SheetJS**: la versione 0.18.5 usata prima aveva CVE note (CVE-2023-30533, CVE-2024-22363, solo in lettura di file non fidati). Qui si usa 0.20.3, che le corregge. L'app usa SheetJS solo per scrivere l'export Excel.
- **Font**: JetBrains Mono e Barlow Condensed sono sotto SIL Open Font License 1.1, che consente la ridistribuzione con i font stessi.
- Nessun file qui è codice dell'applicazione: il frontend dell'app resta `web/templates/index.html`.

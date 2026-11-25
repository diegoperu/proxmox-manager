#!/bin/bash
# pmgr-snippets-sync.sh - allinea script e snippet cloud-init dal nodo SORGENTE ai nodi di destinazione
# (stesso cluster o altri cluster). ROADMAP 13, decisioni D70-D90. Solo ASCII.
#
# Gira sul nodo sorgente come utente "pmgr-sync" (vedi tools/pmgr-sync/pmgr-sync-setup.sh): usa la chiave
# ssh di quell'utente verso le destinazioni, che accettano soltanto "pmgr-list" e una scrittura rsync senza
# cancellazioni (comando forzato "pmgr-sync-gate"). Dal browser non entra mai contenuto: si copia solo
# cio' che c'e' sulla sorgente.
#
# Uso:
#   pmgr-snippets-sync.sh [--config F] [--json] COMANDO [opzioni]
#     discover                       propone le righe "dest" per i nodi del cluster locale (da /etc/pve/.members)
#     manifest [--profile P]         stampa il manifest (hash, percorso) che verrebbe consegnato
#     status [--dest N]              confronto per destinazione (sola lettura: ssh pmgr-list)
#     diff   [--dest N] [--all]      come status, con lo stato di ogni file (--all anche quelli uguali)
#     run    [--dest N] [--apply] [--missing-only]
#                                    anteprima (predefinito) oppure copia (--apply) e verifica degli hash
#   --missing-only                   copia solo i file mancanti, non sovrascrive quelli diversi
#
# Cosa si consegna (un albero per profilo, D90):
#   snippets/       gli snippet generati dagli script con "pmgr-os.sh --render" per il profilo della
#                   destinazione (standard oppure hosting) + eventuali altri .yaml/.yml della cartella
#                   degli snippet della sorgente
#   pmgr-scripts/   la cartella degli script (solo estensioni ammesse, mai segreti, mai link simbolici; i file
#                   scartati per estensione sono elencati in un avviso)
#   in ciascuno un MANIFEST.sha256 con l'hash di ogni file: la destinazione e' "allineata" solo se ogni
#   hash coincide.
# Regole: mai --delete (un errore sulla sorgente non cancella le copie, D73), una sola operazione alla
# volta (flock), anteprima obbligatoria prima della copia, log senza contenuti.
#
# Configurazione (/etc/pmgr/sync.conf, NON sincronizzata; una riga per voce, # commenti):
#   SSH_KEY=/var/lib/pmgr-sync/.ssh/id_ed25519       (predefinito)
#   KNOWN_HOSTS=/var/lib/pmgr-sync/.ssh/known_hosts  (predefinito; chiavi host accettate al primo contatto,
#                                                     un cambio di chiave poi blocca)
#   SCRIPTS_DIR=/var/lib/vz/pmgr-scripts             (predefinito)
#   SNIPPETS_DIR=/var/lib/vz/snippets                (predefinito)
#   STATE_DIR=/var/lib/pmgr-sync                     (predefinito: lock e registro)
#   dest NOME HOST [profile=standard|hosting] [port=22]
#
# Uscite: 0 tutto allineato (o copia riuscita e verificata), 1 errore, 2 uso errato,
#         3 ci sono differenze (status/diff/anteprima).
set -euo pipefail
export LC_ALL=C

SELF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONF="${PMGR_SYNC_CONF:-/etc/pmgr/sync.conf}"
JSON=0

# ------------------------------------------------------------------ limiti e whitelist
readonly MAX_FILES=500
readonly MAX_FILE_BYTES=1048576
readonly MAX_TOTAL_BYTES=20971520
readonly MAX_DEPTH=6
readonly ABS_RE='^/[A-Za-z0-9._/-]{1,200}$'
readonly NAME_RE='^[a-z0-9][a-z0-9-]{0,31}$'
readonly HOST_RE='^([0-9]{1,3}(\.[0-9]{1,3}){3}|[A-Za-z0-9]([A-Za-z0-9.-]{0,253}[A-Za-z0-9])?)$'
readonly REL_RE='^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$'

die() { printf 'ERRORE: %s\n' "$*" >&2; exit 1; }
usage_err() { printf 'ERRORE: %s\n' "$*" >&2; exit 2; }
# con --json non si stampa testo, tranne quando l'uscita e' catturata nel campo "detail" (CAPTURE=1)
say() { if [ "$JSON" = 1 ] && [ -z "${CAPTURE:-}" ]; then return 0; fi; printf '%s\n' "$*"; }
jesc() { printf '%s' "$1" | tr -c '[:print:]' '?' | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'; }

# estensioni ammesse (D74: niente immagini, ISO, archivi, binari)
ext_ok() {
  case "$1" in
    *.sh | *.yaml | *.yml | *.conf | *.md | *.txt | *.example | *.sha256 | *.service | *.timer | *.local | *.sed) return 0 ;;
    *) return 1 ;;
  esac
}
# nomi che non si consegnano mai (segreti, configurazione locale del cluster)
name_denied() {
  local b
  b="$(basename -- "$1")"
  case "$b" in
    cluster.conf | sync.conf | *.pub | *.key | *.pem | *pass* | *secret* | *token* | MANIFEST.sha256) return 0 ;;
  esac
  return 1
}

# ------------------------------------------------------------------ configurazione
SSH_KEY="" KNOWN_HOSTS="" SCRIPTS_DIR="" SNIPPETS_DIR="" STATE_DIR=""
DEST_NAMES=()
declare -A DEST_HOST=() DEST_PROFILE=() DEST_PORT=()

load_conf() {
  local line key val n=0 name host opt
  { [ -f "$CONF" ] && [ ! -L "$CONF" ] && [ -r "$CONF" ]; } || die "configurazione mancante o non leggibile (o e' un link simbolico): $CONF"
  while IFS= read -r line || [ -n "$line" ]; do
    n=$((n + 1))
    line="${line%%#*}"
    line="$(printf '%s' "$line" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')"
    [ -n "$line" ] || continue
    case "$line" in
      dest\ *)
        read -r -a tok <<<"$line"
        [ "${#tok[@]}" -ge 3 ] || die "$CONF riga $n: 'dest NOME HOST [profile=...] [port=...]'"
        name="${tok[1]}"
        host="${tok[2]}"
        [[ "$name" =~ $NAME_RE ]] || die "$CONF riga $n: nome non valido '$name' (a-z 0-9 -, max 32)"
        [[ "$host" =~ $HOST_RE ]] || die "$CONF riga $n: host non valido '$host'"
        [ -z "${DEST_HOST[$name]:-}" ] || die "$CONF riga $n: destinazione '$name' ripetuta"
        DEST_HOST[$name]="$host"
        DEST_PROFILE[$name]="standard"
        DEST_PORT[$name]="22"
        for opt in "${tok[@]:3}"; do
          case "$opt" in
            profile=standard | profile=hosting) DEST_PROFILE[$name]="${opt#profile=}" ;;
            port=*)
              val="${opt#port=}"
              [[ "$val" =~ ^[0-9]{1,5}$ ]] && [ "$val" -ge 1 ] && [ "$val" -le 65535 ] || die "$CONF riga $n: porta non valida '$val'"
              DEST_PORT[$name]="$val"
              ;;
            *) die "$CONF riga $n: opzione sconosciuta '$opt'" ;;
          esac
        done
        DEST_NAMES+=("$name")
        ;;
      *=*)
        key="${line%%=*}"
        val="${line#*=}"
        [[ "$val" =~ $ABS_RE ]] || die "$CONF riga $n: '$key' deve essere un percorso assoluto semplice"
        [[ "$val" != *..* ]] || die "$CONF riga $n: '$key' non puo' contenere '..'"
        case "$key" in
          SSH_KEY) SSH_KEY="$val" ;;
          KNOWN_HOSTS) KNOWN_HOSTS="$val" ;;
          SCRIPTS_DIR) SCRIPTS_DIR="$val" ;;
          SNIPPETS_DIR) SNIPPETS_DIR="$val" ;;
          STATE_DIR) STATE_DIR="$val" ;;
          *) die "$CONF riga $n: chiave sconosciuta '$key'" ;;
        esac
        ;;
      *) die "$CONF riga $n: riga non riconosciuta" ;;
    esac
  done <"$CONF"
  : "${STATE_DIR:=/var/lib/pmgr-sync}"
  : "${SSH_KEY:=$STATE_DIR/.ssh/id_ed25519}"
  : "${KNOWN_HOSTS:=$STATE_DIR/.ssh/known_hosts}"
  : "${SCRIPTS_DIR:=/var/lib/vz/pmgr-scripts}"
  : "${SNIPPETS_DIR:=/var/lib/vz/snippets}"
  [ "${#DEST_NAMES[@]}" -gt 0 ] || die "$CONF: nessuna riga 'dest' (usa 'discover' per una proposta)"
  [ -d "$SCRIPTS_DIR" ] && [ ! -L "$SCRIPTS_DIR" ] || die "cartella degli script non valida: $SCRIPTS_DIR"
  [ -f "$SCRIPTS_DIR/bin/pmgr-os.sh" ] || die "manca $SCRIPTS_DIR/bin/pmgr-os.sh (serve per generare gli snippet)"
}

# ------------------------------------------------------------------ registro
log_event() {
  local line
  line="$(date -Iseconds) $(id -un) $*"
  { [ -d "$STATE_DIR" ] && [ -w "$STATE_DIR" ] && printf '%s\n' "$line" >>"$STATE_DIR/sync.log"; } 2>/dev/null || true
  logger -t PMGR-SNIPPETS-SYNC -- "$*" 2>/dev/null || true
}

# ------------------------------------------------------------------ alberi da consegnare
STAGE=""
cleanup() { [ -z "$STAGE" ] || rm -rf -- "$STAGE"; }
trap cleanup EXIT

# Copia nell'albero di consegna i file ammessi di SRC, con i controlli: niente link, nomi a whitelist,
# estensioni ammesse, limiti di numero/dimensione/profondita'. Aggiorna i contatori globali.
NFILES=0 NBYTES=0
SKIPPED=()   # file ignorati per estensione non ammessa: si dichiarano, mai in silenzio
stage_copy_tree() { # SRC DEST_TREE
  local src="$1" dst="$2" f rel size mode
  [ -z "$(find "$src" -type l -print -quit)" ] || die "ci sono link simbolici in $src: non li seguo, toglili"
  while IFS= read -r -d '' f; do
    rel="${f#"$src"/}"
    [[ "$rel" =~ $REL_RE ]] && [[ "$rel" != *..* ]] && [[ "$rel" != */.* ]] || die "nome non ammesso in $src: $rel"
    [ "$(printf '%s' "$rel" | tr -cd '/' | wc -c)" -lt "$MAX_DEPTH" ] || die "percorso troppo profondo in $src: $rel"
    name_denied "$rel" && continue
    if ! ext_ok "$rel"; then SKIPPED+=("$rel"); continue; fi
    size="$(stat -c %s -- "$f")"
    [ "$size" -le "$MAX_FILE_BYTES" ] || die "file troppo grande (> $MAX_FILE_BYTES byte): $rel"
    NFILES=$((NFILES + 1))
    NBYTES=$((NBYTES + size))
    [ "$NFILES" -le "$MAX_FILES" ] || die "troppi file (> $MAX_FILES)"
    [ "$NBYTES" -le "$MAX_TOTAL_BYTES" ] || die "dimensione totale oltre $MAX_TOTAL_BYTES byte"
    mode=644
    case "$rel" in *.sh) mode=755 ;; esac
    install -D -m "$mode" -- "$f" "$dst/$rel"
  done < <(find "$src" -type f -print0 | sort -z)
}

# Scrive MANIFEST.sha256 in TREE (hash e percorso relativo) e aggiunge a LIST le righe "hash<TAB>cartella/percorso".
write_manifest() { # TREE NOME LIST
  local tree="$1" name="$2" list="$3" f rel
  (
    cd "$tree"
    while IFS= read -r -d '' f; do
      rel="${f#./}"
      [ "$rel" = MANIFEST.sha256 ] && continue
      printf '%s  %s\n' "$(sha256sum -- "$rel" | cut -d' ' -f1)" "$rel"
    done < <(find . -type f -print0 | sort -z)
  ) >"$tree/MANIFEST.sha256"
  chmod 644 "$tree/MANIFEST.sha256"
  (
    cd "$tree"
    while IFS= read -r -d '' f; do
      rel="${f#./}"
      printf '%s\t%s/%s\n' "$(sha256sum -- "$rel" | cut -d' ' -f1)" "$name" "$rel"
    done < <(find . -type f -print0 | sort -z)
  ) >>"$list"
}

# Costruisce $STAGE/<profilo>/{snippets,pmgr-scripts} e $STAGE/<profilo>.list. Una volta sola per profilo.
declare -A STAGED=()
build_stage() { # PROFILO
  local p="$1" d f
  [ -z "${STAGED[$p]:-}" ] || return 0
  [ -n "$STAGE" ] || { STAGE="$(mktemp -d)"; chmod 700 "$STAGE"; }
  d="$STAGE/$p"
  mkdir -p "$d/snippets" "$d/pmgr-scripts" "$STAGE/render-$p"
  NFILES=0 NBYTES=0
  # snippet: generati dalla sorgente degli script per il profilo (nessun cluster, nessun root)
  bash "$SCRIPTS_DIR/bin/pmgr-os.sh" --render "$STAGE/render-$p" --profile "$p" --all >"$STAGE/render-$p.log" 2>&1 \
    || { tail -n 5 "$STAGE/render-$p.log" >&2; die "generazione degli snippet ($p) non riuscita"; }
  stage_copy_tree "$STAGE/render-$p" "$d/snippets"
  # altri snippet della cartella della sorgente (non generati dagli script): consegnati cosi' come sono
  if [ -d "$SNIPPETS_DIR" ] && [ ! -L "$SNIPPETS_DIR" ]; then
    [ -z "$(find "$SNIPPETS_DIR" -type l -print -quit)" ] || die "ci sono link simbolici in $SNIPPETS_DIR"
    while IFS= read -r -d '' f; do
      f="$(basename -- "$f")"
      [[ "$f" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,99}\.(yaml|yml)$ ]] || continue
      name_denied "$f" && continue
      [ ! -e "$d/snippets/$f" ] || continue
      [ "$(stat -c %s -- "$SNIPPETS_DIR/$f")" -le "$MAX_FILE_BYTES" ] || die "snippet troppo grande: $f"
      NFILES=$((NFILES + 1))
      [ "$NFILES" -le "$MAX_FILES" ] || die "troppi file (> $MAX_FILES)"
      install -D -m 644 -- "$SNIPPETS_DIR/$f" "$d/snippets/$f"
    done < <(find "$SNIPPETS_DIR" -maxdepth 1 -type f -print0 | sort -z)
  fi
  stage_copy_tree "$SCRIPTS_DIR" "$d/pmgr-scripts"
  : >"$STAGE/$p.list"
  write_manifest "$d/snippets" snippets "$STAGE/$p.list"
  write_manifest "$d/pmgr-scripts" pmgr-scripts "$STAGE/$p.list"
  STAGED[$p]=1
  if [ "${#SKIPPED[@]}" -gt 0 ] && [ -z "${WARNED_SKIP:-}" ]; then
    WARNED_SKIP=1
    printf 'ATTENZIONE: %d file NON consegnati (estensione non ammessa): %s\n' "${#SKIPPED[@]}" "$(printf '%s ' "${SKIPPED[@]}" | head -c 400)" >&2
  fi
}

# ------------------------------------------------------------------ trasporto
SSHCMD=()
ssh_base() { # DEST
  local n="$1"
  [ -f "$SSH_KEY" ] && [ -r "$SSH_KEY" ] || die "chiave ssh non leggibile: $SSH_KEY"
  mkdir -p "$(dirname "$KNOWN_HOSTS")" 2>/dev/null || true
  SSHCMD=(ssh -i "$SSH_KEY" -o IdentitiesOnly=yes -o BatchMode=yes -o ConnectTimeout=10 -o ServerAliveInterval=15
    -o StrictHostKeyChecking=accept-new -o "UserKnownHostsFile=$KNOWN_HOSTS" -p "${DEST_PORT[$n]}")
}
rsync_ssh_string() { # DEST -> stringa per rsync -e (nessuno spazio nei valori: percorsi validati)
  local n="$1"
  ssh_base "$n"
  printf '%s ' "${SSHCMD[@]}"
}

# Elenco remoto (hash size mtime percorso) in $1; ritorna 1 se la destinazione non risponde.
remote_list() { # DEST OUTFILE
  local n="$1" out="$2"
  ssh_base "$n"
  timeout 120 "${SSHCMD[@]}" -- "pmgr-sync@${DEST_HOST[$n]}" pmgr-list >"$out" 2>"$out.err" || return 1
  if grep -qx 'TRUNCATED' "$out"; then return 2; fi
  return 0
}

# Confronto: stampa "stato<TAB>percorso" (uguale|diverso|mancante|in_piu). Stage list: hash<TAB>percorso.
compare_lists() { # STAGELIST REMOTELIST
  awk -F'\t' '
    NR == FNR { sh[$2] = $1; next }
    NF >= 4 { rh[$4] = $1 }
    END {
      for (p in sh) { if (!(p in rh)) print "mancante\t" p; else if (rh[p] == sh[p]) print "uguale\t" p; else print "diverso\t" p }
      for (p in rh) if (!(p in sh)) print "in_piu\t" p
    }' "$1" "$2" | sort -t"$(printf '\t')" -k2
}

# ------------------------------------------------------------------ comandi
ONLY=""
ALL=0 APPLY=0 MISSING_ONLY=0 PROFILE_OPT=""

select_dests() {
  SELECTED=()
  local n
  if [ -n "$ONLY" ]; then
    [[ "$ONLY" =~ $NAME_RE ]] || usage_err "--dest: nome non valido"
    [ -n "${DEST_HOST[$ONLY]:-}" ] || usage_err "--dest: destinazione sconosciuta '$ONLY'"
    SELECTED=("$ONLY")
  else
    for n in "${DEST_NAMES[@]}"; do SELECTED+=("$n"); done
  fi
}

count_state() { grep -c "^$2"$'\t' "$1" || true; }

# Esegue il confronto per una destinazione e lascia i risultati in $WORK/<nome>.cmp.
# Ritorna 0 ok, 1 destinazione irraggiungibile, 2 elenco troncato.
do_compare() { # DEST
  local n="$1" rc=0
  build_stage "${DEST_PROFILE[$n]}"
  remote_list "$n" "$WORK/$n.rem" || rc=$?
  [ "$rc" = 0 ] || return "$rc"
  if grep -qx 'INVALID' "$WORK/$n.rem"; then say "  ATTENZIONE $n: nomi non validi sulla destinazione (ignorati)"; fi
  compare_lists "$STAGE/${DEST_PROFILE[$n]}.list" "$WORK/$n.rem" >"$WORK/$n.cmp"
  return 0
}

err_text() { # DEST RC
  case "$2" in
    1) printf 'destinazione non raggiungibile o accesso rifiutato (%s)' "$(head -c 200 "$WORK/$1.rem.err" 2>/dev/null | tr '\n' ' ' | tr -c '[:print:]' '?')" ;;
    2) printf 'elenco remoto troncato: troppi file sulla destinazione' ;;
    *) printf 'errore (%s)' "$2" ;;
  esac
}

cmd_status() { # status|diff
  local mode="$1" n rc same diff miss extra first=1 any=0 err=0 state file st
  select_dests
  [ "$JSON" = 0 ] || printf '{"mode":"%s","dests":[' "$mode"
  for n in "${SELECTED[@]}"; do
    rc=0
    do_compare "$n" || rc=$?
    if [ "$rc" != 0 ]; then
      err=1
      if [ "$JSON" = 1 ]; then
        [ "$first" = 1 ] || printf ','
        printf '{"name":"%s","host":"%s","profile":"%s","state":"error","error":"%s"}' "$n" "$(jesc "${DEST_HOST[$n]}")" "${DEST_PROFILE[$n]}" "$(jesc "$(err_text "$n" "$rc")")"
      else
        printf '%-14s %-18s %-9s ERRORE: %s\n' "$n" "${DEST_HOST[$n]}" "${DEST_PROFILE[$n]}" "$(err_text "$n" "$rc")"
      fi
      first=0
      continue
    fi
    same="$(count_state "$WORK/$n.cmp" uguale)"
    diff="$(count_state "$WORK/$n.cmp" diverso)"
    miss="$(count_state "$WORK/$n.cmp" mancante)"
    extra="$(count_state "$WORK/$n.cmp" in_piu)"
    state=ok
    if [ "$diff" -gt 0 ] || [ "$miss" -gt 0 ]; then state=diff; any=1; fi
    if [ "$JSON" = 1 ]; then
      [ "$first" = 1 ] || printf ','
      printf '{"name":"%s","host":"%s","profile":"%s","state":"%s","same":%s,"different":%s,"missing":%s,"extra":%s' \
        "$n" "$(jesc "${DEST_HOST[$n]}")" "${DEST_PROFILE[$n]}" "$state" "$same" "$diff" "$miss" "$extra"
      if [ "$mode" = diff ]; then
        printf ',"files":['
        local ff=1
        while IFS=$'\t' read -r st file; do
          [ "$ALL" = 1 ] || [ "$st" != uguale ] || continue
          [ "$ff" = 1 ] || printf ','
          printf '{"path":"%s","state":"%s"}' "$(jesc "$file")" "$st"
          ff=0
        done <"$WORK/$n.cmp"
        printf ']'
      fi
      printf '}'
    else
      printf '%-14s %-18s %-9s %s  (uguali %s, diversi %s, mancanti %s, in piu %s)\n' "$n" "${DEST_HOST[$n]}" "${DEST_PROFILE[$n]}" \
        "$([ "$state" = ok ] && echo allineata || echo DA-ALLINEARE)" "$same" "$diff" "$miss" "$extra"
      if [ "$mode" = diff ]; then
        while IFS=$'\t' read -r st file; do
          [ "$ALL" = 1 ] || [ "$st" != uguale ] || continue
          printf '    %-9s %s\n' "$st" "$file"
        done <"$WORK/$n.cmp"
      fi
    fi
    first=0
  done
  [ "$JSON" = 0 ] || printf ']}\n'
  [ "$err" = 0 ] || return 1
  [ "$any" = 0 ] || return 3
  return 0
}

cmd_manifest() {
  local p="${PROFILE_OPT:-standard}"
  case "$p" in standard | hosting) ;; *) usage_err "--profile: standard o hosting" ;; esac
  build_stage "$p"
  [ "$JSON" = 0 ] || usage_err "manifest non ha uscita JSON"
  cut -f1,2 "$STAGE/$p.list" | sed 's/\t/  /'
}

# Copia (o anteprima) per una destinazione. Ritorna 0 ok/allineata, 1 errore, 3 differenze (anteprima).
sync_one() { # DEST
  local n="$1" rc=0 p todo_n diff miss tree dry="" opts rsh
  p="${DEST_PROFILE[$n]}"
  do_compare "$n" || rc=$?
  if [ "$rc" != 0 ]; then say "$n: $(err_text "$n" "$rc")"; log_event "run $n errore"; return 1; fi
  diff="$(count_state "$WORK/$n.cmp" diverso)"
  miss="$(count_state "$WORK/$n.cmp" mancante)"
  todo_n=$((miss + (MISSING_ONLY == 1 ? 0 : diff)))
  say "== $n ($(jesc "${DEST_HOST[$n]}"), profilo $p): da copiare $todo_n (mancanti $miss, diversi $diff$([ "$MISSING_ONLY" = 1 ] && [ "$diff" -gt 0 ] && echo " - NON sovrascritti: --missing-only"))"
  { grep -E '^(mancante|diverso)'$'\t' "$WORK/$n.cmp" || true; } | while IFS=$'\t' read -r st file; do
    if [ "$MISSING_ONLY" = 1 ] && [ "$st" = diverso ]; then continue; fi
    say "     $st  $file"
  done
  if [ "$todo_n" = 0 ]; then say "   gia' allineata"; return 0; fi
  if [ "$APPLY" = 0 ]; then say "   (anteprima: nessuna modifica; ripeti con --apply per copiare)"; return 3; fi
  # copia: due rsync (uno per cartella), senza --delete; -c = confronto per contenuto, -O richiesto dal gate
  # niente -p: rsync proverebbe a cambiare i permessi della cartella radice (di root) e fallirebbe, come con le date
  # (-O). I file nuovi prendono i permessi che stiamo consegnando (755 gli .sh, 644 il resto); -E li mantiene.
  opts=(-rltOEc --no-owner --no-group --chmod=D775,Fgo-w,Fu+w)
  [ "$MISSING_ONLY" = 0 ] || opts+=(--ignore-existing)
  rsh="$(rsync_ssh_string "$n")"
  for tree in snippets pmgr-scripts; do
    if ! timeout 600 rsync "${opts[@]}" -e "$rsh" -- "$STAGE/$p/$tree/" "pmgr-sync@${DEST_HOST[$n]}:$tree/" >"$WORK/$n.rsync.$tree" 2>&1; then
      say "   ERRORE rsync ($tree): $(tail -n 3 "$WORK/$n.rsync.$tree" | tr '\n' ' ' | head -c 300)"
      log_event "run $n errore rsync $tree"
      return 1
    fi
  done
  # verifica: ogni file consegnato deve avere l'hash della sorgente
  rc=0
  do_compare "$n" || rc=$?
  [ "$rc" = 0 ] || { say "   ERRORE nella verifica: $(err_text "$n" "$rc")"; log_event "run $n verifica non riuscita"; return 1; }
  diff="$(count_state "$WORK/$n.cmp" diverso)"
  miss="$(count_state "$WORK/$n.cmp" mancante)"
  if [ "$miss" -gt 0 ] || { [ "$MISSING_ONLY" = 0 ] && [ "$diff" -gt 0 ]; }; then
    say "   ERRORE: dopo la copia restano file diversi ($diff) o mancanti ($miss): la destinazione NON e' allineata"
    log_event "run $n verifica: restano $diff diversi $miss mancanti"
    return 1
  fi
  say "   copiati $todo_n file, hash verificati"
  log_event "run $n copiati $todo_n profilo $p"
  return 0
}

cmd_run() {
  local n rc worst=0 first=1 res
  select_dests
  if [ "$APPLY" = 1 ]; then
    mkdir -p "$STATE_DIR" 2>/dev/null || true
    exec 9>"$STATE_DIR/sync.lock" || die "non riesco a creare il blocco in $STATE_DIR"
    flock -n 9 || die "un'altra sincronizzazione e' in corso"
  fi
  [ "$JSON" = 0 ] || printf '{"mode":"run","apply":%s,"dests":[' "$([ "$APPLY" = 1 ] && echo true || echo false)"
  for n in "${SELECTED[@]}"; do
    rc=0
    if [ "$JSON" = 1 ]; then CAPTURE=1 sync_one "$n" >"$WORK/$n.out" 2>&1 || rc=$?; else sync_one "$n" || rc=$?; fi
    case "$rc" in 0) res=ok ;; 3) res=preview ;; *) res=error ;; esac
    if [ "$JSON" = 1 ]; then
      [ "$first" = 1 ] || printf ','
      printf '{"name":"%s","host":"%s","profile":"%s","result":"%s","detail":"%s"}' "$n" "$(jesc "${DEST_HOST[$n]}")" "${DEST_PROFILE[$n]}" "$res" \
        "$(jesc "$(tr '\n' ' ' <"$WORK/$n.out" | head -c 600)")"
    fi
    first=0
    [ "$rc" = 1 ] && worst=1
    [ "$rc" = 3 ] && [ "$worst" = 0 ] && worst=3
  done
  [ "$JSON" = 0 ] || printf ']}\n'
  return "$worst"
}

# Proposta delle righe "dest" dai membri del cluster locale (nessuna scrittura).
cmd_discover() {
  local f=/etc/pve/.members me line name ip
  [ -r "$f" ] || die "$f non leggibile: non e' un nodo Proxmox (o manca il permesso)"
  me="$(hostname -s)"
  echo "# proposta per $CONF (nodi del cluster locale, profilo standard):"
  tr -d '\n' <"$f" | { grep -oE '"[A-Za-z0-9.-]+": *\{[^{}]*"ip": *"[0-9.]+"[^{}]*\}' || true; } | while IFS= read -r line; do
    name="$(sed -n 's/^"\([A-Za-z0-9.-]*\)".*/\1/p' <<<"$line")"
    ip="$(sed -n 's/.*"ip": *"\([0-9.]*\)".*/\1/p' <<<"$line")"
    [ -n "$name" ] && [ -n "$ip" ] || continue
    [ "$name" != "$me" ] || continue
    [[ "$name" =~ $NAME_RE ]] || name="$(printf '%s' "$name" | tr 'A-Z.' 'a-z-' | cut -c1-32)"
    printf 'dest %s %s profile=standard\n' "$name" "$ip"
  done
}

# ------------------------------------------------------------------ argomenti
CMD=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --config) [ "$#" -ge 2 ] || usage_err "--config vuole un valore"; CONF="$2"; shift 2 ;;
    --json) JSON=1; shift ;;
    --dest) [ "$#" -ge 2 ] || usage_err "--dest vuole un valore"; ONLY="$2"; shift 2 ;;
    --profile) [ "$#" -ge 2 ] || usage_err "--profile vuole un valore"; PROFILE_OPT="$2"; shift 2 ;;
    --all) ALL=1; shift ;;
    --apply) APPLY=1; shift ;;
    --missing-only) MISSING_ONLY=1; shift ;;
    -h | --help) sed -n '2,45p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    status | diff | run | manifest | discover)
      [ -z "$CMD" ] || usage_err "un solo comando alla volta"
      CMD="$1"
      shift
      ;;
    *) usage_err "argomento sconosciuto: $1" ;;
  esac
done
[ -n "$CMD" ] || usage_err "manca il comando (status, diff, run, manifest, discover)"
[[ "$CONF" =~ $ABS_RE ]] || usage_err "--config: percorso assoluto semplice"
{ [ "$APPLY" = 0 ] || [ "$CMD" = run ]; } || usage_err "--apply vale solo per run"
{ [ "$MISSING_ONLY" = 0 ] || [ "$CMD" = run ]; } || usage_err "--missing-only vale solo per run"
{ [ "$ALL" = 0 ] || [ "$CMD" = diff ]; } || usage_err "--all vale solo per diff"

if [ "$CMD" = discover ]; then cmd_discover; exit 0; fi
load_conf
WORK="$(mktemp -d)"
STAGE_PARENT_CLEAN() { rm -rf -- "$WORK"; cleanup; }
trap STAGE_PARENT_CLEAN EXIT
chmod 700 "$WORK"

rc=0
case "$CMD" in
  status) cmd_status status || rc=$? ;;
  diff) cmd_status diff || rc=$? ;;
  run) cmd_run || rc=$? ;;
  manifest) cmd_manifest || rc=$? ;;
esac
exit "$rc"

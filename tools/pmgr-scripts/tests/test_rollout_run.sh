#!/bin/bash
# Test di pmgr-rollout-run.sh (ssh e rollout FINTI: nessuna rete, nessuna VM) e di pmgr-rollout-inventory.sh (manager finto).
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC="$(dirname "$HERE")"
WORK="$(mktemp -d)"
trap 'rm -rf -- "$WORK"; [ -z "${SRVPID:-}" ] || kill "$SRVPID" 2>/dev/null' EXIT
PASS=0; FAIL=0
ok() { PASS=$((PASS + 1)); }
ko() { FAIL=$((FAIL + 1)); printf 'FALLITO: %s\n' "$1"; }
check() { local d="$1"; shift; if "$@" >/dev/null 2>&1; then ok; else ko "$d"; fi; }
RUN="$SRC/bin/pmgr-rollout-run.sh"
mkdir -p "$WORK/bin"

# ssh finto: comportamento per indirizzo da $STUB_SSH (righe "ip comportamento")
cat >"$WORK/bin/ssh" <<'STUB'
#!/bin/bash
target="" ; for a in "$@"; do case "$a" in *@*) target="$a" ;; esac; done
ip="${target#*@}"
echo "$ip" >>"$STUB_SSHLOG"
echo "$*" >>"${STUB_SSHARGS:-/dev/null}"
mode="$(awk -v ip="$ip" '$1 == ip { print $2 }' "$STUB_SSH")"
case "$mode" in
  rootcmd) cat >/dev/null; echo "PMGR_ROOT 4" ;;
  partial) cat >/dev/null; echo "PMGR_ROOT 0"; echo "PMGR_PARTIAL 124" ;;
  ubuntu24) case "$*" in *"bash -s"*) cat >/dev/null; echo "PMGR_ROOT 0" ;; *) echo "PMGR_OS ubuntu 24.04" ;; esac ;;
  ubuntu22) echo "PMGR_OS ubuntu 22.04" ;;
  rocky8) echo "PMGR_OS rocky 8.10" ;;
  nosudo) echo "PMGR_NOSUDO" ;;
  denied) echo "tecnico@$ip: Permission denied (publickey)." >&2; exit 255 ;;
  hostkey) echo "Host key verification failed." >&2; exit 255 ;;
  *) echo "ssh: connect to host $ip port 22: Connection timed out" >&2; exit 255 ;;
esac
STUB
chmod 755 "$WORK/bin/ssh"
# rollout finto: stesso formato di chiamata di pmgr-vm-rollout.sh; comportamento per ultimo argomento (l'IP) da $STUB_ROLL
cat >"$WORK/rollout.sh" <<'STUB'
#!/bin/bash
args=("$@"); ip="${args[$((${#args[@]} - 1))]}"
echo "$*" >>"$STUB_ROLLLOG"
mode="$(awk -v ip="$ip" '$1 == ip { print $2 }' "$STUB_ROLL")"
mod=""; verify=0; check=0
for ((i = 0; i < ${#args[@]}; i++)); do
  case "${args[$i]}" in --module) mod="${args[$((i + 1))]}" ;; --verify) verify=1 ;; --check) check=1 ;; esac
done
if [ "$verify" = 1 ]; then
  case "$mode" in verfail) echo "  [FALLITO]  modulo fail2ban: NON conforme"; echo "Verifica: 1 controlli falliti."; exit 1 ;; reboot) echo "  [INFO]     riavvio necessario: SI (kernel)"; echo "Verifica: tutto conforme."; exit 0 ;; *) echo "Verifica: tutto conforme."; exit 0 ;; esac
fi
if [ "$check" = 1 ]; then case "$mode" in need) echo "  [da fare] scrivere x"; exit 3 ;; err) exit 1 ;; *) exit 0 ;; esac; fi
case "$mode" in modfail:"$mod") echo "boom"; exit 1 ;; esac
exit 0
STUB
export PATH="$WORK/bin:$PATH" STUB_SSH="$WORK/ssh.map" STUB_SSHLOG="$WORK/ssh.log" STUB_ROLL="$WORK/roll.map" STUB_ROLLLOG="$WORK/roll.log" PMGR_ROLLOUT_BIN="$WORK/rollout.sh"
run() { bash "$RUN" "$@" >"$WORK/out" 2>&1; RC=$?; }

printf '10.0.0.1\n10.0.0.2\n10.0.0.3\n10.0.0.4\n10.0.0.5\n' >"$WORK/hosts5"
printf '10.0.0.1 ubuntu24\n10.0.0.2 rocky8\n10.0.0.3 ubuntu24\n10.0.0.4 nosudo\n10.0.0.5 ubuntu22\n' >"$WORK/ssh.map"

# ---- porta SSH diversa dalla 22
printf '10.0.0.1\n10.0.0.2\n' >"$WORK/hosts2"
: >"$WORK/ssh.args"; export STUB_SSHARGS="$WORK/ssh.args"
run --hosts "$WORK/hosts2" --user tecnico --state "$WORK/sp0" preflight
check "senza --ssh-port non si passa nessuna porta" bash -c "! grep -q 'Port=' '$WORK/ssh.args' && [ \$(wc -l <'$WORK/ssh.args') = 2 ]"
: >"$WORK/ssh.args"
run --hosts "$WORK/hosts2" --user tecnico --state "$WORK/sp1" --ssh-port 2022 preflight
check "--ssh-port 2022: ogni connessione usa la porta" bash -c "[ \$(grep -c 'Port=2022' '$WORK/ssh.args') = 2 ]"
for badp in 0 70000 22a -1 '' ' 22'; do
  run --hosts "$WORK/hosts2" --user tecnico --state "$WORK/sp2" --ssh-port "$badp" preflight
  [ "$RC" = 2 ] && ok || ko "porta non valida '$badp' accettata (uscita $RC)"
done
unset STUB_SSHARGS

# ---- preflight
: >"$WORK/ssh.log"
run --hosts "$WORK/hosts5" --user tecnico --state "$WORK/s1" preflight
[ "$RC" = 1 ] && ok || ko "preflight con una VM senza sudo: uscita $RC (attesa 1)"
check "preflight: elenca ubuntu 24.04 x2" grep -q 'ubuntu 24.04  *2' "$WORK/out"
check "preflight: segnala la VM senza sudo" grep -q '\[SUDO\].*10.0.0.4' "$WORK/out"
check "preflight: una sola connessione per VM" [ "$(wc -l <"$WORK/ssh.log")" = 5 ]
check "preflight: os.tsv scritto" grep -q $'10.0.0.2\trocky\t8.10' "$WORK/s1/os.tsv"
# interruttore: 3 rifiuti di fila fermano tutto
printf '10.1.0.1 denied\n10.1.0.2 denied\n10.1.0.3 denied\n10.1.0.4 ubuntu24\n10.1.0.5 ubuntu24\n' >"$WORK/ssh.map"
printf '10.1.0.1\n10.1.0.2\n10.1.0.3\n10.1.0.4\n10.1.0.5\n' >"$WORK/hosts_den"
: >"$WORK/ssh.log"
run --hosts "$WORK/hosts_den" --user tecnico --state "$WORK/s2" preflight
[ "$RC" = 1 ] && grep -q 'STOP: 3 VM di fila' "$WORK/out" && ok || ko "3 rifiuti di fila devono fermare il preflight (uscita $RC)"
check "dopo lo STOP le altre VM non vengono contattate" [ "$(wc -l <"$WORK/ssh.log")" = 3 ]
# rifiuti non consecutivi non fermano; hostkey e rete non contano
printf '10.2.0.1 denied\n10.2.0.2 ubuntu24\n10.2.0.3 denied\n10.2.0.4 hostkey\n10.2.0.5 denied\n10.2.0.6 offline\n10.2.0.7 ubuntu24\n' >"$WORK/ssh.map"
printf '10.2.0.1\n10.2.0.2\n10.2.0.3\n10.2.0.4\n10.2.0.5\n10.2.0.6\n10.2.0.7\n' >"$WORK/hosts_mix"
run --hosts "$WORK/hosts_mix" --user tecnico --state "$WORK/s3" preflight
check "rifiuti non consecutivi: si arriva in fondo" grep -q 'Preflight: 2 pronte, 5 con problemi su 7' "$WORK/out"
check "chiave host sconosciuta segnalata" grep -q '\[HOSTKEY\].*10.2.0.4' "$WORK/out"
check "VM irraggiungibile segnalata" grep -q '\[NO RETE\].*10.2.0.6' "$WORK/out"

# ---- rootcheck
printf '10.3.0.1 ubuntu24\n10.3.0.2 rootcmd\n10.3.0.3 offline\n10.3.0.4 partial\n' >"$WORK/ssh.map"
printf '10.3.0.1\n10.3.0.2\n10.3.0.3\n10.3.0.4\n' >"$WORK/hosts_rc"
run --hosts "$WORK/hosts_rc" --user tecnico --state "$WORK/s10" rootcheck
[ "$RC" = 1 ] && ok || ko "rootcheck con accessi come root: uscita $RC (attesa 1)"
check "rootcheck: segnala la VM con accessi come root" grep -q '\[ROOT\].*10.3.0.2.*4 accessi' "$WORK/out"
check "rootcheck: VM senza accessi = ok" grep -q '\[ok\].*10.3.0.1' "$WORK/out"
check "rootcheck: una VM irraggiungibile e' un errore, non un ok" grep -q '\[ERRORE\].*10.3.0.3' "$WORK/out"
check "rootcheck: registro letto solo in parte = PARZIALE, non ok" grep -q '\[PARZIALE\].*10.3.0.4' "$WORK/out"
check "rootcheck: riepilogo" grep -q 'Rootcheck: 1 VM con accessi come root, 2 in errore, su 4' "$WORK/out"

# ---- verify e check
printf '10.0.0.1 ubuntu24\n10.0.0.2 rocky8\n10.0.0.3 ubuntu24\n10.0.0.4 ubuntu24\n10.0.0.5 ubuntu22\n' >"$WORK/ssh.map"
printf '10.0.0.1 ok\n10.0.0.2 verfail\n10.0.0.3 reboot\n10.0.0.4 ok\n10.0.0.5 ok\n' >"$WORK/roll.map"
: >"$WORK/roll.log"
run --hosts "$WORK/hosts5" --user tecnico --state "$WORK/s1" --profile standard verify
check "verify: 4 conformi e 1 da fare" grep -q 'Verifica: 4 conformi, 1 da sistemare' "$WORK/out"
check "verify: usa il profilo scelto" grep -q -- '--verify --profile standard --user tecnico 10.0.0.1' "$WORK/roll.log"
check "verify: non scrive (nessun --module)" bash -c "! grep -q -- '--module' '$WORK/roll.log'"
check "verify: il riavvio necessario e' nel registro" grep -qP 'verify\t10.0.0.3\tok\triavvio necessario' "$WORK/s1/results.tsv"
printf '10.0.0.1 need\n10.0.0.2 ok\n10.0.0.3 err\n10.0.0.4 ok\n10.0.0.5 ok\n' >"$WORK/roll.map"
: >"$WORK/roll.log"
run --hosts "$WORK/hosts5" --user tecnico --state "$WORK/s1" check
check "check: 3 a posto, 1 da aggiornare, 1 errore" grep -q "Controllo: 3 gia' a posto, 1 da aggiornare, 1 in errore" "$WORK/out"
check "check: passa --check e i moduli del profilo" grep -q -- '--module baseline --module ssh-standard --module agent-exec --check --user tecnico 10.0.0.1' "$WORK/roll.log"

# ---- apply: serve --yes
run --hosts "$WORK/hosts5" --user tecnico --state "$WORK/s4" apply
[ "$RC" = 2 ] && grep -q 'SCRIVE sulle VM' "$WORK/out" && ok || ko "apply senza --yes deve rifiutare (uscita $RC)"
run --hosts "$WORK/hosts5" --user tecnico --state "$WORK/s4" canary
[ "$RC" = 2 ] && ok || ko "canary senza --yes deve rifiutare (uscita $RC)"
check "nessuna chiamata di scrittura senza --yes" bash -c "! grep -q 'module' '$WORK/roll.log' || true"

# ---- canary: una VM per sistema operativo (dal preflight)
printf '10.0.0.1 ok\n10.0.0.2 ok\n10.0.0.3 ok\n10.0.0.4 ok\n10.0.0.5 ok\n' >"$WORK/roll.map"
printf '10.0.0.1 ubuntu24\n10.0.0.2 rocky8\n10.0.0.3 ubuntu24\n10.0.0.4 ubuntu24\n10.0.0.5 ubuntu22\n' >"$WORK/ssh.map"
run --hosts "$WORK/hosts5" --user tecnico --state "$WORK/s5" preflight
: >"$WORK/roll.log"
run --hosts "$WORK/hosts5" --user tecnico --state "$WORK/s5" --yes canary
[ "$RC" = 0 ] && ok || ko "canary: uscita $RC"
check "canary: 3 VM (ubuntu 24.04, rocky 8.10, ubuntu 22.04)" grep -q 'Canarini: 3 VM' "$WORK/out"
check "canary: la prima VM per sistema, non le altre" bash -c "grep -q -- '--module baseline --user tecnico 10.0.0.1' '$WORK/roll.log' && ! grep -q -- '--module baseline --user tecnico 10.0.0.3' '$WORK/roll.log'"
check "canary: ogni VM e' verificata dopo i moduli" bash -c "grep -c -- '--verify' '$WORK/roll.log' | grep -qx 3"
# i moduli in ordine
check "moduli nell'ordine: baseline poi ssh-standard" bash -c "grep -n -- '10.0.0.1' '$WORK/roll.log' | head -2 | tr '\n' ' ' | grep -q 'baseline.*ssh-standard'"

# ---- apply a ondate, ripresa, stop al primo problema
printf '10.0.0.1 ok\n10.0.0.2 ok\n10.0.0.3 ok\n10.0.0.4 modfail:ssh-standard\n10.0.0.5 ok\n' >"$WORK/roll.map"
: >"$WORK/roll.log"
run --hosts "$WORK/hosts5" --user tecnico --state "$WORK/s5" --yes --wave 2 --pause 0 apply </dev/null
[ "$RC" = 1 ] && ok || ko "apply con un modulo che fallisce: uscita $RC (attesa 1)"
check "apply: le VM dei canarini non si rifanno" bash -c "! grep -q -- '10.0.0.1\$' '$WORK/roll.log' || ! grep -q -- 'baseline.*10.0.0.1' '$WORK/roll.log'"
check "apply: si ferma sulla VM che fallisce" grep -q 'STOP: 10.0.0.4' "$WORK/out"
check "apply: la VM dopo quella che fallisce non viene toccata" bash -c "! grep -q '10.0.0.5' '$WORK/roll.log'"
check "apply: il fallimento e' nel registro" grep -qP 'apply\t10.0.0.4\tmodulo_fallito\tssh-standard' "$WORK/s5/results.tsv"
# riparazione e ripresa
printf '10.0.0.1 ok\n10.0.0.2 ok\n10.0.0.3 ok\n10.0.0.4 ok\n10.0.0.5 ok\n' >"$WORK/roll.map"
: >"$WORK/roll.log"
run --hosts "$WORK/hosts5" --user tecnico --state "$WORK/s5" --yes --wave 2 --pause 0 apply </dev/null
[ "$RC" = 0 ] && ok || ko "ripresa dopo la riparazione: uscita $RC"
check "ripresa: tocca solo la VM non ancora riuscita (la 4, tre moduli)" bash -c "[ \$(grep -c -- '--module' '$WORK/roll.log') = 3 ] && grep -q -- '--module.*10.0.0.4' '$WORK/roll.log' && ! grep -q -- '--module.*10.0.0.[1235]' '$WORK/roll.log'"
check "ripresa: dice quante erano gia' fatte (3 canarini + la 3)" grep -q "4 gia' fatte in precedenza" "$WORK/out"
run --hosts "$WORK/hosts5" --user tecnico --state "$WORK/s5" --yes apply </dev/null
check "terzo giro: niente da fare" grep -q "0 riuscite, 0 con problemi, 5 gia' fatte" "$WORK/out"

# ---- --max-waves: si ferma da solo dopo N ondate
printf '10.0.0.1 ok\n10.0.0.2 ok\n10.0.0.3 ok\n10.0.0.4 ok\n10.0.0.5 ok\n' >"$WORK/roll.map"; : >"$WORK/roll.log"
run --hosts "$WORK/hosts5" --user tecnico --state "$WORK/s11" --yes --wave 2 --max-waves 1 apply </dev/null
[ "$RC" = 0 ] && grep -q 'Fermo dopo 1 ondate' "$WORK/out" && ok || ko "--max-waves 1: uscita $RC"
check "--max-waves 1: toccate solo 2 VM" bash -c "[ \$(grep -c -- '--verify' '$WORK/roll.log') = 2 ] && ! grep -q '10.0.0.3' '$WORK/roll.log'"
: >"$WORK/roll.log"
run --hosts "$WORK/hosts5" --user tecnico --state "$WORK/s11" --yes --wave 2 --max-waves 1 apply </dev/null
check "ripresa dopo --max-waves: le prossime 2 (la 3 e la 4)" bash -c "grep -q '10.0.0.3' '$WORK/roll.log' && grep -q '10.0.0.4' '$WORK/roll.log' && ! grep -q -- '--module.*10.0.0.1' '$WORK/roll.log' && ! grep -q '10.0.0.5' '$WORK/roll.log'"
bad_arg() { run "$@"; [ "$RC" = 2 ] && ok || ko "argomento ostile accettato: $* (uscita $RC)"; }
bad_arg --hosts "$WORK/hosts2" --user tecnico --state "$WORK/sx" --max-waves x verify

# ---- --modules scelto a mano: si verificano solo quei moduli, non l'intero profilo
printf '10.0.0.1\n10.0.0.2\n' >"$WORK/hosts2"
printf '10.0.0.1 ok\n10.0.0.2 ok\n' >"$WORK/roll.map"; : >"$WORK/roll.log"
run --hosts "$WORK/hosts2" --user tecnico --state "$WORK/s12" --modules agent-exec --yes apply </dev/null
[ "$RC" = 0 ] && ok || ko "moduli scelti: uscita $RC"
check "moduli scelti: nessuna verifica completa" bash -c "! grep -q -- '--verify' '$WORK/roll.log'"
check "moduli scelti: controllati solo i moduli applicati (--check)" bash -c "grep -q -- '--module agent-exec --check' '$WORK/roll.log'"
check "moduli scelti: le VM risultano fatte" grep -q '\[fatto\].*10.0.0.1' "$WORK/out"
printf '10.0.0.1 need\n10.0.0.2 ok\n' >"$WORK/roll.map"
run --hosts "$WORK/hosts2" --user tecnico --state "$WORK/s14" --modules agent-exec --yes apply </dev/null
[ "$RC" = 1 ] && grep -q '\[VERIFICA\].*10.0.0.1' "$WORK/out" && ok || ko "moduli scelti: un --check che chiede ancora modifiche deve fermare (uscita $RC)"
printf '10.0.0.1 verfail\n10.0.0.2 ok\n' >"$WORK/roll.map"
run --hosts "$WORK/hosts2" --user tecnico --state "$WORK/s13" --yes apply </dev/null
[ "$RC" = 1 ] && ok || ko "senza --modules la verifica completa continua a fermare (uscita $RC)"

# ---- verifica fallita dopo i moduli
printf '10.0.0.1 verfail\n10.0.0.2 ok\n' >"$WORK/roll.map"
printf '10.0.0.1\n10.0.0.2\n' >"$WORK/hosts2"; : >"$WORK/roll.log"
run --hosts "$WORK/hosts2" --user tecnico --state "$WORK/s6" --yes apply </dev/null
[ "$RC" = 1 ] && grep -q '\[VERIFICA\]' "$WORK/out" && ok || ko "verifica fallita dopo i moduli deve fermare (uscita $RC)"
check "verifica fallita: la seconda VM non viene toccata" bash -c "! grep -q '10.0.0.2' '$WORK/roll.log' || ! grep -q 'module.*10.0.0.2' '$WORK/roll.log'"
: >"$WORK/roll.log"
run --hosts "$WORK/hosts2" --user tecnico --state "$WORK/s7" --yes --continue-on-fail apply </dev/null
check "--continue-on-fail: si va avanti alla seconda VM" bash -c "grep -q 'module.*10.0.0.2' '$WORK/roll.log'"

# ---- argomenti e file ostili: errore 2 senza chiamate
: >"$WORK/ssh.log"; : >"$WORK/roll.log"
bad() { run "$@"; [ "$RC" = 2 ] && ok || ko "argomenti ostili accettati: $* (uscita $RC)"; }
printf '10.0.0.1;id\n' >"$WORK/h_bad1"; bad --hosts "$WORK/h_bad1" --user tecnico --state "$WORK/sx" verify
printf -- '-oProxyCommand=id\n' >"$WORK/h_bad2"; bad --hosts "$WORK/h_bad2" --user tecnico --state "$WORK/sx" verify
printf '999.1.1.1\n' >"$WORK/h_bad3"; bad --hosts "$WORK/h_bad3" --user tecnico --state "$WORK/sx" verify
printf 'host.example.org\n' >"$WORK/h_bad4"; bad --hosts "$WORK/h_bad4" --user tecnico --state "$WORK/sx" verify
bad --hosts "$WORK/hosts2" --user 'a;b' --state "$WORK/sx" verify
bad --hosts "$WORK/hosts2" --user '-oProxyCommand=x' --state "$WORK/sx" verify
bad --hosts "$WORK/hosts2" --user tecnico --state "/tmp/x y" verify
bad --hosts "$WORK/hosts2" --user tecnico --state "$WORK/sx" --profile root verify
bad --hosts "$WORK/hosts2" --user tecnico --state "$WORK/sx" --modules "ssh-standard;id" verify
bad --hosts "$WORK/hosts2" --user tecnico --state "$WORK/sx" --modules "ssh-hosting" --profile standard --yes apply
bad --hosts "$WORK/hosts2" --user tecnico --state "$WORK/sx" --profile hosting --modules "ssh-standard" --yes apply
bad --hosts "$WORK/hosts2" --user tecnico --state "$WORK/sx" --wave 0 verify
bad --hosts "$WORK/hosts2" --user tecnico --state "$WORK/sx" --wave x verify
bad --hosts "$WORK/hosts2" --user tecnico --state "$WORK/sx" --bogus verify
bad --hosts "$WORK/nonesiste" --user tecnico --state "$WORK/sx" verify
bad --hosts "$WORK/hosts2" --user tecnico --state "$WORK/sx" verify preflight
bad --hosts "$WORK/hosts2" --user tecnico --state "$WORK/sx"
check "argomenti ostili: nessuna connessione" bash -c "[ ! -s '$WORK/ssh.log' ] && [ ! -s '$WORK/roll.log' ]"
# limit, exclude, duplicati
printf '10.0.0.1\n10.0.0.1\n# commento\n\n10.0.0.2 # inline\n10.0.0.3\n' >"$WORK/h_dup"; printf '10.0.0.2\n' >"$WORK/excl"
printf '10.0.0.1 ok\n10.0.0.2 ok\n10.0.0.3 ok\n' >"$WORK/roll.map"; : >"$WORK/roll.log"
run --hosts "$WORK/h_dup" --user tecnico --state "$WORK/s8" --exclude "$WORK/excl" verify
check "duplicati, commenti ed esclusioni: 2 VM verificate (1 e 3)" bash -c "[ \$(grep -c -- '--verify' '$WORK/roll.log') = 2 ] && grep -q '10.0.0.3' '$WORK/roll.log' && ! grep -q '10.0.0.2' '$WORK/roll.log'"
: >"$WORK/roll.log"
run --hosts "$WORK/h_dup" --user tecnico --state "$WORK/s9" --limit 1 verify
check "--limit 1: una sola VM" bash -c "[ \$(grep -c -- '--verify' '$WORK/roll.log') = 1 ]"
# due esecuzioni insieme sullo stesso stato
( exec 9>"$WORK/s8/lock"; flock 9; sleep 4 ) &
sleep 1
run --hosts "$WORK/h_dup" --user tecnico --state "$WORK/s8" verify
[ "$RC" = 1 ] && grep -q "un'altra esecuzione" "$WORK/out" && ok || ko "due esecuzioni sullo stesso stato devono escludersi (uscita $RC)"
wait
# report
run --state "$WORK/s5" report
check "report: elenca le fasi" grep -q 'apply  *ok' "$WORK/out"
run --state "$WORK/s6" report
check "report: elenca le VM non riuscite" grep -q '10.0.0.1  *verifica_fallita' "$WORK/out"

# ---- inventario con un manager finto
cat >"$WORK/fake_mgr.py" <<'PYEND'
import json, http.server, sys
class H(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def do_GET(self):
        if self.path == "/api/clusters":
            body = [{"label": "ALTRO"}, {"label": "CALCOLO"}]
        elif self.path.startswith("/api/vms?cluster=1"):
            vm = lambda **k: dict(dict(template=0, status="running", ostype="l26", gpus=[], tags="ubuntu-template;24.04", ip=""), **k)
            body = [{"node": "n1", "vms": [
                vm(vmid=100, name="web-1", ip="192.0.2.10"),
                vm(vmid=101, name="gpu-1", ip="192.0.2.11", gpus=[{"name": "A100"}]),
                vm(vmid=102, name="dhcp", ip=""),
                vm(vmid=103, name="spenta", status="stopped", ip="192.0.2.13"),
                vm(vmid=104, name="strana; rm -rf /", ip="192.0.2.14", ostype=""),
                vm(vmid=105, name="dup", ip="192.0.2.10"),
                vm(vmid=106, name="ipbrutto", ip="192.0.2.999"),
                vm(vmid=9000, name="tmpl", template=1, ip="192.0.2.250"),
                vm(vmid=107, name="rocky-1", ip="192.0.2.17", tags="rocky-template;8"),
            ]}]
        else:
            self.send_response(404); self.end_headers(); return
        data = json.dumps(body).encode()
        self.send_response(200); self.send_header("Content-Type", "application/json"); self.end_headers(); self.wfile.write(data)
http.server.HTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
PYEND
PORT=$((20000 + RANDOM % 10000))
python3 "$WORK/fake_mgr.py" "$PORT" & SRVPID=$!
sleep 1
bash "$SRC/bin/pmgr-rollout-inventory.sh" --cluster CALCOLO --manager "http://127.0.0.1:$PORT" --out "$WORK/inv" >"$WORK/out" 2>&1; RC=$?
[ "$RC" = 0 ] && ok || ko "inventario: uscita $RC: $(tail -2 "$WORK/out")"
check "target: web-1, la ostype-ignota e rocky-1 (3), non la GPU, la spenta, il template, il doppione, l'IP brutto" bash -c "[ \"\$(sort '$WORK/inv/target.txt' | tr '\n' ' ')\" = '192.0.2.10 192.0.2.14 192.0.2.17 ' ]"
check "gpu.txt: solo la VM con scheda" bash -c "[ \"\$(cat '$WORK/inv/gpu.txt')\" = 192.0.2.11 ]"
check "senza_ip: la DHCP, l'IP brutto e il doppione" bash -c "[ \$(wc -l <'$WORK/inv/senza_ip.txt') = 3 ]"
check "ferme.txt: la spenta" grep -q spenta "$WORK/inv/ferme.txt"
check "ostype_ignoto.txt: la VM col nome ostile (nome ripulito)" bash -c "grep -q 'strana' '$WORK/inv/ostype_ignoto.txt' && ! grep -q ';' '$WORK/inv/ostype_ignoto.txt'"
check "inventario.tsv: famiglia dai tag" grep -q $'rocky-1\tn1\trunning\t192.0.2.17\t0\tl26\trocky' "$WORK/inv/inventario.tsv"
check "la cartella dell'inventario e' 0700" bash -c "[ \"\$(stat -c %a '$WORK/inv')\" = 700 ]"
bash "$SRC/bin/pmgr-rollout-inventory.sh" --cluster NONESISTE --manager "http://127.0.0.1:$PORT" --out "$WORK/inv2" >"$WORK/out" 2>&1; [ "$?" != 0 ] && ok || ko "cluster inesistente deve fallire"
bash "$SRC/bin/pmgr-rollout-inventory.sh" --cluster 'A;b' --manager "http://127.0.0.1:$PORT" >"$WORK/out" 2>&1; [ "$?" = 2 ] && ok || ko "nome cluster ostile accettato"
bash "$SRC/bin/pmgr-rollout-inventory.sh" --cluster CALCOLO --manager 'http://x;id' >"$WORK/out" 2>&1; [ "$?" = 2 ] && ok || ko "URL ostile accettato"
bash -n "$RUN" && bash -n "$SRC/bin/pmgr-rollout-inventory.sh" && ok || ko "sintassi"
printf 'Test rollout a ondate e inventario: %d riusciti, %d falliti\n' "$PASS" "$FAIL"
[ "$FAIL" = 0 ]

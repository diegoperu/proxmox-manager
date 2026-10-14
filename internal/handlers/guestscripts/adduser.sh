#!/bin/sh
# ProxmoxManager — crea un utente OS dentro una VM via QEMU Guest Agent.
#
# Sorgente unica (`//go:embed` in handlers): usato in due modi, stesso testo.
#   - helper etichettato: /usr/local/libexec/pmgr/adduser.sh <utente> <chiave>
#     (su SELinux enforcing gira in virt_qemu_ga_unconfined_t, dominio non confinato)
#   - fallback inline:     sh -c "<questo testo>" pmgr <utente> <chiave>
#     (guest senza helper: Debian/Ubuntu, o Rocky non preparato — qui useradd fallisce
#      sotto SELinux, è il motivo per cui esiste l'helper)
#
# I valori della richiesta arrivano SOLO come argomenti posizionali ($1, $2): mai
# interpolati nel testo, quindi una chiave con apici/metacaratteri non esegue comandi.
# $1 = username (già validato dal server), $2 = chiave pubblica o vuoto.
set -eu
umask 077

# Evita l'interrogazione D-Bus a systemd (NoSuchDynamicUser) a ogni lookup di un nome
# inesistente: nel dominio non confinato dell'agent SELinux nega la risposta e ogni
# lookup aspetta il timeout D-Bus di 25 s (useradd da solo arrivava a oltre 2 minuti).
# Innocuo sui guest senza SELinux: disabilita solo il modulo NSS degli utenti dinamici.
export SYSTEMD_NSS_DYNAMIC_BYPASS=1

U="${1:?username mancante}"
KEY="${2:-}"

# Utente già esistente: exit 3, nessuna modifica. Così una richiesta non può resettare
# la password, aggiungere chiavi o dare sudo a un account preesistente (root incluso).
if id "$U" >/dev/null 2>&1; then
    echo "l'utente $U esiste già nella VM: nessuna modifica" >&2
    exit 3
fi

useradd -m -s /bin/bash "$U"

# Gruppo amministrativo: wheel su RHEL/Rocky, sudo su Debian/Ubuntu. Si aggiunge quello
# che esiste davvero, senza ingoiare l'errore di un gruppo assente.
for g in wheel sudo; do
    if getent group "$g" >/dev/null 2>&1; then
        usermod -aG "$g" "$U"
    fi
done

HOME_DIR=$(getent passwd "$U" | cut -d: -f6)

if [ -z "$KEY" ]; then
    # Password fissa iniziale, da cambiare al primo accesso (decisione di progetto).
    printf '%s:%s\n' "$U" "p4ssw0rd" | chpasswd
    if ! chage -d 0 "$U"; then
        # Se non si riesce a forzare il cambio, l'account NON deve restare usabile con
        # una password fissa nota: lo si blocca.
        if usermod -L "$U"; then
            echo "impossibile forzare il cambio password al primo accesso: account $U bloccato" >&2
        else
            echo "ATTENZIONE: impossibile forzare il cambio password e impossibile bloccare l'account $U" >&2
        fi
        exit 1
    fi
else
    install -d -m 700 -o "$U" -g "$U" "$HOME_DIR/.ssh"
    # Una sola riga (printf >, non >>): niente duplicati su chiamate ripetute.
    printf '%s\n' "$KEY" > "$HOME_DIR/.ssh/authorized_keys"
    chmod 600 "$HOME_DIR/.ssh/authorized_keys"
    chown "$U:$U" "$HOME_DIR/.ssh/authorized_keys"
    # Su SELinux la home appena creata può avere l'etichetta sbagliata (user_home_dir_t):
    # senza ssh_home_t sshd rifiuta la chiave. restorecon la corregge; assente il comando
    # (guest senza SELinux) non è un errore.
    command -v restorecon >/dev/null 2>&1 && restorecon -R "$HOME_DIR/.ssh" || true
    printf '%s ALL=(ALL) NOPASSWD:ALL\n' "$U" > "/etc/sudoers.d/$U"
    chmod 440 "/etc/sudoers.d/$U"
fi

echo "OK"

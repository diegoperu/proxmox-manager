#!/bin/sh
# ProxmoxManager - crea un utente OS dentro una VM via QEMU Guest Agent.
#
# Sorgente unica (`//go:embed` in handlers): usato in due modi, stesso testo.
#   - helper etichettato: /usr/local/libexec/pmgr/adduser.sh <utente> <chiave>
#     (su SELinux enforcing gira in virt_qemu_ga_unconfined_t, dominio non confinato)
#   - fallback inline:     sh -c "<questo testo>" pmgr <utente> <chiave>
#     (guest senza helper: Debian/Ubuntu, o Rocky non preparato - qui useradd fallisce
#      sotto SELinux, e' il motivo per cui esiste l'helper)
#
# I valori della richiesta arrivano SOLO come argomenti posizionali ($1, $2): mai
# interpolati nel testo, quindi una chiave con apici/metacaratteri non esegue comandi.
# $1 = username (gia' validato dal server), $2 = chiave pubblica o vuoto.
set -eu
umask 077

# Evita l'interrogazione D-Bus a systemd (NoSuchDynamicUser) a ogni lookup di un nome
# inesistente: nel dominio non confinato dell'agent SELinux nega la risposta e ogni
# lookup aspetta il timeout D-Bus di 25 s (useradd da solo arrivava a oltre 2 minuti).
# Innocuo sui guest senza SELinux: disabilita solo il modulo NSS degli utenti dinamici.
export SYSTEMD_NSS_DYNAMIC_BYPASS=1

U="${1:?username mancante}"
KEY="${2:-}"

# Utente gia' esistente: exit 3, nessuna modifica. Cosi' una richiesta non puo' resettare
# la password, aggiungere chiavi o dare sudo a un account preesistente (root incluso).
if id "$U" >/dev/null 2>&1; then
    echo "l'utente $U esiste gia' nella VM: nessuna modifica" >&2
    exit 3
fi

# Senza sudo (e senza visudo per validare) l'utente nascerebbe senza poter diventare amministratore
# e lo script direbbe OK: si rifiuta PRIMA di creare qualsiasi cosa (exit 4).
if ! command -v sudo >/dev/null 2>&1 || ! command -v visudo >/dev/null 2>&1; then
    echo "sudo non e' installato nella VM: utente $U non creato" >&2
    exit 4
fi

# Da qui in poi un errore non deve lasciare un utente a meta' (senza sudo e non piu' riparabile
# dal manager, che rifiuta gli utenti esistenti): si annulla la creazione.
created=0
rollback() {
    rc=$?
    if [ "$created" = 1 ] && [ "$rc" != 0 ]; then
        rm -f "/etc/sudoers.d/$U"
        userdel -r "$U" >/dev/null 2>&1 || usermod -L "$U" >/dev/null 2>&1 || true
        echo "creazione dell'utente $U annullata (errore, codice $rc)" >&2
    fi
}
trap rollback EXIT

useradd -m -s /bin/bash "$U"
created=1

# Gruppo amministrativo: wheel su RHEL/Rocky, sudo su Debian/Ubuntu. Si aggiunge quello
# che esiste davvero, senza ingoiare l'errore di un gruppo assente.
for g in wheel sudo; do
    if getent group "$g" >/dev/null 2>&1; then
        usermod -aG "$g" "$U"
    fi
done

HOME_DIR=$(getent passwd "$U" | cut -d: -f6)

# Sudo indipendente dalle regole di gruppo di /etc/sudoers (potrebbero essere state tolte):
# file dedicato, validato con visudo prima di metterlo al suo posto.
write_sudoers() {
    tmp=$(mktemp "/etc/sudoers.d/.pmgr.XXXXXX")
    printf '%s\n' "$1" > "$tmp"
    chmod 440 "$tmp"
    if ! visudo -cf "$tmp" >/dev/null 2>&1; then
        rm -f "$tmp"
        echo "regola sudoers non valida per $U" >&2
        exit 5
    fi
    mv -f "$tmp" "/etc/sudoers.d/$U"
}

if [ -z "$KEY" ]; then
    write_sudoers "$U ALL=(ALL) ALL"
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
        created=0
        exit 1
    fi
else
    install -d -m 700 -o "$U" -g "$U" "$HOME_DIR/.ssh"
    # Una sola riga (printf >, non >>): niente duplicati su chiamate ripetute.
    printf '%s\n' "$KEY" > "$HOME_DIR/.ssh/authorized_keys"
    chmod 600 "$HOME_DIR/.ssh/authorized_keys"
    chown "$U:$U" "$HOME_DIR/.ssh/authorized_keys"
    # Su SELinux la home appena creata puo' avere l'etichetta sbagliata (user_home_dir_t):
    # senza ssh_home_t sshd rifiuta la chiave. restorecon la corregge; assente il comando
    # (guest senza SELinux) non e' un errore.
    command -v restorecon >/dev/null 2>&1 && restorecon -R "$HOME_DIR/.ssh" || true
    write_sudoers "$U ALL=(ALL) NOPASSWD:ALL"
fi

# Verifica finale: l'utente deve davvero poter usare sudo. Altrimenti si annulla (trap).
if ! sudo -l -U "$U" >/dev/null 2>&1; then
    echo "verifica sudo fallita per $U" >&2
    exit 6
fi

echo "OK"

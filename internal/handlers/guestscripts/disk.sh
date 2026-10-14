#!/bin/sh
# ProxmoxManager — operazioni disco lato guest via QEMU Guest Agent.
#
# Sorgente unica (`//go:embed` in handlers): usato in due modi, stesso testo.
#   - helper etichettato: /usr/local/libexec/pmgr/disk.sh <subcommand> [args]
#     (su SELinux enforcing gira non confinato: lsblk/parted/mkfs/mount/fstab permessi)
#   - fallback inline:     sh -c "<questo testo>" pmgr <subcommand> [args]
#
# L'ORCHESTRAZIONE (quale disco, retry, rescan, eventuale riavvio della VM) resta nel
# codice Go, che invoca qui una fase per volta. I valori arrivano come argomenti
# posizionali, mai interpolati. Codici d'uscita non-standard sono documentati per fase.
set -eu

CMD="${1:?subcommand mancante}"
shift || true

case "$CMD" in

# find-blank: elenca (uno per riga) i dischi candidati al setup.
#   Candidato = disco vergine (nessuna partizione, nessun filesystem) OPPURE disco
#   lasciato a metà da un tentativo precedente (una sola partizione ext4 o non
#   formattata, non montata) — così un retry dopo un errore ritrova il disco giusto.
#   Selezione per PROPRIETÀ, non per diff prima/dopo: su q35+PCIe il disco è già
#   collegato a caldo alla PUT /config, prima ancora di questa chiamata.
#   lsblk che fallisce è un ERRORE (exit 3), non "nessun disco".
find-blank)
    LIST=$(lsblk -ndo NAME,TYPE) || { echo "lsblk non riuscito" >&2; exit 3; }
    # Best effort: un device che sparisce a metà scansione non deve abortire la ricerca.
    set +e
    printf '%s\n' "$LIST" | awk '$2=="disk"{print $1}' | while IFS= read -r d; do
        [ -n "$d" ] || continue
        n=$(lsblk -ln "/dev/$d" | wc -l)
        if [ "$n" = "1" ]; then
            blkid "/dev/$d" >/dev/null 2>&1 || echo "$d"
        elif [ "$n" = "2" ]; then
            p="/dev/${d}1"
            t=$(blkid -o value -s TYPE "$p" 2>/dev/null || true)
            if { [ -z "$t" ] || [ "$t" = "ext4" ]; } && ! findmnt -S "$p" >/dev/null 2>&1; then
                echo "$d"
            fi
        fi
    done
    ;;

# rescan: best effort (non tutti i guest hanno partprobe/SCSI hosts/udevadm).
rescan)
    partprobe 2>/dev/null || true
    for host in /sys/class/scsi_host/host*; do
        [ -e "$host/scan" ] && echo "- - -" > "$host/scan" 2>/dev/null || true
    done
    udevadm settle 2>/dev/null || true
    ;;

# setup <device> <mount_point> <perm>: partiziona (GPT, 1 partizione 100%), formatta
#   ext4, crea il mount point, riga fstab per UUID, monta, applica i permessi.
#   Idempotente: partizione/filesystem/fstab esistenti non vengono rifatti.
#   device da find-blank, mount_point e perm (1777|0777) già validati dal server.
setup)
    DEV="${1:?device mancante}"; MNT="${2:?mount point mancante}"; PERM="${3:?permessi mancanti}"
    case "$DEV" in /dev/*) ;; *) echo "device non valido: $DEV" >&2; exit 2;; esac

    if ! [ -b "${DEV}1" ]; then
        parted -s "$DEV" mklabel gpt
        parted -s "$DEV" mkpart primary 0% 100%
        partprobe "$DEV" 2>/dev/null || true
        udevadm settle 2>/dev/null || true
        sleep 1
    fi

    # Formatta solo se la partizione non ha già ext4. Si controlla l'OUTPUT del tag TYPE,
    # non l'exit code di blkid: su una partizione GPT creata ma mai formattata blkid esce
    # 0 (stampa PARTLABEL/PARTUUID) pur senza TYPE, quindi un check exit-code salterebbe mkfs.
    FSTYPE=$(blkid -c /dev/null -o value -s TYPE "${DEV}1" 2>/dev/null || true)
    if [ "$FSTYPE" != ext4 ]; then
        mkfs.ext4 -F "${DEV}1"
        udevadm settle 2>/dev/null || true
    fi

    mkdir -p "$MNT"

    UUID=""
    i=1
    while [ "$i" -le 5 ]; do
        UUID=$(blkid -c /dev/null -o value -s UUID "${DEV}1" 2>/dev/null || true)
        [ -n "$UUID" ] && break
        sync
        udevadm settle 2>/dev/null || true
        sleep 1
        i=$((i + 1))
    done
    if [ -z "$UUID" ]; then
        echo "DIAG blkid=[$(blkid "${DEV}1" 2>&1)] lsblk=[$(lsblk -f "$DEV" 2>&1 | tr '\n' ';')]" >&2
        echo "impossibile determinare UUID di ${DEV}1 dopo 5 tentativi" >&2
        exit 1
    fi

    if ! grep -q "^UUID=$UUID " /etc/fstab; then
        echo "UUID=$UUID $MNT ext4 defaults 0 2" >> /etc/fstab
    fi
    mountpoint -q "$MNT" || mount "$MNT"
    chmod "$PERM" "$MNT"
    echo "OK"
    ;;

*)
    echo "subcommand sconosciuto: $CMD" >&2
    exit 2
    ;;
esac

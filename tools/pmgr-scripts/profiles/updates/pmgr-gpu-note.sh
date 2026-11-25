#!/bin/sh
# pmgr-gpu-note - note per le VM con scheda NVIDIA (ProxmoxManager). Solo ASCII.
#
#   pmgr-gpu-note                stampa la nota (se c'e'); e' quella che si vede al login
#   pmgr-gpu-note --update-flag  la ricalcola e la scrive in /run/pmgr-gpu-note (sparisce al riavvio, si rifa dopo ogni giro di apt)
#
# Cosa dice:
#  1. se c'e' un kernel piu' nuovo di quello in uso: se per quel kernel esiste il modulo NVIDIA il riavvio e' sicuro per la GPU,
#     altrimenti dice di NON riavviare (la GPU non partirebbe);
#  2. quanti aggiornamenti di kernel, driver NVIDIA/CUDA, docker, containerd, slurm e munge sono in attesa: non si installano da
#     soli (vedi /etc/apt/apt.conf.d/53pmgr-gpu-hold), li decide il gruppo.
# Le variabili PMGR_UNAME, PMGR_BOOT e PMGR_MODULES servono solo ai test.
NOTE=/run/pmgr-gpu-note
running=${PMGR_UNAME:-$(uname -r)}
bootdir=${PMGR_BOOT:-/boot}
moddir=${PMGR_MODULES:-/lib/modules}

build() {
  out=""
  flavor=$(printf '%s' "$running" | sed -E 's/^[0-9]+\.[0-9]+\.[0-9]+-[0-9]+//')
  newest=$(ls "$bootdir" 2>/dev/null | sed -n 's/^vmlinuz-//p' | grep -E -- "${flavor}\$" | sort -V | tail -n 1)
  if [ -n "$newest" ] && [ "$newest" != "$running" ] && [ "$(printf '%s\n%s\n' "$running" "$newest" | sort -V | tail -n 1)" = "$newest" ]; then
    if find "$moddir/$newest" -name 'nvidia.ko*' 2>/dev/null | grep -q .; then
      out="GPU: kernel $newest installato con il modulo NVIDIA: il riavvio e' sicuro per la GPU."
    else
      out="GPU: ATTENZIONE, per il kernel $newest manca il modulo NVIDIA. NON riavviare finche' non e' installato, altrimenti la GPU non parte."
    fi
  fi
  held=$(timeout 60 apt list --upgradable 2>/dev/null | grep -E '^(linux-image|linux-headers|linux-modules|nvidia|libnvidia|xserver-xorg-video-nvidia|cuda|libcudnn|libnccl|docker|containerd|slurm|libslurm|munge|libmunge)' | cut -d/ -f1 | tr '\n' ' ')
  n=$(printf '%s' "$held" | wc -w | tr -d ' ')
  if [ "${n:-0}" != 0 ]; then
    out="${out:+$out
}GPU: $n aggiornamenti NON automatici in attesa (kernel, driver NVIDIA/CUDA, docker, slurm): li decide il gruppo. Elenco: apt list --upgradable"
  fi
  printf '%s' "$out"
}

case "${1:-}" in
  --update-flag)
    msg=$(build)
    if [ -n "$msg" ]; then printf '%s\n' "$msg" >"$NOTE"; else rm -f "$NOTE"; fi
    ;;
  *)
    if [ -s "$NOTE" ]; then cat "$NOTE"; fi
    ;;
esac
exit 0

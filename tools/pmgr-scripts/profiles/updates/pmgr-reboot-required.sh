# Avvisa al login (console e SSH) se serve un riavvio dopo gli aggiornamenti di sicurezza.
# Gestito da ProxmoxManager. Letto da /etc/profile per le shell di login. Solo ASCII.
# Scrive solo se la shell e' interattiva e l'uscita e' un terminale: gli script non vedono il messaggio.
case "$-" in
  *i*) ;;
  *) return 0 2>/dev/null || exit 0 ;;
esac
[ -t 1 ] || { return 0 2>/dev/null || exit 0; }
[ -x /usr/local/sbin/pmgr-reboot-check ] || { return 0 2>/dev/null || exit 0; }
_pmgr_why=$(/usr/local/sbin/pmgr-reboot-check 2>/dev/null)
if [ "$?" = 1 ]; then
  printf '\n*** ATTENZIONE: riavvio necessario dopo gli aggiornamenti di sicurezza (%s).\n' "$_pmgr_why"
  # VM con scheda NVIDIA: se la nota GPU dice di NON riavviare (kernel nuovo senza il modulo del driver) non si invita a riavviare
  if [ -x /usr/local/sbin/pmgr-gpu-note ] && /usr/local/sbin/pmgr-gpu-note 2>/dev/null | grep -q 'NON riavviare'; then
    printf '*** NON riavviare ora: il kernel nuovo non ha il modulo NVIDIA e la GPU non partirebbe (vedi avviso GPU sotto).\n'
    printf '*** DO NOT reboot yet: the new kernel has no NVIDIA module, the GPU would not start (see GPU note below).\n\n'
  else
    printf '*** Riavvia appena possibile: sudo reboot\n'
    printf '*** WARNING: restart required after security updates. Please reboot soon: sudo reboot\n\n'
  fi
fi
unset _pmgr_why

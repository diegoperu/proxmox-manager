# Mostra al login (console e SSH) la nota delle VM con scheda NVIDIA: kernel nuovo e modulo, aggiornamenti trattenuti.
# Gestito da ProxmoxManager. Solo se la shell e' interattiva e l'uscita e' un terminale. Solo ASCII.
case "$-" in
  *i*) ;;
  *) return 0 2>/dev/null || exit 0 ;;
esac
[ -t 1 ] || { return 0 2>/dev/null || exit 0; }
[ -x /usr/local/sbin/pmgr-gpu-note ] || { return 0 2>/dev/null || exit 0; }
_pmgr_gpu=$(/usr/local/sbin/pmgr-gpu-note 2>/dev/null)
if [ -n "$_pmgr_gpu" ]; then
  printf '\n%s\n\n' "$_pmgr_gpu"
fi
unset _pmgr_gpu

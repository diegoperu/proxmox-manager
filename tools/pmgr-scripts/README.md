# pmgr-scripts - template cloud-init e snippet (nuova struttura)

Sostituisce `/root/cloud-images` (una cartella per sistema operativo, con script quasi identici).
Provato su tutti i cluster in uso; la procedura per un nodo o un cluster nuovo e' in `docs/ONBOARDING.md`.
Questa cartella e' nel repository: non contiene segreti (password e chiavi stanno in `/etc/pmgr` sui nodi) ne'
indirizzi interni (un test lo verifica).

## Idea

- Gli script sono **identici su ogni cluster** (l'hash del manifest resta valido).
- Cio' che cambia da una release all'altra sta in `os.d/<id>.conf` (indirizzo dell'immagine, nome del
  template, tag, nome dello snippet) e in `snippets.d/<id>.yaml` (il corpo dello snippet).
- Cio' che cambia da un cluster all'altro sta in `/etc/pmgr/cluster.conf`, **locale e non sincronizzato**
  (storage, bridge, DNS, VMID di partenza, utente, percorsi dei segreti). Se manca o e' incompleto gli
  script si fermano senza toccare niente. Vedi `cluster.conf.example`.
- Nessun file di configurazione viene eseguito: si legge riga per riga con una whitelist.

## Struttura

```
pmgr-scripts/
  bin/pmgr-os.sh           crea/aggiorna i template (una release, una famiglia, tutte)
  bin/pmgr-add-release.sh  aggiunge una release nuova (os.d + snippets.d)
  lib/pmgr-lib.sh          funzioni comuni (configurazione, controlli, lock, download)
  os.d/<id>.conf           una per release: ubuntu-22.04, ubuntu-24.04, ubuntu-26.04, debian-12,
                           debian-13, rocky-8, rocky-9
  snippets.d/<id>.yaml     corpo dello snippet cloud-init di ogni release
  helpers/*.sh             helper del guest (adduser, disk, dmesg), copiati dal repository
  cluster.conf.example     modello di /etc/pmgr/cluster.conf
  update.sh, update_all.sh, update_{ubuntu,debian,rocky}_template.sh   i soliti nomi, ora sottili
  tests/test_pmgr_scripts.sh   175 controlli con qm/pvesm/curl/qemu-img finti
```

## Uso

```bash
bin/pmgr-os.sh --list                       # release abilitate e relativi VMID
bin/pmgr-os.sh ubuntu-24.04 --dry-run       # controlli in sola lettura + cosa farebbe
bin/pmgr-os.sh ubuntu-24.04                 # image -> snippet -> template -> finalize
bin/pmgr-os.sh ubuntu-24.04 --step snippet  # un solo passo
bin/pmgr-os.sh --family rocky               # tutte le release Rocky abilitate
./update.sh                                 # tutto, con log in /var/log/pmgr/AAAA_MM_GG_update.log
```

Il VMID di un template e' `VMID_BASE` (cluster.conf) + `SLOT` (os.d): con 1000 si ottengono gli stessi
1001-1007 di oggi. Una release si esclude dagli aggiornamenti con `ENABLED=0` nel suo file.

## Aggiungere una release nuova

```bash
bin/pmgr-add-release.sh --family ubuntu --version 26.10 --codename <nome>   # Ubuntu e Debian vogliono il nome in codice
bin/pmgr-add-release.sh --family rocky  --version 10
```

Crea `os.d/<id>.conf` (SLOT successivo, indirizzo dell'immagine e checksum secondo lo schema del fornitore) e
copia il corpo dello snippet dalla release piu' recente della stessa famiglia (`--like` per sceglierne
un'altra). Controlla che l'immagine esista sul mirror (`--no-check-url` per saltarlo) e non sovrascrive mai.
Poi: **leggi `snippets.d/<id>.yaml`** (pacchetti e sintassi di sshd cambiano tra le release), `--dry-run`,
creazione, sincronizzazione.

## Cosa fanno di diverso dagli script di oggi

| Prima | Ora |
|---|---|
| `qm destroy` subito, anche se il download non e' riuscito | Prima si controllano configurazione, storage, bridge, password, chiavi; si scarica in `.part`, si controllano dimensione, formato qcow2 e (dove il mirror lo pubblica) checksum, si genera lo snippet. Solo dopo si cancella il template vecchio. Un guasto prima di quel punto lascia template e immagine precedenti intatti. |
| Una VM qualsiasi al VMID del template veniva distrutta | Si distrugge solo una VM con template o nome `*-cloudinit-template`; altrimenti ci si ferma |
| Nome del disco importato indovinato (`vm-1003-disk-1`) | Letto da Proxmox (`unused0`): funziona con ZFS, LVM, directory e Ceph |
| Password in chiaro in 7 file | Letta da un file `0600` (`PASSWORD_FILE`), mai stampata nemmeno in `--dry-run`; passata a `openssl` da stdin |
| Percorsi fissi e relativi | Cartella base ricavata dalla posizione dello script |
| VMID, storage, bridge, DNS fissi | `cluster.conf` |
| Snippet cancellato e riscritto | Scritto in un file temporaneo e rinominato; invariato se identico |
| Ubuntu 26.04 con lo snippet di 24.04 | Ha il suo (`vendor_ubuntu_26.04.yaml`, stesso contenuto per ora) |
| Helper Rocky `adduser.sh`, `disk.sh` (copie vecchie, non ASCII) | `helpers/` copiati dal repository, controllo ASCII; **si aggiunge `dmesg.sh`** (prima non incorporato) |
| Nessun controllo di concorrenza | `flock` su `/run/lock/pmgr-templates.lock` |
| Un errore fermava tutto o passava inosservato | Una release che fallisce non ferma le altre; riepilogo finale e uscita 1 |
| `rsync_snippets.sh` verso i nodi | `update_all.sh` dichiara in chiaro che la sincronizzazione non e' stata eseguita finche' `bin/pmgr-snippets-sync` non esiste |

Gli snippet generati dal nuovo sistema erano **identici byte per byte** a quelli degli script originali (verificato per
tutte e sette le release, prima delle modifiche seguenti). Le differenze volute sono due: il blocco `dmesg.sh` nelle
release Rocky e la **configurazione di fail2ban** (vedi sotto).

## fail2ban (corretto il 2026-11-05)

Verificato con i pacchetti veri di Debian 12/13, Ubuntu 22.04/24.04 e Rocky 8/9 in container (`tests/test_fail2ban_snippets.sh`).
Quello che c'era prima: copia integrale di `jail.conf` in `jail.local` con `bantime = 1d`; finestra e soglia lasciate ai valori
predefiniti (10 minuti), nessun ban progressivo. Problemi trovati:

| Problema | Effetto |
|---|---|
| Ban di **1 giorno** invece di 1 ora, `findtime` 10 min | non corrisponde alla regola voluta |
| Nessun ban progressivo | la recidiva non e' punita di piu' |
| `jail.local` e' una copia di tutto `jail.conf` e viene letto dopo `jail.d/` | **annulla le impostazioni della distribuzione**: l'azione di ban tornava a `iptables-multiport` (invece di `nftables` su Debian 13 e Ubuntu 24.04) e su Ubuntu il backend tornava `auto` |
| **Rocky: nessuna jail sshd avviata** | il pacchetto EPEL non abilita sshd: fail2ban girava senza proteggere niente |
| **Rocky: `restart` ma mai `enable`** (preset di Rocky: `disable *`) | dopo il `reboot` finale fail2ban non partiva |
| Backend `auto` su Debian 12 e Ubuntu 22.04 | senza `rsyslog` (assente nelle immagini cloud) manca `auth.log` e **fail2ban non parte affatto** |

Ora ogni snippet scrive due piccoli file e non copia piu' niente:

- `/etc/fail2ban/jail.d/50-pmgr.local`: `bantime = 1h`, `findtime = 15m`, `maxretry = 5`, `bantime.increment = true`,
  `bantime.multipliers = 1 2 4 8 16 32 64` (il ban raddoppia a ogni recidiva: 1h, 2h, 4h ... 64h, poi resta a 64h),
  `bantime.maxtime = 1w` (rete di sicurezza), `[sshd] enabled = true`, `backend = systemd`;
- `/etc/fail2ban/fail2ban.d/50-pmgr.local`: `dbpurgeage = 4w`. Il ban progressivo conta le recidive nel database, che per
  default dimentica tutto dopo **1 giorno**: senza questo la recidiva dopo tre giorni ripartirebbe da 1 ora;
- `systemctl enable` oltre a `restart`.

Comportamento osservato con un demone vero (tempi ridotti, `nftables`): 3 fallimenti dentro la finestra -> ban, regola nft
aggiunta; allo sblocco la regola sparisce; la recidiva raddoppia (4 s, 8, 16, 32, poi resta 32); 2+2 fallimenti separati
da una pausa lunga non bannano. Da sapere (comportamento di fail2ban, non della configurazione): un tentativo con un
**utente inesistente** conta per **2** fallimenti (una riga "Invalid user" e una "Failed password"), quindi chi prova nomi
inventati viene bannato al 3 tentativo; con utente valido conta 1. E la finestra non e' mobile e precisa: con una finestra
di 6 s due gruppi distanti 8 s contavano ancora insieme, a 15 s no.

Decisione dell'utente: **nessun `ignoreip`** (in caso di blocco si entra dalla console della VM).

### Aggiornare le VM gia' esistenti

Le VM create con i vecchi snippet non cambiano da sole. Due script, entrambi idempotenti:

- `bin/pmgr-fail2ban-update.sh` gira **dentro** la VM, come root. `--check` non scrive niente (uscita 3 se c'e' da fare).
  Scrive i due file di configurazione, mette da parte la **vecchia copia di `jail.conf`** in `jail.local` (solo se e'
  davvero quella: uguale a `jail.conf` tranne le righe `bantime`; un `jail.local` con altre personalizzazioni non si tocca),
  controlla con `fail2ban-client -d` che la configurazione **caricata** sia quella attesa, abilita e riavvia il servizio e
  verifica che la jail sshd risponda. Se qualcosa non torna **rimette tutto com'era** (copie in
  `/var/backups/pmgr-fail2ban/<data>/`). Non dipende da `cmp`/`diff` (assenti su Rocky minimale). Richiede `python3-systemd`.
- `bin/pmgr-fail2ban-rollout.sh [--check] [--user U] [--hosts-file F] HOST...` lo lancia su piu' VM via ssh, una alla volta,
  con `sudo -n`; una VM irraggiungibile o in errore non ferma le altre; riepilogo finale.

```bash
bin/pmgr-fail2ban-rollout.sh --check --user tecnico --hosts-file vm.txt   # 1. cosa cambierebbe, senza toccare niente
bin/pmgr-fail2ban-rollout.sh --user tecnico una-vm-di-prova               # 2. una VM di prova
bin/pmgr-fail2ban-rollout.sh --user tecnico --hosts-file vm.txt           # 3. il resto
```

Test: `tests/test_fail2ban_update.sh` (contenuto identico agli snippet; rollout con ssh finto; in 6 container con il
fail2ban vero: VM vecchia simulata, controllo, aggiornamento, idempotenza, `jail.local` personalizzato, configurazione
rotta e valori in conflitto con ritorno indietro) e `tests/test_fail2ban_update_systemd.sh` (systemd vero: servizio fermo
e disabilitato come su Rocky, valori letti dal demone, riavvio che fallisce e ritorno indietro). **Non provato su una VM
vera** (ne' su Magi): `ufw`/`firewalld` reali e `reboot` non sono coperti.

## Preparazione di un cluster (una volta)

1. `/etc/pmgr/cluster.conf` (da `cluster.conf.example`), `root`, `0600`.
2. `/etc/pmgr/ssh_keys.pub` (le chiavi pubbliche dei template; oggi `ssh_pub_auth_keys/auth`).
3. `/etc/pmgr/template-password`: una riga, `root`, `0600`.
4. Lo storage degli snippet deve avere il contenuto **Snippets** (su `local` di un'installazione nuova non c'e': lo
   aggiungi tu in Datacenter > Storage; lo script lo segnala e non lo fa da solo).
5. Gli helper: dopo ogni modifica agli helper nel repository, copiare
   `internal/handlers/guestscripts/{adduser,disk,dmesg}.sh` in `helpers/` e rigenerare gli snippet Rocky.

## Limiti noti

- **Checksum:** Ubuntu e Debian sono configurati (`SHA256SUMS` / `SHA512SUMS`, il file di checksum e' cercato per nome);
  per Rocky il mirror pubblica nomi con la data e non `latest`, quindi per ora Rocky si controlla solo per
  dimensione e formato. Il formato dei file di checksum e' scritto da fixture e **va provato sui mirror veri** (su Magi):
  se non si legge l'aggiornamento di quella release si ferma (senza danni); in quel caso togli `IMAGE_CHECKSUM_*`
  da `os.d/<id>.conf` e dimmelo.
- **Utente nello snippet:** il corpo di Ubuntu contiene `tecnico` (cron degli aggiornamenti). Se un cluster usa un
  `CI_USER` diverso, gli snippet vanno generati li' e non copiati.
- `--cipassword` passa da riga di comando (`qm` non legge da stdin): per un istante l'hash e' visibile a chi puo'
  leggere i processi del nodo (era cosi' anche prima, ma prima era la password in chiaro, ora e' solo l'hash).
- Le immagini si scaricano in `IMAGES_DIR` (predefinito `/var/lib/vz/pmgr-images`, circa 6 GB per sette release): verifica lo
  spazio. Le vecchie in `/root/cloud-images/<os>/` non vengono toccate.

## SSH e profilo "hosting" (2026-11-05)

**Cluster di calcolo (profilo `standard`, predefinito):** SSH con password abilitato su tutte le release. Ubuntu e Debian
lo facevano gia' con i `sed` dello snippet; le **Rocky 8 e 9 non avevano nessuna impostazione SSH** e ora ricevono
`/etc/ssh/sshd_config.d/00-pmgr-ssh.conf` (`PasswordAuthentication yes`, `PermitRootLogin no`). Su Rocky 8 lo snippet
aggiunge anche la riga `Include /etc/ssh/sshd_config.d/*.conf` in testa a `sshd_config`, perche' la configurazione di serie
non la ha (l'`sshd` la supporta).

**Cluster di hosting (profilo `hosting`):** SSH solo con chiave, e rimesso cosi' dopo ogni modifica.

- Come si sceglie il profilo: `PROFILE=hosting` in `/etc/pmgr/cluster.conf` (per i template generati in quel cluster);
  `pmgr-os.sh --render DIR --profile hosting --all` genera le varianti di hosting in una cartella, senza toccare nessun
  cluster (per il sync, vedi D90). La casella "hosting" per destinazione del sync arriva con `pmgr-snippets-sync`.
- Cosa cambia nello snippet: spariscono le righe che abilitano la password SSH (marcate `# pmgr:ssh-password-begin/end`
  nei corpi) e prima del `reboot` finale si installano `/usr/local/sbin/pmgr-ssh-enforce`, `pmgr-ssh-enforce.service`
  (`Before=ssh.service`, a ogni avvio) e `pmgr-ssh-enforce.timer` (ogni 15 minuti), si esegue subito e si abilitano.
- Cosa fa `pmgr-ssh-enforce` (`profiles/hosting/`): scrive `/etc/ssh/sshd_config.d/00-pmgr-hosting.conf`
  (`PasswordAuthentication no`, `AuthenticationMethods publickey`, `PubkeyAuthentication yes`, `PermitRootLogin no`,
  `PermitEmptyPasswords no`); porta `Include` in testa a `sshd_config` (in sshd **vince il primo valore** e un `Include`
  dopo un blocco `Match` varrebbe solo per quell'utente); commenta (`# pmgr-disabled:`) in tutti gli altri file, anche
  dentro i `Match`, le righe che riabiliterebbero la password o la tastiera; controlla con `sshd -T` il valore **effettivo**;
  se la configurazione risulta invalida riporta i file com'erano; ricarica sshd (`--no-block`) solo se ha cambiato qualcosa.
- La password dell'utente del template resta (D89): serve per la console; via SSH si entra solo con chiave.
- Limiti: chi ha root puo' sempre fermare il servizio o il timer; la regola si ripristina al riavvio e al massimo dopo
  15 minuti. **Non provato**: SELinux in enforcing su una Rocky vera (nei container non c'e'), `ufw`/`firewalld` reali.
- Prima di usarlo su un cluster di hosting verifica che ogni VM abbia le chiavi giuste: dopo il riavvio chi entrava con la
  password non entra piu' (resta la console).

Test: `tests/test_hosting_profile.sh` (rendering, YAML, fedelta' ai file sorgente, ordine, nessuna riga "password si"),
`tests/test_ssh_enforce.sh` (6 container con `openssh-server` vero e configurazioni ostili), `tests/test_hosting_boot.sh`
(systemd vero e `sshd` in esecuzione: manomissione, timer, **riavvio**, login a password rifiutato). Il test con systemd
ha trovato un deadlock (il servizio e' `Before=ssh.service` e ricaricava `ssh` aspettando): ora `reload --no-block`.

## Aggiornamenti di sicurezza automatici (2026-11-05, tutti i cluster e i profili)

**Cosa c'era prima** (verificato con i pacchetti veri): Debian non installava `unattended-upgrades` (nessun aggiornamento
automatico); Ubuntu aveva un cron di mezzanotte lanciato come utente `tecnico` (non root: non poteva installare) mentre i
timer di apt installano **una volta sola** al giorno (06:00 + ritardo casuale fino a 1 ora) e gli intervalli "1 giorno"
saltano comunque il secondo giro; Rocky abilitava `dnf-automatic.timer` ma la configurazione di serie e'
`apply_updates = no` (**scarica soltanto**) e `upgrade_type = default` (tutti gli aggiornamenti); nessun avviso di riavvio
al login (Ubuntu solo con `update-notifier-common`).

**Ora** (blocco aggiunto da `pmgr-os.sh` prima del `reboot` finale, file in `profiles/updates/`):

| | Debian | Ubuntu | Rocky |
|---|---|---|---|
| Pacchetti | `unattended-upgrades` | idem | `dnf-automatic`, `dnf-utils` |
| Cosa installa | solo `-security` e `Debian-Security` | solo `-security`, ESM apps e infra | `upgrade_type = security` |
| Quando | liste 04:30 e 16:30, installazione 06:00 e 18:00 (ritardo casuale 20-30 min) | idem | 06:00 e 18:00 (ritardo casuale 30 min) |
| Come non salta il secondo giro | `APT::Periodic::* "always"`, `Enable "1"` | idem | `dnf-automatic` non ha marcatori giornalieri |
| Riavvio automatico | `Automatic-Reboot "false"` | idem | `reboot = never` (dove l'opzione esiste) |

- **Avviso di riavvio**: `/usr/local/sbin/pmgr-reboot-check` (kernel installato piu' nuovo di quello in uso, `reboot-required` dei
  pacchetti, `needs-restarting`, flag in `/run` che sparisce da solo al riavvio) e `/etc/profile.d/pmgr-reboot-required.sh`: a
  **ogni login interattivo** (console e SSH) stampa *"ATTENZIONE: riavvio necessario... sudo reboot"* in italiano e in
  inglese. Non stampa nelle shell non interattive: l'output di script e di `ssh host comando` resta pulito.
  Il flag si aggiorna dopo ogni `dpkg` (hook `DPkg::Post-Invoke`) e dopo ogni giro di `dnf-automatic`.
- Il vecchio cron `/etc/cron.d/auto_updates` e' tolto dagli snippet Ubuntu.
- `APT::Periodic::Enable "1"` e' impostato esplicitamente: alcune immagini (container, minimali) hanno `"0"` e spengono in
  silenzio tutti i giri automatici. Un file che si legge dopo `52pmgr-unattended-upgrades` (per esempio `99-...`) lo puo'
  ancora annullare; lo scopre `pmgr-vm-apply.sh` con la verifica della configurazione effettiva.

**Prove** (`tests/test_updates.sh`, `tests/test_updates_systemd.sh`): configurazione effettiva con i pacchetti veri su 6
distribuzioni (origini solo di sicurezza, nessun riavvio, hook, orari validi con `systemd-analyze calendar`, avviso con un
vero terminale e assente senza terminale); **giri reali** (Ubuntu 24.04 ha installato `apt`, `libapt-pkg6.0t64`, `libpcre2-8-0`;
Rocky 9: da **180 avvisi di sicurezza in sospeso a 0**, 55 pacchetti cambiati, **47 aggiornamenti non di sicurezza lasciati
dove erano**); con systemd vero (Ubuntu 24.04 e Rocky 9): timer con esattamente due orari e quelli di serie azzerati, due
esecuzioni ravvicinate dell'installazione **entrambe eseguite**, avviso su un **vero login SSH**, comando via SSH non sporcato.

## VM gia' esistenti: `pmgr-vm-apply.sh` e `pmgr-vm-rollout.sh`

Le VM create con i vecchi snippet non cambiano da sole (cloud-init esegue `runcmd` una volta per istanza).
`bin/pmgr-vm-apply.sh` (dentro la VM, root) porta una VM alla configurazione degli snippet usando gli **stessi file** dei profili
(una sola sorgente). Moduli, e quali usare per ogni cluster:

| Modulo | Cosa fa | Dove |
|---|---|---|
| `fail2ban` | sshd: 5 tentativi in 15 minuti = ban di 1 ora, ban progressivo (delega a `pmgr-fail2ban-update.sh`) | tutti i cluster |
| `updates` | aggiornamenti di **sicurezza** automatici due volte al giorno, nessun riavvio automatico, avviso al login | tutti i cluster |
| `baseline` | `fail2ban` + `updates` (**non** comprende i moduli ssh-*) | tutti i cluster |
| `ssh-standard` | SSH con password, root no, `Include` di `sshd_config.d` in testa (Rocky 8 non ce l'ha e ha root abilitato). **Si rifiuta su una VM di hosting.** | **calcolo**: `baseline` + `ssh-standard` |
| `ssh-hosting` | SSH solo con chiave (`pmgr-ssh-enforce`). **Si rifiuta se nessun utente ha una chiave valida** in `authorized_keys` (righe finte e utenti `nologin` non contano) salvo `--allow-no-keys`: chi entrava con la password non entra piu' (resta la console). | **hosting**: `baseline` + `ssh-hosting` |

`--module all` non esiste piu' apposta: un gruppo non deve poter includere `ssh-hosting` per errore.

- **`--verify [--profile standard|hosting]`**: sola lettura, stampa `[OK]`/`[FALLITO]`/`[INFO]` e esce 1 se qualcosa non va. Controlla cloud-init
  (`status` e `instance-id`), agent, i moduli `fail2ban` e `updates`, l'SSH secondo il profilo (valori **effettivi** con `sshd -T`; il profilo
  standard segnala una VM con configurazione di hosting e viceversa), su Rocky gli helper del manager, l'etichetta SELinux
  `virt_qemu_ga_unconfined_exec_t`, il booleano `virt_qemu_ga_run_unconfined` e `guest-exec` nell'agent, e se serve un riavvio.
- `--check` non scrive niente (uscita 3 se c'e' da fare); idempotente; copie in `/var/backups/pmgr-vm/<data>/`; se la verifica finale non
  passa rimette tutto com'era.
- `bin/pmgr-vm-rollout.sh --module M [--check] [--allow-no-keys] [--user U] [--hosts-file F] HOST...` e `--verify --profile P ...` lo lanciano via
  ssh, una VM alla volta, con `sudo -n`: mandano sul canale ssh uno script autosufficiente (archivio con gli script e i profili), non copiano
  niente prima. Una VM irraggiungibile o in errore non ferma le altre; riepilogo finale.
  Sequenza consigliata per ogni gruppo: `--verify` -> `--check` -> **una VM di prova** -> il resto -> `--verify` e riavvio.
  Esempi: calcolo `--module baseline` poi `--module ssh-standard`; hosting `--module baseline` poi `--module ssh-hosting`.

Test: `tests/test_vm_apply.sh` (argomenti ostili, payload, catena completa in 6 container: 56 controlli ciascuno - moduli, `--verify` su VM
vecchia e conforme, rifiuti, ripristino con sshd gia' rotto).


## Hardening dei sistemi di hosting (verificato il 2026-11-06)

Requisiti: un firewall, **22, 80 e 443 aperte**, tutto il resto chiuso; SSH solo con chiave; fail2ban; aggiornamenti di sicurezza.
Release provate (regola: **ultima LTS nei nuovi deploy**, qualche LTS precedente): Ubuntu **26.04** / 24.04 / 22.04, Debian **13** / 12,
Rocky **10** / 9 / 8 (Rocky 10 e' stata aggiunta alle release: `os.d/rocky-10.conf`, VMID 1008).

| Requisito | Prima | Ora |
|---|---|---|
| Firewall | Debian/Ubuntu: `ufw` attivo ma **solo la 22** aperta (80/443 chiuse). Rocky: blocco commentato, valeva `firewalld` di serie: 80/443 chiuse e **cockpit (9090) aperto** anche se non installato | `profiles/hosting/firewall-ufw.sh` e `firewall-firewalld.sh`: deny in ingresso, aperte 22/80/443 (ufw con `--force`, perche' cloud-init non ha terminale; firewalld senza cockpit e dhcpv6-client) |
| SSH solo con chiave | fatto (`pmgr-ssh-enforce`) | idem, piu' `LoginGraceTime 30` e `X11Forwarding no` |
| fail2ban | fatto | idem; provato con il firewall attivo (la regola del ban finisce in `iptables`/`nft`/`firewalld`) |
| Aggiornamenti di sicurezza | fatto | idem |
| MAC obbligatorio | SELinux (Rocky) / AppArmor (Debian, Ubuntu) di serie | `--verify --profile hosting` lo controlla |

**Prova con traffico vero** (`tests/test_hosting_firewall.sh`): container con diritti di rete e un secondo container che si collega dall'esterno
(le porte pubblicate di Podman entrano dal loopback, che il firewall lascia passare: non provano niente). Senza firewall tutte e cinque le porte di prova
rispondono; con i comandi dello snippet di hosting rispondono **22, 80, 443** e restano **filtrate 8080 e 9090**, su tutte e otto le release.
Difetto trovato dalla prova: su Rocky `firewall-offline-cmd` non accetta `--remove-service` insieme a `--zone` ("Can't use lokkit options with other
options"), quindi **cockpit restava aperto** senza nessun errore visibile; ora la rimozione e' un servizio per volta sulla zona predefinita.
(`sysctl`/`procps` mancano nelle immagini container minimali e `ufw status` si lamenta: nelle immagini cloud ci sono.)

**VM di hosting gia' esistenti**: modulo `firewall-hosting` di `pmgr-vm-apply.sh` (`pmgr-vm-rollout.sh --module firewall-hosting`).
Sicurezza del modulo: su una VM con il **firewall spento** si **rifiuta** se qualche servizio ascolta su altre porte (un servizio dei clienti verrebbe
tagliato): si indicano le porte da tenere aperte con `--open 3306/tcp,...` oppure si accetta la chiusura con `--allow-close-ports`. Se il firewall e' gia' attivo
aggiunge soltanto le regole mancanti e **conserva quelle del cliente**. Copie in `/var/backups/pmgr-vm/`, ripristino se la verifica fallisce.
Per l'hosting: `baseline` + `ssh-hosting` + `firewall-hosting`. `--verify --profile hosting` ora controlla anche il firewall (configurazione su disco, e lo stato
attivo se leggibile), SELinux/AppArmor e segnala le porte in ascolto filtrate dal firewall. Test: `tests/test_firewall_module.sh`.

**Non coperto, da sapere:** i container dei clienti (Docker pubblica le porte aggirando `ufw`); servizi che il cliente apre sulle porte 80/443 non sono
filtrati; `ufw`/`firewalld` provati in container (non su una VM vera); Rocky 10 e Ubuntu 26.04 provati con pacchetti e immagini di container,
**non** con le immagini cloud ufficiali (da controllare su Magi: `guest-exec` nell'agent di Rocky 10, SELinux, `curl-minimal`/`curl` su Rocky 9).


## pmgr-snippets-sync - allineare script e snippet tra nodi e cluster

`bin/pmgr-snippets-sync.sh` gira sul **nodo sorgente** come utente `pmgr-sync` e porta snippet e script sulle
destinazioni (stesso cluster o altri cluster) attraverso il comando ristretto di `tools/pmgr-sync` (la destinazione
accetta solo `pmgr-list` e una scrittura rsync senza cancellazioni).

```
pmgr-snippets-sync.sh [--config F] [--json] status [--dest N]
pmgr-snippets-sync.sh [--config F] [--json] diff   [--dest N] [--all]
pmgr-snippets-sync.sh [--config F] [--json] run    [--dest N] [--apply] [--missing-only]
pmgr-snippets-sync.sh manifest [--profile standard|hosting]
pmgr-snippets-sync.sh discover          # propone le righe "dest" dai membri del cluster (/etc/pve/.members)
```
Uscite: 0 allineato / copia riuscita e verificata, 1 errore, 2 uso errato, 3 differenze (status, diff, anteprima).

Configurazione `/etc/pmgr/sync.conf` (non sincronizzata): `dest NOME HOST [profile=standard|hosting] [port=22]`, piu'
facoltativi `SSH_KEY`, `KNOWN_HOSTS`, `SCRIPTS_DIR` (predefinito `/var/lib/vz/pmgr-scripts`), `SNIPPETS_DIR`
(`/var/lib/vz/snippets`), `STATE_DIR` (`/var/lib/pmgr-sync`: blocco e registro `sync.log`). Tutto a whitelist: un valore
non valido ferma lo strumento prima di aprire qualunque connessione.

Cosa consegna, per destinazione:
- `snippets/`: gli snippet **generati dagli script** con `pmgr-os.sh --render` per il profilo della destinazione
  (D90: la casella "hosting" e' `profile=hosting` nella riga `dest`), piu' gli altri `.yaml`/`.yml` della cartella degli
  snippet della sorgente, cosi' come sono;
- `pmgr-scripts/`: la cartella degli script, **solo estensioni ammesse** (`.sh .yaml .yml .conf .md .txt .example .service .timer .local .sed`; gli altri file sono elencati in un avviso, mai scartati in silenzio),
  mai `cluster.conf`, chiavi, password, immagini/ISO, file nascosti, link simbolici; massimo 500 file, 1 MiB ciascuno, 20 MiB in tutto;
- in ciascuna cartella un `MANIFEST.sha256`. La destinazione e' "allineata" solo se **ogni hash** coincide con quello della
  sorgente; dopo una copia l'elenco remoto viene riletto e confrontato (se qualcosa non torna: errore, uscita 1).

Regole di sicurezza: **mai `--delete`** (un errore sulla sorgente non cancella le copie; i file "in piu'" sono solo segnalati),
anteprima di default e `--apply` esplicito, un'operazione alla volta (`flock`), registro con chi/cosa/quando e mai con i contenuti,
chiavi host accettate al primo contatto e poi fisse (`StrictHostKeyChecking=accept-new` con un `known_hosts` dedicato).
`--missing-only` copia solo i file mancanti e non sovrascrive quelli diversi.
Permessi sulle destinazioni: file nuovi 755 (`.sh`) o 644, proprietario `pmgr-sync`, cartelle `root:pmgr-sync` 2775. rsync senza `-p`
(proverebbe a cambiare i permessi della cartella radice, che e' di root) e con `-O`.

**Correzione collaterale**: `pmgr-os.sh --render` ora funziona anche quando `/etc/pmgr/cluster.conf` non e' leggibile (sulla sorgente
e' di root 0600 e `pmgr-sync` non lo legge): in quel caso il profilo e' `standard` salvo `--profile`. Prima il render falliva.

Test: `tests/test_snippets_sync.sh` (podman: due destinazioni vere con sshd, rsync e gate, una sorgente che esegue lo strumento come
`pmgr-sync`): 80 controlli lato sorgente (stato, anteprima senza modifiche, copia, profili diversi per alfa/beta, file mai consegnati,
modifica e ricopia parziale, `--missing-only`, link simbolici e file troppo grandi, JSON, destinazione irraggiungibile, blocco,
configurazioni e argomenti ostili, `discover`) e 14 lato destinazioni (proprietario, permessi, nulla fuori dalle due cartelle).

## Hardening dell'hosting (D93, approvato 2026-11-06, porta 25 lasciata aperta)

Solo profilo `hosting` (`pmgr-os.sh --profile hosting`, oppure `PROFILE=hosting` in `cluster.conf`):
- **sysctl** (`profiles/hosting/90-pmgr-hosting-sysctl.conf` -> `/etc/sysctl.d/90-pmgr-hosting.conf`): niente redirect ICMP ne' source routing,
  SYN cookies, risposte ICMP false ignorate, `kernel.kptr_restrict=2`, `kernel.dmesg_restrict=1`, `kernel.unprivileged_bpf_disabled=1`.
  Il "Log del kernel" del manager gira come root e non cambia. Le voci che il kernel non conosce non sono un errore.
- **fail2ban `recidive`** (`60-pmgr-recidive.local` -> `/etc/fail2ban/jail.d/`): 3 ban in un giorno = ban di una settimana; legge
  `/var/log/fail2ban.log` (lo snippet lo crea se manca: senza, la jail non partirebbe). Se la configurazione non si carica lo snippet
  toglie il file: meglio senza recidive che senza fail2ban.
- **SSH**: `ClientAliveInterval 300` e `ClientAliveCountMax 2` in `00-pmgr-hosting.conf` (rimessi a ogni avvio da `pmgr-ssh-enforce`).
- **Pacchetti ridotti**: tcpdump, iptraf, nethogs, iftop, arpwatch, nfs-common, cifs-utils, acct, nmon sono in un blocco
  `# pmgr:extra-tools-begin/end` degli snippet che l'hosting non installa (i clienti li aggiungono se servono). Le VM gia' esistenti
  **non** perdono pacchetti.
- NON fatti (decisione dell'utente): blocco della porta 25 in uscita, 443/udp, `AllowGroups`, limite connessioni SSH, log remoti.

VM esistenti: modulo `hardening-hosting` di `pmgr-vm-apply.sh` / `pmgr-vm-rollout.sh` (sysctl + recidive, con ripristino se fail2ban non
riparte; richiede fail2ban) e `ssh-hosting` (porta anche `ClientAlive`). `--verify --profile hosting` controlla tutto, compresi i
valori di sysctl in uso (non nei container, dove /proc/sys e' di sola lettura). Hosting = baseline + ssh-hosting + firewall-hosting + hardening-hosting.

## Rollout su molte VM esistenti (CALCOLO)

`bin/pmgr-rollout-inventory.sh` (elenchi dal manager, sola lettura) e `bin/pmgr-rollout-run.sh` (fasi `preflight`, `rootcheck`, `verify`, `check`,
`canary`, `apply`, `report`: una VM alla volta, a ondate, con stato ripetibile, si ferma al primo problema; le fasi che scrivono richiedono `--yes`).
Test: `tests/test_rollout_run.sh` (76 controlli con ssh e rollout finti, manager finto).

## Regola sulla sicurezza SSH delle VM esistenti (2026-11-10)

Se un gruppo ha **alzato** la sicurezza di una VM (per esempio disattivando l'accesso SSH con password), il rollout **non la riporta indietro**.
Quello che si garantisce e' solo l'**accesso amministrativo**: `tecnico` deve poter entrare con la chiave. Quindi il modulo `ssh-standard` per
le VM esistenti: non scrive mai `PasswordAuthentication` (lo controlla dopo l'applicazione e, se per errore fosse cambiato, ripristina), imposta
`PermitRootLogin no` e `PubkeyAuthentication yes` solo se mancano (in `/etc/ssh/sshd_config.d/00-pmgr-admin.conf`), e segnala (`[avviso]`, e
`[FALLITO]` in `--verify`) se l'utente con cui si lancia non puo' entrare: nessuna chiave valida, account bloccato, `.ssh` link simbolico,
`AllowUsers`/`DenyUsers` che lo escludono, file delle chiavi non standard. Il file `profiles/ssh-standard/00-pmgr-ssh.conf` (password si') resta
solo per gli snippet delle VM NUOVE. Per ripristinare la chiave di `tecnico` dove manca: `bin/pmgr-push-key-guest.sh` (dal nodo Proxmox, via guest agent).

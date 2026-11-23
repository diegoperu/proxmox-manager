package main

import (
	"errors"
	"fmt"
	"io"

	"proxmox-manager/internal/auth"
	"proxmox-manager/internal/config"
)

const userUsage = `uso:
  proxmox-manager user add <nome> --role reader|operator|admin
  proxmox-manager user reset <nome>          nuova password e nuovo TOTP, chiude le sessioni
  proxmox-manager user disable <nome>        disattiva (chiude le sessioni)
  proxmox-manager user enable <nome>
  proxmox-manager user role <nome> reader|operator|admin
  proxmox-manager user list
Va eseguito come l'utente del servizio, nella cartella del servizio (stessi config.json, database e chiave).`

// runUserCommand manages accounts from the command line (D141). Passwords are generated, never typed or
// passed as arguments (they would stay in the shell history and in ps); password and TOTP secret are printed
// once, to the terminal only.
func runUserCommand(args []string, out io.Writer) error {
	if len(args) < 1 {
		return errors.New(userUsage)
	}
	if err := config.Load(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	cfg := config.Get()
	store, err := auth.Open(cfg.DBPath, cfg.AuthKeyFile(), nil)
	if err != nil {
		return err
	}
	defer store.Close()

	need := func(n int) error {
		if len(args) != n {
			return errors.New(userUsage)
		}
		return nil
	}
	printCredentials := func(name, password, secret string) {
		fmt.Fprintf(out, "utente:   %s\npassword: %s\nTOTP:     %s\nURI:      %s\n", name, password, secret,
			auth.TOTPURI("ProxmoxManager", name, secret))
		fmt.Fprintln(out, "Password e segreto TOTP sono mostrati solo ora: consegnali di persona; la password va cambiata al primo accesso.")
	}
	switch args[0] {
	case "add":
		if err := need(4); err != nil {
			return err
		}
		if args[2] != "--role" {
			return errors.New(userUsage)
		}
		role, ok := auth.ParseRole(args[3])
		if !ok {
			return errors.New("ruolo non valido: reader, operator o admin")
		}
		password, err := auth.RandomPassword()
		if err != nil {
			return err
		}
		secret, err := store.CreateUser(args[1], role, password)
		if err != nil {
			return err
		}
		printCredentials(args[1], password, secret)
	case "reset":
		if err := need(2); err != nil {
			return err
		}
		password, err := auth.RandomPassword()
		if err != nil {
			return err
		}
		secret, err := store.ResetCredentials(args[1], password)
		if err != nil {
			return err
		}
		printCredentials(args[1], password, secret)
	case "disable", "enable":
		if err := need(2); err != nil {
			return err
		}
		if err := store.SetDisabled(args[1], args[0] == "disable"); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s: %s\n", args[1], map[bool]string{true: "disattivato", false: "attivato"}[args[0] == "disable"])
	case "role":
		if err := need(3); err != nil {
			return err
		}
		role, ok := auth.ParseRole(args[2])
		if !ok {
			return errors.New("ruolo non valido: reader, operator o admin")
		}
		if err := store.SetRole(args[1], role); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s: ruolo %s\n", args[1], role)
	case "list":
		if err := need(1); err != nil {
			return err
		}
		users, err := store.ListUsers()
		if err != nil {
			return err
		}
		for _, u := range users {
			state := "attivo"
			if u.Disabled {
				state = "disattivato"
			}
			fmt.Fprintf(out, "%-24s %-9s %s\n", u.Username, u.RoleName, state)
		}
	default:
		return errors.New(userUsage)
	}
	return nil
}

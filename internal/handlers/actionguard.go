package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
)

// Power actions and node commands are forwarded to Proxmox as part of an API path: with roles (voce 20)
// they are a privilege boundary, so the action is a whitelist, node and VMID are validated and the request
// body may not carry extra Proxmox parameters (before 2026-11-23 any action string and any body key were
// forwarded, e.g. an operator could have sent parameters the page never sends).
var (
	vmActions    = map[string]bool{"start": true, "stop": true, "shutdown": true, "reboot": true, "suspend": true, "resume": true, "reset": true}
	lxcActions   = map[string]bool{"start": true, "stop": true, "shutdown": true, "reboot": true, "suspend": true, "resume": true}
	nodeCommands = map[string]bool{"reboot": true, "shutdown": true}

	errBadTarget = errors.New("nodo o VMID non validi")
	errBadAction = errors.New("azione non consentita")
	errParams    = errors.New("parametri non ammessi: il corpo deve essere vuoto o {}")
)

func validVMID(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil && n >= 100 && n <= 999999999
}

// noParams accepts an empty body or an empty JSON object, nothing else (at most 1 KB is read).
func noParams(w http.ResponseWriter, r *http.Request) error {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
	if err != nil {
		return errParams
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil || len(m) != 0 {
		return errParams
	}
	return nil
}

// decodeBatch reads a Batch request strictly: known fields only, no Proxmox parameters, at most 500 VMs,
// every target valid, the action in the whitelist (per-VM type checks happen in the loop).
func decodeBatch(w http.ResponseWriter, r *http.Request) (BatchRequest, error) {
	var req BatchRequest
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		return req, errors.New("richiesta troppo grande")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || dec.More() {
		return req, errors.New("richiesta non valida")
	}
	if len(req.Params) != 0 {
		return req, errParams
	}
	if !vmActions[req.Action] {
		return req, errBadAction
	}
	if len(req.VMs) == 0 || len(req.VMs) > 500 {
		return req, errors.New("da 1 a 500 VM per richiesta")
	}
	for _, vm := range req.VMs {
		if !nodeNamePattern.MatchString(vm.Node) || vm.VMID < 100 || vm.VMID > 999999999 || (vm.Type != "" && vm.Type != "qemu" && vm.Type != "lxc") {
			return req, errBadTarget
		}
	}
	return req, nil
}

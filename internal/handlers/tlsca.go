package handlers

// Cluster CA for the TLS verification of the connections to Proxmox (D163).
//
// GET    /api/clusters/{idx}/tls  status: CA configured or not, its key fingerprint, and a live check of the cluster.
// PUT    /api/clusters/{idx}/tls  {pem}: stores the CA only if the certificate the cluster presents NOW is signed by it.
// DELETE /api/clusters/{idx}/tls  forgets the CA (connections go back to unverified).
// PUT and DELETE are administrator actions with the TOTP step-up (routeAccess) and appear in the action log.

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"proxmox-manager/internal/config"
	"proxmox-manager/internal/pvetls"
)

type tlsStatus struct {
	Configured  bool   `json:"configured"`
	Fingerprint string `json:"fingerprint"`
	Subject     string `json:"subject"`
	NotAfter    string `json:"not_after"`
	Check       string `json:"check"` // ok | failed | unverified
	Message     string `json:"message"`
	Issuer      string `json:"issuer"` // issuer of the certificate the cluster presents (to recognise the cluster CA)
	Help        string `json:"help"`
}

const tlsHelp = "Sul nodo, da root: cat /etc/pve/pve-root-ca.pem (certificato pubblico della CA del cluster). Copia tutto il testo, da -----BEGIN a -----END."

// probeTLS opens a TLS connection to the cluster with the CA (or unverified when caPEM is empty) and returns
// the issuer of the certificate the server presents.
func probeTLS(rawURL, caPEM string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", errors.New("indirizzo del cluster non valido (serve https)")
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "443")
	}
	d := &net.Dialer{Timeout: 8 * time.Second}
	conn, err := tls.DialWithDialer(d, "tcp", host, pvetls.Config(caPEM))
	if err != nil {
		for _, known := range []error{pvetls.ErrUntrusted, pvetls.ErrCertValidity, pvetls.ErrNoCert, pvetls.ErrBadCA} {
			if strings.Contains(err.Error(), known.Error()) {
				return "", known
			}
		}
		return "", errors.New("connessione TLS al cluster non riuscita")
	}
	defer conn.Close()
	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return "", pvetls.ErrNoCert
	}
	return state.PeerCertificates[0].Issuer.String(), nil
}

func buildTLSStatus(cl config.ClusterConfig) tlsStatus {
	st := tlsStatus{Help: tlsHelp}
	issuer, err := probeTLS(cl.URL, cl.TLSCA)
	st.Issuer = issuer
	if cl.TLSCA == "" {
		st.Check = "unverified"
		st.Message = "Il certificato del cluster non viene verificato: chi si mettesse in mezzo sulla rete potrebbe leggere il token. Incolla la CA del cluster per attivare la verifica."
		if err != nil {
			st.Message = "Il cluster non risponde in TLS: " + err.Error()
		}
		return st
	}
	st.Configured = true
	if ca, perr := pvetls.ParseCA(cl.TLSCA); perr == nil {
		st.Fingerprint, st.Subject = pvetls.Fingerprint(ca), ca.Subject.String()
		st.NotAfter = ca.NotAfter.UTC().Format("2006-01-02")
	}
	if err != nil {
		st.Check, st.Message = "failed", "Verifica non riuscita: "+err.Error()+". Le connessioni a questo cluster sono bloccate finche' la CA non e' corretta (se la CA e' stata ruotata con pvecm ca rotate, incolla quella nuova)."
		return st
	}
	st.Check, st.Message = "ok", "Certificato del cluster verificato con la CA configurata."
	return st
}

func (h *Handler) GetClusterTLS(w http.ResponseWriter, r *http.Request) {
	idx, ok := networkSettingsIndex(w, r)
	if !ok {
		return
	}
	cl, err := config.GetCluster(idx)
	if err != nil {
		writeError(w, errors.New("cluster non configurato"), http.StatusBadRequest)
		return
	}
	writeJSON(w, buildTLSStatus(cl))
}

func (h *Handler) PutClusterTLS(w http.ResponseWriter, r *http.Request) {
	idx, ok := networkSettingsIndex(w, r)
	if !ok {
		return
	}
	var req struct {
		PEM string `json:"pem"`
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<10))
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err != nil || dec.Decode(&req) != nil || dec.More() {
		writeError(w, errors.New("richiesta non valida"), http.StatusBadRequest)
		return
	}
	norm, err := pvetls.Normalize(req.PEM)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	cl, err := config.GetCluster(idx)
	if err != nil {
		writeError(w, errors.New("cluster non configurato"), http.StatusBadRequest)
		return
	}
	// Never store a CA the cluster does not actually use: the check is made now, before saving.
	if _, err := probeTLS(cl.URL, norm); err != nil {
		writeError(w, errors.New("la CA non corrisponde al cluster: "+err.Error()+"; nulla e' stato salvato"), http.StatusConflict)
		return
	}
	if err := config.SetClusterTLSCA(idx, norm); err != nil {
		log.Printf("tls: save CA cluster %d: %v", idx, err)
		writeError(w, errors.New("impossibile salvare la configurazione"), http.StatusInternalServerError)
		return
	}
	ca, _ := pvetls.ParseCA(norm)
	log.Printf("tls: CA of cluster %d (%s) configured: %s", idx, cl.Label, pvetls.Fingerprint(ca))
	cl.TLSCA = norm
	writeJSON(w, buildTLSStatus(cl))
}

func (h *Handler) DeleteClusterTLS(w http.ResponseWriter, r *http.Request) {
	idx, ok := networkSettingsIndex(w, r)
	if !ok {
		return
	}
	if err := config.SetClusterTLSCA(idx, ""); err != nil {
		log.Printf("tls: clear CA cluster %d: %v", idx, err)
		writeError(w, errors.New("impossibile salvare la configurazione"), http.StatusInternalServerError)
		return
	}
	log.Printf("tls: CA of cluster %d removed, connections no longer verified", idx)
	cl, _ := config.GetCluster(idx)
	writeJSON(w, buildTLSStatus(cl))
}

package handlers

// Cluster CA for the TLS verification of the connections to Proxmox (D163).
//
// GET    /api/clusters/{idx}/tls  status: CA configured or not, its key fingerprint, and a live check of the cluster.
// PUT    /api/clusters/{idx}/tls  {pem}: stores the CA only if the certificate the cluster presents NOW is signed by it.
// DELETE /api/clusters/{idx}/tls  forgets the CA (connections go back to unverified).
// PUT and DELETE are administrator actions with the TOTP step-up (routeAccess) and appear in the action log.

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"proxmox-manager/internal/api"
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
}

// tlsFingerprintCommand prints, on a node, the CA key fingerprint in the same format as pvetls.Fingerprint.
const tlsFingerprintCommand = "openssl x509 -in /etc/pve/pve-root-ca.pem -noout -pubkey | openssl pkey -pubin -outform der | openssl dgst -sha256 -binary | base64 | tr -d '=' | sed 's/^/SHA256:/'"

var fingerprintLine = regexp.MustCompile(`SHA256:[A-Za-z0-9+/]{43}`)

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
	st := tlsStatus{}
	issuer, err := probeTLS(cl.URL, cl.TLSCA)
	st.Issuer = issuer
	if cl.TLSCA == "" {
		st.Check = "unverified"
		st.Message = "Il certificato del cluster non viene verificato: chi si mettesse in mezzo sulla rete potrebbe leggere il token. Per attivare la verifica segui i passi qui sotto, a partire da \"Leggi la CA dal cluster\"."
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
		st.Check, st.Message = "failed", "Verifica non riuscita: "+err.Error()+". Le connessioni a questo cluster sono bloccate finche' la CA non e' corretta. Se la CA e' stata ruotata (pvecm ca rotate), usa \"Oppure incolla il certificato della CA a mano\" qui sotto."
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
		PEM         string `json:"pem"`
		Fingerprint string `json:"fingerprint"`
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<10))
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err != nil || dec.Decode(&req) != nil || dec.More() || (req.PEM == "") == (req.Fingerprint == "") {
		writeError(w, errors.New("richiesta non valida: serve il certificato o l'impronta letta sul nodo"), http.StatusBadRequest)
		return
	}
	cl, err := config.GetCluster(idx)
	if err != nil {
		writeError(w, errors.New("cluster non configurato"), http.StatusBadRequest)
		return
	}
	var norm string
	if req.PEM != "" {
		if norm, err = pvetls.Normalize(req.PEM); err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
	} else {
		// The fingerprint was read on the node (trusted channel); the CA is read again here from the cluster and
		// must have exactly that fingerprint. The page's own comparison is only a convenience.
		want := fingerprintLine.FindString(req.Fingerprint)
		if want == "" || len(req.Fingerprint) > 4096 {
			writeError(w, errors.New("impronta non riconosciuta: incolla la riga SHA256:... stampata dal comando sul nodo"), http.StatusBadRequest)
			return
		}
		cand, err := h.tlsCandidate(r.Context(), cl)
		if err != nil {
			writeError(w, err, http.StatusBadGateway)
			return
		}
		if cand.Fingerprint != want {
			log.Printf("tls: CA fingerprint MISMATCH for cluster %d (%s): node %s, received %s", idx, cl.Label, want, cand.Fingerprint)
			writeError(w, errors.New("DISCREPANZA: l'impronta letta sul nodo non corrisponde alla CA ricevuta dal cluster (possibile intercettazione, oppure il comando e' stato eseguito su un altro cluster). Nulla e' stato salvato"), http.StatusConflict)
			return
		}
		norm = cand.pem
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

// ── reading the CA from the cluster API (D163, compared with the fingerprint printed on a node) ──

type tlsCandidate struct {
	Fingerprint string `json:"fingerprint"`
	Subject     string `json:"subject"`
	NotAfter    string `json:"not_after"`
	Node        string `json:"node"`
	Command     string `json:"command"`
	Verified    bool   `json:"read_over_verified_connection"`
	pem         string
}

// tlsCandidate reads pve-root-ca.pem through the API. With a CA already configured the read uses the verified
// connection only: falling back to an unverified one would send the API token to whoever might be in the middle.
func (h *Handler) tlsCandidate(ctx context.Context, cl config.ClusterConfig) (tlsCandidate, error) {
	c := tlsCandidateClient(cl)
	raw, err := c.Get("/nodes")
	if err != nil {
		if cl.TLSCA != "" {
			return tlsCandidate{}, errors.New("il cluster non supera la verifica con la CA configurata: se la CA e' stata ruotata usa l'incolla manuale del certificato (non invia il token)")
		}
		return tlsCandidate{}, errors.New("impossibile leggere i nodi del cluster")
	}
	var nodes []struct {
		Node   string `json:"node"`
		Status string `json:"status"`
	}
	if json.Unmarshal(raw, &nodes) != nil {
		return tlsCandidate{}, errors.New("elenco dei nodi non valido")
	}
	for _, n := range nodes {
		if n.Status != "online" || !nodeNamePattern.MatchString(n.Node) {
			continue
		}
		raw, err := c.Get("/nodes/" + n.Node + "/certificates/info")
		if err != nil {
			continue
		}
		var certs []struct {
			Filename string `json:"filename"`
			PEM      string `json:"pem"`
		}
		if json.Unmarshal(raw, &certs) != nil {
			continue
		}
		for _, x := range certs {
			if x.Filename != "pve-root-ca.pem" {
				continue
			}
			norm, err := pvetls.Normalize(x.PEM)
			if err != nil {
				return tlsCandidate{}, errors.New("il cluster ha restituito una CA non valida")
			}
			ca, _ := pvetls.ParseCA(norm)
			return tlsCandidate{Fingerprint: pvetls.Fingerprint(ca), Subject: ca.Subject.String(), NotAfter: ca.NotAfter.UTC().Format("2006-01-02"),
				Node: n.Node, Command: tlsFingerprintCommand, Verified: cl.TLSCA != "", pem: norm}, nil
		}
	}
	return tlsCandidate{}, errors.New("nessun nodo in linea ha restituito la CA (serve Sys.Audit)")
}

// tlsCandidateClient is a variable so tests can count which connection is used.
var tlsCandidateClient = func(cl config.ClusterConfig) interface {
	Get(string) (json.RawMessage, error)
} {
	return api.NewClientCA(cl.URL, cl.APIToken, cl.TLSCA)
}

func (h *Handler) GetClusterTLSCandidate(w http.ResponseWriter, r *http.Request) {
	idx, ok := networkSettingsIndex(w, r)
	if !ok {
		return
	}
	cl, err := config.GetCluster(idx)
	if err != nil {
		writeError(w, errors.New("cluster non configurato"), http.StatusBadRequest)
		return
	}
	cand, err := h.tlsCandidate(r.Context(), cl)
	if err != nil {
		writeError(w, err, http.StatusBadGateway)
		return
	}
	writeJSON(w, cand)
}

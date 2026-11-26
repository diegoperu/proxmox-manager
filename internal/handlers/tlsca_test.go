package handlers

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"proxmox-manager/internal/config"
	"proxmox-manager/internal/pvetls"
)

type testCA struct {
	key  *ecdsa.PrivateKey
	cert *x509.Certificate
	pem  string
}

func makeTestCA(t *testing.T) testCA {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Proxmox Virtual Environment", Organization: []string{"PVE Cluster Manager CA"}},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return testCA{key, cert, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))}
}

func (ca testCA) leaf(t *testing.T) tls.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "node-1.local"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// A fake Proxmox over TLS whose certificate can be swapped (to simulate a MITM or a CA rotation).
type swappableCert struct {
	cur   *tls.Certificate
	caPEM string // CA returned by the fake API (what a node or a MITM would serve)
}

func tlsTestRouter(t *testing.T, ca testCA) (http.Handler, *swappableCert) {
	t.Helper()
	sw := &swappableCert{caPEM: ca.pem}
	leaf := ca.leaf(t)
	sw.cur = &leaf
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api2/json/nodes/n1/certificates/info":
			b, _ := json.Marshal(map[string]any{"data": []map[string]string{{"filename": "pve-ssl.pem", "pem": "x"}, {"filename": "pve-root-ca.pem", "pem": sw.caPEM}}})
			w.Write(b)
		default:
			w.Write([]byte(`{"data":[{"node":"n1","status":"online"}]}`))
		}
	}))
	// GetConfigForClient is consulted on every handshake (GetCertificate is skipped without SNI, as with an IP).
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{leaf}, GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
		return &tls.Config{Certificates: []tls.Certificate{*sw.cur}}, nil
	}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	reportSettingsTestRouter(t) // temporary config.json, restored after the test
	cfg := config.Get()
	cfg.Clusters = []config.ClusterConfig{{Label: "C", URL: srv.URL, APIToken: "u@pve!t=secret", Default: true}}
	if err := config.Update(cfg); err != nil {
		t.Fatal(err)
	}
	h := &Handler{}
	r := chi.NewRouter()
	r.Get("/api/clusters/{idx}/tls", h.GetClusterTLS)
	r.Get("/api/clusters/{idx}/tls/candidate", h.GetClusterTLSCandidate)
	r.Put("/api/clusters/{idx}/tls", h.PutClusterTLS)
	r.Delete("/api/clusters/{idx}/tls", h.DeleteClusterTLS)
	r.Put("/api/clusters/{idx}", h.UpdateCluster)
	r.Get("/api/nodes", h.GetNodes)
	return r, sw
}

func tlsState(t *testing.T, body string) tlsStatus {
	t.Helper()
	var st tlsStatus
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return st
}

func TestClusterTLSFlow(t *testing.T) {
	ca, other := makeTestCA(t), makeTestCA(t)
	r, sw := tlsTestRouter(t, ca)
	put := func(p string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]string{"pem": p})
		return reportRequest(r, "PUT", "/api/clusters/0/tls", string(b))
	}

	w := reportRequest(r, "GET", "/api/clusters/0/tls", "")
	if st := tlsState(t, w.Body.String()); st.Configured || st.Check != "unverified" || !strings.Contains(st.Issuer, "PVE Cluster Manager CA") {
		t.Fatalf("initial %+v", st)
	}
	// a CA that did not sign the cluster certificate is refused and nothing is stored
	if w = put(other.pem); w.Code != http.StatusConflict || config.Get().Clusters[0].TLSCA != "" {
		t.Fatalf("wrong CA %d %s", w.Code, w.Body)
	}
	for _, bad := range []string{"", "garbage", "-----BEGIN PRIVATE KEY-----\nAA\n-----END PRIVATE KEY-----\n"} {
		if w = put(bad); w.Code != http.StatusBadRequest {
			t.Fatalf("%q: %d", bad, w.Code)
		}
	}
	if w = reportRequest(r, "PUT", "/api/clusters/0/tls", `{"pem":"x","extra":1}`); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown field %d", w.Code)
	}
	// the right CA (pasted with surrounding blanks) is stored normalized and the cluster is verified
	if w = put("\n  " + ca.pem + "\n"); w.Code != 200 {
		t.Fatalf("right CA %d %s", w.Code, w.Body)
	}
	if st := tlsState(t, w.Body.String()); !st.Configured || st.Check != "ok" || !strings.HasPrefix(st.Fingerprint, "SHA256:") {
		t.Fatalf("after put %+v", st)
	}
	if config.Get().Clusters[0].TLSCA != ca.pem {
		t.Fatal("CA not stored in canonical form")
	}
	// normal API calls now go through the verified connection
	if w = reportRequest(r, "GET", "/api/nodes", ""); w.Code != 200 {
		t.Fatalf("verified call %d %s", w.Code, w.Body)
	}
	// editing the cluster (label) keeps the CA
	if w = reportRequest(r, "PUT", "/api/clusters/0", `{"label":"C2","url":"","api_token":"","username":""}`); w.Code != 200 || config.Get().Clusters[0].TLSCA != ca.pem {
		t.Fatalf("update cluster %d %s", w.Code, w.Body)
	}
	// a server with a certificate from another CA (MITM, or a rotated CA) is refused for every call
	foreign := other.leaf(t)
	sw.cur = &foreign
	if st := tlsState(t, reportRequest(r, "GET", "/api/clusters/0/tls", "").Body.String()); st.Check != "failed" {
		t.Fatalf("foreign certificate %+v", st)
	}
	if w = reportRequest(r, "GET", "/api/nodes", ""); w.Code == 200 || strings.Contains(w.Body.String(), "n1") {
		t.Fatalf("call through an untrusted certificate succeeded: %d %s", w.Code, w.Body)
	}
	// removing the CA returns to unverified connections
	if w = reportRequest(r, "DELETE", "/api/clusters/0/tls", ""); w.Code != 200 || config.Get().Clusters[0].TLSCA != "" {
		t.Fatalf("delete %d %s", w.Code, w.Body)
	}
	if st := tlsState(t, w.Body.String()); st.Check != "unverified" {
		t.Fatalf("after delete %+v", st)
	}
}

func TestReadinessReportsTLSState(t *testing.T) {
	base := readinessResult{ClusterChecks: []readinessCheck{}, Summary: readinessSummary{OK: 1}}
	got := withTLSCheck(base, false)
	if len(base.ClusterChecks) != 0 || len(got.ClusterChecks) != 1 || got.ClusterChecks[0].Status != "warn" || got.Summary.Warn != 1 {
		t.Fatalf("%+v", got)
	}
	if got = withTLSCheck(base, true); got.ClusterChecks[0].Status != "ok" || got.Summary.OK != 2 {
		t.Fatalf("%+v", got)
	}
}

func fpOfPEM(t *testing.T, ca testCA) string {
	t.Helper()
	return pvetlsFingerprint(ca.cert)
}

// The CA read through the API is saved only if its fingerprint equals the one printed on the node.
func TestClusterTLSFromAPIWithNodeFingerprint(t *testing.T) {
	ca, other := makeTestCA(t), makeTestCA(t)
	r, sw := tlsTestRouter(t, ca)
	putFP := func(fp string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]string{"fingerprint": fp})
		return reportRequest(r, "PUT", "/api/clusters/0/tls", string(b))
	}
	w := reportRequest(r, "GET", "/api/clusters/0/tls/candidate", "")
	var cand tlsCandidate
	json.Unmarshal(w.Body.Bytes(), &cand)
	if w.Code != 200 || cand.Fingerprint != fpOfPEM(t, ca) || cand.Node != "n1" || !strings.Contains(cand.Command, "pve-root-ca.pem") || cand.Verified {
		t.Fatalf("candidate %d %+v", w.Code, cand)
	}
	// wrong fingerprint (another cluster, or a MITM): nothing saved
	if w = putFP(fpOfPEM(t, other)); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "DISCREPANZA") || config.Get().Clusters[0].TLSCA != "" {
		t.Fatalf("mismatch %d %s", w.Code, w.Body)
	}
	for _, body := range []string{`{}`, `{"pem":"x","fingerprint":"SHA256:x"}`, `{"fingerprint":"not a fingerprint"}`} {
		if w = reportRequest(r, "PUT", "/api/clusters/0/tls", body); w.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", body, w.Code)
		}
	}
	// the whole terminal output pasted (prompt included) is accepted: the fingerprint line is found
	pasted := "root@n1:~# openssl x509 ... \n" + fpOfPEM(t, ca) + "\nroot@n1:~# "
	if w = putFP(pasted); w.Code != 200 || config.Get().Clusters[0].TLSCA != ca.pem {
		t.Fatalf("match %d %s", w.Code, w.Body)
	}
	// with a CA configured, a server that now presents a foreign certificate is never read unverified
	foreign := other.leaf(t)
	sw.cur, sw.caPEM = &foreign, other.pem
	if w = reportRequest(r, "GET", "/api/clusters/0/tls/candidate", ""); w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "incolla manuale") {
		t.Fatalf("no unverified fallback expected: %d %s", w.Code, w.Body)
	}
}

// A man in the middle before any CA is configured: it presents its own certificate and serves its own CA through
// the API; the fingerprint printed on the real node does not match, so nothing is saved.
func TestClusterTLSFromAPIDetectsMITM(t *testing.T) {
	ca, mitm := makeTestCA(t), makeTestCA(t)
	r, sw := tlsTestRouter(t, ca)
	leaf := mitm.leaf(t)
	sw.cur, sw.caPEM = &leaf, mitm.pem
	b, _ := json.Marshal(map[string]string{"fingerprint": fpOfPEM(t, ca)})
	w := reportRequest(r, "PUT", "/api/clusters/0/tls", string(b))
	if w.Code != http.StatusConflict || config.Get().Clusters[0].TLSCA != "" {
		t.Fatalf("MITM not detected: %d %s", w.Code, w.Body)
	}
}

var pvetlsFingerprint = pvetls.Fingerprint

package pvetls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type pki struct {
	key  *ecdsa.PrivateKey
	cert *x509.Certificate
	pem  string
}

func newCA(t *testing.T, key *ecdsa.PrivateKey, notBefore, notAfter time.Time) pki {
	t.Helper()
	if key == nil {
		var err error
		if key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
			t.Fatal(err)
		}
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(notBefore.Unix()), Subject: pkix.Name{CommonName: "Proxmox Virtual Environment", Organization: []string{"PVE Cluster Manager CA"}},
		NotBefore: notBefore, NotAfter: notAfter, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return pki{key, cert, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))}
}

func newLeaf(t *testing.T, ca pki, notBefore, notAfter time.Time) (tls.Certificate, []byte) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "node-1.local"}, NotBefore: notBefore, NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, der
}

func TestParseCA(t *testing.T) {
	now := time.Now()
	ca := newCA(t, nil, now.Add(-time.Hour), now.Add(time.Hour))
	if _, err := ParseCA(ca.pem); err != nil {
		t.Fatal(err)
	}
	if n, err := Normalize("\n  " + ca.pem + "\n\n"); err != nil || n != ca.pem {
		t.Fatalf("normalize %v", err)
	}
	_, leafDER := newLeaf(t, ca, now.Add(-time.Hour), now.Add(time.Hour))
	leafPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}))
	noSign := &x509.Certificate{SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: "x"}, NotBefore: now, NotAfter: now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature}
	der, _ := x509.CreateCertificate(rand.Reader, noSign, noSign, &ca.key.PublicKey, ca.key)
	for name, bad := range map[string]string{
		"empty": "", "garbage": "not a certificate", "leaf (not a CA)": leafPEM, "two blocks": ca.pem + ca.pem,
		"trailing text": ca.pem + "rm -rf /", "leading text": "rm -rf /\n" + ca.pem, "private key": "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n",
		"CA without certSign": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), "too large": strings.Repeat("A", maxPEM+1),
	} {
		if _, err := ParseCA(bad); !errors.Is(err, ErrBadCA) {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestVerifyLeaf(t *testing.T) {
	now := time.Now()
	ca := newCA(t, nil, now.Add(-time.Hour), now.Add(time.Hour))
	other := newCA(t, nil, now.Add(-time.Hour), now.Add(time.Hour))
	_, good := newLeaf(t, ca, now.Add(-time.Hour), now.Add(time.Hour))
	_, foreign := newLeaf(t, other, now.Add(-time.Hour), now.Add(time.Hour))
	_, expired := newLeaf(t, ca, now.Add(-2*time.Hour), now.Add(-time.Hour))
	_, future := newLeaf(t, ca, now.Add(time.Hour), now.Add(2*time.Hour))
	if err := VerifyLeaf([][]byte{good}, ca.cert, now); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		raw  [][]byte
		want error
	}{
		"other CA": {[][]byte{foreign}, ErrUntrusted}, "expired": {[][]byte{expired}, ErrCertValidity},
		"not yet valid": {[][]byte{future}, ErrCertValidity}, "none": {nil, ErrNoCert}, "garbage": {[][]byte{[]byte("x")}, ErrNoCert},
		// a chain whose FIRST certificate is foreign must fail even if a good one follows
		"foreign first": {[][]byte{foreign, good}, ErrUntrusted},
	} {
		if err := VerifyLeaf(c.raw, ca.cert, now); !errors.Is(err, c.want) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// PVE 9.3 renews the CA certificate with the same key: same fingerprint, old node certificates still verify,
	// even when the stored CA certificate itself has expired.
	renewed := newCA(t, ca.key, now.Add(-time.Minute), now.Add(10*365*24*time.Hour))
	oldStored := newCA(t, ca.key, now.Add(-3*time.Hour), now.Add(-2*time.Hour))
	if Fingerprint(renewed.cert) != Fingerprint(ca.cert) || Fingerprint(other.cert) == Fingerprint(ca.cert) {
		t.Fatal("fingerprint must follow the key")
	}
	if err := VerifyLeaf([][]byte{good}, oldStored.cert, now); err != nil {
		t.Fatalf("renewed CA, same key: %v", err)
	}
	if !strings.HasPrefix(Fingerprint(ca.cert), "SHA256:") {
		t.Fatal(Fingerprint(ca.cert))
	}
}

// A real TLS connection: with the right CA it works, with another CA or a broken one it fails, without a CA it
// works unverified (legacy).
func TestConfigOverRealTLS(t *testing.T) {
	now := time.Now()
	ca := newCA(t, nil, now.Add(-time.Hour), now.Add(time.Hour))
	other := newCA(t, nil, now.Add(-time.Hour), now.Add(time.Hour))
	leaf, _ := newLeaf(t, ca, now.Add(-time.Hour), now.Add(time.Hour))
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{leaf}}
	srv.StartTLS()
	defer srv.Close()
	get := func(caPEM string) error {
		c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: Config(caPEM)}}
		resp, err := c.Get(srv.URL)
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	if err := get(ca.pem); err != nil {
		t.Fatalf("right CA: %v", err)
	}
	if err := get(other.pem); err == nil || !strings.Contains(err.Error(), ErrUntrusted.Error()) {
		t.Fatalf("other CA accepted: %v", err)
	}
	if err := get("garbage"); err == nil {
		t.Fatal("invalid CA must fail closed")
	}
	if err := get(""); err != nil {
		t.Fatalf("legacy unverified: %v", err)
	}
	if Config(ca.pem).MinVersion != tls.VersionTLS12 {
		t.Fatal("TLS 1.2 minimum")
	}
}

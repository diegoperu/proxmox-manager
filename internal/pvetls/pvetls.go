// Package pvetls verifies the TLS certificate of a Proxmox VE cluster against the cluster's own CA (D163).
//
// Proxmox nodes present only their own certificate (pve-ssl.pem), signed by the private "PVE Cluster Manager CA"
// of the cluster (/etc/pve/pve-root-ca.pem). The manager connects to nodes by IP address, so the usual hostname
// check cannot apply; instead an administrator gives the manager the CA certificate copied from a node (a trusted
// channel) and every connection must present a certificate signed by that CA's key and currently valid.
//
// The CA certificate's own validity dates are deliberately not checked: Proxmox VE 9.3 renews the CA certificate
// with the same key, and the signature of the node certificate is what proves the server belongs to the cluster.
// A rotated CA key (pvecm ca rotate) stops every connection until the new CA is entered: that is the intended
// effect. Without a configured CA the connection is not verified (legacy behaviour, reported as such).
package pvetls

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"strings"
	"time"
)

const maxPEM = 16 << 10

var (
	ErrBadCA        = errors.New("certificato della CA non valido: incolla il contenuto di /etc/pve/pve-root-ca.pem")
	ErrNoCert       = errors.New("il server non ha presentato un certificato")
	ErrUntrusted    = errors.New("il certificato del server non e' firmato dalla CA del cluster configurata")
	ErrCertValidity = errors.New("il certificato del server e' scaduto o non ancora valido")
)

// ParseCA accepts exactly one PEM CERTIFICATE block that is a CA able to sign certificates.
func ParseCA(text string) (*x509.Certificate, error) {
	if len(text) > maxPEM {
		return nil, ErrBadCA
	}
	text = strings.TrimSpace(text) // copied from a terminal: surrounding blanks are normal, anything else is not
	if !strings.HasPrefix(text, "-----BEGIN CERTIFICATE-----") {
		return nil, ErrBadCA
	}
	block, rest := pem.Decode([]byte(text))
	if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 || strings.TrimSpace(string(rest)) != "" {
		return nil, ErrBadCA
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !cert.BasicConstraintsValid || !cert.IsCA {
		return nil, ErrBadCA
	}
	if cert.KeyUsage != 0 && cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, ErrBadCA
	}
	return cert, nil
}

// Normalize returns the canonical PEM of a valid CA (what is stored in config.json).
func Normalize(text string) (string, error) {
	cert, err := ParseCA(text)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})), nil
}

// Fingerprint is the SHA-256 of the CA public key (SubjectPublicKeyInfo): it stays the same across renewals.
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// VerifyLeaf checks the first certificate the server presented: signed by ca, and within its validity
// (with five minutes of tolerance for clock differences).
func VerifyLeaf(rawCerts [][]byte, ca *x509.Certificate, now time.Time) error {
	if len(rawCerts) == 0 {
		return ErrNoCert
	}
	leaf, err := x509.ParseCertificate(rawCerts[0])
	if err != nil {
		return ErrNoCert
	}
	if now.Add(5*time.Minute).Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return ErrCertValidity
	}
	if err := leaf.CheckSignatureFrom(ca); err != nil {
		return ErrUntrusted
	}
	return nil
}

// Config returns the TLS settings for connections to a cluster.
//   - caPEM empty: the certificate is not verified (legacy, the page says so);
//   - caPEM valid: the server certificate must be signed by that CA;
//   - caPEM invalid: every connection fails (fail closed, never a silent fallback).
func Config(caPEM string) *tls.Config {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	// Standard verification needs a hostname that matches the certificate; the manager connects by IP and the
	// certificate names the node, so it is replaced by VerifyPeerCertificate below.
	cfg.InsecureSkipVerify = true //nolint:gosec // verified in VerifyPeerCertificate when a CA is configured
	if caPEM == "" {
		return cfg
	}
	ca, err := ParseCA(caPEM)
	if err != nil {
		cfg.VerifyPeerCertificate = func([][]byte, [][]*x509.Certificate) error { return ErrBadCA }
		return cfg
	}
	cfg.VerifyPeerCertificate = func(raw [][]byte, _ [][]*x509.Certificate) error {
		return VerifyLeaf(raw, ca, time.Now())
	}
	return cfg
}

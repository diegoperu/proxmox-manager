package handlers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
)

// externalClient is separate from the Proxmox client: system CAs, environment
// proxy and redirects confined to the original host and scheme.
func externalClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSClientConfig = nil
	return &http.Client{Transport: t, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 || req.URL.User != nil || !strings.EqualFold(req.URL.Host, via[0].URL.Host) || req.URL.Scheme != via[0].URL.Scheme {
			return errors.New("redirect non ammesso")
		}
		return nil
	}}
}

// externalRead returns HTTP status separately so callers can recognise fixed
// API outcomes without exposing or parsing upstream error bodies.
func externalRead(ctx context.Context, client *http.Client, address string, limit int64, count *int64) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", "proxmox-manager public check")
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, errors.New("risposta HTTP non valida")
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	*count += int64(len(b))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if int64(len(b)) > limit {
		return nil, resp.StatusCode, errors.New("risposta troppo grande")
	}
	return b, resp.StatusCode, nil
}

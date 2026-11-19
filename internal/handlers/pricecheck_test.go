package handlers

import (
	"bytes"
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"proxmox-manager/internal/config"
)

type priceRoundTrip func(*http.Request) (*http.Response, error)

func (f priceRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func priceFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, e := os.ReadFile("testdata/" + name)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func priceFake(body []byte) *http.Client {
	return &http.Client{Transport: priceRoundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header), Request: r}, nil
	})}
}
func TestPriceFixtures(t *testing.T) {
	azure := priceFixture(t, "azure_d4s_v5_italynorth.json")
	ovh := priceFixture(t, "ovh_catalog_cloud_it_excerpt.json")
	var p priceProposal
	var n int64
	checkAzure(context.Background(), priceFake(azure), "https://prices.azure.com/api/retail/prices", &p, &n)
	if p.RAMGBHour == nil || *p.RAMGBHour != 0.2011/46 || *p.VCPUHour != *p.RAMGBHour*7.5 || len(p.Items) != 1 || len(p.Errors) != 3 {
		t.Fatalf("azure: %+v", p)
	}
	p = priceProposal{}
	checkOVH(context.Background(), priceFake(ovh), "https://api.ovh.com/catalog", &p, &n)
	if p.RAMGBHour == nil || *p.RAMGBHour != 0.1023/46 || *p.VCPUHour != *p.RAMGBHour*7.5 || len(p.Errors) != 0 {
		t.Fatalf("ovh: %+v", p)
	}
	want := []config.GPUPrice{{MinGiB: 17, Price: .75}, {MinGiB: 25, Price: 1.4}, {MinGiB: 49, Price: 2.75}}
	if !reflect.DeepEqual(p.GPUHour, want) {
		t.Fatalf("gpu: %+v", p.GPUHour)
	}
	wrong := bytes.Replace(ovh, []byte(`"currencyCode": "EUR"`), []byte(`"currencyCode": "USD"`), 1)
	p = priceProposal{}
	checkOVH(context.Background(), priceFake(wrong), "https://api.ovh.com/catalog", &p, &n)
	if len(p.Items) != 0 || len(p.Errors) == 0 {
		t.Fatal("wrong currency accepted")
	}
	for _, v := range []float64{math.NaN(), math.Inf(1), -1, 0, 1001} {
		p = priceProposal{}
		p.add(priceSpec{sku: "x", tier: 0}, v)
		if len(p.Items) > 0 || len(p.Errors) != 1 {
			t.Fatalf("accepted %v", v)
		}
	}
}
func TestPricePaginationAndLimit(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		calls      int
	}{{"cross host", `{"Items":[],"NextPageLink":"https://evil.example/page"}`, 4}, {"three pages", `{"Items":[],"NextPageLink":"https://prices.azure.com/page"}`, 12}} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := &http.Client{Transport: priceRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body)), Request: r}, nil
			})}
			var p priceProposal
			var n int64
			checkAzure(context.Background(), c, "https://prices.azure.com/api", &p, &n)
			if calls != tc.calls || len(p.Errors) == 0 {
				t.Fatalf("calls %d errors %v", calls, p.Errors)
			}
		})
	}
	var n int64
	_, err := priceRead(context.Background(), priceFake(bytes.Repeat([]byte{'a'}, priceBodyLimit+1)), "https://example.test", &n)
	if err == nil {
		t.Fatal("oversized body accepted")
	}
}
func TestPriceClientSafety(t *testing.T) {
	c := priceClient()
	tr := c.Transport.(*http.Transport)
	if tr.TLSClientConfig != nil && tr.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("TLS verification disabled")
	}
	orig := httptest.NewRequest("GET", "https://example.com", nil)
	other := httptest.NewRequest("GET", "https://other.example", nil)
	if c.CheckRedirect(other, []*http.Request{orig}) == nil {
		t.Fatal("cross-host redirect accepted")
	}
}
func TestPriceCheckValidationAndGate(t *testing.T) {
	reportSettingsTestRouter(t)
	h := &Handler{}
	r := chi.NewRouter()
	r.Post("/api/clusters/{idx}/report-settings/price-check", h.PriceCheck)
	before := config.Get()
	disk, _ := os.ReadFile("config.json")
	for _, body := range []string{`{"sources":["unknown"]}`, `{"unknown":1}`, `{"sources":[]}`, strings.Repeat("x", 1025)} {
		if w := reportRequest(r, "POST", "/api/clusters/0/report-settings/price-check", body); w.Code != 400 {
			t.Fatalf("invalid %d", w.Code)
		}
	}
	priceGate.Lock()
	oldLast, oldRunning := priceGate.last, priceGate.running
	priceGate.running = true
	priceGate.last = time.Now()
	priceGate.Unlock()
	t.Cleanup(func() { priceGate.Lock(); priceGate.last = oldLast; priceGate.running = oldRunning; priceGate.Unlock() })
	for _, running := range []bool{true, false} {
		priceGate.Lock()
		priceGate.running = running
		priceGate.Unlock()
		w := reportRequest(r, "POST", "/api/clusters/0/report-settings/price-check", "")
		if w.Code != 429 || w.Header().Get("Retry-After") == "" {
			t.Fatal("rate gate failed")
		}
	}
	priceGate.Lock()
	priceGate.running = false
	priceGate.last = time.Time{}
	priceGate.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest("POST", "/api/clusters/0/report-settings/price-check", strings.NewReader(`{"sources":["azure","ovh"]}`)).WithContext(ctx)
	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, request)
	if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), `"proposals"`) {
		t.Fatalf("check: %d %s", recorder.Code, recorder.Body.String())
	}
	after, _ := os.ReadFile("config.json")
	if !reflect.DeepEqual(before, config.Get()) || !bytes.Equal(disk, after) {
		t.Fatal("config changed")
	}
}

package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"proxmox-manager/internal/config"
)

const priceBodyLimit = 16 << 20

var priceGate struct {
	sync.Mutex
	running bool
	last    time.Time
}

type priceItem struct {
	SKU   string  `json:"sku"`
	Price float64 `json:"price"`
}
type priceProposal struct {
	Source       string            `json:"source"`
	ProviderName string            `json:"provider_name"`
	Level        string            `json:"level"`
	Currency     string            `json:"currency"`
	VCPUHour     *float64          `json:"vcpu_hour"`
	RAMGBHour    *float64          `json:"ram_gb_hour"`
	GPUHour      []config.GPUPrice `json:"gpu_hour"`
	SourceText   string            `json:"source_text"`
	Date         string            `json:"date"`
	Items        []priceItem       `json:"items"`
	Errors       []string          `json:"errors"`
}
type priceSpec struct {
	sku, region string
	tier        int
}

var azureSpecs = []priceSpec{{"Standard_D4s_v5", "italynorth", -1}, {"Standard_NC4as_T4_v3", "westeurope", 0}, {"Standard_NV36ads_A10_v5", "westeurope", 17}, {"Standard_NC24ads_A100_v4", "westeurope", 49}}
var ovhSpecs = []priceSpec{{"b3-16.consumption", "", -1}, {"l4-90.consumption", "", 17}, {"a10-45.consumption", "", 17}, {"l40s-90.consumption", "", 25}, {"a100-180.consumption", "", 49}, {"h100-380.consumption", "", 49}}

func priceClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSClientConfig = nil
	return &http.Client{Transport: t, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 || !strings.EqualFold(req.URL.Host, via[0].URL.Host) || req.URL.Scheme != via[0].URL.Scheme {
			return errors.New("redirect non ammesso")
		}
		return nil
	}}
}
func priceRead(ctx context.Context, client *http.Client, address string, count *int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "proxmox-manager price check")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("risposta HTTP non valida")
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, priceBodyLimit+1))
	*count += int64(len(b))
	if err != nil {
		return nil, err
	}
	if len(b) > priceBodyLimit {
		return nil, errors.New("risposta troppo grande")
	}
	return b, nil
}
func validPrice(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v > 0 && v <= 1000 }
func (p *priceProposal) add(spec priceSpec, value float64) {
	if !validPrice(value) {
		p.Errors = append(p.Errors, spec.sku+": prezzo non valido")
		return
	}
	if spec.tier < 0 {
		ram := value / 46
		cpu := ram * 7.5
		if !validPrice(ram) || !validPrice(cpu) {
			p.Errors = append(p.Errors, spec.sku+": prezzo derivato non valido")
			return
		}
		p.RAMGBHour = &ram
		p.VCPUHour = &cpu
	} else {
		found := false
		for i := range p.GPUHour {
			if p.GPUHour[i].MinGiB == float64(spec.tier) {
				found = true
				if value < p.GPUHour[i].Price {
					p.GPUHour[i].Price = value
				}
				break
			}
		}
		if !found {
			p.GPUHour = append(p.GPUHour, config.GPUPrice{MinGiB: float64(spec.tier), Price: value})
		}
	}
	p.Items = append(p.Items, priceItem{spec.sku, value})
}
func missing(p *priceProposal, sku string) {
	p.Errors = append(p.Errors, sku+": voce non trovata o non valida")
}

type azureResponse struct {
	Items []struct {
		Currency string   `json:"currencyCode"`
		Price    *float64 `json:"retailPrice"`
		Region   string   `json:"armRegionName"`
		SKU      string   `json:"armSkuName"`
		Product  string   `json:"productName"`
		SKUName  string   `json:"skuName"`
		Unit     string   `json:"unitOfMeasure"`
		Type     string   `json:"type"`
	} `json:"Items"`
	Next string `json:"NextPageLink"`
}

func checkAzure(ctx context.Context, client *http.Client, base string, p *priceProposal, count *int64) {
	for _, spec := range azureSpecs {
		u, err := url.Parse(base)
		if err != nil {
			missing(p, spec.sku)
			continue
		}
		q := u.Query()
		q.Set("currencyCode", "'EUR'")
		q.Set("$filter", fmt.Sprintf("serviceName eq 'Virtual Machines' and priceType eq 'Consumption' and armRegionName eq '%s' and armSkuName eq '%s'", spec.region, spec.sku))
		u.RawQuery = q.Encode()
		address := u.String()
		found := false
		for page := 0; page < 3; page++ {
			b, err := priceRead(ctx, client, address, count)
			if err != nil {
				p.Errors = append(p.Errors, spec.sku+": impossibile leggere il listino (rete, timeout o risposta non valida)")
				break
			}
			var data azureResponse
			if json.Unmarshal(b, &data) != nil {
				p.Errors = append(p.Errors, spec.sku+": formato della risposta non valido")
				break
			}
			for _, item := range data.Items {
				if item.SKU != spec.sku || item.Region != spec.region || item.Currency != "EUR" || item.Unit != "1 Hour" || item.Type != "Consumption" || strings.Contains(strings.ToLower(item.Product), "windows") || strings.Contains(strings.ToLower(item.SKUName), "spot") || strings.Contains(strings.ToLower(item.SKUName), "low priority") {
					continue
				}
				if item.Price == nil || !validPrice(*item.Price) {
					p.Errors = append(p.Errors, spec.sku+": prezzo non valido")
					found = true
					break
				}
				p.add(spec, *item.Price)
				found = true
				break
			}
			if found || data.Next == "" {
				break
			}
			next, err := url.Parse(data.Next)
			if err != nil || next.User != nil || next.Scheme != u.Scheme || !strings.EqualFold(next.Host, u.Host) {
				p.Errors = append(p.Errors, spec.sku+": pagina successiva non ammessa")
				break
			}
			if page == 2 {
				p.Errors = append(p.Errors, spec.sku+": limite di pagine raggiunto")
				break
			}
			address = next.String()
		}
		if !found {
			missing(p, spec.sku)
		}
	}
}
func checkOVH(ctx context.Context, client *http.Client, address string, p *priceProposal, count *int64) {
	b, err := priceRead(ctx, client, address, count)
	if err != nil {
		p.Errors = append(p.Errors, "Impossibile leggere il listino OVHcloud (rete, timeout o risposta non valida)")
		return
	}
	var data struct {
		Locale struct {
			Currency string `json:"currencyCode"`
		} `json:"locale"`
		Addons []struct {
			Code     string `json:"planCode"`
			Pricings []struct {
				Capacities []string `json:"capacities"`
				Unit       string   `json:"intervalUnit"`
				Price      *float64 `json:"price"`
			} `json:"pricings"`
		} `json:"addons"`
	}
	if json.Unmarshal(b, &data) != nil {
		p.Errors = append(p.Errors, "Formato del listino OVHcloud non valido")
		return
	}
	if data.Locale.Currency != "EUR" {
		p.Errors = append(p.Errors, "Valuta del listino OVHcloud diversa da EUR")
		return
	}
	for _, spec := range ovhSpecs {
		found := false
		for _, a := range data.Addons {
			if a.Code != spec.sku {
				continue
			}
			for _, v := range a.Pricings {
				if v.Unit != "hour" {
					continue
				}
				consumption := false
				for _, c := range v.Capacities {
					if c == "consumption" {
						consumption = true
					}
				}
				if !consumption {
					continue
				}
				found = true
				if v.Price == nil {
					p.Errors = append(p.Errors, spec.sku+": prezzo non valido")
				} else {
					p.add(spec, *v.Price/1e8)
				}
				break
			}
			if found {
				break
			}
		}
		if !found {
			missing(p, spec.sku)
		}
	}
}
func (h *Handler) PriceCheck(w http.ResponseWriter, r *http.Request) {
	if _, ok := reportSettingsIndex(w, r); !ok {
		return
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
	if err != nil {
		writeError(w, errors.New("richiesta troppo grande"), 400)
		return
	}
	sources := []string{"azure", "ovh"}
	if len(bytes.TrimSpace(b)) > 0 {
		var body struct {
			Sources []string `json:"sources"`
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if dec.Decode(&body) != nil || dec.Decode(new(any)) != io.EOF || body.Sources == nil {
			writeError(w, errors.New("richiesta non valida"), 400)
			return
		}
		sources = body.Sources
	}
	seen := map[string]bool{}
	for _, s := range sources {
		if (s != "azure" && s != "ovh") || seen[s] {
			writeError(w, errors.New("sorgente non valida"), 400)
			return
		}
		seen[s] = true
	}
	if len(sources) == 0 {
		writeError(w, errors.New("selezionare almeno una sorgente"), 400)
		return
	}
	priceGate.Lock()
	now := time.Now()
	if priceGate.running || now.Sub(priceGate.last) < 5*time.Minute {
		wait := int(math.Ceil((5*time.Minute - now.Sub(priceGate.last)).Seconds()))
		if wait < 1 {
			wait = 1
		}
		priceGate.Unlock()
		w.Header().Set("Retry-After", fmt.Sprint(wait))
		writeError(w, errors.New("controllo già in corso o troppo recente"), 429)
		return
	}
	priceGate.running = true
	priceGate.last = now
	priceGate.Unlock()
	defer func() { priceGate.Lock(); priceGate.running = false; priceGate.Unlock() }()
	proposals := make([]priceProposal, len(sources))
	var wg sync.WaitGroup
	for i, source := range sources {
		wg.Add(1)
		go func(i int, source string) {
			defer wg.Done()
			start := time.Now()
			ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
			defer cancel()
			p := priceProposal{Source: source, Currency: "EUR", Date: now.Format("2006-01-02"), GPUHour: []config.GPUPrice{}, Items: []priceItem{}, Errors: []string{}}
			var count int64
			if source == "azure" {
				p.ProviderName = "Azure"
				p.Level = "high"
				p.SourceText = "Azure Retail Prices API, westeurope/italynorth, Linux pay-as-you-go"
				checkAzure(ctx, priceClient(), "https://prices.azure.com/api/retail/prices", &p, &count)
			} else {
				p.ProviderName = "OVHcloud"
				p.Level = "low"
				p.SourceText = "OVHcloud catalogo pubblico IT, prezzi orari"
				checkOVH(ctx, priceClient(), "https://api.ovh.com/1.0/order/catalog/public/cloud?ovhSubsidiary=IT", &p, &count)
			}
			sort.Slice(p.GPUHour, func(i, j int) bool { return p.GPUHour[i].MinGiB < p.GPUHour[j].MinGiB })
			result := "ok"
			if len(p.Errors) > 0 {
				result = "errore"
			}
			log.Printf("price check source=%s result=%s duration=%s bytes=%d", source, result, time.Since(start).Round(time.Millisecond), count)
			proposals[i] = p
		}(i, source)
	}
	wg.Wait()
	writeJSON(w, map[string]any{"checked_at": now.Format(time.RFC3339), "proposals": proposals})
}

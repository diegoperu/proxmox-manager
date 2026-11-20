package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
)

type NetworkSettings struct {
	Networks     []VMNetwork `json:"networks"`
	DNS          []string    `json:"dns"`
	SearchDomain string      `json:"search_domain"`
}

type VMNetwork struct {
	CIDR    string `json:"cidr"`
	Gateway string `json:"gateway"`
}

func (n *NetworkSettings) UnmarshalJSON(data []byte) error {
	type networkJSON NetworkSettings
	var value networkJSON
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&value); err != nil {
		return err
	}
	*n = NetworkSettings(value)
	return nil
}

// ValidSearchDomain accepts optional ASCII RFC 1123 labels and returns lowercase.
func ValidSearchDomain(s string) (string, bool) {
	if len(s) > 253 {
		return "", false
	}
	if s == "" {
		return "", true
	}
	for _, label := range strings.Split(s, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return "", false
			}
		}
	}
	return strings.ToLower(s), true
}

// ValidNetworkSettings validates and normalizes a settings object.
func ValidNetworkSettings(n *NetworkSettings) error {
	if n == nil {
		return nil
	}
	// Messages name the field and the row, never the submitted text.
	if len(n.Networks) > 16 {
		return errors.New("al massimo 16 reti")
	}
	if len(n.DNS) > 3 {
		return errors.New("al massimo 3 DNS")
	}
	networks := make([]VMNetwork, 0, len(n.Networks))
	var parsed []*net.IPNet
	for i, v := range n.Networks {
		row := i + 1
		ip, nw, err := net.ParseCIDR(v.CIDR)
		if err != nil || ip.To4() == nil {
			return fmt.Errorf("rete %d: indirizzo di rete IPv4 non valido (es. 10.75.4.0/24)", row)
		}
		ones, bits := nw.Mask.Size()
		if bits != 32 || ones < 8 || ones > 30 {
			return fmt.Errorf("rete %d: il prefisso deve essere tra 8 e 30", row)
		}
		if !ip.Equal(nw.IP) {
			return fmt.Errorf("rete %d: scrivi l'indirizzo di rete (%s), non quello di un host", row, nw.IP)
		}
		for _, prev := range parsed {
			if prev.Contains(nw.IP) || nw.Contains(prev.IP) {
				return fmt.Errorf("rete %d: si sovrappone a un'altra rete (il gateway sarebbe ambiguo)", row)
			}
		}
		gw := net.ParseIP(v.Gateway)
		if gw == nil || gw.To4() == nil {
			return fmt.Errorf("rete %d: gateway IPv4 non valido", row)
		}
		if !nw.Contains(gw) {
			return fmt.Errorf("rete %d: il gateway non e' dentro la rete", row)
		}
		if gw.Equal(nw.IP) {
			return fmt.Errorf("rete %d: il gateway non puo' essere l'indirizzo di rete", row)
		}
		broadcast := make(net.IP, net.IPv4len)
		for i := range broadcast {
			broadcast[i] = nw.IP.To4()[i] | ^nw.Mask[i]
		}
		if gw.Equal(broadcast) {
			return fmt.Errorf("rete %d: il gateway non puo' essere l'indirizzo di broadcast", row)
		}
		parsed = append(parsed, nw)
		networks = append(networks, VMNetwork{CIDR: nw.String(), Gateway: gw.String()})
	}
	dns := make([]string, 0, len(n.DNS))
	seen := make(map[string]bool)
	for i, value := range n.DNS {
		ip := net.ParseIP(value)
		if ip == nil {
			return fmt.Errorf("DNS %d: indirizzo IP non valido", i+1)
		}
		if seen[ip.String()] {
			return fmt.Errorf("DNS %d: indirizzo ripetuto", i+1)
		}
		seen[ip.String()] = true
		dns = append(dns, ip.String())
	}
	domain, ok := ValidSearchDomain(n.SearchDomain)
	if !ok {
		return errors.New("dominio di ricerca non valido (lettere, cifre e trattini separati da punti)")
	}
	n.Networks, n.DNS, n.SearchDomain = networks, dns, domain
	return nil
}

func copyNetwork(n *NetworkSettings) *NetworkSettings {
	if n == nil {
		return nil
	}
	c := *n
	c.Networks = append([]VMNetwork{}, n.Networks...)
	c.DNS = append([]string{}, n.DNS...)
	return &c
}

var ErrNetworkCluster = errors.New("cluster non trovato")

func SetNetworkSettings(idx int, n *NetworkSettings) error {
	n = copyNetwork(n)
	if err := ValidNetworkSettings(n); err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	if idx < 0 || idx >= len(current.Clusters) {
		return ErrNetworkCluster
	}
	old := current.Clusters[idx].Network
	current.Clusters[idx].Network = n
	if err := save(); err != nil {
		current.Clusters[idx].Network = old
		return err
	}
	return nil
}

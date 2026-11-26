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
	// ManagerFrom: IPv4 addresses the cluster nodes see the manager connecting from (D158). They go into the
	// from= restriction of the manager's key on the nodes; a router in between means its address, not the
	// manager's own.
	ManagerFrom []string `json:"manager_from"`
	// SnippetProfile: cloud-init snippets the manager delivers to the nodes of this cluster (D90): "standard" or
	// "hosting" (key-only SSH, firewall, hardening). Empty is read as "standard".
	SnippetProfile string `json:"snippet_profile"`
	// IPMode controls addressing for new VMs: static (default), dhcp or both.
	IPMode string `json:"ip_mode"`
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
	from, err := validManagerFrom(n.ManagerFrom)
	if err != nil {
		return err
	}
	domain, ok := ValidSearchDomain(n.SearchDomain)
	if !ok {
		return errors.New("dominio di ricerca non valido (lettere, cifre e trattini separati da punti)")
	}
	profile := n.SnippetProfile
	if profile == "" {
		profile = "standard"
	}
	if profile != "standard" && profile != "hosting" {
		return errors.New("profilo degli snippet non valido (standard o hosting)")
	}
	mode := n.IPMode
	if mode == "" {
		mode = "static"
	}
	if mode != "static" && mode != "dhcp" && mode != "both" {
		return errors.New("ip_mode non valido (static, dhcp o both)")
	}
	n.Networks, n.DNS, n.SearchDomain, n.ManagerFrom, n.SnippetProfile = networks, dns, domain, from, profile
	n.IPMode = mode
	return nil
}

// validManagerFrom accepts up to 4 distinct IPv4 host addresses (the from= list of pmgr-sync-setup.sh is IPv4).
func validManagerFrom(list []string) ([]string, error) {
	if len(list) > 4 {
		return nil, errors.New("indirizzo del manager: al massimo 4 indirizzi")
	}
	out := make([]string, 0, len(list))
	seen := map[string]bool{}
	for i, value := range list {
		ip := net.ParseIP(value)
		if ip == nil || ip.To4() == nil || ip.IsUnspecified() || ip.IsMulticast() || ip.Equal(net.IPv4bcast) {
			return nil, fmt.Errorf("indirizzo del manager %d: indirizzo IPv4 non valido", i+1)
		}
		s := ip.To4().String()
		if seen[s] {
			return nil, fmt.Errorf("indirizzo del manager %d: indirizzo ripetuto", i+1)
		}
		seen[s] = true
		out = append(out, s)
	}
	return out, nil
}

func copyNetwork(n *NetworkSettings) *NetworkSettings {
	if n == nil {
		return nil
	}
	c := *n
	c.Networks = append([]VMNetwork{}, n.Networks...)
	c.DNS = append([]string{}, n.DNS...)
	c.ManagerFrom = append([]string{}, n.ManagerFrom...)
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

// SetClusterTLSCA stores (caPEM already normalized by the caller) or clears ("") the CA of a cluster.
func SetClusterTLSCA(idx int, caPEM string) error {
	mu.Lock()
	defer mu.Unlock()
	if idx < 0 || idx >= len(current.Clusters) {
		return ErrNetworkCluster
	}
	old := current.Clusters[idx].TLSCA
	current.Clusters[idx].TLSCA = caPEM
	if err := save(); err != nil {
		current.Clusters[idx].TLSCA = old
		return err
	}
	return nil
}

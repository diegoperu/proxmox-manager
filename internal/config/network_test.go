package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestNetworkValidation(t *testing.T) {
	valid := func() *NetworkSettings {
		return &NetworkSettings{Networks: []VMNetwork{{"10.75.4.0/24", "10.75.4.254"}}, DNS: []string{"2001:0DB8::1"}, SearchDomain: "EXAMPLE.COM"}
	}
	cases := map[string]func(*NetworkSettings){
		"ipv6 network":    func(n *NetworkSettings) { n.Networks[0].CIDR = "2001:db8::/64" },
		"host bits":       func(n *NetworkSettings) { n.Networks[0].CIDR = "10.75.4.1/24" },
		"prefix short":    func(n *NetworkSettings) { n.Networks[0].CIDR = "10.0.0.0/7" },
		"prefix long":     func(n *NetworkSettings) { n.Networks[0].CIDR = "10.75.4.0/31" },
		"duplicate":       func(n *NetworkSettings) { n.Networks = append(n.Networks, n.Networks[0]) },
		"overlap":         func(n *NetworkSettings) { n.Networks = append(n.Networks, VMNetwork{"10.75.0.0/16", "10.75.0.1"}) },
		"outside":         func(n *NetworkSettings) { n.Networks[0].Gateway = "10.75.5.1" },
		"network gateway": func(n *NetworkSettings) { n.Networks[0].Gateway = "10.75.4.0" },
		"broadcast":       func(n *NetworkSettings) { n.Networks[0].Gateway = "10.75.4.255" },
		"ipv6 gateway":    func(n *NetworkSettings) { n.Networks[0].Gateway = "::1" },
		"17 networks": func(n *NetworkSettings) {
			for i := 0; i < 16; i++ {
				n.Networks = append(n.Networks, VMNetwork{fmt.Sprintf("11.%d.0.0/16", i), fmt.Sprintf("11.%d.0.1", i)})
			}
		},
		"invalid dns":   func(n *NetworkSettings) { n.DNS = []string{"bad"} },
		"4 dns":         func(n *NetworkSettings) { n.DNS = []string{"1.1.1.1", "2.2.2.2", "3.3.3.3", "4.4.4.4"} },
		"duplicate dns": func(n *NetworkSettings) { n.DNS = []string{"2001:db8::1", "2001:0db8::1"} },
	}
	for _, domain := range []string{"a b", "a_b", "é.example", "-a", "a-", "a..b", "a.", strings.Repeat("a", 64), strings.Repeat("a.", 127) + "aa"} {
		d := domain
		cases["domain "+domain] = func(n *NetworkSettings) { n.SearchDomain = d }
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			n := valid()
			mutate(n)
			if ValidNetworkSettings(n) == nil {
				t.Fatal("accepted invalid settings")
			}
		})
	}
	n := valid()
	if err := ValidNetworkSettings(n); err != nil || n.DNS[0] != "2001:db8::1" || n.SearchDomain != "example.com" {
		t.Fatalf("normalization: %+v %v", n, err)
	}
	if err := ValidNetworkSettings(&NetworkSettings{}); err != nil {
		t.Fatal(err)
	}
	for _, domain := range []string{"", "localhost", "a-b.example", strings.Repeat("a", 63) + ".com"} {
		if _, ok := ValidSearchDomain(domain); !ok {
			t.Errorf("invalid: %q", domain)
		}
	}
}

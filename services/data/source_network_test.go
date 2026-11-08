package data

import (
	"context"
	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"net"
	"net/netip"
	"testing"
)

func testSourceNetworkPolicy(t *testing.T) *SourceNetworkPolicy {
	t.Helper()
	p, err := ParseSourceNetworkPolicy(`[{"cidr":"127.0.0.0/8","ports":[23306,23307,23308,25432,3306,5432],"allowInsecureTLS":true},{"cidr":"::1/128","ports":[23306,23307,23308,25432,3306,5432],"allowInsecureTLS":true}]`)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSourceNetworkDeniesUnlistedAddressesPortsAndInsecureTLS(t *testing.T) {
	for _, raw := range []string{`[]`, `null`, `[{"cidr":"0.0.0.0/0","ports":[3306]}]`, `[{"cidr":"127.0.0.1/32","ports":[0]}]`, `[{"cidr":"127.0.0.1/32","ports":[3306],"unknown":true}]`} {
		if _, err := ParseSourceNetworkPolicy(raw); err == nil {
			t.Fatalf("accepted invalid policy %s", raw)
		}
	}
	p, err := ParseSourceNetworkPolicy(`[{"cidr":"127.0.0.1/32","ports":[3306]}]`)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		host string
		port int
		tls  string
	}{{"127.0.0.1", 22, "verify-full"}, {"127.0.0.1", 3306, "disable"}, {"127.0.0.1", 3306, "require"}, {"10.0.0.1", 3306, "verify-full"}, {"169.254.169.254", 3306, "verify-full"}, {"::1", 3306, "verify-full"}} {
		if _, err := p.addresses(context.Background(), c.DataSource{Host: test.host, Port: test.port, TLSMode: test.tls}); err == nil {
			t.Fatalf("unlisted connection accepted: %+v", test)
		}
	}
	if _, err := (*SourceNetworkPolicy)(nil).addresses(context.Background(), c.DataSource{Host: "127.0.0.1", Port: 3306, TLSMode: "verify-full"}); err == nil {
		t.Fatal("missing policy allowed connection")
	}
}

func TestSourceNetworkPinsDNSAndChecksEveryDial(t *testing.T) {
	p, _ := ParseSourceNetworkPolicy(`[{"cidr":"203.0.113.10/32","ports":[3306]}]`)
	answers := []netip.Addr{netip.MustParseAddr("203.0.113.10")}
	p.lookup = func(context.Context, string) ([]netip.Addr, error) { return answers, nil }
	dials := 0
	p.dial = func(_ context.Context, network, address string) (net.Conn, error) {
		dials++
		if network != "tcp" || address != "203.0.113.10:3306" {
			t.Fatal("hostname or uninspected IP reached dial")
		}
		client, server := net.Pipe()
		server.Close()
		return client, nil
	}
	source := c.DataSource{Host: "db.example", Port: 3306, TLSMode: "verify-full"}
	conn, err := p.connect(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	answers = []netip.Addr{netip.MustParseAddr("127.0.0.1")}
	if _, err = p.connect(context.Background(), source); err == nil || dials != 1 {
		t.Fatal("DNS rebinding reached network")
	}
	answers = []netip.Addr{netip.MustParseAddr("203.0.113.10"), netip.MustParseAddr("127.0.0.1")}
	if _, err = p.connect(context.Background(), source); err == nil || dials != 1 {
		t.Fatal("mixed DNS answers reached network")
	}
}

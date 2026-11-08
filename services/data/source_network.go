package data

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

type sourceNetworkRule struct {
	CIDR             string `json:"cidr"`
	Ports            []int  `json:"ports"`
	AllowInsecureTLS bool   `json:"allowInsecureTLS"`
}
type sourceNetworkEntry struct {
	prefix   netip.Prefix
	ports    map[int]bool
	insecure bool
}

// SourceNetworkPolicy is operator-owned. Tenant input cannot expand it.
// An absent policy denies all external source connections.
type SourceNetworkPolicy struct {
	entries []sourceNetworkEntry
	lookup  func(context.Context, string) ([]netip.Addr, error)
	dial    func(context.Context, string, string) (net.Conn, error)
}

func ParseSourceNetworkPolicy(raw string) (*SourceNetworkPolicy, error) {
	p := &SourceNetworkPolicy{lookup: func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}, dial: (&net.Dialer{Timeout: 5 * time.Second}).DialContext}
	if raw == "" {
		return p, nil
	}
	if len(raw) > 32768 {
		return nil, fmt.Errorf("数据源网络规则超过长度限制")
	}
	var rules []sourceNetworkRule
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&rules) != nil || decoder.Decode(new(any)) != io.EOF || len(rules) == 0 || len(rules) > 64 {
		return nil, fmt.Errorf("数据源网络规则须为1至64项JSON数组")
	}
	for _, rule := range rules {
		prefix, err := netip.ParsePrefix(rule.CIDR)
		if err != nil || prefix.Bits() == 0 || prefix.Addr().Is4In6() || len(rule.Ports) == 0 || len(rule.Ports) > 32 {
			return nil, fmt.Errorf("数据源网络规则须指定明确CIDR及1至32个端口")
		}
		ports := map[int]bool{}
		for _, port := range rule.Ports {
			if port < 1 || port > 65535 || ports[port] {
				return nil, fmt.Errorf("数据源允许端口无效或重复")
			}
			ports[port] = true
		}
		p.entries = append(p.entries, sourceNetworkEntry{prefix.Masked(), ports, rule.AllowInsecureTLS})
	}
	return p, nil
}

func sourceNetworkDenied() error {
	return problem(403, "source_network_denied", "数据源地址、端口或TLS模式未获部署方允许，请联系平台管理员")
}

func (p *SourceNetworkPolicy) allowed(address netip.Addr, source c.DataSource) bool {
	address = address.Unmap()
	if !address.IsValid() || address.Zone() != "" || address.IsUnspecified() || address.IsMulticast() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() {
		return false
	}
	if p == nil {
		return false
	}
	for _, entry := range p.entries {
		if entry.prefix.Contains(address) && entry.ports[source.Port] && (source.TLSMode == "verify-full" || entry.insecure) {
			return true
		}
	}
	return false
}

func (p *SourceNetworkPolicy) addresses(ctx context.Context, source c.DataSource) ([]netip.Addr, error) {
	if p == nil || len(p.entries) == 0 {
		return nil, sourceNetworkDenied()
	}
	addresses := []netip.Addr{}
	if address, err := netip.ParseAddr(source.Host); err == nil {
		addresses = append(addresses, address)
	} else {
		var err error
		addresses, err = p.lookup(ctx, source.Host)
		if err != nil {
			return nil, sourceNetworkDenied()
		}
	}
	if len(addresses) == 0 || len(addresses) > 16 {
		return nil, sourceNetworkDenied()
	}
	// Mixed allowed/denied DNS answers are rejected, including rebinding.
	for _, address := range addresses {
		if !p.allowed(address, source) {
			return nil, sourceNetworkDenied()
		}
	}
	return addresses, nil
}

func (p *SourceNetworkPolicy) connect(ctx context.Context, source c.DataSource) (net.Conn, error) {
	addresses, err := p.addresses(ctx, source)
	if err != nil {
		return nil, err
	}
	for _, address := range addresses {
		// Dial the inspected numeric address, never resolve the hostname again.
		conn, err := p.dial(ctx, "tcp", net.JoinHostPort(address.Unmap().String(), strconv.Itoa(source.Port)))
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, problem(422, "source_connection_failed", "无法连接数据源，请核对地址、凭据、TLS和数据库权限")
}

type sourceDialKey struct{}
type sourceDialScope struct {
	policy *SourceNetworkPolicy
	source c.DataSource
}
type sourcePolicyConnector struct {
	driver.Connector
	scope sourceDialScope
}

func (p sourcePolicyConnector) Connect(ctx context.Context) (driver.Conn, error) {
	return p.Connector.Connect(context.WithValue(ctx, sourceDialKey{}, p.scope))
}

func init() {
	mysql.RegisterDialContext("ontology-source", func(ctx context.Context, _ string) (net.Conn, error) {
		scope, ok := ctx.Value(sourceDialKey{}).(sourceDialScope)
		if !ok {
			return nil, sourceNetworkDenied()
		}
		return scope.policy.connect(ctx, scope.source)
	})
}

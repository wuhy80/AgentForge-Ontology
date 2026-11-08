package platform

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
)

type proxyHTTPSContextKey struct{}
type proxyClientAddressKey struct{}

// ClientAddress uses only the TCP peer unless a configured proxy supplies a
// single valid X-Real-IP. The edge must overwrite that header.
func ClientAddress(r *http.Request) string {
	if address, ok := r.Context().Value(proxyClientAddressKey{}).(string); ok {
		return address
	}
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return "unknown"
	}
	return peer.Addr().Unmap().String()
}

// TrustTLSProxy recognizes TLS termination only from explicitly configured
// network peers. Host and Origin remain untouched; X-Forwarded-Host and client
// supplied X-Forwarded-For never select either the peer or the public authority.
func TrustTLSProxy(app http.Handler, cidrs string) (http.Handler, error) {
	if strings.TrimSpace(cidrs) == "" {
		return app, nil
	}
	entries := strings.Split(cidrs, ",")
	if len(entries) > 16 {
		return nil, fmt.Errorf("可信代理CIDR最多16项")
	}
	prefixes := make([]netip.Prefix, 0, len(entries))
	for _, entry := range entries {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(entry))
		if err != nil || prefix.Bits() == 0 {
			return nil, fmt.Errorf("ONTOLOGY_TRUSTED_PROXY_CIDRS须为明确的代理CIDR，不能信任全部地址")
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proto := r.Header.Values("X-Forwarded-Proto")
		peer, err := netip.ParseAddrPort(r.RemoteAddr)
		if err == nil {
			for _, prefix := range prefixes {
				if prefix.Contains(peer.Addr().Unmap()) {
					ctx := r.Context()
					if r.TLS == nil && len(proto) == 1 && proto[0] == "https" {
						ctx = context.WithValue(ctx, proxyHTTPSContextKey{}, true)
					}
					addresses := r.Header.Values("X-Real-IP")
					if len(addresses) == 1 {
						if address, err := netip.ParseAddr(addresses[0]); err == nil && address.Zone() == "" {
							ctx = context.WithValue(ctx, proxyClientAddressKey{}, address.Unmap().String())
						}
					}
					r = r.WithContext(ctx)
					break
				}
			}
		}
		app.ServeHTTP(w, r)
	}), nil
}

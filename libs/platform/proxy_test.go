package platform

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func TestTLSProxyOriginAndSessionBoundaries(t *testing.T) {
	for _, test := range []struct {
		name, cidrs, peer, origin, host, forwardedHost, csrf string
		proto                                                []string
		tls                                                  bool
		want                                                 int
	}{
		{name: "trusted termination", cidrs: "127.0.0.1/32", peer: "127.0.0.1:1234", want: 204},
		{name: "trusted IPv6", cidrs: "::1/128", peer: "[::1]:1234", want: 204},
		{name: "mapped peer", cidrs: "127.0.0.1/32", peer: "[::ffff:127.0.0.1]:1234", want: 204},
		{name: "disabled by default", peer: "127.0.0.1:1234", want: 403},
		{name: "untrusted peer", cidrs: "127.0.0.1/32", peer: "198.51.100.4:1234", want: 403},
		{name: "forged host", cidrs: "127.0.0.1/32", peer: "127.0.0.1:1234", origin: "https://attacker.test", forwardedHost: "attacker.test", want: 403},
		{name: "different port", cidrs: "127.0.0.1/32", peer: "127.0.0.1:1234", origin: "https://workbench.test:8080", want: 403},
		{name: "wrong CSRF", cidrs: "127.0.0.1/32", peer: "127.0.0.1:1234", csrf: sessionToken, want: 403},
		{name: "multiple values", cidrs: "127.0.0.1/32", peer: "127.0.0.1:1234", proto: []string{"https", "http"}, want: 403},
		{name: "comma list", cidrs: "127.0.0.1/32", peer: "127.0.0.1:1234", proto: []string{"https, http"}, want: 403},
		{name: "actual TLS cannot downgrade", cidrs: "127.0.0.1/32", peer: "127.0.0.1:1234", proto: []string{"http"}, origin: "http://workbench.test", tls: true, want: 403},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := ProtectSessionRoutes(serviceToken, false, sessionCheckerFunc(func(context.Context, c.SessionCheck) (c.Session, error) {
				return liveSession(), nil
			}), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
			app, err := TrustTLSProxy(app, test.cidrs)
			if err != nil {
				t.Fatal(err)
			}
			r := sessionRequest("POST", "/data/query")
			r.RemoteAddr = test.peer
			r.Header.Set("Origin", "https://workbench.test")
			r.Header.Set("X-Forwarded-For", "127.0.0.1")
			r.Header.Set("X-Forwarded-Proto", "https")
			if test.origin != "" {
				r.Header.Set("Origin", test.origin)
			}
			if test.forwardedHost != "" {
				r.Header.Set("X-Forwarded-Host", test.forwardedHost)
			}
			if test.csrf != "" {
				r.Header.Set("X-CSRF-Token", test.csrf)
			}
			if test.proto != nil {
				r.Header["X-Forwarded-Proto"] = test.proto
			}
			if test.tls {
				r.TLS = &tls.ConnectionState{}
			}
			w := httptest.NewRecorder()
			app.ServeHTTP(w, r)
			if w.Code != test.want {
				t.Fatalf("status=%d want=%d: %s", w.Code, test.want, w.Body.String())
			}
		})
	}
	for _, cidrs := range []string{"*", "127.0.0.1", "0.0.0.0/0", "::/0", "127.0.0.1/32,"} {
		if _, err := TrustTLSProxy(http.NotFoundHandler(), cidrs); err == nil {
			t.Fatalf("accepted invalid proxy configuration %q", cidrs)
		}
	}
}

func TestTLSReverseProxyPreservesPublicOrigin(t *testing.T) {
	app := ProtectSessionRoutes(serviceToken, true, sessionCheckerFunc(func(context.Context, c.SessionCheck) (c.Session, error) {
		return liveSession(), nil
	}), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	app, err := TrustTLSProxy(app, "127.0.0.1/32,::1/128")
	if err != nil {
		t.Fatal(err)
	}
	backend := httptest.NewServer(app)
	defer backend.Close()
	target, _ := url.Parse(backend.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(r *http.Request) {
		director(r)
		r.Header.Set("X-Forwarded-Proto", "https")
	}
	frontend := httptest.NewTLSServer(proxy)
	defer frontend.Close()
	for _, path := range []string{"/metadata/auth/login", "/data/query"} {
		r, _ := http.NewRequest("POST", frontend.URL+path, nil)
		r.Header.Set("Origin", frontend.URL)
		r.Header.Set("X-CSRF-Token", csrfToken)
		r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessionToken})
		response, err := frontend.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 204 {
			t.Fatalf("TLS frontend %s status=%d", path, response.StatusCode)
		}
	}
}

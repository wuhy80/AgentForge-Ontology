package identity

import "testing"

func TestAdditionalBrowserOrigins(t *testing.T) {
	const primary = "http://127.0.0.1:5173"
	const preview = "http://127.0.0.1:4173"
	store := NewStore(nil, nil, primary)
	if store.checkOrigin(preview) == nil {
		t.Fatal("preview origin must be explicitly configured")
	}
	if err := store.ConfigureAdditionalOrigins(" "+preview+" ", ""); err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{primary, preview, ""} {
		if err := store.checkOrigin(origin); err != nil {
			t.Fatalf("configured origin rejected: %q", origin)
		}
	}
	for _, origin := range []string{"http://attacker.test", "http://localhost:4173", preview + ".attacker.test", "null", "http://127.0.0.1:6173"} {
		if store.checkOrigin(origin) == nil {
			t.Fatalf("unconfigured origin accepted: %q", origin)
		}
	}
	if store.Origin != primary {
		t.Fatal("aliases changed the canonical OAuth/cookie origin")
	}
	if err := store.ConfigureAdditionalOrigins(); err != nil || store.checkOrigin(preview) == nil {
		t.Fatal("removing aliases did not restore the primary-only boundary")
	}
}

func TestAdditionalOriginConfigurationRejectsAmbiguousAndMixedSchemeURLs(t *testing.T) {
	for _, primary := range []string{"http://127.0.0.1:5173", "https://workbench.test"} {
		store := NewStore(nil, nil, primary)
		for _, origin := range []string{"*", "null", "https://*.example.test", "http://public.test", primary + "/", primary + "/path", primary + "?", primary + "?x=1", primary + "#", primary + "#fragment", "https://user:password@workbench.test", "ftp://127.0.0.1", "http://[invalid"} {
			if err := store.ConfigureAdditionalOrigins(origin); err == nil {
				t.Fatalf("invalid alias accepted: %q", origin)
			}
		}
	}
	secure := NewStore(nil, nil, "https://workbench.test")
	if err := secure.ConfigureAdditionalOrigins("https://preview.test"); err != nil {
		t.Fatal(err)
	}
	if err := secure.ConfigureAdditionalOrigins("http://127.0.0.1:4173"); err == nil {
		t.Fatal("Secure cookie alias was allowed over HTTP")
	}
	if secure.checkOrigin("https://preview.test") != nil {
		t.Fatal("failed configuration discarded the active aliases")
	}
	insecure := NewStore(nil, nil, "http://127.0.0.1:5173")
	if err := insecure.ConfigureAdditionalOrigins("https://preview.test"); err == nil {
		t.Fatal("mixed cookie-security schemes accepted")
	}
}

package platform

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSessionCookieRejectsRawAmbiguity(t *testing.T) {
	token := strings.Repeat("a", 64)
	for _, test := range []struct {
		name           string
		headers        []string
		present, valid bool
	}{
		{"missing", nil, false, true},
		{"normal", []string{"other=x; ontology_session=" + token + "; another=y"}, true, true},
		{"separate unrelated headers", []string{"other=x", "ontology_session=" + token}, true, true},
		{"empty", []string{"ontology_session="}, true, false},
		{"missing equals", []string{"ontology_session"}, true, false},
		{"quoted", []string{`ontology_session="` + token + `"`}, true, false},
		{"whitespace", []string{"ontology_session =" + token}, true, false},
		{"duplicate", []string{"ontology_session=" + token + "; ontology_session=" + token}, true, false},
		{"multiple headers", []string{"ontology_session=" + token, "ontology_session=" + token}, true, false},
		{"discarded escape", []string{"ontology_session=" + token + `; ontology_session=bad\value`}, true, false},
		{"discarded quote", []string{"ontology_session=" + token + `; ontology_session="unclosed`}, true, false},
		{"malformed first", []string{`ontology_session="unclosed; ontology_session=` + token}, true, false},
		{"comma joined", []string{"ontology_session=" + token + ", ontology_session=" + token}, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "https://review.test/data/query/context", nil)
			for _, value := range test.headers {
				r.Header.Add("Cookie", value)
			}
			got, present, err := SessionCookie(r)
			if present != test.present || (err == nil) != test.valid || test.valid && present && got != token {
				t.Fatal("unexpected cookie validation")
			}
			_, requestErr := SessionRequest(r)
			if requestErr == nil && (!test.valid || !present) {
				t.Fatal("session accepted invalid raw credentials")
			}
		})
	}
}

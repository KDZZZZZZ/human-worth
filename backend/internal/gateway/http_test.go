package gateway

import (
	"crypto/tls"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestForwardedHeadersRequireTrustedPeerAndFixedHost(t *testing.T) {
	h, err := New(nil, Options{Origin: "https://worth.example.test", TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.8/32")}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, peer, host, proto string
		tls                     bool
		want                    int
	}{
		{"direct TLS", "203.0.113.1:50000", "worth.example.test", "", true, 200},
		{"trusted HTTPS proxy", "10.0.0.8:50000", "worth.example.test", "https", false, 200},
		{"forged forwarded proto", "203.0.113.1:50000", "worth.example.test", "https", false, 400},
		{"cleartext", "10.0.0.8:50000", "worth.example.test", "http", false, 400},
		{"forged host", "10.0.0.8:50000", "evil.test", "https", false, 400},
		{"duplicate proto", "10.0.0.8:50000", "worth.example.test", "https, http", false, 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://worth.example.test/", nil)
			r.RemoteAddr = test.peer
			r.Host = test.host
			r.Header.Set("X-Forwarded-Proto", test.proto)
			r.Header.Set("X-Forwarded-Host", "worth.example.test")
			if test.tls {
				r.TLS = &tls.ConnectionState{}
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != test.want {
				t.Fatalf("status=%d want=%d", w.Code, test.want)
			}
			if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
				t.Fatal("unsafe response cache/referrer policy")
			}
		})
	}
}

package api

import (
	"testing"
	"time"

	"github.com/crewjam/saml"
)

// The brief allows 60 seconds of clock difference with the identity provider;
// the library's own default is 180.
func TestSAMLClockSkewIsSixtySeconds(t *testing.T) {
	if saml.MaxClockSkew != 60*time.Second {
		t.Fatalf("saml.MaxClockSkew = %v, want 60s", saml.MaxClockSkew)
	}
}

func TestRelayStateIsAuthenticatedAndExpires(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	now := time.Unix(1_800_000_000, 0)
	value := sealRelayState(key, "nonce1", "id-abc", now.Add(10*time.Minute))
	if nonce, id, ok := openRelayState(key, value, now); !ok || nonce != "nonce1" || id != "id-abc" {
		t.Fatalf("open = %q, %q, %v; want the sealed values", nonce, id, ok)
	}
	if _, _, ok := openRelayState(key, value, now.Add(10*time.Minute)); ok {
		t.Errorf("an expired relay state was accepted")
	}
	if _, _, ok := openRelayState([]byte("another key another key another!"), value, now); ok {
		t.Errorf("a relay state sealed under another key was accepted")
	}
	if _, _, ok := openRelayState(key, "x"+value, now); ok {
		t.Errorf("a tampered relay state was accepted")
	}
}

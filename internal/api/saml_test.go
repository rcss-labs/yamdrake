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
	value := sealRelayState(key, "nonce1", "id-abc", "/x", now.Add(10*time.Minute))
	if nonce, id, next, ok := openRelayState(key, value, now); !ok || nonce != "nonce1" || id != "id-abc" || next != "/x" {
		t.Fatalf("open = %q, %q, %q, %v; want the sealed values", nonce, id, next, ok)
	}
	if _, _, _, ok := openRelayState(key, value, now.Add(10*time.Minute)); ok {
		t.Errorf("an expired relay state was accepted")
	}
	if _, _, _, ok := openRelayState([]byte("another key another key another!"), value, now); ok {
		t.Errorf("a relay state sealed under another key was accepted")
	}
	if _, _, _, ok := openRelayState(key, "x"+value, now); ok {
		t.Errorf("a tampered relay state was accepted")
	}
}

func TestSafeNextPath(t *testing.T) {
	for _, ok := range []string{"/", "/jobs/3", "/a?b=c", "/a/b#frag"} {
		if !safeNextPath(ok) {
			t.Errorf("%q was refused, want accepted", ok)
		}
	}
	for _, bad := range []string{"", "//evil", `/\evil`, "https://evil", "javascript:x", "/%2F%2Fevil", "\t/x", "x/y", "/a\nb", "/%5Cevil", "/%0Aevil", "/a\x7f"} {
		if safeNextPath(bad) {
			t.Errorf("%q was accepted, want refused", bad)
		}
	}
}

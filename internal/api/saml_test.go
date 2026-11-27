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

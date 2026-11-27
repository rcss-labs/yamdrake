package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rcss-labs/yamdrake/internal/config"
)

func serveRequest(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

// marker handlers let the tests see which part of the mux answered.
func marker(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(name))
	})
}

func TestDoesNotShadowAPI(t *testing.T) {
	ext := Extension{Handlers: map[string]http.Handler{"/api/v1/": marker("api"), "/saml/": marker("saml")}}
	h := serviceHandler(marker("health"), ext, marker("web"))

	cases := map[string]string{
		"/healthz":                "health",
		"/readyz":                 "health",
		"/api/v1/me":              "api",
		"/api/v1/auth/saml/start": "api",
		"/saml/acs":               "saml",
		"/":                       "web",
		"/jobs/3?tab=definition":  "web",
		"/assets/index-abc.js":    "web",
		"/api/v2/anything":        "health",
		"/healthz/extra":          "web",
	}
	for target, want := range cases {
		if got := serveRequest(h, http.MethodGet, target).Body.String(); got != want {
			t.Errorf("GET %s answered by %q, want %q", target, got, want)
		}
	}
}

func TestServiceHandlerWithoutWebKeepsHealthAtRoot(t *testing.T) {
	h := serviceHandler(marker("health"), Extension{Handlers: map[string]http.Handler{"/api/v1/": marker("api")}}, nil)
	for target, want := range map[string]string{"/": "health", "/healthz": "health", "/api/v1/x": "api"} {
		if got := serveRequest(h, http.MethodGet, target).Body.String(); got != want {
			t.Errorf("GET %s answered by %q, want %q", target, got, want)
		}
	}
	// No extension and no web app: the health handler is served as is.
	if got := serveRequest(serviceHandler(marker("health"), Extension{}, nil), http.MethodGet, "/x").Body.String(); got != "health" {
		t.Errorf("bare service answered %q, want health", got)
	}
}

func TestOrchestratorServesTheWebApplication(t *testing.T) {
	svc := NewOrchestratorService(config.Orchestrator{})
	if svc.Web == nil {
		t.Fatal("the orchestrator service has no web handler")
	}
	rec := serveRequest(svc.Web, http.MethodGet, "/jobs")
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'self'") {
		t.Errorf("web handler did not answer with the web application's headers: %v", rec.Header())
	}
}

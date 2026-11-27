//go:build integration

package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"encoding/xml"
	"html"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/crewjam/saml"
	valkey "github.com/valkey-io/valkey-go"

	"github.com/rcss-labs/yamdrake/internal/cache"
	"github.com/rcss-labs/yamdrake/internal/store"
)

const (
	testIDPEntityID = "https://idp.test/metadata"
	relayCookieName = "yumdrake_saml_relay"
)

// testIDP is an in-process identity provider built on the SAML library's own
// identity provider. tweak edits the assertion after it is made and before it
// is signed, so a test can issue a response that is wrong in one way and still
// correctly signed.
type testIDP struct {
	t       *testing.T
	h       *harness
	idp     *saml.IdentityProvider
	session saml.Session
	tweak   func(req *saml.IdpAuthnRequest)
}

type tweakingMaker struct{ idp *testIDP }

func (m tweakingMaker) MakeAssertion(req *saml.IdpAuthnRequest, session *saml.Session) error {
	if err := (saml.DefaultAssertionMaker{}).MakeAssertion(req, session); err != nil {
		return err
	}
	if m.idp.tweak != nil {
		m.idp.tweak(req)
	}
	return nil
}

type fixedSession struct{ idp *testIDP }

func (p fixedSession) GetSession(_ http.ResponseWriter, _ *http.Request, _ *saml.IdpAuthnRequest) *saml.Session {
	s := p.idp.session
	return &s
}

type spMetadataSource struct{ idp *testIDP }

func (p spMetadataSource) GetServiceProvider(_ *http.Request, _ string) (*saml.EntityDescriptor, error) {
	rec := httptest.NewRecorder()
	p.idp.h.handler.ServeHTTP(rec, httptest.NewRequest("GET", "/saml/metadata", nil))
	var md saml.EntityDescriptor
	if err := xml.Unmarshal(rec.Body.Bytes(), &md); err != nil {
		return nil, err
	}
	return &md, nil
}

func selfSignedCert(t *testing.T, key *rsa.PrivateKey, cn string) *x509.Certificate {
	t.Helper()
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature,
	}, &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
	}, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	return cert
}

// newIDP builds an identity provider named testIDPEntityID with its own key.
func (h *harness) newIDP() *testIDP {
	h.t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		h.t.Fatalf("generate key: %v", err)
	}
	i := &testIDP{t: h.t, h: h}
	meta, _ := url.Parse(testIDPEntityID)
	sso, _ := url.Parse("https://idp.test/sso")
	i.idp = &saml.IdentityProvider{
		Key: key, Certificate: selfSignedCert(h.t, key, "test idp"),
		MetadataURL: *meta, SSOURL: *sso,
		ServiceProviderProvider: spMetadataSource{i},
		SessionProvider:         fixedSession{i},
		AssertionMaker:          tweakingMaker{i},
	}
	return i
}

// rival returns an identity provider with the same name and a different key.
func (i *testIDP) rival() *testIDP {
	r := i.h.newIDP()
	r.session = i.session
	return r
}

func (i *testIDP) metadataXML() string {
	raw, err := xml.Marshal(i.idp.Metadata())
	if err != nil {
		i.t.Fatalf("marshal idp metadata: %v", err)
	}
	return string(raw)
}

// signIn sets who the identity provider will say is signing in.
func (i *testIDP) signIn(email, name string, groups ...string) {
	values := make([]saml.AttributeValue, 0, len(groups))
	for _, g := range groups {
		values = append(values, saml.AttributeValue{Type: "xs:string", Value: g})
	}
	i.session = saml.Session{
		ID: "idp-session", CreateTime: time.Now(), ExpireTime: time.Now().Add(time.Hour), Index: "1",
		NameID: email, NameIDFormat: string(saml.EmailAddressNameIDFormat), UserEmail: email,
		CustomAttributes: []saml.Attribute{
			{Name: "groups", Values: values},
			{Name: "displayName", Values: []saml.AttributeValue{{Type: "xs:string", Value: name}}},
		},
	}
}

// configureSSO enables SSO against idp through the admin API.
func (h *harness) configureSSO(idp *testIDP) {
	h.t.Helper()
	rec := h.send(sent{method: "PUT", path: "/api/v1/admin/sso", bearer: h.adminToken, body: map[string]any{
		"enabled": true, "idp_metadata_xml": idp.metadataXML(),
	}})
	h.expect(rec, http.StatusOK, "")
}

// samlFlow is one sign-in attempt up to the POST to the assertion consumer.
type samlFlow struct {
	form   url.Values
	relay  *http.Cookie
	locURL *url.URL
}

var signatureElement = regexp.MustCompile(`(?s)<ds:Signature.*?</ds:Signature>`)
var formField = regexp.MustCompile(`name="(SAMLResponse|RelayState)" value="([^"]*)"`)

// begin starts sign-in, lets the identity provider answer and returns the form
// the browser would post.
func (i *testIDP) begin() samlFlow {
	i.t.Helper()
	rec := httptest.NewRecorder()
	i.h.handler.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/auth/saml/start", nil))
	if rec.Code != http.StatusFound {
		i.t.Fatalf("start: status %d %s, want 302", rec.Code, failure(rec))
	}
	var relay *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == relayCookieName {
			relay = c
		}
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil || loc.Host != "idp.test" || loc.Query().Get("SAMLRequest") == "" || loc.Query().Get("Signature") == "" {
		i.t.Fatalf("start: Location %q is not a signed redirect to the identity provider", rec.Header().Get("Location"))
	}
	if relay == nil || loc.Query().Get("RelayState") == "" {
		i.t.Fatalf("start: no relay state cookie or RelayState parameter")
	}
	idpReq := httptest.NewRequest("GET", loc.String(), nil)
	idpReq.RemoteAddr = "192.0.2.1:4000"
	idpRec := httptest.NewRecorder()
	i.idp.ServeSSO(idpRec, idpReq)
	if idpRec.Code != http.StatusOK {
		i.t.Fatalf("identity provider answered %d", idpRec.Code)
	}
	form := url.Values{}
	for _, m := range formField.FindAllStringSubmatch(idpRec.Body.String(), -1) {
		form.Set(m[1], html.UnescapeString(m[2]))
	}
	if form.Get("SAMLResponse") == "" {
		i.t.Fatalf("identity provider sent no SAMLResponse")
	}
	return samlFlow{form: form, relay: relay, locURL: loc}
}

// unsigned removes every signature from the response.
func (f samlFlow) unsigned(t *testing.T) samlFlow {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(f.form.Get("SAMLResponse"))
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	stripped := signatureElement.ReplaceAllString(string(raw), "")
	if stripped == string(raw) {
		t.Fatalf("the response held no signature to remove")
	}
	f.form = url.Values{"SAMLResponse": {base64.StdEncoding.EncodeToString([]byte(stripped))}, "RelayState": {f.form.Get("RelayState")}}
	return f
}

func (h *harness) postACS(f samlFlow, withCookie bool) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest("POST", "/saml/acs", strings.NewReader(f.form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// The identity provider posts from its own origin.
	req.Header.Set("Origin", "https://idp.test")
	if withCookie && f.relay != nil {
		req.AddCookie(&http.Cookie{Name: f.relay.Name, Value: f.relay.Value})
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

// ssoSignIn runs a whole sign-in and expects it to succeed. It returns the
// session id.
func (h *harness) ssoSignIn(idp *testIDP) string {
	h.t.Helper()
	rec := h.postACS(idp.begin(), true)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		h.t.Fatalf("acs: status %d %s, want 303 to /", rec.Code, failure(rec))
	}
	cookie := sessionCookie(rec)
	if cookie == nil {
		h.t.Fatalf("acs: no session cookie")
	}
	return cookie.Value
}

func (h *harness) me(sessionID string) principalResponse {
	h.t.Helper()
	rec := h.send(sent{method: "GET", path: "/api/v1/me", sessionID: sessionID})
	h.expect(rec, http.StatusOK, "")
	var p principalResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		h.t.Fatalf("decode me: %v", err)
	}
	return p
}

func (h *harness) auditCount(action string) int {
	n := 0
	for _, e := range h.auditEntries() {
		if e.Action == action {
			n++
		}
	}
	return n
}

func (h *harness) userCount() int {
	users, err := store.ListUsers(context.Background(), h.pool)
	if err != nil {
		h.t.Fatalf("ListUsers: %v", err)
	}
	return len(users)
}

// ssoSetup configures SSO and maps the identity provider group eng-devops to an
// admin group.
func ssoSetup(t *testing.T) (*harness, *testIDP, store.Group) {
	t.Helper()
	h := newHarness(t)
	idp := h.newIDP()
	h.configureSSO(idp)
	group, err := store.CreateGroup(context.Background(), h.pool, "sso-admins", true)
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if err := store.SetIDPMapping(context.Background(), h.pool, "eng-devops", group.ID); err != nil {
		t.Fatalf("SetIDPMapping: %v", err)
	}
	return h, idp, group
}

func TestMetadataIsServedAndNamesACS(t *testing.T) {
	h := newHarness(t)
	rec := h.send(sent{method: "GET", path: "/saml/metadata"})
	h.expect(rec, http.StatusOK, "")
	if !strings.Contains(rec.Header().Get("Content-Type"), "xml") {
		t.Errorf("Content-Type = %q, want XML", rec.Header().Get("Content-Type"))
	}
	var md saml.EntityDescriptor
	if err := xml.Unmarshal(rec.Body.Bytes(), &md); err != nil {
		t.Fatalf("metadata is not XML: %v", err)
	}
	if md.EntityID != publicURL+"/saml/metadata" {
		t.Errorf("entity id = %q, want the default %q", md.EntityID, publicURL+"/saml/metadata")
	}
	if len(md.SPSSODescriptors) != 1 {
		t.Fatalf("want one SP descriptor, got %d", len(md.SPSSODescriptors))
	}
	sp := md.SPSSODescriptors[0]
	var acs []string
	for _, e := range sp.AssertionConsumerServices {
		if e.Binding == saml.HTTPPostBinding {
			acs = append(acs, e.Location)
		}
	}
	if len(acs) != 1 || acs[0] != publicURL+"/saml/acs" {
		t.Errorf("POST assertion consumer locations = %v, want [%s/saml/acs]", acs, publicURL)
	}
	if len(sp.KeyDescriptors) == 0 {
		t.Errorf("metadata publishes no signing certificate")
	}
}

func TestSSOHappyPathCreatesUserMapsGroupsAndSignsIn(t *testing.T) {
	h, idp, _ := ssoSetup(t)
	email := uniqueEmail(t, "alice")
	idp.signIn(email, "Alice Example", "eng-devops", "unmapped")

	flow := idp.begin()
	if c := flow.relay; !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteNoneMode || c.MaxAge <= 0 || c.MaxAge > 900 || c.Path != "/saml/acs" {
		t.Errorf("relay cookie = %+v, want Secure HttpOnly SameSite=None, Path=/saml/acs, short-lived", c)
	}
	rec := h.postACS(flow, true)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("acs: status %d %s, want 303 to /", rec.Code, failure(rec))
	}
	cookie := sessionCookie(rec)
	if cookie == nil || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" {
		t.Fatalf("session cookie = %+v, want Secure HttpOnly SameSite=Lax at /", cookie)
	}
	me := h.me(cookie.Value)
	if me.Email != email || me.Name != "Alice Example" || !me.Admin {
		t.Errorf("me = %+v, want %s, Alice Example, admin", me, email)
	}
	user, err := store.UserByEmail(context.Background(), h.pool, email)
	if err != nil || user.Source != "saml" {
		t.Fatalf("user = %+v, %v; want a saml user", user, err)
	}
	groups, _ := store.GroupsOfUser(context.Background(), h.pool, user.ID)
	if len(groups) != 1 || groups[0].Name != "sso-admins" {
		t.Errorf("groups = %+v, want only sso-admins (the unmapped group is ignored)", groups)
	}
	var login *store.AuditEntry
	for _, e := range h.auditEntries() {
		if e.Action == "auth.login" {
			login = &e
		}
	}
	var loginDetail map[string]any
	if login != nil {
		_ = json.Unmarshal(login.Detail, &loginDetail)
	}
	if login == nil || loginDetail["via"] != "saml" || login.Actor != email {
		t.Errorf("auth.login audit entry = %+v, want via saml by the user", login)
	}
}

func TestGroupRemovedAtIDPIsRemovedAtNextSignIn(t *testing.T) {
	h, idp, _ := ssoSetup(t)
	email := uniqueEmail(t, "bob")
	idp.signIn(email, "Bob", "eng-devops")
	if !h.me(h.ssoSignIn(idp)).Admin {
		t.Fatalf("first sign-in did not grant the mapped admin group")
	}
	idp.signIn(email, "Bob", "other")
	if h.me(h.ssoSignIn(idp)).Admin {
		t.Errorf("the group was removed at the identity provider but the user is still an admin")
	}
}

func TestManualMembershipSurvivesSSO(t *testing.T) {
	h, idp, _ := ssoSetup(t)
	ctx := context.Background()
	email := uniqueEmail(t, "carol")
	user, err := store.UpsertSAMLUser(ctx, h.pool, email, "Carol", time.Now())
	if err != nil {
		t.Fatalf("UpsertSAMLUser: %v", err)
	}
	manual, _ := store.CreateGroup(ctx, h.pool, "manual-only", false)
	if err := store.SetManualMembership(ctx, h.pool, user.ID, manual.ID, true); err != nil {
		t.Fatalf("SetManualMembership: %v", err)
	}
	idp.signIn(email, "Carol", "eng-devops")
	h.ssoSignIn(idp)
	idp.signIn(email, "Carol") // no groups at all
	h.ssoSignIn(idp)
	groups, _ := store.GroupsOfUser(ctx, h.pool, user.ID)
	if len(groups) != 1 || groups[0].ID != manual.ID {
		t.Errorf("groups = %+v, want only the manual one", groups)
	}
}

func TestRejects(t *testing.T) {
	h, idp, _ := ssoSetup(t)
	ctx := context.Background()
	past := func(d time.Duration) func(*saml.IdpAuthnRequest) {
		return func(req *saml.IdpAuthnRequest) {
			a := req.Assertion
			a.IssueInstant = a.IssueInstant.Add(d)
			a.Conditions.NotBefore = a.Conditions.NotBefore.Add(d)
			a.Conditions.NotOnOrAfter = a.Conditions.NotOnOrAfter.Add(d)
			a.Subject.SubjectConfirmations[0].SubjectConfirmationData.NotOnOrAfter =
				a.Subject.SubjectConfirmations[0].SubjectConfirmationData.NotOnOrAfter.Add(d)
			req.Now = req.Now.Add(d)
		}
	}
	rows := []struct {
		name   string
		reason string
		setup  func(t *testing.T) samlFlow // builds the request to post
		cookie bool                        // send the relay cookie
		twice  bool                        // post the same response again; the second post is the rejected one
	}{
		{"unsigned", "bad_signature", func(t *testing.T) samlFlow { return idp.begin().unsigned(t) }, true, false},
		{"signed by another key", "bad_signature", func(t *testing.T) samlFlow {
			return idp.rival().begin()
		}, true, false},
		{"expired", "expired", func(t *testing.T) samlFlow {
			idp.tweak = past(-10 * time.Minute)
			return idp.begin()
		}, true, false},
		{"not yet valid", "not_yet_valid", func(t *testing.T) samlFlow {
			idp.tweak = past(10 * time.Minute)
			return idp.begin()
		}, true, false},
		{"wrong audience", "wrong_audience", func(t *testing.T) samlFlow {
			idp.tweak = func(req *saml.IdpAuthnRequest) {
				req.Assertion.Conditions.AudienceRestrictions[0].Audience.Value = "https://other-sp.test/metadata"
			}
			return idp.begin()
		}, true, false},
		{"wrong destination", "wrong_destination", func(t *testing.T) samlFlow {
			idp.tweak = func(req *saml.IdpAuthnRequest) {
				endpoint := *req.ACSEndpoint
				endpoint.Location = publicURL + "/saml/elsewhere"
				req.ACSEndpoint = &endpoint
			}
			return idp.begin()
		}, true, false},
		{"no relay cookie", "relay_state", func(t *testing.T) samlFlow { return idp.begin() }, false, false},
		{"relay state does not match the cookie", "relay_state", func(t *testing.T) samlFlow {
			f := idp.begin()
			f.form.Set("RelayState", "not-the-one-we-issued")
			return f
		}, true, false},
		{"same response posted twice", "replay", func(t *testing.T) samlFlow { return idp.begin() }, true, true},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			email := uniqueEmail(t, "mallory")
			idp.signIn(email, "Mallory", "eng-devops")
			idp.tweak = nil
			flow := row.setup(t)
			idp.tweak = nil
			if row.twice {
				first := h.postACS(flow, row.cookie)
				if first.Code != http.StatusSeeOther {
					t.Fatalf("first post: status %d %s, want 303", first.Code, failure(first))
				}
			}
			usersBefore, rejectedBefore := h.userCount(), h.auditCount("saml.rejected")
			rec := h.postACS(flow, row.cookie)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d %s, want 400", rec.Code, failure(rec))
			}
			if sessionCookie(rec) != nil {
				t.Errorf("a rejected response set a session cookie")
			}
			if got := h.userCount(); got != usersBefore {
				t.Errorf("users: %d before, %d after the rejected post", usersBefore, got)
			}
			if !row.twice {
				if _, err := store.UserByEmail(ctx, h.pool, email); err == nil {
					t.Errorf("a rejected response created the user")
				}
			}
			if got := h.auditCount("saml.rejected"); got != rejectedBefore+1 {
				t.Fatalf("saml.rejected entries: %d before, %d after, want one more", rejectedBefore, got)
			}
			var entry *store.AuditEntry
			for _, e := range h.auditEntries() {
				if e.Action == "saml.rejected" {
					entry = &e
					break
				}
			}
			var detail map[string]any
			_ = json.Unmarshal(entry.Detail, &detail)
			if detail["reason"] != row.reason {
				t.Errorf("reason = %v, want %s", detail["reason"], row.reason)
			}
			for key := range detail {
				if key != "reason" {
					t.Errorf("audit detail has the extra key %q", key)
				}
			}
			if strings.Contains(string(entry.Detail), "SAMLResponse") || strings.Contains(string(entry.Detail), "Assertion") {
				t.Errorf("audit detail holds assertion material")
			}
		})
	}
}

func TestDisabledUserCannotSSO(t *testing.T) {
	h, idp, _ := ssoSetup(t)
	ctx := context.Background()
	email := uniqueEmail(t, "dave")
	user, err := store.UpsertSAMLUser(ctx, h.pool, email, "Dave", time.Now())
	if err != nil {
		t.Fatalf("UpsertSAMLUser: %v", err)
	}
	if err := store.SetDisabled(ctx, h.pool, user.ID, true); err != nil {
		t.Fatalf("SetDisabled: %v", err)
	}
	idp.signIn(email, "Dave", "eng-devops")
	rec := h.postACS(idp.begin(), true)
	h.expect(rec, http.StatusForbidden, "forbidden")
	if sessionCookie(rec) != nil {
		t.Errorf("a disabled user got a session")
	}
	groups, _ := store.GroupsOfUser(ctx, h.pool, user.ID)
	if len(groups) != 0 {
		t.Errorf("a disabled user's memberships changed: %+v", groups)
	}
	if h.auditCount("saml.rejected") != 1 || h.auditCount("auth.login") != 0 {
		t.Errorf("audit actions = %v, want one saml.rejected and no auth.login", h.auditActions())
	}
}

func TestSSORefusesTheEmailOfALocalUser(t *testing.T) {
	h, idp, _ := ssoSetup(t)
	local := h.newUser("local")
	idp.signIn(local.Email, "Not The Local User", "eng-devops")
	rec := h.postACS(idp.begin(), true)
	h.expect(rec, http.StatusForbidden, "forbidden")
	if sessionCookie(rec) != nil || h.auditCount("saml.rejected") != 1 {
		t.Errorf("a SAML sign-in took over a local account")
	}
	got, _ := store.UserByEmail(context.Background(), h.pool, local.Email)
	if got.Source != "local" || got.Name != local.Name {
		t.Errorf("the local user was changed: %+v", got)
	}
}

func TestSSOUnavailable(t *testing.T) {
	h := newHarness(t)
	// Not configured: start and acs refuse.
	h.expect(h.send(sent{method: "GET", path: "/api/v1/auth/saml/start"}), http.StatusNotFound, "not_found")
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, httptest.NewRequest("POST", "/saml/acs", strings.NewReader("SAMLResponse=x")))
	if rec.Code != http.StatusNotFound {
		t.Errorf("acs while SSO is off: status %d, want 404", rec.Code)
	}

	// Valkey down at the assertion consumer: 503, never an accepted response.
	idp := h.newIDP()
	h.configureSSO(idp)
	idp.signIn(uniqueEmail(t, "erin"), "Erin")
	flow := idp.begin()
	h.rebuild(func(d *Deps) { d.Valkey = deadValkey(t) })
	rec = h.postACS(flow, true)
	if rec.Code != http.StatusServiceUnavailable || sessionCookie(rec) != nil {
		t.Errorf("acs with Valkey down: status %d %s, want 503 and no session", rec.Code, failure(rec))
	}
	if h.auditCount("auth.login") != 0 {
		t.Errorf("a sign-in was audited while Valkey was down")
	}
}

func TestSPKeyIsStoredEncryptedAndReused(t *testing.T) {
	h := newHarness(t)
	certs := make([]string, 8)
	var wg gosync.WaitGroup
	for i := range certs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			h.handler.ServeHTTP(rec, httptest.NewRequest("GET", "/saml/metadata", nil))
			var md saml.EntityDescriptor
			if err := xml.Unmarshal(rec.Body.Bytes(), &md); err == nil && len(md.SPSSODescriptors) == 1 && len(md.SPSSODescriptors[0].KeyDescriptors) > 0 {
				certs[i] = md.SPSSODescriptors[0].KeyDescriptors[0].KeyInfo.X509Data.X509Certificates[0].Data
			}
		}()
	}
	wg.Wait()
	for _, c := range certs {
		if c == "" || c != certs[0] {
			t.Fatalf("concurrent first requests published different certificates")
		}
	}

	var rows int
	if err := h.pool.QueryRow(context.Background(), "SELECT count(*) FROM instance_settings WHERE key = 'saml_sp_key'").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("stored SP keys = %d, %v; want exactly one", rows, err)
	}
	var value []byte
	if err := h.pool.QueryRow(context.Background(), "SELECT value FROM instance_settings WHERE key = 'saml_sp_key'").Scan(&value); err != nil {
		t.Fatalf("read stored key: %v", err)
	}
	if strings.Contains(string(value), "PRIVATE KEY") {
		t.Fatalf("the stored SP key is not encrypted")
	}
	var stored struct {
		Key  []byte `json:"key"`
		Cert string `json:"cert"`
	}
	if err := json.Unmarshal(value, &stored); err != nil {
		t.Fatalf("stored key document: %v", err)
	}
	plain, err := h.cipher.Decrypt(context.Background(), "saml-sp-key", stored.Key)
	if err != nil {
		t.Fatalf("the stored key does not decrypt under purpose saml-sp-key: %v", err)
	}
	block, _ := pem.Decode(plain)
	if block == nil {
		t.Fatalf("decrypted key is not PEM")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil || key.N.BitLen() != 2048 {
		t.Fatalf("decrypted key = %v, %v; want RSA 2048", key, err)
	}
	certBlock, _ := pem.Decode([]byte(stored.Cert))
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		t.Fatalf("stored certificate: %v", err)
	}
	if life := cert.NotAfter.Sub(cert.NotBefore); life < 9*365*24*time.Hour || life > 11*365*24*time.Hour {
		t.Errorf("certificate lifetime = %v, want about 10 years", life)
	}
	if err := cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature); err != nil || cert.Subject.String() != cert.Issuer.String() {
		t.Errorf("the certificate is not self-signed: %v", err)
	}
	if encoded := base64.StdEncoding.EncodeToString(cert.Raw); encoded != certs[0] {
		t.Errorf("metadata publishes a different certificate than the stored one")
	}
}

func TestSSOSettingsValidation(t *testing.T) {
	h := newHarness(t)
	idp := h.newIDP()
	put := func(body map[string]any) *httptest.ResponseRecorder {
		return h.send(sent{method: "PUT", path: "/api/v1/admin/sso", bearer: h.adminToken, body: body})
	}
	h.expect(put(map[string]any{"enabled": true, "idp_metadata_xml": "this is not metadata"}), http.StatusBadRequest, "invalid")
	h.expect(put(map[string]any{"enabled": true}), http.StatusBadRequest, "invalid") // enabling needs metadata
	h.expect(put(map[string]any{"enabled": false, "idp_metadata_xml": idp.metadataXML(), "bogus": 1}), http.StatusBadRequest, "invalid")

	h.expect(put(map[string]any{"enabled": true, "idp_metadata_xml": idp.metadataXML(), "groups_attribute": "memberOf"}), http.StatusOK, "")
	rec := h.send(sent{method: "GET", path: "/api/v1/admin/sso", bearer: h.adminToken})
	h.expect(rec, http.StatusOK, "")
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["enabled"] != true || got["groups_attribute"] != "memberOf" || got["name_attribute"] != "displayName" ||
		got["entity_id"] != publicURL+"/saml/metadata" {
		t.Errorf("settings = %v, want enabled, memberOf and the defaults", got)
	}
	h.expect(h.send(sent{method: "POST", path: "/api/v1/admin/sso/test", bearer: h.adminToken}), http.StatusOK, "")
	if h.auditCount("sso.update") != 1 {
		t.Errorf("audit actions = %v, want one sso.update", h.auditActions())
	}

	// Metadata that parses but has no sign-on endpoint or certificate can be
	// stored while SSO is off, cannot be switched on, and fails the test endpoint.
	bare := `<EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata" entityID="https://idp.test/x"><IDPSSODescriptor protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol"/></EntityDescriptor>`
	h.expect(put(map[string]any{"enabled": true, "idp_metadata_xml": bare}), http.StatusBadRequest, "invalid")
	h.expect(put(map[string]any{"enabled": false, "idp_metadata_xml": bare}), http.StatusOK, "")
	h.expect(h.send(sent{method: "POST", path: "/api/v1/admin/sso/test", bearer: h.adminToken}), http.StatusBadRequest, "invalid")

	// Only manage_access may read or change the settings.
	plain := h.newUser("plain")
	token, _ := h.newToken(plain.ID)
	for _, r := range []sent{
		{method: "GET", path: "/api/v1/admin/sso"},
		{method: "PUT", path: "/api/v1/admin/sso", body: map[string]any{"enabled": false}},
		{method: "POST", path: "/api/v1/admin/sso/test"},
	} {
		r.bearer = token
		h.expect(h.send(r), http.StatusForbidden, "forbidden")
	}
}

// deadValkey is a client for an address nothing listens on.
func deadValkey(t *testing.T) valkey.Client {
	t.Helper()
	c, err := cache.Open("127.0.0.1:1")
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	t.Cleanup(c.Close)
	return c
}

// Two posts of one response racing: the replay check lets exactly one win.
func TestConcurrentPostsOfOneResponseSignInOnce(t *testing.T) {
	h, idp, _ := ssoSetup(t)
	idp.signIn(uniqueEmail(t, "frank"), "Frank", "eng-devops")
	flow := idp.begin()
	const posts = 6
	codes := make([]int, posts)
	var wg gosync.WaitGroup
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = h.postACS(flow, true).Code
		}()
	}
	wg.Wait()
	won := 0
	for _, c := range codes {
		if c == http.StatusSeeOther {
			won++
		} else if c != http.StatusBadRequest {
			t.Errorf("status %d, want 303 or 400", c)
		}
	}
	if won != 1 {
		t.Errorf("%d of %d concurrent posts signed in, want exactly one (codes %v)", won, posts, codes)
	}
}

func TestStartPostsTheRequestWhenOnlyThePostBindingIsOffered(t *testing.T) {
	h := newHarness(t)
	idp := h.newIDP()
	md := idp.idp.Metadata()
	var postOnly []saml.Endpoint
	for _, e := range md.IDPSSODescriptors[0].SingleSignOnServices {
		if e.Binding == saml.HTTPPostBinding {
			postOnly = append(postOnly, e)
		}
	}
	if len(postOnly) == 0 {
		t.Fatalf("the test identity provider offers no POST binding")
	}
	md.IDPSSODescriptors[0].SingleSignOnServices = postOnly
	raw, err := xml.Marshal(md)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	h.expect(h.send(sent{method: "PUT", path: "/api/v1/admin/sso", bearer: h.adminToken,
		body: map[string]any{"enabled": true, "idp_metadata_xml": string(raw)}}), http.StatusOK, "")
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/auth/saml/start", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "SAMLRequest") ||
		!strings.Contains(rec.Header().Get("Content-Security-Policy"), "nonce-") {
		t.Fatalf("start: status %d %s, want an auto-posting form with a CSP nonce", rec.Code, failure(rec))
	}
}

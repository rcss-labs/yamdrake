package api

import (
	"bytes"
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/crewjam/saml"
	"github.com/crewjam/saml/samlsp"
	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/yamdrake/internal/audit"
	"github.com/rcss-labs/yamdrake/internal/auth"
	"github.com/rcss-labs/yamdrake/internal/session"
	"github.com/rcss-labs/yamdrake/internal/store"
)

const (
	// samlRelayCookie ties an assertion consumer post to the browser that
	// started the sign-in. The identity provider posts to /saml/acs from its
	// own site, and a SameSite=Lax cookie is not sent on a cross-site POST, so
	// the cookie must be SameSite=None (which requires Secure). It carries the
	// request id, a nonce and the post-sign-in path under an HMAC, so nothing is
	// stored when sign-in starts, and it is scoped to the one path that reads it.
	samlRelayCookie = "yumdrake_saml_relay"
	samlRelayTTL    = 10 * time.Minute
	samlRelayBytes  = 32
	samlACSPath     = "/saml/acs"

	maxACSBodyBytes = 2 << 20

	// rsaSHA256 is the XML signature method for the requests we sign (the key is RSA).
	rsaSHA256 = "http://www.w3.org/2001/04/xmldsig-more#rsa-sha256"
)

func init() {
	// The library's default allowance for clock differences between the
	// identity provider and us is 180 seconds; ours is 60.
	saml.MaxClockSkew = 60 * time.Second
}

// errSSODisabled means single sign-on is switched off or not set up.
var errSSODisabled = errors.New("single sign-on is not enabled")

// errSSOMisconfigured means single sign-on is switched on but the stored
// identity provider settings cannot be used.
var errSSOMisconfigured = errors.New("single sign-on is not set up correctly")

// The codes a failed browser sign-in puts in /sign-in?sso_error=. They are
// short and fixed on purpose: the reason (audit entry, log) never reaches the URL.
const (
	ssoErrorRejected    = "rejected"    // the response was not accepted
	ssoErrorRefused     = "refused"     // the account may not sign in
	ssoErrorUnavailable = "unavailable" // single sign-on is off or not set up
)

// redirectSSOError answers a browser that reached a single sign-on route by
// top-level navigation: a 303 to the sign-in page with a fixed code. The target
// is a constant path, never built from the request.
func redirectSSOError(w http.ResponseWriter, r *http.Request, code string) {
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/sign-in?sso_error="+code, http.StatusSeeOther)
}

// ssoUnavailable handles the newProvider errors that mean "not usable": the
// browser is sent back to sign-in. It reports whether it answered.
func (s *server) ssoUnavailable(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case errors.Is(err, errSSODisabled):
		redirectSSOError(w, r, ssoErrorUnavailable)
	case errors.Is(err, errSSOMisconfigured):
		s.Log.Warn("single sign-on is misconfigured", "error", err.Error())
		redirectSSOError(w, r, ssoErrorUnavailable)
	default:
		return false
	}
	return true
}

// ssoFailed handles an unexpected failure (database, Valkey, key material) on a
// route a browser reaches by top-level navigation: the error is logged exactly
// as before and the browser is sent back to sign-in with a fixed code. Valkey
// being unavailable reads as "unavailable", everything else as "rejected".
func (s *server) ssoFailed(w http.ResponseWriter, r *http.Request, err error, code string) {
	s.Log.Error("single sign-on request failed", "method", r.Method, "path", r.URL.Path, "error", err.Error())
	redirectSSOError(w, r, code)
}

// samlProvider is the service provider for one request, with the settings it
// was built from.
type samlProvider struct {
	*saml.ServiceProvider
	settings auth.SAMLSettings
}

func (s *server) effectiveEntityID(settings auth.SAMLSettings) string {
	if settings.EntityID != "" {
		return settings.EntityID
	}
	return strings.TrimRight(s.PublicURL, "/") + "/saml/metadata"
}

// newProvider builds the service provider from the stored settings and key.
// With needIDP it also loads the identity provider's metadata and answers
// errSSODisabled unless single sign-on is enabled.
func (s *server) newProvider(ctx context.Context, needIDP, needKey bool) (*samlProvider, error) {
	settings, err := auth.LoadSAMLSettings(ctx, s.Pool)
	if err != nil {
		return nil, err
	}
	var idp *saml.EntityDescriptor
	if needIDP {
		if !settings.Enabled || settings.IDPMetadataXML == "" {
			return nil, errSSODisabled
		}
		if idp, err = samlsp.ParseMetadata([]byte(settings.IDPMetadataXML)); err != nil {
			return nil, fmt.Errorf("stored identity provider metadata: %w: %w", errSSOMisconfigured, err)
		}
	}
	// Anonymous metadata requests read the certificate only; the private key
	// is decrypted for the routes that sign or decrypt with it.
	var key *rsa.PrivateKey
	var cert *x509.Certificate
	if needKey {
		key, cert, err = auth.LoadSPKeyPair(ctx, s.Pool, s.Cipher, s.Now())
	} else {
		cert, err = auth.LoadSPCertificate(ctx, s.Pool, s.Cipher, s.Now())
	}
	if err != nil {
		return nil, err
	}
	var signer crypto.Signer // stays a nil interface without a key
	if key != nil {
		signer = key
	}
	base := strings.TrimRight(s.PublicURL, "/")
	metadataURL, err := url.Parse(base + "/saml/metadata")
	if err != nil {
		return nil, fmt.Errorf("public url: %w", err)
	}
	acsURL, err := url.Parse(base + samlACSPath)
	if err != nil {
		return nil, fmt.Errorf("public url: %w", err)
	}
	nameIDFormat := saml.UnspecifiedNameIDFormat
	if settings.EmailAttribute == "" {
		nameIDFormat = saml.EmailAddressNameIDFormat // the NameID is the email address
	}
	return &samlProvider{settings: settings, ServiceProvider: &saml.ServiceProvider{
		EntityID:    s.effectiveEntityID(settings),
		Key:         signer,
		Certificate: cert,
		MetadataURL: *metadataURL,
		AcsURL:      *acsURL,
		IDPMetadata: idp,
		// Responses arrive only by the HTTP-POST binding.
		AssertionConsumerServiceBindings: []string{saml.HTTPPostBinding},
		AuthnNameIDFormat:                nameIDFormat,
		SignatureMethod:                  rsaSHA256,
		RejectSHA1Signatures:             true,
		RejectUnknownConditions:          true,
		RequireSignedResponseForCBC:      true,
	}}, nil
}

// samlMetadata serves the service provider metadata the administrator hands to
// the identity provider. It works before single sign-on is enabled.
func (s *server) samlMetadata(w http.ResponseWriter, r *http.Request) {
	sp, err := s.newProvider(r.Context(), false, false)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	metadata := sp.Metadata()
	// We publish a signing key only. Assertions then arrive unencrypted inside
	// the TLS-protected browser post, and we never decrypt attacker-chosen
	// ciphertext (the CBC padding-oracle surface that comes with it).
	for i := range metadata.SPSSODescriptors {
		var keys []saml.KeyDescriptor
		for _, key := range metadata.SPSSODescriptors[i].KeyDescriptors {
			if key.Use != "encryption" {
				keys = append(keys, key)
			}
		}
		metadata.SPSSODescriptors[i].KeyDescriptors = keys
	}
	body, err := xml.MarshalIndent(metadata, "", "  ")
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/samlmetadata+xml")
	_, _ = w.Write(append([]byte(xml.Header), body...))
}

func randomHandle() (string, error) {
	raw := make([]byte, samlRelayBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// sealRelayState makes the relay cookie: nonce, request id and expiry,
// authenticated with an HMAC under a server key. Nothing is stored server-side,
// so an anonymous start request costs no memory. The nonce is also the
// RelayState the identity provider echoes, so a response can only be consumed
// by the browser that holds the cookie.
func sealRelayState(key []byte, nonce, requestID, next string, expires time.Time) string {
	payload := nonce + "." + requestID + "." + strconv.FormatInt(expires.Unix(), 10) + "." + base64.RawURLEncoding.EncodeToString([]byte(next))
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// openRelayState verifies a relay cookie (MAC in constant time, then expiry)
// and returns its nonce and request id.
func openRelayState(key []byte, value string, now time.Time) (nonce, requestID, next string, ok bool) {
	encodedPayload, encodedMAC, found := strings.Cut(value, ".")
	if !found {
		return "", "", "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(encodedPayload)
	if err != nil {
		return "", "", "", false
	}
	got, err := base64.RawURLEncoding.DecodeString(encodedMAC)
	if err != nil {
		return "", "", "", false
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	if !hmac.Equal(got, mac.Sum(nil)) {
		return "", "", "", false
	}
	parts := strings.Split(string(payload), ".")
	if len(parts) != 4 {
		return "", "", "", false
	}
	expiry, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || !now.Before(time.Unix(expiry, 0)) {
		return "", "", "", false
	}
	nextBytes, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		return "", "", "", false
	}
	return parts[0], parts[1], string(nextBytes), true
}

// samlStart sends the browser to the identity provider with a signed request.
func (s *server) samlStart(w http.ResponseWriter, r *http.Request) {
	sp, err := s.newProvider(r.Context(), true, true)
	if s.ssoUnavailable(w, r, err) {
		return
	}
	if err != nil {
		s.ssoFailed(w, r, err, ssoErrorRejected)
		return
	}
	redirectURL := sp.GetSSOBindingLocation(saml.HTTPRedirectBinding)
	postURL := sp.GetSSOBindingLocation(saml.HTTPPostBinding)
	binding, target := saml.HTTPRedirectBinding, redirectURL
	if redirectURL == "" {
		binding, target = saml.HTTPPostBinding, postURL
	}
	if target == "" {
		s.ssoUnavailable(w, r, fmt.Errorf("identity provider metadata has no sign-on endpoint: %w", errSSOMisconfigured))
		return
	}
	authnRequest, err := sp.MakeAuthenticationRequest(target, binding, saml.HTTPPostBinding)
	if err != nil {
		s.ssoFailed(w, r, err, ssoErrorRejected)
		return
	}
	relayKey, err := auth.LoadRelayKey(r.Context(), s.Pool, s.Cipher)
	if err != nil {
		s.ssoFailed(w, r, err, ssoErrorRejected)
		return
	}
	nonce, err := randomHandle()
	if err != nil {
		s.ssoFailed(w, r, err, ssoErrorRejected)
		return
	}
	nextPath := r.URL.Query().Get("next")
	if !safeNextPath(nextPath) {
		nextPath = ""
	}
	handle := nonce // what the identity provider echoes as RelayState
	cookieValue := sealRelayState(relayKey, nonce, authnRequest.ID, nextPath, s.Now().Add(samlRelayTTL))
	//nolint:gosec // SameSite=None is deliberate: the identity provider posts cross-site, see samlRelayCookie
	http.SetCookie(w, &http.Cookie{
		Name: samlRelayCookie, Value: cookieValue, Path: samlACSPath,
		MaxAge: int(samlRelayTTL / time.Second), Secure: true, HttpOnly: true, SameSite: http.SameSiteNoneMode,
	})
	w.Header().Set("Cache-Control", "no-store")
	if binding == saml.HTTPRedirectBinding {
		location, err := authnRequest.Redirect(handle, sp.ServiceProvider)
		if err != nil {
			s.ssoFailed(w, r, err, ssoErrorRejected)
			return
		}
		http.Redirect(w, r, location.String(), http.StatusFound)
		return
	}
	scriptNonce, err := randomHandle()
	if err != nil {
		s.ssoFailed(w, r, err, ssoErrorRejected)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'nonce-"+scriptNonce+"'; form-action "+target)
	_, _ = w.Write(authnRequest.PostWithNonce(handle, scriptNonce))
}

// valkeyReplayCache remembers assertion ids so that each is accepted once. It
// is made per request: a Valkey failure is kept in unavailable so the caller
// can answer 503 instead of 400, and the library refuses the assertion either
// way, so nothing is accepted without the check.
type valkeyReplayCache struct {
	ctx         context.Context
	s           *server
	unavailable error
}

func (c *valkeyReplayCache) Consume(assertionID string, expiresAt time.Time) error {
	sum := sha256.Sum256([]byte(assertionID))
	// Keep the id a little past its own expiry so a response at the very edge
	// of the allowed skew is still remembered.
	ttl := max(time.Until(expiresAt), 0) + time.Minute
	b := c.s.Valkey.B()
	err := c.s.Valkey.Do(c.ctx, b.Set().Key(c.s.KeyPrefix+"saml-assertion:"+hex.EncodeToString(sum[:])).Value("1").Nx().Px(ttl).Build()).Error()
	switch {
	case err == nil:
		return nil
	case isNil(err): // SET NX found the key: the assertion was used before
		return saml.ErrAssertionReplayed
	default:
		c.unavailable = err
		return err
	}
}

// rejectReason names why the library refused a response, in a short code that
// is safe to audit (never the response or any of its content).
func rejectReason(err error) string {
	var badStatus saml.ErrBadStatus
	var invalid *saml.InvalidResponseError
	switch {
	// The library has no exported error for a response without any signature;
	// TestRejects/unsigned fails if its wording ever changes.
	case errors.As(err, &invalid) && invalid.PrivateErr != nil && invalid.PrivateErr.Error() == "signature element not present":
		return "bad_signature"
	case errors.Is(err, saml.ErrAssertionReplayed):
		return "replay"
	case errors.Is(err, saml.ErrInvalidSignature), errors.Is(err, saml.ErrUntrustedCertificate), errors.Is(err, saml.ErrCertificateNotValidNow):
		return "bad_signature"
	case errors.Is(err, saml.ErrExpired):
		return "expired"
	case errors.Is(err, saml.ErrNotYetValid):
		return "not_yet_valid"
	case errors.Is(err, saml.ErrAudienceMismatch):
		return "wrong_audience"
	case errors.Is(err, saml.ErrDestinationMismatch):
		return "wrong_destination"
	case errors.Is(err, saml.ErrRequestIDMismatch):
		return "request_mismatch"
	case errors.Is(err, saml.ErrIssuerMismatch):
		return "wrong_issuer"
	case errors.As(err, &badStatus):
		return "idp_refused"
	default:
		return "invalid_response"
	}
}

// samlReject answers with a redirect to sign-in (code "rejected"), audits saml.rejected with a reason code (and detail
// that is neither the response nor a key), and creates nothing.
func (s *server) samlReject(w http.ResponseWriter, r *http.Request, reason string, extra map[string]any) {
	detail := map[string]any{"reason": reason}
	for k, v := range extra {
		detail[k] = v
	}
	if err := audit.Record(context.WithoutCancel(r.Context()), s.Pool, nil, s.Now(), audit.Event{
		Action: "saml.rejected", TargetType: "user", Detail: detail}); err != nil {
		s.Log.Error("audit rejected saml response", "error", err.Error())
	}
	s.Log.Warn("saml response rejected", "reason", reason)
	redirectSSOError(w, r, ssoErrorRejected)
}

// responseDestination returns the Destination attribute of the response's root
// element, "" when it has none.
func responseDestination(encoded string) (string, bool) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", false
	}
	// Reads one start tag; encoding/xml does not expand external entities, and
	// the whole document is validated later by the SAML library.
	decoder := xml.NewDecoder(bytes.NewReader(raw)) //nolint:gosec // see above
	for {
		token, err := decoder.Token()
		if err != nil {
			return "", false
		}
		if start, ok := token.(xml.StartElement); ok {
			for _, attr := range start.Attr {
				if attr.Name.Space == "" && attr.Name.Local == "Destination" {
					return attr.Value, true
				}
			}
			return "", true
		}
	}
}

// samlACS is the assertion consumer: the identity provider's browser post.
// Every check that can refuse the response runs before anything is written.
func (s *server) samlACS(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxACSBodyBytes)
	sp, err := s.newProvider(r.Context(), true, true)
	if s.ssoUnavailable(w, r, err) {
		return
	}
	if err != nil {
		s.ssoFailed(w, r, err, ssoErrorRejected)
		return
	}
	// Whatever happens next, this relay cookie has been used.
	clearRelayCookie(w)
	if err := r.ParseForm(); err != nil {
		s.samlReject(w, r, "malformed", nil)
		return
	}
	// Only the HTTP-POST binding is advertised. The artifact and SimpleSign
	// bindings would make the library call out to the identity provider or
	// take another signature path.
	if r.PostForm.Get("SAMLart") != "" || r.PostForm.Has("Signature") || r.PostForm.Has("SigAlg") || r.Form.Get("SAMLart") != "" {
		s.samlReject(w, r, "unsupported_binding", nil)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), valkeyTimeout)
	defer cancel()

	// The response must answer a request this browser started: the cookie is
	// ours (MAC, unexpired) and its nonce is the RelayState the identity
	// provider echoes; its request id is the only InResponseTo we accept.
	relayKey, err := auth.LoadRelayKey(r.Context(), s.Pool, s.Cipher)
	if err != nil {
		s.ssoFailed(w, r, err, ssoErrorRejected)
		return
	}
	cookie, err := r.Cookie(samlRelayCookie)
	relayState := r.PostForm.Get("RelayState")
	var requestID, next string
	ok := false
	if err == nil && relayState != "" {
		var nonce string
		nonce, requestID, next, ok = openRelayState(relayKey, cookie.Value, s.Now())
		ok = ok && subtle.ConstantTimeCompare([]byte(nonce), []byte(relayState)) == 1
	}
	if !ok {
		s.samlReject(w, r, "relay_state", nil)
		return
	}

	// Responses are always for our ACS address, signed or not (the library
	// allows an unsigned response to omit it).
	if destination, ok := responseDestination(r.PostForm.Get("SAMLResponse")); !ok || destination != sp.AcsURL.String() {
		reason := "wrong_destination"
		if !ok {
			reason = "malformed"
		}
		s.samlReject(w, r, reason, nil)
		return
	}

	replay := &valkeyReplayCache{ctx: ctx, s: s}
	sp.ReplayCache = replay
	assertion, err := sp.ParseResponse(r, []string{requestID})
	if replay.unavailable != nil {
		s.ssoFailed(w, r, fmt.Errorf("check saml assertion replay"+": %w", replay.unavailable), ssoErrorUnavailable)
		return
	}
	if err != nil {
		s.samlReject(w, r, rejectReason(err), nil)
		return
	}

	// One sign-in request is answered once: a second, different valid response
	// to the same request is refused. Done after full validation, before any write.
	sum := sha256.Sum256([]byte(requestID))
	requestKey := s.KeyPrefix + "saml-request:" + hex.EncodeToString(sum[:])
	switch err := s.Valkey.Do(ctx, s.Valkey.B().Set().Key(requestKey).Value("1").Nx().Px(samlRelayTTL+time.Minute).Build()).Error(); {
	case isNil(err):
		s.samlReject(w, r, "request_reused", nil)
		return
	case err != nil:
		s.ssoFailed(w, r, fmt.Errorf("record saml request id"+": %w", err), ssoErrorUnavailable)
		return
	}

	email, name, groups := identityFromAssertion(assertion, sp.settings)
	if email == "" || len(email) > maxEmailLength || !strings.Contains(email, "@") {
		s.samlReject(w, r, "bad_email", nil)
		return
	}
	user, err := s.provisionUser(r.Context(), email, name, groups)
	switch {
	case errors.Is(err, store.ErrDuplicate):
		s.samlRefuse(w, r, "email_taken", email)
		return
	case errors.Is(err, auth.ErrDisabled):
		s.samlRefuse(w, r, "user_disabled", email)
		return
	case err != nil:
		s.ssoFailed(w, r, err, ssoErrorRejected)
		return
	}

	if _, err := s.startSession(w, r, user.ID, map[string]any{"via": "saml"}); err != nil {
		switch {
		case errors.Is(err, session.ErrUnavailable):
			s.ssoFailed(w, r, fmt.Errorf("start session"+": %w", err), ssoErrorUnavailable)
		case errors.Is(err, auth.ErrDisabled):
			s.samlRefuse(w, r, "user_disabled", email)
		default:
			s.ssoFailed(w, r, err, ssoErrorRejected)
		}
		return
	}
	// The sign-in is done: the relay cookie has no further use.
	destination := "/"
	if safeNextPath(next) { // the cookie is authenticated; checked again anyway
		destination = next
	}
	http.Redirect(w, r, destination, http.StatusSeeOther) //nolint:gosec // destination is "/" or a path that passed safeNextPath
}

// samlRefuse is samlReject for a valid response of a person who may not sign in
// here: the account is known and the browser is sent to sign-in with code "refused".
func (s *server) samlRefuse(w http.ResponseWriter, r *http.Request, reason, email string) {
	if err := audit.Record(context.WithoutCancel(r.Context()), s.Pool, nil, s.Now(), audit.Event{
		Action: "saml.rejected", TargetType: "user", Detail: map[string]any{"reason": reason, "email": email}}); err != nil {
		s.Log.Error("audit rejected saml response", "error", err.Error())
	}
	s.Log.Warn("saml sign-in refused", "reason", reason)
	redirectSSOError(w, r, ssoErrorRefused)
}

// provisionUser creates or updates the user and replaces their identity-provider
// memberships in one transaction together with the audit entries for what
// changed (user.provision on first sign-in, sso.memberships when the set of
// groups changed, with the group ids added and removed). A disabled user, or
// an email owned by a local or service account, is refused before any write.
func (s *server) provisionUser(ctx context.Context, email, name string, idpGroups []string) (store.User, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return store.User{}, fmt.Errorf("provision saml user: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	existing, err := store.UserByEmail(ctx, tx, email)
	switch {
	case err == nil:
		if existing.Source != "saml" {
			return store.User{}, fmt.Errorf("saml user: %w", store.ErrDuplicate)
		}
		if existing.Disabled {
			return store.User{}, auth.ErrDisabled
		}
	case errors.Is(err, store.ErrNotFound):
	default:
		return store.User{}, err
	}
	user, created, err := store.UpsertSAMLUserCreated(ctx, tx, email, name, s.Now())
	if err != nil {
		return store.User{}, err
	}
	target := strconv.FormatInt(user.ID, 10)
	if created {
		if err := audit.Record(ctx, tx, nil, s.Now(), audit.Event{
			Action: "user.provision", TargetType: "user", TargetID: target,
			Detail: map[string]any{"email": email, "source": "saml"}}); err != nil {
			return store.User{}, err
		}
	}

	mappings, err := store.ListIDPMappings(ctx, tx)
	if err != nil {
		return store.User{}, err
	}
	wanted := map[int64]bool{}
	for _, group := range idpGroups {
		if id, ok := mappings[group]; ok {
			wanted[id] = true
		}
	}
	current, err := store.IDPMembershipGroupIDs(ctx, tx, user.ID)
	if err != nil {
		return store.User{}, err
	}
	added, removed := []int64{}, []int64{}
	have := map[int64]bool{}
	for _, id := range current {
		have[id] = true
		if !wanted[id] {
			removed = append(removed, id)
		}
	}
	for id := range wanted {
		if !have[id] {
			added = append(added, id)
		}
	}
	slices.Sort(added)
	if len(added)+len(removed) > 0 {
		ids := make([]int64, 0, len(wanted))
		for id := range wanted {
			ids = append(ids, id)
		}
		if err := store.ReplaceIDPMemberships(ctx, tx, user.ID, ids); err != nil {
			return store.User{}, err
		}
		if err := audit.Record(ctx, tx, nil, s.Now(), audit.Event{
			Action: "sso.memberships", TargetType: "user", TargetID: target,
			Detail: map[string]any{"email": email, "added": added, "removed": removed}}); err != nil {
			return store.User{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return store.User{}, fmt.Errorf("provision saml user: commit: %w", err)
	}
	return user, nil
}

// attributeValues returns the non-empty values of the attribute called name
// (matched on its Name or its FriendlyName).
func attributeValues(assertion *saml.Assertion, name string) []string {
	var out []string
	for _, statement := range assertion.AttributeStatements {
		for _, attribute := range statement.Attributes {
			if attribute.Name != name && attribute.FriendlyName != name {
				continue
			}
			for _, value := range attribute.Values {
				if v := strings.TrimSpace(value.Value); v != "" {
					out = append(out, v)
				}
			}
		}
	}
	return out
}

// identityFromAssertion reads the email address, display name and group names.
func identityFromAssertion(assertion *saml.Assertion, settings auth.SAMLSettings) (email, name string, groups []string) {
	if settings.EmailAttribute != "" {
		if values := attributeValues(assertion, settings.EmailAttribute); len(values) > 0 {
			email = values[0]
		}
	} else if assertion.Subject != nil && assertion.Subject.NameID != nil {
		email = assertion.Subject.NameID.Value
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if values := attributeValues(assertion, settings.NameAttribute); len(values) > 0 {
		name = values[0]
	}
	if name == "" {
		name = email
	}
	return email, truncate(name, maxNameLength), attributeValues(assertion, settings.GroupsAttribute)
}

// ssoSettingsView is the settings as the administrator sees them.
type ssoSettingsView struct {
	Enabled        bool   `json:"enabled"`
	IDPMetadataXML string `json:"idp_metadata_xml"`
	EntityID       string `json:"entity_id"`
	// EntityIDDefault is the entity id used while entity_id is stored empty;
	// it follows the public URL.
	EntityIDDefault string `json:"entity_id_default"`
	GroupsAttribute string `json:"groups_attribute"`
	EmailAttribute  string `json:"email_attribute"`
	NameAttribute   string `json:"name_attribute"`
	MetadataURL     string `json:"metadata_url"`
	ACSURL          string `json:"acs_url"`
}

func (s *server) ssoView(settings auth.SAMLSettings) ssoSettingsView {
	base := strings.TrimRight(s.PublicURL, "/")
	return ssoSettingsView{
		Enabled: settings.Enabled, IDPMetadataXML: settings.IDPMetadataXML, EntityID: s.effectiveEntityID(settings),
		EntityIDDefault: s.effectiveEntityID(auth.SAMLSettings{}),
		GroupsAttribute: settings.GroupsAttribute, EmailAttribute: settings.EmailAttribute, NameAttribute: settings.NameAttribute,
		MetadataURL: base + "/saml/metadata", ACSURL: base + samlACSPath,
	}
}

func (s *server) getSSO(w http.ResponseWriter, r *http.Request) {
	settings, err := auth.LoadSAMLSettings(r.Context(), s.Pool)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.ssoView(settings))
}

type ssoRequest struct {
	Enabled bool `json:"enabled"`
	// IDPMetadataXML replaces the stored identity provider metadata; empty
	// keeps what is stored.
	IDPMetadataXML  string `json:"idp_metadata_xml"`
	EntityID        string `json:"entity_id"`
	GroupsAttribute string `json:"groups_attribute"`
	EmailAttribute  string `json:"email_attribute"`
	NameAttribute   string `json:"name_attribute"`
	// Read-only fields of the settings view; accepted and ignored so the view
	// can be sent back as it was read.
	MetadataURL     string `json:"metadata_url"`
	ACSURL          string `json:"acs_url"`
	EntityIDDefault string `json:"entity_id_default"`
}

// checkIDPMetadata parses the metadata and, when strict, also requires what
// signing in needs: a signing certificate and a redirect or POST sign-on
// endpoint. It returns a message for the administrator, never a parser dump of
// the document.
func checkIDPMetadata(xmlText string, strict bool) (*saml.EntityDescriptor, string) {
	if strings.TrimSpace(xmlText) == "" {
		return nil, "identity provider metadata is required"
	}
	idp, err := samlsp.ParseMetadata([]byte(xmlText))
	if err != nil || len(idp.IDPSSODescriptors) == 0 {
		return nil, "the identity provider metadata is not valid SAML metadata with an IDPSSODescriptor"
	}
	if !strict {
		return idp, ""
	}
	if !hasSigningCertificate(idp) {
		return nil, "the identity provider metadata has no signing certificate"
	}
	probe := saml.ServiceProvider{IDPMetadata: idp}
	if probe.GetSSOBindingLocation(saml.HTTPRedirectBinding) == "" && probe.GetSSOBindingLocation(saml.HTTPPostBinding) == "" {
		return nil, "the identity provider metadata has no HTTP-Redirect or HTTP-POST sign-on endpoint"
	}
	return idp, ""
}

func hasSigningCertificate(idp *saml.EntityDescriptor) bool {
	for _, descriptor := range idp.IDPSSODescriptors {
		for _, key := range descriptor.KeyDescriptors {
			if key.Use == "encryption" {
				continue
			}
			for _, c := range key.KeyInfo.X509Data.X509Certificates {
				raw, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(c.Data), ""))
				if err != nil {
					continue
				}
				if _, err := x509.ParseCertificate(raw); err == nil {
					return true
				}
			}
		}
	}
	return false
}

func (s *server) putSSO(w http.ResponseWriter, r *http.Request) {
	var req ssoRequest
	if !decodeBody(w, r, &req) {
		return
	}
	current, err := auth.LoadSAMLSettings(r.Context(), s.Pool)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	next := auth.SAMLSettings{
		Enabled: req.Enabled, IDPMetadataXML: current.IDPMetadataXML, EntityID: strings.TrimSpace(req.EntityID),
		GroupsAttribute: strings.TrimSpace(req.GroupsAttribute), EmailAttribute: strings.TrimSpace(req.EmailAttribute),
		NameAttribute: strings.TrimSpace(req.NameAttribute),
	}
	if strings.TrimSpace(req.IDPMetadataXML) != "" {
		next.IDPMetadataXML = req.IDPMetadataXML
	}
	// Anything stored must parse; switching sign-in on needs the whole of it.
	if next.IDPMetadataXML != "" || next.Enabled {
		if _, problem := checkIDPMetadata(next.IDPMetadataXML, next.Enabled); problem != "" {
			invalid(w, problem)
			return
		}
	}
	if next.GroupsAttribute == "" {
		next.GroupsAttribute = auth.DefaultGroupsAttribute
	}
	if next.NameAttribute == "" {
		next.NameAttribute = auth.DefaultNameAttribute
	}
	err = s.auditedWrite(r, func(ctx context.Context, tx pgx.Tx) (audit.Event, error) {
		if err := auth.SaveSAMLSettings(ctx, tx, next); err != nil {
			return audit.Event{}, err
		}
		return audit.Event{Action: "sso.update", TargetType: "sso", TargetID: "saml", Detail: map[string]any{
			"enabled": next.Enabled, "entity_id": s.effectiveEntityID(next), "groups_attribute": next.GroupsAttribute,
			"email_attribute": next.EmailAttribute, "name_attribute": next.NameAttribute,
		}}, nil
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.ssoView(next))
}

// testSSO checks the stored identity provider metadata without signing anyone in.
func (s *server) testSSO(w http.ResponseWriter, r *http.Request) {
	settings, err := auth.LoadSAMLSettings(r.Context(), s.Pool)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	idp, problem := checkIDPMetadata(settings.IDPMetadataXML, true)
	if problem != "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": problem})
		return
	}
	probe := saml.ServiceProvider{IDPMetadata: idp}
	location := probe.GetSSOBindingLocation(saml.HTTPRedirectBinding)
	if location == "" {
		location = probe.GetSSOBindingLocation(saml.HTTPPostBinding)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "identity provider " + idp.EntityID + " signs in at " + location})
}

// safeNextPath reports whether p is a same-site absolute path that is safe to
// redirect to after sign-in: it starts with a single "/", has no backslash and
// no control characters, and still satisfies that after URL-decoding (so
// "/%2F%2Fevil" is refused).
func safeNextPath(p string) bool {
	plain := func(v string) bool {
		if v == "" || v[0] != '/' || strings.HasPrefix(v, "//") || strings.ContainsRune(v, '\\') {
			return false
		}
		for _, r := range v {
			if r < 0x20 || r == 0x7f {
				return false
			}
		}
		return true
	}
	if !plain(p) {
		return false
	}
	decoded, err := url.PathUnescape(p)
	return err == nil && plain(decoded)
}

// authMethods tells the sign-in page which ways of signing in exist. Open to
// everyone: it reveals only whether SSO is on.
func (s *server) authMethods(w http.ResponseWriter, r *http.Request) {
	settings, err := auth.LoadSAMLSettings(r.Context(), s.Pool)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"password": true, "saml": settings.Enabled && settings.IDPMetadataXML != ""})
}

// clearRelayCookie expires the relay cookie with the attributes it was set with.
func clearRelayCookie(w http.ResponseWriter) {
	//nolint:gosec // same attributes as the cookie it clears; SameSite=None is deliberate (see samlRelayCookie)
	http.SetCookie(w, &http.Cookie{
		Name: samlRelayCookie, Path: samlACSPath, MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteNoneMode,
	})
}

package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

const (
	jwtLifetime = 9 * time.Minute
	// renewMargin is how long before expiry an installation token is replaced.
	renewMargin = time.Minute
)

// parsePrivateKey accepts PKCS#1 and PKCS#8 PEM-encoded RSA keys.
// Errors never include key material.
func parsePrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("private key is not valid PEM")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("private key is neither PKCS#1 nor PKCS#8")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("private key is not an RSA key")
	}
	return key, nil
}

// signAppJWT builds an RS256 JWT issued by the GitHub App.
func signAppJWT(key *rsa.PrivateKey, appID int64, now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	iat := now.Add(-30 * time.Second) // tolerate clock skew
	claims, _ := json.Marshal(map[string]any{
		"iss": strconv.FormatInt(appID, 10),
		"iat": iat.Unix(),
		"exp": iat.Add(jwtLifetime).Unix(),
	})
	signingInput := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", errors.New("sign app JWT failed")
	}
	return signingInput + "." + enc.EncodeToString(sig), nil
}

// authorization returns the Authorization header value for API calls.
func (c *Client) authorization(ctx context.Context) (string, error) {
	if c.creds.Method == "token" {
		return "Bearer " + c.creds.Token, nil
	}
	c.appTokenMu.Lock()
	defer c.appTokenMu.Unlock()
	if c.appToken != "" && c.now().Add(renewMargin).Before(c.appTokenExpiry) {
		return "Bearer " + c.appToken, nil
	}
	token, expiry, err := c.exchangeInstallationToken(ctx)
	if err != nil {
		return "", err
	}
	c.appToken, c.appTokenExpiry = token, expiry
	return "Bearer " + token, nil
}

func (c *Client) exchangeInstallationToken(ctx context.Context) (string, time.Time, error) {
	jwt, err := signAppJWT(c.key, c.creds.AppID, c.now())
	if err != nil {
		return "", time.Time{}, err
	}
	path := fmt.Sprintf("/app/installations/%d/access_tokens", c.creds.InstallationID)
	body, _, err := c.send(ctx, http.MethodPost, path, "Bearer "+jwt, "")
	if err != nil {
		return "", time.Time{}, err
	}
	var resp struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || resp.Token == "" {
		return "", time.Time{}, errors.New("github: POST " + path + ": invalid installation token response")
	}
	return resp.Token, resp.ExpiresAt, nil
}

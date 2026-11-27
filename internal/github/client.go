// Package github is a small read-only GitHub REST client supporting personal
// access tokens and GitHub App installation tokens.
package github

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrNotFound is returned when GitHub answers 404.
var ErrNotFound = errors.New("github: not found")

// RateLimitError is returned when GitHub throttles the client.
type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("github: rate limited, retry after %s", e.RetryAfter.Round(time.Second))
}

// Credentials selects and carries the authentication method.
type Credentials struct {
	Method         string // "token" | "app"
	Token          string
	AppID          int64
	InstallationID int64
	PrivateKeyPEM  []byte
}

type etagEntry struct {
	path string // exact request path this entry answers
	etag string
	body []byte
}

// Client is safe for concurrent use.
type Client struct {
	base  string
	hc    *http.Client
	creds Credentials
	key   *rsa.PrivateKey
	now   func() time.Time

	appTokenMu     sync.Mutex
	appToken       string
	appTokenExpiry time.Time

	etagMu sync.Mutex
	etags  map[string]etagEntry // one entry per family, so size is bounded
}

// New validates the credentials and base URL. A nil httpClient gets a default
// client with a 30 second timeout.
func New(baseURL string, httpClient *http.Client, creds Credentials) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("github: base URL must be an absolute http(s) URL")
	}
	c := &Client{
		base:  strings.TrimRight(baseURL, "/"),
		hc:    httpClient,
		creds: creds,
		now:   time.Now,
		etags: map[string]etagEntry{},
	}
	if c.hc == nil {
		c.hc = &http.Client{Timeout: 30 * time.Second}
	}
	switch creds.Method {
	case "token":
		if creds.Token == "" {
			return nil, errors.New("github: token method requires a token")
		}
	case "app":
		if creds.AppID == 0 || creds.InstallationID == 0 {
			return nil, errors.New("github: app method requires app id and installation id")
		}
		c.key, err = parsePrivateKey(creds.PrivateKeyPEM)
		if err != nil {
			return nil, fmt.Errorf("github: %w", err)
		}
	default:
		return nil, errors.New("github: method must be \"token\" or \"app\"")
	}
	return c, nil
}

// repoPath validates "owner/name" and returns "/repos/owner/name".
func repoPath(repo string) (string, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || !validRepoPart(owner) || !validRepoPart(name) {
		return "", fmt.Errorf("github: repo %q must be owner/name", repo)
	}
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name), nil
}

func validRepoPart(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\ ?#%")
}

// escapeSegments escapes each slash-separated segment of a ref or file path.
func escapeSegments(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// HeadCommit returns the sha at the tip of branch.
func (c *Client) HeadCommit(ctx context.Context, repo, branch string) (string, error) {
	rp, err := repoPath(repo)
	if err != nil {
		return "", err
	}
	body, err := c.get(ctx, rp+"/commits/"+escapeSegments(branch), rp+"/commits")
	if err != nil {
		return "", err
	}
	var resp struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || resp.SHA == "" {
		return "", errors.New("github: commit response could not be decoded")
	}
	return resp.SHA, nil
}

// BranchExists reports whether branch exists in repo.
func (c *Client) BranchExists(ctx context.Context, repo, branch string) (bool, error) {
	rp, err := repoPath(repo)
	if err != nil {
		return false, err
	}
	_, err = c.get(ctx, rp+"/branches/"+escapeSegments(branch), "")
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// ListFiles returns the paths of files (not directories) under dir at commit,
// recursively. An empty dir lists the whole repository.
func (c *Client) ListFiles(ctx context.Context, repo, commit, dir string) ([]string, error) {
	rp, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	body, err := c.get(ctx, rp+"/git/trees/"+url.PathEscape(commit)+"?recursive=1", rp+"/git/trees")
	if err != nil {
		return nil, err
	}
	var resp struct {
		Truncated bool `json:"truncated"`
		Tree      []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"tree"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, errors.New("github: tree response could not be decoded")
	}
	if resp.Truncated {
		return nil, errors.New("github: repository tree is too large and was truncated by GitHub")
	}
	prefix := strings.Trim(dir, "/")
	if prefix != "" {
		prefix += "/"
	}
	files := []string{}
	for _, e := range resp.Tree {
		if e.Type == "blob" && strings.HasPrefix(e.Path, prefix) {
			files = append(files, e.Path)
		}
	}
	return files, nil
}

// ReadFile returns the content of path at commit.
func (c *Client) ReadFile(ctx context.Context, repo, commit, path string) ([]byte, error) {
	rp, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	body, err := c.get(ctx, rp+"/contents/"+escapeSegments(strings.TrimLeft(path, "/"))+"?ref="+url.QueryEscape(commit), "")
	if err != nil {
		return nil, err
	}
	var resp struct {
		Type     string `json:"type"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, errors.New("github: contents response could not be decoded")
	}
	if resp.Type != "file" || resp.Encoding != "base64" {
		return nil, fmt.Errorf("github: %q is not a regular file", path)
	}
	// The API wraps base64 across lines.
	data, err := base64.StdEncoding.DecodeString(strings.NewReplacer("\n", "", "\r", "").Replace(resp.Content))
	if err != nil {
		return nil, errors.New("github: file content could not be decoded")
	}
	return data, nil
}

// get performs an authenticated GET. A non-empty etagFamily enables the ETag
// cache; the cache keeps only the latest entry per family (for example one
// tree per repository), which bounds memory as new commits arrive. A repeat
// request for the same path still gets conditional-request reuse.
func (c *Client) get(ctx context.Context, path, etagFamily string) ([]byte, error) {
	auth, err := c.authorization(ctx)
	if err != nil {
		return nil, err
	}
	body, _, err := c.send(ctx, http.MethodGet, path, auth, etagFamily)
	return body, err
}

// send performs one request. Errors carry the method, path and status only,
// never headers or response bodies, so credentials cannot leak.
// A non-empty etagFamily enables conditional requests against the cache.
func (c *Client) send(ctx context.Context, method, path, auth, etagFamily string) ([]byte, int, error) {
	label := "github: " + method + " " + strings.SplitN(path, "?", 2)[0]
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, nil)
	if err != nil {
		return nil, 0, errors.New(label + ": invalid request")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "yumdrake")
	req.Header.Set("Authorization", auth)

	var cached etagEntry
	var haveCached bool
	if etagFamily != "" {
		c.etagMu.Lock()
		cached, haveCached = c.etags[etagFamily]
		c.etagMu.Unlock()
		haveCached = haveCached && cached.path == path
		if haveCached {
			req.Header.Set("If-None-Match", cached.etag)
		}
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // drop the URL wrapper
		}
		return nil, 0, fmt.Errorf("%s: request failed: %w", label, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, resp.StatusCode, errors.New(label + ": reading response failed")
	}

	switch {
	case resp.StatusCode == http.StatusNotModified && haveCached:
		return cached.body, resp.StatusCode, nil
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		if etag := resp.Header.Get("ETag"); etagFamily != "" && resp.StatusCode == http.StatusOK && etag != "" {
			c.etagMu.Lock()
			c.etags[etagFamily] = etagEntry{path: path, etag: etag, body: body}
			c.etagMu.Unlock()
		}
		return body, resp.StatusCode, nil
	case resp.StatusCode == http.StatusNotFound:
		return nil, resp.StatusCode, fmt.Errorf("%s: %w", label, ErrNotFound)
	case isRateLimited(resp):
		return nil, resp.StatusCode, &RateLimitError{RetryAfter: c.retryAfter(resp)}
	default:
		return nil, resp.StatusCode, fmt.Errorf("%s: unexpected status %d", label, resp.StatusCode)
	}
}

func isRateLimited(resp *http.Response) bool {
	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		return true
	case http.StatusForbidden:
		return resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.Header.Get("Retry-After") != ""
	}
	return false
}

func (c *Client) retryAfter(resp *http.Response) time.Duration {
	if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
		return time.Duration(s) * time.Second
	}
	if reset, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		if d := time.Unix(reset, 0).Sub(c.now()); d > 0 {
			return d
		}
	}
	return 0
}

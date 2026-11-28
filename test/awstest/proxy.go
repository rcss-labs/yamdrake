package awstest

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Request is one call seen by a Proxy.
type Request struct {
	// Service is the SigV4 signing name: sts, ecs, ecr, s3, kms, iam, ec2.
	Service string
	// Operation is the API operation, from X-Amz-Target, the Query Action, or
	// for S3 derived from the method and path (GetObject, PutObject, ...).
	Operation string
	// Account is the access key id of the caller (the Floci account).
	Account string
	Body    []byte
}

type rule struct {
	service, operation string
	status             int
	code               string
	rewrite            func([]byte) []byte
	used               bool
}

func (r *rule) matches(service, operation string) bool {
	return !r.used && r.service == service && r.operation == operation
}

// Proxy is a reverse proxy in front of Floci that records every call and can
// inject failures, edit responses and cut connections. SDK clients reach it
// through AWSConfigAt(region, proxy.URL).
type Proxy struct {
	// URL is the proxy's address.
	URL string

	upstream string
	client   *http.Client
	server   *httptest.Server

	mu       sync.Mutex
	requests []Request
	fails    []*rule
	drops    []*rule
	rewrites []*rule
}

// Proxy starts a proxy in front of the world's Floci; it stops with the test.
func (w *World) Proxy(t *testing.T) *Proxy {
	t.Helper()
	p := &Proxy{
		upstream: w.Endpoint,
		client: &http.Client{
			// Bodies pass through untouched, and a redirect is the caller's business.
			Transport:     &http.Transport{DisableCompression: true},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
	p.server = httptest.NewServer(http.HandlerFunc(p.handle))
	p.URL = p.server.URL
	t.Cleanup(func() {
		p.server.CloseClientConnections()
		p.server.Close()
	})
	return p
}

// Requests returns a copy of every call seen so far, in arrival order.
func (p *Proxy) Requests() []Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Request, len(p.requests))
	copy(out, p.requests)
	return out
}

// FailNext makes the next matching call return the given AWS error and status
// instead of reaching Floci. Only one call is failed per FailNext. Note the SDK
// retries throttling and 5xx errors by default.
func (p *Proxy) FailNext(service, operation, awsErrorCode string, status int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fails = append(p.fails, &rule{service: service, operation: operation, code: awsErrorCode, status: status})
}

// Rewrite edits the response body of every matching call from now on.
func (p *Proxy) Rewrite(service, operation string, fn func(body []byte) []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rewrites = append(p.rewrites, &rule{service: service, operation: operation, rewrite: fn})
}

// DropAfterForward lets the next matching call reach Floci, then cuts the
// connection before the response is sent: the effect happened, the caller
// sees a network error.
func (p *Proxy) DropAfterForward(service, operation string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.drops = append(p.drops, &rule{service: service, operation: operation})
}

// take marks and returns the first unused rule in list that matches.
func (p *Proxy) take(list []*rule, service, operation string) *rule {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, r := range list {
		if r.matches(service, operation) {
			r.used = true
			return r
		}
	}
	return nil
}

func (p *Proxy) handle(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read request: "+err.Error(), http.StatusBadGateway)
		return
	}
	service, operation, account := classify(r, body)
	p.mu.Lock()
	p.requests = append(p.requests, Request{Service: service, Operation: operation, Account: account, Body: body})
	p.mu.Unlock()

	if f := p.take(p.fails, service, operation); f != nil {
		writeAWSError(w, service, f.code, f.status)
		return
	}

	status, header, respBody, err := p.forward(r, body)
	if err != nil {
		http.Error(w, "forward: "+err.Error(), http.StatusBadGateway)
		return
	}

	if d := p.take(p.drops, service, operation); d != nil {
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
				return
			}
		}
		panic(http.ErrAbortHandler) // cuts the connection without a response
	}

	p.mu.Lock()
	var rewrites []func([]byte) []byte
	for _, rw := range p.rewrites {
		if rw.service == service && rw.operation == operation {
			rewrites = append(rewrites, rw.rewrite)
		}
	}
	p.mu.Unlock()
	if len(rewrites) > 0 {
		if header.Get("Content-Encoding") == "gzip" {
			if zr, err := gzip.NewReader(bytes.NewReader(respBody)); err == nil {
				if plain, err := io.ReadAll(zr); err == nil {
					respBody = plain
					header.Del("Content-Encoding")
				}
			}
		}
		for _, fn := range rewrites {
			respBody = fn(respBody)
		}
	}

	for k, vs := range header {
		if strings.EqualFold(k, "Content-Length") || strings.EqualFold(k, "Transfer-Encoding") {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(respBody)))
	w.WriteHeader(status)
	_, _ = w.Write(respBody) //nolint:gosec // test proxy relays Floci responses to the test client
}

// forward sends the request to Floci with Floci's own Host, so that URIs it
// puts in responses (for example repository URIs) point at Floci, not the proxy.
func (p *Proxy) forward(r *http.Request, body []byte) (int, http.Header, []byte, error) {
	target, err := url.Parse(p.upstream)
	if err != nil {
		return 0, nil, nil, err
	}
	out, err := http.NewRequestWithContext(r.Context(), r.Method, p.upstream+r.URL.RequestURI(), bytes.NewReader(body)) //nolint:gosec // test proxy: the upstream is the test emulator
	if err != nil {
		return 0, nil, nil, err
	}
	for k, vs := range r.Header {
		switch http.CanonicalHeaderKey(k) {
		case "Connection", "Keep-Alive", "Transfer-Encoding", "Upgrade", "Te", "Trailer", "Proxy-Connection":
			continue
		}
		out.Header[k] = append([]string(nil), vs...)
	}
	out.Host = target.Host
	resp, err := p.client.Do(out) //nolint:gosec // test proxy: the upstream is the test emulator
	if err != nil {
		return 0, nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, nil, err
	}
	return resp.StatusCode, resp.Header.Clone(), respBody, nil
}

// classify names the service, operation and account of a signed AWS request.
func classify(r *http.Request, body []byte) (service, operation, account string) {
	// Authorization: AWS4-HMAC-SHA256 Credential=<key>/<date>/<region>/<service>/aws4_request, ...
	if _, rest, ok := strings.Cut(r.Header.Get("Authorization"), "Credential="); ok {
		cred, _, _ := strings.Cut(rest, ",")
		if parts := strings.Split(cred, "/"); len(parts) >= 5 {
			account, service = parts[0], parts[3]
		}
	}
	if target := r.Header.Get("X-Amz-Target"); target != "" {
		return service, target[strings.LastIndex(target, ".")+1:], account
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		if form, err := url.ParseQuery(string(body)); err == nil {
			return service, form.Get("Action"), account
		}
	}
	if service == "s3" {
		return service, s3Operation(r), account
	}
	return service, r.Method + " " + r.URL.Path, account
}

// s3Operation maps an S3 REST call to its operation name for the calls tests use.
func s3Operation(r *http.Request) string {
	q := r.URL.Query()
	hasKey := strings.Count(strings.Trim(r.URL.Path, "/"), "/") >= 1
	if !hasKey {
		_, versioning := q["versioning"]
		_, versions := q["versions"]
		switch {
		case r.Method == http.MethodPut && versioning:
			return "PutBucketVersioning"
		case r.Method == http.MethodGet && versioning:
			return "GetBucketVersioning"
		case r.Method == http.MethodGet && versions:
			return "ListObjectVersions"
		case r.Method == http.MethodPut:
			return "CreateBucket"
		case r.Method == http.MethodGet:
			return "ListObjectsV2"
		case r.Method == http.MethodHead:
			return "HeadBucket"
		case r.Method == http.MethodDelete:
			return "DeleteBucket"
		}
		return r.Method + " " + r.URL.Path
	}
	switch r.Method {
	case http.MethodGet:
		return "GetObject"
	case http.MethodHead:
		return "HeadObject"
	case http.MethodDelete:
		return "DeleteObject"
	case http.MethodPut:
		if r.Header.Get("X-Amz-Copy-Source") != "" {
			return "CopyObject"
		}
		return "PutObject"
	}
	return r.Method + " " + r.URL.Path
}

// writeAWSError answers in the error format of the service's protocol.
func writeAWSError(w http.ResponseWriter, service, code string, status int) {
	const message = "injected by awstest.Proxy"
	switch service {
	case "ecs", "ecr", "kms":
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		w.Header().Set("X-Amzn-Errortype", code)
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `{"__type":%q,"message":%q}`, code, message)
	case "s3":
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s</Message><RequestId>awstest</RequestId></Error>`, code, message)
	default: // Query protocol: sts, iam, ec2
		w.Header().Set("Content-Type", "text/xml")
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `<ErrorResponse><Error><Type>Sender</Type><Code>%s</Code><Message>%s</Message></Error><RequestId>awstest</RequestId></ErrorResponse>`, code, message)
	}
}

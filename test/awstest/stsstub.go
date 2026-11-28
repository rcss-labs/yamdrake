package awstest

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// STSStub is an http.RoundTripper that answers sts:AssumeRole itself, for the
// paths Floci cannot show (for example it does not enforce ExternalId). Put it
// in aws.Config.HTTPClient. Every other STS action is answered 400.
type STSStub struct {
	// Expires is when the issued credentials expire; zero means one hour from now.
	Expires time.Time
	// FailCode, when set, makes every AssumeRole fail with that AWS error code.
	FailCode string
	// RequestID, when set, is returned as the x-amzn-RequestId of every answer.
	RequestID string

	mu    sync.Mutex
	calls []url.Values
}

// Client returns an *http.Client that sends every request to the stub.
func (s *STSStub) Client() *http.Client { return &http.Client{Transport: s} }

// Calls returns the form parameters of every AssumeRole request so far.
func (s *STSStub) Calls() []url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]url.Values(nil), s.calls...)
}

func (s *STSStub) RoundTrip(r *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	form, err := url.ParseQuery(string(body))
	if err != nil || form.Get("Action") != "AssumeRole" {
		return s.reply(r, http.StatusBadRequest, errorXML("InvalidAction", "only AssumeRole is stubbed")), nil
	}
	s.mu.Lock()
	s.calls = append(s.calls, form)
	n := len(s.calls)
	s.mu.Unlock()
	if s.FailCode != "" {
		return s.reply(r, http.StatusForbidden, errorXML(s.FailCode, "stubbed failure")), nil
	}
	expires := s.Expires
	if expires.IsZero() {
		expires = time.Now().Add(time.Hour)
	}
	return s.reply(r, http.StatusOK, fmt.Sprintf(`<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleResult><Credentials>`+
		`<AccessKeyId>ASIASTUB%07d</AccessKeyId><SecretAccessKey>stub-secret-%d</SecretAccessKey><SessionToken>stub-token-%d</SessionToken>`+
		`<Expiration>%s</Expiration></Credentials><AssumedRoleUser><AssumedRoleId>AROASTUB:s</AssumedRoleId><Arn>arn:aws:sts::123456789012:assumed-role/stub/s</Arn></AssumedRoleUser>`+
		`</AssumeRoleResult><ResponseMetadata><RequestId>stub</RequestId></ResponseMetadata></AssumeRoleResponse>`,
		n, n, n, expires.UTC().Format(time.RFC3339))), nil
}

func errorXML(code, message string) string {
	return fmt.Sprintf(`<ErrorResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><Error><Type>Sender</Type><Code>%s</Code><Message>%s</Message></Error><RequestId>stub</RequestId></ErrorResponse>`, code, message)
}

func (s *STSStub) reply(r *http.Request, status int, body string) *http.Response {
	header := http.Header{"Content-Type": {"text/xml"}}
	if s.RequestID != "" {
		header.Set("X-Amzn-Requestid", s.RequestID)
	}
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: r}
}

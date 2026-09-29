// Package httpx is the one HTTP client every provider adapter uses. It gives
// all of them the same behaviour: bounded retries with exponential backoff and
// jitter, respect for Retry-After, client-side rate limiting, idempotency keys,
// error classification, and request logging with secrets redacted.
//
// API failures are never swallowed: every non-2xx response becomes an *Error
// carrying a classification the diagnosis engine can explain in plain English.
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Error kinds.
const (
	KindAuth       = "auth"       // credential missing, invalid, expired, revoked
	KindPermission = "permission" // credential valid but not allowed
	KindNotFound   = "not_found"
	KindConflict   = "conflict" // already exists / state conflict
	KindInvalid    = "invalid"  // request rejected as malformed
	KindRateLimit  = "rate_limit"
	KindServer     = "server"  // provider-side failure
	KindNetwork    = "network" // could not reach provider
	KindTimeout    = "timeout"
	KindCanceled   = "canceled"
)

// Error is a classified API failure.
type Error struct {
	Provider  string
	Method    string
	Path      string
	Status    int
	Kind      string
	Code      string // provider error code, e.g. "resource_missing", "10000"
	Message   string // provider message (redacted)
	RequestID string
	Body      string // truncated, redacted body for the Advanced view
	Retryable bool
	Attempts  int
	Cause     error
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(e.Provider)
	if e.Status > 0 {
		fmt.Fprintf(&b, " HTTP %d", e.Status)
	}
	if e.Method != "" {
		fmt.Fprintf(&b, " on %s %s", e.Method, e.Path)
	}
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	} else if e.Cause != nil {
		b.WriteString(": ")
		b.WriteString(e.Cause.Error())
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Cause }

// AsError extracts an *Error from any error.
func AsError(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// IsNotFound reports whether err is a 404-style error.
func IsNotFound(err error) bool {
	e, ok := AsError(err)
	return ok && e.Kind == KindNotFound
}

// IsConflict reports whether err is a conflict (usually "already exists").
func IsConflict(err error) bool {
	e, ok := AsError(err)
	return ok && e.Kind == KindConflict
}

// Request describes one API call.
type Request struct {
	Method string
	Path   string // relative to BaseURL, or absolute URL
	Query  url.Values
	JSON   any        // encoded as application/json
	Form   url.Values // encoded as application/x-www-form-urlencoded (Stripe)
	Body   []byte     // raw body (multipart etc.) with ContentType
	// ContentType for Body.
	ContentType string
	Header      http.Header
	// Idempotent marks the call as safe to repeat. GET/PUT/DELETE/HEAD are
	// always idempotent; POST/PATCH are retried only when this is set (for
	// example Stripe calls that carry an Idempotency-Key).
	Idempotent bool
	// IdempotencyKey is sent as the Idempotency-Key header when set.
	IdempotencyKey string
	// NoAuth skips the client's auth hook (e.g. calling a Worker probe).
	NoAuth bool
	// Quiet suppresses info logging (still logs errors).
	Quiet bool
	// Timeout overrides the client default for this call.
	Timeout time.Duration
	// Resource is attached to the log entry.
	Resource string
}

// Response is a completed call.
type Response struct {
	Status    int
	Header    http.Header
	Body      []byte
	RequestID string
	Latency   time.Duration
	Attempts  int
}

// Decode unmarshals the JSON body into v.
func (r *Response) Decode(v any) error {
	if len(r.Body) == 0 {
		return errors.New("empty response body")
	}
	return json.Unmarshal(r.Body, v)
}

// LogEvent is emitted for every call (after redaction by the caller's hook).
type LogEvent struct {
	Provider  string
	Method    string
	URL       string
	Status    int
	Latency   time.Duration
	Attempt   int
	RequestID string
	Err       error
	Resource  string
	Quiet     bool
	// Signals carries API-change headers the provider sent (Deprecation,
	// Sunset, Warning, API version selection) so Backplane can warn before a
	// provider removes something it relies on.
	Signals map[string]string
}

// signalHeaders are response headers that announce API changes.
var signalHeaders = []string{"Deprecation", "Sunset", "Warning", "X-Api-Warn", "X-GitHub-Api-Version-Selected", "X-Deprecated", "Stripe-Should-Retry"}

func signalsOf(h http.Header) map[string]string {
	var out map[string]string
	for _, k := range signalHeaders {
		if k == "Stripe-Should-Retry" {
			continue
		}
		if v := h.Get(k); v != "" {
			if out == nil {
				out = map[string]string{}
			}
			out[k] = truncate(v, 300)
		}
	}
	return out
}

// ErrorParser extracts a provider's error code and message from a body.
type ErrorParser func(status int, body []byte) (code, message string)

// Client is a configured provider client.
type Client struct {
	Provider    string
	BaseURL     string
	HTTP        *http.Client
	Auth        func(*http.Request)
	UserAgent   string
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	Timeout     time.Duration
	Limiter     *Limiter
	ParseError  ErrorParser
	// Classify lets a provider override the default status-based
	// classification (e.g. Cloudflare returns 400 for some auth failures).
	Classify func(status int, code, message string) string
	Log      func(LogEvent)
	// RequestIDHeaders are checked in order for a request id.
	RequestIDHeaders []string
	// Sleep is replaceable in tests.
	Sleep func(context.Context, time.Duration) error
}

// New returns a client with sensible defaults.
func New(provider, baseURL string) *Client {
	return &Client{
		Provider:    provider,
		BaseURL:     strings.TrimRight(baseURL, "/"),
		HTTP:        &http.Client{Timeout: 0},
		UserAgent:   "Backplane/1.0 (+https://www.safisolutions.org)",
		MaxAttempts: 5,
		BaseDelay:   500 * time.Millisecond,
		MaxDelay:    20 * time.Second,
		Timeout:     45 * time.Second,
		RequestIDHeaders: []string{"request-id", "x-request-id", "cf-ray", "x-github-request-id",
			"sb-request-id", "x-vercel-id", "x-nf-request-id", "x-amzn-requestid"},
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Do performs the request with retries.
func (c *Client) Do(ctx context.Context, rq Request) (*Response, error) {
	method := strings.ToUpper(rq.Method)
	if method == "" {
		method = http.MethodGet
	}
	idempotent := rq.Idempotent || rq.IdempotencyKey != "" ||
		method == http.MethodGet || method == http.MethodPut || method == http.MethodDelete || method == http.MethodHead
	attempts := c.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	sleep := c.Sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	target, err := c.url(rq)
	if err != nil {
		return nil, &Error{Provider: c.Provider, Method: method, Path: rq.Path, Kind: KindInvalid, Message: err.Error(), Cause: err}
	}
	var lastErr *Error
	for attempt := 1; attempt <= attempts; attempt++ {
		if c.Limiter != nil {
			if err := c.Limiter.Wait(ctx); err != nil {
				return nil, &Error{Provider: c.Provider, Method: method, Path: rq.Path, Kind: KindCanceled, Cause: err, Attempts: attempt}
			}
		}
		resp, e := c.once(ctx, method, target, rq, attempt)
		if e == nil {
			resp.Attempts = attempt
			return resp, nil
		}
		e.Attempts = attempt
		lastErr = e
		if !e.Retryable || !idempotent || attempt == attempts || ctx.Err() != nil {
			break
		}
		delay := c.backoff(attempt, resp)
		if err := sleep(ctx, delay); err != nil {
			lastErr = &Error{Provider: c.Provider, Method: method, Path: rq.Path, Kind: KindCanceled, Cause: err, Attempts: attempt}
			break
		}
	}
	return nil, lastErr
}

func (c *Client) url(rq Request) (string, error) {
	raw := rq.Path
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		if !strings.HasPrefix(raw, "/") {
			raw = "/" + raw
		}
		raw = c.BaseURL + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if len(rq.Query) > 0 {
		q := u.Query()
		for k, vs := range rq.Query {
			for _, v := range vs {
				q.Add(k, v)
			}
		}
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}

func (c *Client) once(ctx context.Context, method, target string, rq Request, attempt int) (*Response, *Error) {
	timeout := c.Timeout
	if rq.Timeout > 0 {
		timeout = rq.Timeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var body io.Reader
	contentType := ""
	switch {
	case rq.JSON != nil:
		bs, err := json.Marshal(rq.JSON)
		if err != nil {
			return nil, &Error{Provider: c.Provider, Method: method, Path: rq.Path, Kind: KindInvalid, Message: err.Error(), Cause: err}
		}
		body = bytes.NewReader(bs)
		contentType = "application/json"
	case rq.Form != nil:
		body = strings.NewReader(rq.Form.Encode())
		contentType = "application/x-www-form-urlencoded"
	case rq.Body != nil:
		body = bytes.NewReader(rq.Body)
		contentType = rq.ContentType
	}
	req, err := http.NewRequestWithContext(cctx, method, target, body)
	if err != nil {
		return nil, &Error{Provider: c.Provider, Method: method, Path: rq.Path, Kind: KindInvalid, Message: err.Error(), Cause: err}
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.UserAgent)
	for k, vs := range rq.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if rq.IdempotencyKey != "" {
		req.Header.Set("Idempotency-Key", rq.IdempotencyKey)
	}
	if c.Auth != nil && !rq.NoAuth {
		c.Auth(req)
	}
	start := time.Now()
	hr, err := c.HTTP.Do(req)
	latency := time.Since(start)
	logURL := stripQuerySecrets(target)
	if err != nil {
		kind := KindNetwork
		msg := "could not reach " + hostOf(target)
		retry := true
		switch {
		case errors.Is(err, context.Canceled) && ctx.Err() != nil:
			kind, msg, retry = KindCanceled, "canceled", false
		case errors.Is(err, context.DeadlineExceeded) || isTimeout(err):
			kind, msg = KindTimeout, fmt.Sprintf("no answer from %s within %s", hostOf(target), timeout)
		}
		e := &Error{Provider: c.Provider, Method: method, Path: pathOf(target), Kind: kind, Message: msg, Retryable: retry, Cause: err}
		c.emit(LogEvent{Provider: c.Provider, Method: method, URL: logURL, Latency: latency, Attempt: attempt, Err: e, Resource: rq.Resource, Quiet: rq.Quiet})
		return nil, e
	}
	defer hr.Body.Close()
	bs, err := io.ReadAll(io.LimitReader(hr.Body, 32<<20))
	if err != nil {
		e := &Error{Provider: c.Provider, Method: method, Path: pathOf(target), Kind: KindNetwork, Message: "connection dropped while reading the answer", Retryable: true, Cause: err}
		c.emit(LogEvent{Provider: c.Provider, Method: method, URL: logURL, Latency: latency, Attempt: attempt, Err: e, Resource: rq.Resource, Quiet: rq.Quiet})
		return nil, e
	}
	resp := &Response{Status: hr.StatusCode, Header: hr.Header, Body: bs, Latency: latency}
	for _, h := range c.RequestIDHeaders {
		if v := hr.Header.Get(h); v != "" {
			resp.RequestID = v
			break
		}
	}
	if hr.StatusCode >= 200 && hr.StatusCode < 300 {
		c.emit(LogEvent{Provider: c.Provider, Method: method, URL: logURL, Status: hr.StatusCode, Latency: latency, Attempt: attempt, RequestID: resp.RequestID, Resource: rq.Resource, Quiet: rq.Quiet, Signals: signalsOf(hr.Header)})
		return resp, nil
	}
	code, msg := "", ""
	if c.ParseError != nil {
		code, msg = c.ParseError(hr.StatusCode, bs)
	}
	if msg == "" {
		msg = genericMessage(hr.StatusCode, bs)
	}
	kind := classify(hr.StatusCode)
	if c.Classify != nil {
		if k := c.Classify(hr.StatusCode, code, msg); k != "" {
			kind = k
		}
	}
	e := &Error{Provider: c.Provider, Method: method, Path: pathOf(target), Status: hr.StatusCode, Kind: kind, Code: code,
		Message: msg, RequestID: resp.RequestID, Body: truncate(string(bs), 2000),
		Retryable: kind == KindRateLimit || kind == KindServer}
	c.emit(LogEvent{Provider: c.Provider, Method: method, URL: logURL, Status: hr.StatusCode, Latency: latency, Attempt: attempt, RequestID: resp.RequestID, Err: e, Resource: rq.Resource, Quiet: rq.Quiet, Signals: signalsOf(hr.Header)})
	return resp, e
}

func (c *Client) emit(ev LogEvent) {
	if c.Log != nil {
		c.Log(ev)
	}
}

// backoff computes the wait before the next attempt, honouring Retry-After
// and rate-limit reset headers when the provider sends them.
func (c *Client) backoff(attempt int, resp *Response) time.Duration {
	if resp != nil {
		if d, ok := retryAfter(resp.Header); ok {
			if d > c.MaxDelay*3 {
				d = c.MaxDelay * 3
			}
			return d
		}
	}
	base := c.BaseDelay
	if base <= 0 {
		base = 500 * time.Millisecond
	}
	d := base << (attempt - 1)
	if d > c.MaxDelay {
		d = c.MaxDelay
	}
	// full jitter between d/2 and d
	half := int64(d / 2)
	if half <= 0 {
		return d
	}
	return time.Duration(half + rand.Int63n(half+1))
}

func retryAfter(h http.Header) (time.Duration, bool) {
	if v := h.Get("Retry-After"); v != "" {
		if secs, err := strconv.ParseFloat(v, 64); err == nil && secs >= 0 {
			return time.Duration(secs * float64(time.Second)), true
		}
		if t, err := http.ParseTime(v); err == nil {
			d := time.Until(t)
			if d < 0 {
				d = 0
			}
			return d, true
		}
	}
	for _, k := range []string{"RateLimit-Reset", "X-RateLimit-Reset", "ratelimit-reset"} {
		if v := h.Get(k); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				if n > 1_000_000_000 { // unix timestamp
					d := time.Until(time.Unix(n, 0))
					if d < 0 {
						d = 0
					}
					return d, true
				}
				return time.Duration(n) * time.Second, true
			}
		}
	}
	return 0, false
}

func classify(status int) string {
	switch {
	case status == 401:
		return KindAuth
	case status == 403:
		return KindPermission
	case status == 404 || status == 410:
		return KindNotFound
	case status == 409 || status == 412:
		return KindConflict
	case status == 429:
		return KindRateLimit
	case status == 408:
		return KindTimeout
	case status >= 500:
		return KindServer
	default:
		return KindInvalid
	}
}

func genericMessage(status int, body []byte) string {
	var probe map[string]any
	if json.Unmarshal(body, &probe) == nil {
		for _, k := range []string{"message", "error_description", "error", "msg", "detail"} {
			switch v := probe[k].(type) {
			case string:
				if v != "" {
					return v
				}
			case map[string]any:
				if m, ok := v["message"].(string); ok && m != "" {
					return m
				}
			}
		}
	}
	if t := strings.TrimSpace(string(body)); t != "" && len(t) < 300 && !strings.HasPrefix(t, "<") {
		return t
	}
	return http.StatusText(status)
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return u.Host
}

func pathOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return u.Path
}

// stripQuerySecrets hides query values that commonly carry credentials.
func stripQuerySecrets(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	for k := range q {
		lk := strings.ToLower(k)
		if strings.Contains(lk, "key") || strings.Contains(lk, "token") || strings.Contains(lk, "secret") || strings.Contains(lk, "sig") || lk == "code" {
			q.Set(k, "••••")
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Limiter is a simple token bucket shared by all calls to one provider.
type Limiter struct {
	mu     sync.Mutex
	rate   float64 // tokens per second
	burst  float64
	tokens float64
	last   time.Time
}

// NewLimiter allows `perSecond` calls per second with the given burst.
func NewLimiter(perSecond float64, burst int) *Limiter {
	return &Limiter{rate: perSecond, burst: float64(burst), tokens: float64(burst), last: time.Now()}
}

// Wait blocks until a token is available.
func (l *Limiter) Wait(ctx context.Context) error {
	for {
		l.mu.Lock()
		now := time.Now()
		l.tokens += now.Sub(l.last).Seconds() * l.rate
		if l.tokens > l.burst {
			l.tokens = l.burst
		}
		l.last = now
		if l.tokens >= 1 {
			l.tokens--
			l.mu.Unlock()
			return nil
		}
		need := time.Duration((1 - l.tokens) / l.rate * float64(time.Second))
		l.mu.Unlock()
		if err := sleepCtx(ctx, need); err != nil {
			return err
		}
	}
}

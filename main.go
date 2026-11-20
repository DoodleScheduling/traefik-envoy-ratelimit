package traefik_envoy_ratelimit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Config is populated from Traefik dynamic configuration.
type Config struct {
	// ServiceURL is the envoyproxy/ratelimit HTTP json endpoint,
	// e.g. "http://ratelimit.ratelimit.svc.cluster.local:8080/json".
	ServiceURL string `json:"serviceUrl,omitempty"`

	// Domain must match a `domain:` defined in the ratelimit service
	Domain string `json:"domain,omitempty"`

	// Descriptors describe how to build rate-limit descriptors from each request.
	// Each descriptor becomes one tuple sent to the service; the service returns an
	// aggregate (logical OR) over-limit decision across all of them.
	Descriptors []DescriptorConfig `json:"descriptors,omitempty"`

	// TimeoutMs caps the call to the ratelimit service (default 50ms).
	TimeoutMs int `json:"timeoutMs,omitempty"`

	// FailOnError: when the service is unreachable/slow, deny (503) if true,
	// otherwise allow the request through (default false).
	FailOnError bool `json:"failOnError,omitempty"`

	// HitsAddend is how many tokens each request consumes (default 1).
	HitsAddend int `json:"hitsAddend,omitempty"`

	// MaxBodyBytes caps how much of the request body is buffered to resolve
	// "form:" entries (default 1MiB). Larger bodies leave form values unresolved.
	MaxBodyBytes int64 `json:"maxBodyBytes,omitempty"`
}

const defaultMaxBodyBytes int64 = 1 << 20

// DescriptorConfig is one descriptor: an ordered list of key/value entries.
type DescriptorConfig struct {
	Entries []EntryConfig `json:"entries,omitempty"`
}

// EntryConfig maps a descriptor key to a value sourced from the request.
type EntryConfig struct {
	// Key is the descriptor key as expected by the ratelimit config.yaml
	// (e.g. "remote_address", "PATH", "API_KEY").
	Key string `json:"key,omitempty"`

	// From selects the value source:
	//   "remote_address"      -> client IP
	//   "path"                -> request URL path
	//   "method"              -> HTTP method
	//   "host"                -> request host
	//   "header:<HeaderName>" -> value of the given request header
	//   "value:<literal>"     -> a fixed literal value (great for grouping routes)
	//   "form:<FieldName>"    -> first value of the given field from an
	//                            application/x-www-form-urlencoded or
	//                            multipart/form-data request body
	From string `json:"from,omitempty"`

	// TrimSpace removes leading/trailing whitespace from the resolved value.
	TrimSpace bool `json:"trimSpace,omitempty"`

	// Lowercase lowercases the resolved value.
	Lowercase bool `json:"lowercase,omitempty"`

	// Default is used when the value cannot be resolved (or is empty after
	// normalization). If unset, the whole descriptor is dropped instead.
	Default string `json:"default,omitempty"`
}

// CreateConfig returns the default configuration. Required by Traefik.
func CreateConfig() *Config {
	return &Config{
		TimeoutMs:    50,
		HitsAddend:   1,
		MaxBodyBytes: defaultMaxBodyBytes,
	}
}

// rateLimit is the middleware handler.
type rateLimit struct {
	next   http.Handler
	name   string
	cfg    *Config
	client httpClient
}

type httpClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// New builds the middleware. Required signature for Traefik plugins.
func New(_ context.Context, next http.Handler, cfg *Config, name string) (http.Handler, error) {
	if cfg.ServiceURL == "" {
		return nil, fmt.Errorf("envoyratelimit: serviceUrl is required")
	}
	if cfg.Domain == "" {
		return nil, fmt.Errorf("envoyratelimit: domain is required")
	}
	timeout := time.Duration(cfg.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 50 * time.Millisecond
	}
	if cfg.HitsAddend <= 0 {
		cfg.HitsAddend = 1
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = defaultMaxBodyBytes
	}

	return &rateLimit{
		next:   next,
		name:   name,
		cfg:    cfg,
		client: &http.Client{Timeout: timeout},
	}, nil
}

// ---- wire types for the /json endpoint ----
// Request field names (domain/descriptors/entries/key/value) are accepted as-is
// by the service's JSON decoder.
type rlsRequest struct {
	Domain      string          `json:"domain"`
	Descriptors []rlsDescriptor `json:"descriptors"`
	HitsAddend  int             `json:"hitsAddend,omitempty"`
}

type rlsDescriptor struct {
	Entries []rlsEntry `json:"entries"`
}

type rlsEntry struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type rlsResponse struct {
	OverallCode          string           `json:"overallCode"`
	ResponseHeadersToAdd []rlsHeaderValue `json:"responseHeadersToAdd"`
	RequestHeadersToAdd  []rlsHeaderValue `json:"requestHeadersToAdd"`
}

type rlsHeaderValue struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func (r *rateLimit) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	desc := r.buildDescriptors(req)
	if len(desc) == 0 {
		// No descriptor resolved (e.g. missing header) -> nothing to limit on.
		r.next.ServeHTTP(rw, req)
		return
	}

	payload, err := json.Marshal(rlsRequest{
		Domain:      r.cfg.Domain,
		Descriptors: desc,
		HitsAddend:  r.cfg.HitsAddend,
	})
	if err != nil {
		r.onServiceError(rw, req)
		return
	}

	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodPost, r.cfg.ServiceURL, bytes.NewReader(payload))
	if err != nil {
		r.onServiceError(rw, req)
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(httpReq)
	if err != nil {
		r.onServiceError(rw, req)
		return
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	var parsed rlsResponse
	_ = json.NewDecoder(resp.Body).Decode(&parsed)

	overLimit := resp.StatusCode == http.StatusTooManyRequests ||
		strings.EqualFold(parsed.OverallCode, "OVER_LIMIT")

	for _, h := range parsed.ResponseHeadersToAdd {
		rw.Header().Add(h.Key, h.Value)
	}

	if overLimit {
		rw.WriteHeader(http.StatusTooManyRequests)
		_, _ = rw.Write([]byte("rate limit exceeded\n"))
		return
	}

	for _, h := range parsed.RequestHeadersToAdd {
		req.Header.Set(h.Key, h.Value)
	}

	r.next.ServeHTTP(rw, req)
}

// buildDescriptors mirrors Envoy's semantics: if any entry in a descriptor
// resolves to no value (and has no default), that whole descriptor is dropped.
func (r *rateLimit) buildDescriptors(req *http.Request) []rlsDescriptor {
	values := &requestValues{req: req, maxBody: r.cfg.MaxBodyBytes}
	out := make([]rlsDescriptor, 0, len(r.cfg.Descriptors))
	for _, d := range r.cfg.Descriptors {
		entries := make([]rlsEntry, 0, len(d.Entries))
		drop := false
		for _, e := range d.Entries {
			val, ok := resolveValue(values, e.From)
			if !ok {
				val = ""
			}
			if e.TrimSpace {
				val = strings.TrimSpace(val)
			}
			if e.Lowercase {
				val = strings.ToLower(val)
			}
			if val == "" {
				val = e.Default
			}
			if val == "" {
				drop = true
				break
			}
			entries = append(entries, rlsEntry{Key: e.Key, Value: val})
		}
		if drop || len(entries) == 0 {
			continue
		}
		out = append(out, rlsDescriptor{Entries: entries})
	}
	return out
}

// requestValues resolves values from a request and caches the parsed form body
// so the body is read at most once per request.
type requestValues struct {
	req      *http.Request
	maxBody  int64
	form     url.Values
	formDone bool
}

func (v *requestValues) formValue(name string) (string, bool) {
	if !v.formDone {
		v.formDone = true
		v.form = parseFormBody(v.req, v.maxBody)
	}
	if v.form == nil {
		return "", false
	}
	val := v.form.Get(name)
	return val, val != ""
}

func resolveValue(v *requestValues, from string) (string, bool) {
	req := v.req
	switch {
	case from == "remote_address":
		return clientIP(req), true
	case from == "path":
		return req.URL.Path, true
	case from == "method":
		return req.Method, true
	case from == "host":
		return req.Host, true
	case strings.HasPrefix(from, "header:"):
		v := req.Header.Get(strings.TrimPrefix(from, "header:"))
		return v, v != ""
	case strings.HasPrefix(from, "value:"):
		return strings.TrimPrefix(from, "value:"), true
	case strings.HasPrefix(from, "form:"):
		return v.formValue(strings.TrimPrefix(from, "form:"))
	default:
		return "", false
	}
}

// readCloser combines a replacement reader with the original body's Close.
type readCloser struct {
	io.Reader
	io.Closer
}

// parseFormBody buffers up to maxBody bytes of the request body, restores the
// body so downstream handlers receive identical bytes, and parses form fields
// from urlencoded or multipart bodies. Query string values are not included.
// It returns nil if the body is not a form, too large, or unreadable.
func parseFormBody(req *http.Request, maxBody int64) url.Values {
	if req.Body == nil || req.Body == http.NoBody {
		return nil
	}
	mediaType, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil {
		return nil
	}
	if mediaType != "application/x-www-form-urlencoded" && mediaType != "multipart/form-data" {
		return nil
	}
	if maxBody <= 0 {
		maxBody = defaultMaxBodyBytes
	}

	// Read one byte past the limit to detect oversized bodies, without
	// overflowing when maxBody is math.MaxInt64.
	readLimit := maxBody
	if readLimit < math.MaxInt64 {
		readLimit++
	}

	orig := req.Body
	buf, err := io.ReadAll(io.LimitReader(orig, readLimit))
	if err != nil || int64(len(buf)) > maxBody {
		// Hand downstream what we consumed followed by the rest of the stream.
		req.Body = readCloser{io.MultiReader(bytes.NewReader(buf), orig), orig}
		return nil
	}
	req.Body = readCloser{bytes.NewReader(buf), orig}
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(buf)), nil
	}

	// Any parse or read error makes the whole form unresolved. Returning the
	// pairs parsed around a malformed portion would risk resolving a different
	// value than the backend's (more lenient or stricter) parser sees.
	if mediaType == "application/x-www-form-urlencoded" {
		values, err := url.ParseQuery(string(buf))
		if err != nil {
			return nil
		}
		return values
	}

	boundary := params["boundary"]
	if boundary == "" {
		return nil
	}
	values := url.Values{}
	mr := multipart.NewReader(bytes.NewReader(buf), boundary)
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return values
		}
		if err != nil {
			return nil
		}
		if part.FileName() != "" || part.FormName() == "" {
			_ = part.Close()
			continue
		}
		data, err := io.ReadAll(part)
		_ = part.Close()
		if err != nil {
			return nil
		}
		values.Add(part.FormName(), string(data))
	}
}

func clientIP(req *http.Request) string {
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		return req.RemoteAddr
	}
	return host
}

func (r *rateLimit) onServiceError(rw http.ResponseWriter, req *http.Request) {
	if r.cfg.FailOnError {
		rw.WriteHeader(http.StatusServiceUnavailable)
		_, _ = rw.Write([]byte("rate limit service unavailable\n"))
		return
	}
	r.next.ServeHTTP(rw, req)
}

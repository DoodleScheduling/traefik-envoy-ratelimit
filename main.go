package traefik_envoy_ratelimit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
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
}

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
	From string `json:"from,omitempty"`
}

// CreateConfig returns the default configuration. Required by Traefik.
func CreateConfig() *Config {
	return &Config{
		TimeoutMs:  50,
		HitsAddend: 1,
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

	if overLimit {
		rw.WriteHeader(http.StatusTooManyRequests)
		_, _ = rw.Write([]byte("rate limit exceeded\n"))
		return
	}

	for _, h := range parsed.RequestHeadersToAdd {
		req.Header.Set(h.Key, h.Value)
	}

	for _, h := range parsed.ResponseHeadersToAdd {
		rw.Header().Add(h.Key, h.Value)
	}

	r.next.ServeHTTP(rw, req)
}

// buildDescriptors mirrors Envoy's semantics: if any entry in a descriptor
// resolves to no value, that whole descriptor is dropped (not sent).
func (r *rateLimit) buildDescriptors(req *http.Request) []rlsDescriptor {
	out := make([]rlsDescriptor, 0, len(r.cfg.Descriptors))
	for _, d := range r.cfg.Descriptors {
		entries := make([]rlsEntry, 0, len(d.Entries))
		drop := false
		for _, e := range d.Entries {
			val, ok := resolveValue(req, e.From)
			if !ok || val == "" {
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

func resolveValue(req *http.Request, from string) (string, bool) {
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
	default:
		return "", false
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

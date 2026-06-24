package traefik_envoy_ratelimit

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNew(t *testing.T) {
	tests := []struct {
		name        string
		errExpected bool
		cfg         *Config
	}{
		{
			name:        "no serviceUrl returns error",
			errExpected: true,
			cfg:         &Config{},
		},
		{
			name:        "no domain returns error",
			errExpected: true,
			cfg: &Config{
				ServiceURL: "https://example.com",
			},
		},
		{
			name:        "returns new handler with defaults",
			errExpected: true,
			cfg: &Config{
				ServiceURL: "https://example.com",
				Domain:     "mydomain",
			},
		},
		{
			name:        "returns new handler with custom settings",
			errExpected: true,
			cfg: &Config{
				ServiceURL: "https://example.com",
				Domain:     "mydomain",
				TimeoutMs:  100,
				HitsAddend: 2,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, err := New(t.Context(), nil, CreateConfig(), tt.name)
			if tt.errExpected {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.NotNil(t, handler)

			rateLimitHandler, ok := handler.(*rateLimit)
			assert.True(t, ok)

			assert.Equal(t, tt.cfg.ServiceURL, rateLimitHandler.cfg.ServiceURL)
			assert.Equal(t, tt.cfg.Domain, rateLimitHandler.cfg.Domain)

			if tt.cfg.TimeoutMs == 0 {
				assert.Equal(t, 50*time.Millisecond, rateLimitHandler.cfg.TimeoutMs)
			} else {
				assert.Equal(t, tt.cfg.TimeoutMs, rateLimitHandler.cfg.TimeoutMs)
			}

			if tt.cfg.HitsAddend == 0 {
				assert.Equal(t, 1, rateLimitHandler.cfg.TimeoutMs)
			} else {
				assert.Equal(t, tt.cfg.HitsAddend, rateLimitHandler.cfg.TimeoutMs)
			}

			assert.Equal(t, tt.cfg.FailOnError, rateLimitHandler.cfg.FailOnError)
		})
	}
}

func TestServeHTTP(t *testing.T) {
	tests := []struct {
		name             string
		cfg              *Config
		request          func() *http.Request
		expectedResponse func() *http.Response
		rateLimitHandler func(t *testing.T, req *http.Request) (*http.Response, error)
		upstreamHandler  func(t *testing.T, rw http.ResponseWriter, req *http.Request)
	}{
		{
			name: "over limit returns 429",
			cfg: &Config{
				ServiceURL: "https://example.com",
				Domain:     "mydomain",
				Descriptors: []DescriptorConfig{
					{
						Entries: []EntryConfig{
							{
								From: "header:myheader",
								Key:  "myheader",
							},
						},
					},
				},
			},
			request: func() *http.Request {
				req, _ := http.NewRequest(http.MethodPost, "http://dest", io.NopCloser(bytes.NewReader(nil)))
				req.Header.Set("myheader", "myvalue")
				return req
			},
			expectedResponse: func() *http.Response {
				response := &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Header:     make(http.Header),
				}
				return response
			},
			upstreamHandler: func(t *testing.T, rw http.ResponseWriter, req *http.Request) {
				assert.Fail(t, "upstream handler should not be called")
			},
			rateLimitHandler: func(t *testing.T, req *http.Request) (*http.Response, error) {
				assert.Equal(t, []string{"application/json"}, req.Header["Content-Type"])
				assert.Equal(t, http.MethodPost, req.Method)
				assert.Equal(t, "https://example.com", req.URL.String())

				var body rlsRequest
				assert.NoError(t, json.NewDecoder(req.Body).Decode(&body))
				assert.Equal(t, "mydomain", body.Domain)
				assert.Equal(t, "myvalue", body.Descriptors[0].Entries[0].Value)

				response := rlsResponse{
					OverallCode: "OVER_LIMIT",
					ResponseHeadersToAdd: []rlsHeaderValue{
						{
							Key:   "hello",
							Value: "world",
						},
					},
				}

				responseBody, err := json.Marshal(response)
				assert.NoError(t, err)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(responseBody)),
				}, nil
			},
		},
		{
			name: "not ratelimited returns upstream response",
			cfg: &Config{
				ServiceURL: "https://example.com",
				Domain:     "mydomain",
				Descriptors: []DescriptorConfig{
					{
						Entries: []EntryConfig{
							{
								From: "header:myheader",
								Key:  "myheader",
							},
						},
					},
				},
			},
			request: func() *http.Request {
				req, _ := http.NewRequest(http.MethodPost, "http://dest", io.NopCloser(bytes.NewReader(nil)))
				req.Header.Set("myheader", "myvalue")
				return req
			},
			expectedResponse: func() *http.Response {
				response := &http.Response{
					StatusCode: http.StatusPartialContent,
					Header:     make(http.Header),
				}
				response.Header.Set("hello", "world")
				return response
			},
			upstreamHandler: func(t *testing.T, rw http.ResponseWriter, req *http.Request) {
				assert.Equal(t, "myvalue", req.Header.Get("myheader"))
				rw.WriteHeader(http.StatusPartialContent)
			},
			rateLimitHandler: func(t *testing.T, req *http.Request) (*http.Response, error) {
				assert.Equal(t, []string{"application/json"}, req.Header["Content-Type"])
				assert.Equal(t, http.MethodPost, req.Method)
				assert.Equal(t, "https://example.com", req.URL.String())

				var body rlsRequest
				assert.NoError(t, json.NewDecoder(req.Body).Decode(&body))
				assert.Equal(t, "mydomain", body.Domain)
				assert.Equal(t, "myvalue", body.Descriptors[0].Entries[0].Value)

				response := rlsResponse{
					OverallCode: "OK",
					ResponseHeadersToAdd: []rlsHeaderValue{
						{
							Key:   "hello",
							Value: "world",
						},
					},
					RequestHeadersToAdd: []rlsHeaderValue{
						{
							Key:   "myheader",
							Value: "myvalue",
						},
					},
				}

				responseBody, err := json.Marshal(response)
				assert.NoError(t, err)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(responseBody)),
				}, nil
			},
		},
		{
			name: "in case of a ratelimit service problem it returns upstream if FailOnError is false",
			cfg: &Config{
				ServiceURL: "https://example.com",
				Domain:     "mydomain",
				Descriptors: []DescriptorConfig{
					{
						Entries: []EntryConfig{
							{
								From: "header:myheader",
								Key:  "myheader",
							},
						},
					},
				},
			},
			request: func() *http.Request {
				req, _ := http.NewRequest(http.MethodPost, "http://dest", io.NopCloser(bytes.NewReader(nil)))
				req.Header.Set("myheader", "myvalue")
				return req
			},
			expectedResponse: func() *http.Response {
				response := &http.Response{
					StatusCode: http.StatusPartialContent,
					Header:     make(http.Header),
				}
				response.Header.Set("world", "hello")
				return response
			},
			upstreamHandler: func(t *testing.T, rw http.ResponseWriter, req *http.Request) {
				assert.Equal(t, "myvalue", req.Header.Get("myheader"))
				rw.Header().Set("world", "hello")
				rw.WriteHeader(http.StatusPartialContent)
			},
			rateLimitHandler: func(t *testing.T, req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusInternalServerError,
					Body:       io.NopCloser(bytes.NewReader([]byte{})),
				}, nil
			},
		},
		{
			name: "in case of a ratelimit service problem it returns StatusServiceUnavailable if FailOnError is true",
			cfg: &Config{
				ServiceURL:  "https://example.com",
				Domain:      "mydomain",
				FailOnError: true,
				Descriptors: []DescriptorConfig{
					{
						Entries: []EntryConfig{
							{
								From: "header:myheader",
								Key:  "myheader",
							},
						},
					},
				},
			},
			request: func() *http.Request {
				req, _ := http.NewRequest(http.MethodPost, "http://dest", io.NopCloser(bytes.NewReader(nil)))
				req.Header.Set("myheader", "myvalue")
				return req
			},
			expectedResponse: func() *http.Response {
				response := &http.Response{
					StatusCode: http.StatusServiceUnavailable,
					Header:     make(http.Header),
				}
				return response
			},
			upstreamHandler: func(t *testing.T, rw http.ResponseWriter, req *http.Request) {
				assert.Fail(t, "upstream handler should not be called")
			},
			rateLimitHandler: func(t *testing.T, req *http.Request) (*http.Response, error) {
				return nil, errors.New("timed out")
			},
		},
		{
			name: "no descriptor match returns upstream response",
			cfg: &Config{
				ServiceURL: "https://example.com",
				Domain:     "mydomain",
				Descriptors: []DescriptorConfig{
					{
						Entries: []EntryConfig{
							{
								From: "remote_address",
								Key:  "",
							},
							{
								From: "path",
								Key:  "",
							},
							{
								From: "method",
								Key:  "",
							},
							{
								From: "host",
								Key:  "",
							},
						},
					},
				},
			},
			request: func() *http.Request {
				req, _ := http.NewRequest(http.MethodPost, "http://dest", io.NopCloser(bytes.NewReader(nil)))
				req.Header.Set("myheader", "myvalue")
				return req
			},
			expectedResponse: func() *http.Response {
				response := &http.Response{
					StatusCode: http.StatusPartialContent,
					Header:     make(http.Header),
				}
				return response
			},
			upstreamHandler: func(t *testing.T, rw http.ResponseWriter, req *http.Request) {
				assert.Equal(t, "myvalue", req.Header.Get("myheader"))
				rw.WriteHeader(http.StatusPartialContent)
			},
			rateLimitHandler: func(t *testing.T, req *http.Request) (*http.Response, error) {
				assert.Fail(t, "rateLimitHandler should not be called")
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader([]byte{})),
				}, nil
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := &rateLimit{
				next: &mockHTTPHandler{
					t:       t,
					handler: tt.upstreamHandler,
				},
				name: tt.name,
				cfg:  tt.cfg,
				client: &mockHTTPClient{
					t:       t,
					handler: tt.rateLimitHandler,
				},
			}

			responseWriter := httptest.NewRecorder()
			handler.ServeHTTP(responseWriter, tt.request())

			expectedResponse := tt.expectedResponse()
			assert.Equal(t, expectedResponse.StatusCode, responseWriter.Code)
			assert.Equal(t, expectedResponse.Header, responseWriter.Header())
		})
	}
}

type mockHTTPClient struct {
	t       *testing.T
	handler func(t *testing.T, req *http.Request) (*http.Response, error)
}

func (m *mockHTTPClient) Do(req *http.Request) (*http.Response, error) {
	return m.handler(m.t, req)
}

type mockHTTPHandler struct {
	t       *testing.T
	handler func(t *testing.T, rw http.ResponseWriter, req *http.Request)
}

func (m *mockHTTPHandler) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	m.handler(m.t, rw, req)
}

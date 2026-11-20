package traefik_envoy_ratelimit

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
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
				response.Header.Set("hello", "world")
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

func multipartBody(t *testing.T) (string, []byte) {
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	fw, err := w.CreateFormFile("username", "evil.txt")
	assert.NoError(t, err)
	_, _ = fw.Write([]byte("from-file"))
	assert.NoError(t, w.WriteField("username", "bob"))
	assert.NoError(t, w.WriteField("other", "x"))
	assert.NoError(t, w.Close())
	return w.FormDataContentType(), b.Bytes()
}

func TestFormValues(t *testing.T) {
	formEntry := EntryConfig{Key: "user", From: "form:username"}
	multipartType, multipartData := multipartBody(t)

	tests := []struct {
		name         string
		entries      []EntryConfig
		maxBodyBytes int64
		contentType  string
		body         []byte
		expected     []rlsEntry // nil means the descriptor must be dropped
	}{
		{
			name:        "urlencoded field is resolved",
			entries:     []EntryConfig{formEntry},
			contentType: "application/x-www-form-urlencoded",
			body:        []byte("x=1&username=alice&username=other"),
			expected:    []rlsEntry{{Key: "user", Value: "alice"}},
		},
		{
			name:        "urlencoded content type with charset parameter",
			entries:     []EntryConfig{formEntry},
			contentType: "Application/X-WWW-Form-Urlencoded; charset=UTF-8",
			body:        []byte("username=alice%40example.com"),
			expected:    []rlsEntry{{Key: "user", Value: "alice@example.com"}},
		},
		{
			name:        "multipart text field is resolved and file parts are skipped",
			entries:     []EntryConfig{formEntry},
			contentType: multipartType,
			body:        multipartData,
			expected:    []rlsEntry{{Key: "user", Value: "bob"}},
		},
		{
			name:        "multiple form entries share one body read",
			entries:     []EntryConfig{formEntry, {Key: "client", From: "form:client_id"}},
			contentType: "application/x-www-form-urlencoded",
			body:        []byte("username=alice&client_id=web"),
			expected:    []rlsEntry{{Key: "user", Value: "alice"}, {Key: "client", Value: "web"}},
		},
		{
			// the request URL carries ?username=fromquery which must not be used
			name:        "missing field drops descriptor and query string is ignored",
			entries:     []EntryConfig{formEntry},
			contentType: "application/x-www-form-urlencoded",
			body:        []byte("other=1"),
		},
		{
			name:        "json body is not parsed",
			entries:     []EntryConfig{formEntry},
			contentType: "application/json",
			body:        []byte(`{"username":"alice"}`),
		},
		{
			name:         "oversized body is not parsed",
			entries:      []EntryConfig{formEntry},
			maxBodyBytes: 16,
			contentType:  "application/x-www-form-urlencoded",
			body:         []byte("username=alice&padding=" + strings.Repeat("a", 64)),
		},
		{
			name:         "maxBodyBytes of MaxInt64 does not overflow",
			entries:      []EntryConfig{formEntry},
			maxBodyBytes: math.MaxInt64,
			contentType:  "application/x-www-form-urlencoded",
			body:         []byte("username=alice"),
			expected:     []rlsEntry{{Key: "user", Value: "alice"}},
		},
		{
			name:         "oversized body falls back to default",
			entries:      []EntryConfig{{Key: "user", From: "form:username", Default: "__unresolved__"}},
			maxBodyBytes: 16,
			contentType:  "application/x-www-form-urlencoded",
			body:         []byte("username=alice&padding=" + strings.Repeat("a", 64)),
			expected:     []rlsEntry{{Key: "user", Value: "__unresolved__"}},
		},
		{
			name:        "json body falls back to default",
			entries:     []EntryConfig{{Key: "user", From: "form:username", Default: "__unresolved__"}},
			contentType: "application/json",
			body:        []byte(`{"username":"alice"}`),
			expected:    []rlsEntry{{Key: "user", Value: "__unresolved__"}},
		},
		{
			name:        "urlencoded body with invalid escape after the field falls back to default",
			entries:     []EntryConfig{{Key: "user", From: "form:username", Default: "__unresolved__"}},
			contentType: "application/x-www-form-urlencoded",
			body:        []byte("username=victim&x=%zz"),
			expected:    []rlsEntry{{Key: "user", Value: "__unresolved__"}},
		},
		{
			// Go skips "username=x;y" while e.g. Netty would see it as the first value
			name:        "urlencoded body with semicolon pair falls back to default",
			entries:     []EntryConfig{{Key: "user", From: "form:username", Default: "__unresolved__"}},
			contentType: "application/x-www-form-urlencoded",
			body:        []byte("username=x;y&username=victim"),
			expected:    []rlsEntry{{Key: "user", Value: "__unresolved__"}},
		},
		{
			name:        "malformed urlencoded body without default drops descriptor",
			entries:     []EntryConfig{formEntry},
			contentType: "application/x-www-form-urlencoded",
			body:        []byte("username=victim&x=%zz"),
		},
		{
			name:        "truncated multipart body falls back to default",
			entries:     []EntryConfig{{Key: "user", From: "form:username", Default: "__unresolved__"}},
			contentType: "multipart/form-data; boundary=XYZ",
			body:        []byte("--XYZ\r\nContent-Disposition: form-data; name=\"username\"\r\n\r\nvictim\r\n--XYZ\r\nContent-Disposition: form-data; name=\"x\"\r\n\r\ntrunc"),
			expected:    []rlsEntry{{Key: "user", Value: "__unresolved__"}},
		},
		{
			name:        "multipart content type without boundary falls back to default",
			entries:     []EntryConfig{{Key: "user", From: "form:username", Default: "__unresolved__"}},
			contentType: "multipart/form-data",
			body:        []byte("username=victim"),
			expected:    []rlsEntry{{Key: "user", Value: "__unresolved__"}},
		},
		{
			name:        "trimSpace and lowercase normalize the value",
			entries:     []EntryConfig{{Key: "user", From: "form:username", TrimSpace: true, Lowercase: true}},
			contentType: "application/x-www-form-urlencoded",
			body:        []byte("username=+%20Alice%40X.com%09"),
			expected:    []rlsEntry{{Key: "user", Value: "alice@x.com"}},
		},
		{
			name:        "whitespace-only value with trimSpace falls back to default",
			entries:     []EntryConfig{{Key: "user", From: "form:username", TrimSpace: true, Default: "__unresolved__"}},
			contentType: "application/x-www-form-urlencoded",
			body:        []byte("username=+++"),
			expected:    []rlsEntry{{Key: "user", Value: "__unresolved__"}},
		},
		{
			name:        "default applies to header source",
			entries:     []EntryConfig{{Key: "h", From: "header:X-Missing", Default: "none"}},
			contentType: "text/plain",
			body:        []byte("hello"),
			expected:    []rlsEntry{{Key: "h", Value: "none"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rlsCalled := false
			upstreamCalled := false

			handler := &rateLimit{
				next: &mockHTTPHandler{
					t: t,
					handler: func(t *testing.T, rw http.ResponseWriter, req *http.Request) {
						upstreamCalled = true
						got, err := io.ReadAll(req.Body)
						assert.NoError(t, err)
						assert.Equal(t, tt.body, got, "upstream must receive the untouched body")
						assert.NoError(t, req.Body.Close())
						rw.WriteHeader(http.StatusNoContent)
					},
				},
				cfg: &Config{
					ServiceURL:   "https://example.com",
					Domain:       "mydomain",
					MaxBodyBytes: tt.maxBodyBytes,
					Descriptors:  []DescriptorConfig{{Entries: tt.entries}},
				},
				client: &mockHTTPClient{
					t: t,
					handler: func(t *testing.T, req *http.Request) (*http.Response, error) {
						rlsCalled = true
						var body rlsRequest
						assert.NoError(t, json.NewDecoder(req.Body).Decode(&body))
						if assert.Len(t, body.Descriptors, 1) {
							assert.Equal(t, tt.expected, body.Descriptors[0].Entries)
						}
						return &http.Response{
							StatusCode: http.StatusOK,
							Body:       io.NopCloser(strings.NewReader(`{"overallCode":"OK"}`)),
						}, nil
					},
				},
			}

			req := httptest.NewRequest(http.MethodPost, "http://dest/login?username=fromquery", bytes.NewReader(tt.body))
			req.Header.Set("Content-Type", tt.contentType)
			rw := httptest.NewRecorder()
			handler.ServeHTTP(rw, req)

			assert.Equal(t, tt.expected != nil, rlsCalled)
			assert.True(t, upstreamCalled)
			assert.Equal(t, http.StatusNoContent, rw.Code)
		})
	}
}

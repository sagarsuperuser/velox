// Package client is a minimal HTTP wrapper for the Velox API. It is
// deliberately thin — just enough to set the auth header, encode a JSON
// body, and decode a JSON response — so the CLI is honest about hitting
// the same /v1/* surface any external integration would.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is the local-dev API endpoint. Production operators
// override via VELOX_API_URL or --api-url.
const DefaultBaseURL = "http://localhost:8080"

// DefaultTimeout caps a single API call. Long-running list pages and a
// PDF-render-on-send-now both finish well inside this window.
const DefaultTimeout = 30 * time.Second

// Client wraps an *http.Client with the bits every Velox API call needs:
// a base URL, an API key, and a fixed Content-Type. Callers don't
// construct *http.Request directly.
type Client struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

// New builds a Client with sensible defaults. baseURL "" is treated as
// DefaultBaseURL; trailing slashes are trimmed so URL composition is
// stable.
func New(baseURL, apiKey string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		APIKey:     apiKey,
		HTTPClient: &http.Client{Timeout: DefaultTimeout},
	}
}

// APIError wraps a non-2xx response. The CLI surfaces these so the
// operator sees the exact status + body the server returned — easier to
// debug than a generic "request failed".
type APIError struct {
	StatusCode int
	Method     string
	Path       string
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("velox api: %s %s: %d %s", e.Method, e.Path, e.StatusCode, strings.TrimSpace(e.Body))
}

// Get performs an authenticated GET against path with query params, and
// decodes the JSON response into out. path must start with "/".
func (c *Client) Get(ctx context.Context, path string, query url.Values, out any) error {
	full := c.BaseURL + path
	if len(query) > 0 {
		full += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	return c.do(req, http.MethodGet, path, out)
}

// Post performs an authenticated POST. body may be nil for endpoints
// that accept an empty body. out may be nil if the caller doesn't care
// about the response payload.
func (c *Client) Post(ctx context.Context, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode body: %w", err)
		}
		reader = bytes.NewReader(buf)
	}
	full := c.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, full, reader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.do(req, http.MethodPost, path, out)
}

// do is the shared transport step: stamps the bearer header, executes
// the request, and decodes the body. Non-2xx responses surface as
// *APIError with the raw body — the operator decides what to do.
func (c *Client) do(req *http.Request, method, path string, out any) error {
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("velox api: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("velox api: %s %s: read body: %w", method, path, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{
			StatusCode: resp.StatusCode,
			Method:     method,
			Path:       path,
			Body:       string(body),
		}
	}

	if out == nil {
		return nil
	}
	if len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("velox api: %s %s: decode response: %w", method, path, err)
	}
	return nil
}

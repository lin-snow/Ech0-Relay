// SPDX-License-Identifier: Apache-2.0

// Package ech0 is a thin client for the subset of the Ech0 REST API the relay
// needs: create an echo, list tags, query echoes (for retention counting) and
// delete an echo. All calls go through /api and use a Bearer access token.
package ech0

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"time"
)

// Client talks to one Ech0 instance with one access token.
type Client struct {
	BaseURL string // instance root, e.g. https://echo.example.com (no /api)
	Token   string // access token with echo:read + echo:write, audience integration
	HTTP    *http.Client
	Retries int
	Logger  *slog.Logger
}

// New returns a Client with sane defaults.
func New(baseURL, token string) *Client {
	return &Client{
		BaseURL: baseURL,
		Token:   token,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
		Retries: 3,
		Logger:  slog.Default(),
	}
}

// Tag is an Ech0 tag (query filters by ID, not name, so callers resolve first).
type Tag struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// TagRef is the tag shape accepted when creating an echo.
type TagRef struct {
	Name string `json:"name"`
}

// EchoFileRef attaches a previously uploaded file to an echo by id.
type EchoFileRef struct {
	FileID    string `json:"file_id"`
	SortOrder int    `json:"sort_order"`
}

// EchoRequest is the body for POST /api/echo.
type EchoRequest struct {
	Content   string        `json:"content"`
	Private   bool          `json:"private"`
	CreatedAt *int64        `json:"created_at,omitempty"` // Unix seconds; backfills the TG post time
	Tags      []TagRef      `json:"tags,omitempty"`
	Layout    string        `json:"layout,omitempty"` // media layout, e.g. "waterfall"
	EchoFiles []EchoFileRef `json:"echo_files,omitempty"`
}

// FileDto is the subset of the upload response the relay needs.
type FileDto struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// EchoItem is the subset of an echo the relay reads back from a query.
type EchoItem struct {
	ID        string `json:"id"`
	CreatedAt int64  `json:"created_at"`
}

// APIError describes a non-success response (transport, HTTP or envelope-level).
type APIError struct {
	Status    int    // HTTP status
	Code      int    // envelope code (1 = success); 0 when unknown
	Msg       string // envelope msg or transport detail
	ErrorCode string // envelope error_code
}

func (e *APIError) Error() string {
	if e.ErrorCode != "" {
		return fmt.Sprintf("ech0 api: http %d code %d %s (%s)", e.Status, e.Code, e.Msg, e.ErrorCode)
	}
	return fmt.Sprintf("ech0 api: http %d code %d %s", e.Status, e.Code, e.Msg)
}

// envelope is the uniform Ech0 response wrapper.
type envelope struct {
	Code      int             `json:"code"`
	Msg       string          `json:"msg"`
	ErrorCode string          `json:"error_code"`
	Data      json.RawMessage `json:"data"`
}

// PostEcho creates a new echo.
func (c *Client) PostEcho(ctx context.Context, req EchoRequest) error {
	_, err := c.do(ctx, http.MethodPost, "/api/echo", req)
	return err
}

// ListTags returns all tags on the instance (for name -> id resolution).
func (c *Client) ListTags(ctx context.Context) ([]Tag, error) {
	data, err := c.do(ctx, http.MethodGet, "/api/tags", nil)
	if err != nil {
		return nil, err
	}
	var tags []Tag
	if err := json.Unmarshal(data, &tags); err != nil {
		return nil, fmt.Errorf("ech0: decode tags: %w", err)
	}
	return tags, nil
}

// QueryEchos runs POST /api/echo/query and returns the total count plus the
// page of items. Filter by tagIDs and order with sortBy/sortOrder
// (e.g. "created_at","asc" for oldest first).
func (c *Client) QueryEchos(ctx context.Context, tagIDs []string, sortBy, sortOrder string, page, pageSize int) (total int64, items []EchoItem, err error) {
	body := map[string]any{
		"page":      page,
		"pageSize":  pageSize,
		"tagIds":    tagIDs,
		"sortBy":    sortBy,
		"sortOrder": sortOrder,
	}
	data, err := c.do(ctx, http.MethodPost, "/api/echo/query", body)
	if err != nil {
		return 0, nil, err
	}
	var result struct {
		Total int64      `json:"total"`
		Items []EchoItem `json:"items"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return 0, nil, fmt.Errorf("ech0: decode query result: %w", err)
	}
	return result.Total, result.Items, nil
}

// DeleteEcho removes an echo by id.
func (c *Client) DeleteEcho(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodDelete, "/api/echo/"+id, nil)
	return err
}

// UploadImage uploads image bytes to the instance's local storage
// (POST /api/files/upload, multipart). Requires the file:write scope. The
// returned FileDto.ID is referenced from an echo via EchoFiles; Ech0
// garbage-collects uploads that stay unreferenced for 24h.
func (c *Client) UploadImage(ctx context.Context, filename string, data []byte) (FileDto, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return FileDto{}, fmt.Errorf("ech0: build multipart: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return FileDto{}, fmt.Errorf("ech0: build multipart: %w", err)
	}
	_ = mw.WriteField("category", "image")
	_ = mw.WriteField("storage_type", "local")
	if err := mw.Close(); err != nil {
		return FileDto{}, fmt.Errorf("ech0: build multipart: %w", err)
	}

	raw, err := c.doRaw(ctx, http.MethodPost, "/api/files/upload", buf.Bytes(), mw.FormDataContentType())
	if err != nil {
		return FileDto{}, err
	}
	var dto FileDto
	if err := json.Unmarshal(raw, &dto); err != nil {
		return FileDto{}, fmt.Errorf("ech0: decode upload result: %w", err)
	}
	if dto.ID == "" {
		return FileDto{}, fmt.Errorf("ech0: upload result missing file id")
	}
	return dto, nil
}

// DeleteFile removes an uploaded file by id (DELETE /api/file/{id}). Used to
// tidy up uploads whose echo failed to post; only safe for unreferenced files.
func (c *Client) DeleteFile(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodDelete, "/api/file/"+id, nil)
	return err
}

// do performs a request with retry/backoff and decodes the envelope. Success is
// HTTP 2xx AND code == 1; anything else is an *APIError. Only network errors,
// 429 and 5xx are retried — other 4xx (bad token/scope/body) fail immediately.
func (c *Client) do(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	var payload []byte
	var contentType string
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return nil, fmt.Errorf("ech0: marshal request: %w", err)
		}
		contentType = "application/json"
	}
	return c.doRaw(ctx, method, path, payload, contentType)
}

// doRaw is do for a pre-encoded payload (JSON or multipart).
func (c *Client) doRaw(ctx context.Context, method, path string, payload []byte, contentType string) (json.RawMessage, error) {
	url := c.BaseURL + path

	attempts := c.Retries
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			if err := sleep(ctx, backoff(attempt)); err != nil {
				return nil, err
			}
		}
		var reader io.Reader
		if payload != nil {
			reader = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, reader)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("Accept", "application/json")
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}

		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		data, apiErr, retryable := decodeResponse(resp)
		if retryable {
			lastErr = apiErr
			continue
		}
		if apiErr != nil {
			return nil, apiErr
		}
		return data, nil
	}
	return nil, fmt.Errorf("ech0: %s %s failed after %d attempts: %w", method, path, attempts, lastErr)
}

func decodeResponse(resp *http.Response) (data json.RawMessage, apiErr *APIError, retryable bool) {
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return nil, &APIError{Status: resp.StatusCode, Msg: snippet(raw)}, true
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, &APIError{Status: resp.StatusCode, Msg: "non-JSON response: " + snippet(raw)}, false
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && env.Code == 1 {
		return env.Data, nil, false
	}
	return nil, &APIError{Status: resp.StatusCode, Code: env.Code, Msg: env.Msg, ErrorCode: env.ErrorCode}, false
}

func snippet(b []byte) string {
	const max = 200
	s := string(bytes.TrimSpace(b))
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

func backoff(attempt int) time.Duration {
	d := time.Duration(1<<uint(attempt-1)) * time.Second
	if d > 15*time.Second {
		d = 15 * time.Second
	}
	return d
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

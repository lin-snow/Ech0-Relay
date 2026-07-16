// SPDX-License-Identifier: Apache-2.0

package ech0

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(url string) *Client {
	c := New(url, "test-token")
	c.HTTP = &http.Client{Timeout: 5 * time.Second}
	return c
}

func TestPostEcho_Success(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/echo" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":1,"msg":"ok","data":null}`))
	}))
	defer srv.Close()

	ts := int64(1_700_000_000)
	err := testClient(srv.URL).PostEcho(context.Background(), EchoRequest{
		Content:   "hello",
		CreatedAt: &ts,
		Tags:      []TagRef{{Name: "src"}},
	})
	if err != nil {
		t.Fatalf("PostEcho: %v", err)
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(gotBody), &body); err != nil {
		t.Fatalf("body not json: %v", err)
	}
	if body["created_at"].(float64) != 1_700_000_000 {
		t.Errorf("created_at = %v (want unix seconds)", body["created_at"])
	}
	if body["content"] != "hello" {
		t.Errorf("content = %v", body["content"])
	}
}

func TestPostEcho_EnvelopeFailIsError(t *testing.T) {
	// HTTP 200 but code:0 (e.g. missing scope) must surface as an error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":0,"msg":"forbidden","error_code":"SCOPE_FORBIDDEN","data":null}`))
	}))
	defer srv.Close()

	err := testClient(srv.URL).PostEcho(context.Background(), EchoRequest{Content: "x"})
	if err == nil {
		t.Fatal("expected error for code:0")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("want *APIError, got %T", err)
	}
	if apiErr.ErrorCode != "SCOPE_FORBIDDEN" {
		t.Errorf("ErrorCode = %q", apiErr.ErrorCode)
	}
}

func TestDo_RetriesOn5xxThenSucceeds(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":1,"msg":"ok","data":null}`))
	}))
	defer srv.Close()

	c := testClient(srv.URL)
	c.Retries = 3
	// Shorten backoff for the test by using an already-cancelled-free ctx.
	if err := c.PostEcho(context.Background(), EchoRequest{Content: "x"}); err != nil {
		t.Fatalf("PostEcho: %v", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2 (one 502 then success)", calls)
	}
}

func TestDo_NoRetryOn4xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":0,"msg":"no","error_code":"NO_PERMISSION"}`))
	}))
	defer srv.Close()

	err := testClient(srv.URL).DeleteEcho(context.Background(), "abc")
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (4xx must not retry)", calls)
	}
}

func TestQueryEchos(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/echo/query" {
			t.Errorf("path = %s", r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["sortOrder"] != "asc" {
			t.Errorf("sortOrder = %v", body["sortOrder"])
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":1,"msg":"ok","data":{"total":42,"items":[{"id":"e1","created_at":100},{"id":"e2","created_at":200}]}}`))
	}))
	defer srv.Close()

	total, items, err := testClient(srv.URL).QueryEchos(context.Background(), []string{"t1"}, "created_at", "asc", 1, 2)
	if err != nil {
		t.Fatalf("QueryEchos: %v", err)
	}
	if total != 42 {
		t.Errorf("total = %d", total)
	}
	if len(items) != 2 || items[0].ID != "e1" || items[1].CreatedAt != 200 {
		t.Errorf("items = %+v", items)
	}
}

func TestDeleteEcho_PathAndSuccess(t *testing.T) {
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":1,"msg":"ok","data":null}`))
	}))
	defer srv.Close()

	if err := testClient(srv.URL).DeleteEcho(context.Background(), "abc-123"); err != nil {
		t.Fatalf("DeleteEcho: %v", err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/api/echo/abc-123" {
		t.Errorf("got %s %s", gotMethod, gotPath)
	}
}

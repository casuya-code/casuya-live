package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type stubBroker struct {
	lastRaw string
	err     error
}

func (s *stubBroker) ExecuteEnvelope(_ context.Context, raw string) error {
	s.lastRaw = raw
	return s.err
}

func newTestServer(b *stubBroker, token string) *httptest.Server {
	h := New(b, "127.0.0.1:0", token)
	return httptest.NewServer(h.server.Handler)
}

func TestExecuteRequiresAuth(t *testing.T) {
	b := &stubBroker{}
	srv := newTestServer(b, "secret")
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/execute", "application/json",
		strings.NewReader(`{"envelope":"a.b"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestExecuteAllowsOnlyPost(t *testing.T) {
	b := &stubBroker{}
	srv := newTestServer(b, "secret")
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/execute", nil)
	req.Header.Set("X-Internal-Token", "secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", resp.StatusCode)
	}
}

func TestExecuteRoutesValidEnvelope(t *testing.T) {
	b := &stubBroker{}
	srv := newTestServer(b, "secret")
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/execute",
		strings.NewReader(`{"envelope":"a.b"}`))
	req.Header.Set("X-Internal-Token", "secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	var out map[string]string
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("bad response json: %v", err)
	}
	if out["status"] != "filled" {
		t.Fatalf("expected status filled, got %q", out["status"])
	}
	if b.lastRaw != "a.b" {
		t.Fatalf("broker did not receive envelope, got %q", b.lastRaw)
	}
}

func TestExecuteRejectsBrokerError(t *testing.T) {
	b := &stubBroker{err: context.DeadlineExceeded}
	srv := newTestServer(b, "secret")
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/execute",
		strings.NewReader(`{"envelope":"a.b"}`))
	req.Header.Set("X-Internal-Token", "secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", resp.StatusCode)
	}
}

func TestExecuteRejectsBadBody(t *testing.T) {
	b := &stubBroker{}
	srv := newTestServer(b, "secret")
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/execute",
		strings.NewReader(`not json`))
	req.Header.Set("X-Internal-Token", "secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestHealthz(t *testing.T) {
	b := &stubBroker{}
	srv := newTestServer(b, "secret")
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}
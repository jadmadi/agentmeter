// SPDX-License-Identifier: AGPL-3.0-only

package emit

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

const serveHTML = `<!doctype html><html><body><script src="data/telemetry-data.js"></script></body></html>`

func serveReport(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	data := map[string]any{"schemaVersion": int64(3), "title": "serve me"}
	if _, _, _, err := WriteReport(dir, data, []byte(serveHTML), "CMD_TELEMETRY"); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	return dir
}

func TestListenBindsLoopbackOnly(t *testing.T) {
	listener, err := listen(0)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	host, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("bound %q, want 127.0.0.1", host)
	}
	if port == "" || port == "0" {
		t.Fatalf("port = %q, want an allocated port", port)
	}
}

func TestNewHandlerRoutes(t *testing.T) {
	dir := serveReport(t)
	handler := NewHandler(dir)

	cases := []struct {
		path        string
		status      int
		contentType string
		contains    string
	}{
		{"/", http.StatusOK, htmlContentType, "<html>"},
		{"/index.html", http.StatusOK, htmlContentType, "telemetry-data.js"},
		{"/telemetry-data.js", http.StatusOK, jsContentType, "window.CMD_TELEMETRY = {"},
		{"/telemetry-data.json", http.StatusOK, jsonContentType, `"schemaVersion": 3`},
		{"/missing.txt", http.StatusNotFound, "", "404 page not found"},
		{"/data/telemetry-data.js", http.StatusNotFound, "", "404 page not found"},
		{"/../etc/passwd", http.StatusNotFound, "", "404 page not found"},
		{"/telemetry-data.js/", http.StatusNotFound, "", "404 page not found"},
	}
	for _, test := range cases {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))
		if recorder.Code != test.status {
			t.Errorf("GET %s = %d, want %d", test.path, recorder.Code, test.status)
			continue
		}
		if test.contentType != "" {
			if got := recorder.Header().Get("Content-Type"); got != test.contentType {
				t.Errorf("GET %s content type = %q, want %q", test.path, got, test.contentType)
			}
		}
		if test.contains != "" && !strings.Contains(recorder.Body.String(), test.contains) {
			t.Errorf("GET %s body missing %q:\n%s", test.path, test.contains, recorder.Body.String())
		}
	}
}

func TestNewHandlerHasNoDirectoryListing(t *testing.T) {
	dir := serveReport(t)
	recorder := httptest.NewRecorder()
	NewHandler(dir).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	body := recorder.Body.String()
	if strings.Contains(body, "telemetry-data.json") || strings.Contains(body, "Index of") {
		t.Fatalf("root response looks like a directory listing:\n%s", body)
	}
}

func TestNewHandlerMethods(t *testing.T) {
	handler := NewHandler(serveReport(t))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodHead, "/telemetry-data.json", nil))
	if recorder.Code != http.StatusOK {
		t.Errorf("HEAD = %d, want 200", recorder.Code)
	}
	if recorder.Body.Len() != 0 {
		t.Errorf("HEAD returned a body: %q", recorder.Body.String())
	}
	if recorder.Header().Get("Content-Length") == "" {
		t.Error("HEAD did not set Content-Length")
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d, want 405", recorder.Code)
	}
	if recorder.Header().Get("Allow") != "GET, HEAD" {
		t.Errorf("Allow = %q", recorder.Header().Get("Allow"))
	}
}

func TestNewHandlerMissingDataFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/index.html", []byte("<html></html>"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	recorder := httptest.NewRecorder()
	NewHandler(dir).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/telemetry-data.js", nil))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("missing data file = %d, want 500", recorder.Code)
	}
}

func TestServeContextValidation(t *testing.T) {
	if err := ServeContext(context.Background(), t.TempDir(), 0, false, nil); err == nil {
		t.Fatal("missing report accepted")
	}

	dir := serveReport(t)
	for _, port := range []int{-1, 70000} {
		if err := ServeContext(context.Background(), dir, port, false, nil); err == nil {
			t.Errorf("port %d accepted", port)
		}
	}

	// A busy port must surface as an error rather than a silent fallback.
	occupied, err := listen(0)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer occupied.Close()
	_, portText, err := net.SplitHostPort(occupied.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse port %q: %v", portText, err)
	}
	if err := ServeContext(context.Background(), dir, port, false, nil); err == nil {
		t.Fatal("busy port accepted")
	}
}

func TestServeMissingReport(t *testing.T) {
	if err := Serve(t.TempDir(), 0, false); err == nil {
		t.Fatal("Serve accepted an empty report directory")
	}
}

func TestServeListenerServesAndShutsDown(t *testing.T) {
	dir := serveReport(t)
	listener, err := listen(0)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var log strings.Builder
	done := make(chan error, 1)
	go func() { done <- serveListener(ctx, listener, dir, false, &log) }()

	base := "http://" + listener.Addr().String()
	client := &http.Client{Timeout: 2 * time.Second}

	status, body, _ := fetch(t, client, base+"/")
	if status != http.StatusOK || !strings.Contains(body, "telemetry-data.js") {
		t.Fatalf("GET / = %d %q", status, body)
	}
	status, body, _ = fetch(t, client, base+"/telemetry-data.js")
	if status != http.StatusOK || !strings.HasPrefix(body, "window.CMD_TELEMETRY = ") {
		t.Fatalf("GET js = %d %q", status, body)
	}
	status, body, _ = fetch(t, client, base+"/telemetry-data.json")
	if status != http.StatusOK || !strings.Contains(body, `"title": "serve me"`) {
		t.Fatalf("GET json = %d %q", status, body)
	}
	status, _, _ = fetch(t, client, base+"/nope")
	if status != http.StatusNotFound {
		t.Fatalf("GET /nope = %d, want 404", status)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serveListener: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveListener did not stop after context cancellation")
	}
	if !strings.Contains(log.String(), "Serving ") || !strings.Contains(log.String(), base) {
		t.Errorf("serve log = %q", log.String())
	}
}

// With no browser opener on PATH the server must print the URL and keep
// serving instead of failing.
func TestServeListenerBrowserFallback(t *testing.T) {
	t.Setenv("PATH", "")
	dir := serveReport(t)
	listener, err := listen(0)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var log strings.Builder
	done := make(chan error, 1)
	go func() { done <- serveListener(ctx, listener, dir, true, &log) }()

	base := "http://" + listener.Addr().String()
	client := &http.Client{Timeout: 2 * time.Second}
	if status, _, _ := fetch(t, client, base+"/"); status != http.StatusOK {
		t.Fatalf("GET / = %d", status)
	}
	if !strings.Contains(log.String(), "manually") || !strings.Contains(log.String(), base) {
		t.Errorf("fallback log = %q", log.String())
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serveListener: %v", err)
	}
}

// fetch retries until the server accepts connections or the deadline expires.
func fetch(t *testing.T, client *http.Client, url string) (int, string, http.Header) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		response, err := client.Get(url)
		if err == nil {
			body, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if readErr != nil {
				t.Fatalf("GET %s: %v", url, readErr)
			}
			return response.StatusCode, string(body), response.Header
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET %s: %v", url, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

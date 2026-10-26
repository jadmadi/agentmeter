// SPDX-License-Identifier: AGPL-3.0-only

package emit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"
)

// Content types for the three served files.
const (
	htmlContentType = "text/html; charset=utf-8"
	jsContentType   = "text/javascript; charset=utf-8"
	jsonContentType = "application/json; charset=utf-8"
)

// Serve serves the report in dir on http://127.0.0.1:port until the process
// receives SIGINT/SIGTERM, then shuts down gracefully.
//
// It binds loopback only and exposes exactly four routes: "/" and
// "/index.html" (the HTML shell), "/telemetry-data.js" and
// "/telemetry-data.json". Directory listings and any other path return 404.
// When openBrowser is true it launches the platform browser opener
// (xdg-open / open / rundll32); if the opener is missing or fails it prints
// the URL and keeps serving. A port of 0 asks the kernel for a free port and
// the chosen URL is printed.
func Serve(dir string, port int, openBrowser bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return ServeContext(ctx, dir, port, openBrowser, os.Stdout)
}

// ServeContext is Serve with a caller-supplied context and log writer, which
// makes it testable.
func ServeContext(ctx context.Context, dir string, port int, openBrowser bool, log io.Writer) error {
	if log == nil {
		log = io.Discard
	}
	if port < 0 || port > 65535 {
		return fmt.Errorf("emit: port %d out of range", port)
	}
	indexPath := filepath.Join(dir, HTMLName)
	info, err := os.Stat(indexPath)
	if err != nil {
		return fmt.Errorf("emit: report not found: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("emit: %s is a directory", indexPath)
	}
	listener, err := listen(port)
	if err != nil {
		return err
	}
	return serveListener(ctx, listener, dir, openBrowser, log)
}

// listen binds the loopback interface only. A port of 0 selects a free port.
func listen(port int) (net.Listener, error) {
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("emit: listen on %s: %w", address, err)
	}
	return listener, nil
}

// serveListener runs the HTTP server on listener until ctx is cancelled.
func serveListener(ctx context.Context, listener net.Listener, dir string, openBrowser bool, log io.Writer) error {
	server := &http.Server{
		Handler:           NewHandler(dir),
		ReadHeaderTimeout: 10 * time.Second,
	}
	url := "http://" + listener.Addr().String() + "/"
	fmt.Fprintf(log, "Serving %s at %s\n", dir, url)
	if openBrowser {
		openInBrowser(url, log)
	} else {
		fmt.Fprintf(log, "Open %s in a browser\n", url)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(listener)
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("emit: serve: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			// Force-close so the goroutine cannot leak.
			server.Close()
		}
		err := <-errCh
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("emit: serve: %w", err)
		}
		return nil
	}
}

// NewHandler returns the static report handler. Only the three report files
// and "/" are served; everything else is a 404, so there is no directory
// listing and no path traversal surface.
func NewHandler(dir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead:
		default:
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name, contentType, ok := route(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			http.Error(w, "report file not available", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	})
}

// route maps an exact request path to a report file.
func route(path string) (name, contentType string, ok bool) {
	switch path {
	case "/", "/index.html":
		return HTMLName, htmlContentType, true
	case "/" + JSName:
		return JSName, jsContentType, true
	case "/" + JSONName:
		return JSONName, jsonContentType, true
	}
	return "", "", false
}

// openInBrowser launches the platform opener, falling back to printing the
// URL. It never fails the serve loop.
func openInBrowser(url string, log io.Writer) {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", url)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		command = exec.Command("xdg-open", url)
	}
	if _, err := exec.LookPath(command.Path); err != nil {
		fmt.Fprintf(log, "No browser opener (%s) available; open %s manually\n", command.Path, url)
		return
	}
	if err := command.Start(); err != nil {
		fmt.Fprintf(log, "Could not open a browser (%v); open %s manually\n", err, url)
		return
	}
	// Reap the child without blocking the caller.
	go func() { _ = command.Wait() }()
}

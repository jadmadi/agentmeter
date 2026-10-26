// SPDX-License-Identifier: AGPL-3.0-only

package cmdtelemetry

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// serve starts a loopback-only static server for the three report files and
// optionally opens the browser. It blocks until the process is interrupted.
func serve(outDir string, port int, open bool) int {
	address := fmt.Sprintf("127.0.0.1:%d", port)
	listener, err := net.Listen("tcp", address)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agentmeter cmd: cannot listen on %s: %v\n", address, err)
		return 1
	}
	routes := map[string]string{
		"/":                    "index.html",
		"/index.html":          "index.html",
		"/telemetry-data.js":   "telemetry-data.js",
		"/telemetry-data.json": "telemetry-data.json",
	}
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		name, ok := routes[request.URL.Path]
		if !ok {
			http.NotFound(writer, request)
			return
		}
		http.ServeFile(writer, request, filepath.Join(outDir, name))
	})
	server := &http.Server{Handler: handler}
	url := fmt.Sprintf("http://%s/index.html", address)
	fmt.Printf("Serving %s at %s\n", outDir, url)
	if open {
		openBrowser(url)
	}
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "agentmeter cmd: server stopped: %v\n", err)
		return 1
	}
	return 0
}

// openBrowser uses the platform opener; when it is unavailable the URL is
// already printed, so the user can open it manually.
func openBrowser(url string) {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", url)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		command = exec.Command("xdg-open", url)
	}
	if err := command.Start(); err != nil {
		fmt.Printf("Open %s in a browser.\n", url)
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

package emit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Watch serves the report and rebuilds it when fingerprint changes.
//
// The initial report must already exist in dir. Watch binds loopback, opens
// the browser once, and polls fingerprint every interval. A rebuild failure is
// reported and does not stop the server; the previous report stays available.
// It returns when the process receives SIGINT/SIGTERM or the server fails.
func Watch(
	dir string,
	port int,
	openBrowser bool,
	interval time.Duration,
	fingerprint func() string,
	rebuild func() error,
) error {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	last := fingerprint()
	errCh := make(chan error, 1)
	go func() {
		errCh <- ServeContext(ctx, dir, port, openBrowser, os.Stdout)
	}()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-errCh:
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		case <-ticker.C:
			next := fingerprint()
			if next == last {
				continue
			}
			if err := rebuild(); err != nil {
				fmt.Fprintf(os.Stderr, "watch: rebuild failed: %v\n", err)
				continue
			}
			last = next
			fmt.Printf("Rebuilt report at %s\n", time.Now().Format(time.RFC3339))
		}
	}
}

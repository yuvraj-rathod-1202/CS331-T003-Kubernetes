package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

func newProbeClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{DisableKeepAlives: true},
	}
}

// probeOnce performs one GET and writes the result line. ts is the request start.
func probeOnce(client *http.Client, out *lineWriter, name, url string) {
	start := time.Now()
	code := 0
	resp, err := client.Get(url)
	if err == nil {
		code = resp.StatusCode
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	lat := time.Since(start).Microseconds()
	out.emitAt(start.UnixNano(), "http", name, strconv.Itoa(code), strconv.FormatInt(lat, 10))
}

// runHTTP probes every interval until ctx is done, then waits for in-flight requests.
func runHTTP(ctx context.Context, out *lineWriter, name, url string, interval, timeout time.Duration) {
	client := newProbeClient(timeout)
	var wg sync.WaitGroup
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case <-t.C:
			wg.Add(1)
			go func() {
				defer wg.Done()
				probeOnce(client, out, name, url)
			}()
		}
	}
}

type httpConfig struct {
	Name     string
	URL      string
	Interval time.Duration
	Timeout  time.Duration
}

func parseHTTPFlags(args []string) (httpConfig, error) {
	var c httpConfig
	fs := flag.NewFlagSet("http", flag.ContinueOnError)
	fs.StringVar(&c.Name, "name", "", "probe name")
	fs.StringVar(&c.URL, "url", "", "URL to GET")
	fs.DurationVar(&c.Interval, "interval", 100*time.Millisecond, "probe interval")
	fs.DurationVar(&c.Timeout, "timeout", 500*time.Millisecond, "request timeout")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if c.Name == "" || c.URL == "" {
		return c, fmt.Errorf("--name and --url are required")
	}
	if c.Interval <= 0 || c.Timeout <= 0 {
		return c, fmt.Errorf("--interval and --timeout must be positive")
	}
	return c, nil
}

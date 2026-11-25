package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPayloadRoundTrip(t *testing.T) {
	s := makePayload("q1-s03", 42, 1700000000123456789, 1)
	if s != "q1-s03 42 1700000000123456789 1" {
		t.Fatalf("got %q", s)
	}
	p, err := parsePayload(s)
	if err != nil || p.ClientID != "q1-s03" || p.Seq != 42 || p.SentNs != 1700000000123456789 || p.QoS != 1 {
		t.Fatalf("bad parse %+v %v", p, err)
	}
	for _, bad := range []string{"", "a b c", "a x 1 1", "a 1 y 1", "a 1 1 z"} {
		if _, err := parsePayload(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestClientIDAndReason(t *testing.T) {
	if clientID("q0", 7) != "q0-s07" || clientID("q1", 12) != "q1-s12" {
		t.Fatal("clientID")
	}
	if got := sanitizeReason("EOF reached: i/o  timeout"); got != "EOF_reached:_i/o_timeout" {
		t.Fatalf("got %q", got)
	}
}

func TestLineFormat(t *testing.T) {
	if got := formatLine(5, "http", "pod", "200", "1234"); got != "5 http pod 200 1234" {
		t.Fatalf("got %q", got)
	}
	var buf bytes.Buffer
	w := newLineWriter(&buf)
	w.emitAt(9, "connected", "x")
	if buf.String() != "9 connected x\n" {
		t.Fatalf("got %q", buf.String())
	}
}

func TestFlags(t *testing.T) {
	ok := []string{"--broker", "tcp://h:1883", "--group", "q1", "--clients", "3", "--rate", "5", "--qos", "1", "--keepalive", "15s"}
	c, err := parsePubFlags(ok)
	if err != nil || c.Clients != 3 || c.Rate != 5 || c.KeepAlive != 15*time.Second {
		t.Fatalf("%+v %v", c, err)
	}
	for _, bad := range [][]string{
		{},
		{"--broker", "b", "--group", "g", "--qos", "2"},
		{"--broker", "b", "--group", "g", "--clients", "0"},
		{"--broker", "b", "--group", "g", "--rate", "0"},
		{"--group", "g"},
	} {
		if _, err := parsePubFlags(bad); err == nil {
			t.Errorf("expected error for %v", bad)
		}
	}
	if _, err := parseSubFlags([]string{"--broker", "b"}); err == nil {
		t.Error("sub without id should fail")
	}
	if _, err := parseHTTPFlags([]string{"--name", "a"}); err == nil {
		t.Error("http without url should fail")
	}
}

func parseHTTPLine(t *testing.T, line string) (ts int64, code int, lat int64) {
	t.Helper()
	f := strings.Fields(line)
	if len(f) != 5 || f[1] != "http" {
		t.Fatalf("bad line %q", line)
	}
	ts, _ = strconv.ParseInt(f[0], 10, 64)
	code, _ = strconv.Atoi(f[3])
	lat, _ = strconv.ParseInt(f[4], 10, 64)
	return
}

func TestHTTPSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	var buf bytes.Buffer
	probeOnce(newProbeClient(time.Second), newLineWriter(&buf), "svc", srv.URL)
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], " http svc 200 ") {
		t.Fatalf("got %q", buf.String())
	}
	if ts, _, _ := parseHTTPLine(t, lines[0]); ts <= 0 {
		t.Fatal("bad ts")
	}
}

func TestHTTPClosedPort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()
	var buf bytes.Buffer
	probeOnce(newProbeClient(time.Second), newLineWriter(&buf), "pod", url)
	_, code, _ := parseHTTPLine(t, strings.TrimSpace(buf.String()))
	if code != 0 {
		t.Fatalf("want code 0, got %d", code)
	}
}

func TestRunHTTPLoop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	var buf bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()
	runHTTP(ctx, newLineWriter(&buf), "svc", srv.URL, 50*time.Millisecond, time.Second)
	n := len(strings.Split(strings.TrimSpace(buf.String()), "\n"))
	if n < 3 {
		t.Fatalf("expected several lines, got %d", n)
	}
}

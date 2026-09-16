package status

import (
	"context"
	"crypto/x509"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func trustedProbe(server *httptest.Server) *HTTPProbe {
	probe := NewHTTPProbe()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	probe.client.Transport.(*http.Transport).TLSClientConfig.RootCAs = roots
	return probe
}

func TestHTTPSResponseClassification(t *testing.T) {
	tests := []struct {
		name          string
		code          int
		body          string
		chunked       bool
		state, reason string
	}{
		{"responding", 200, "join page", false, "responding", "observed"},
		{"redirect", 302, "redirect", false, "unexpected_response", "unexpected_status"},
		{"unauthorized", 401, "denied", false, "unexpected_response", "unexpected_status"},
		{"server error", 503, "unavailable", false, "unexpected_response", "unexpected_status"},
		{"other success", 204, "", false, "unexpected_response", "unexpected_status"},
		{"exact body limit", 200, strings.Repeat("x", MaxResponseBytes), false, "responding", "observed"},
		{"oversized known length", 200, strings.Repeat("x", MaxResponseBytes+1), false, "unexpected_response", "response_too_large"},
		{"oversized streamed", 200, strings.Repeat("x", MaxResponseBytes+1), true, "unexpected_response", "response_too_large"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var redirects atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/join" {
					redirects.Add(1)
				}
				if r.Method != "GET" {
					t.Errorf("method=%s", r.Method)
				}
				w.Header().Set("Location", "/should-not-follow")
				if test.chunked {
					w.WriteHeader(test.code)
					w.(http.Flusher).Flush()
				} else {
					w.Header().Set("Content-Length", fmt.Sprint(len(test.body)))
					w.WriteHeader(test.code)
				}
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			probe := trustedProbe(server)
			result := probe.Probe(context.Background(), server.URL+"/join")
			if result.State != test.state || result.Reason != test.reason || result.HTTPStatus != test.code {
				t.Fatalf("result=%+v", result)
			}
			if redirects.Load() != 0 {
				t.Fatal("redirect followed")
			}
		})
	}
}

func TestTLSVerification(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("untrusted TLS request reached handler") }))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	result := NewHTTPProbe().Probe(context.Background(), server.URL+"/join")
	if result.State != "unreachable" || result.Reason != "tls_error" || result.HTTPStatus != 0 {
		t.Fatalf("result=%+v", result)
	}
}

func TestHTTPTimeoutAndCanceledContext(t *testing.T) {
	for _, bodyStarted := range []bool{false, true} {
		t.Run(fmt.Sprint("body started ", bodyStarted), func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if bodyStarted {
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
			defer cancel()
			result := trustedProbe(server).Probe(ctx, server.URL+"/join")
			if result.Reason != "timeout" {
				t.Fatalf("result=%+v", result)
			}
			if bodyStarted {
				if result.State != "unknown" || result.HTTPStatus != 200 {
					t.Fatalf("result=%+v", result)
				}
			} else if result.State != "unreachable" || result.HTTPStatus != 0 {
				t.Fatalf("result=%+v", result)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := NewHTTPProbe().Probe(ctx, "https://example.invalid/join")
	if result.Reason != "canceled" || result.State != "unreachable" {
		t.Fatalf("result=%+v", result)
	}
}

func TestBrokenResponseBodyAndConnection(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = io.WriteString(w, "short")
	}))
	probe := trustedProbe(server)
	result := probe.Probe(context.Background(), server.URL+"/join")
	if result.State != "unknown" || result.Reason != "response_error" || result.HTTPStatus != 200 {
		t.Fatalf("result=%+v", result)
	}
	server.Close()
	result = probe.Probe(context.Background(), server.URL+"/join")
	if result.State != "unreachable" || result.Reason != "network_error" {
		t.Fatalf("result=%+v", result)
	}
}

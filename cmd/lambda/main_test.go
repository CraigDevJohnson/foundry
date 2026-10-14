package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func environment(overrides map[string]string) func(string) string {
	values := map[string]string{
		"FOUNDRY_AWS_REGION":  "us-west-2",
		"FOUNDRY_INSTANCE_ID": "i-0123456789abcdef0",
		"FOUNDRY_HTTPS_URL":   "https://foundry.example.com:30000/join",
	}
	for key, value := range overrides {
		values[key] = value
	}
	return func(key string) string { return values[key] }
}

// Each missing or invalid setting stops the controller before it serves, and
// the error names the setting.
func TestInvalidConfigurationStopsBeforeServing(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	for _, variable := range []string{"FOUNDRY_AWS_REGION", "FOUNDRY_INSTANCE_ID", "FOUNDRY_HTTPS_URL"} {
		handler, err := newHandler(context.Background(), environment(map[string]string{variable: ""}), log)
		if handler != nil || err == nil || !strings.Contains(err.Error(), variable) {
			t.Fatalf("%s unset: handler=%v err=%v", variable, handler, err)
		}
	}
}

// The site role is optional, but one that is not an IAM role ARN stops the
// controller too.
func TestInvalidSiteRoleStopsBeforeServing(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	for _, value := range []string{"site-dev-server", "arn:aws:iam::123456789012:user/site-dev-server", "arn:aws:sts::123456789012:assumed-role/site-dev-server/x"} {
		handler, err := newHandler(context.Background(), environment(map[string]string{"FOUNDRY_SITE_ROLE_ARN": value}), log)
		if handler != nil || err == nil || !strings.Contains(err.Error(), "FOUNDRY_SITE_ROLE_ARN") {
			t.Fatalf("%q: handler=%v err=%v", value, handler, err)
		}
	}
	if _, err := newHandler(context.Background(), environment(map[string]string{"FOUNDRY_SITE_ROLE_ARN": "arn:aws:iam::123456789012:role/site-dev-server"}), log); err != nil {
		t.Fatal(err)
	}
}

// A valid configuration builds the routes without credentials or an AWS call.
func TestValidConfigurationServesWithoutAWS(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "none")
	for _, name := range []string{"AWS_PROFILE", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN"} {
		t.Setenv(name, "")
	}
	t.Setenv("AWS_CONFIG_FILE", empty)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", empty)
	handler, err := newHandler(context.Background(), environment(nil), slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if recorder.Code != http.StatusNoContent || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %v", recorder.Code, recorder.Header())
	}
}

func TestListenAddress(t *testing.T) {
	if got := listenAddr(environment(nil)); got != "127.0.0.1:8083" {
		t.Fatalf("default address %q", got)
	}
	if got := listenAddr(environment(map[string]string{"FOUNDRY_ADDR": "127.0.0.1:9000"})); got != "127.0.0.1:9000" {
		t.Fatalf("FOUNDRY_ADDR gave %q", got)
	}
}

// The local listener answers only requests addressed to this computer, and
// refuses a browser's cross-origin POST, while curl's requests pass.
func TestLocalOnly(t *testing.T) {
	handler := localOnly(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, test := range []struct {
		name, method, host string
		header             map[string]string
		want               int
	}{
		{"curl status", http.MethodGet, "127.0.0.1:8083", nil, http.StatusNoContent},
		{"curl stop", http.MethodPost, "127.0.0.1:8083", nil, http.StatusNoContent},
		{"localhost", http.MethodPost, "LocalHost:8083", nil, http.StatusNoContent},
		{"IPv6", http.MethodGet, "[::1]:8083", nil, http.StatusNoContent},
		{"no port", http.MethodGet, "127.0.0.1", nil, http.StatusNoContent},
		{"same origin", http.MethodPost, "127.0.0.1:8083", map[string]string{"Sec-Fetch-Site": "same-origin"}, http.StatusNoContent},
		{"another domain", http.MethodGet, "rebound.example:8083", nil, http.StatusMisdirectedRequest},
		{"localhost subdomain", http.MethodGet, "localhost.rebound.example", nil, http.StatusMisdirectedRequest},
		{"empty", http.MethodGet, "", nil, http.StatusMisdirectedRequest},
		{"cross site", http.MethodPost, "127.0.0.1:8083", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"other origin", http.MethodPost, "127.0.0.1:8083", map[string]string{"Origin": "https://page.example"}, http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, "/server/stop", strings.NewReader(`{"nobody_playing":true}`))
			request.Host = test.host
			for key, value := range test.header {
				request.Header.Set(key, value)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != test.want {
				t.Fatalf("%d, want %d", recorder.Code, test.want)
			}
		})
	}
}

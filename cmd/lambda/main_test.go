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

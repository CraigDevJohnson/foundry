// Package api is the controller's HTTP interface. Only The Lobby's site calls
// it, with requests signed for the function's AWS_IAM Function URL (the-lobby
// ADR 0006, ADR 0005 here). It reports the host's status and changes nothing.
// docs/status-api.md is the contract.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/CraigDevJohnson/foundry/internal/status"
)

const (
	// ObserveTimeout bounds one GET /server observation. The site's client
	// gives up at 7 seconds and its router at 8.
	ObserveTimeout = 6 * time.Second
	// bootGrace is how long after EC2 started the host a join page that does
	// not answer still means starting rather than unresponsive.
	bootGrace = 10 * time.Minute
)

// The states GET /server reports.
const (
	Stopped      = "stopped"
	Starting     = "starting"
	Ready        = "ready"
	Stopping     = "stopping"
	Unresponsive = "unresponsive"
	Unknown      = "unknown"
)

// Observer takes one observation of the host, as status.Service does.
type Observer interface {
	Observe(context.Context) status.Report
}

// Handler serves GET /server and GET /healthz. Status and Log are required.
type Handler struct {
	Status Observer
	// JoinURL is the join page players open, the configured FOUNDRY_HTTPS_URL.
	JoinURL string
	Log     *slog.Logger
	// Timeout bounds one observation; zero means ObserveTimeout.
	Timeout time.Duration
}

// Router returns the routes. A known path asked with another method answers
// 405 with an Allow header, any other path 404, including one not in clean
// form such as //server, and every answer carries Cache-Control: no-store.
// Request bodies and headers are ignored.
func (h *Handler) Router() http.Handler {
	// A later action, such as POST /server/start, is one more line here.
	routes := []struct {
		method, path string
		handle       http.HandlerFunc
	}{
		{http.MethodGet, "/server", h.server},
		{http.MethodGet, "/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }},
	}
	mux := http.NewServeMux()
	allowed := map[string][]string{}
	for _, route := range routes {
		mux.HandleFunc(route.method+" "+route.path, route.handle)
		allowed[route.path] = append(allowed[route.path], route.method)
		if route.method == http.MethodGet {
			// A GET pattern also answers HEAD.
			allowed[route.path] = append(allowed[route.path], http.MethodHead)
		}
	}
	for p, methods := range allowed {
		allow := strings.Join(methods, ", ")
		mux.HandleFunc(p, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Allow", allow)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
		})
	}
	mux.HandleFunc("/", notFound)
	return h.logRequests(noStore(cleanOnly(mux)))
}

func notFound(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotFound, map[string]string{"code": "not_found"})
}

// cleanOnly answers 404 for a path not in clean form, such as //server or
// /server/../server, which ServeMux would otherwise redirect to the clean path
// with an HTML body. No route ends in a slash.
func cleanOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := r.URL.Path; !strings.HasPrefix(p, "/") || path.Clean(p) != p {
			notFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// serverStatus is the 200 answer to GET /server.
type serverStatus struct {
	State     string `json:"state"`
	JoinURL   string `json:"join_url"`
	StartedAt string `json:"started_at,omitempty"`
	CheckedAt string `json:"checked_at"`
	Checks    checks `json:"checks"`
}

// checks are the raw observation states, without their reasons.
type checks struct {
	Host    string `json:"host"`
	Agent   string `json:"agent"`
	Foundry string `json:"foundry"`
}

// GET /server: the host's status, or 404 no_server when EC2 has no such
// instance or it is terminated (for example mid-rebuild).
func (h *Handler) server(w http.ResponseWriter, r *http.Request) {
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = ObserveTimeout
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	report := h.Status.Observe(ctx)
	if report.Host.State == "terminated" || report.Host.State == "not_found" {
		h.logReport(r.Context(), report, "no_server")
		writeJSON(w, http.StatusNotFound, map[string]string{"code": "no_server"})
		return
	}
	state := State(report)
	h.logReport(r.Context(), report, state)
	answer := serverStatus{
		State:     state,
		JoinURL:   h.JoinURL,
		CheckedAt: report.ObservedAt.UTC().Format(time.RFC3339),
		Checks:    checks{Host: report.Host.State, Agent: report.SSM.State, Foundry: report.HTTPS.State},
	}
	if (report.Host.State == "pending" || report.Host.State == "running") && !report.Host.LaunchTime.IsZero() {
		answer.StartedAt = report.Host.LaunchTime.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, answer)
}

// State derives the reported state from one observation. Only a running host
// whose join page answered 200 is ready. A running host whose page does not
// answer is starting for bootGrace after EC2 started it, and unresponsive after
// that or when EC2 gave no start time. A host EC2 could not describe is
// unknown, never guessed. GET /server answers a missing or terminated host
// with no_server before asking for a state.
func State(report status.Report) string {
	switch report.Host.State {
	case "stopped":
		return Stopped
	case "pending":
		return Starting
	case "stopping", "shutting-down":
		return Stopping
	case "running":
		if report.HTTPS.State == "responding" {
			return Ready
		}
		launched := report.Host.LaunchTime
		if !launched.IsZero() && report.ObservedAt.Sub(launched) < bootGrace {
			return Starting
		}
		return Unresponsive
	}
	return Unknown
}

// logReport writes one line per observation, with the reasons the answer
// leaves out, so the operator can see why a check failed. A host EC2 could not
// describe is a warning. The Member is never logged for a read.
func (h *Handler) logReport(ctx context.Context, report status.Report, state string) {
	level := slog.LevelInfo
	if report.Host.State == "unknown" {
		level = slog.LevelWarn
	}
	attributes := []any{
		"state", state,
		"host", report.Host.State, "host_reason", report.Host.Reason,
		"agent", report.SSM.State, "agent_reason", report.SSM.Reason,
		"foundry", report.HTTPS.State, "foundry_reason", report.HTTPS.Reason,
	}
	if report.SSM.LastPingAt != nil {
		attributes = append(attributes, "agent_last_ping", report.SSM.LastPingAt.Format(time.RFC3339))
	}
	if report.HTTPS.HTTPStatus != 0 {
		attributes = append(attributes, "foundry_http_status", report.HTTPS.HTTPStatus)
	}
	h.Log.Log(ctx, level, "status", attributes...)
}

// logRequests writes one line per request. It logs the route pattern, not the
// path, and no headers, so nothing a caller sends reaches the logs.
func (h *Handler) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		h.Log.Info("request", "method", r.Method, "route", r.Pattern, "status", recorder.status, "ms", time.Since(start).Milliseconds())
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status, s.wroteHeader = code, true
	}
	s.ResponseWriter.WriteHeader(code)
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}

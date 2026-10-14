// Package api is the controller's HTTP interface. Only The Lobby's site calls
// it, with requests signed for the function's AWS_IAM Function URL (the-lobby
// ADR 0006, ADR 0005 here). It reports the host's status, and starts and stops
// the host on request (ADR 0006 here). docs/status-api.md is the contract.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/awslabs/aws-lambda-go-api-proxy/core"

	"github.com/CraigDevJohnson/foundry/internal/control"
	"github.com/CraigDevJohnson/foundry/internal/status"
)

const (
	// ObserveTimeout bounds one GET /server observation. The site's client
	// gives up at 7 seconds and its router at 8.
	ObserveTimeout = 6 * time.Second
	// ChangeTimeout bounds a whole start or stop: the observation, then the
	// one EC2 or Systems Manager call, inside the site's 7 seconds. A sent
	// stop's record has control.RecordTimeout beyond it.
	ChangeTimeout = 6 * time.Second
	// changeObserveTimeout is the observation's share of ChangeTimeout, which
	// leaves the change itself at least 2 seconds.
	changeObserveTimeout = 4 * time.Second
	// bootGrace is how long after EC2 started the host a join page that does
	// not answer still means starting rather than unresponsive.
	bootGrace = 10 * time.Minute
	// stopGrace is how long after a stop was sent a host whose stop succeeded
	// but which is still running counts as stopping: the stop powers the host
	// off a minute after Foundry is down. After that the state follows the
	// host again.
	stopGrace = 15 * time.Minute
	// maxConfirmation bounds the stop's request body.
	maxConfirmation = 1 << 10
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

// Changer starts the host or sends it the stop, as control.Service does.
type Changer interface {
	Start(context.Context) (string, error)
	Stop(context.Context) (string, error)
}

// Handler serves GET /server, POST /server/start, POST /server/stop and GET
// /healthz. Status, Change and Log are required.
type Handler struct {
	Status Observer
	Change Changer
	// JoinURL is the join page players open, the configured FOUNDRY_HTTPS_URL.
	JoinURL string
	// SiteSession is the start of the site role's session ARN, from
	// SiteSession. Only a caller whose ARN starts with it has the Member it
	// names in X-Lobby-Member logged as who asked. Empty trusts no caller's
	// Member.
	SiteSession string
	Log         *slog.Logger
	// Timeout bounds one observation; zero means ObserveTimeout.
	Timeout time.Duration
	// Now is the clock a change's answer is stamped with; nil means time.Now.
	Now func() time.Time
}

// Router returns the routes. A known path asked with another method answers
// 405 with an Allow header, any other path 404, including one not in clean
// form such as //server, and every answer carries Cache-Control: no-store.
// Request bodies and headers are ignored, except the stop's confirmation and,
// for a change, the Member the site names.
func (h *Handler) Router() http.Handler {
	routes := []struct {
		method, path string
		handle       http.HandlerFunc
	}{
		{http.MethodGet, "/server", h.server},
		{http.MethodPost, "/server/start", h.start},
		{http.MethodPost, "/server/stop", h.stop},
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
	Stop    string `json:"stop"`
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
	if noServer(report) {
		h.logReport(r.Context(), report, "no_server")
		writeJSON(w, http.StatusNotFound, map[string]string{"code": "no_server"})
		return
	}
	state := State(report)
	h.logReport(r.Context(), report, state)
	writeJSON(w, http.StatusOK, h.answer(report, state))
}

func noServer(report status.Report) bool {
	return report.Host.State == "terminated" || report.Host.State == "not_found"
}

func (h *Handler) answer(report status.Report, state string) serverStatus {
	answer := serverStatus{
		State:     state,
		JoinURL:   h.JoinURL,
		CheckedAt: report.ObservedAt.UTC().Format(time.RFC3339),
		Checks:    checks{Host: report.Host.State, Agent: report.SSM.State, Foundry: report.HTTPS.State, Stop: report.Stop.State},
	}
	if (report.Host.State == "pending" || report.Host.State == "running") && !report.Host.LaunchTime.IsZero() {
		answer.StartedAt = report.Host.LaunchTime.UTC().Format(time.RFC3339)
	}
	return answer
}

// State derives the reported state from one observation. A running host is
// unknown when the stop recorded on it could not be read, because one may be
// under way; it is stopping while a stop is under way, and otherwise ready
// only when its join page answered 200. A running host whose page does not answer is starting
// for bootGrace after EC2 started it, and unresponsive after that or when EC2
// gave no start time. A host EC2 could not describe is unknown, never
// guessed. GET /server answers a missing or terminated host with no_server
// before asking for a state.
func State(report status.Report) string {
	switch report.Host.State {
	case "stopped":
		return Stopped
	case "pending":
		return Starting
	case "stopping", "shutting-down":
		return Stopping
	case "running":
		if report.Stop.State == "unknown" {
			return Unknown
		}
		if stopUnderWay(report) {
			return Stopping
		}
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

// stopUnderWay says whether a stop sent since EC2 last started the host is
// still running, or succeeded less than stopGrace ago, so the host is about to
// power off.
func stopUnderWay(report status.Report) bool {
	switch report.Stop.State {
	case "in_progress":
		return true
	case "succeeded":
		return !report.Stop.RequestedAt.IsZero() && report.ObservedAt.Sub(report.Stop.RequestedAt) < stopGrace
	}
	return false
}

// observeForChange observes the host with the observation's share of a
// change's time.
func (h *Handler) observeForChange(ctx context.Context) status.Report {
	ctx, cancel := context.WithTimeout(ctx, changeObserveTimeout)
	defer cancel()
	return h.Status.Observe(ctx)
}

// POST /server/start: starts a stopped host. A host already starting or
// running is left as it is; one that is stopping, or has a stop under way,
// can't be started until it has stopped.
func (h *Handler) start(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), ChangeTimeout)
	defer cancel()
	who := h.whoAsked(r)
	report := h.observeForChange(ctx)
	state := State(report)
	switch {
	case noServer(report):
		h.logChange(ctx, slog.LevelInfo, "start", "no_server", who, report)
		writeJSON(w, http.StatusNotFound, map[string]string{"code": "no_server"})
		return
	case state == Unknown:
		h.logChange(ctx, slog.LevelWarn, "start", "status_unknown", who, report)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"code": "status_unknown"})
		return
	case state == Stopping:
		h.logChange(ctx, slog.LevelInfo, "start", "refused_stopping", who, report)
		writeJSON(w, http.StatusConflict, map[string]string{"code": "stopping"})
		return
	case report.Host.State != "stopped":
		h.logChange(ctx, slog.LevelInfo, "start", "unchanged", who, report)
		writeJSON(w, http.StatusOK, h.answer(report, state))
		return
	}
	current, err := h.Change.Start(ctx)
	if errors.Is(err, control.ErrNotStopped) {
		h.logChange(ctx, slog.LevelInfo, "start", "refused_stopping", who, report)
		writeJSON(w, http.StatusConflict, map[string]string{"code": "stopping"})
		return
	}
	if err != nil {
		h.logChange(ctx, slog.LevelError, "start", "failed", who, report, "err", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"code": "start_failed"})
		return
	}
	h.logChange(ctx, slog.LevelInfo, "start", "started", who, report, "now", current)
	// The answer is the status from here: EC2's state after the start, with
	// the checks that only a running host gets not taken. EC2 gives the new
	// start time only when the host is next described, so a host it already
	// reports running (started by someone else meanwhile) is still starting
	// until the next status says otherwise.
	started := status.Report{ObservedAt: h.now().UTC()}
	started.Host.State, started.Host.Reason = current, "observed"
	started.SSM.State, started.HTTPS.State, started.Stop.State = "not_checked", "not_checked", "not_checked"
	state = State(started)
	if current == "running" {
		state = Starting
	}
	writeJSON(w, http.StatusAccepted, h.answer(started, state))
}

// POST /server/stop: shuts Foundry down and powers off a running host, once
// the Member has confirmed nobody is playing. The controller can't see
// players, so that confirmation, {"nobody_playing": true}, is required. A
// host already stopped or stopping, or with a stop under way, is left as it
// is; one still starting is refused until it is running.
func (h *Handler) stop(w http.ResponseWriter, r *http.Request) {
	who := h.whoAsked(r)
	if !confirmed(w, r) {
		h.logAsked(r.Context(), slog.LevelInfo, "stop", "confirmation_required", who)
		writeJSON(w, http.StatusBadRequest, map[string]string{"code": "confirmation_required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), ChangeTimeout)
	defer cancel()
	report := h.observeForChange(ctx)
	state := State(report)
	switch {
	case noServer(report):
		h.logChange(ctx, slog.LevelInfo, "stop", "no_server", who, report)
		writeJSON(w, http.StatusNotFound, map[string]string{"code": "no_server"})
		return
	case state == Unknown || (report.Host.State == "running" && !stopsKnown(report)):
		// Without the stop recorded on a running host, a second one could
		// overlap the first; nothing is sent.
		h.logChange(ctx, slog.LevelWarn, "stop", "status_unknown", who, report)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"code": "status_unknown"})
		return
	case state == Stopped || state == Stopping:
		h.logChange(ctx, slog.LevelInfo, "stop", "unchanged", who, report)
		writeJSON(w, http.StatusOK, h.answer(report, state))
		return
	case report.Host.State != "running":
		h.logChange(ctx, slog.LevelInfo, "stop", "refused_starting", who, report)
		writeJSON(w, http.StatusConflict, map[string]string{"code": "starting"})
		return
	}
	command, err := h.Change.Stop(ctx)
	switch {
	case errors.Is(err, control.ErrAgentOffline):
		h.logChange(ctx, slog.LevelInfo, "stop", "refused_agent_offline", who, report)
		writeJSON(w, http.StatusConflict, map[string]string{"code": "agent_offline"})
		return
	case errors.Is(err, control.ErrStopNotRecorded):
		// The stop is under way, but until the host powers off the status
		// can't show it, nor hold off a second stop.
		h.logChange(ctx, slog.LevelError, "stop", "sent_not_recorded", who, report, "command_id", command, "err", err.Error())
	case err != nil:
		h.logChange(ctx, slog.LevelError, "stop", "failed", who, report, "err", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"code": "stop_failed"})
		return
	default:
		h.logChange(ctx, slog.LevelInfo, "stop", "sent", who, report, "command_id", command)
	}
	sent := report
	sent.ObservedAt = h.now().UTC()
	sent.Stop = status.StopObservation{Observation: status.Observation{State: "in_progress", Reason: "sent"}, RequestedAt: sent.ObservedAt}
	writeJSON(w, http.StatusAccepted, h.answer(sent, State(sent)))
}

// stopsKnown says whether the stop recorded on the host since EC2 last
// started it was read.
func stopsKnown(report status.Report) bool {
	switch report.Stop.State {
	case "none", "in_progress", "succeeded", "failed":
		return true
	}
	return false
}

// confirmed reads the stop's body: a JSON object of at most 1 KiB whose
// nobody_playing is true. Any other field is ignored.
func confirmed(w http.ResponseWriter, r *http.Request) bool {
	var body struct {
		NobodyPlaying *bool `json:"nobody_playing"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxConfirmation))
	if err := decoder.Decode(&body); err != nil || body.NobodyPlaying == nil || !*body.NobodyPlaying {
		return false
	}
	// Nothing may follow the object.
	if _, err := decoder.Token(); err != io.EOF {
		return false
	}
	return true
}

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// asker is who asked for a change: the caller AWS verified, and the Member
// the site names when that caller is the site's own role.
type asker struct {
	caller, member string
}

// whoAsked reads the caller from the Function URL's request context, which
// AWS fills in after checking the request's signature. A request without one
// (on a developer's computer) has the caller "local". The Member header is
// believed only from the site's role, and only as a plain email address.
func (h *Handler) whoAsked(r *http.Request) asker {
	who := asker{caller: "local"}
	request, ok := core.GetAPIGatewayV2ContextFromContext(r.Context())
	if !ok || request.Authorizer == nil || request.Authorizer.IAM == nil || request.Authorizer.IAM.UserARN == "" {
		return who
	}
	who.caller = request.Authorizer.IAM.UserARN
	if h.SiteSession != "" && strings.HasPrefix(who.caller, h.SiteSession) {
		who.member = member(r.Header.Get("X-Lobby-Member"))
	}
	return who
}

// member is the Member the site names, or "unreadable" for anything that is
// not a plain email address.
func member(value string) string {
	if len(value) > 254 || strings.Count(value, "@") != 1 || strings.IndexFunc(value, func(c rune) bool { return c > unicode.MaxASCII || unicode.IsSpace(c) || unicode.IsControl(c) }) >= 0 {
		return "unreadable"
	}
	return value
}

// siteRolePattern is an IAM role ARN; the role's path, if any, is not part of
// its session ARNs.
var siteRolePattern = regexp.MustCompile(`^arn:aws:iam::([0-9]{12}):role/(?:[\w+=,.@-]+/)*([\w+=,.@-]{1,64})$`)

// SiteSession turns the site role's ARN, FOUNDRY_SITE_ROLE_ARN, into the start
// of the session ARNs AWS reports for requests that role signed:
// arn:aws:sts::<account>:assumed-role/<name>/. Empty stays empty.
func SiteSession(roleARN string) (string, error) {
	if roleARN == "" {
		return "", nil
	}
	match := siteRolePattern.FindStringSubmatch(roleARN)
	if match == nil {
		return "", errors.New("FOUNDRY_SITE_ROLE_ARN must be an IAM role ARN such as arn:aws:iam::123456789012:role/site-dev-server")
	}
	return "arn:aws:sts::" + match[1] + ":assumed-role/" + match[2] + "/", nil
}

// logChange writes one line per start or stop: what happened, who asked and
// what the host looked like, so the operator can follow every change.
func (h *Handler) logChange(ctx context.Context, level slog.Level, action, outcome string, who asker, report status.Report, extra ...any) {
	attributes := []any{
		"host", report.Host.State, "host_reason", report.Host.Reason,
		"stop", report.Stop.State, "stop_reason", report.Stop.Reason,
	}
	h.logAsked(ctx, level, action, outcome, who, append(attributes, extra...)...)
}

// logAsked writes a change's log line with who asked, for a change refused
// before the host was observed too.
func (h *Handler) logAsked(ctx context.Context, level slog.Level, action, outcome string, who asker, extra ...any) {
	attributes := []any{"outcome", outcome, "caller", who.caller}
	if who.member != "" {
		attributes = append(attributes, "member", who.member)
	}
	h.Log.Log(ctx, level, action, append(attributes, extra...)...)
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
		"stop", report.Stop.State, "stop_reason", report.Stop.Reason,
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

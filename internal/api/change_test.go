package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"

	"github.com/CraigDevJohnson/foundry/internal/control"
	"github.com/CraigDevJohnson/foundry/internal/status"
)

// answeredAt is when a change's answer is stamped, after the observation.
var answeredAt = now.Add(2 * time.Second)

const siteSession = "arn:aws:sts::123456789012:assumed-role/site-dev-server/"

// changer records the starts and stops asked for and answers as told.
type changer struct {
	mu            sync.Mutex
	starts, stops int
	startState    string
	startErr      error
	stopErr       error
	deadlines     []time.Duration
}

func (c *changer) Start(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.starts++
	c.record(ctx)
	if c.startState == "" {
		c.startState = "pending"
	}
	return c.startState, c.startErr
}

func (c *changer) Stop(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stops++
	c.record(ctx)
	if c.stopErr != nil && !errors.Is(c.stopErr, control.ErrStopNotRecorded) {
		return "", c.stopErr
	}
	return "0b1c2d3e-0000-4000-8000-000000000001", c.stopErr
}

func (c *changer) record(ctx context.Context) {
	if deadline, ok := ctx.Deadline(); ok {
		c.deadlines = append(c.deadlines, time.Until(deadline))
	}
}

func (c *changer) counts() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.starts, c.stops
}

func changeServer(observer Observer, change *changer) (http.Handler, *bytes.Buffer) {
	logs := &bytes.Buffer{}
	h := &Handler{
		Status: observer, Change: change, JoinURL: joinURL, SiteSession: siteSession,
		Log: slog.New(slog.NewJSONHandler(logs, nil)),
		Now: func() time.Time { return answeredAt },
	}
	return h.Router(), logs
}

const confirmation = `{"nobody_playing":true}`

func post(h http.Handler, path, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return recorder
}

func withStop(r status.Report, state string, requested time.Time) status.Report {
	r.Stop.State, r.Stop.Reason, r.Stop.RequestedAt = state, "observed", requested
	return r
}

// The answer to a change, in the shape GET /server answers.
func decodeStatus(t *testing.T, recorder *httptest.ResponseRecorder, code int) answer {
	t.Helper()
	if recorder.Code != code {
		t.Fatalf("status %d, want %d: %s", recorder.Code, code, recorder.Body)
	}
	checkJSONHeaders(t, recorder.Header())
	var a answer
	if err := json.Unmarshal(recorder.Body.Bytes(), &a); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body, err)
	}
	if a.JoinURL != joinURL {
		t.Fatalf("join_url %q", a.JoinURL)
	}
	return a
}

func checkCode(t *testing.T, recorder *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	checkJSONHeaders(t, recorder.Header())
	if recorder.Code != status || strings.TrimSpace(recorder.Body.String()) != `{"code":"`+code+`"}` {
		t.Fatalf("%d %s, want %d %s", recorder.Code, recorder.Body, status, code)
	}
}

var (
	hourAgo   = now.Add(-time.Hour)
	recently  = now.Add(-2 * time.Minute)
	longAgo   = now.Add(-stopGrace)
	readyHost = report("running", "online", "responding", hourAgo)
)

// A stopped host is started, and the answer says it is starting, with the
// checks only a running host gets not taken.
func TestStartStartsAStoppedHost(t *testing.T) {
	change := &changer{}
	h, logs := changeServer(fixed(report("stopped", "not_checked", "not_checked", hourAgo)), change)
	a := decodeStatus(t, post(h, "/server/start", ""), http.StatusAccepted)
	want := map[string]string{"host": "pending", "agent": "not_checked", "foundry": "not_checked", "stop": "not_checked"}
	if a.State != Starting || a.StartedAt != nil || a.CheckedAt != "2026-10-13T18:03:14Z" || !reflect.DeepEqual(a.Checks, want) {
		t.Fatalf("%+v", a)
	}
	if starts, stops := change.counts(); starts != 1 || stops != 0 {
		t.Fatalf("starts=%d stops=%d", starts, stops)
	}
	if !strings.Contains(logs.String(), `"msg":"start","outcome":"started","caller":"local"`) {
		t.Errorf("logs %s", logs)
	}

	// EC2 may already report it running when someone else started it first;
	// it is still starting until the next status says otherwise.
	change = &changer{startState: "running"}
	h, _ = changeServer(fixed(report("stopped", "not_checked", "not_checked", hourAgo)), change)
	if a := decodeStatus(t, post(h, "/server/start", ""), http.StatusAccepted); a.State != Starting || a.Checks["host"] != "running" {
		t.Fatalf("%+v", a)
	}
}

// A host that is starting or running is left as it is; one stopping, or with
// a stop under way, can't be started yet.
func TestStartLeavesOtherHostsAlone(t *testing.T) {
	for _, test := range []struct {
		name   string
		report status.Report
		code   int
		answer string
	}{
		{"pending", report("pending", "not_checked", "not_checked", recently), http.StatusOK, Starting},
		{"ready", readyHost, http.StatusOK, Ready},
		{"booting", report("running", "online", "unreachable", recently), http.StatusOK, Starting},
		{"unresponsive", report("running", "online", "unreachable", hourAgo), http.StatusOK, Unresponsive},
		{"stop failed", withStop(readyHost, "failed", recently), http.StatusOK, Ready},
		{"stop succeeded long ago", withStop(report("running", "online", "unreachable", hourAgo), "succeeded", longAgo), http.StatusOK, Unresponsive},
		{"stopping", report("stopping", "not_checked", "not_checked", hourAgo), http.StatusConflict, "stopping"},
		{"shutting down", report("shutting-down", "not_checked", "not_checked", hourAgo), http.StatusConflict, "stopping"},
		{"stop under way", withStop(readyHost, "in_progress", recently), http.StatusConflict, "stopping"},
		{"stop about to power off", withStop(report("running", "online", "unreachable", hourAgo), "succeeded", recently), http.StatusConflict, "stopping"},
		{"terminated", report("terminated", "not_checked", "not_checked", hourAgo), http.StatusNotFound, "no_server"},
		{"not found", report("not_found", "not_checked", "not_checked", time.Time{}), http.StatusNotFound, "no_server"},
		{"EC2 unreadable", report("unknown", "not_checked", "not_checked", time.Time{}), http.StatusServiceUnavailable, "status_unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			change := &changer{}
			h, _ := changeServer(fixed(test.report), change)
			recorder := post(h, "/server/start", "")
			if test.code == http.StatusOK {
				if a := decodeStatus(t, recorder, test.code); a.State != test.answer {
					t.Fatalf("state %q, want %q", a.State, test.answer)
				}
			} else {
				checkCode(t, recorder, test.code, test.answer)
			}
			if starts, stops := change.counts(); starts != 0 || stops != 0 {
				t.Fatalf("starts=%d stops=%d", starts, stops)
			}
		})
	}
}

// EC2 refusing because the host is not stopped yet is a 409; anything else
// is reported without its detail, which goes to the log.
func TestStartFailures(t *testing.T) {
	stopped := fixed(report("stopped", "not_checked", "not_checked", hourAgo))
	h, _ := changeServer(stopped, &changer{startErr: control.ErrNotStopped})
	checkCode(t, post(h, "/server/start", ""), http.StatusConflict, "stopping")

	h, logs := changeServer(stopped, &changer{startErr: errors.New("sensitive provider detail")})
	checkCode(t, post(h, "/server/start", ""), http.StatusServiceUnavailable, "start_failed")
	if !strings.Contains(logs.String(), `"level":"ERROR","msg":"start","outcome":"failed"`) || !strings.Contains(logs.String(), "sensitive provider detail") {
		t.Errorf("logs %s", logs)
	}
}

// A stop needs the Member's word that nobody is playing, and nothing is
// observed or sent without it.
func TestStopNeedsTheConfirmation(t *testing.T) {
	for _, body := range []string{
		"",
		"{}",
		`{"nobody_playing":false}`,
		`{"nobody_playing":"true"}`,
		`{"nobody_playing":1}`,
		`{"nobody_playing":null}`,
		`[{"nobody_playing":true}]`,
		`{"nobody_playing":true}{"nobody_playing":true}`,
		`{"nobody_playing":true} trailing`,
		`{"nobody_playing":true,"pad":"` + strings.Repeat("x", maxConfirmation) + `"}`,
		"nobody_playing=true",
	} {
		change := &changer{}
		h, _ := changeServer(refuse(t), change)
		checkCode(t, post(h, "/server/stop", body), http.StatusBadRequest, "confirmation_required")
		if _, stops := change.counts(); stops != 0 {
			t.Fatalf("%q: stopped", body)
		}
	}
	change := &changer{}
	h, _ := changeServer(fixed(readyHost), change)
	decodeStatus(t, post(h, "/server/stop", ` {"nobody_playing": true, "note": "ignored"} `+"\n"), http.StatusAccepted)
	if _, stops := change.counts(); stops != 1 {
		t.Fatalf("stops=%d", stops)
	}
}

// A running host is sent the stop, and the answer says it is stopping.
func TestStopStopsARunningHost(t *testing.T) {
	for _, test := range []struct {
		name   string
		report status.Report
	}{
		{"ready", readyHost},
		{"booting", report("running", "online", "unreachable", recently)},
		{"unresponsive", report("running", "offline", "unreachable", hourAgo)},
		{"after a failed stop", withStop(readyHost, "failed", recently)},
		{"a stop that never powered off", withStop(report("running", "online", "unreachable", hourAgo), "succeeded", longAgo)},
	} {
		t.Run(test.name, func(t *testing.T) {
			change := &changer{}
			h, logs := changeServer(fixed(test.report), change)
			a := decodeStatus(t, post(h, "/server/stop", confirmation), http.StatusAccepted)
			if a.State != Stopping || a.Checks["stop"] != "in_progress" || a.Checks["host"] != "running" || a.CheckedAt != "2026-10-13T18:03:14Z" {
				t.Fatalf("%+v", a)
			}
			if starts, stops := change.counts(); starts != 0 || stops != 1 {
				t.Fatalf("starts=%d stops=%d", starts, stops)
			}
			if !strings.Contains(logs.String(), `"msg":"stop","outcome":"sent","caller":"local"`) || !strings.Contains(logs.String(), `"command_id":"0b1c2d3e-0000-4000-8000-000000000001"`) {
				t.Errorf("logs %s", logs)
			}
		})
	}
}

// A host already stopped or stopping, or with a stop under way, is left as it
// is; one still starting is refused; and with the stops unreadable nothing is
// sent, so two stops never overlap.
func TestStopLeavesOtherHostsAlone(t *testing.T) {
	for _, test := range []struct {
		name   string
		report status.Report
		code   int
		answer string
	}{
		{"stopped", report("stopped", "not_checked", "not_checked", hourAgo), http.StatusOK, Stopped},
		{"stopping", report("stopping", "not_checked", "not_checked", hourAgo), http.StatusOK, Stopping},
		{"shutting down", report("shutting-down", "not_checked", "not_checked", hourAgo), http.StatusOK, Stopping},
		{"stop under way", withStop(readyHost, "in_progress", recently), http.StatusOK, Stopping},
		{"stop about to power off", withStop(report("running", "online", "unreachable", hourAgo), "succeeded", recently), http.StatusOK, Stopping},
		{"pending", report("pending", "not_checked", "not_checked", recently), http.StatusConflict, "starting"},
		{"stops unreadable", withStop(readyHost, "unknown", time.Time{}), http.StatusServiceUnavailable, "status_unknown"},
		{"no start time", report("running", "online", "responding", time.Time{}), http.StatusServiceUnavailable, "status_unknown"},
		{"EC2 unreadable", report("unknown", "not_checked", "not_checked", time.Time{}), http.StatusServiceUnavailable, "status_unknown"},
		{"terminated", report("terminated", "not_checked", "not_checked", hourAgo), http.StatusNotFound, "no_server"},
	} {
		t.Run(test.name, func(t *testing.T) {
			change := &changer{}
			h, _ := changeServer(fixed(test.report), change)
			recorder := post(h, "/server/stop", confirmation)
			if test.code == http.StatusOK {
				if a := decodeStatus(t, recorder, test.code); a.State != test.answer {
					t.Fatalf("state %q, want %q", a.State, test.answer)
				}
			} else {
				checkCode(t, recorder, test.code, test.answer)
			}
			if starts, stops := change.counts(); starts != 0 || stops != 0 {
				t.Fatalf("starts=%d stops=%d", starts, stops)
			}
		})
	}
}

// No connected agent to send the stop to is a 409; anything else is reported
// without its detail.
func TestStopFailures(t *testing.T) {
	h, _ := changeServer(fixed(readyHost), &changer{stopErr: control.ErrAgentOffline})
	checkCode(t, post(h, "/server/stop", confirmation), http.StatusConflict, "agent_offline")

	h, logs := changeServer(fixed(readyHost), &changer{stopErr: errors.New("sensitive provider detail")})
	recorder := post(h, "/server/stop", confirmation)
	checkCode(t, recorder, http.StatusServiceUnavailable, "stop_failed")
	if !strings.Contains(logs.String(), `"level":"ERROR","msg":"stop","outcome":"failed"`) {
		t.Errorf("logs %s", logs)
	}
}

// A stop that was sent but could not be recorded on the host is still under
// way: the answer says so, and the log says the status can't follow it.
func TestAStopNotRecordedIsStillSent(t *testing.T) {
	h, logs := changeServer(fixed(readyHost), &changer{stopErr: fmt.Errorf("%w: %w", control.ErrStopNotRecorded, errors.New("tagging refused"))})
	a := decodeStatus(t, post(h, "/server/stop", confirmation), http.StatusAccepted)
	if a.State != Stopping || a.Checks["stop"] != "in_progress" {
		t.Fatalf("%+v", a)
	}
	if !strings.Contains(logs.String(), `"level":"ERROR","msg":"stop","outcome":"sent_not_recorded"`) ||
		!strings.Contains(logs.String(), `"command_id":"0b1c2d3e-0000-4000-8000-000000000001"`) ||
		strings.Contains(logs.String(), `"outcome":"sent",`) {
		t.Errorf("logs %s", logs)
	}
}

// A change fits inside the site's 7 seconds: the observation gets at most 4,
// and the whole change at most 6.
func TestChangesAreBounded(t *testing.T) {
	var observed time.Duration
	observer := observeFunc(func(ctx context.Context) status.Report {
		deadline, _ := ctx.Deadline()
		observed = time.Until(deadline)
		return report("stopped", "not_checked", "not_checked", hourAgo)
	})
	change := &changer{}
	h, _ := changeServer(observer, change)
	post(h, "/server/start", "")
	if observed > changeObserveTimeout || observed < changeObserveTimeout-time.Second {
		t.Fatalf("observation had %v", observed)
	}
	if len(change.deadlines) != 1 || change.deadlines[0] > ChangeTimeout || change.deadlines[0] < ChangeTimeout-time.Second {
		t.Fatalf("change had %v", change.deadlines)
	}
}

// Who asked is the caller AWS verified. The Member the site names is logged
// only when that caller is the site's own role, and only as a plain address.
func TestWhoAskedIsLogged(t *testing.T) {
	event := func(caller, member string) events.APIGatewayV2HTTPRequest {
		e := events.APIGatewayV2HTTPRequest{
			Version: "2.0",
			RawPath: "/server/stop",
			Body:    confirmation,
			Headers: map[string]string{"content-type": "application/json"},
			RequestContext: events.APIGatewayV2HTTPRequestContext{
				HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{Method: http.MethodPost, Path: "/server/stop"},
			},
		}
		if member != "" {
			e.Headers["x-lobby-member"] = member
		}
		if caller != "" {
			e.RequestContext.Authorizer = &events.APIGatewayV2HTTPRequestContextAuthorizerDescription{
				IAM: &events.APIGatewayV2HTTPRequestContextAuthorizerIAMDescription{UserARN: caller},
			}
		}
		return e
	}
	for _, test := range []struct {
		name, caller, member string
		want, never          []string
	}{
		{"the site", siteSession + "1760400000000", "member@example.com",
			[]string{`"caller":"` + siteSession + `1760400000000"`, `"member":"member@example.com"`}, nil},
		{"the site with a strange header", siteSession + "1", "two@at@example.com",
			[]string{`"member":"unreadable"`}, []string{"two@at"}},
		{"the site without a Member", siteSession + "1", "",
			[]string{`"member":"unreadable"`}, nil},
		{"an operator", "arn:aws:sts::123456789012:assumed-role/WorkloadsAdmin/craig", "member@example.com",
			[]string{`"caller":"arn:aws:sts::123456789012:assumed-role/WorkloadsAdmin/craig"`}, []string{"member@example.com", `"member"`}},
		{"another environment's site", "arn:aws:sts::123456789012:assumed-role/site-prod-server/1", "member@example.com",
			nil, []string{"member@example.com"}},
		{"a look-alike role", "arn:aws:sts::123456789012:assumed-role/site-dev-server-copy/1", "member@example.com",
			nil, []string{"member@example.com"}},
		{"unsigned", "", "member@example.com",
			[]string{`"caller":"local"`}, []string{"member@example.com"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			h, logs := changeServer(fixed(readyHost), &changer{})
			response, err := httpadapter.NewV2(h).ProxyWithContext(context.Background(), event(test.caller, test.member))
			if err != nil || response.StatusCode != http.StatusAccepted {
				t.Fatalf("%v %d %s", err, response.StatusCode, response.Body)
			}
			for _, want := range test.want {
				if !strings.Contains(logs.String(), want) {
					t.Errorf("logs lack %s: %s", want, logs)
				}
			}
			for _, never := range test.never {
				if strings.Contains(logs.String(), never) {
					t.Errorf("logs carry %s: %s", never, logs)
				}
			}
		})
	}

	// A status read never logs who asked.
	h, logs := changeServer(fixed(readyHost), &changer{})
	read := event(siteSession+"1", "member@example.com")
	read.RawPath, read.RequestContext.HTTP.Method, read.RequestContext.HTTP.Path = "/server", http.MethodGet, "/server"
	if _, err := httpadapter.NewV2(h).ProxyWithContext(context.Background(), read); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "member@example.com") || strings.Contains(logs.String(), siteSession) {
		t.Errorf("a read logged who asked: %s", logs)
	}
}

func TestSiteSession(t *testing.T) {
	for role, want := range map[string]string{
		"": "",
		"arn:aws:iam::123456789012:role/site-dev-server":               "arn:aws:sts::123456789012:assumed-role/site-dev-server/",
		"arn:aws:iam::123456789012:role/service/site-prod-server":      "arn:aws:sts::123456789012:assumed-role/site-prod-server/",
		"arn:aws:iam::123456789012:role/a/b/site-dev-server@x.y=z,1+2": "arn:aws:sts::123456789012:assumed-role/site-dev-server@x.y=z,1+2/",
	} {
		if got, err := SiteSession(role); err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", role, got, err, want)
		}
	}
	for _, role := range []string{
		"site-dev-server",
		"arn:aws:iam::123456789012:user/site-dev-server",
		"arn:aws:iam::12345678901:role/site-dev-server",
		"arn:aws:iam::123456789012:role/",
		"arn:aws:sts::123456789012:assumed-role/site-dev-server/1",
		"arn:aws:iam::123456789012:role/site dev",
	} {
		if got, err := SiteSession(role); err == nil {
			t.Errorf("%q accepted as %q", role, got)
		}
	}
}

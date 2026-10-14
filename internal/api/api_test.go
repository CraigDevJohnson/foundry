package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/smithy-go"
	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"

	"github.com/CraigDevJohnson/foundry/internal/status"
)

const (
	joinURL    = "https://foundry.example.com:30000/join"
	instanceID = "i-0123456789abcdef0"
)

// now is when the fake observations finish.
var now = time.Date(2026, 10, 13, 18, 3, 12, 0, time.UTC)

type observeFunc func(context.Context) status.Report

func (f observeFunc) Observe(ctx context.Context) status.Report { return f(ctx) }

// fixed always reports the same observation.
func fixed(report status.Report) Observer {
	return observeFunc(func(context.Context) status.Report { return report })
}

// refuse fails the test if anything observes the host.
func refuse(t *testing.T) Observer {
	return observeFunc(func(context.Context) status.Report {
		t.Error("the host was observed")
		return status.Report{}
	})
}

// report is one observation. A running host has had its stops read and none
// found; any other host has not had them read.
func report(host, agent, foundry string, launched time.Time) status.Report {
	r := status.Report{ObservedAt: now}
	r.Host.State, r.Host.Reason, r.Host.LaunchTime = host, "observed", launched
	r.SSM.State, r.SSM.Reason = agent, "observed"
	r.HTTPS.State, r.HTTPS.Reason = foundry, "observed"
	r.Stop.State, r.Stop.Reason = stopFor(host, launched), "observed"
	return r
}

func stopFor(host string, launched time.Time) string {
	if host == "running" {
		return "none"
	}
	return "not_checked"
}

// unchanged fails the test if anything starts or stops the host.
type unchanged struct{ t *testing.T }

func (u unchanged) Start(context.Context) (string, error) {
	u.t.Error("the host was started")
	return "", errors.New("unexpected start")
}

func (u unchanged) Stop(context.Context) (string, error) {
	u.t.Error("the host was stopped")
	return "", errors.New("unexpected stop")
}

func newServer(observer Observer) (http.Handler, *bytes.Buffer) {
	logs := &bytes.Buffer{}
	h := &Handler{Status: observer, Change: unchanged{}, JoinURL: joinURL, Log: slog.New(slog.NewJSONHandler(logs, nil))}
	return h.Router(), logs
}

func do(h http.Handler, method, target string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, httptest.NewRequest(method, target, nil))
	return recorder
}

type answer struct {
	State     string            `json:"state"`
	JoinURL   string            `json:"join_url"`
	StartedAt *string           `json:"started_at"`
	CheckedAt string            `json:"checked_at"`
	Checks    map[string]string `json:"checks"`
}

func decode(t *testing.T, recorder *httptest.ResponseRecorder) answer {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body)
	}
	var a answer
	if err := json.Unmarshal(recorder.Body.Bytes(), &a); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body, err)
	}
	return a
}

func checkJSONHeaders(t *testing.T, header http.Header) {
	t.Helper()
	if header.Get("Content-Type") != "application/json" || header.Get("Cache-Control") != "no-store" {
		t.Errorf("headers %v", header)
	}
}

func TestStateRules(t *testing.T) {
	var missing time.Time
	tests := []struct {
		name                 string
		host, agent, foundry string
		launched             time.Time
		state                string
		started              bool
	}{
		{"stopped", "stopped", "not_checked", "not_checked", now.Add(-2 * time.Hour), Stopped, false},
		{"pending", "pending", "not_checked", "not_checked", now.Add(-5 * time.Second), Starting, true},
		{"pending without a start time", "pending", "not_checked", "not_checked", missing, Starting, false},
		{"ready just after start", "running", "online", "responding", now.Add(-30 * time.Second), Ready, true},
		{"ready long after start", "running", "online", "responding", now.Add(-5 * time.Hour), Ready, true},
		{"ready with the agent offline", "running", "offline", "responding", now.Add(-time.Hour), Ready, true},
		{"ready without a start time", "running", "unknown", "responding", missing, Ready, false},
		{"booting, no answer yet", "running", "online", "unreachable", now.Add(-time.Minute), Starting, true},
		{"one second inside the boot grace", "running", "online", "unexpected_response", now.Add(-10*time.Minute + time.Second), Starting, true},
		{"exactly at the boot grace", "running", "online", "unreachable", now.Add(-10 * time.Minute), Unresponsive, true},
		{"one second past the boot grace", "running", "online", "unknown", now.Add(-10*time.Minute - time.Second), Unresponsive, true},
		{"long past the boot grace", "running", "offline", "unexpected_response", now.Add(-3 * time.Hour), Unresponsive, true},
		{"no answer and no start time", "running", "online", "unreachable", missing, Unresponsive, false},
		{"stopping", "stopping", "not_checked", "not_checked", now.Add(-time.Hour), Stopping, false},
		{"shutting down", "shutting-down", "not_checked", "not_checked", now.Add(-time.Hour), Stopping, false},
		{"EC2 unreadable", "unknown", "not_checked", "not_checked", missing, Unknown, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h, _ := newServer(fixed(report(test.host, test.agent, test.foundry, test.launched)))
			recorder := do(h, http.MethodGet, "/server")
			checkJSONHeaders(t, recorder.Header())
			a := decode(t, recorder)
			if a.State != test.state {
				t.Errorf("state %q, want %q", a.State, test.state)
			}
			if test.started != (a.StartedAt != nil) {
				t.Errorf("started_at %v, want present=%v", a.StartedAt, test.started)
			}
			if a.StartedAt != nil && *a.StartedAt != test.launched.Format(time.RFC3339) {
				t.Errorf("started_at %q, want %q", *a.StartedAt, test.launched.Format(time.RFC3339))
			}
			want := map[string]string{"host": test.host, "agent": test.agent, "foundry": test.foundry, "stop": stopFor(test.host, test.launched)}
			if !reflect.DeepEqual(a.Checks, want) {
				t.Errorf("checks %v, want %v", a.Checks, want)
			}
		})
	}
}

// A running host whose stop could not be read, or was recorded but not checked
// because EC2 gave no start time to place it, is unknown, whatever its join
// page answers: a stop may be under way.
func TestAnUnreadableStopIsUnknown(t *testing.T) {
	for _, test := range []struct {
		stop, reason string
		launched     time.Time
	}{
		{"unknown", "timeout", now.Add(-time.Hour)},
		{"not_checked", "no_launch_time", time.Time{}},
	} {
		r := report("running", "online", "responding", test.launched)
		r.Stop.State, r.Stop.Reason = test.stop, test.reason
		h, _ := newServer(fixed(r))
		a := decode(t, do(h, http.MethodGet, "/server"))
		if a.State != Unknown || a.Checks["stop"] != test.stop {
			t.Errorf("%s: %+v", test.stop, a)
		}
	}
}

// Nothing but a running host whose join page answered 200 is ever ready.
func TestOnlyAnAnsweringJoinPageIsReady(t *testing.T) {
	for _, host := range []string{"pending", "running", "stopping", "stopped", "shutting-down", "unknown"} {
		for _, agent := range []string{"online", "offline", "unknown", "not_checked"} {
			for _, foundry := range []string{"responding", "unexpected_response", "unreachable", "unknown", "not_checked"} {
				for _, launched := range []time.Time{{}, now.Add(-time.Minute), now.Add(-time.Hour)} {
					got := State(report(host, agent, foundry, launched))
					if want := host == "running" && foundry == "responding"; (got == Ready) != want {
						t.Errorf("host=%s agent=%s foundry=%s launched=%v: %s", host, agent, foundry, launched, got)
					}
				}
			}
		}
	}
}

func TestNoServer(t *testing.T) {
	for _, host := range []string{"terminated", "not_found"} {
		h, logs := newServer(fixed(report(host, "not_checked", "not_checked", now.Add(-time.Hour))))
		recorder := do(h, http.MethodGet, "/server")
		checkJSONHeaders(t, recorder.Header())
		if recorder.Code != http.StatusNotFound || recorder.Body.String() != "{\"code\":\"no_server\"}\n" {
			t.Errorf("%s: %d %q", host, recorder.Code, recorder.Body)
		}
		if !strings.Contains(logs.String(), `"state":"no_server"`) {
			t.Errorf("%s: logs %s", host, logs)
		}
	}
}

// The answer has exactly the contract's fields, times in UTC RFC 3339.
func TestAnswerShape(t *testing.T) {
	local := time.FixedZone("local", -7*3600)
	r := report("running", "online", "responding", time.Date(2026, 10, 13, 11, 0, 0, 0, local))
	r.ObservedAt = time.Date(2026, 10, 13, 11, 3, 12, 987654321, local)
	h, _ := newServer(fixed(r))
	recorder := do(h, http.MethodGet, "/server")
	var body map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for key := range body {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, []string{"checked_at", "checks", "join_url", "started_at", "state"}) {
		t.Fatalf("keys %v", keys)
	}
	a := decode(t, recorder)
	if a.JoinURL != joinURL || a.CheckedAt != "2026-10-13T18:03:12Z" || a.StartedAt == nil || *a.StartedAt != "2026-10-13T18:00:00Z" {
		t.Fatalf("%+v body=%s", a, recorder.Body)
	}
}

type ec2Func func(context.Context) (*ec2.DescribeInstancesOutput, error)

func (f ec2Func) DescribeInstances(ctx context.Context, _ *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	return f(ctx)
}

type ssmFunc func(context.Context) (*ssm.DescribeInstanceInformationOutput, error)

func (f ssmFunc) DescribeInstanceInformation(ctx context.Context, _ *ssm.DescribeInstanceInformationInput, _ ...func(*ssm.Options)) (*ssm.DescribeInstanceInformationOutput, error) {
	return f(ctx)
}

type probeFunc func(context.Context) status.HTTPSObservation

func (f probeFunc) Probe(ctx context.Context, _ string) status.HTTPSObservation { return f(ctx) }

// running is EC2's record of the host, with the addresses the answer must
// never carry.
func running(launched time.Time) *ec2.DescribeInstancesOutput {
	return &ec2.DescribeInstancesOutput{Reservations: []ec2types.Reservation{{Instances: []ec2types.Instance{{
		InstanceId:       aws.String(instanceID),
		State:            &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning},
		LaunchTime:       aws.Time(launched),
		PublicIpAddress:  aws.String("52.27.248.145"),
		PrivateIpAddress: aws.String("172.31.40.7"),
	}}}}}
}

// noStops answers for a host with no stop recorded on its tags, so it is
// never asked.
type noStops struct{}

func (noStops) GetCommandInvocation(context.Context, *ssm.GetCommandInvocationInput, ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error) {
	return nil, errors.New("no stop is recorded on the host")
}

// service is the real observation engine over fake providers.
func service(ec2Reader status.EC2Reader, ssmReader status.SSMReader, prober status.HTTPSProber) *status.Service {
	return &status.Service{
		Config: status.Config{Region: "us-west-2", InstanceID: instanceID, HTTPSURL: joinURL},
		EC2:    ec2Reader, SSM: ssmReader, HTTPS: prober, Commands: noStops{},
		Now: func() time.Time { return now },
	}
}

// Reasons, provider errors, page content, instance IDs and addresses stay out
// of every answer; the reasons go to the log for the operator.
func TestAnswersLeakNothing(t *testing.T) {
	secret := "sensitive provider detail"
	leaks := []string{instanceID, "52.27.248.145", "172.31.40.7", secret, "reason", "observed", "access_denied", "provider_error", "unexpected_status", "http_status", "302", "license"}
	tests := []struct {
		name     string
		observer Observer
		code     int
	}{
		{"EC2 denied", service(
			ec2Func(func(context.Context) (*ec2.DescribeInstancesOutput, error) {
				return nil, &smithy.GenericAPIError{Code: "UnauthorizedOperation", Message: secret + " " + instanceID}
			}), nil, nil), http.StatusOK},
		{"EC2 failed", service(
			ec2Func(func(context.Context) (*ec2.DescribeInstancesOutput, error) { return nil, errors.New(secret) }), nil, nil), http.StatusOK},
		{"agent denied and page redirected", service(
			ec2Func(func(context.Context) (*ec2.DescribeInstancesOutput, error) { return running(now.Add(-time.Hour)), nil }),
			ssmFunc(func(context.Context) (*ssm.DescribeInstanceInformationOutput, error) {
				return nil, &smithy.GenericAPIError{Code: "AccessDeniedException", Message: secret}
			}),
			probeFunc(func(context.Context) status.HTTPSObservation {
				return status.HTTPSObservation{Observation: status.Observation{State: "unexpected_response", Reason: "unexpected_status"}, HTTPStatus: 302}
			})), http.StatusOK},
		{"not found", service(
			ec2Func(func(context.Context) (*ec2.DescribeInstancesOutput, error) {
				return nil, &smithy.GenericAPIError{Code: "InvalidInstanceID.NotFound", Message: secret + " " + instanceID}
			}), nil, nil), http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h, logs := newServer(test.observer)
			recorder := do(h, http.MethodGet, "/server")
			if recorder.Code != test.code {
				t.Fatalf("%d %s", recorder.Code, recorder.Body)
			}
			for _, leak := range leaks {
				if strings.Contains(recorder.Body.String(), leak) {
					t.Errorf("answer carries %q: %s", leak, recorder.Body)
				}
			}
			if !strings.Contains(logs.String(), `"host_reason"`) || strings.Contains(logs.String(), secret) {
				t.Errorf("logs %s", logs)
			}
		})
	}
}

func TestLogsCarryTheReasons(t *testing.T) {
	ping := now.Add(-time.Minute)
	r := report("running", "online", "unexpected_response", now.Add(-time.Hour))
	r.SSM.LastPingAt = &ping
	r.HTTPS.Reason, r.HTTPS.HTTPStatus = "unexpected_status", 302
	h, logs := newServer(fixed(r))
	do(h, http.MethodGet, "/server")
	for _, want := range []string{
		`"level":"INFO","msg":"status","state":"unresponsive","host":"running","host_reason":"observed"`,
		`"foundry":"unexpected_response","foundry_reason":"unexpected_status"`,
		`"foundry_http_status":302`,
		`"agent_last_ping":"2026-10-13T18:02:12Z"`,
		`"msg":"request","method":"GET","route":"GET /server","status":200`,
	} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs lack %s: %s", want, logs)
		}
	}

	r = report("unknown", "not_checked", "not_checked", time.Time{})
	r.Host.Reason, r.SSM.Reason, r.HTTPS.Reason = "access_denied", "host_unknown", "host_unknown"
	h, logs = newServer(fixed(r))
	do(h, http.MethodGet, "/server")
	if !strings.Contains(logs.String(), `"level":"WARN","msg":"status","state":"unknown","host":"unknown","host_reason":"access_denied"`) {
		t.Errorf("an unreadable host is not a warning: %s", logs)
	}
}

// The Member header and any body change nothing and are never logged.
func TestRequestBodiesAndHeadersAreIgnored(t *testing.T) {
	h, logs := newServer(fixed(report("running", "online", "responding", now.Add(-time.Hour))))
	plain := do(h, http.MethodGet, "/server").Body.String()
	request := httptest.NewRequest(http.MethodGet, "/server?instance=i-fffffffffffffffff", strings.NewReader(`{"instance_id":"i-fffffffffffffffff"}`))
	request.Header.Set("X-Lobby-Member", "member@example.com")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, request)
	if recorder.Body.String() != plain {
		t.Fatalf("%s != %s", recorder.Body, plain)
	}
	if strings.Contains(logs.String(), "member@example.com") || strings.Contains(logs.String(), "i-fffffffffffffffff") {
		t.Fatalf("logs %s", logs)
	}
}

func TestRoutes(t *testing.T) {
	h, _ := newServer(refuse(t))
	for _, test := range []struct {
		method, path string
		code         int
		body, allow  string
	}{
		{http.MethodGet, "/healthz", http.StatusNoContent, "", ""},
		{http.MethodHead, "/healthz", http.StatusNoContent, "", ""},
		{http.MethodPost, "/healthz", http.StatusMethodNotAllowed, `{"code":"method_not_allowed"}`, "GET, HEAD"},
		{http.MethodPost, "/server", http.StatusMethodNotAllowed, `{"code":"method_not_allowed"}`, "GET, HEAD"},
		{http.MethodDelete, "/server", http.StatusMethodNotAllowed, `{"code":"method_not_allowed"}`, "GET, HEAD"},
		{http.MethodGet, "/", http.StatusNotFound, `{"code":"not_found"}`, ""},
		{http.MethodGet, "/nowhere", http.StatusNotFound, `{"code":"not_found"}`, ""},
		{http.MethodGet, "/server/", http.StatusNotFound, `{"code":"not_found"}`, ""},
		{http.MethodGet, "/server/start", http.StatusMethodNotAllowed, `{"code":"method_not_allowed"}`, "POST"},
		{http.MethodPut, "/server/stop", http.StatusMethodNotAllowed, `{"code":"method_not_allowed"}`, "POST"},
		{http.MethodPost, "/server/stop", http.StatusBadRequest, `{"code":"confirmation_required"}`, ""},
		{http.MethodPost, "/server/restart", http.StatusNotFound, `{"code":"not_found"}`, ""},
		{http.MethodPost, "/server/start/", http.StatusNotFound, `{"code":"not_found"}`, ""},
		{http.MethodGet, "//server", http.StatusNotFound, `{"code":"not_found"}`, ""},
		{http.MethodGet, "/server/../server", http.StatusNotFound, `{"code":"not_found"}`, ""},
		{http.MethodGet, "/./healthz", http.StatusNotFound, `{"code":"not_found"}`, ""},
	} {
		recorder := do(h, test.method, test.path)
		if recorder.Code != test.code || strings.TrimSpace(recorder.Body.String()) != test.body || recorder.Header().Get("Allow") != test.allow {
			t.Errorf("%s %s: %d %q Allow=%q", test.method, test.path, recorder.Code, recorder.Body, recorder.Header().Get("Allow"))
		}
		if recorder.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s %s: Cache-Control %q", test.method, test.path, recorder.Header().Get("Cache-Control"))
		}
		if test.body != "" && recorder.Header().Get("Content-Type") != "application/json" {
			t.Errorf("%s %s: Content-Type %q", test.method, test.path, recorder.Header().Get("Content-Type"))
		}
	}
}

// The observation gets at most 6 seconds, or less when the caller has less.
func TestObservationIsBoundedAtSixSeconds(t *testing.T) {
	var left time.Duration
	h, _ := newServer(observeFunc(func(ctx context.Context) status.Report {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Error("no deadline")
		}
		left = time.Until(deadline)
		return report("stopped", "not_checked", "not_checked", time.Time{})
	}))
	do(h, http.MethodGet, "/server")
	if left > ObserveTimeout || left < ObserveTimeout-time.Second {
		t.Fatalf("observation had %v", left)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/server", nil).WithContext(ctx))
	if left > time.Second {
		t.Fatalf("the caller's shorter deadline was lost: %v", left)
	}
}

// Providers that never answer still give an answer when the bound runs out,
// never a guess.
func TestSlowProvidersStillAnswerInTime(t *testing.T) {
	hang := ec2Func(func(ctx context.Context) (*ec2.DescribeInstancesOutput, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	up := ec2Func(func(context.Context) (*ec2.DescribeInstancesOutput, error) { return running(now.Add(-time.Hour)), nil })
	agentHangs := ssmFunc(func(ctx context.Context) (*ssm.DescribeInstanceInformationOutput, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	pageHangs := probeFunc(func(ctx context.Context) status.HTTPSObservation {
		<-ctx.Done()
		return status.HTTPSObservation{Observation: status.Observation{State: "unreachable", Reason: "timeout"}}
	})
	for _, test := range []struct {
		name     string
		observer Observer
		state    string
		checks   map[string]string
	}{
		{"EC2", service(hang, nil, nil), Unknown, map[string]string{"host": "unknown", "agent": "not_checked", "foundry": "not_checked", "stop": "not_checked"}},
		{"agent and page", service(up, agentHangs, pageHangs), Unresponsive, map[string]string{"host": "running", "agent": "unknown", "foundry": "unreachable", "stop": "none"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := (&Handler{Status: test.observer, Change: unchanged{t}, JoinURL: joinURL, Log: slog.New(slog.DiscardHandler), Timeout: 50 * time.Millisecond}).Router()
			start := time.Now()
			a := decode(t, do(h, http.MethodGet, "/server"))
			if elapsed := time.Since(start); elapsed > 2*time.Second {
				t.Fatalf("answered after %v", elapsed)
			}
			if a.State != test.state || !reflect.DeepEqual(a.Checks, test.checks) {
				t.Fatalf("%+v", a)
			}
		})
	}
}

// The same routes answer through the Lambda adapter main.go uses, given a
// Function URL event (payload format 2.0).
func TestLambdaAdapter(t *testing.T) {
	h, _ := newServer(fixed(report("running", "online", "responding", now.Add(-time.Hour))))
	adapter := httpadapter.NewV2(h)
	event := func(method, path string) events.APIGatewayV2HTTPRequest {
		return events.APIGatewayV2HTTPRequest{
			Version: "2.0",
			RawPath: path,
			Headers: map[string]string{"x-lobby-member": "member@example.com"},
			RequestContext: events.APIGatewayV2HTTPRequestContext{
				HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{Method: method, Path: path},
			},
		}
	}
	for _, test := range []struct {
		method, path string
		code         int
		body         string
	}{
		{http.MethodGet, "/server", http.StatusOK, `"state":"ready"`},
		{http.MethodGet, "/healthz", http.StatusNoContent, ""},
		{http.MethodPost, "/server", http.StatusMethodNotAllowed, `"code":"method_not_allowed"`},
		{http.MethodGet, "/", http.StatusNotFound, `"code":"not_found"`},
	} {
		response, err := adapter.ProxyWithContext(context.Background(), event(test.method, test.path))
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != test.code || !strings.Contains(response.Body, test.body) || response.Headers["Cache-Control"] != "no-store" {
			t.Errorf("%s %s: %d %v %s", test.method, test.path, response.StatusCode, response.Headers, response.Body)
		}
	}
}

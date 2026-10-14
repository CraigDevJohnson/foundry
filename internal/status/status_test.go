package status

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/aws/smithy-go"
)

const testInstanceID = "i-0123456789abcdef0"

var (
	testNow    = time.Date(2026, 10, 13, 18, 3, 12, 0, time.UTC)
	testLaunch = time.Date(2026, 10, 13, 11, 0, 0, 0, time.FixedZone("local", -7*3600))
)

type ec2Func func(context.Context, *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error)

func (f ec2Func) DescribeInstances(ctx context.Context, in *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	return f(ctx, in)
}

type ssmFunc func(context.Context, *ssm.DescribeInstanceInformationInput) (*ssm.DescribeInstanceInformationOutput, error)

func (f ssmFunc) DescribeInstanceInformation(ctx context.Context, in *ssm.DescribeInstanceInformationInput, _ ...func(*ssm.Options)) (*ssm.DescribeInstanceInformationOutput, error) {
	return f(ctx, in)
}

type probeFunc func(context.Context, string) HTTPSObservation

func (f probeFunc) Probe(ctx context.Context, url string) HTTPSObservation { return f(ctx, url) }

type invocationFunc func(context.Context, *ssm.GetCommandInvocationInput) (*ssm.GetCommandInvocationOutput, error)

func (f invocationFunc) GetCommandInvocation(ctx context.Context, in *ssm.GetCommandInvocationInput, _ ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error) {
	return f(ctx, in)
}

// testCommand is the stop recorded on the host in the tests.
const testCommand = "0b1c2d3e-4f5a-4b6c-8d7e-9f0a1b2c3d4e"

// withStop records a stop on the host's tags, as the controller does.
func withStop(output *ec2.DescribeInstancesOutput, command, requested string) *ec2.DescribeInstancesOutput {
	instance := &output.Reservations[0].Instances[0]
	if command != "" {
		instance.Tags = append(instance.Tags, ec2types.Tag{Key: aws.String(StopCommandTag), Value: aws.String(command)})
	}
	if requested != "" {
		instance.Tags = append(instance.Tags, ec2types.Tag{Key: aws.String(StopRequestedTag), Value: aws.String(requested)})
	}
	return output
}

// invocation is the stop's record on the host, in the given state.
func invocation(status ssmtypes.CommandInvocationStatus) *ssm.GetCommandInvocationOutput {
	return &ssm.GetCommandInvocationOutput{CommandId: aws.String(testCommand), InstanceId: aws.String(testInstanceID), DocumentName: aws.String(StopDocument), Status: status}
}

func hostOutput(state string) *ec2.DescribeInstancesOutput {
	return &ec2.DescribeInstancesOutput{Reservations: []ec2types.Reservation{{Instances: []ec2types.Instance{{
		InstanceId: aws.String(testInstanceID),
		State:      &ec2types.InstanceState{Name: ec2types.InstanceStateName(state)},
		LaunchTime: aws.Time(testLaunch),
		Tags:       []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String("foundry")}},
	}}}}}
}
func agentOutput(state string) *ssm.DescribeInstanceInformationOutput {
	return &ssm.DescribeInstanceInformationOutput{InstanceInformationList: []ssmtypes.InstanceInformation{{InstanceId: aws.String(testInstanceID), PingStatus: ssmtypes.PingStatus(state)}}}
}
func testService(t *testing.T) *Service {
	t.Helper()
	return &Service{
		Config: Config{Region: "us-west-2", InstanceID: testInstanceID, HTTPSURL: "https://example.com/join"},
		EC2: ec2Func(func(ctx context.Context, in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
			if len(in.InstanceIds) != 1 || in.InstanceIds[0] != testInstanceID || len(in.Filters) != 0 {
				t.Errorf("unscoped EC2 input: %+v", in)
			}
			checkDeadline(t, ctx)
			return hostOutput("running"), nil
		}),
		SSM: ssmFunc(func(ctx context.Context, in *ssm.DescribeInstanceInformationInput) (*ssm.DescribeInstanceInformationOutput, error) {
			if len(in.Filters) != 1 || aws.ToString(in.Filters[0].Key) != "InstanceIds" || len(in.Filters[0].Values) != 1 || in.Filters[0].Values[0] != testInstanceID {
				t.Errorf("unscoped SSM input: %+v", in)
			}
			checkDeadline(t, ctx)
			return agentOutput("Online"), nil
		}),
		HTTPS: probeFunc(func(ctx context.Context, url string) HTTPSObservation {
			if url != "https://example.com/join" {
				t.Errorf("wrong HTTPS URL: %q", url)
			}
			checkDeadline(t, ctx)
			return HTTPSObservation{Observation: Observation{State: "responding", Reason: "observed"}, HTTPStatus: 200}
		}),
		Commands: invocationFunc(func(context.Context, *ssm.GetCommandInvocationInput) (*ssm.GetCommandInvocationOutput, error) {
			t.Error("a stop read with none recorded on the host")
			return nil, nil
		}),
		Now: func() time.Time { return testNow },
	}
}

// checkStopQuery fails the test unless only the recorded stop is asked for,
// on only the host.
func checkStopQuery(t *testing.T, in *ssm.GetCommandInvocationInput) {
	t.Helper()
	if aws.ToString(in.CommandId) != testCommand || aws.ToString(in.InstanceId) != testInstanceID || in.PluginName != nil {
		t.Errorf("unscoped GetCommandInvocation input: %+v", in)
	}
}
func checkDeadline(t *testing.T, ctx context.Context) {
	t.Helper()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > ProviderTimeout {
		t.Errorf("missing or unbounded deadline %v", deadline)
	}
}

func TestNativeHostStatesAndDependentProbes(t *testing.T) {
	for _, state := range []string{"pending", "running", "shutting-down", "terminated", "stopping", "stopped"} {
		t.Run(state, func(t *testing.T) {
			s := testService(t)
			s.EC2 = ec2Func(func(context.Context, *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
				return hostOutput(state), nil
			})
			if state != "running" {
				s.SSM = ssmFunc(func(context.Context, *ssm.DescribeInstanceInformationInput) (*ssm.DescribeInstanceInformationOutput, error) {
					t.Error("SSM called for non-running host")
					return nil, nil
				})
				s.HTTPS = probeFunc(func(context.Context, string) HTTPSObservation {
					t.Error("HTTPS called for non-running host")
					return HTTPSObservation{}
				})
				s.Commands = invocationFunc(func(context.Context, *ssm.GetCommandInvocationInput) (*ssm.GetCommandInvocationOutput, error) {
					t.Error("stops read for non-running host")
					return nil, nil
				})
			}
			report := s.Observe(context.Background())
			if report.Host.State != state || report.Host.Reason != "observed" {
				t.Fatalf("host=%+v", report.Host)
			}
			if !report.Host.LaunchTime.Equal(testLaunch) || report.Host.LaunchTime.Location() != time.UTC {
				t.Fatalf("launch time=%v", report.Host.LaunchTime)
			}
			if state != "running" && (report.SSM.State != "not_checked" || report.SSM.Reason != "host_not_running" || report.HTTPS.State != "not_checked" || report.HTTPS.Reason != "host_not_running" || report.Stop.State != "not_checked" || report.Stop.Reason != "host_not_running") {
				t.Fatalf("probes: %+v %+v %+v", report.SSM, report.HTTPS, report.Stop)
			}
			if state == "running" && (report.Stop.State != "none" || report.Stop.Reason != "observed" || !report.Stop.RequestedAt.IsZero()) {
				t.Fatalf("stop: %+v", report.Stop)
			}
		})
	}
}

func TestMissingLaunchTimeStaysMissing(t *testing.T) {
	s := testService(t)
	output := withStop(hostOutput("running"), testCommand, "2026-10-13T18:01:00Z")
	output.Reservations[0].Instances[0].LaunchTime = nil
	s.EC2 = ec2Func(func(context.Context, *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		return output, nil
	})
	s.Commands = invocationFunc(func(context.Context, *ssm.GetCommandInvocationInput) (*ssm.GetCommandInvocationOutput, error) {
		t.Error("stops read without a start time to bound them")
		return nil, nil
	})
	report := s.Observe(context.Background())
	if report.Host.State != "running" || !report.Host.LaunchTime.IsZero() {
		t.Fatalf("host=%+v", report.Host)
	}
	if report.Stop.State != "not_checked" || report.Stop.Reason != "no_launch_time" {
		t.Fatalf("stop=%+v", report.Stop)
	}
}

// Without a start time, a host with no stop recorded on it has none to follow.
func TestMissingLaunchTimeWithoutAStop(t *testing.T) {
	s := testService(t)
	output := hostOutput("running")
	output.Reservations[0].Instances[0].LaunchTime = nil
	s.EC2 = ec2Func(func(context.Context, *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		return output, nil
	})
	s.Commands = invocationFunc(func(context.Context, *ssm.GetCommandInvocationInput) (*ssm.GetCommandInvocationOutput, error) {
		t.Error("stops read with none recorded")
		return nil, nil
	})
	report := s.Observe(context.Background())
	if report.Stop.State != "none" || report.Stop.Reason != "observed" {
		t.Fatalf("stop=%+v", report.Stop)
	}
}

func TestObservedAtIsWhenTheObservationFinished(t *testing.T) {
	s := testService(t)
	finished := false
	s.HTTPS = probeFunc(func(context.Context, string) HTTPSObservation {
		finished = true
		return HTTPSObservation{Observation: Observation{State: "responding", Reason: "observed"}, HTTPStatus: 200}
	})
	local := time.Date(2026, 10, 13, 11, 3, 12, 0, time.FixedZone("local", -7*3600))
	s.Now = func() time.Time {
		if !finished {
			t.Error("clock read before the probes finished")
		}
		return local
	}
	report := s.Observe(context.Background())
	if !report.ObservedAt.Equal(local) || report.ObservedAt.Location() != time.UTC {
		t.Fatalf("observed at %v", report.ObservedAt)
	}
	s.Now = nil
	before := time.Now()
	if report := s.Observe(context.Background()); report.ObservedAt.Before(before) || report.ObservedAt.After(time.Now()) {
		t.Fatalf("default clock gave %v", report.ObservedAt)
	}
}

func TestUncertainHostRecords(t *testing.T) {
	duplicate := hostOutput("running")
	duplicate.Reservations = append(duplicate.Reservations, duplicate.Reservations[0])
	foreign := hostOutput("running")
	foreign.Reservations[0].Instances[0].InstanceId = aws.String("i-fffffffffffffffff")
	missingState := hostOutput("running")
	missingState.Reservations[0].Instances[0].State = nil
	paginated := hostOutput("running")
	paginated.NextToken = aws.String("more")
	tests := []struct {
		name          string
		output        *ec2.DescribeInstancesOutput
		err           error
		state, reason string
	}{
		{"empty", &ec2.DescribeInstancesOutput{}, nil, "not_found", "no_record"},
		{"nil", nil, nil, "unknown", "unexpected_record"},
		{"duplicate", duplicate, nil, "unknown", "unexpected_record"},
		{"foreign", foreign, nil, "unknown", "unexpected_record"},
		{"missing state", missingState, nil, "unknown", "unexpected_record"},
		{"unexpected state", hostOutput("future-state"), nil, "unknown", "unexpected_state"},
		{"partial page", paginated, nil, "unknown", "unexpected_record"},
		{"not found error", nil, &smithy.GenericAPIError{Code: "InvalidInstanceID.NotFound"}, "not_found", "no_record"},
		{"access denied", nil, &smithy.GenericAPIError{Code: "UnauthorizedOperation", Message: "sensitive provider detail"}, "unknown", "access_denied"},
		{"unknown error", nil, errors.New("sensitive provider detail"), "unknown", "provider_error"},
		{"deadline", nil, context.DeadlineExceeded, "unknown", "timeout"},
		{"canceled", nil, context.Canceled, "unknown", "canceled"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := testService(t)
			s.EC2 = ec2Func(func(context.Context, *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
				return test.output, test.err
			})
			s.SSM = ssmFunc(func(context.Context, *ssm.DescribeInstanceInformationInput) (*ssm.DescribeInstanceInformationOutput, error) {
				t.Error("SSM called for uncertain/absent host")
				return nil, nil
			})
			s.HTTPS = probeFunc(func(context.Context, string) HTTPSObservation {
				t.Error("HTTPS called for uncertain/absent host")
				return HTTPSObservation{}
			})
			s.Commands = invocationFunc(func(context.Context, *ssm.GetCommandInvocationInput) (*ssm.GetCommandInvocationOutput, error) {
				t.Error("stops read for uncertain/absent host")
				return nil, nil
			})
			r := s.Observe(context.Background())
			if r.Host.State != test.state || r.Host.Reason != test.reason || !r.Host.LaunchTime.IsZero() {
				t.Fatalf("host=%+v", r.Host)
			}
			if r.SSM.State != "not_checked" || r.HTTPS.State != "not_checked" || r.Stop.State != "not_checked" {
				t.Fatalf("probes: %+v %+v %+v", r.SSM, r.HTTPS, r.Stop)
			}
			if test.state == "unknown" && (r.SSM.Reason != "host_unknown" || r.HTTPS.Reason != "host_unknown" || r.Stop.Reason != "host_unknown") {
				t.Fatalf("uncertain host reason: %+v %+v %+v", r.SSM, r.HTTPS, r.Stop)
			}
		})
	}
}

func TestSSMRecordsRemainIndependentFromHTTPS(t *testing.T) {
	ping := time.Date(2026, 9, 8, 12, 0, 0, 0, time.FixedZone("local", -6*3600))
	offline := agentOutput("ConnectionLost")
	offline.InstanceInformationList[0].LastPingDateTime = &ping
	duplicate := agentOutput("Online")
	duplicate.InstanceInformationList = append(duplicate.InstanceInformationList, duplicate.InstanceInformationList[0])
	foreign := agentOutput("Online")
	foreign.InstanceInformationList[0].InstanceId = aws.String("i-fffffffffffffffff")
	paginated := agentOutput("Online")
	paginated.NextToken = aws.String("more")
	tests := []struct {
		name          string
		output        *ssm.DescribeInstanceInformationOutput
		err           error
		state, reason string
	}{
		{"online", agentOutput("Online"), nil, "online", "observed"},
		{"offline", offline, nil, "offline", "observed"},
		{"inactive", agentOutput("Inactive"), nil, "offline", "observed"},
		{"empty", &ssm.DescribeInstanceInformationOutput{}, nil, "unknown", "no_record"},
		{"nil", nil, nil, "unknown", "unexpected_record"},
		{"duplicate", duplicate, nil, "unknown", "unexpected_record"},
		{"foreign", foreign, nil, "unknown", "unexpected_record"},
		{"page", paginated, nil, "unknown", "unexpected_record"},
		{"unexpected state", agentOutput("NewState"), nil, "unknown", "unexpected_state"},
		{"unknown error", nil, errors.New("private provider detail"), "unknown", "provider_error"},
		{"denied", nil, &smithy.GenericAPIError{Code: "AccessDeniedException"}, "unknown", "access_denied"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := testService(t)
			s.SSM = ssmFunc(func(context.Context, *ssm.DescribeInstanceInformationInput) (*ssm.DescribeInstanceInformationOutput, error) {
				return test.output, test.err
			})
			r := s.Observe(context.Background())
			if r.SSM.State != test.state || r.SSM.Reason != test.reason || r.HTTPS.State != "responding" {
				t.Fatalf("SSM=%+v HTTPS=%+v", r.SSM, r.HTTPS)
			}
			if test.name == "offline" && (r.SSM.LastPingAt == nil || !r.SSM.LastPingAt.Equal(ping) || r.SSM.LastPingAt.Location() != time.UTC) {
				t.Fatalf("last_ping_at=%v", r.SSM.LastPingAt)
			}
		})
	}
}

func TestIndependentProbesAndCallerDeadline(t *testing.T) {
	s := testService(t)
	httpStarted := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	deadline, _ := ctx.Deadline()
	s.SSM = ssmFunc(func(ctx context.Context, _ *ssm.DescribeInstanceInformationInput) (*ssm.DescribeInstanceInformationOutput, error) {
		if child, _ := ctx.Deadline(); !child.Equal(deadline) {
			t.Errorf("caller deadline lost: %v != %v", child, deadline)
		}
		select {
		case <-httpStarted:
		case <-ctx.Done():
			t.Error("HTTPS did not run independently")
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	s.HTTPS = probeFunc(func(ctx context.Context, _ string) HTTPSObservation {
		if child, _ := ctx.Deadline(); !child.Equal(deadline) {
			t.Errorf("caller deadline lost: %v != %v", child, deadline)
		}
		close(httpStarted)
		return HTTPSObservation{Observation: Observation{State: "responding", Reason: "observed"}, HTTPStatus: 200}
	})
	r := s.Observe(ctx)
	if r.SSM.State != "unknown" || r.SSM.Reason != "timeout" || r.HTTPS.State != "responding" {
		t.Fatalf("SSM=%+v HTTPS=%+v", r.SSM, r.HTTPS)
	}
}

// The stop recorded on the host decides, if it was sent since EC2 last
// started the host: one under way, one that succeeded and one that failed are
// told apart, and a record that does not match what was asked is never
// trusted.
func TestStopsUnderWay(t *testing.T) {
	launched := testLaunch.UTC()
	sent := launched.Add(time.Minute)       // 2m12s before testNow
	recent := launched.Add(2 * time.Minute) // 1m12s before testNow
	at := func(when time.Time) string { return when.Format(time.RFC3339) }
	otherCommand := invocation(ssmtypes.CommandInvocationStatusInProgress)
	otherCommand.CommandId = aws.String("ffffffff-4f5a-4b6c-8d7e-9f0a1b2c3d4e")
	otherInstance := invocation(ssmtypes.CommandInvocationStatusInProgress)
	otherInstance.InstanceId = aws.String("i-fffffffffffffffff")
	otherDocument := invocation(ssmtypes.CommandInvocationStatusInProgress)
	otherDocument.DocumentName = aws.String("AWS-RunShellScript")
	notYet := &smithy.GenericAPIError{Code: "InvocationDoesNotExist"}
	tests := []struct {
		name               string
		command, requested string
		output             *ssm.GetCommandInvocationOutput
		err                error
		state, reason      string
		at                 time.Time
	}{
		{"none recorded", "", "", nil, nil, "none", "observed", time.Time{}},
		{"before this start", testCommand, at(launched.Add(-time.Minute)), nil, nil, "none", "observed", time.Time{}},
		{"pending", testCommand, at(sent), invocation(ssmtypes.CommandInvocationStatusPending), nil, "in_progress", "observed", sent},
		{"in progress", testCommand, at(sent), invocation(ssmtypes.CommandInvocationStatusInProgress), nil, "in_progress", "observed", sent},
		{"delayed", testCommand, at(sent), invocation(ssmtypes.CommandInvocationStatusDelayed), nil, "in_progress", "observed", sent},
		{"cancelling", testCommand, at(sent), invocation(ssmtypes.CommandInvocationStatusCancelling), nil, "in_progress", "observed", sent},
		{"succeeded", testCommand, at(sent), invocation(ssmtypes.CommandInvocationStatusSuccess), nil, "succeeded", "observed", sent},
		{"failed", testCommand, at(sent), invocation(ssmtypes.CommandInvocationStatusFailed), nil, "failed", "observed", sent},
		{"timed out", testCommand, at(sent), invocation(ssmtypes.CommandInvocationStatusTimedOut), nil, "failed", "observed", sent},
		{"cancelled", testCommand, at(sent), invocation(ssmtypes.CommandInvocationStatusCancelled), nil, "failed", "observed", sent},
		{"unexpected status", testCommand, at(sent), invocation("Exploded"), nil, "unknown", "unexpected_state", sent},
		{"not recorded yet", testCommand, at(recent), nil, notYet, "in_progress", "not_recorded_yet", recent},
		{"never recorded", testCommand, at(sent), nil, notYet, "unknown", "no_record", sent},
		{"only the command", testCommand, "", nil, nil, "unknown", "unexpected_record", time.Time{}},
		{"only the time", "", at(sent), nil, nil, "unknown", "unexpected_record", time.Time{}},
		{"not a command ID", "c-180100", at(sent), nil, nil, "unknown", "unexpected_record", time.Time{}},
		{"not a time", testCommand, "yesterday", nil, nil, "unknown", "unexpected_record", time.Time{}},
		{"another command", testCommand, at(sent), otherCommand, nil, "unknown", "unexpected_record", sent},
		{"another instance", testCommand, at(sent), otherInstance, nil, "unknown", "unexpected_record", sent},
		{"another document", testCommand, at(sent), otherDocument, nil, "unknown", "unexpected_record", sent},
		{"nil", testCommand, at(sent), nil, nil, "unknown", "unexpected_record", sent},
		{"denied", testCommand, at(sent), nil, &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "sensitive provider detail"}, "unknown", "access_denied", sent},
		{"failed call", testCommand, at(sent), nil, errors.New("sensitive provider detail"), "unknown", "provider_error", sent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := testService(t)
			s.EC2 = ec2Func(func(context.Context, *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
				return withStop(hostOutput("running"), test.command, test.requested), nil
			})
			asked := false
			s.Commands = invocationFunc(func(ctx context.Context, in *ssm.GetCommandInvocationInput) (*ssm.GetCommandInvocationOutput, error) {
				asked = true
				checkStopQuery(t, in)
				checkDeadline(t, ctx)
				return test.output, test.err
			})
			r := s.Observe(context.Background())
			if r.Stop.State != test.state || r.Stop.Reason != test.reason || !r.Stop.RequestedAt.Equal(test.at) {
				t.Fatalf("stop=%+v", r.Stop)
			}
			if !test.at.IsZero() && r.Stop.RequestedAt.Location() != time.UTC {
				t.Fatalf("requested at %v", r.Stop.RequestedAt)
			}
			if asked != !test.at.IsZero() {
				t.Fatalf("asked Systems Manager: %v", asked)
			}
			if r.SSM.State != "online" || r.HTTPS.State != "responding" {
				t.Fatalf("the stop check changed the others: %+v %+v", r.SSM, r.HTTPS)
			}
		})
	}
}

// A stop whose record Systems Manager no longer has, once no stop could still
// be running, is over: it neither holds the host in stopping nor blocks
// another stop.
func TestAnExpiredStopIsOver(t *testing.T) {
	for _, test := range []struct {
		name          string
		age           time.Duration
		state, reason string
	}{
		{"within the lifetime", stopLifetime - time.Second, "unknown", "no_record"},
		{"at the lifetime", stopLifetime, "none", "expired"},
		{"a month on", 31 * 24 * time.Hour, "none", "expired"},
	} {
		t.Run(test.name, func(t *testing.T) {
			requested := testNow.Add(-test.age)
			s := testService(t)
			s.EC2 = ec2Func(func(context.Context, *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
				output := withStop(hostOutput("running"), testCommand, requested.Format(time.RFC3339))
				output.Reservations[0].Instances[0].LaunchTime = aws.Time(requested.Add(-time.Hour))
				return output, nil
			})
			s.Commands = invocationFunc(func(context.Context, *ssm.GetCommandInvocationInput) (*ssm.GetCommandInvocationOutput, error) {
				return nil, &smithy.GenericAPIError{Code: "InvocationDoesNotExist"}
			})
			r := s.Observe(context.Background())
			if r.Stop.State != test.state || r.Stop.Reason != test.reason || !r.Stop.RequestedAt.Equal(requested) {
				t.Fatalf("stop=%+v", r.Stop)
			}
		})
	}
}

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

func hostOutput(state string) *ec2.DescribeInstancesOutput {
	return &ec2.DescribeInstancesOutput{Reservations: []ec2types.Reservation{{Instances: []ec2types.Instance{{
		InstanceId: aws.String(testInstanceID),
		State:      &ec2types.InstanceState{Name: ec2types.InstanceStateName(state)},
		LaunchTime: aws.Time(testLaunch),
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
		Now: func() time.Time { return testNow },
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
			}
			report := s.Observe(context.Background())
			if report.Host.State != state || report.Host.Reason != "observed" {
				t.Fatalf("host=%+v", report.Host)
			}
			if !report.Host.LaunchTime.Equal(testLaunch) || report.Host.LaunchTime.Location() != time.UTC {
				t.Fatalf("launch time=%v", report.Host.LaunchTime)
			}
			if state != "running" && (report.SSM.State != "not_checked" || report.SSM.Reason != "host_not_running" || report.HTTPS.State != "not_checked" || report.HTTPS.Reason != "host_not_running") {
				t.Fatalf("probes: %+v %+v", report.SSM, report.HTTPS)
			}
		})
	}
}

func TestMissingLaunchTimeStaysMissing(t *testing.T) {
	s := testService(t)
	output := hostOutput("running")
	output.Reservations[0].Instances[0].LaunchTime = nil
	s.EC2 = ec2Func(func(context.Context, *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		return output, nil
	})
	if report := s.Observe(context.Background()); report.Host.State != "running" || !report.Host.LaunchTime.IsZero() {
		t.Fatalf("host=%+v", report.Host)
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
			r := s.Observe(context.Background())
			if r.Host.State != test.state || r.Host.Reason != test.reason || !r.Host.LaunchTime.IsZero() {
				t.Fatalf("host=%+v", r.Host)
			}
			if r.SSM.State != "not_checked" || r.HTTPS.State != "not_checked" {
				t.Fatalf("probes: %+v %+v", r.SSM, r.HTTPS)
			}
			if test.state == "unknown" && (r.SSM.Reason != "host_unknown" || r.HTTPS.Reason != "host_unknown") {
				t.Fatalf("uncertain host reason: %+v %+v", r.SSM, r.HTTPS)
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

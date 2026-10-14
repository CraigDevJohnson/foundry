package control

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

	"github.com/CraigDevJohnson/foundry/internal/status"
)

const instanceID = "i-0123456789abcdef0"

type startFunc func(context.Context, *ec2.StartInstancesInput) (*ec2.StartInstancesOutput, error)

// StartInstances fails the start unless it is asked with retries turned off.
func (f startFunc) StartInstances(ctx context.Context, in *ec2.StartInstancesInput, optFns ...func(*ec2.Options)) (*ec2.StartInstancesOutput, error) {
	options := ec2.Options{RetryMaxAttempts: 2}
	for _, fn := range optFns {
		fn(&options)
	}
	if options.RetryMaxAttempts != 1 {
		return nil, errors.New("the start could be asked more than once")
	}
	return f(ctx, in)
}

func (f startFunc) DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	panic("only a start EC2 refused for the host's state may look at the host again")
}

func (f startFunc) CreateTags(context.Context, *ec2.CreateTagsInput, ...func(*ec2.Options)) (*ec2.CreateTagsOutput, error) {
	return nil, errors.New("a start must not tag the host")
}

// refusedStart is EC2 refusing the start for the host's state, then
// describing the host with describe.
type refusedStart func(context.Context, *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error)

func (f refusedStart) StartInstances(context.Context, *ec2.StartInstancesInput, ...func(*ec2.Options)) (*ec2.StartInstancesOutput, error) {
	return nil, &smithy.GenericAPIError{Code: "IncorrectInstanceState"}
}

func (f refusedStart) DescribeInstances(ctx context.Context, in *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	return f(ctx, in)
}

func (f refusedStart) CreateTags(context.Context, *ec2.CreateTagsInput, ...func(*ec2.Options)) (*ec2.CreateTagsOutput, error) {
	return nil, errors.New("a start must not tag the host")
}

type tagFunc func(context.Context, *ec2.CreateTagsInput) (*ec2.CreateTagsOutput, error)

func (f tagFunc) StartInstances(context.Context, *ec2.StartInstancesInput, ...func(*ec2.Options)) (*ec2.StartInstancesOutput, error) {
	return nil, errors.New("a stop must not start the host")
}

func (f tagFunc) DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	return nil, errors.New("a stop must not describe the host")
}

func (f tagFunc) CreateTags(ctx context.Context, in *ec2.CreateTagsInput, _ ...func(*ec2.Options)) (*ec2.CreateTagsOutput, error) {
	return f(ctx, in)
}

const commandID = "0b1c2d3e-4f5a-4b6c-8d7e-9f0a1b2c3d4e"

var sentAt = time.Date(2026, 10, 14, 2, 30, 5, 123, time.FixedZone("local", -7*3600))

type sendFunc func(context.Context, *ssm.SendCommandInput) (*ssm.SendCommandOutput, error)

// SendCommand fails the stop unless it is sent with retries turned off.
func (f sendFunc) SendCommand(ctx context.Context, in *ssm.SendCommandInput, optFns ...func(*ssm.Options)) (*ssm.SendCommandOutput, error) {
	options := ssm.Options{RetryMaxAttempts: 2}
	for _, fn := range optFns {
		fn(&options)
	}
	if options.RetryMaxAttempts != 1 {
		return nil, errors.New("the stop could be sent more than once")
	}
	return f(ctx, in)
}

func checkDeadline(t *testing.T, ctx context.Context) {
	t.Helper()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > status.ProviderTimeout {
		t.Errorf("missing or unbounded deadline %v", deadline)
	}
}

func starting(id string, state ec2types.InstanceStateName) *ec2.StartInstancesOutput {
	return &ec2.StartInstancesOutput{StartingInstances: []ec2types.InstanceStateChange{{
		InstanceId:   aws.String(id),
		CurrentState: &ec2types.InstanceState{Name: state},
	}}}
}

// Start asks EC2 to start only the configured host and reports the state EC2
// gives back.
func TestStart(t *testing.T) {
	s := &Service{InstanceID: instanceID, EC2: startFunc(func(ctx context.Context, in *ec2.StartInstancesInput) (*ec2.StartInstancesOutput, error) {
		if len(in.InstanceIds) != 1 || in.InstanceIds[0] != instanceID || in.AdditionalInfo != nil || in.DryRun != nil {
			t.Errorf("unscoped start: %+v", in)
		}
		checkDeadline(t, ctx)
		return starting(instanceID, ec2types.InstanceStateNamePending), nil
	})}
	if state, err := s.Start(context.Background()); state != "pending" || err != nil {
		t.Fatalf("%q %v", state, err)
	}
}

func TestStartFailures(t *testing.T) {
	foreign := starting("i-fffffffffffffffff", ec2types.InstanceStateNamePending)
	noState := starting(instanceID, ec2types.InstanceStateNamePending)
	noState.StartingInstances[0].CurrentState = nil
	two := starting(instanceID, ec2types.InstanceStateNamePending)
	two.StartingInstances = append(two.StartingInstances, two.StartingInstances[0])
	failure := errors.New("provider failure")
	for _, test := range []struct {
		name   string
		output *ec2.StartInstancesOutput
		err    error
		want   error
	}{
		{"other failure", nil, failure, failure},
		{"timed out", nil, context.DeadlineExceeded, context.DeadlineExceeded},
		{"nil", nil, nil, nil},
		{"empty", &ec2.StartInstancesOutput{}, nil, nil},
		{"foreign", foreign, nil, nil},
		{"no state", noState, nil, nil},
		{"two", two, nil, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := &Service{InstanceID: instanceID, EC2: startFunc(func(context.Context, *ec2.StartInstancesInput) (*ec2.StartInstancesOutput, error) {
				return test.output, test.err
			})}
			state, err := s.Start(context.Background())
			if state != "" || err == nil || (test.want != nil && !errors.Is(err, test.want)) {
				t.Fatalf("%q %v", state, err)
			}
		})
	}
}

func described(id string, state ec2types.InstanceStateName) *ec2.DescribeInstancesOutput {
	return &ec2.DescribeInstancesOutput{Reservations: []ec2types.Reservation{{Instances: []ec2types.Instance{{
		InstanceId: aws.String(id),
		State:      &ec2types.InstanceState{Name: state},
	}}}}}
}

// When EC2 refuses the start for the host's state, Start looks at only the
// configured host again, within the request's time. A host another start got
// to first, now starting or running, is ErrStartedElsewhere with its state;
// any other state is ErrNotStopped with that state, and a look that can't
// read the host is ErrNotStopped with what went wrong.
func TestARefusedStartLooksAgain(t *testing.T) {
	noState := described(instanceID, ec2types.InstanceStateNamePending)
	noState.Reservations[0].Instances[0].State = nil
	two := described(instanceID, ec2types.InstanceStateNamePending)
	two.Reservations[0].Instances = append(two.Reservations[0].Instances, two.Reservations[0].Instances[0])
	failure := errors.New("provider failure")
	for _, test := range []struct {
		name   string
		output *ec2.DescribeInstancesOutput
		err    error
		state  string
		want   error
		failed bool // whether the second look failed to read the host
	}{
		{"started elsewhere", described(instanceID, ec2types.InstanceStateNamePending), nil, "pending", ErrStartedElsewhere, false},
		{"already running", described(instanceID, ec2types.InstanceStateNameRunning), nil, "running", ErrStartedElsewhere, false},
		{"still stopping", described(instanceID, ec2types.InstanceStateNameStopping), nil, "stopping", ErrNotStopped, false},
		{"stopped", described(instanceID, ec2types.InstanceStateNameStopped), nil, "stopped", ErrNotStopped, false},
		{"shutting down", described(instanceID, ec2types.InstanceStateNameShuttingDown), nil, "shutting-down", ErrNotStopped, false},
		{"describe failed", nil, failure, "", failure, true},
		{"nil", nil, nil, "", ErrNotStopped, true},
		{"empty", &ec2.DescribeInstancesOutput{}, nil, "", ErrNotStopped, true},
		{"foreign", described("i-fffffffffffffffff", ec2types.InstanceStateNamePending), nil, "", ErrNotStopped, true},
		{"no state", noState, nil, "", ErrNotStopped, true},
		{"two", two, nil, "", ErrNotStopped, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			requestDeadline, _ := request.Deadline()
			s := &Service{InstanceID: instanceID, EC2: refusedStart(func(ctx context.Context, in *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
				if len(in.InstanceIds) != 1 || in.InstanceIds[0] != instanceID || len(in.Filters) != 0 {
					t.Errorf("unscoped describe: %+v", in)
				}
				checkDeadline(t, ctx)
				if deadline, _ := ctx.Deadline(); deadline.After(requestDeadline) {
					t.Errorf("the look outlives the request: %v after %v", deadline, requestDeadline)
				}
				return test.output, test.err
			})}
			state, err := s.Start(request)
			if state != test.state || !errors.Is(err, test.want) {
				t.Fatalf("%q %v, want %q %v", state, err, test.state, test.want)
			}
			if test.failed != (err != ErrStartedElsewhere && err != ErrNotStopped) || (test.failed && !errors.Is(err, ErrNotStopped)) {
				t.Fatalf("a failed look must be ErrNotStopped with what went wrong, a look that read the host plain: %v", err)
			}
		})
	}

	// A request with no time left can't look again, and the start stays
	// refused.
	request, cancel := context.WithCancel(context.Background())
	cancel()
	s := &Service{InstanceID: instanceID, EC2: refusedStart(func(ctx context.Context, _ *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
		return nil, ctx.Err()
	})}
	if state, err := s.Start(request); state != "" || !errors.Is(err, ErrNotStopped) || !errors.Is(err, context.Canceled) {
		t.Fatalf("%q %v", state, err)
	}
}

// EC2 can also accept a start for a host another start already got to,
// answering with the state it was in before: pending or running is
// ErrStartedElsewhere with the state EC2 reports now.
func TestAnAcceptedStartAfterAnother(t *testing.T) {
	for _, test := range []struct {
		previous, current ec2types.InstanceStateName
		want              error
	}{
		{ec2types.InstanceStateNameStopped, ec2types.InstanceStateNamePending, nil},
		{ec2types.InstanceStateNamePending, ec2types.InstanceStateNamePending, ErrStartedElsewhere},
		{ec2types.InstanceStateNameRunning, ec2types.InstanceStateNameRunning, ErrStartedElsewhere},
	} {
		output := starting(instanceID, test.current)
		output.StartingInstances[0].PreviousState = &ec2types.InstanceState{Name: test.previous}
		s := &Service{InstanceID: instanceID, EC2: startFunc(func(context.Context, *ec2.StartInstancesInput) (*ec2.StartInstancesOutput, error) {
			return output, nil
		})}
		if state, err := s.Start(context.Background()); state != string(test.current) || err != test.want {
			t.Fatalf("from %s: %q %v", test.previous, state, err)
		}
	}
}

// Stop sends only the stop document, with no parameters, to only the
// configured host, with a 60-second delivery timeout, then records the
// command's ID and when it was sent on only the host's two stop tags.
func TestStop(t *testing.T) {
	sent := false
	s := &Service{
		InstanceID: instanceID,
		SSM: sendFunc(func(ctx context.Context, in *ssm.SendCommandInput) (*ssm.SendCommandOutput, error) {
			if aws.ToString(in.DocumentName) != "foundry-stop" || aws.ToString(in.DocumentVersion) != "$LATEST" ||
				len(in.InstanceIds) != 1 || in.InstanceIds[0] != instanceID || len(in.Targets) != 0 || len(in.Parameters) != 0 ||
				aws.ToInt32(in.TimeoutSeconds) != 60 || in.ServiceRoleArn != nil || in.OutputS3BucketName != nil {
				t.Errorf("unscoped stop: %+v", in)
			}
			checkDeadline(t, ctx)
			sent = true
			return &ssm.SendCommandOutput{Command: &ssmtypes.Command{CommandId: aws.String(commandID)}}, nil
		}),
		EC2: tagFunc(func(ctx context.Context, in *ec2.CreateTagsInput) (*ec2.CreateTagsOutput, error) {
			if !sent {
				t.Error("the stop was recorded before it was sent")
			}
			tags := map[string]string{}
			for _, tag := range in.Tags {
				tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
			}
			if len(in.Resources) != 1 || in.Resources[0] != instanceID || len(in.Tags) != 2 ||
				tags[status.StopCommandTag] != commandID || tags[status.StopRequestedTag] != "2026-10-14T09:30:05Z" {
				t.Errorf("unscoped record: %+v", in)
			}
			checkDeadline(t, ctx)
			return &ec2.CreateTagsOutput{}, nil
		}),
		Now: func() time.Time { return sentAt },
	}
	if command, err := s.Stop(context.Background()); command != commandID || err != nil {
		t.Fatalf("%q %v", command, err)
	}
}

// The record gets its own time once the stop is sent, even when the
// request's time has run out or its caller has gone.
func TestStopIsRecordedOnItsOwnTime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	s := &Service{
		InstanceID: instanceID,
		SSM: sendFunc(func(context.Context, *ssm.SendCommandInput) (*ssm.SendCommandOutput, error) {
			cancel()
			return &ssm.SendCommandOutput{Command: &ssmtypes.Command{CommandId: aws.String(commandID)}}, nil
		}),
		EC2: tagFunc(func(ctx context.Context, _ *ec2.CreateTagsInput) (*ec2.CreateTagsOutput, error) {
			deadline, ok := ctx.Deadline()
			if left := time.Until(deadline); ctx.Err() != nil || !ok || left > RecordTimeout || left < RecordTimeout-time.Second {
				t.Errorf("the record had %v left, %v", left, ctx.Err())
			}
			return &ec2.CreateTagsOutput{}, nil
		}),
	}
	if command, err := s.Stop(ctx); command != commandID || err != nil {
		t.Fatalf("%q %v", command, err)
	}
}

// A stop that was not sent is never recorded.
func TestStopFailures(t *testing.T) {
	failure := errors.New("provider failure")
	for _, test := range []struct {
		name   string
		output *ssm.SendCommandOutput
		err    error
		want   error
	}{
		{"no agent", nil, &smithy.GenericAPIError{Code: "InvalidInstanceId"}, ErrAgentOffline},
		{"other failure", nil, failure, failure},
		{"nil", nil, nil, nil},
		{"no command", &ssm.SendCommandOutput{}, nil, nil},
		{"no ID", &ssm.SendCommandOutput{Command: &ssmtypes.Command{}}, nil, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := &Service{
				InstanceID: instanceID,
				SSM: sendFunc(func(context.Context, *ssm.SendCommandInput) (*ssm.SendCommandOutput, error) {
					return test.output, test.err
				}),
				EC2: tagFunc(func(context.Context, *ec2.CreateTagsInput) (*ec2.CreateTagsOutput, error) {
					t.Error("a stop that was not sent was recorded")
					return nil, nil
				}),
			}
			command, err := s.Stop(context.Background())
			if command != "" || err == nil || errors.Is(err, ErrStopNotRecorded) || (test.want != nil && !errors.Is(err, test.want)) {
				t.Fatalf("%q %v", command, err)
			}
		})
	}
}

// When only the record fails, the stop is still reported as sent, with its
// ID, so the caller can say it is under way.
func TestStopNotRecorded(t *testing.T) {
	failure := &smithy.GenericAPIError{Code: "UnauthorizedOperation"}
	s := &Service{
		InstanceID: instanceID,
		SSM: sendFunc(func(context.Context, *ssm.SendCommandInput) (*ssm.SendCommandOutput, error) {
			return &ssm.SendCommandOutput{Command: &ssmtypes.Command{CommandId: aws.String(commandID)}}, nil
		}),
		EC2: tagFunc(func(context.Context, *ec2.CreateTagsInput) (*ec2.CreateTagsOutput, error) {
			return nil, failure
		}),
	}
	command, err := s.Stop(context.Background())
	if command != commandID || !errors.Is(err, ErrStopNotRecorded) || !errors.Is(err, failure) {
		t.Fatalf("%q %v", command, err)
	}
}

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

func (f startFunc) StartInstances(ctx context.Context, in *ec2.StartInstancesInput, _ ...func(*ec2.Options)) (*ec2.StartInstancesOutput, error) {
	return f(ctx, in)
}

func (f startFunc) CreateTags(context.Context, *ec2.CreateTagsInput, ...func(*ec2.Options)) (*ec2.CreateTagsOutput, error) {
	return nil, errors.New("a start must not tag the host")
}

type tagFunc func(context.Context, *ec2.CreateTagsInput) (*ec2.CreateTagsOutput, error)

func (f tagFunc) StartInstances(context.Context, *ec2.StartInstancesInput, ...func(*ec2.Options)) (*ec2.StartInstancesOutput, error) {
	return nil, errors.New("a stop must not start the host")
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
		{"not stopped yet", nil, &smithy.GenericAPIError{Code: "IncorrectInstanceState"}, ErrNotStopped},
		{"other failure", nil, failure, failure},
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

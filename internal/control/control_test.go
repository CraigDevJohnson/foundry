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

type sendFunc func(context.Context, *ssm.SendCommandInput) (*ssm.SendCommandOutput, error)

func (f sendFunc) SendCommand(ctx context.Context, in *ssm.SendCommandInput, _ ...func(*ssm.Options)) (*ssm.SendCommandOutput, error) {
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
// configured host, with a 60-second delivery timeout.
func TestStop(t *testing.T) {
	s := &Service{InstanceID: instanceID, SSM: sendFunc(func(ctx context.Context, in *ssm.SendCommandInput) (*ssm.SendCommandOutput, error) {
		if aws.ToString(in.DocumentName) != "foundry-stop" || aws.ToString(in.DocumentVersion) != "$LATEST" ||
			len(in.InstanceIds) != 1 || in.InstanceIds[0] != instanceID || len(in.Targets) != 0 || len(in.Parameters) != 0 ||
			aws.ToInt32(in.TimeoutSeconds) != 60 || in.ServiceRoleArn != nil || in.OutputS3BucketName != nil {
			t.Errorf("unscoped stop: %+v", in)
		}
		checkDeadline(t, ctx)
		return &ssm.SendCommandOutput{Command: &ssmtypes.Command{CommandId: aws.String("0b1c2d3e")}}, nil
	})}
	if command, err := s.Stop(context.Background()); command != "0b1c2d3e" || err != nil {
		t.Fatalf("%q %v", command, err)
	}
}

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
			s := &Service{InstanceID: instanceID, SSM: sendFunc(func(context.Context, *ssm.SendCommandInput) (*ssm.SendCommandOutput, error) {
				return test.output, test.err
			})}
			command, err := s.Stop(context.Background())
			if command != "" || err == nil || (test.want != nil && !errors.Is(err, test.want)) {
				t.Fatalf("%q %v", command, err)
			}
		})
	}
}

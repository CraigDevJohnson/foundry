// Package control changes the Foundry host for The Lobby: it starts the host
// with EC2, or sends it the stop document, which shuts Foundry down and then
// powers the host off (ADR 0006). It never waits for either to finish; the
// status observation (internal/status) follows them from there.
package control

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/smithy-go"

	"github.com/CraigDevJohnson/foundry/internal/status"
)

// deliveryTimeoutSeconds is how long Systems Manager may take to hand the stop
// to the host's agent. SendCommand's reference says a command that hasn't
// started by then won't run; Systems Manager's user guide puts its expiry at
// this plus the document's 5-minute execution timeout. Either way a stop that
// doesn't reach the agent ends within about 6 minutes, and the status shows
// it under way until then.
const deliveryTimeoutSeconds = 60

// RecordTimeout bounds writing a sent stop to the host's tags. It runs on its
// own time, not what is left of the request's: once the stop is sent, the
// status can follow it, and hold off a second one, only if it is recorded.
// The 6 seconds a change gets plus this stay inside the function's 10.
const RecordTimeout = 2 * time.Second

// RecheckTimeout bounds looking at the host again after EC2 refused a start.
// Like RecordTimeout it runs on its own time, and a start never records a
// stop, so the 6 seconds a change gets plus this stay inside the function's 10.
const RecheckTimeout = 2 * time.Second

var (
	// ErrNotStopped means EC2 refused the start because the host is not
	// stopped yet, for example while it is still stopping.
	ErrNotStopped = errors.New("EC2 can't start the host in its current state")
	// ErrStartedElsewhere means EC2 refused the start because the host had
	// already left stopped and is starting or running: another start, such
	// as the other site environment's, got there first. Start returns the
	// state EC2 reported with it.
	ErrStartedElsewhere = errors.New("another start got to the host first")
	// ErrAgentOffline means Systems Manager has no connected agent on the
	// host to send the stop to, for example right after it booted.
	ErrAgentOffline = errors.New("the host's Systems Manager agent is not connected")
	// ErrStopNotRecorded means the stop was sent, but its record could not
	// be written to the host's tags, so the status can't follow it.
	ErrStopNotRecorded = errors.New("the stop was sent but not recorded on the host")
)

type EC2Changer interface {
	StartInstances(context.Context, *ec2.StartInstancesInput, ...func(*ec2.Options)) (*ec2.StartInstancesOutput, error)
	DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
	CreateTags(context.Context, *ec2.CreateTagsInput, ...func(*ec2.Options)) (*ec2.CreateTagsOutput, error)
}

type CommandSender interface {
	SendCommand(context.Context, *ssm.SendCommandInput, ...func(*ssm.Options)) (*ssm.SendCommandOutput, error)
}

// Service changes only the host in InstanceID, which comes from the
// function's configuration, never from a request.
type Service struct {
	InstanceID string
	EC2        EC2Changer
	SSM        CommandSender
	// Now is the clock a stop's time is read from; nil means time.Now.
	Now func() time.Time
}

// Start asks EC2 to start the host and returns the state EC2 reports for it
// afterwards, normally pending. It is asked once: a retry after an answer that
// was lost would find the host already starting, and could report it as
// refused. When EC2 refuses it for the host's state, Start looks at the host
// again, because another start may have got there first since the host was
// observed stopped: a host now starting or running is ErrStartedElsewhere,
// anything else ErrNotStopped.
func (s *Service) Start(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, status.ProviderTimeout)
	defer cancel()
	output, err := s.EC2.StartInstances(ctx, &ec2.StartInstancesInput{InstanceIds: []string{s.InstanceID}}, func(o *ec2.Options) { o.RetryMaxAttempts = 1 })
	if err != nil {
		if code(err) == "IncorrectInstanceState" {
			if state := s.hostState(ctx); state == string(ec2types.InstanceStateNamePending) || state == string(ec2types.InstanceStateNameRunning) {
				return state, ErrStartedElsewhere
			}
			return "", ErrNotStopped
		}
		return "", err
	}
	if output == nil || len(output.StartingInstances) != 1 || aws.ToString(output.StartingInstances[0].InstanceId) != s.InstanceID || output.StartingInstances[0].CurrentState == nil {
		return "", errors.New("EC2 answered the start with an unexpected record")
	}
	return string(output.StartingInstances[0].CurrentState.Name), nil
}

// Stop sends the stop document to the host, records it on the host's tags
// for the status to follow, and returns the command's ID. The document takes
// no parameters, so nothing here can change what runs. It is sent once:
// SendCommand has no idempotency token, so a retry after an answer that was
// lost could send a second stop. The record gets RecordTimeout of its own,
// even when ctx has run out or been cancelled. When only the record fails,
// the ID comes back with ErrStopNotRecorded: the stop is under way
// regardless.
func (s *Service) Stop(ctx context.Context) (string, error) {
	sendCtx, cancel := context.WithTimeout(ctx, status.ProviderTimeout)
	defer cancel()
	output, err := s.SSM.SendCommand(sendCtx, &ssm.SendCommandInput{
		DocumentName:    aws.String(status.StopDocument),
		DocumentVersion: aws.String("$LATEST"),
		InstanceIds:     []string{s.InstanceID},
		Comment:         aws.String("Stop requested through The Lobby"),
		TimeoutSeconds:  aws.Int32(deliveryTimeoutSeconds),
	}, func(o *ssm.Options) { o.RetryMaxAttempts = 1 })
	if err != nil {
		if code(err) == "InvalidInstanceId" {
			return "", ErrAgentOffline
		}
		return "", err
	}
	if output == nil || output.Command == nil || aws.ToString(output.Command.CommandId) == "" {
		return "", errors.New("Systems Manager answered the stop with an unexpected record")
	}
	command := aws.ToString(output.Command.CommandId)
	recordCtx, cancelRecord := context.WithTimeout(context.WithoutCancel(ctx), RecordTimeout)
	defer cancelRecord()
	_, err = s.EC2.CreateTags(recordCtx, &ec2.CreateTagsInput{
		Resources: []string{s.InstanceID},
		Tags: []ec2types.Tag{
			{Key: aws.String(status.StopCommandTag), Value: aws.String(command)},
			{Key: aws.String(status.StopRequestedTag), Value: aws.String(s.now().UTC().Format(time.RFC3339))},
		},
	})
	if err != nil {
		return command, fmt.Errorf("%w: %w", ErrStopNotRecorded, err)
	}
	return command, nil
}

// hostState reads the host's state from EC2 on its own time, or returns ""
// when EC2 doesn't answer with the host.
func (s *Service) hostState(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), RecheckTimeout)
	defer cancel()
	output, err := s.EC2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{s.InstanceID}})
	if err != nil || output == nil || len(output.Reservations) != 1 || len(output.Reservations[0].Instances) != 1 {
		return ""
	}
	instance := output.Reservations[0].Instances[0]
	if aws.ToString(instance.InstanceId) != s.InstanceID || instance.State == nil {
		return ""
	}
	return string(instance.State.Name)
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func code(err error) string {
	var apiError smithy.APIError
	if errors.As(err, &apiError) {
		return apiError.ErrorCode()
	}
	return ""
}

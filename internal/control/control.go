// Package control changes the Foundry host for The Lobby: it starts the host
// with EC2, or sends it the stop document, which shuts Foundry down and then
// powers the host off (ADR 0006). It never waits for either to finish; the
// status observation (internal/status) follows them from there.
package control

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
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

var (
	// ErrNotStopped means EC2 refused the start because the host is not
	// stopped yet, for example while it is still stopping.
	ErrNotStopped = errors.New("EC2 can't start the host in its current state")
	// ErrAgentOffline means Systems Manager has no connected agent on the
	// host to send the stop to, for example right after it booted.
	ErrAgentOffline = errors.New("the host's Systems Manager agent is not connected")
)

type EC2Starter interface {
	StartInstances(context.Context, *ec2.StartInstancesInput, ...func(*ec2.Options)) (*ec2.StartInstancesOutput, error)
}

type CommandSender interface {
	SendCommand(context.Context, *ssm.SendCommandInput, ...func(*ssm.Options)) (*ssm.SendCommandOutput, error)
}

// Service changes only the host in InstanceID, which comes from the
// function's configuration, never from a request.
type Service struct {
	InstanceID string
	EC2        EC2Starter
	SSM        CommandSender
}

// Start asks EC2 to start the host and returns the state EC2 reports for it
// afterwards, normally pending.
func (s *Service) Start(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, status.ProviderTimeout)
	defer cancel()
	output, err := s.EC2.StartInstances(ctx, &ec2.StartInstancesInput{InstanceIds: []string{s.InstanceID}})
	if err != nil {
		if code(err) == "IncorrectInstanceState" {
			return "", ErrNotStopped
		}
		return "", err
	}
	if output == nil || len(output.StartingInstances) != 1 || aws.ToString(output.StartingInstances[0].InstanceId) != s.InstanceID || output.StartingInstances[0].CurrentState == nil {
		return "", errors.New("EC2 answered the start with an unexpected record")
	}
	return string(output.StartingInstances[0].CurrentState.Name), nil
}

// Stop sends the stop document to the host and returns the command's ID. The
// document takes no parameters, so nothing here can change what runs.
func (s *Service) Stop(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, status.ProviderTimeout)
	defer cancel()
	output, err := s.SSM.SendCommand(ctx, &ssm.SendCommandInput{
		DocumentName:    aws.String(status.StopDocument),
		DocumentVersion: aws.String("$LATEST"),
		InstanceIds:     []string{s.InstanceID},
		Comment:         aws.String("Stop requested through The Lobby"),
		TimeoutSeconds:  aws.Int32(deliveryTimeoutSeconds),
	})
	if err != nil {
		if code(err) == "InvalidInstanceId" {
			return "", ErrAgentOffline
		}
		return "", err
	}
	if output == nil || output.Command == nil || aws.ToString(output.Command.CommandId) == "" {
		return "", errors.New("Systems Manager answered the stop with an unexpected record")
	}
	return aws.ToString(output.Command.CommandId), nil
}

func code(err error) string {
	var apiError smithy.APIError
	if errors.As(err, &apiError) {
		return apiError.ErrorCode()
	}
	return ""
}

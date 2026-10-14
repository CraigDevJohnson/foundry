// Package status observes the Foundry host: EC2's lifecycle state, the
// Systems Manager agent's connectivity, whether Foundry's join page answers
// and whether a stop sent through The Lobby is under way. It reports what it
// saw, with a fixed reason for each result, and never guesses. The HTTP interface (internal/api) turns a Report into the
// answer The Lobby shows.
package status

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/aws/smithy-go"
)

// ProviderTimeout bounds each EC2 and SSM call and the HTTPS probe.
const ProviderTimeout = 3 * time.Second

// StopDocument is the Systems Manager command document a stop sends to the
// host (infra/controller.tf): it shuts Foundry down, then powers the host off.
const StopDocument = "foundry-stop"

type EC2Reader interface {
	DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
}

type SSMReader interface {
	DescribeInstanceInformation(context.Context, *ssm.DescribeInstanceInformationInput, ...func(*ssm.Options)) (*ssm.DescribeInstanceInformationOutput, error)
}

type HTTPSProber interface {
	Probe(context.Context, string) HTTPSObservation
}

type CommandLister interface {
	ListCommands(context.Context, *ssm.ListCommandsInput, ...func(*ssm.Options)) (*ssm.ListCommandsOutput, error)
}

// Observation is one component's state and the fixed reason for it.
type Observation struct {
	State  string
	Reason string
}

// HostObservation is EC2's view of the instance. LaunchTime is when EC2 last
// started it, which EC2 resets on every start; it is zero when EC2 gave none.
type HostObservation struct {
	Observation
	LaunchTime time.Time
}

type SSMObservation struct {
	Observation
	LastPingAt *time.Time
}

type HTTPSObservation struct {
	Observation
	HTTPStatus int
}

// StopObservation is the latest stop sent to the host since EC2 last started
// it. RequestedAt is when it was sent; it is zero when there is none.
type StopObservation struct {
	Observation
	RequestedAt time.Time
}

// Report is one observation of the host, its agent, Foundry's join page and
// any stop under way. They are taken during one call, not as an atomic
// snapshot. ObservedAt is when the observation finished.
type Report struct {
	Host       HostObservation
	SSM        SSMObservation
	HTTPS      HTTPSObservation
	Stop       StopObservation
	ObservedAt time.Time
}

type Service struct {
	Config   Config
	EC2      EC2Reader
	SSM      SSMReader
	HTTPS    HTTPSProber
	Commands CommandLister
	// Now is the clock ObservedAt is read from; nil means time.Now.
	Now func() time.Time
}

// Observe reads the host first. Only a running host has its agent, join page
// and stops checked, independently of each other. Each call has
// ProviderTimeout, shortened by any deadline on ctx.
func (s *Service) Observe(ctx context.Context) Report {
	host := s.host(ctx)
	ssmObservation := SSMObservation{Observation: Observation{State: "not_checked", Reason: "host_not_running"}}
	https := HTTPSObservation{Observation: Observation{State: "not_checked", Reason: "host_not_running"}}
	stop := StopObservation{Observation: Observation{State: "not_checked", Reason: "host_not_running"}}
	if host.State == "unknown" {
		ssmObservation.Reason = "host_unknown"
		https.Reason = "host_unknown"
		stop.Reason = "host_unknown"
	}
	if host.State == "running" {
		var pending sync.WaitGroup
		pending.Add(3)
		go func() { defer pending.Done(); ssmObservation = s.agent(ctx) }()
		go func() {
			defer pending.Done()
			probeCtx, cancel := context.WithTimeout(ctx, ProviderTimeout)
			defer cancel()
			https = s.HTTPS.Probe(probeCtx, s.Config.HTTPSURL)
		}()
		go func() { defer pending.Done(); stop = s.stop(ctx, host.LaunchTime) }()
		pending.Wait()
	}
	return Report{Host: host, SSM: ssmObservation, HTTPS: https, Stop: stop, ObservedAt: s.now().UTC()}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) host(ctx context.Context) HostObservation {
	ctx, cancel := context.WithTimeout(ctx, ProviderTimeout)
	defer cancel()
	output, err := s.EC2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{s.Config.InstanceID}})
	if err != nil {
		var apiError smithy.APIError
		if errors.As(err, &apiError) && apiError.ErrorCode() == "InvalidInstanceID.NotFound" {
			return hostObservation("not_found", "no_record")
		}
		return hostObservation("unknown", providerReason(err))
	}
	if output == nil || aws.ToString(output.NextToken) != "" {
		return hostObservation("unknown", "unexpected_record")
	}
	count := 0
	var state string
	var launched time.Time
	for _, reservation := range output.Reservations {
		for _, instance := range reservation.Instances {
			count++
			if aws.ToString(instance.InstanceId) != s.Config.InstanceID || instance.State == nil {
				return hostObservation("unknown", "unexpected_record")
			}
			state = string(instance.State.Name)
			launched = aws.ToTime(instance.LaunchTime).UTC()
		}
	}
	if count == 0 {
		return hostObservation("not_found", "no_record")
	}
	if count != 1 {
		return hostObservation("unknown", "unexpected_record")
	}
	switch state {
	case "pending", "running", "shutting-down", "terminated", "stopping", "stopped":
		result := hostObservation(state, "observed")
		result.LaunchTime = launched
		return result
	default:
		return hostObservation("unknown", "unexpected_state")
	}
}

func hostObservation(state, reason string) HostObservation {
	return HostObservation{Observation: Observation{State: state, Reason: reason}}
}

func (s *Service) agent(ctx context.Context) SSMObservation {
	ctx, cancel := context.WithTimeout(ctx, ProviderTimeout)
	defer cancel()
	output, err := s.SSM.DescribeInstanceInformation(ctx, &ssm.DescribeInstanceInformationInput{Filters: []ssmtypes.InstanceInformationStringFilter{{Key: aws.String("InstanceIds"), Values: []string{s.Config.InstanceID}}}})
	if err != nil {
		return SSMObservation{Observation: Observation{State: "unknown", Reason: providerReason(err)}}
	}
	if output == nil || aws.ToString(output.NextToken) != "" {
		return SSMObservation{Observation: Observation{State: "unknown", Reason: "unexpected_record"}}
	}
	if len(output.InstanceInformationList) == 0 {
		return SSMObservation{Observation: Observation{State: "unknown", Reason: "no_record"}}
	}
	if len(output.InstanceInformationList) != 1 || aws.ToString(output.InstanceInformationList[0].InstanceId) != s.Config.InstanceID {
		return SSMObservation{Observation: Observation{State: "unknown", Reason: "unexpected_record"}}
	}
	record := output.InstanceInformationList[0]
	result := SSMObservation{Observation: Observation{State: "unknown", Reason: "unexpected_state"}}
	if record.LastPingDateTime != nil {
		ping := record.LastPingDateTime.UTC()
		result.LastPingAt = &ping
	}
	switch record.PingStatus {
	case ssmtypes.PingStatusOnline:
		result.State, result.Reason = "online", "observed"
	case ssmtypes.PingStatusConnectionLost, ssmtypes.PingStatusInactive:
		result.State, result.Reason = "offline", "observed"
	}
	return result
}

// stop finds the latest stop sent to the host since EC2 last started it, so a
// stop from an earlier run never counts. Without a start time there is no
// such bound, and nothing is guessed. The commands are listed by document and
// time, not by instance: Systems Manager may list a command against an
// instance only once it reaches the instance, and a stop still pending must
// count. Each one names the instances it was sent to.
func (s *Service) stop(ctx context.Context, launched time.Time) StopObservation {
	if launched.IsZero() {
		return StopObservation{Observation: Observation{State: "not_checked", Reason: "no_launch_time"}}
	}
	ctx, cancel := context.WithTimeout(ctx, ProviderTimeout)
	defer cancel()
	output, err := s.Commands.ListCommands(ctx, &ssm.ListCommandsInput{
		Filters: []ssmtypes.CommandFilter{
			{Key: ssmtypes.CommandFilterKeyDocumentName, Value: aws.String(StopDocument)},
			{Key: ssmtypes.CommandFilterKeyInvokedAfter, Value: aws.String(launched.UTC().Format(time.RFC3339))},
		},
		MaxResults: aws.Int32(50),
	})
	if err != nil {
		return StopObservation{Observation: Observation{State: "unknown", Reason: providerReason(err)}}
	}
	if output == nil || aws.ToString(output.NextToken) != "" {
		return StopObservation{Observation: Observation{State: "unknown", Reason: "unexpected_record"}}
	}
	var latest *ssmtypes.Command
	for i := range output.Commands {
		command := &output.Commands[i]
		if aws.ToString(command.DocumentName) != StopDocument || command.RequestedDateTime == nil || command.RequestedDateTime.Before(launched) ||
			len(command.Targets) != 0 || len(command.InstanceIds) == 0 {
			return StopObservation{Observation: Observation{State: "unknown", Reason: "unexpected_record"}}
		}
		if !slices.Contains(command.InstanceIds, s.Config.InstanceID) {
			continue
		}
		if latest == nil || command.RequestedDateTime.After(*latest.RequestedDateTime) {
			latest = command
		}
	}
	if latest == nil {
		return StopObservation{Observation: Observation{State: "none", Reason: "observed"}}
	}
	result := StopObservation{Observation: Observation{State: "unknown", Reason: "unexpected_state"}, RequestedAt: latest.RequestedDateTime.UTC()}
	switch latest.Status {
	case ssmtypes.CommandStatusPending, ssmtypes.CommandStatusInProgress, ssmtypes.CommandStatusCancelling:
		result.State, result.Reason = "in_progress", "observed"
	case ssmtypes.CommandStatusSuccess:
		result.State, result.Reason = "succeeded", "observed"
	case ssmtypes.CommandStatusFailed, ssmtypes.CommandStatusTimedOut, ssmtypes.CommandStatusCancelled:
		result.State, result.Reason = "failed", "observed"
	}
	return result
}

func providerReason(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var apiError smithy.APIError
	if errors.As(err, &apiError) {
		switch apiError.ErrorCode() {
		case "AccessDenied", "AccessDeniedException", "UnauthorizedOperation", "UnrecognizedClientException":
			return "access_denied"
		}
	}
	return "provider_error"
}

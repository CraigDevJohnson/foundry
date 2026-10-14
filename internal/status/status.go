// Package status observes the Foundry host: EC2's lifecycle state, the
// Systems Manager agent's connectivity, whether Foundry's join page answers
// and whether a stop sent through The Lobby is under way. It reports what it
// saw, with a fixed reason for each result, and never guesses. The HTTP interface (internal/api) turns a Report into the
// answer The Lobby shows.
package status

import (
	"context"
	"errors"
	"regexp"
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

// The controller records the latest stop on the host itself, under these
// tags (internal/control): the command's ID, and when it was sent in RFC 3339.
// The status follows that one command with GetCommandInvocation, so it never
// lists the account's commands.
const (
	StopCommandTag   = "foundry:stop-command"
	StopRequestedTag = "foundry:stop-requested"
)

// invocationGrace is how long after a stop was sent Systems Manager may still
// answer that it has no such invocation: it records one only once the command
// is under way, eventually.
const invocationGrace = 2 * time.Minute

// stopLifetime is how long a stop can run at most: a 60-second delivery
// timeout (internal/control) and the document's 5-minute execution timeout,
// with room to spare. After that a stop Systems Manager has no record of is
// over, because its history has expired, and no longer holds anything up.
const stopLifetime = 10 * time.Minute

// commandID is the form of a Systems Manager command ID.
var commandID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type EC2Reader interface {
	DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
}

type SSMReader interface {
	DescribeInstanceInformation(context.Context, *ssm.DescribeInstanceInformationInput, ...func(*ssm.Options)) (*ssm.DescribeInstanceInformationOutput, error)
}

type HTTPSProber interface {
	Probe(context.Context, string) HTTPSObservation
}

type CommandReader interface {
	GetCommandInvocation(context.Context, *ssm.GetCommandInvocationInput, ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error)
}

// Observation is one component's state and the fixed reason for it.
type Observation struct {
	State  string
	Reason string
}

// HostObservation is EC2's view of the instance. LaunchTime is when EC2 last
// started it, which EC2 resets on every start; it is zero when EC2 gave none.
// StopCommand and StopRequested are the host's stop tags, empty when absent.
type HostObservation struct {
	Observation
	LaunchTime    time.Time
	StopCommand   string
	StopRequested string
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
	Commands CommandReader
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
		go func() { defer pending.Done(); stop = s.stop(ctx, host) }()
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
	var state, stopCommand, stopRequested string
	var launched time.Time
	for _, reservation := range output.Reservations {
		for _, instance := range reservation.Instances {
			count++
			if aws.ToString(instance.InstanceId) != s.Config.InstanceID || instance.State == nil {
				return hostObservation("unknown", "unexpected_record")
			}
			state = string(instance.State.Name)
			launched = aws.ToTime(instance.LaunchTime).UTC()
			for _, tag := range instance.Tags {
				switch aws.ToString(tag.Key) {
				case StopCommandTag:
					stopCommand = aws.ToString(tag.Value)
				case StopRequestedTag:
					stopRequested = aws.ToString(tag.Value)
				}
			}
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
		result.StopCommand, result.StopRequested = stopCommand, stopRequested
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

// stop follows the latest stop sent to the host, which the controller recorded
// on the host's tags, if it was sent since EC2 last started the host: a stop
// from an earlier run never counts. Without a start time a recorded stop
// can't be placed before or after it, so it isn't checked and nothing is
// guessed. A stop whose record has expired is reported as none: it is long
// over, whatever it did.
func (s *Service) stop(ctx context.Context, host HostObservation) StopObservation {
	if host.StopCommand == "" && host.StopRequested == "" {
		return StopObservation{Observation: Observation{State: "none", Reason: "observed"}}
	}
	if host.LaunchTime.IsZero() {
		return StopObservation{Observation: Observation{State: "not_checked", Reason: "no_launch_time"}}
	}
	requested, err := time.Parse(time.RFC3339, host.StopRequested)
	if err != nil || !commandID.MatchString(host.StopCommand) {
		return StopObservation{Observation: Observation{State: "unknown", Reason: "unexpected_record"}}
	}
	requested = requested.UTC()
	if requested.Before(host.LaunchTime) {
		return StopObservation{Observation: Observation{State: "none", Reason: "observed"}}
	}
	ctx, cancel := context.WithTimeout(ctx, ProviderTimeout)
	defer cancel()
	output, err := s.Commands.GetCommandInvocation(ctx, &ssm.GetCommandInvocationInput{
		CommandId:  aws.String(host.StopCommand),
		InstanceId: aws.String(s.Config.InstanceID),
	})
	result := StopObservation{Observation: Observation{State: "unknown", Reason: "unexpected_state"}, RequestedAt: requested}
	if err != nil {
		var apiError smithy.APIError
		if errors.As(err, &apiError) && apiError.ErrorCode() == "InvocationDoesNotExist" {
			switch age := s.now().Sub(requested); {
			case age < invocationGrace:
				result.State, result.Reason = "in_progress", "not_recorded_yet"
			case age < stopLifetime:
				result.Reason = "no_record"
			default:
				result.State, result.Reason = "none", "expired"
			}
			return result
		}
		result.Reason = providerReason(err)
		return result
	}
	if output == nil || aws.ToString(output.CommandId) != host.StopCommand || aws.ToString(output.InstanceId) != s.Config.InstanceID ||
		aws.ToString(output.DocumentName) != StopDocument {
		result.Reason = "unexpected_record"
		return result
	}
	switch output.Status {
	case ssmtypes.CommandInvocationStatusPending, ssmtypes.CommandInvocationStatusInProgress,
		ssmtypes.CommandInvocationStatusDelayed, ssmtypes.CommandInvocationStatusCancelling:
		result.State, result.Reason = "in_progress", "observed"
	case ssmtypes.CommandInvocationStatusSuccess:
		result.State, result.Reason = "succeeded", "observed"
	case ssmtypes.CommandInvocationStatusFailed, ssmtypes.CommandInvocationStatusTimedOut, ssmtypes.CommandInvocationStatusCancelled:
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

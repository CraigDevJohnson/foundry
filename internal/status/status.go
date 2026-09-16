package status

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/aws/smithy-go"
)

type EC2Reader interface {
	DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
}

type SSMReader interface {
	DescribeInstanceInformation(context.Context, *ssm.DescribeInstanceInformationInput, ...func(*ssm.Options)) (*ssm.DescribeInstanceInformationOutput, error)
}

type HTTPSProber interface {
	Probe(context.Context, string) HTTPSObservation
}

type Service struct {
	Config Config
	EC2    EC2Reader
	SSM    SSMReader
	HTTPS  HTTPSProber
}

func (s *Service) Observe(ctx context.Context, requestID string) Response {
	host := s.host(ctx)
	ssmObservation := SSMObservation{Observation: Observation{State: "not_checked", Reason: "host_not_running"}}
	https := HTTPSObservation{Observation: Observation{State: "not_checked", Reason: "host_not_running"}}
	if host.State == "unknown" {
		ssmObservation.Reason = "host_unknown"
		https.Reason = "host_unknown"
	}
	if host.State == "running" {
		var pending sync.WaitGroup
		pending.Add(2)
		go func() { defer pending.Done(); ssmObservation = s.agent(ctx) }()
		go func() {
			defer pending.Done()
			probeCtx, cancel := context.WithTimeout(ctx, ProviderTimeout)
			defer cancel()
			https = s.HTTPS.Probe(probeCtx, s.Config.HTTPSURL)
		}()
		pending.Wait()
	}
	return Response{Version: 1, Kind: "status", RequestID: requestID, ObservedAt: time.Now().UTC(), Host: &host, SSM: &ssmObservation, HTTPS: &https}
}

func (s *Service) host(ctx context.Context) Observation {
	ctx, cancel := context.WithTimeout(ctx, ProviderTimeout)
	defer cancel()
	output, err := s.EC2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{s.Config.InstanceID}})
	if err != nil {
		var apiError smithy.APIError
		if errors.As(err, &apiError) && apiError.ErrorCode() == "InvalidInstanceID.NotFound" {
			return Observation{State: "not_found", Reason: "no_record"}
		}
		return Observation{State: "unknown", Reason: providerReason(err)}
	}
	if output == nil || aws.ToString(output.NextToken) != "" {
		return Observation{State: "unknown", Reason: "unexpected_record"}
	}
	count := 0
	var state string
	for _, reservation := range output.Reservations {
		for _, instance := range reservation.Instances {
			count++
			if aws.ToString(instance.InstanceId) != s.Config.InstanceID || instance.State == nil {
				return Observation{State: "unknown", Reason: "unexpected_record"}
			}
			state = string(instance.State.Name)
		}
	}
	if count == 0 {
		return Observation{State: "not_found", Reason: "no_record"}
	}
	if count != 1 {
		return Observation{State: "unknown", Reason: "unexpected_record"}
	}
	switch state {
	case "pending", "running", "shutting-down", "terminated", "stopping", "stopped":
		return Observation{State: state, Reason: "observed"}
	default:
		return Observation{State: "unknown", Reason: "unexpected_state"}
	}
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

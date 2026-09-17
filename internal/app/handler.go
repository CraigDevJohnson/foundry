package app

import (
	"context"
	"os"

	"github.com/CraigDevJohnson/foundry/internal/status"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

func Handler() status.Handler {
	probe := status.NewHTTPProbe()
	return status.Handler{Build: func(ctx context.Context) (*status.Service, string) {
		target, valid := status.ConfigFromEnvironment(os.Getenv)
		if !valid {
			return nil, "invalid_configuration"
		}
		cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(target.Region), config.WithRetryMaxAttempts(2))
		if err != nil {
			return nil, "provider_configuration_error"
		}
		return &status.Service{Config: target, EC2: ec2.NewFromConfig(cfg), SSM: ssm.NewFromConfig(cfg), HTTPS: probe}, ""
	}}
}

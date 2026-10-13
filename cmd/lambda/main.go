// The Foundry controller: it answers The Lobby's site with the host's status
// and changes nothing (ADR 0005). On Lambda it serves through the Function URL
// adapter (payload format 2.0); anywhere else it listens on a local port with
// the caller's own AWS credentials.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"

	"github.com/CraigDevJohnson/foundry/internal/api"
	"github.com/CraigDevJohnson/foundry/internal/status"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	handler, err := newHandler(context.Background(), os.Getenv, log)
	if err != nil {
		// On Lambda this is an init error, which the function's Errors alarm counts.
		log.Error("invalid_configuration", "err", err)
		os.Exit(1)
	}

	if os.Getenv("AWS_LAMBDA_RUNTIME_API") != "" {
		lambda.Start(httpadapter.NewV2(handler).ProxyWithContext)
		return
	}
	addr := listenAddr(os.Getenv)
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	log.Info("listening", "addr", "http://"+addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
}

// newHandler reads the configuration and builds the routes. It makes no AWS
// call: credentials are resolved on the first observation.
func newHandler(ctx context.Context, getenv func(string) string, log *slog.Logger) (http.Handler, error) {
	target, err := status.ConfigFromEnvironment(getenv)
	if err != nil {
		return nil, err
	}
	awsConfig, err := config.LoadDefaultConfig(ctx, config.WithRegion(target.Region), config.WithRetryMaxAttempts(2))
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}
	service := &status.Service{
		Config: target,
		EC2:    ec2.NewFromConfig(awsConfig),
		SSM:    ssm.NewFromConfig(awsConfig),
		HTTPS:  status.NewHTTPProbe(),
	}
	return (&api.Handler{Status: service, JoinURL: target.HTTPSURL, Log: log}).Router(), nil
}

// listenAddr is FOUNDRY_ADDR, or 127.0.0.1:8083, beside the soccer
// backend's 8081 and The Lobby's 8080.
func listenAddr(getenv func(string) string) string {
	if addr := getenv("FOUNDRY_ADDR"); addr != "" {
		return addr
	}
	return "127.0.0.1:8083"
}

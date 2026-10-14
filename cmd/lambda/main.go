// The Foundry controller: it answers The Lobby's site with the host's status,
// and starts and stops the host when the site asks (ADR 0005, ADR 0006). On
// Lambda it serves through the Function URL adapter (payload format 2.0);
// anywhere else it listens on a local port with the caller's own AWS
// credentials.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"

	"github.com/CraigDevJohnson/foundry/internal/api"
	"github.com/CraigDevJohnson/foundry/internal/control"
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
	addr, err := listenAddr(os.Getenv)
	if err != nil {
		log.Error("invalid_configuration", "err", err)
		os.Exit(1)
	}
	srv := &http.Server{Addr: addr, Handler: localOnly(handler), ReadHeaderTimeout: 5 * time.Second}
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
	site, err := api.SiteSession(getenv("FOUNDRY_SITE_ROLE_ARN"))
	if err != nil {
		return nil, err
	}
	awsConfig, err := config.LoadDefaultConfig(ctx, config.WithRegion(target.Region), config.WithRetryMaxAttempts(2))
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}
	ec2Client := ec2.NewFromConfig(awsConfig)
	ssmClient := ssm.NewFromConfig(awsConfig)
	service := &status.Service{
		Config:   target,
		EC2:      ec2Client,
		SSM:      ssmClient,
		HTTPS:    status.NewHTTPProbe(),
		Commands: ssmClient,
	}
	change := &control.Service{InstanceID: target.InstanceID, EC2: ec2Client, SSM: ssmClient}
	return (&api.Handler{Status: service, Change: change, JoinURL: target.HTTPSURL, SiteSession: site, Log: log}).Router(), nil
}

// localOnly guards the local listener, which acts with the developer's own AWS
// credentials and has no sign-in. A request must be addressed to localhost or
// a loopback address, so a web page can't reach the listener through a domain
// of its own that points at this computer, and a browser's cross-origin POST
// is refused, so a page open on this computer can't start or stop the host.
// curl sends neither browser header and is allowed. listenAddr keeps the
// listener itself on a loopback address.
func localOnly(next http.Handler) http.Handler {
	guarded := http.NewCrossOriginProtection().Handler(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !localHost(r.Host) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusMisdirectedRequest)
			io.WriteString(w, `{"code":"not_local"}`+"\n")
			return
		}
		guarded.ServeHTTP(w, r)
	})
}

// localHost reports whether a request's Host, or a listen address, names
// localhost or a loopback address, with or without a port.
func localHost(host string) bool {
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// listenAddr is FOUNDRY_ADDR, or 127.0.0.1:8083, beside the soccer
// backend's 8081 and The Lobby's 8080. Anything but a loopback address is
// refused: the listener has no sign-in, so another computer must not reach it.
func listenAddr(getenv func(string) string) (string, error) {
	addr := getenv("FOUNDRY_ADDR")
	if addr == "" {
		return "127.0.0.1:8083", nil
	}
	if host, _, err := net.SplitHostPort(addr); err != nil || !localHost(host) {
		return "", fmt.Errorf("FOUNDRY_ADDR %q must be a loopback address and port, such as 127.0.0.1:8083: the local server acts with your AWS credentials and has no sign-in", addr)
	}
	return addr, nil
}

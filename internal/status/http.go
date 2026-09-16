package status

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
)

const MaxResponseBytes = 64 * 1024

type HTTPProbe struct{ client *http.Client }

func NewHTTPProbe() *HTTPProbe {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	transport.DialContext = (&net.Dialer{Timeout: ProviderTimeout}).DialContext
	transport.TLSHandshakeTimeout = ProviderTimeout
	transport.ResponseHeaderTimeout = ProviderTimeout
	transport.MaxResponseHeaderBytes = 16 * 1024
	return &HTTPProbe{client: &http.Client{Transport: transport, Timeout: ProviderTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (p *HTTPProbe) Probe(ctx context.Context, endpoint string) HTTPSObservation {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return HTTPSObservation{Observation: Observation{State: "unknown", Reason: "response_error"}}
	}
	response, err := p.client.Do(request)
	if err != nil {
		return HTTPSObservation{Observation: Observation{State: "unreachable", Reason: networkReason(err)}}
	}
	defer response.Body.Close()
	result := HTTPSObservation{Observation: Observation{State: "unexpected_response", Reason: "unexpected_status"}, HTTPStatus: response.StatusCode}
	if response.ContentLength > MaxResponseBytes {
		result.Reason = "response_too_large"
		return result
	}
	size, err := io.Copy(io.Discard, io.LimitReader(response.Body, MaxResponseBytes+1))
	if size > MaxResponseBytes {
		result.Reason = "response_too_large"
		return result
	}
	if err != nil {
		result.State, result.Reason = "unknown", networkReason(err)
		return result
	}
	if response.StatusCode == http.StatusOK {
		result.State, result.Reason = "responding", "observed"
	}
	return result
}

func networkReason(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var certificateError *tls.CertificateVerificationError
	var unknownAuthority x509.UnknownAuthorityError
	var hostnameError x509.HostnameError
	if errors.As(err, &certificateError) || errors.As(err, &unknownAuthority) || errors.As(err, &hostnameError) {
		return "tls_error"
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		if networkError.Timeout() {
			return "timeout"
		}
		return "network_error"
	}
	return "response_error"
}

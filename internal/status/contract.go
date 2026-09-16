package status

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"regexp"
	"time"
	"unicode/utf8"
)

const (
	MaxRequestBytes  = 4096
	OperationTimeout = 8 * time.Second
	ProviderTimeout  = 3 * time.Second
)

type Request struct {
	Version   int    `json:"version"`
	Action    string `json:"action"`
	RequestID string `json:"request_id"`
}

type Observation struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
}

type SSMObservation struct {
	Observation
	LastPingAt *time.Time `json:"last_ping_at,omitempty"`
}

type HTTPSObservation struct {
	Observation
	HTTPStatus int `json:"http_status,omitempty"`
}

type Error struct {
	Code string `json:"code"`
}

type Response struct {
	Version    int               `json:"version"`
	Kind       string            `json:"kind"`
	RequestID  string            `json:"request_id,omitempty"`
	ObservedAt time.Time         `json:"observed_at"`
	Host       *Observation      `json:"host,omitempty"`
	SSM        *SSMObservation   `json:"ssm,omitempty"`
	HTTPS      *HTTPSObservation `json:"https,omitempty"`
	Error      *Error            `json:"error,omitempty"`
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-8][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

// DecodeRequest accepts one exact v1 object; duplicate and differently-cased keys are invalid.
func DecodeRequest(raw []byte) (Request, string) {
	var request Request
	if len(raw) > MaxRequestBytes || !utf8.Valid(raw) {
		return request, "invalid_request"
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return request, "invalid_request"
	}
	seen := make(map[string]bool, 3)
	for decoder.More() {
		token, err = decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return Request{}, "invalid_request"
		}
		seen[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil || bytes.Equal(value, []byte("null")) {
			return Request{}, "invalid_request"
		}
		switch key {
		case "version":
			err = json.Unmarshal(value, &request.Version)
		case "action":
			err = json.Unmarshal(value, &request.Action)
		case "request_id":
			err = json.Unmarshal(value, &request.RequestID)
		default:
			return Request{}, "invalid_request"
		}
		if err != nil {
			return Request{}, "invalid_request"
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return Request{}, "invalid_request"
	}
	if _, err = decoder.Token(); err != io.EOF || len(seen) != 3 {
		return Request{}, "invalid_request"
	}
	if !uuidPattern.MatchString(request.RequestID) {
		return Request{}, "invalid_request_id"
	}
	if request.Version != 1 {
		return request, "unsupported_version"
	}
	if request.Action != "get_status" {
		return request, "unsupported_action"
	}
	return request, ""
}

type Handler struct {
	Build func(context.Context) (*Service, string)
}

// Invoke preserves the original Lambda payload bytes for strict request validation.
func (h Handler) Invoke(ctx context.Context, raw []byte) ([]byte, error) {
	response, err := h.Handle(ctx, raw)
	if err != nil {
		return nil, err
	}
	return json.Marshal(response)
}

func (h Handler) Handle(ctx context.Context, raw json.RawMessage) (Response, error) {
	request, code := DecodeRequest(raw)
	if code != "" {
		return ErrorResponse(request.RequestID, code), nil
	}
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	service, code := h.Build(ctx)
	if code != "" {
		return ErrorResponse(request.RequestID, code), nil
	}
	return service.Observe(ctx, request.RequestID), nil
}

func ErrorResponse(requestID, code string) Response {
	return Response{Version: 1, Kind: "error", RequestID: requestID, ObservedAt: time.Now().UTC(), Error: &Error{Code: code}}
}

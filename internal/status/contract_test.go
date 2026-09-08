package status

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/lambda"
)

const testRequestID = "8595c657-1fce-4730-89c7-711cf81cb203"
const validRequest = `{"version":1,"action":"get_status","request_id":"` + testRequestID + `"}`

func TestRejectRequestBeforeBuildingProviders(t *testing.T) {
	tests := []struct{ name, raw, code string }{
		{"malformed", `{`, "invalid_request"},
		{"invalid UTF-8", strings.Replace(validRequest, "get_status", "get_status"+string([]byte{0xff}), 1), "invalid_request"},
		{"null", `null`, "invalid_request"},
		{"array", `[]`, "invalid_request"},
		{"missing", `{"version":1,"action":"get_status"}`, "invalid_request"},
		{"unknown field", strings.TrimSuffix(validRequest, "}") + `,"instance_id":"i-fffffffffffffffff"}`, "invalid_request"},
		{"caller identity", strings.TrimSuffix(validRequest, "}") + `,"email":"admin@example.com"}`, "invalid_request"},
		{"duplicate", strings.TrimSuffix(validRequest, "}") + `,"action":"get_status"}`, "invalid_request"},
		{"case variant", strings.Replace(validRequest, `"action"`, `"Action"`, 1), "invalid_request"},
		{"trailing object", validRequest + ` {}`, "invalid_request"},
		{"trailing junk", validRequest + ` x`, "invalid_request"},
		{"wrong type", strings.Replace(validRequest, `"version":1`, `"version":"1"`, 1), "invalid_request"},
		{"null field", strings.Replace(validRequest, `"version":1`, `"version":null`, 1), "invalid_request"},
		{"fractional version", strings.Replace(validRequest, `"version":1`, `"version":1.0`, 1), "invalid_request"},
		{"unsupported version", strings.Replace(validRequest, `"version":1`, `"version":2`, 1), "unsupported_version"},
		{"mutating action", strings.Replace(validRequest, `get_status`, `end_session`, 1), "unsupported_action"},
		{"bad ID", strings.Replace(validRequest, testRequestID, "not-a-uuid", 1), "invalid_request_id"},
		{"nil UUID", strings.Replace(validRequest, testRequestID, "00000000-0000-0000-0000-000000000000", 1), "invalid_request_id"},
		{"oversized", validRequest + strings.Repeat(" ", MaxRequestBytes), "invalid_request"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := Handler{Build: func(context.Context) (*Service, string) {
				t.Fatal("providers built for rejected request")
				return nil, ""
			}}
			response, err := handler.Handle(context.Background(), json.RawMessage(test.raw))
			if err != nil || response.Kind != "error" || response.Error.Code != test.code {
				t.Fatalf("response=%+v err=%v", response, err)
			}
			if response.Host != nil || response.SSM != nil || response.HTTPS != nil || response.ObservedAt.IsZero() {
				t.Fatalf("invalid error envelope: %+v", response)
			}
		})
	}
}

func TestHandlerEchoAndBuildError(t *testing.T) {
	handler := Handler{Build: func(ctx context.Context) (*Service, string) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("missing operation deadline")
		}
		return nil, "invalid_configuration"
	}}
	response, err := handler.Handle(context.Background(), json.RawMessage(" \n"+validRequest+"\n"))
	if err != nil || response.RequestID != testRequestID || response.Error.Code != "invalid_configuration" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestServerConfiguration(t *testing.T) {
	valid := Config{Region: "us-west-2", InstanceID: "i-0123456789abcdef0", HTTPSURL: "https://foundry.example.com:30000/join"}
	if !valid.Valid() {
		t.Fatal("valid config rejected")
	}
	for _, region := range []string{"", " us-west-2", "us-west-2\n"} {
		c := valid
		c.Region = region
		if c.Valid() {
			t.Errorf("accepted region %q", region)
		}
	}
	for _, id := range []string{"", "i-*", "i-0123456789abcdef0,i-fffffffffffffffff"} {
		c := valid
		c.InstanceID = id
		if c.Valid() {
			t.Errorf("accepted instance %q", id)
		}
	}
	for _, endpoint := range []string{"", "http://example.com/join", "https://user:secret@example.com/join", "https://example.com/", "https://example.com/join?x=1", "https://example.com/join?", "https://example.com/join#x", "https://example.com/%6Aoin", "https://example.com:99999/join", "https:///join"} {
		c := valid
		c.HTTPSURL = endpoint
		if c.Valid() {
			t.Errorf("accepted endpoint %q", endpoint)
		}
	}
	values := map[string]string{"FOUNDRY_AWS_REGION": valid.Region, "FOUNDRY_INSTANCE_ID": valid.InstanceID, "FOUNDRY_HTTPS_URL": valid.HTTPSURL}
	if c, ok := ConfigFromEnvironment(func(key string) string { return values[key] }); !ok || c != valid {
		t.Fatalf("config=%+v valid=%v", c, ok)
	}
}

func TestSuccessfulWireEnvelopeHasOnlyIndependentObservations(t *testing.T) {
	handler := Handler{Build: func(context.Context) (*Service, string) { return testService(t), "" }}
	response, err := handler.Handle(context.Background(), json.RawMessage(validRequest))
	if err != nil || response.Kind != "status" || response.Version != 1 || response.RequestID != testRequestID || response.ObservedAt.IsZero() {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope) != 7 {
		t.Fatalf("unexpected envelope: %s", raw)
	}
	for _, key := range []string{"version", "kind", "request_id", "observed_at", "host", "ssm", "https"} {
		if _, ok := envelope[key]; !ok {
			t.Errorf("missing %s: %s", key, raw)
		}
	}
}

func TestLambdaHandlerPreservesOriginalPayload(t *testing.T) {
	for _, test := range []struct{ name, raw, code string }{
		{"trailing junk", validRequest + " junk", "invalid_request"},
		{"trailing object", validRequest + " {}", "invalid_request"},
		{"oversized whitespace", strings.Repeat(" ", MaxRequestBytes) + validRequest, "invalid_request"},
		{"malformed", "{", "invalid_request"},
		{"duplicate keys", strings.TrimSuffix(validRequest, "}") + `,"version":1}`, "invalid_request"},
		{"valid", validRequest, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			builds := 0
			handler := Handler{Build: func(context.Context) (*Service, string) {
				builds++
				return testService(t), ""
			}}
			raw, err := lambda.NewHandler(handler).Invoke(context.Background(), []byte(test.raw))
			if err != nil {
				t.Fatalf("unexpected Lambda FunctionError: %v", err)
			}
			var response Response
			if err := json.Unmarshal(raw, &response); err != nil {
				t.Fatal(err)
			}
			if test.code != "" {
				if builds != 0 || response.Kind != "error" || response.Error == nil || response.Error.Code != test.code {
					t.Fatalf("builds=%d response=%s", builds, raw)
				}
			} else if builds != 1 || response.Kind != "status" || response.RequestID != testRequestID {
				t.Fatalf("builds=%d response=%s", builds, raw)
			}
		})
	}
}

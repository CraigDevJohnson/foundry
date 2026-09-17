package app

import (
	"context"
	"testing"
)

func TestInvalidInputAndMissingConfigurationNeedNoCredentials(t *testing.T) {
	for _, name := range []string{"FOUNDRY_AWS_REGION", "FOUNDRY_INSTANCE_ID", "FOUNDRY_HTTPS_URL"} {
		t.Setenv(name, "")
	}
	for _, test := range []struct{ raw, code string }{
		{`{"action":"start_session"}`, "invalid_request"},
		{`{"version":1,"action":"get_status","request_id":"8595c657-1fce-4730-89c7-711cf81cb203"}`, "invalid_configuration"},
	} {
		response, err := Handler().Handle(context.Background(), []byte(test.raw))
		if err != nil || response.Kind != "error" || response.Error.Code != test.code {
			t.Fatalf("response=%+v err=%v", response, err)
		}
	}
}

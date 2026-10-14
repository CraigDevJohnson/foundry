package status

import (
	"strings"
	"testing"
)

func TestServerConfiguration(t *testing.T) {
	valid := Config{Region: "us-west-2", InstanceID: "i-0123456789abcdef0", HTTPSURL: "https://foundry.example.com:30000/join"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	refused := func(c Config, variable string) {
		t.Helper()
		if err := c.Validate(); err == nil || !strings.HasPrefix(err.Error(), variable+" ") {
			t.Errorf("config %+v: err=%v, want one naming %s", c, err, variable)
		}
	}
	for _, region := range []string{"", " us-west-2", "us-west-2\n"} {
		c := valid
		c.Region = region
		refused(c, "FOUNDRY_AWS_REGION")
	}
	for _, id := range []string{"", "i-*", "i-0123456789abcdef0,i-fffffffffffffffff"} {
		c := valid
		c.InstanceID = id
		refused(c, "FOUNDRY_INSTANCE_ID")
	}
	for _, endpoint := range []string{"", "http://example.com/join", "https://user:secret@example.com/join", "https://example.com/", "https://example.com/join?x=1", "https://example.com/join?", "https://example.com/join#x", "https://example.com/%6Aoin", "https://example.com:99999/join", "https:///join"} {
		c := valid
		c.HTTPSURL = endpoint
		refused(c, "FOUNDRY_HTTPS_URL")
	}
	values := map[string]string{"FOUNDRY_AWS_REGION": valid.Region, "FOUNDRY_INSTANCE_ID": valid.InstanceID, "FOUNDRY_HTTPS_URL": valid.HTTPSURL}
	if c, err := ConfigFromEnvironment(func(key string) string { return values[key] }); err != nil || c != valid {
		t.Fatalf("config=%+v err=%v", c, err)
	}
	if _, err := ConfigFromEnvironment(func(string) string { return "" }); err == nil {
		t.Fatal("empty environment accepted")
	}
}

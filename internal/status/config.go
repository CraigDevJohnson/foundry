package status

import (
	"errors"
	"net/url"
	"regexp"
	"strconv"
)

// Config names the one host the controller observes. It comes only from the
// environment: no request can choose an instance, Region or URL.
type Config struct {
	Region     string
	InstanceID string
	HTTPSURL   string
}

var (
	regionPattern   = regexp.MustCompile(`^[a-z]{2}(?:-[a-z]+)+-[0-9]+$`)
	instancePattern = regexp.MustCompile(`^i-(?:[0-9a-f]{8}|[0-9a-f]{17})$`)
)

// ConfigFromEnvironment reads FOUNDRY_AWS_REGION, FOUNDRY_INSTANCE_ID and
// FOUNDRY_HTTPS_URL, all required, and says which one is invalid.
func ConfigFromEnvironment(getenv func(string) string) (Config, error) {
	config := Config{Region: getenv("FOUNDRY_AWS_REGION"), InstanceID: getenv("FOUNDRY_INSTANCE_ID"), HTTPSURL: getenv("FOUNDRY_HTTPS_URL")}
	return config, config.Validate()
}

func (c Config) Validate() error {
	if !regionPattern.MatchString(c.Region) {
		return errors.New("FOUNDRY_AWS_REGION must be a Region such as us-west-2")
	}
	if !instancePattern.MatchString(c.InstanceID) {
		return errors.New("FOUNDRY_INSTANCE_ID must be one EC2 instance ID")
	}
	if !joinURL(c.HTTPSURL) {
		return errors.New("FOUNDRY_HTTPS_URL must be exactly https://host[:port]/join")
	}
	return nil
}

func joinURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Path != "/join" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	return true
}

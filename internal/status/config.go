package status

import (
	"net/url"
	"regexp"
	"strconv"
)

type Config struct {
	Region     string
	InstanceID string
	HTTPSURL   string
}

var (
	regionPattern   = regexp.MustCompile(`^[a-z]{2}(?:-[a-z]+)+-[0-9]+$`)
	instancePattern = regexp.MustCompile(`^i-(?:[0-9a-f]{8}|[0-9a-f]{17})$`)
)

func ConfigFromEnvironment(getenv func(string) string) (Config, bool) {
	config := Config{Region: getenv("FOUNDRY_AWS_REGION"), InstanceID: getenv("FOUNDRY_INSTANCE_ID"), HTTPSURL: getenv("FOUNDRY_HTTPS_URL")}
	return config, config.Valid()
}

func (c Config) Valid() bool {
	if !regionPattern.MatchString(c.Region) || !instancePattern.MatchString(c.InstanceID) {
		return false
	}
	u, err := url.Parse(c.HTTPSURL)
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

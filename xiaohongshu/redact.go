package xiaohongshu

import (
	"net/url"
	"regexp"
)

var sensitiveTextPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(xsec_token=)[^&\s"']+`),
	regexp.MustCompile(`(?i)("xsec_token"\s*:\s*")[^"]+`),
	regexp.MustCompile(`(?i)(xsecToken=)[^&\s"']+`),
	regexp.MustCompile(`(?i)("xsecToken"\s*:\s*")[^"]+`),
	regexp.MustCompile(`(?i)(access_token=)[^&\s"']+`),
	regexp.MustCompile(`(?i)("access_token"\s*:\s*")[^"]+`),
}

func redactSensitiveURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return redactSensitiveText(raw)
	}
	q := u.Query()
	for _, key := range []string{"xsec_token", "xsecToken", "token", "access_token"} {
		if q.Has(key) {
			q.Set(key, "***")
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func redactSensitiveText(value string) string {
	for _, pattern := range sensitiveTextPatterns {
		value = pattern.ReplaceAllString(value, `${1}***`)
	}
	return value
}

package xiaohongshu

import (
	"net/url"
	"regexp"
	"strings"
)

var sensitiveTextPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(xsec_token=)[^&\s"']+`),
	regexp.MustCompile(`(?i)("xsec_token"\s*:\s*")[^"]+`),
	regexp.MustCompile(`(?i)(xsecToken=)[^&\s"']+`),
	regexp.MustCompile(`(?i)("xsecToken"\s*:\s*")[^"]+`),
	regexp.MustCompile(`(?i)(access_token=)[^&\s"']+`),
	regexp.MustCompile(`(?i)("access_token"\s*:\s*")[^"]+`),
	regexp.MustCompile(`(?i)(refresh_token=)[^&\s"']+`),
	regexp.MustCompile(`(?i)("refresh_token"\s*:\s*")[^"]+`),
	regexp.MustCompile(`(?i)(web_session=)[^&\s"']+`),
	regexp.MustCompile(`(?i)("web_session"\s*:\s*")[^"]+`),
	regexp.MustCompile(`(?i)(["']?authorization["']?\s*[:=]\s*["']?bearer\s+)[^&\s"',}]+`),
	regexp.MustCompile(`(?i)(["']?(?:token|session|a1)["']?\s*[:=]\s*["']?)[^&\s"',}]+`),
}

var sensitiveURLQueryKeys = map[string]struct{}{
	"xsec_token":    {},
	"xsectoken":     {},
	"token":         {},
	"access_token":  {},
	"refresh_token": {},
	"web_session":   {},
	"session":       {},
	"a1":            {},
	"authorization": {},
}

func redactSensitiveURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return redactSensitiveText(raw)
	}
	q := u.Query()
	for key := range q {
		if _, ok := sensitiveURLQueryKeys[strings.ToLower(key)]; ok {
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

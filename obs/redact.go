package obs

import "regexp"

var (
	// Bearer / sk- / xoxb- / ghp_ etc. — anything that looks like a credential.
	redactPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._\-+/=]{8,}`),
		regexp.MustCompile(`(?i)\b(sk|xoxb|ghp|ghs|gho|github_pat|api[_-]?key)[-_=:][A-Za-z0-9._\-+/=]{8,}`),
		// JWT-ish: three base64 segments.
		regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\b`),
	}
)

func Redact(s string) string {
	out := s
	for _, re := range redactPatterns {
		out = re.ReplaceAllString(out, "[REDACTED]")
	}
	return out
}

package runner

import "regexp"

// Patterns for redacting sensitive values from terraform plan output.
var (
	// Attribute values for sensitive-looking keys in plan output.
	// Matches: password = "foo" / secret_key = "bar" / etc.
	sensitiveAttrPattern = regexp.MustCompile(
		`(?i)((?:password|secret|token|key|private_key|credentials|api_key|access_key|secret_key|connection_string)\s*=\s*)"[^"]*"`)
	// Matches the common plan format: key = "old" -> "new"
	sensitiveAttrTransitionPattern = regexp.MustCompile(
		`(?i)((?:password|secret|token|key|private_key|credentials|api_key|access_key|secret_key|connection_string)\s*=\s*)"[^"]*"(\s*->\s*)"[^"]*"`)
	// Matches unquoted sensitive assignments: token = abc123...
	sensitiveAttrUnquotedPattern = regexp.MustCompile(
		`(?i)((?:password|secret|token|key|private_key|credentials|api_key|access_key|secret_key|connection_string)\s*=\s*)([A-Za-z0-9+/=._:-]{12,})`)

	// AWS access key IDs (always start with AKIA).
	awsAccessKeyPattern = regexp.MustCompile(`AKIA[0-9A-Z]{16}`)

	// Connection strings with embedded credentials: proto://user:pass@host
	connStringPattern = regexp.MustCompile(`(://[^:]+:)[^@]+(@)`)
	// JWT tokens.
	jwtPattern = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`)
	// Bearer tokens in logs/plan output.
	bearerTokenPattern = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._-]{20,}`)
	// PEM private keys in multiline output.
	pemPrivateKeyPattern = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`)
)

// RedactPlanOutput scrubs known sensitive patterns from terraform plan output.
// This is best-effort — it catches common cases, not 100% of secrets.
func RedactPlanOutput(output string) string {
	output = sensitiveAttrTransitionPattern.ReplaceAllString(output, `${1}"REDACTED"${2}"REDACTED"`)
	output = sensitiveAttrPattern.ReplaceAllString(output, `${1}"REDACTED"`)
	output = sensitiveAttrUnquotedPattern.ReplaceAllString(output, "${1}REDACTED")
	output = awsAccessKeyPattern.ReplaceAllString(output, "REDACTED")
	output = connStringPattern.ReplaceAllString(output, "${1}REDACTED${2}")
	output = jwtPattern.ReplaceAllString(output, "REDACTED")
	output = bearerTokenPattern.ReplaceAllString(output, "${1}REDACTED")
	output = pemPrivateKeyPattern.ReplaceAllString(output, "REDACTED_PRIVATE_KEY")
	return output
}

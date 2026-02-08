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

	// AWS access key IDs (always start with AKIA).
	awsAccessKeyPattern = regexp.MustCompile(`AKIA[0-9A-Z]{16}`)

	// Connection strings with embedded credentials: proto://user:pass@host
	connStringPattern = regexp.MustCompile(`(://[^:]+:)[^@]+(@)`)
)

// RedactPlanOutput scrubs known sensitive patterns from terraform plan output.
// This is best-effort — it catches common cases, not 100% of secrets.
func RedactPlanOutput(output string) string {
	output = sensitiveAttrTransitionPattern.ReplaceAllString(output, `${1}"REDACTED"${2}"REDACTED"`)
	output = sensitiveAttrPattern.ReplaceAllString(output, `${1}"REDACTED"`)
	output = awsAccessKeyPattern.ReplaceAllString(output, "REDACTED")
	output = connStringPattern.ReplaceAllString(output, "${1}REDACTED${2}")
	return output
}

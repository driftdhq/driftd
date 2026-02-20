package runner

import "testing"

func TestRedactPlanOutput(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect string
	}{
		{
			name:   "no secrets",
			input:  "No changes. Your infrastructure matches the configuration.",
			expect: "No changes. Your infrastructure matches the configuration.",
		},
		{
			name:   "password attribute",
			input:  `  + password = "super-secret-123"`,
			expect: `  + password = "REDACTED"`,
		},
		{
			name:   "secret_key attribute",
			input:  `  ~ secret_key = "old-value" -> "new-value"`,
			expect: `  ~ secret_key = "REDACTED" -> "REDACTED"`,
		},
		{
			name:   "api_key case insensitive",
			input:  `  + API_KEY = "abc123"`,
			expect: `  + API_KEY = "REDACTED"`,
		},
		{
			name:   "token attribute",
			input:  `  + token = "ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"`,
			expect: `  + token = "REDACTED"`,
		},
		{
			name:   "connection_string attribute",
			input:  `  + connection_string = "postgres://user:pass@host/db"`,
			expect: `  + connection_string = "REDACTED"`,
		},
		{
			name:   "aws access key ID",
			input:  `  access_key_id = AKIAIOSFODNN7EXAMPLE`,
			expect: `  access_key_id = REDACTED`,
		},
		{
			name:   "aws access key in text",
			input:  `The key is AKIAIOSFODNN7EXAMPLE in the config`,
			expect: `The key is REDACTED in the config`,
		},
		{
			name:   "connection string with credentials",
			input:  `  url = "mysql://admin:passw0rd@db.example.com:3306/mydb"`,
			expect: `  url = "mysql://admin:REDACTED@db.example.com:3306/mydb"`,
		},
		{
			name:   "private_key attribute",
			input:  `  + private_key = "-----BEGIN RSA PRIVATE KEY-----\nMIIE..."`,
			expect: `  + private_key = "REDACTED"`,
		},
		{
			name:   "credentials attribute",
			input:  `  + credentials = "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9"`,
			expect: `  + credentials = "REDACTED"`,
		},
		{
			name:   "plan summary not redacted",
			input:  "Plan: 3 to add, 1 to change, 0 to destroy.",
			expect: "Plan: 3 to add, 1 to change, 0 to destroy.",
		},
		{
			name:   "multiple sensitive fields",
			input:  "  password = \"foo\"\n  token = \"bar\"\n  name = \"ok\"",
			expect: "  password = \"REDACTED\"\n  token = \"REDACTED\"\n  name = \"ok\"",
		},
		{
			name:   "empty input",
			input:  "",
			expect: "",
		},
		{
			name:   "non-sensitive key-like word not redacted",
			input:  `  name = "donkey"`,
			expect: `  name = "donkey"`,
		},
		{
			name:   "access_key attribute",
			input:  `  + access_key = "AKIAIOSFODNN7EXAMPLE"`,
			expect: `  + access_key = "REDACTED"`,
		},
		{
			name:   "multiple aws keys in one line",
			input:  `old=AKIAIOSFODNN7EXAMPLE new=AKIAZ3456789ABCDEFGH`,
			expect: `old=REDACTED new=REDACTED`,
		},
		{
			name: "realistic plan output",
			input: `  # aws_db_instance.main will be updated in-place
  ~ resource "aws_db_instance" "main" {
      ~ password         = "hunter2" -> "new-password"
        name             = "production-db"
        engine           = "postgres"
      + secret_key       = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
    }

Plan: 0 to add, 1 to change, 0 to destroy.`,
			expect: `  # aws_db_instance.main will be updated in-place
  ~ resource "aws_db_instance" "main" {
      ~ password         = "REDACTED" -> "REDACTED"
        name             = "production-db"
        engine           = "postgres"
      + secret_key       = "REDACTED"
    }

Plan: 0 to add, 1 to change, 0 to destroy.`,
		},
		{
			name:   "secret with equals in various spacing",
			input:  `  secret="topsecret"`,
			expect: `  secret="REDACTED"`,
		},
		{
			name:   "connection string postgres",
			input:  `  database_url = "postgres://myuser:myp4ss@db.host.com:5432/mydb"`,
			expect: `  database_url = "postgres://myuser:REDACTED@db.host.com:5432/mydb"`,
		},
		{
			name:   "jwt token redacted",
			input:  `  id_token = eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abc1234567890defghijklmnop`,
			expect: `  id_token = REDACTED`,
		},
		{
			name:   "unquoted secret redacted",
			input:  `  secret = abcdefghijklmnop1234567890`,
			expect: `  secret = REDACTED`,
		},
		{
			name:   "bearer token redacted",
			input:  `Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123456789.ABCDEF`,
			expect: `Authorization: Bearer REDACTED`,
		},
		{
			name: "pem private key redacted",
			input: `private_key = <<EOF
-----BEGIN PRIVATE KEY-----
MIIEvQIBADANBgkqhkiG9w0BAQEFAASC...
-----END PRIVATE KEY-----
EOF`,
			expect: `private_key = <<EOF
REDACTED_PRIVATE_KEY
EOF`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedactPlanOutput(tt.input)
			if got != tt.expect {
				t.Errorf("RedactPlanOutput():\n  got:    %q\n  expect: %q", got, tt.expect)
			}
		})
	}
}

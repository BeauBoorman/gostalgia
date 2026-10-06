package sdk

import (
	"strings"
	"testing"
)

func FuzzParseManifest(f *testing.F) {
	seeds := []string{
		`{"id":"com.test.app","name":"App","version":"1.0.0","entrypoint":"app","permissions":["ipc"]}`,
		`{"id":"com.test.ext","name":"External","version":"1.0.0","mode":"external","executable":"/bin/ext","protocol_version":1,"permissions":["ipc"]}`,
		`{"id":"com.test.sandbox","name":"Sandbox","version":"1.0.0","mode":"external","executable":"/bin/ext","isolation":"sandbox","permissions":["ipc"]}`,
		`{"id":"com.test.strict","name":"Strict","version":"1.0.0","mode":"external","executable":"/bin/ext","isolation":"strict","permissions":["ipc"]}`,
		`{}`,
		`null`,
		`{"id":"bad_id"}`,
		`{"id":"com.test.app","permissions":["admin"]}`,
		`{"id":"com.test.app","mode":"inproc","isolation":"strict"}`,
		`{"id":"com.test.app","version":"v1.0.0"}`,
		`{"id":"com.test.app","name":"App","version":"1.0.0","entrypoint":"app","unknown_field":true}`,
		`{"id":"com.test.app"}{"id":"com.test.app2"}`,
		strings.Repeat(`{"a":`, 100) + `1` + strings.Repeat(`}`, 100),
	}
	for _, seed := range seeds {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := ParseManifest(data)
		if err == nil {
			// Invariant: parsed manifest must satisfy all validation rules
			if err := m.Validate(); err != nil {
				t.Fatalf("ParseManifest returned manifest that failed Validate: %v", err)
			}
			if !idPattern.MatchString(m.ID) {
				t.Fatalf("ParseManifest accepted invalid ID: %s", m.ID)
			}
			if !versionPattern.MatchString(m.Version) {
				t.Fatalf("ParseManifest accepted invalid version: %s", m.Version)
			}
			if m.Mode == ModeInProc && (m.Isolation == IsolationSandbox || m.Isolation == IsolationStrict) {
				t.Fatalf("ParseManifest accepted inproc app with sandbox/strict isolation")
			}
			for _, p := range m.Permissions {
				if p == "admin" {
					t.Fatalf("ParseManifest accepted admin permission in manifest")
				}
			}
		}
	})
}

func TestAdversarialManifests(t *testing.T) {
	testCases := []struct {
		name     string
		json     string
		errMatch string
	}{
		{
			name:     "Command injection in reverse-DNS ID",
			json:     `{"id":"com.test;rm -rf /","name":"Injected","version":"1.0.0","entrypoint":"app"}`,
			errMatch: "reverse-DNS name",
		},
		{
			name:     "Path traversal in reverse-DNS ID",
			json:     `{"id":"../../etc/passwd","name":"Traversal","version":"1.0.0","entrypoint":"app"}`,
			errMatch: "reverse-DNS name",
		},
		{
			name:     "Uppercase letters in reverse-DNS ID",
			json:     `{"id":"Com.Test.App","name":"Upper","version":"1.0.0","entrypoint":"app"}`,
			errMatch: "reverse-DNS name",
		},
		{
			name:     "Single component ID without dot",
			json:     `{"id":"myapp","name":"Single","version":"1.0.0","entrypoint":"app"}`,
			errMatch: "reverse-DNS name",
		},
		{
			name:     "Trailing dot in ID",
			json:     `{"id":"com.test.app.","name":"Trailing","version":"1.0.0","entrypoint":"app"}`,
			errMatch: "reverse-DNS name",
		},
		{
			name:     "Empty name string",
			json:     `{"id":"com.test.app","name":"   ","version":"1.0.0","entrypoint":"app"}`,
			errMatch: "name is required",
		},
		{
			name:     "Non-semver version (missing patch)",
			json:     `{"id":"com.test.app","name":"App","version":"1.0","entrypoint":"app"}`,
			errMatch: "MAJOR.MINOR.PATCH",
		},
		{
			name:     "Non-semver version with v prefix",
			json:     `{"id":"com.test.app","name":"App","version":"v1.0.0","entrypoint":"app"}`,
			errMatch: "MAJOR.MINOR.PATCH",
		},
		{
			name:     "Negative version numbers",
			json:     `{"id":"com.test.app","name":"App","version":"-1.0.0","entrypoint":"app"}`,
			errMatch: "MAJOR.MINOR.PATCH",
		},
		{
			name:     "Operator admin capability declared",
			json:     `{"id":"com.test.app","name":"App","version":"1.0.0","entrypoint":"app","permissions":["admin"]}`,
			errMatch: "unknown or reserved permission",
		},
		{
			name:     "Duplicate permissions declared",
			json:     `{"id":"com.test.app","name":"App","version":"1.0.0","entrypoint":"app","permissions":["fs.read","fs.read"]}`,
			errMatch: "duplicate permission",
		},
		{
			name:     "Inproc requesting sandbox isolation",
			json:     `{"id":"com.test.app","name":"App","version":"1.0.0","mode":"inproc","entrypoint":"app","isolation":"sandbox"}`,
			errMatch: "does not support \"sandbox\" isolation",
		},
		{
			name:     "Inproc requesting strict isolation",
			json:     `{"id":"com.test.app","name":"App","version":"1.0.0","mode":"inproc","entrypoint":"app","isolation":"strict"}`,
			errMatch: "does not support \"strict\" isolation",
		},
		{
			name:     "Inproc specifying executable binary",
			json:     `{"id":"com.test.app","name":"App","version":"1.0.0","mode":"inproc","entrypoint":"app","executable":"/bin/sh"}`,
			errMatch: "executable is not permitted for inproc mode",
		},
		{
			name:     "External missing executable",
			json:     `{"id":"com.test.ext","name":"Ext","version":"1.0.0","mode":"external","executable":""}`,
			errMatch: "executable is required for external mode",
		},
		{
			name:     "Unknown isolation level",
			json:     `{"id":"com.test.ext","name":"Ext","version":"1.0.0","mode":"external","executable":"/bin/ext","isolation":"hypervisor"}`,
			errMatch: "unknown isolation",
		},
		{
			name:     "Unknown unexpected JSON field",
			json:     `{"id":"com.test.app","name":"App","version":"1.0.0","entrypoint":"app","superuser":true}`,
			errMatch: "unknown field",
		},
		{
			name:     "Trailing content after JSON object",
			json:     `{"id":"com.test.app","name":"App","version":"1.0.0","entrypoint":"app"}{"extra":1}`,
			errMatch: "exactly one JSON object",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseManifest([]byte(tc.json))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.errMatch)
			}
			if !strings.Contains(err.Error(), tc.errMatch) {
				t.Fatalf("error %q does not contain expected %q", err.Error(), tc.errMatch)
			}
		})
	}
}

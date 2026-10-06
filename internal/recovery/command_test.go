package recovery_test

import (
	"testing"

	"gostalgia/internal/recovery"
)

func TestParseCommand(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantMethod string
		wantErr    bool
	}{
		{
			name:       "empty args",
			args:       []string{},
			wantMethod: "",
			wantErr:    true,
		},
		{
			name:       "export default",
			args:       []string{"export"},
			wantMethod: "backup/export",
			wantErr:    false,
		},
		{
			name:       "export with flags",
			args:       []string{"export", "/backup.gbar", "--profile", "alice", "--description", "my backup", "--no-system"},
			wantMethod: "backup/export",
			wantErr:    false,
		},
		{
			name:       "inspect",
			args:       []string{"inspect", "/backup.gbar"},
			wantMethod: "backup/inspect",
			wantErr:    false,
		},
		{
			name:       "preview",
			args:       []string{"preview", "/backup.gbar"},
			wantMethod: "backup/preview",
			wantErr:    false,
		},
		{
			name:       "restore default abort",
			args:       []string{"restore", "/backup.gbar"},
			wantMethod: "backup/restore",
			wantErr:    false,
		},
		{
			name:       "restore with overwrite strategy",
			args:       []string{"restore", "/backup.gbar", "--strategy", "overwrite", "--profile", "guest"},
			wantMethod: "backup/restore",
			wantErr:    false,
		},
		{
			name:       "restore invalid strategy",
			args:       []string{"restore", "/backup.gbar", "--strategy", "invalid"},
			wantMethod: "",
			wantErr:    true,
		},
		{
			name:       "unknown action",
			args:       []string{"foo"},
			wantMethod: "",
			wantErr:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			method, params, err := recovery.ParseCommand(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (method=%s)", method)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if method != tc.wantMethod {
				t.Errorf("method = %q, want %q", method, tc.wantMethod)
			}
			if params == nil {
				t.Error("params should not be nil")
			}
		})
	}
}

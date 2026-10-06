package pkg

import "testing"

func TestParseCommand(t *testing.T) {
	tests := []struct {
		args   []string
		method string
		params Params
	}{
		{[]string{"list"}, "pkg/list", Params{}},
		{[]string{"inspect", "com.test.app"}, "pkg/inspect", Params{ID: "com.test.app"}},
		{[]string{"inspect", "--archive", "/users/guest/a.gpkg"}, "pkg/inspect", Params{Path: "/users/guest/a.gpkg"}},
		{[]string{"install", "/a.gpkg", "--confirm-permissions"}, "pkg/install", Params{Path: "/a.gpkg", ConfirmPermissions: true}},
		{[]string{"update", "/a.gpkg"}, "pkg/update", Params{Path: "/a.gpkg"}},
		{[]string{"rollback", "com.test.app", "--confirm-permissions"}, "pkg/rollback", Params{ID: "com.test.app", ConfirmPermissions: true}},
		{[]string{"uninstall", "com.test.app"}, "pkg/uninstall", Params{ID: "com.test.app"}},
	}
	for _, tt := range tests {
		method, params, err := ParseCommand(tt.args)
		if err != nil || method != tt.method || params != tt.params {
			t.Fatalf("%v: %s %+v %v", tt.args, method, params, err)
		}
	}
	for _, args := range [][]string{
		nil, {"list", "extra"}, {"list", "--confirm-permissions"}, {"install"}, {"install", "path", "--unknown"},
		{"update", "path", "--archive"}, {"inspect"}, {"inspect", "id", "--confirm-permissions"},
		{"uninstall", "id", "--confirm-permissions"}, {"rollback", "id", "extra"},
		{"install", "path", "--confirm-permissions", "--confirm-permissions"},
	} {
		if _, _, err := ParseCommand(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

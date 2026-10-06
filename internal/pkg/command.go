package pkg

import (
	"fmt"
	"strings"
)

const CommandUsage = "pkg list | inspect APP-ID | inspect --archive VFS-PATH | install VFS-PATH [--confirm-permissions] | update VFS-PATH [--confirm-permissions] | uninstall APP-ID | rollback APP-ID [--confirm-permissions]"

type Params struct {
	ID                 string `json:"id,omitempty"`
	Path               string `json:"path,omitempty"`
	ConfirmPermissions bool   `json:"confirm_permissions,omitempty"`
}

// ParseCommand is shared by gctl and the shell; paths are environment paths,
// never arbitrary host filenames.
func ParseCommand(args []string) (string, Params, error) {
	p := Params{}
	if len(args) == 0 {
		return "", p, fmt.Errorf("usage: %s", CommandUsage)
	}
	action := strings.ToLower(args[0])
	var positional []string
	archive := false
	for _, arg := range args[1:] {
		switch arg {
		case "--confirm-permissions":
			if p.ConfirmPermissions {
				return "", p, fmt.Errorf("duplicate confirmation flag")
			}
			p.ConfirmPermissions = true
		case "--archive":
			if archive {
				return "", p, fmt.Errorf("duplicate archive flag")
			}
			archive = true
		default:
			if strings.HasPrefix(arg, "--") {
				return "", p, fmt.Errorf("unknown package option %q", arg)
			}
			positional = append(positional, arg)
		}
	}
	valid := false
	switch action {
	case "list":
		valid = len(positional) == 0 && !archive && !p.ConfirmPermissions
	case "install", "update":
		valid = len(positional) == 1 && !archive
		if valid {
			p.Path = positional[0]
		}
	case "inspect":
		valid = len(positional) == 1 && !p.ConfirmPermissions
		if valid {
			if archive {
				p.Path = positional[0]
			} else {
				p.ID = positional[0]
			}
		}
	case "uninstall", "rollback":
		valid = len(positional) == 1 && !archive && (action == "rollback" || !p.ConfirmPermissions)
		if valid {
			p.ID = positional[0]
		}
	}
	if !valid {
		return "", Params{}, fmt.Errorf("usage: %s", CommandUsage)
	}
	return "pkg/" + action, p, nil
}

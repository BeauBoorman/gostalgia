package recovery

import (
	"fmt"
	"strings"
)

// CommandUsage describes the CLI and shell syntax for backup operations.
const CommandUsage = "backup export [PATH] [--profile ID] [--no-system] [--description DESC] | inspect PATH | preview PATH | restore PATH [--strategy abort|overwrite|skip] [--profile ID]"

// ExportParams represents parameters for backup export.
type ExportParams struct {
	Path           string   `json:"path,omitempty"`
	ProfileID      string   `json:"profile_id,omitempty"`
	IncludeSystem  *bool    `json:"include_system,omitempty"`
	Description    string   `json:"description,omitempty"`
	ExcludeSecrets []string `json:"exclude_secrets,omitempty"`
}

// PathParams represents parameters for backup inspect and preview.
type PathParams struct {
	Path string `json:"path"`
}

// RestoreParams represents parameters for backup restore.
type RestoreParams struct {
	Path          string           `json:"path"`
	Strategy      ConflictStrategy `json:"strategy,omitempty"`
	ProfileFilter string           `json:"profile_id,omitempty"`
}

// ParseCommand converts CLI/shell argument tokens into an IPC method and typed parameter payload.
func ParseCommand(args []string) (string, any, error) {
	if len(args) == 0 {
		return "", nil, fmt.Errorf("usage: %s", CommandUsage)
	}

	action := strings.ToLower(args[0])
	var positional []string

	// Flags
	var profileID string
	var description string
	var strategy ConflictStrategy
	var noSystem bool

	for i := 1; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--no-system":
			noSystem = true
		case "--profile":
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("--profile requires an argument")
			}
			i++
			profileID = args[i]
		case "--description":
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("--description requires an argument")
			}
			i++
			description = args[i]
		case "--strategy":
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("--strategy requires an argument (abort, overwrite, skip)")
			}
			i++
			strat := ConflictStrategy(strings.ToLower(args[i]))
			switch strat {
			case ConflictAbort, ConflictOverwrite, ConflictSkip:
				strategy = strat
			default:
				return "", nil, fmt.Errorf("invalid strategy %q (allowed: abort, overwrite, skip)", strat)
			}
		default:
			if strings.HasPrefix(arg, "--") {
				return "", nil, fmt.Errorf("unknown backup option %q", arg)
			}
			positional = append(positional, arg)
		}
	}

	switch action {
	case "export":
		if len(positional) > 1 {
			return "", nil, fmt.Errorf("usage: backup export [PATH] [--profile ID] [--no-system] [--description DESC]")
		}
		var p ExportParams
		if len(positional) == 1 {
			p.Path = positional[0]
		}
		p.ProfileID = profileID
		p.Description = description
		if noSystem {
			inc := false
			p.IncludeSystem = &inc
		}
		return "backup/export", p, nil

	case "inspect":
		if len(positional) != 1 || profileID != "" || description != "" || noSystem || strategy != "" {
			return "", nil, fmt.Errorf("usage: backup inspect PATH")
		}
		return "backup/inspect", PathParams{Path: positional[0]}, nil

	case "preview":
		if len(positional) != 1 || profileID != "" || description != "" || noSystem || strategy != "" {
			return "", nil, fmt.Errorf("usage: backup preview PATH")
		}
		return "backup/preview", PathParams{Path: positional[0]}, nil

	case "restore":
		if len(positional) != 1 || description != "" || noSystem {
			return "", nil, fmt.Errorf("usage: backup restore PATH [--strategy abort|overwrite|skip] [--profile ID]")
		}
		if strategy == "" {
			strategy = ConflictAbort
		}
		return "backup/restore", RestoreParams{
			Path:          positional[0],
			Strategy:      strategy,
			ProfileFilter: profileID,
		}, nil

	default:
		return "", nil, fmt.Errorf("unknown backup action %q; usage: %s", action, CommandUsage)
	}
}

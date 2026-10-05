package shell

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"unicode"
)

// Caller is the single environment API used by the experience layer.
type Caller interface {
	Call(context.Context, string, any, any) error
}

type appStatus struct {
	Manifest struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Version     string   `json:"version"`
		Description string   `json:"description"`
		Permissions []string `json:"permissions"`
	} `json:"manifest"`
	Running bool  `json:"running"`
	PID     int32 `json:"pid"`
}

type resultMsg struct {
	text string
	err  error
	cwd  string
	apps []appStatus
	quit bool
}

const helpText = `COMMAND CENTER
  apps                     installed apps and their grants
  launch / run APP-ID       start a manifest-declared app
  stop APP-ID               stop, clean up, retract routes
  echo MESSAGE              talk to the Echo demo
  call METHOD [JSON]        invoke any app or service route
  dir / ls [PATH]           browse the environment drive
  cd PATH                   change directory (C: is the VFS)
  type / cat PATH           read a file
  ps · status               processes · system dashboard
  cls / clear               clear the transcript
  exit                      leave shell (owned boot shuts down)
  shutdown                  shut down the environment

F2 app shelf · F3 stop selected · Tab complete · ↑↓ history
Paths accept /users/guest or C:\users\guest. Quote paths with spaces.`

// words handles quoted paths and messages. Backslashes remain literal for DOS
// paths. Raw call JSON is parsed separately so JSON escaping is unchanged.
func words(line string) ([]string, error) {
	var out []string
	var word strings.Builder
	var quote rune
	started := false
	for _, r := range line {
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			continue
		}
		switch {
		case r == '\'' || r == '"':
			quote = r
			started = true
		case unicode.IsSpace(r):
			if started {
				out = append(out, word.String())
				word.Reset()
				started = false
			}
		default:
			word.WriteRune(r)
			started = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unclosed quote")
	}
	if started {
		out = append(out, word.String())
	}
	return out, nil
}

func envPath(cwd, input string) string {
	input = strings.ReplaceAll(input, `\`, "/")
	if len(input) >= 2 && strings.EqualFold(input[:2], "c:") {
		input = "/" + strings.TrimPrefix(input[2:], "/")
	}
	if strings.HasPrefix(input, "/") {
		return path.Clean(input)
	}
	return path.Join(cwd, input)
}

func execute(ctx context.Context, c Caller, cwd, line string) resultMsg {
	result := resultMsg{cwd: cwd}
	text, next, quit, err := command(ctx, c, cwd, line)
	result.text, result.cwd, result.quit, result.err = text, next, quit, err
	if !quit {
		var apps []appStatus
		if refreshErr := c.Call(ctx, "app/list", nil, &apps); refreshErr != nil {
			if result.err == nil {
				result.err = fmt.Errorf("refresh apps: %w", refreshErr)
			}
		} else {
			result.apps = apps
		}
	}
	return result
}

func command(ctx context.Context, c Caller, cwd, line string) (string, string, bool, error) {
	ok := func(s string) (string, string, bool, error) { return s, cwd, false, nil }
	fail := func(err error) (string, string, bool, error) { return "", cwd, false, err }
	// Preserve everything after the raw IPC method as JSON, including quotes.
	head, tail, _ := strings.Cut(strings.TrimSpace(line), " ")
	if strings.EqualFold(head, "call") {
		tail = strings.TrimSpace(tail)
		method, raw, _ := strings.Cut(tail, " ")
		if method == "" {
			return fail(fmt.Errorf("usage: call METHOD [JSON]"))
		}
		var params any
		if strings.TrimSpace(raw) != "" {
			if err := json.Unmarshal([]byte(raw), &params); err != nil {
				return fail(fmt.Errorf("params must be JSON: %w", err))
			}
		}
		var out json.RawMessage
		if err := c.Call(ctx, method, params, &out); err != nil {
			return fail(err)
		}
		pretty, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return fail(err)
		}
		return ok(string(pretty))
	}
	args, err := words(line)
	if err != nil {
		return fail(err)
	}
	if len(args) == 0 {
		return ok("")
	}
	cmd, args := strings.ToLower(args[0]), args[1:]
	switch cmd {
	case "help", "?":
		if len(args) != 0 {
			return fail(fmt.Errorf("usage: help"))
		}
		return ok(helpText)
	case "exit", "quit":
		if len(args) != 0 {
			return fail(fmt.Errorf("usage: exit"))
		}
		return "", cwd, true, nil
	case "cls", "clear":
		if len(args) != 0 {
			return fail(fmt.Errorf("usage: cls"))
		}
		return ok("")
	case "apps":
		if len(args) != 0 {
			return fail(fmt.Errorf("usage: apps"))
		}
		var apps []appStatus
		if err := c.Call(ctx, "app/list", nil, &apps); err != nil {
			return fail(err)
		}
		var rows []string
		for _, a := range apps {
			state := "READY"
			if a.Running {
				state = fmt.Sprintf("LIVE · PID %d", a.PID)
			}
			rows = append(rows, fmt.Sprintf("%s  %s v%s\n  %s · caps: %s", a.Manifest.ID, a.Manifest.Name, a.Manifest.Version, state, strings.Join(a.Manifest.Permissions, ", ")))
		}
		return ok(strings.Join(rows, "\n"))
	case "launch", "run", "stop":
		if len(args) != 1 {
			return fail(fmt.Errorf("usage: %s APP-ID", cmd))
		}
		method := "app/launch"
		verb := "Launched"
		if cmd == "stop" {
			method, verb = "app/stop", "Stopped"
		}
		var out json.RawMessage
		if err := c.Call(ctx, method, map[string]string{"id": args[0]}, &out); err != nil {
			return fail(err)
		}
		return ok(verb + " " + args[0])
	case "echo":
		if len(args) == 0 {
			return fail(fmt.Errorf("usage: echo MESSAGE"))
		}
		var out struct {
			Msg    string `json:"msg"`
			Echoes int64  `json:"echoes"`
		}
		if err := c.Call(ctx, "app/com.gostalgia.echo/echo", map[string]string{"msg": strings.Join(args, " ")}, &out); err != nil {
			return fail(err)
		}
		return ok(fmt.Sprintf("%s   [echo #%d]", out.Msg, out.Echoes))
	case "ls", "dir", "cd":
		if len(args) > 1 || (cmd == "cd" && len(args) != 1) {
			return fail(fmt.Errorf("usage: %s PATH", cmd))
		}
		p := cwd
		if len(args) == 1 {
			p = envPath(cwd, args[0])
		}
		var out struct {
			Entries []struct {
				Name  string `json:"name"`
				IsDir bool   `json:"is_dir"`
				Size  int64  `json:"size"`
			} `json:"entries"`
		}
		if err := c.Call(ctx, "fs/list", map[string]string{"path": p}, &out); err != nil {
			return fail(err)
		}
		if cmd == "cd" {
			return "Directory: " + p, p, false, nil
		}
		rows := []string{"Directory of " + p}
		for _, e := range out.Entries {
			if e.IsDir {
				rows = append(rows, "  <DIR>       "+e.Name)
			} else {
				rows = append(rows, fmt.Sprintf("  %8d    %s", e.Size, e.Name))
			}
		}
		return ok(strings.Join(rows, "\n"))
	case "cat", "type":
		if len(args) != 1 {
			return fail(fmt.Errorf("usage: %s PATH", cmd))
		}
		var out struct {
			Data string `json:"data_base64"`
		}
		if err := c.Call(ctx, "fs/read", map[string]string{"path": envPath(cwd, args[0])}, &out); err != nil {
			return fail(err)
		}
		data, err := base64.StdEncoding.DecodeString(out.Data)
		if err != nil {
			return fail(err)
		}
		return ok(string(data))
	case "ps":
		if len(args) != 0 {
			return fail(fmt.Errorf("usage: ps"))
		}
		var procs []struct {
			ID    int32    `json:"id"`
			Name  string   `json:"name"`
			State string   `json:"state"`
			Caps  []string `json:"caps"`
		}
		if err := c.Call(ctx, "proc/list", nil, &procs); err != nil {
			return fail(err)
		}
		rows := []string{"PID   PROCESS                          STATE / GRANT"}
		for _, p := range procs {
			rows = append(rows, fmt.Sprintf("%-5d %-32s %s [%s]", p.ID, p.Name, p.State, strings.Join(p.Caps, ",")))
		}
		return ok(strings.Join(rows, "\n"))
	case "status":
		if len(args) != 0 {
			return fail(fmt.Errorf("usage: status"))
		}
		var out json.RawMessage
		if err := c.Call(ctx, "sys/status", nil, &out); err != nil {
			return fail(err)
		}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return fail(err)
		}
		return ok(string(b))
	case "shutdown":
		if len(args) != 0 {
			return fail(fmt.Errorf("usage: shutdown"))
		}
		if err := c.Call(ctx, "sys/shutdown", map[string]string{"reason": "requested by Charm shell"}, nil); err != nil {
			return fail(err)
		}
		return "Shutdown requested", cwd, true, nil
	default:
		return fail(fmt.Errorf("unknown command %q — type help", cmd))
	}
}

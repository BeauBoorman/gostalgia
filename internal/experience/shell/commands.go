package shell

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"
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

type sysStatusData struct {
	UptimeSeconds float64
	User          string
	ProcessCount  int
	ServicesCount int
}

type resultMsg struct {
	text         string
	err          error
	cwd          string
	apps         []appStatus
	status       sysStatusData
	documents    []docShortcut
	hasStatus    bool
	hasDocs      bool
	quit         bool
	switchView   string
	receiptPID   int32
	listReceipts bool
	toggleDND    bool
	setDND       *bool
	setTheme     string
	showTheme    bool
	setMotion    *bool
	toggleMotion bool
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
  tasks / taskmanager       live Task Manager process dashboard
  notifications / alerts    notification center and alert history
  receipt [PID]             view process crash receipts
  reap                      clean up terminated processes
  dnd [on|off]              toggle or set Do-Not-Disturb
  theme [NAME]              switch theme (nostalgia, midnight, monochrome, high-contrast, high-contrast-light)
  motion [on|off]           toggle or set reduced-motion mode
  settings / preferences    interactive system preferences and themes
  ps · status               processes · system dashboard
  logs / log PID [TAIL]     view child process logs and diagnostics
  cls / clear               clear the transcript
  exit                      leave shell (owned boot shuts down)
  shutdown                  shut down the environment

F1 home · F2 apps · F5 tasks · F6 alerts · F7 settings · Ctrl+P / / palette · F3 stop · F4 view
Tab complete · ↑↓ history · Paths accept /users/guest or C:\users\guest.`

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
	if strings.HasPrefix(result.text, "__SWITCH_VIEW__:") {
		result.switchView = strings.TrimPrefix(result.text, "__SWITCH_VIEW__:")
		result.text = ""
	} else if strings.HasPrefix(result.text, "__SHOW_RECEIPT__:") {
		var pid int32
		fmt.Sscan(strings.TrimPrefix(result.text, "__SHOW_RECEIPT__:"), &pid)
		result.receiptPID = pid
		result.text = ""
	} else if result.text == "__LIST_RECEIPTS__" {
		result.listReceipts = true
		result.text = ""
	} else if result.text == "__TOGGLE_DND__" {
		result.toggleDND = true
		result.text = ""
	} else if result.text == "__SET_DND_ON__" {
		on := true
		result.setDND = &on
		result.text = ""
	} else if result.text == "__SET_DND_OFF__" {
		off := false
		result.setDND = &off
		result.text = ""
	} else if strings.HasPrefix(result.text, "__SET_THEME__:") {
		result.setTheme = strings.TrimPrefix(result.text, "__SET_THEME__:")
		result.text = ""
	} else if result.text == "__SHOW_THEME__" {
		result.showTheme = true
		result.text = ""
	} else if strings.HasPrefix(result.text, "__SET_MOTION__:") {
		val := strings.TrimPrefix(result.text, "__SET_MOTION__:") == "on"
		result.setMotion = &val
		result.text = ""
	} else if result.text == "__TOGGLE_MOTION__" {
		result.toggleMotion = true
		result.text = ""
	}
	if !quit {
		var apps []appStatus
		if refreshErr := c.Call(ctx, "app/list", nil, &apps); refreshErr != nil {
			if result.err == nil {
				result.err = fmt.Errorf("refresh apps: %w", refreshErr)
			}
		} else {
			result.apps = apps
		}

		var status struct {
			UptimeSeconds float64 `json:"uptime_seconds"`
			User          string  `json:"user"`
			Processes     []any   `json:"processes"`
			Services      []any   `json:"services"`
		}
		if err := c.Call(ctx, "sys/status", nil, &status); err == nil {
			result.status = sysStatusData{
				UptimeSeconds: status.UptimeSeconds,
				User:          status.User,
				ProcessCount:  len(status.Processes),
				ServicesCount: len(status.Services),
			}
			result.hasStatus = true
		}

		var dir struct {
			Entries []struct {
				Name  string `json:"name"`
				IsDir bool   `json:"is_dir"`
				Size  int64  `json:"size"`
			} `json:"entries"`
		}
		if err := c.Call(ctx, "fs/list", map[string]string{"path": "/users/guest/documents"}, &dir); err == nil {
			var docs []docShortcut
			for _, e := range dir.Entries {
				if !e.IsDir {
					docs = append(docs, docShortcut{
						Name: e.Name,
						Path: "/users/guest/documents/" + e.Name,
						Size: e.Size,
					})
				}
			}
			result.documents = docs
			result.hasDocs = true
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
		return ok(safe(string(pretty)))
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
			rows = append(rows, fmt.Sprintf("%s  %s v%s\n  %s · caps: %s", safe(a.Manifest.ID), safe(a.Manifest.Name), safe(a.Manifest.Version), state, strings.Join(a.Manifest.Permissions, ", ")))
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
		return ok(verb + " " + safe(args[0]))
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
		return ok(fmt.Sprintf("%s   [echo #%d]", safe(out.Msg), out.Echoes))
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
			return "Directory: " + safe(p), p, false, nil
		}
		rows := []string{"Directory of " + safe(p)}
		for _, e := range out.Entries {
			if e.IsDir {
				rows = append(rows, "  <DIR>       "+safe(e.Name))
			} else {
				rows = append(rows, fmt.Sprintf("  %8d    %s", e.Size, safe(e.Name)))
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
		return ok(safe(string(data)))
	case "ps":
		if len(args) != 0 {
			return fail(fmt.Errorf("usage: ps"))
		}
		var procs []struct {
			ID           int32     `json:"id"`
			Name         string    `json:"name"`
			Kind         string    `json:"kind"`
			State        string    `json:"state"`
			Caps         []string  `json:"caps"`
			StartedAt    time.Time `json:"started_at"`
			ExitedAt     time.Time `json:"exited_at"`
			ExitCode     int       `json:"exit_code"`
			RestartCount int       `json:"restart_count"`
			CrashLoop    bool      `json:"crash_loop"`
		}
		if err := c.Call(ctx, "proc/list", nil, &procs); err != nil {
			return fail(err)
		}
		rows := []string{"PID   PROCESS                          STATE / STATUS     TIME     GRANT"}
		for _, p := range procs {
			stateDesc := p.State
			if p.CrashLoop {
				stateDesc = fmt.Sprintf("crashloop (%d)", p.RestartCount)
			} else if p.RestartCount > 0 && (p.State == "running" || p.State == "restarting") {
				stateDesc = fmt.Sprintf("%s (%d)", p.State, p.RestartCount)
			} else if p.State == "stopped" || p.State == "failed" {
				stateDesc = fmt.Sprintf("%s (exit %d)", p.State, p.ExitCode)
			}
			timeStr := "-"
			if !p.StartedAt.IsZero() {
				if !p.ExitedAt.IsZero() {
					timeStr = formatDuration(p.ExitedAt.Sub(p.StartedAt))
				} else {
					timeStr = formatDuration(time.Since(p.StartedAt))
				}
			}
			rows = append(rows, fmt.Sprintf("%-5d %-32s %-18s %-8s [%s]",
				p.ID, safe(p.Name), stateDesc, timeStr, strings.Join(p.Caps, ",")))
		}
		return ok(strings.Join(rows, "\n"))
	case "logs", "log":
		if len(args) == 0 || len(args) > 2 {
			return fail(fmt.Errorf("usage: logs PID [TAIL]"))
		}
		var pid int32
		if _, err := fmt.Sscan(args[0], &pid); err != nil || pid <= 0 {
			return fail(fmt.Errorf("invalid pid: %s", args[0]))
		}
		params := map[string]any{"id": pid}
		if len(args) == 2 {
			var tail int
			if _, err := fmt.Sscan(args[1], &tail); err == nil && tail > 0 {
				params["tail"] = tail
			}
		}
		var logs struct {
			ID        int32     `json:"id"`
			Name      string    `json:"name"`
			Kind      string    `json:"kind"`
			State     string    `json:"state"`
			ExitCode  int       `json:"exit_code"`
			StartedAt time.Time `json:"started_at"`
			ExitedAt  time.Time `json:"exited_at"`
			Duration  string    `json:"duration"`
			Stdout    struct {
				TotalBytes    int64  `json:"total_bytes"`
				BufferedBytes int    `json:"buffered_bytes"`
				DroppedBytes  int64  `json:"dropped_bytes"`
				Truncated     bool   `json:"truncated"`
				Content       string `json:"content"`
			} `json:"stdout"`
			Stderr struct {
				TotalBytes    int64  `json:"total_bytes"`
				BufferedBytes int    `json:"buffered_bytes"`
				DroppedBytes  int64  `json:"dropped_bytes"`
				Truncated     bool   `json:"truncated"`
				Content       string `json:"content"`
			} `json:"stderr"`
		}
		if err := c.Call(ctx, "proc/logs", params, &logs); err != nil {
			return fail(err)
		}
		exitStr := "-"
		if logs.State == "stopped" || logs.State == "failed" {
			exitStr = fmt.Sprintf("%d", logs.ExitCode)
		}
		durStr := logs.Duration
		if durStr == "" {
			durStr = "-"
		}
		var out []string
		out = append(out, fmt.Sprintf("PROCESS %d (%s) · %s · %s (exit %s, time %s)",
			logs.ID, safe(logs.Name), logs.Kind, logs.State, exitStr, durStr))
		out = append(out, fmt.Sprintf("stdout: %dB (dropped %dB) · stderr: %dB (dropped %dB)",
			logs.Stdout.TotalBytes, logs.Stdout.DroppedBytes, logs.Stderr.TotalBytes, logs.Stderr.DroppedBytes))
		if logs.Stdout.Content != "" {
			out = append(out, "--- STDOUT ---")
			out = append(out, safe(strings.TrimRight(logs.Stdout.Content, "\r\n")))
		}
		if logs.Stderr.Content != "" {
			out = append(out, "--- STDERR ---")
			out = append(out, safe(strings.TrimRight(logs.Stderr.Content, "\r\n")))
		}
		if logs.Stdout.Content == "" && logs.Stderr.Content == "" {
			out = append(out, "(no output recorded)")
		}
		return ok(strings.Join(out, "\n"))
	case "tasks", "taskmanager", "top":
		if len(args) != 0 {
			return fail(fmt.Errorf("usage: %s", cmd))
		}
		return ok("__SWITCH_VIEW__:tasks")
	case "notifications", "alerts":
		if len(args) != 0 {
			return fail(fmt.Errorf("usage: %s", cmd))
		}
		return ok("__SWITCH_VIEW__:notifications")
	case "settings", "preferences", "pref":
		if len(args) != 0 {
			return fail(fmt.Errorf("usage: %s", cmd))
		}
		return ok("__SWITCH_VIEW__:settings")
	case "reap":
		if len(args) != 0 {
			return fail(fmt.Errorf("usage: reap"))
		}
		var resp struct {
			Reaped int `json:"reaped"`
		}
		if err := c.Call(ctx, "proc/reap", map[string]any{}, &resp); err != nil {
			return fail(err)
		}
		return ok(fmt.Sprintf("Reaped %d terminated processes", resp.Reaped))
	case "receipt", "receipts":
		if len(args) > 1 {
			return fail(fmt.Errorf("usage: receipt [PID]"))
		}
		if len(args) == 0 {
			return ok("__LIST_RECEIPTS__")
		}
		var pid int32
		if _, err := fmt.Sscan(args[0], &pid); err != nil || pid <= 0 {
			return fail(fmt.Errorf("invalid pid: %s", args[0]))
		}
		return ok(fmt.Sprintf("__SHOW_RECEIPT__:%d", pid))
	case "dnd":
		if len(args) > 1 {
			return fail(fmt.Errorf("usage: dnd [on|off]"))
		}
		if len(args) == 0 {
			return ok("__TOGGLE_DND__")
		}
		switch strings.ToLower(args[0]) {
		case "on", "enable", "true", "1":
			return ok("__SET_DND_ON__")
		case "off", "disable", "false", "0":
			return ok("__SET_DND_OFF__")
		default:
			return fail(fmt.Errorf("usage: dnd [on|off]"))
		}
	case "theme":
		if len(args) == 0 {
			return ok("__SHOW_THEME__")
		}
		if len(args) != 1 {
			return fail(fmt.Errorf("usage: theme [NAME]"))
		}
		return ok("__SET_THEME__:" + strings.ToLower(args[0]))
	case "motion":
		if len(args) == 0 {
			return ok("__TOGGLE_MOTION__")
		}
		if len(args) != 1 {
			return fail(fmt.Errorf("usage: motion [on|off|reduce|normal]"))
		}
		arg := strings.ToLower(args[0])
		if arg == "on" || arg == "reduce" || arg == "reduced" {
			return ok("__SET_MOTION__:on")
		} else if arg == "off" || arg == "normal" || arg == "standard" {
			return ok("__SET_MOTION__:off")
		}
		return fail(fmt.Errorf("usage: motion [on|off|reduce|normal]"))
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
		return ok(safe(string(b)))
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

func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}

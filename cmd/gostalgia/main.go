// Command gostalgia boots and serves the Gostalgia environment.
//
//	gostalgia shell [--root DIR] [--verbose] [--attach] (default)
//	gostalgia attach [--root DIR]
//	gostalgia boot [--root DIR] [--verbose]
//	gostalgia init [--root DIR]
//	gostalgia version
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"

	"gostalgia/internal/experience/notifications"
	"gostalgia/internal/experience/shell"
	"gostalgia/internal/ipc"
	"gostalgia/internal/runtime"
	"gostalgia/internal/service"
	"gostalgia/platform"
)

// notifSink adapts the experience-layer notification manager to the
// service.NotificationSink boundary.
type notifSink struct{ mgr *notifications.Manager }

func (s *notifSink) Record(r service.NotificationRecord) bool {
	return s.mgr.Record(notifications.Notification{
		Title:     r.Title,
		Message:   r.Message,
		Level:     notifications.Level(r.Level),
		Source:    r.Source,
		PID:       r.PID,
		Timestamp: r.Timestamp,
	})
}

func newNotifications() (*notifications.Manager, service.NotificationSink) {
	mgr := notifications.NewManager(notifications.DefaultMaxHistory, notifications.DefaultMaxActiveToasts, notifications.DefaultToastTicks)
	return mgr, &notifSink{mgr}
}

const usageText = `gostalgia — the Gostalgia environment runtime

Usage:
  gostalgia boot [--root DIR] [--verbose]             boot and serve the environment
  gostalgia shell [--root DIR] [--verbose] [--attach] boot into the Charm terminal (default)
  gostalgia attach [--root DIR]                       attach shell to a running environment
  gostalgia init [--root DIR]                         create the environment root
  gostalgia version                                   print the version
`

func main() {
	args := os.Args[1:]
	cmd := "shell"
	if len(args) > 0 {
		cmd = args[0]
		args = args[1:]
	}

	var err error
	switch cmd {
	case "boot":
		err = cmdBoot(args)
	case "shell":
		err = cmdShell(args)
	case "attach":
		err = cmdAttachCLI(args)
	case "init":
		err = cmdInit(args)
	case "version", "--version", "-v":
		fmt.Println("gostalgia", runtime.Version)
	case "help", "--help", "-h":
		fmt.Print(usageText)
	default:
		fmt.Fprintf(os.Stderr, "gostalgia: unknown command %q\n\n%s", cmd, usageText)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "gostalgia:", err)
		os.Exit(1)
	}
}

func rootFlag(fs *flag.FlagSet) *string {
	return fs.String("root", "", "environment root directory (default $GOSTALGIA_ROOT or ~/.gostalgia)")
}

func cmdBoot(args []string) error {
	fs := flag.NewFlagSet("boot", flag.ExitOnError)
	root := rootFlag(fs)
	verbose := fs.Bool("verbose", false, "enable debug logging")
	fs.Parse(args)

	_, sink := newNotifications()
	rt, err := runtime.Boot(context.Background(), runtime.Options{Root: *root, Verbose: *verbose, Notifications: sink})
	if err != nil {
		return err
	}
	fmt.Printf("Gostalgia %s ready\n  root:     %s\n  endpoint: %s\n  (Ctrl-C to shut down)\n",
		rt.Version, rt.Root, rt.Endpoint())

	// The signal set is platform-specific (platform.ShutdownSignals):
	// unix delivers SIGTERM, Windows only ever delivers os.Interrupt.
	sigCtx, stop := signal.NotifyContext(context.Background(), platform.ShutdownSignals()...)
	defer stop()

	select {
	case <-sigCtx.Done():
		rt.Shutdown("interrupt signal")
	case <-rt.Done():
		// Shutdown was requested over IPC.
	}
	rt.Wait()
	fmt.Println("Gostalgia shut down cleanly")
	return nil
}

func cmdAttachCLI(args []string) error {
	fs := flag.NewFlagSet("attach", flag.ExitOnError)
	root := rootFlag(fs)
	fs.Parse(args)
	return cmdAttach(*root)
}

func cmdAttach(root string) error {
	dir, err := runtime.ResolveRoot(root)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(dir, "runtime.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no running Gostalgia instance found in %s; boot one with 'gostalgia boot' first", dir)
		}
		return err
	}
	var info struct {
		PID      int    `json:"pid"`
		Endpoint string `json:"endpoint"`
		Token    string `json:"token"`
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return fmt.Errorf("invalid runtime metadata in %s: %w", dir, err)
	}
	conn, err := platform.DialIPC(info.Endpoint)
	if err != nil {
		return fmt.Errorf("failed to connect to Gostalgia runtime at %s (PID %d): %w", info.Endpoint, info.PID, err)
	}
	client, err := ipc.NewClient(conn, info.Token)
	if err != nil {
		return fmt.Errorf("failed to authenticate with Gostalgia runtime: %w", err)
	}
	defer client.Close()

	ctx, stop := signal.NotifyContext(context.Background(), platform.ShutdownSignals()...)
	defer stop()
	return shell.RunAttached(ctx, client, client.Done())
}

// cmdShell owns the environment unless --attach is passed.
func cmdShell(args []string) error {
	fs := flag.NewFlagSet("shell", flag.ExitOnError)
	root := rootFlag(fs)
	verbose := fs.Bool("verbose", false, "enable debug logging to the runtime log")
	attach := fs.Bool("attach", false, "attach to an existing running Gostalgia environment")
	fs.Parse(args)

	if *attach {
		return cmdAttach(*root)
	}

	ctx, stop := signal.NotifyContext(context.Background(), platform.ShutdownSignals()...)
	defer stop()
	notifs, sink := newNotifications()
	rt, err := runtime.Boot(context.Background(), runtime.Options{Root: *root, Verbose: *verbose, LogOutput: io.Discard, Notifications: sink})
	if err != nil {
		return err
	}
	defer rt.Shutdown("shell exited")
	data, err := os.ReadFile(filepath.Join(rt.Root, "runtime.json"))
	if err != nil {
		return err
	}
	var info struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return err
	}
	conn, err := platform.DialIPC(rt.Endpoint())
	if err != nil {
		return err
	}
	client, err := ipc.NewClient(conn, info.Token)
	if err != nil {
		return err
	}
	defer client.Close()
	return shell.Run(ctx, client, rt.Done(), notifs)
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	root := rootFlag(fs)
	fs.Parse(args)

	dir, err := runtime.ResolveRoot(*root)
	if err != nil {
		return err
	}
	if err := runtime.InitRoot(dir); err != nil {
		return err
	}
	fmt.Println("initialized Gostalgia environment root at", dir)
	return nil
}

// Command gostalgia boots and serves the Gostalgia environment.
//
//	gostalgia shell [--root DIR] [--verbose] (default)
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
	"syscall"

	"gostalgia/internal/experience/shell"
	"gostalgia/internal/ipc"
	"gostalgia/internal/runtime"
	"gostalgia/platform"
)

const usageText = `gostalgia — the Gostalgia environment runtime

Usage:
  gostalgia boot [--root DIR] [--verbose]   boot and serve the environment
  gostalgia shell [--root DIR] [--verbose] boot into the Charm terminal (default)
  gostalgia init [--root DIR]               create the environment root
  gostalgia version                         print the version
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

	rt, err := runtime.Boot(context.Background(), runtime.Options{Root: *root, Verbose: *verbose})
	if err != nil {
		return err
	}
	fmt.Printf("Gostalgia %s ready\n  root:     %s\n  endpoint: %s\n  (Ctrl-C to shut down)\n",
		rt.Version, rt.Root, rt.Endpoint())

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
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

// cmdShell owns the environment. Headless operators still use boot + gctl.
func cmdShell(args []string) error {
	fs := flag.NewFlagSet("shell", flag.ExitOnError)
	root := rootFlag(fs)
	verbose := fs.Bool("verbose", false, "enable debug logging to the runtime log")
	fs.Parse(args)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	rt, err := runtime.Boot(context.Background(), runtime.Options{Root: *root, Verbose: *verbose, LogOutput: io.Discard})
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
	return shell.Run(ctx, client, rt.Done())
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

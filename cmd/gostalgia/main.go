// Command gostalgia boots and serves the Gostalgia environment.
//
//	gostalgia boot [--root DIR] [--verbose]
//	gostalgia init [--root DIR]
//	gostalgia version
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"gostalgia/internal/runtime"
	"gostalgia/platform"
)

const usageText = `gostalgia — the Gostalgia environment runtime

Usage:
  gostalgia boot [--root DIR] [--verbose]   boot and serve the environment
  gostalgia init [--root DIR]               create the environment root
  gostalgia version                         print the version
`

func main() {
	args := os.Args[1:]
	cmd := "boot"
	if len(args) > 0 {
		cmd = args[0]
		args = args[1:]
	}

	var err error
	switch cmd {
	case "boot":
		err = cmdBoot(args)
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

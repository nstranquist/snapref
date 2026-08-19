package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/nstranquist/snapref/browser"
	browserscript "github.com/nstranquist/snapref/script"
)

const scriptHelpText = `Usage:
  snapref script run <file.yaml|.json|.star> [--session <name>] [--open] [--runtime <kind>] [--allow-drive] [--allow-host-write]
  snapref script validate <file.yaml|.json|.star>

Run a playbook against an open session. --allow-drive is required for
navigate/click/type. --open starts the named session if it is not already
alive. validate loads the file and does not launch a browser.
`

func scriptCommand(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Print(scriptHelpText)
		return nil
	}
	switch args[0] {
	case "validate":
		return scriptValidate(args[1:])
	case "run":
		return scriptRun(args[1:])
	default:
		return fmt.Errorf("script: unknown operation %q\n\n%s", args[0], scriptHelpText)
	}
}

func scriptValidate(args []string) error {
	if len(args) != 1 {
		return errors.New("script validate: requires exactly one playbook path")
	}
	path := args[0]
	if strings.EqualFold(filepath.Ext(path), ".star") {
		return errors.New("script validate: Starlark playbooks run eagerly; use script run")
	}
	plan, err := browserscript.LoadFile(path, nil)
	if err != nil {
		return err
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	fmt.Printf("script: valid  plan=%s steps=%d\n", plan.Name, len(plan.Steps))
	return nil
}

func scriptRun(args []string) error {
	var (
		session        string
		openSession    bool
		allowDrive     bool
		allowHostWrite bool
		runtimeKind    = browser.RuntimeAuto
		positionals    []string
	)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--open":
			openSession = true
		case arg == "--allow-drive":
			allowDrive = true
		case arg == "--allow-host-write":
			allowHostWrite = true
		case arg == "--session" && i+1 < len(args):
			i++
			session = args[i]
		case strings.HasPrefix(arg, "--session="):
			session = strings.TrimPrefix(arg, "--session=")
		case arg == "--runtime" && i+1 < len(args):
			i++
			parsed, err := browser.ParseRuntimeKind(args[i])
			if err != nil {
				return fmt.Errorf("script run: %w", err)
			}
			runtimeKind = parsed
		case strings.HasPrefix(arg, "--runtime="):
			parsed, err := browser.ParseRuntimeKind(strings.TrimPrefix(arg, "--runtime="))
			if err != nil {
				return fmt.Errorf("script run: %w", err)
			}
			runtimeKind = parsed
		case strings.HasPrefix(arg, "-"):
			return fmt.Errorf("script run: unknown flag %q", arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) != 1 {
		return errors.New(strings.TrimSpace(scriptHelpText))
	}
	if openSession && session == "" {
		return errors.New("script run: --open requires --session <name>")
	}
	path := positionals[0]

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if openSession {
		st, statusErr := browser.Status(session)
		if statusErr == nil && st.Alive && st.Reachable {
			if runtimeKind != browser.RuntimeAuto && st.RuntimeKind != runtimeKind {
				return fmt.Errorf("script run: session %q uses runtime %s, requested %s", session, st.RuntimeKind, runtimeKind)
			}
		} else {
			if statusErr == nil && st.Alive {
				_ = browser.Close(session, true)
			}
			idleTimeout, err := configuredSnapRefIdleTimeout()
			if err != nil {
				return err
			}
			if _, err := openManagedSnapRefSession(browser.OpenOptions{Name: session, Runtime: runtimeKind, Headful: false, ReadyTimeout: 30 * time.Second}, idleTimeout); err != nil {
				return fmt.Errorf("script run: open session %q: %w", session, err)
			}
			defer func() { _ = browser.Close(session, true) }()
		}
	}

	drv := browser.NewRemoteDriver()
	out := bufio.NewWriter(os.Stdout)
	opts := browserscript.RunOptions{
		AllowDrive:     allowDrive,
		AllowHostWrite: allowHostWrite,
		Session:        session,
		Sink:           out,
		Telemetry:      browserscript.NewFileTelemetry(),
	}
	var (
		rep    *browserscript.RunReport
		runErr error
	)
	if strings.EqualFold(filepath.Ext(path), ".star") {
		rep, runErr = browserscript.RunStarlarkFile(ctx, drv, path, opts)
	} else {
		plan, err := browserscript.LoadFile(path, nil)
		if err != nil {
			_ = out.Flush()
			return err
		}
		if session != "" {
			plan.Session = session
		}
		opts.Source = browserscript.SourceForPath(path)
		rep, runErr = browserscript.NewRunner(drv).Run(ctx, plan, opts)
	}
	_ = out.Flush()
	if rep == nil {
		if runErr != nil {
			return runErr
		}
		return errors.New("script run: no report")
	}
	if !rep.OK || runErr != nil {
		if runErr != nil {
			return fmt.Errorf("script: failed plan=%s steps=%d: %w", rep.Plan, len(rep.Steps), runErr)
		}
		return fmt.Errorf("script: failed plan=%s steps=%d", rep.Plan, len(rep.Steps))
	}
	fmt.Fprintf(os.Stderr, "script: ok  plan=%s steps=%d run=%s\n", rep.Plan, len(rep.Steps), rep.RunID)
	return nil
}

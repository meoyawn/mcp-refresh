package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
)

const version = "0.1.0"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "mcp-refresh:", err)
		os.Exit(1)
	}
}

const usage = `Usage:
  mcp-refresh list [--json]
  mcp-refresh refresh <mcp name, link ID, or connector ID> [--json]
  mcp-refresh auth import [--browser chrome] [--profile Default] [--account-id ID]
  mcp-refresh auth status
  mcp-refresh auth logout
  mcp-refresh serve

All commands accept --session PATH (or MCP_REFRESH_SESSION).
The first list/refresh imports a signed-in ChatGPT browser session automatically.
serve exposes the list and refresh MCP tools over stdio using the same session.
`

func run(ctx context.Context, args []string, out, errOut io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprint(out, usage)
		return err
	}
	if args[0] == "version" || args[0] == "--version" {
		_, err := fmt.Fprintln(out, version)
		return err
	}
	command, rest := args[0], args[1:]
	if command == "auth" {
		if len(rest) == 0 {
			return fmt.Errorf("auth requires import, status, or logout")
		}
		command, rest = "auth "+rest[0], rest[1:]
	}
	switch command {
	case "list", "refresh", "serve", "auth import", "auth status", "auth logout":
	default:
		return fmt.Errorf("unknown command %q; run mcp-refresh help", command)
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	var sessionPath, browser, profile, accountID string
	var asJSON bool
	fs.StringVar(&sessionPath, "session", os.Getenv("MCP_REFRESH_SESSION"), "path to the private session file")
	if command == "list" || command == "refresh" {
		fs.BoolVar(&asJSON, "json", false, "write JSON")
	}
	if command == "auth import" {
		fs.StringVar(&browser, "browser", "", "browser to import; default: automatic discovery")
		fs.StringVar(&profile, "profile", "", "profile name, directory, or cookie database; requires --browser")
		fs.StringVar(&accountID, "account-id", "", "ChatGPT account/workspace ID")
	}
	flagArgs, err := interspersedFlags(fs, rest)
	if err != nil {
		return err
	}
	if err := fs.Parse(flagArgs); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if command != "refresh" && fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments for %s: %q", command, fs.Args())
	}
	if command == "refresh" && strings.TrimSpace(strings.Join(fs.Args(), " ")) == "" {
		return fmt.Errorf("refresh requires an MCP name or ID; run mcp-refresh list")
	}
	if sessionPath == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return fmt.Errorf("find config directory: %w", err)
		}
		sessionPath = filepath.Join(dir, "mcp-refresh", "session.json")
	}
	svc := &service{sessionPath: sessionPath, http: newHTTPClient(), log: errOut}
	switch command {
	case "auth import":
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		s, err := importSession(ctx, browser, profile, accountID, svc.http)
		if err != nil {
			return err
		}
		if err := saveSession(sessionPath, s); err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "Imported ChatGPT session from %s (%s). Saved to %s\n", s.Browser, s.Profile, sessionPath)
		return err
	case "auth status":
		s, err := loadSession(sessionPath)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "Session: %s\nBrowser: %s\nProfile: %s\nImported: %s\nCookies: %d\n", sessionPath, s.Browser, s.Profile, s.ImportedAt.Format(time.RFC3339), len(s.Cookies))
		return err
	case "auth logout":
		if err := os.Remove(sessionPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove session: %w", err)
		}
		_, err := fmt.Fprintln(out, "Removed stored session.")
		return err
	case "serve":
		return serve(ctx, svc)
	case "list":
		links, err := svc.list(ctx)
		if err != nil {
			return err
		}
		if asJSON {
			return writeJSON(out, links)
		}
		if len(links) == 0 {
			_, err = fmt.Fprintln(out, "No development MCPs found.")
			return err
		}
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tLINK NAME\tTOOLS\tAUTH\tLINK ID")
		for _, l := range links {
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n", l.displayName(), l.Name, len(l.Actions), l.AuthStatus, l.ID)
		}
		return w.Flush()
	case "refresh":
		result, err := svc.refresh(ctx, strings.Join(fs.Args(), " "))
		if err != nil {
			return err
		}
		if asJSON {
			return writeJSON(out, result)
		}
		_, err = fmt.Fprintf(out, "Refreshed %s (%d tools).\n", result.Name, len(result.Actions))
		return err
	}
	return nil
}

// flag.FlagSet stops at the first positional argument; allow refresh NAME --json.
func interspersedFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			positional = append(positional, a)
			continue
		}
		flags = append(flags, a)
		name, _, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		f := fs.Lookup(name)
		if f == nil {
			continue
		} // FlagSet supplies the unknown-flag diagnostic.
		isBool := false
		if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok {
			isBool = b.IsBoolFlag()
		}
		if !hasValue && !isBool {
			if i+1 == len(args) {
				return nil, fmt.Errorf("flag --%s needs a value", name)
			}
			i++
			flags = append(flags, args[i])
		}
	}
	return append(append(flags, "--"), positional...), nil
}

func writeJSON(out io.Writer, value any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

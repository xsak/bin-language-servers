// Command bls runs one of the bundled language servers over stdio.
//
//	bls <language> [--stdio] [--log-file PATH] [--log-level debug|info|warn|error]
//	bls version
//	bls list
//
// Called through a symlink named "<language>-ls" or "bls-<language>", the language is taken
// from the program name.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/xsak/bin-language-servers/internal/jsonrpc"
	"github.com/xsak/bin-language-servers/internal/lang"
	"github.com/xsak/bin-language-servers/internal/server"
	_ "github.com/xsak/bin-language-servers/langs/all"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(run(os.Args, os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	name, rest := languageFromArgs(args)
	switch name {
	case "", "help", "-h", "--help":
		usage(stderr)
		if name == "" {
			return 2
		}
		return 0
	case "version", "--version":
		fmt.Fprintf(stdout, "bls %s\n", version)
		return 0
	case "list":
		for _, info := range lang.All() {
			fmt.Fprintf(stdout, "%-12s %s\n", info.Name, info.Description)
		}
		return 0
	}

	info, ok := lang.Lookup(name)
	if !ok {
		fmt.Fprintf(stderr, "bls: unknown language %q (see 'bls list')\n", name)
		return 2
	}

	fs := flag.NewFlagSet("bls "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Bool("stdio", true, "communicate over stdin/stdout (the only transport)")
	logFile := fs.String("log-file", "", "write logs to this file instead of stderr")
	logLevel := fs.String("log-level", "info", "debug, info, warn or error")
	if err := fs.Parse(rest); err != nil {
		return 2
	}

	logger, closeLog, err := newLogger(stderr, *logFile, *logLevel)
	if err != nil {
		fmt.Fprintf(stderr, "bls: %v\n", err)
		return 2
	}
	defer closeLog()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	exitCode := make(chan int, 1)
	srv := server.New(info.New(), server.Options{
		Version:  version,
		Logger:   logger,
		CacheDir: server.DefaultCacheDir(),
		OnExit: func(code int) {
			select {
			case exitCode <- code:
			default:
			}
		},
	})

	// Serve returns on EOF; "exit" arrives before that and ends the process right away,
	// as the protocol requires.
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, jsonrpc.NewConn(stdin, stdout)) }()
	select {
	case code := <-exitCode:
		return code
	case err := <-done:
		if err != nil {
			logger.Error("serve", "err", err)
			return 1
		}
		return 0
	case <-ctx.Done():
		return 0
	}
}

// languageFromArgs picks the subcommand, or derives it from a symlink name.
func languageFromArgs(args []string) (string, []string) {
	prog := filepath.Base(args[0])
	switch {
	case strings.HasPrefix(prog, "bls-"):
		return strings.TrimPrefix(prog, "bls-"), args[1:]
	case strings.HasSuffix(prog, "-ls") && prog != "bls":
		return strings.TrimSuffix(prog, "-ls"), args[1:]
	}
	if len(args) < 2 {
		return "", nil
	}
	return args[1], args[2:]
}

func newLogger(stderr io.Writer, path, level string) (*slog.Logger, func(), error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		return nil, nil, fmt.Errorf("bad --log-level %q", level)
	}
	w, closeFn := stderr, func() {}
	if path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, nil, err
		}
		w, closeFn = f, func() { _ = f.Close() }
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: lvl})), closeFn, nil
}

func usage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  bls <language> [--stdio] [--log-file PATH] [--log-level LEVEL]
  bls list        languages compiled into this binary
  bls version

Logs go to stderr unless --log-file is given (Zed shows stderr in its LSP log).
`)
}

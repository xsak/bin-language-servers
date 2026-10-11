// Package extproc runs optional external tools (shellcheck, ansible-lint, ...) with a timeout.
// A missing tool is not an error for the language module: it just loses that feature.
package extproc

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

// DefaultTimeout bounds every external call.
const DefaultTimeout = 10 * time.Second

// Result is the output of a finished process. ExitCode is set even for non-zero exits, which
// linters use to say "found problems".
type Result struct {
	Stdout, Stderr []byte
	ExitCode       int
}

// Find returns the absolute path of name (or name itself if it is a path), or "" if it can't be
// found.
func Find(name string) string {
	if name == "" {
		return ""
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return p
}

// Run executes path with args in dir, feeding stdin. It returns an error only if the process
// could not run or was killed by the timeout/context; a non-zero exit is reported in Result.
func Run(ctx context.Context, dir string, stdin string, path string, args ...string) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = dir
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	res := Result{Stdout: out.Bytes(), Stderr: errOut.Bytes()}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr) && ctx.Err() == nil:
		res.ExitCode = exitErr.ExitCode()
	default:
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		return res, err
	}
	return res, nil
}

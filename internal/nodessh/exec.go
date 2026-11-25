package nodessh

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"
)

// capped keeps at most max bytes and silently drains the rest, so a chatty program can neither fill memory
// nor block on a full pipe.
type capped struct {
	buf bytes.Buffer
	max int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
		} else {
			c.buf.Write(p)
		}
	}
	return len(p), nil
}

// allowedPrograms: the only executables this package may start.
var allowedPrograms = map[string]bool{"ssh": true, "ssh-keyscan": true, "ssh-keygen": true}

// execRunner starts the program without a shell. stdin is empty, the environment is exactly env.
func execRunner(ctx context.Context, name string, args, env []string, maxOut int) (Result, error) {
	if !allowedPrograms[name] {
		return Result{}, errors.New("programma non ammesso")
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return Result{}, err
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = env
	cmd.Stdin = nil
	out := &capped{max: maxOut}
	errOut := &capped{max: 16 << 10}
	cmd.Stdout, cmd.Stderr = out, errOut
	cmd.WaitDelay = 2 * time.Second
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	res := Result{Stdout: out.buf.Bytes(), Stderr: errOut.buf.Bytes()}
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
		res.Exit = 0
	case errors.As(runErr, &exitErr):
		res.Exit = exitErr.ExitCode()
	default:
		return Result{}, runErr
	}
	return res, nil
}

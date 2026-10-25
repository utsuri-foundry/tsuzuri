package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	tsuzuri "github.com/utsuri-foundry/tsuzuri"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:])
	stop()
	os.Exit(code)
}
func run(ctx context.Context, args []string) int {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	jsonMode := slices.Contains(args, "--json")
	if slices.Contains(args, "--version") {
		if _, e := fmt.Println("tsuzuri " + tsuzuri.Version); e != nil {
			return emitError(jsonMode, e)
		}
		return 0
	}
	if len(args) == 0 || slices.Contains(args, "--help") || slices.Contains(args, "-h") {
		if _, e := fmt.Println("Tsuzuri — shared Git-native tasks\nUsage: tsuzuri [--json] <command> [options]\nCommands: compatibility, validate, projects, project, create-project, create-task, transition-task, list, next, show, overview, status, doctor, claim, accept, handoff, materialize, release"); e != nil {
			return emitError(jsonMode, e)
		}
		return 0
	}
	invocation, e := parseInvocation(args)
	if e != nil {
		return emitError(jsonMode, e)
	}
	if invocation.query.Command == "overview" && !jsonMode && invocation.query.Format != "markdown" {
		_, noColor := os.LookupEnv("NO_COLOR")
		invocation.query.Color = resolveOverviewColor(invocation.query.Color, isTerminal(os.Stdout), noColor, os.Getenv("TERM"))
	}
	client, e := tsuzuri.Open(".")
	if e != nil {
		return emitError(jsonMode, e)
	}
	var result tsuzuri.Result
	if invocation.author {
		var body []byte
		body, e = readInput(ctx, invocation.input)
		if e == nil {
			result, e = client.Author(ctx, invocation.query.Command, invocation.query.ID, invocation.requestID, body)
		}
	} else if invocation.write {
		result, e = client.Coordinate(ctx, invocation.coord)
	} else {
		result, e = client.Query(ctx, invocation.query)
	}
	if e != nil {
		return emitError(jsonMode, e)
	}
	output := result.Human
	if jsonMode {
		body, e := tsuzuri.JSON(result)
		if e != nil {
			return emitError(true, e)
		}
		output = string(body)
	}
	if _, e = fmt.Println(output); e != nil {
		return emitError(jsonMode, e)
	}
	return 0
}
func readInput(ctx context.Context, path string) (body []byte, err error) {
	file := os.Stdin
	if path != "" && path != "-" {
		before, e := os.Lstat(path)
		if e != nil {
			return nil, e
		}
		if !before.Mode().IsRegular() {
			return nil, usage("input must be a regular file")
		}
		file, e = openInputFile(path)
		if e != nil {
			return nil, e
		}
		defer func() {
			if e := file.Close(); e != nil && !errors.Is(e, os.ErrClosed) {
				err = errors.Join(err, e)
			}
		}()
		after, e := file.Stat()
		if e != nil {
			return nil, e
		}
		if !after.Mode().IsRegular() || !os.SameFile(before, after) {
			return nil, usage("input changed while opening")
		}
	}
	info, e := file.Stat()
	if e != nil {
		return nil, e
	}
	if !info.Mode().IsRegular() {
		// Blocking streams must support Go's poller so Close interrupts Read.
		var cleanup func() error
		file, cleanup, e = prepareInput(file)
		if e != nil {
			return nil, e
		}
		defer func() { err = errors.Join(err, cleanup()) }()
	}
	if e = ctx.Err(); e != nil {
		return nil, errors.Join(usage("input cancelled or timed out"), e)
	}
	closed := make(chan error, 1)
	stop := context.AfterFunc(ctx, func() { closed <- file.Close() })
	const limit = 1 << 20
	body, err = io.ReadAll(io.LimitReader(file, limit+1))
	if !stop() {
		// Observe both Read and cancellation callback completion before return.
		closeErr := <-closed
		return nil, errors.Join(usage("input cancelled or timed out"), ctx.Err(), err, closeErr)
	}
	if e = ctx.Err(); e != nil {
		return nil, errors.Join(usage("input cancelled or timed out"), e, err)
	}
	if err != nil {
		return nil, err
	}
	if len(body) > limit {
		return nil, usage("input exceeds 1 MiB")
	}
	return body, nil
}
func usage(message string) *tsuzuri.Error {
	return &tsuzuri.Error{Kind: "usage", Code: "usage", Message: message, ExitCode: 2}
}
func emitError(jsonMode bool, err error) int {
	var e *tsuzuri.Error
	if !errors.As(err, &e) {
		e = &tsuzuri.Error{Kind: "internal", Code: "internal", Message: err.Error(), ExitCode: 3}
	}
	if jsonMode {
		body, marshalErr := json.MarshalIndent(map[string]any{"schema": 1, "ok": false, "error": e}, "", "  ")
		if marshalErr != nil {
			body = []byte(`{"schema":1,"ok":false}`)
		}
		if _, writeErr := fmt.Println(string(body)); writeErr != nil {
			return 3
		}
	}
	if _, writeErr := fmt.Fprintln(os.Stderr, "error:", e.Message); writeErr != nil {
		return 3
	}
	return e.ExitCode
}

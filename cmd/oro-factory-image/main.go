// Command oro-factory-image builds a trusted guard image before admission.
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
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := runCLI(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func runCLI(ctx context.Context, args []string, output, diagnostic io.Writer) int {
	options, err := parseGuardImageOptions(args, diagnostic)
	var receipt guardImageReceipt
	if err == nil {
		receipt, err = buildGuardImage(ctx, options)
	}
	if err == nil {
		err = json.NewEncoder(output).Encode(receipt)
	}
	if err != nil {
		_, _ = fmt.Fprintf(diagnostic, "oro-factory-image: %v\n", err)
		return 1
	}
	return 0
}

func parseGuardImageOptions(args []string, diagnostic io.Writer) (guardImageOptions, error) {
	var options guardImageOptions
	flags := flag.NewFlagSet("oro-factory-image", flag.ContinueOnError)
	flags.SetOutput(diagnostic)
	for _, field := range []struct {
		name  string
		value *string
	}{
		{"guard", &options.Guard},
		{"guard-sha256", &options.GuardSHA256},
		{"docker", &options.Docker},
		{"docker-sha256", &options.DockerSHA256},
		{"context", &options.Context},
		{"endpoint", &options.Endpoint},
		{"output", &options.Output},
	} {
		flags.StringVar(field.value, field.name, "", "required explicit trusted bootstrap input")
	}
	if err := flags.Parse(args); err != nil {
		return guardImageOptions{}, fmt.Errorf("parse guard image options: %w", err)
	}
	if flags.NArg() != 0 {
		return guardImageOptions{}, errors.New("guard image builder accepts no positional arguments")
	}
	return options, nil
}

type guardImageOptions struct {
	Guard, GuardSHA256, Docker, DockerSHA256, Context, Endpoint, Output string
}

type guardImageReceipt struct {
	Version       int    `json:"version"`
	BaseReference string `json:"base_reference"`
	BaseID        string `json:"base_id"`
	GuardSHA256   string `json:"guard_sha256"`
	Platform      string `json:"platform"`
	ImageID       string `json:"image_id"`
	BuildLabel    string `json:"build_label"`
}

type (
	guardImageCommand struct {
		Path      string
		Args, Env []string
		Dir       string
	}
	guardImageRunner func(context.Context, guardImageCommand) ([]byte, error)
)

func buildGuardImage(ctx context.Context, options guardImageOptions) (guardImageReceipt, error) {
	return buildGuardImageWithRunner(ctx, options, runGuardCommand)
}

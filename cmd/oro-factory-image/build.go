package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	guardBaseReference = "golang@sha256:27f2d3b83cfc5803c8078f8e730af0f7c431bf067afaec8f60a36fb763187cba"
	guardImageLabel    = "dev.getoro.factory.guard-build"
	guardProgramLimit  = 64 << 20
	guardDockerLimit   = 512 << 20
	guardOutputLimit   = 1 << 20
)

func buildGuardImageWithRunner(ctx context.Context, options guardImageOptions, runner guardImageRunner) (guardImageReceipt, error) {
	if ctx == nil || runner == nil {
		return guardImageReceipt{}, errors.New("guard image build requires context and runner")
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	guard, err := validateGuardImageInputs(ctx, options)
	if err != nil {
		return guardImageReceipt{}, err
	}
	var nonce [16]byte
	_, _ = rand.Read(nonce[:])
	label := hex.EncodeToString(nonce[:])
	if err := prepareGuardImageDirectory(options, guard); err != nil {
		return guardImageReceipt{}, err
	}
	base, err := invokeGuardImage(ctx, options, runner, "image", "inspect", "--format", "{{json .}}", guardBaseReference)
	if err != nil {
		return guardImageReceipt{}, err
	}
	baseID, err := inspectGuardImage(base, "")
	if err != nil {
		return guardImageReceipt{}, err
	}
	tag := "oro-factory-guard:" + label
	if _, err := invokeGuardImage(ctx, options, runner, "build", "--network=none", "--pull=false", "--platform=linux/arm64", "--label", guardImageLabel+"="+label, "--tag", tag, filepath.Join(options.Output, "context")); err != nil {
		return guardImageReceipt{}, err
	}
	derived, err := invokeGuardImage(ctx, options, runner, "image", "inspect", "--format", "{{json .}}", tag)
	if err != nil {
		return guardImageReceipt{}, err
	}
	imageID, err := inspectGuardImage(derived, label)
	if err != nil {
		return guardImageReceipt{}, err
	}
	receipt := guardImageReceipt{Version: 1, BaseReference: guardBaseReference, BaseID: baseID, GuardSHA256: options.GuardSHA256, Platform: "linux/arm64", ImageID: imageID, BuildLabel: label}
	if err := publishGuardImageReceipt(ctx, options.Output, receipt); err != nil {
		return guardImageReceipt{}, err
	}
	return receipt, nil
}

func validateGuardImageInputs(ctx context.Context, options guardImageOptions) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("guard build canceled: %w", err)
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`).MatchString(options.Context) || options.Context == "default" {
		return nil, errors.New("guard image requires an explicit dedicated Docker context")
	}
	if err := canonicalGuardPath(options.Output, false); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(options.Output); !errors.Is(err, os.ErrNotExist) { // #nosec G703 -- Metadata-only absence check of an explicit canonical bootstrap output; actual creation uses a confined parent Root.
		return nil, errors.New("guard image output must be a new private directory")
	}
	if err := validateGuardEndpoint(options.Endpoint); err != nil {
		return nil, err
	}
	guard, err := readGuardProgram(ctx, options.Guard, options.GuardSHA256, guardProgramLimit, true)
	if err != nil {
		return nil, err
	}
	if err := validateStaticGuard(guard); err != nil {
		return nil, err
	}
	if _, err := readGuardProgram(ctx, options.Docker, options.DockerSHA256, guardDockerLimit, false); err != nil {
		return nil, err
	}
	return guard, nil
}

func canonicalGuardPath(path string, exists bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsRune(path, 0) {
		return errors.New("guard image paths must be canonical and absolute")
	}
	check := path
	if !exists {
		check = filepath.Dir(path)
	}
	canonical, err := filepath.EvalSymlinks(check)
	if err != nil || canonical != check {
		return errors.New("guard image path is missing or aliases another path")
	}
	return nil
}

func readGuardProgram(ctx context.Context, path, digest string, limit int64, retain bool) ([]byte, error) {
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(digest) {
		return nil, errors.New("guard image requires exact lowercase SHA256 identities")
	}
	if err := canonicalGuardPath(path, true); err != nil {
		return nil, err
	}
	file, opened, err := openGuardProgram(path, limit)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var data bytes.Buffer
	hash := sha256.New()
	var destination io.Writer = hash
	if retain {
		destination = io.MultiWriter(hash, &data)
	}
	count, err := io.Copy(destination, io.LimitReader(guardContextReader{ctx, file}, limit+1))
	if err != nil {
		return nil, fmt.Errorf("hash guard image program: %w", err)
	}
	if err := verifyGuardProgramRead(path, opened, count, limit); err != nil {
		return nil, err
	}
	if hex.EncodeToString(hash.Sum(nil)) != digest {
		return nil, errors.New("guard image program differs from its digest")
	}
	return data.Bytes(), nil
}

func openGuardProgram(path string, limit int64) (*os.File, os.FileInfo, error) {
	before, err := os.Lstat(path) // #nosec G703 -- Explicit canonical input metadata is checked for ownership, type and size, then bound to the opened inode.
	if err != nil || !validGuardProgram(before, limit) {
		return nil, nil, errors.New("guard image input is not a bounded owned regular executable")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0) // #nosec G304 G703 -- Trusted explicit canonical program; both Lstats bind to this nofollow/nonblocking descriptor and content is bounded and SHA-verified before execution.
	if err != nil {
		return nil, nil, fmt.Errorf("open guard image program: %w", err)
	}
	opened, err := file.Stat()
	if err != nil || !validGuardProgram(opened, limit) || !os.SameFile(before, opened) {
		_ = file.Close()
		return nil, nil, errors.New("guard program changed while opening")
	}
	return file, opened, nil
}

func verifyGuardProgramRead(path string, opened os.FileInfo, count, limit int64) error {
	after, err := os.Lstat(path) // #nosec G703 -- Metadata-only final binding to the owned program descriptor after its bounded hash read.
	if err != nil || !validGuardProgram(after, limit) || !os.SameFile(opened, after) || count != opened.Size() || count != after.Size() || !opened.ModTime().Equal(after.ModTime()) {
		return errors.New("guard image program changed while reading")
	}
	return nil
}

type guardContextReader struct {
	ctx    context.Context
	source io.Reader
}

func (reader guardContextReader) Read(data []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, fmt.Errorf("guard program read canceled: %w", err)
	}
	n, err := reader.source.Read(data)
	if errors.Is(err, io.EOF) {
		return n, io.EOF
	}
	if err != nil {
		return n, fmt.Errorf("read guard program: %w", err)
	}
	return n, nil
}

func validGuardProgram(info os.FileInfo, limit int64) bool {
	if info == nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 && info.Mode().Perm()&0o022 == 0 && stat.Nlink == 1 && (stat.Uid == 0 || stat.Uid == uint32(os.Getuid())) && info.Size() > 0 && info.Size() <= limit
}

func validateStaticGuard(data []byte) error {
	image, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("read static guard ELF: %w", err)
	}
	defer image.Close()
	if image.Class != elf.ELFCLASS64 || image.Data != elf.ELFDATA2LSB || image.Type != elf.ET_EXEC || image.Machine != elf.EM_AARCH64 || (image.OSABI != elf.ELFOSABI_NONE && image.OSABI != elf.ELFOSABI_LINUX) {
		return errors.New("guard must be a static Linux AARCH64 executable")
	}
	loaded := false
	for _, program := range image.Progs {
		if program.Type == elf.PT_INTERP || program.Type == elf.PT_DYNAMIC {
			return errors.New("guard ELF must not use a dynamic loader")
		}
		loaded = loaded || program.Type == elf.PT_LOAD
	}
	if !loaded {
		return errors.New("guard ELF has no loadable program")
	}
	return nil
}

func validateGuardEndpoint(endpoint string) error {
	path := strings.TrimPrefix(endpoint, "unix://")
	if !strings.HasPrefix(endpoint, "unix://") || strings.ContainsAny(path, " ?#%") {
		return errors.New("guard build requires a canonical dedicated Unix endpoint")
	}
	if err := canonicalGuardPath(path, true); err != nil {
		return err
	}
	info, err := os.Lstat(path) // #nosec G703 -- Metadata-only check of the explicit canonical endpoint; only an owned private single-link Unix socket is accepted.
	if err != nil {
		return fmt.Errorf("inspect guard Docker endpoint: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0o077 != 0 || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 {
		return errors.New("guard Docker endpoint must be an owned private socket")
	}
	return nil
}

func prepareGuardImageDirectory(options guardImageOptions, guard []byte) error {
	parent, err := os.OpenRoot(filepath.Dir(options.Output))
	if err != nil {
		return fmt.Errorf("open guard output parent: %w", err)
	}
	defer parent.Close()
	if err := parent.Mkdir(filepath.Base(options.Output), 0o700); err != nil {
		return fmt.Errorf("create guard image output: %w", err)
	}
	if err := syncGuardImageRoot(parent); err != nil {
		return err
	}
	root, err := parent.OpenRoot(filepath.Base(options.Output))
	if err != nil {
		return fmt.Errorf("open private guard output: %w", err)
	}
	defer root.Close()
	meta := filepath.Join("docker", "contexts", "meta", guardDigest([]byte(options.Context)))
	for _, directory := range []string{"context", "home", meta} {
		if err := root.MkdirAll(directory, 0o700); err != nil {
			return fmt.Errorf("create private guard build directory: %w", err)
		}
	}
	metadata, err := json.Marshal(map[string]any{"Name": options.Context, "Metadata": map[string]any{}, "Endpoints": map[string]any{"docker": map[string]any{"Host": options.Endpoint, "SkipTLSVerify": false}}})
	if err != nil {
		return fmt.Errorf("encode dedicated guard Docker context: %w", err)
	}
	recipe := "FROM " + guardBaseReference + "\nCOPY guard /usr/local/bin/oro-factory-guard\nUSER 65534:65534\nENTRYPOINT [\"/usr/local/bin/oro-factory-guard\"]\n"
	for _, file := range []struct {
		path string
		data []byte
		mode os.FileMode
	}{
		{filepath.Join("context", "guard"), guard, 0o555},
		{filepath.Join("context", "Dockerfile"), []byte(recipe), 0o400},
		{filepath.Join("docker", "config.json"), []byte("{}"), 0o400},
		{filepath.Join(meta, "meta.json"), metadata, 0o400},
	} {
		if err := writeGuardImageFile(root, file.path, file.data, file.mode); err != nil {
			return err
		}
	}
	return nil
}

func writeGuardImageFile(root *os.Root, path string, data []byte, mode os.FileMode) error {
	file, err := root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("create guard image artifact: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write guard image artifact: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync guard image artifact: %w", err)
	}
	return nil
}

func invokeGuardImage(ctx context.Context, options guardImageOptions, runner guardImageRunner, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if _, err := readGuardProgram(ctx, options.Docker, options.DockerSHA256, guardDockerLimit, false); err != nil {
		return nil, err
	}
	if err := validateGuardEndpoint(options.Endpoint); err != nil {
		return nil, err
	}
	prefix := []string{"--config", filepath.Join(options.Output, "docker"), "--context", options.Context}
	result, err := runner(ctx, guardImageCommand{Path: options.Docker, Args: append(prefix, args...), Env: []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "LANG=C", "HOME=" + filepath.Join(options.Output, "home")}, Dir: options.Output})
	if err != nil {
		return nil, fmt.Errorf("guard image Docker operation: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("guard image Docker operation expired: %w", err)
	}
	if len(result) > guardOutputLimit {
		return nil, errors.New("guard image Docker output exceeds limit")
	}
	return result, nil
}

func guardDigest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func inspectGuardImage(data []byte, label string) (string, error) {
	var image struct {
		ID           string `json:"Id"`
		OS           string `json:"Os"`
		Architecture string
		Config       struct {
			User       string
			Entrypoint []string
			Labels     map[string]string
		}
	}
	if err := validateGuardImageJSON(data); err != nil {
		return "", err
	}
	fields, err := requireGuardImageFields(data, "Id", "Os", "Architecture")
	if err != nil {
		return "", err
	}
	if label != "" {
		if _, err := requireGuardImageFields(fields["Config"], "User", "Entrypoint", "Labels"); err != nil {
			return "", err
		}
	}
	if err := json.Unmarshal(data, &image); err != nil {
		return "", fmt.Errorf("decode guard Docker image: %w", err)
	}
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(image.ID) || image.OS != "linux" || image.Architecture != "arm64" {
		return "", errors.New("guard Docker image lacks exact immutable Linux arm64 identity")
	}
	if label != "" && (image.Config.User != "65534:65534" || !slices.Equal(image.Config.Entrypoint, []string{"/usr/local/bin/oro-factory-guard"}) || image.Config.Labels[guardImageLabel] != label) {
		return "", errors.New("derived image differs from exact guard configuration")
	}
	return image.ID, nil
}

func validateGuardImageJSON(data []byte) error {
	if len(data) > guardOutputLimit || !utf8.Valid(data) {
		return errors.New("guard image inspection exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := guardJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("guard image inspection has trailing data")
	}
	return nil
}

func requireGuardImageFields(data []byte, names ...string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, fmt.Errorf("decode guard image object: %w", err)
	}
	for _, name := range names {
		if len(fields[name]) == 0 || bytes.Equal(fields[name], []byte("null")) {
			return nil, fmt.Errorf("guard image inspection is missing exact %s", name)
		}
	}
	return fields, nil
}

func guardJSONValue(decoder *json.Decoder, depth int) error {
	if depth > 16 {
		return errors.New("guard image inspection exceeds nesting limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("read guard image JSON: %w", err)
	}
	switch token {
	case json.Delim('{'):
		if err := guardJSONMembers(decoder, depth); err != nil {
			return err
		}
	case json.Delim('['):
		for decoder.More() {
			if err := guardJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return nil
	}
	if _, err := decoder.Token(); err != nil {
		return fmt.Errorf("close guard image JSON: %w", err)
	}
	return nil
}

func guardJSONMembers(decoder *json.Decoder, depth int) error {
	seen := map[string]bool{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("read guard image field: %w", err)
		}
		key, ok := keyToken.(string)
		folded := strings.ToLower(key)
		if !ok || seen[folded] || len(seen) >= 128 {
			return errors.New("guard image inspection has duplicate or excessive fields")
		}
		seen[folded] = true
		if err := guardJSONValue(decoder, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func publishGuardImageReceipt(ctx context.Context, output string, receipt guardImageReceipt) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("guard image receipt canceled: %w", err)
	}
	data, err := json.Marshal(receipt)
	if err != nil || len(data) > 4096 {
		return errors.New("guard image receipt exceeds bounded encoding")
	}
	root, err := os.OpenRoot(output)
	if err != nil {
		return fmt.Errorf("open guard image receipt root: %w", err)
	}
	defer root.Close()
	if err := writeGuardImageFile(root, "receipt.pending", data, 0o400); err != nil {
		return err
	}
	if err := root.Rename("receipt.pending", "receipt.json"); err != nil {
		return fmt.Errorf("publish guard image receipt: %w", err)
	}
	return syncGuardImageRoot(root)
}

func syncGuardImageRoot(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("open guard image output for sync: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync guard image receipt directory: %w", err)
	}
	return nil
}

type guardOutput struct {
	mu       sync.Mutex
	data     bytes.Buffer
	cancel   context.CancelFunc
	overflow bool
}

func (output *guardOutput) Write(data []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	remaining := guardOutputLimit - output.data.Len()
	if len(data) > remaining {
		_, _ = output.data.Write(data[:remaining])
		output.overflow = true
		output.cancel()
		return remaining, errors.New("guard image command output exceeds1MiB")
	}
	_, _ = output.data.Write(data)
	return len(data), nil
}

func runGuardCommand(ctx context.Context, call guardImageCommand) ([]byte, error) {
	// Leave the last two seconds of the caller's bounded operation for Wait's
	// pipe join and positive process-group retirement, rather than extending it.
	if deadline, ok := ctx.Deadline(); ok {
		var stop context.CancelFunc
		ctx, stop = context.WithDeadline(ctx, deadline.Add(-2*time.Second))
		defer stop()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	output := &guardOutput{cancel: cancel}
	command := exec.CommandContext(ctx, call.Path, call.Args...) // #nosec G204 -- Private runner receives only the rehashed explicit Docker executable and fixed bootstrap operations; never candidate commands.
	command.Env, command.Dir = call.Env, call.Dir
	command.Stdout, command.Stderr = output, output
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return killGuardGroup(command.Process.Pid) }
	command.WaitDelay = time.Second
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start guard image command: %w", err)
	}
	waitErr := command.Wait()
	retireErr := retireGuardGroup(command.Process.Pid)
	if output.overflow {
		return nil, errors.Join(errors.New("guard image output exceeded1MiB"), retireErr)
	}
	if waitErr != nil || retireErr != nil || ctx.Err() != nil {
		return nil, errors.Join(errors.New("guard image command did not complete cleanly"), waitErr, retireErr, ctx.Err())
	}
	return output.data.Bytes(), nil
}

func killGuardGroup(pid int) error {
	err := syscall.Kill(-pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("kill owned guard image group: %w", err)
	}
	return nil
}

func retireGuardGroup(pid int) error {
	return retireGuardGroupWithSignal(pid, syscall.Kill, time.Second)
}

func retireGuardGroupWithSignal(pid int, signal func(int, syscall.Signal) error, timeout time.Duration) error {
	killError := signal(-pid, syscall.SIGKILL)
	deadline := time.Now().Add(timeout)
	var observationError error
	for time.Now().Before(deadline) {
		observationError = signal(-pid, 0)
		if errors.Is(observationError, syscall.ESRCH) {
			return nil
		}
		// A killed orphan can temporarily remain visible with EPERM while its
		// new parent reaps it. Neither permission denial nor a live probe is
		// retirement: retain both errors unless a later probe proves ESRCH.
		time.Sleep(time.Millisecond)
	}
	return errors.Join(errors.New("guard image process group did not positively retire"), killError, observationError)
}

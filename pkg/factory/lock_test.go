package factory //nolint:testpackage // Process ownership is tested through the private lock boundary.

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestFactoryProcessLock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "objective")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	owner := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFactoryLockChild$")
	owner.Env = append(os.Environ(), "FACTORY_LOCK_CHILD="+dir)
	stdout, err := owner.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	owner.Stderr = os.Stderr
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Process.Kill(); _ = owner.Wait() }()
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "locked" {
		t.Fatalf("owner did not lock: %s %v", scanner.Text(), scanner.Err())
	}
	started := time.Now()
	if release, err := lockObjective(dir); err == nil {
		_ = release()
		t.Fatal("second controller acquired owned objective")
	}
	if time.Since(started) > time.Second {
		t.Fatal("contending lock blocked")
	}
	other, err := lockObjective(filepath.Join(t.TempDir(), "other"))
	if err != nil {
		t.Fatal(err)
	}
	if err := other(); err != nil {
		t.Fatal(err)
	}
	if err := other(); err != nil {
		t.Fatalf("repeated release: %v", err)
	}
	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := owner.Wait(); err == nil {
		t.Fatal("killed owner exited successfully")
	}
	release, err := lockObjective(dir)
	if err != nil {
		t.Fatalf("owner death retained lock: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}

func TestFactoryLockChild(t *testing.T) {
	dir := os.Getenv("FACTORY_LOCK_CHILD")
	if dir == "" {
		t.Skip("subprocess fixture")
	}
	release, err := lockObjective(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	fmt.Println("locked")
	for {
		time.Sleep(time.Second)
	}
}

func TestFactoryLockErrors(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := lockObjective(file); err == nil {
		t.Fatal("file accepted as objective directory")
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "objective.lock"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := lockObjective(dir); err == nil {
		t.Fatal("directory accepted as lock file")
	}
}

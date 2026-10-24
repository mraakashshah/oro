package main

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestFactoryHostPeer(t *testing.T) {
	if socket := os.Getenv("FACTORY_HOST_PEER_SOCKET"); socket != "" {
		connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socket, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		defer connection.Close()
		if err := json.NewEncoder(connection).Encode(os.Getpid()); err != nil {
			t.Fatal(err)
		}
		var acknowledgment bool
		if err := json.NewDecoder(connection).Decode(&acknowledgment); err != nil {
			t.Fatal(err)
		}
		return
	}
	if runtime.GOOS != "darwin" {
		if _, _, err := factoryHostPeer(nil); err == nil {
			t.Fatal("unsupported peer authentication succeeded")
		}
		return
	}
	if _, _, err := factoryHostPeer(nil); err == nil {
		t.Fatal("nil connection authenticated")
	}
	dir, err := os.MkdirTemp("/private/tmp", "fhp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "socket")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := listener.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(t.Context(), executable, "-test.run=^TestFactoryHostPeer$")
	child.Env = []string{"FACTORY_HOST_PEER_SOCKET=" + socket, "GORACE=atexit_sleep_ms=0"}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	connection, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var claimedPID int
	if err := json.NewDecoder(connection).Decode(&claimedPID); err != nil {
		t.Fatal(err)
	}
	pid, uid, err := factoryHostPeer(connection)
	if err != nil || pid != child.Process.Pid || uid != os.Getuid() {
		t.Fatalf("kernel peer=%d/%d err=%v; owned child=%d/%d", pid, uid, err, child.Process.Pid, os.Getuid())
	}
	if pid == os.Getpid() {
		t.Fatal("authenticated listener instead of remote child")
	}
	if err := json.NewEncoder(connection).Encode(true); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := factoryHostPeer(connection); err == nil {
		t.Fatal("closed socket authenticated")
	}
}

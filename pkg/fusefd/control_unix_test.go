//go:build !windows

/*
 * JuiceFS, Copyright 2026 Juicedata, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package fusefd

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func shortSocketPath(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "jfs-control-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, name)
}

func TestControlEndpointPermissionsAndCleanup(t *testing.T) {
	path := shortSocketPath(t, "unmount.sock")
	called := make(chan bool, 1)
	server, err := ServeControl(path, func(force bool) error {
		called <- force
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("control endpoint mode is %v, want socket 0600", info.Mode())
	}
	if err := RequestUnmount(path, true, time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case force := <-called:
		if !force {
			t.Fatal("force flag was not forwarded")
		}
	case <-time.After(time.Second):
		t.Fatal("control handler was not called")
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("control endpoint was not removed: %v", err)
	}
}

func TestDerivedControlEndpointPermissionsAndCleanup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("pre-opened FUSE mode is Linux-only")
	}
	for _, name := range []string{Env, ControlEnv, "JFS_SUPER_COMM", "_FUSE_FD_COMM", "JFS_SUPERVISOR", "_FUSE_STATE_PATH"} {
		t.Setenv(name, "")
	}
	handoff := shortSocketPath(t, "handoff.sock")
	t.Setenv(Env, handoff)
	path, err := ControlSocket()
	if err != nil {
		t.Fatal(err)
	}
	if want := handoff + ".juicefs-control"; path != want {
		t.Fatalf("derived control socket = %q, want %q", path, want)
	}
	server, err := ServeControl(path, func(bool) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("derived control endpoint mode is %v, want socket 0600", info.Mode())
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("derived control endpoint was not removed: %v", err)
	}
}

func TestControlRequestErrorsAndTimeouts(t *testing.T) {
	t.Run("handler", func(t *testing.T) {
		path := shortSocketPath(t, "unmount.sock")
		server, err := ServeControl(path, func(bool) error { return errors.New("flush failed") })
		if err != nil {
			t.Fatal(err)
		}
		defer server.Close()
		err = RequestUnmount(path, false, time.Second)
		if err == nil || !strings.Contains(err.Error(), "flush failed") {
			t.Fatalf("handler error was not returned: %v", err)
		}
	})

	t.Run("no-server", func(t *testing.T) {
		err := RequestUnmount(filepath.Join(t.TempDir(), "missing.sock"), false, 50*time.Millisecond)
		if err == nil || !strings.Contains(err.Error(), "connect clean-unmount") {
			t.Fatalf("missing server: %v", err)
		}
	})

	t.Run("silent-server", func(t *testing.T) {
		path := shortSocketPath(t, "silent.sock")
		listener, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		release := make(chan struct{})
		defer close(release)
		go func() {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
			<-release
		}()
		start := time.Now()
		err = RequestUnmount(path, false, 50*time.Millisecond)
		if err == nil || !strings.Contains(err.Error(), "receive clean-unmount response") {
			t.Fatalf("silent server: %v", err)
		}
		if time.Since(start) > time.Second {
			t.Fatalf("request timeout was not bounded: %s", time.Since(start))
		}
	})
}

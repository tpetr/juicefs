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
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
	"golang.org/x/sys/unix"
)

func transfer(t *testing.T, payload []byte, fds ...int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fd.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, err := listener.AcceptUnix()
		if err != nil {
			return
		}
		defer conn.Close()
		if payload != nil {
			var rights []byte
			if len(fds) > 0 {
				rights = unix.UnixRights(fds...)
			}
			if _, _, err := conn.WriteMsgUnix(payload, rights, nil); err != nil {
				t.Error(err)
			}
		}
	}()
	return path
}

func TestReceive(t *testing.T) {
	file, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for _, tc := range []struct {
		name    string
		count   int
		payload []byte
		want    string
	}{
		{"one", 1, []byte("arbitrary payload is not InitIn"), ""},
		{"zero", 0, []byte("x"), "expected exactly one FD, received 0"},
		{"two", 2, []byte("x"), "expected exactly one FD, received 2"},
		{"truncated", 16, []byte("x"), "control data was truncated"},
		{"closed", 0, nil, "peer closed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fds := make([]int, tc.count)
			for i := range fds {
				fds[i] = int(file.Fd())
			}
			fd, err := receive(transfer(t, tc.payload, fds...), time.Second)
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) || fd != -1 {
					t.Fatalf("fd=%d err=%v, want %s", fd, err, tc.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer unix.Close(fd)
			flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
			if err != nil || flags&unix.FD_CLOEXEC == 0 {
				t.Fatalf("close-on-exec: flags=%d err=%v", flags, err)
			}
			var stat unix.Stat_t
			if err := unix.Fstat(fd, &stat); err != nil {
				t.Fatal(err)
			}
		})
	}
	if _, err := receive(filepath.Join(t.TempDir(), "missing"), time.Second); err == nil || !strings.Contains(err.Error(), "connect pre-opened FUSE socket") {
		t.Fatalf("missing socket: %v", err)
	}
}

// A pipe has no writer after rejected SCM_RIGHTS descriptors have been closed.
func TestReceiveClosesRejectedFDs(t *testing.T) {
	for _, count := range []int{2, 16} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			fds := make([]int, count)
			for i := range fds {
				fds[i] = int(writer.Fd())
			}
			_, err = receive(transfer(t, []byte("x"), fds...), time.Second)
			writer.Close()
			if err == nil {
				t.Fatal("accepted extra descriptors")
			}
			if err := reader.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			b := make([]byte, 1)
			if n, err := reader.Read(b); n != 0 || err != io.EOF {
				t.Fatalf("received FDs leaked: n=%d err=%v", n, err)
			}
		})
	}
}

type initFS struct {
	fuse.RawFileSystem
	initialized chan *fuse.InitIn
}

func (fs *initFS) Init(server *fuse.Server) { fs.initialized <- server.KernelSettings() }

func cleanEnvironment(t *testing.T) {
	for _, name := range []string{Env, ControlEnv, "JFS_SUPER_COMM", "_FUSE_FD_COMM", "JFS_SUPERVISOR", "_FUSE_STATE_PATH"} {
		t.Setenv(name, "")
	}
}

func TestBootstrapInit(t *testing.T) {
	cleanEnvironment(t)
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(pair[0])
	defer func() { _ = unix.Close(pair[1]) }()
	if err := unix.SetsockoptTimeval(pair[0], unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Sec: 5}); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", pair[1]))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(Env, transfer(t, []byte("not serialized FUSE state"), pair[1]))
	fs := &initFS{RawFileSystem: fuse.NewDefaultRawFileSystem(), initialized: make(chan *fuse.InitIn, 1)}
	type result struct {
		server *fuse.Server
		err    error
	}
	done := make(chan result, 1)
	go func() {
		server, err := NewServer(fs, &fuse.MountOptions{DirectMount: true, DirectMountStrict: true})
		done <- result{server, err}
	}()
	select {
	case <-fs.initialized:
		t.Fatal("filesystem initialized before FUSE_INIT")
	case r := <-done:
		t.Fatalf("server returned before FUSE_INIT: %v", r.err)
	case <-time.After(50 * time.Millisecond):
	}
	// Wire-format kernel INIT (opcode 26), not control-socket payload.
	input := fuse.InitIn{InHeader: fuse.InHeader{Opcode: 26, Unique: 42}, Major: 7, Minor: 38, MaxReadAhead: 65536, Flags: fuse.CAP_ASYNC_READ}
	input.Length = uint32(binary.Size(input))
	var request bytes.Buffer
	if err := binary.Write(&request, binary.NativeEndian, input); err != nil {
		t.Fatal(err)
	}
	if _, err := unix.Write(pair[0], request.Bytes()); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 256)
	n, err := unix.Read(pair[0], reply)
	if err != nil {
		t.Fatal(err)
	}
	var header fuse.OutHeader
	var output fuse.InitOut
	response := bytes.NewReader(reply[:n])
	if err := binary.Read(response, binary.NativeEndian, &header); err != nil {
		t.Fatal(err)
	}
	if err := binary.Read(response, binary.NativeEndian, &output); err != nil {
		t.Fatal(err)
	}
	if header.Unique != 42 || header.Status != 0 || header.Length != uint32(n) || output.Major != 7 || output.Minor == 0 || output.MaxWrite == 0 || output.Flags&fuse.CAP_ASYNC_READ == 0 {
		t.Fatalf("invalid INIT reply: %+v %+v", header, output)
	}
	select {
	case settings := <-fs.initialized:
		if settings.Major != input.Major || settings.Minor != input.Minor || settings.MaxReadAhead != input.MaxReadAhead {
			t.Fatalf("settings not from kernel: %+v", settings)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("filesystem never initialized")
	}
	var server *fuse.Server
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatal(r.err)
		}
		server = r.server
	case <-time.After(5 * time.Second):
		t.Fatal("NewServer did not complete")
	}
	// Only the server's received duplicate remains after closing the sender FD.
	unix.Close(pair[1])
	pair[1] = -1
	served := make(chan struct{})
	go func() { server.Serve(); close(served) }()
	unix.Shutdown(pair[0], unix.SHUT_RDWR)
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not close")
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		link, _ := os.Readlink("/proc/self/fd/" + entry.Name())
		if link == target {
			t.Fatalf("server leaked FD %s", entry.Name())
		}
	}
}

func TestBootstrapInitError(t *testing.T) {
	cleanEnvironment(t)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	// A write-only FD cannot read INIT. On failure go-fuse must close its copy.
	t.Setenv(Env, transfer(t, []byte("x"), int(writer.Fd())))
	fs := &initFS{RawFileSystem: fuse.NewDefaultRawFileSystem(), initialized: make(chan *fuse.InitIn, 1)}
	if _, err := NewServer(fs, nil); err == nil {
		t.Fatal("expected INIT read failure")
	}
	select {
	case <-fs.initialized:
		t.Fatal("initialized after failed handshake")
	default:
	}
	writer.Close()
	reader.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := reader.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("FD leaked after INIT failure: %v", err)
	}
}

func TestSocket(t *testing.T) {
	cleanEnvironment(t)
	t.Setenv("JFS_SUPER_COMM", "legacy")
	if path, err := Socket(); path != "" || err != nil {
		t.Fatalf("normal mode changed: %q %v", path, err)
	}
	t.Setenv("JFS_SUPER_COMM", "")
	t.Setenv(Env, "/bootstrap.sock")
	for _, name := range []string{"JFS_SUPER_COMM", "_FUSE_FD_COMM", "JFS_SUPERVISOR", "_FUSE_STATE_PATH"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "conflict")
			if _, err := Socket(); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("conflict: %v", err)
			}
		})
	}
	if path, err := Socket(); path != "/bootstrap.sock" || err != nil {
		t.Fatalf("bootstrap: %q %v", path, err)
	}
	if path, err := ControlSocket(); path != "/bootstrap.sock.control" || err != nil {
		t.Fatalf("derived control socket: %q %v", path, err)
	}
	t.Setenv(ControlEnv, "/control.sock")
	if path, err := ControlSocket(); path != "/control.sock" || err != nil {
		t.Fatalf("configured control socket: %q %v", path, err)
	}
	t.Setenv(ControlEnv, "/bootstrap.sock")
	if _, err := ControlSocket(); err == nil || !strings.Contains(err.Error(), "must differ") {
		t.Fatalf("shared bootstrap and control socket: %v", err)
	}
}

func TestReceiveTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "silent.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	release := make(chan struct{})
	defer close(release)
	accepted := make(chan struct{})
	go func() {
		conn, err := listener.AcceptUnix()
		if err != nil {
			return
		}
		defer conn.Close()
		close(accepted)
		<-release
	}()
	fd, err := receive(path, 50*time.Millisecond)
	if fd != -1 || !errors.Is(err, os.ErrDeadlineExceeded) || !strings.Contains(err.Error(), "read pre-opened FUSE socket") {
		t.Fatalf("silent peer: fd=%d err=%v", fd, err)
	}
	select {
	case <-accepted:
	default:
		t.Fatal("peer never accepted the connection")
	}
}

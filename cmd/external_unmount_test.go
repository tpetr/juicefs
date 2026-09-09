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

package cmd

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/juicedata/juicefs/pkg/chunk"
	"github.com/juicedata/juicefs/pkg/fusefd"
	"github.com/juicedata/juicefs/pkg/vfs"
)

func TestExternalUnmountLifecycle(t *testing.T) {
	flushFailure := errors.New("flush failure")
	unmountFailure := errors.New("proxy failure")
	for _, test := range []struct {
		name          string
		flushErr      error
		unmountErr    error
		want          string
		wantCompleted bool
		wantResult    string
	}{
		{name: "success", wantCompleted: true},
		{name: "flush failure", flushErr: flushFailure, want: "flush failure", wantCompleted: true, wantResult: "flush failure"},
		{name: "proxy failure", unmountErr: unmountFailure, want: "proxy failure"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var order []string
			u := &externalUnmount{
				flush: func() error {
					order = append(order, "flush")
					return test.flushErr
				},
				unmount: func(bool) error {
					order = append(order, "fusermount")
					return test.unmountErr
				},
			}
			err := u.request(false)
			if strings.Join(order, ",") != "flush,fusermount" {
				t.Fatalf("operation order is %v", order)
			}
			if test.want == "" && err != nil || test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("request error is %v, want %q", err, test.want)
			}
			completed, result := u.mountResult()
			if completed != test.wantCompleted {
				t.Fatalf("completed is %v, want %v", completed, test.wantCompleted)
			}
			if test.wantResult == "" && result != nil || test.wantResult != "" && (result == nil || !strings.Contains(result.Error(), test.wantResult)) {
				t.Fatalf("mount result is %v, want %q", result, test.wantResult)
			}
		})
	}
}

func TestMountResourcesCleanupOnce(t *testing.T) {
	var sessions, storages int
	resources := &mountResources{
		closeSession: func() error {
			sessions++
			return nil
		},
		shutdownStorage: func() { storages++ },
	}
	if err := resources.close(); err != nil {
		t.Fatal(err)
	}
	if err := resources.close(); err != nil {
		t.Fatal(err)
	}
	if sessions != 1 || storages != 1 {
		t.Fatalf("cleanup counts: metadata=%d storage=%d", sessions, storages)
	}
}

func TestPreopenedUmountSkipsMountConfig(t *testing.T) {
	dummy := t.TempDir()
	if err := os.WriteFile(filepath.Join(dummy, ".config"), []byte("not JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	var requested, read, local int
	ops := umountOperations{
		requestExternal: func(path string, force bool, timeout time.Duration) error {
			requested++
			if path != "/private/control.sock" || force || timeout <= 0 {
				t.Fatalf("unexpected request: path=%q force=%v timeout=%s", path, force, timeout)
			}
			return nil
		},
		readConfig: func(string) ([]byte, error) {
			read++
			return nil, errors.New("dummy config inspected")
		},
		localUnmount: func(string, bool) error {
			local++
			return nil
		},
	}
	if err := unmountMountpoint(dummy, true, false, "/private/control.sock", ops); err != nil {
		t.Fatal(err)
	}
	if requested != 1 || read != 0 || local != 0 {
		t.Fatalf("calls: request=%d readConfig=%d localUnmount=%d", requested, read, local)
	}
}

func TestPreopenedUmountCommandSkipsMountConfig(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("pre-opened FUSE mode is Linux-only")
	}
	dir, err := os.MkdirTemp("/tmp", "jfs-umount-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	controlPath := filepath.Join(dir, "control.sock")
	requested := make(chan struct{}, 1)
	server, err := fusefd.ServeControl(controlPath, func(force bool) error {
		if force {
			t.Error("unexpected forced unmount")
		}
		requested <- struct{}{}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	for _, name := range []string{"JFS_SUPER_COMM", "_FUSE_FD_COMM", "JFS_SUPERVISOR", "_FUSE_STATE_PATH"} {
		t.Setenv(name, "")
	}
	t.Setenv(fusefd.Env, filepath.Join(dir, "bootstrap.sock"))
	t.Setenv(fusefd.ControlEnv, controlPath)
	dummy := filepath.Join(dir, "dummy")
	if err := os.Mkdir(dummy, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dummy, ".config"), []byte("not JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Main([]string{"juicefs", "umount", "--flush", dummy}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-requested:
	case <-time.After(time.Second):
		t.Fatal("foreground mount process did not receive the request")
	}
}

func TestNormalUmountStillReadsConfigAndUnmountsLocally(t *testing.T) {
	raw, err := json.Marshal(vfs.Config{Chunk: &chunk.Config{}})
	if err != nil {
		t.Fatal(err)
	}
	var read, local int
	ops := umountOperations{
		requestExternal: func(string, bool, time.Duration) error {
			return errors.New("unexpected external request")
		},
		readConfig: func(mp string) ([]byte, error) {
			read++
			if mp != "/normal" {
				t.Fatalf("mountpoint is %q", mp)
			}
			return raw, nil
		},
		localUnmount: func(mp string, force bool) error {
			local++
			if mp != "/normal" || !force {
				t.Fatalf("local unmount: path=%q force=%v", mp, force)
			}
			return nil
		},
	}
	if err := unmountMountpoint("/normal", true, true, "", ops); err != nil {
		t.Fatal(err)
	}
	if read != 1 || local != 1 {
		t.Fatalf("calls: readConfig=%d localUnmount=%d", read, local)
	}
}

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
	"github.com/juicedata/juicefs/pkg/meta"
	"github.com/juicedata/juicefs/pkg/object"
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

func TestExternalCheckpointIsSerializedWithUnmount(t *testing.T) {
	var operations []string
	u := &externalUnmount{
		checkpoint: func(destination string) error {
			operations = append(operations, "checkpoint:"+destination)
			return nil
		},
		flush: func() error {
			operations = append(operations, "flush")
			return nil
		},
		unmount: func(bool) error {
			operations = append(operations, "unmount")
			return nil
		},
	}
	if err := u.handle(fusefd.ControlRequest{Operation: "checkpoint", Destination: "/checkpoints/meta.bin"}); err != nil {
		t.Fatal(err)
	}
	if err := u.request(false); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(operations, ","); got != "checkpoint:/checkpoints/meta.bin,flush,unmount" {
		t.Fatalf("operation order: %s", got)
	}
}

func TestSQLiteCheckpointLoadsAndLeavesVFSUsable(t *testing.T) {
	dir := t.TempDir()
	uri := "sqlite3://" + filepath.Join(dir, "source.db")
	source := meta.NewClient(uri, meta.DefaultConf())
	if err := source.Reset(); err != nil {
		t.Fatal(err)
	}
	if err := source.Init(&meta.Format{Name: "checkpoint", BlockSize: 4096, Capacity: 1 << 30}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Load(true); err != nil {
		t.Fatal(err)
	}
	storage, err := object.CreateStorage("mem", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	conf := &vfs.Config{Meta: meta.DefaultConf(), Chunk: &chunk.Config{BlockSize: 4096}}
	v := vfs.NewVFS(conf, source, chunk.NewCachedStore(storage, *conf.Chunk, nil), nil, nil)
	ctx := vfs.NewLogContext(meta.NewContext(1, 0, []uint32{0}))
	if _, eno := v.Mkdir(ctx, meta.RootInode, "before", 0755, 0); eno != 0 {
		t.Fatalf("mkdir before checkpoint: %s", eno)
	}
	destination := filepath.Join(dir, "checkpoint.bin")
	if err := checkpointSQLite(v, source, destination, dir, nil); err != nil {
		t.Fatal(err)
	}
	if err := checkpointSQLite(v, source, destination, dir, nil); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing destination error: %v", err)
	}
	if err := checkpointSQLite(v, source, filepath.Join(dir, "outside", "metadata.bin"), dir, nil); err == nil {
		t.Fatal("checkpoint accepted an unsafe destination")
	}
	f, err := os.Open(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	destinationMeta := meta.NewClient("sqlite3://"+filepath.Join(dir, "destination.db"), meta.DefaultConf())
	if err := destinationMeta.Reset(); err != nil {
		t.Fatal(err)
	}
	if err := destinationMeta.LoadMetaV2(meta.Background(), f, nil); err != nil {
		t.Fatal(err)
	}
	var ino meta.Ino
	var attr meta.Attr
	if eno := destinationMeta.Lookup(meta.Background(), meta.RootInode, "before", &ino, &attr, true); eno != 0 {
		t.Fatalf("checkpoint is missing prior write: %s", eno)
	}
	if _, eno := v.Mkdir(ctx, meta.RootInode, "after", 0755, 0); eno != 0 {
		t.Fatalf("mkdir after checkpoint: %s", eno)
	}
	if err := checkpointSQLite(v, source, filepath.Join(dir, "checkpoint-2.bin"), dir, nil); err != nil {
		t.Fatal(err)
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

func TestPreopenedMountAndUmountUseSameDefaultControlPath(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("pre-opened FUSE mode is Linux-only")
	}
	dir := t.TempDir()
	for _, name := range []string{"JFS_SUPER_COMM", "_FUSE_FD_COMM", "JFS_SUPERVISOR", "_FUSE_STATE_PATH", fusefd.ControlEnv} {
		t.Setenv(name, "")
	}
	handoff := filepath.Join(dir, "handoff.sock")
	t.Setenv(fusefd.Env, handoff)
	controlPath, err := fusefd.ControlSocket()
	if err != nil {
		t.Fatal(err)
	}
	if want := handoff + ".juicefs-control"; controlPath != want {
		t.Fatalf("control path = %q, want %q", controlPath, want)
	}
	requested := make(chan struct{}, 1)
	external, err := newExternalUnmount(controlPath, func() error { return nil }, func(bool) error {
		requested <- struct{}{}
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer external.close()
	dummy := filepath.Join(dir, "dummy")
	if err := os.Mkdir(dummy, 0700); err != nil {
		t.Fatal(err)
	}
	if err := Main([]string{"juicefs", "umount", "--flush", dummy}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-requested:
	case <-time.After(time.Second):
		t.Fatal("default control socket did not receive the request")
	}
}

func TestPreopenedCheckpointCommandSkipsMountConfig(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("pre-opened FUSE mode is Linux-only")
	}
	dir, err := os.MkdirTemp("/tmp", "jfs-checkpoint-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	controlPath := filepath.Join(dir, "control.sock")
	requested := make(chan string, 1)
	server, err := fusefd.ServeControlRequests(controlPath, func(request fusefd.ControlRequest) error {
		if !request.IsCheckpoint() {
			t.Fatalf("unexpected request: %+v", request)
		}
		requested <- request.Destination
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
	destination := filepath.Join(dir, "metadata.bin")
	if err := Main([]string{"juicefs", "checkpoint", destination}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-requested:
		if got != destination {
			t.Fatalf("destination: %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("foreground mount process did not receive checkpoint")
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

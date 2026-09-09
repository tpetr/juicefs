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
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/juicedata/juicefs/pkg/fusefd"
	"github.com/juicedata/juicefs/pkg/meta"
	"github.com/juicedata/juicefs/pkg/vfs"
	"github.com/urfave/cli/v2"
)

const checkpointTimeout = 2 * time.Minute

func cmdCheckpoint() *cli.Command {
	return &cli.Command{
		Name:      "checkpoint",
		Category:  "SERVICE",
		Usage:     "Request a binary metadata checkpoint from a pre-opened FUSE mount",
		ArgsUsage: "DESTINATION",
		Action: func(ctx *cli.Context) error {
			setup(ctx, 1)
			control, err := fusefd.ControlSocket()
			if err != nil {
				return err
			}
			if control == "" {
				return fmt.Errorf("checkpoint requires %s", fusefd.Env)
			}
			return fusefd.RequestCheckpoint(control, ctx.Args().First(), checkpointTimeout)
		},
	}
}

func checkpointSQLite(v *vfs.VFS, m meta.Meta, destination, checkpointDir string, waitWriteback func() error) error {
	if m.Name() != "sqlite3" {
		return fmt.Errorf("online checkpoint is supported only for sqlite3 metadata, not %s", m.Name())
	}
	dir, err := filepath.EvalSymlinks(checkpointDir)
	if err != nil {
		return fmt.Errorf("resolve checkpoint directory %q: %w", checkpointDir, err)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("checkpoint directory %q is not a directory", checkpointDir)
	}
	destination = filepath.Clean(destination)
	destinationDir, err := filepath.EvalSymlinks(filepath.Dir(destination))
	if !filepath.IsAbs(destination) || err != nil || destinationDir != dir || filepath.Base(destination) == "." {
		return fmt.Errorf("checkpoint destination must be an absolute file directly in %q", dir)
	}
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("checkpoint destination %q already exists", destination)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect checkpoint destination %q: %w", destination, err)
	}
	return v.Checkpoint(func() error {
		if waitWriteback != nil {
			if err := waitWriteback(); err != nil {
				return err
			}
		}
		tmp, err := os.CreateTemp(dir, ".juicefs-checkpoint-*")
		if err != nil {
			return fmt.Errorf("create checkpoint temporary file: %w", err)
		}
		tmpName := tmp.Name()
		defer func() { _ = os.Remove(tmpName) }()
		defer tmp.Close()
		ctx := meta.WrapWithTimeout(meta.Background(), checkpointTimeout)
		defer ctx.Cancel()
		if err := m.DumpMetaV2(ctx, tmp, &meta.DumpOption{Threads: 1}); err != nil {
			return fmt.Errorf("dump sqlite metadata: %w", err)
		}
		if err := tmp.Sync(); err != nil {
			return fmt.Errorf("sync checkpoint: %w", err)
		}
		if err := tmp.Close(); err != nil {
			return fmt.Errorf("close checkpoint: %w", err)
		}
		if err := os.Link(tmpName, destination); err != nil {
			return fmt.Errorf("publish checkpoint: %w", err)
		}
		if err := os.Remove(tmpName); err != nil {
			_ = os.Remove(destination)
			return fmt.Errorf("remove checkpoint temporary file: %w", err)
		}
		parent, err := os.Open(dir)
		if err != nil {
			return fmt.Errorf("open checkpoint directory: %w", err)
		}
		defer parent.Close()
		if err := parent.Sync(); err != nil {
			_ = os.Remove(destination)
			return fmt.Errorf("sync checkpoint directory: %w", err)
		}
		return nil
	})
}

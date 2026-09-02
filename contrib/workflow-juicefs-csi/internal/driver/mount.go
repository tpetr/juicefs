/*
 * Copyright 2026 Semgrep, Inc.
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

package driver

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const outputTailLimit = 16 << 10

type mountSession struct {
	volumeID    string
	targetPath  string
	privatePath string
	statePath   string
	metadataURL string
	juicefsPath string
	process     *exec.Cmd
	processDone chan struct{}
	processErr  error
	processMu   sync.Mutex
	output      *tailBuffer
	logger      *slog.Logger
}

func startMount(ctx context.Context, config Config, volumeID, target string, pod podIdentity, logger *slog.Logger) (_ *mountSession, returnedErr error) {
	statePath := filepath.Join(config.StateRoot, shortID(volumeID))
	if err := os.MkdirAll(config.StateRoot, 0o700); err != nil {
		return nil, fmt.Errorf("prepare state root: %w", err)
	}
	if err := ensureNewDirectory(statePath); err != nil {
		return nil, fmt.Errorf("prepare private state: %w", err)
	}
	session := &mountSession{
		volumeID: volumeID, targetPath: target, statePath: statePath,
		privatePath: filepath.Join(statePath, "mount"), metadataURL: "sqlite3://" + filepath.Join(statePath, "metadata.db"),
		juicefsPath: config.JuiceFSPath, processDone: make(chan struct{}), output: newTailBuffer(outputTailLimit), logger: logger,
	}
	defer func() {
		if returnedErr != nil {
			session.rollback()
		}
	}()
	for _, path := range []string{session.privatePath, filepath.Join(statePath, "cache")} {
		if err := os.Mkdir(path, 0o700); err != nil {
			return nil, fmt.Errorf("create private path: %w", err)
		}
	}
	if err := os.MkdirAll(target, 0o750); err != nil {
		return nil, fmt.Errorf("create kubelet target: %w", err)
	}

	bucketURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com", config.Bucket, config.Region)
	formatArgs := []string{"format", "--storage", "s3", "--bucket", bucketURL, "--bucket-prefix", pod.objectDataPrefix, "--trash-days", "0", session.metadataURL, "data"}
	if err := runCommand(ctx, config.JuiceFSPath, formatArgs, nil); err != nil {
		return nil, fmt.Errorf("format fresh SQLite metadata: %w", err)
	}

	mountArgs := []string{"mount", "--foreground", "--no-usage-report", "--cache-dir", filepath.Join(statePath, "cache"), session.metadataURL, session.privatePath}
	cmd := exec.Command(config.JuiceFSPath, mountArgs...)
	cmd.Env = append(os.Environ(), "JFS_INSIDE_CONTAINER=1", "JFS_FOREGROUND=1")
	cmd.Stdout = session.output
	cmd.Stderr = session.output
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start JuiceFS mount: %w", err)
	}
	session.process = cmd
	go func() {
		err := cmd.Wait()
		session.processMu.Lock()
		session.processErr = err
		session.processMu.Unlock()
		close(session.processDone)
	}()

	readyCtx, cancel := context.WithTimeout(context.Background(), config.MountTimeout)
	defer cancel()
	if err := session.waitReady(readyCtx); err != nil {
		return nil, err
	}
	if err := runCommand(ctx, "/bin/mount", []string{"--bind", session.privatePath, target}, nil); err != nil {
		return nil, fmt.Errorf("bind private mount to kubelet target: %w", err)
	}
	mounted, err := isMounted(target)
	if err != nil || !mounted {
		return nil, fmt.Errorf("verify bind mount: mounted=%t error=%v", mounted, err)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return nil, fmt.Errorf("read bind-mounted filesystem: %w", err)
	}
	logger.Info("JuiceFS mount is readable before publish returns", "root_entry_count", len(entries))
	return session, nil
}

func (s *mountSession) waitReady(ctx context.Context) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("mount readiness timed out: %w; output_tail=%q", ctx.Err(), s.output.String())
		case <-s.processDone:
			return fmt.Errorf("JuiceFS exited before readiness: %v; output_tail=%q", s.exitErr(), s.output.String())
		case <-ticker.C:
			mounted, err := isMounted(s.privatePath)
			if err != nil || !mounted {
				continue
			}
			info, err := os.Stat(s.privatePath)
			if err != nil {
				continue
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if ok && stat.Ino == 1 {
				if _, err = os.ReadDir(s.privatePath); err == nil {
					return nil
				}
			}
		}
	}
}

func (s *mountSession) stop(ctx context.Context, timeout time.Duration) error {
	stopCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if mounted, err := isMounted(s.targetPath); err != nil {
		return fmt.Errorf("inspect bind target: %w", err)
	} else if mounted {
		if err = runCommand(stopCtx, "/bin/umount", []string{s.targetPath}, nil); err != nil {
			return fmt.Errorf("remove kubelet bind mount: %w", err)
		}
	}
	if mounted, err := isMounted(s.privatePath); err != nil {
		return fmt.Errorf("inspect private mount: %w", err)
	} else if mounted {
		if err = runCommand(stopCtx, s.juicefsPath, []string{"umount", "--flush", s.privatePath}, nil); err != nil {
			return fmt.Errorf("clean JuiceFS unmount: %w", err)
		}
	}
	select {
	case <-s.processDone:
		if err := s.exitErr(); err != nil {
			return fmt.Errorf("JuiceFS process exit: %w; output_tail=%q", err, s.output.String())
		}
	case <-stopCtx.Done():
		return fmt.Errorf("wait for JuiceFS process: %w; output_tail=%q", stopCtx.Err(), s.output.String())
	}
	if err := os.RemoveAll(s.statePath); err != nil {
		return fmt.Errorf("remove private state: %w", err)
	}
	return nil
}

func (s *mountSession) rollback() {
	if mounted, _ := isMounted(s.targetPath); mounted {
		_ = runCommand(context.Background(), "/bin/umount", []string{s.targetPath}, nil)
	}
	if mounted, _ := isMounted(s.privatePath); mounted {
		_ = runCommand(context.Background(), "/bin/umount", []string{"-l", s.privatePath}, nil)
	}
	if s.process != nil {
		select {
		case <-s.processDone:
		default:
			_ = s.process.Process.Kill()
			select {
			case <-s.processDone:
			case <-time.After(5 * time.Second):
			}
		}
	}
	_ = os.RemoveAll(s.statePath)
}

func (s *mountSession) exitErr() error {
	s.processMu.Lock()
	defer s.processMu.Unlock()
	return s.processErr
}

func runCommand(ctx context.Context, executable string, args []string, extraEnv []string) error {
	cmd := exec.CommandContext(ctx, executable, args...)
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	output := newTailBuffer(outputTailLimit)
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("command %s failed: %w; output_tail=%q", filepath.Base(executable), err, output.String())
	}
	return nil
}

func isMounted(target string) (bool, error) {
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return false, err
	}
	defer file.Close()
	want := escapeMountInfoPath(target)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) > 4 && fields[4] == want {
			return true, nil
		}
	}
	return false, scanner.Err()
}

func escapeMountInfoPath(path string) string {
	replacer := strings.NewReplacer("\\", `\134`, " ", `\040`, "\t", `\011`, "\n", `\012`)
	return replacer.Replace(path)
}

func shortID(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:12])
}

type tailBuffer struct {
	mu    sync.Mutex
	limit int
	data  []byte
}

func newTailBuffer(limit int) *tailBuffer {
	return &tailBuffer{limit: limit}
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if len(b.data) > b.limit {
		b.data = append([]byte(nil), b.data[len(b.data)-b.limit:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(bytes.TrimSpace(b.data))
}

var _ io.Writer = (*tailBuffer)(nil)
var _ = errors.Is

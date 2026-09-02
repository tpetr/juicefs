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
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

const controlSocketName = "workspace.sock"

func (s *mountSession) startControl() error {
	info, err := os.Lstat(s.controlDir)
	if err != nil {
		return fmt.Errorf("inspect control EmptyDir: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("control path is not a real directory")
	}
	socketPath := s.controlSocketPath()
	if info, err = os.Lstat(socketPath); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("refusing to remove non-socket control endpoint")
		}
		if err = os.Remove(socketPath); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen on success-intent socket: %w", err)
	}
	if err = os.Chmod(socketPath, 0o666); err != nil {
		listener.Close()
		return fmt.Errorf("make success-intent socket accessible to the pod: %w", err)
	}
	s.control = listener
	s.controlWG.Add(1)
	go func() {
		defer s.controlWG.Done()
		s.serveControl(listener)
	}()
	return nil
}

func (s *mountSession) serveControl(listener net.Listener) {
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		s.controlWG.Add(1)
		go func() {
			defer s.controlWG.Done()
			s.handleControl(connection)
		}()
	}
}

func (s *mountSession) handleControl(connection net.Conn) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(connection).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "MARK_SUCCESS" {
		_, _ = connection.Write([]byte("ERROR invalid request\n"))
		return
	}
	s.stateMu.Lock()
	s.state.SuccessIntent = true
	err = saveVolumeState(s.statePath, s.state)
	s.stateMu.Unlock()
	if err != nil {
		_, _ = connection.Write([]byte("ERROR persist failed\n"))
		s.logger.Error("persist success intent failed", "error", err)
		return
	}
	_, _ = connection.Write([]byte("OK\n"))
	s.logger.Info("workspace success intent persisted")
}

func (s *mountSession) closeControl() {
	if s.control != nil {
		_ = s.control.Close()
		s.controlWG.Wait()
		_ = os.Remove(s.controlSocketPath())
		s.control = nil
	}
}

func (s *mountSession) controlSocketPath() string {
	return s.controlDir + string(os.PathSeparator) + controlSocketName
}

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
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

const (
	controlOperation  = "clean-unmount"
	controlMaxMessage = 4096
	ControlTimeout    = 5 * time.Minute
)

type controlRequest struct {
	Operation string `json:"operation"`
	Force     bool   `json:"force,omitempty"`
}

type controlResponse struct {
	Error string `json:"error,omitempty"`
}

// ControlServer owns a private endpoint used to request clean external unmounts.
type ControlServer struct {
	path     string
	listener *net.UnixListener
	once     sync.Once
}

// ServeControl creates a mode-0600 Unix socket and starts accepting requests.
func ServeControl(path string, handler func(bool) error) (*ControlServer, error) {
	if path == "" {
		return nil, fmt.Errorf("clean-unmount control socket is empty")
	}
	if handler == nil {
		return nil, fmt.Errorf("clean-unmount handler is nil")
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("clean-unmount control path %q is not a socket", path)
		}
		conn, dialErr := net.DialTimeout("unix", path, 100*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			return nil, fmt.Errorf("clean-unmount control socket %q is already in use", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale clean-unmount control socket %q: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect clean-unmount control socket %q: %w", path, err)
	}

	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen on clean-unmount control socket %q: %w", path, err)
	}
	listener.SetUnlinkOnClose(false)
	if err := os.Chmod(path, 0600); err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("restrict clean-unmount control socket %q: %w", path, err)
	}
	server := &ControlServer{path: path, listener: listener}
	go server.serve(handler)
	return server, nil
}

func (s *ControlServer) serve(handler func(bool) error) {
	for {
		conn, err := s.listener.AcceptUnix()
		if err != nil {
			return
		}
		go handleControlConnection(conn, handler)
	}
}

func handleControlConnection(conn *net.UnixConn, handler func(bool) error) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(ControlTimeout))
	var request controlRequest
	err := authorizeControl(conn)
	if err == nil {
		err = json.NewDecoder(io.LimitReader(conn, controlMaxMessage)).Decode(&request)
	}
	if err == nil && request.Operation != controlOperation {
		err = fmt.Errorf("unsupported clean-unmount operation %q", request.Operation)
	}
	if err == nil {
		err = handler(request.Force)
	}
	response := controlResponse{}
	if err != nil {
		response.Error = err.Error()
	}
	_ = json.NewEncoder(conn).Encode(&response)
}

// RequestUnmount asks the foreground mount process to flush and unmount.
func RequestUnmount(path string, force bool, timeout time.Duration) error {
	if timeout <= 0 {
		return fmt.Errorf("clean-unmount timeout must be positive")
	}
	conn, err := net.DialTimeout("unix", path, timeout)
	if err != nil {
		return fmt.Errorf("connect clean-unmount control socket %q: %w", path, err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return fmt.Errorf("set clean-unmount control deadline: %w", err)
	}
	if err := json.NewEncoder(conn).Encode(&controlRequest{Operation: controlOperation, Force: force}); err != nil {
		return fmt.Errorf("send clean-unmount request: %w", err)
	}
	var response controlResponse
	if err := json.NewDecoder(io.LimitReader(conn, controlMaxMessage)).Decode(&response); err != nil {
		return fmt.Errorf("receive clean-unmount response: %w", err)
	}
	if response.Error != "" {
		return fmt.Errorf("clean unmount failed: %s", response.Error)
	}
	return nil
}

// Close stops the server and removes its socket.
func (s *ControlServer) Close() error {
	if s == nil {
		return nil
	}
	var closeErr error
	s.once.Do(func() {
		if err := s.listener.Close(); err != nil {
			closeErr = err
		}
		if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) && closeErr == nil {
			closeErr = err
		}
	})
	return closeErr
}

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
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestControlSocketPersistsSuccessIntent(t *testing.T) {
	statePath := t.TempDir()
	controlPath, err := os.MkdirTemp("/tmp", "wcsi-control-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(controlPath) })
	session := &mountSession{statePath: statePath, controlDir: controlPath, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), state: volumeState{Version: 1}}
	if err := session.startControl(); err != nil {
		t.Fatal(err)
	}
	defer session.closeControl()
	connection, err := net.Dial("unix", session.controlSocketPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = connection.Write([]byte("MARK_SUCCESS\n")); err != nil {
		t.Fatal(err)
	}
	response, err := bufio.NewReader(connection).ReadString('\n')
	connection.Close()
	if err != nil || response != "OK\n" {
		t.Fatalf("response = %q, %v", response, err)
	}
	if !session.state.SuccessIntent {
		t.Fatal("success intent was not set")
	}
	if _, err = os.Stat(filepath.Join(statePath, "state.json")); err != nil {
		t.Fatalf("state was not persisted: %v", err)
	}
}

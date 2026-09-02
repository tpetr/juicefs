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

package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"time"
)

func main() {
	if len(os.Args) != 3 || os.Args[1] != "mark-success" {
		fmt.Fprintln(os.Stderr, "usage: workspace-csi-control mark-success /path/to/workspace.sock")
		os.Exit(2)
	}
	connection, err := net.DialTimeout("unix", os.Args[2], 5*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect to workspace control socket: %v\n", err)
		os.Exit(1)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err = connection.Write([]byte("MARK_SUCCESS\n")); err != nil {
		fmt.Fprintf(os.Stderr, "send success intent: %v\n", err)
		os.Exit(1)
	}
	response, err := bufio.NewReader(connection).ReadString('\n')
	if err != nil || response != "OK\n" {
		fmt.Fprintf(os.Stderr, "success intent rejected: %q (%v)\n", response, err)
		os.Exit(1)
	}
}

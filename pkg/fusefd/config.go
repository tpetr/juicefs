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

// Package fusefd bootstraps a FUSE connection mounted by an external driver.
package fusefd

import (
	"fmt"
	"os"
	"runtime"
)

const Env = "JFS_PREOPENED_FUSE_FD_COMM"

// Socket returns the opt-in bootstrap socket and rejects state-transfer settings.
func Socket() (string, error) {
	path := os.Getenv(Env)
	if path == "" {
		return "", nil
	}
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("%s is supported only on Linux", Env)
	}
	for _, name := range []string{"JFS_SUPER_COMM", "_FUSE_FD_COMM", "JFS_SUPERVISOR", "_FUSE_STATE_PATH"} {
		if os.Getenv(name) != "" {
			return "", fmt.Errorf("%s cannot be combined with %s", Env, name)
		}
	}
	return path, nil
}

//go:build windows

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
	"fmt"
	"time"
)

const ControlTimeout = 5 * time.Minute

type ControlServer struct{}

func ServeControl(path string, handler func(bool) error) (*ControlServer, error) {
	return nil, fmt.Errorf("%s is supported only on Linux", Env)
}

func RequestUnmount(path string, force bool, timeout time.Duration) error {
	return fmt.Errorf("%s is supported only on Linux", Env)
}

func (s *ControlServer) Close() error { return nil }

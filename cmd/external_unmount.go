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
	"errors"
	"sync"

	"github.com/juicedata/juicefs/pkg/fusefd"
)

type externalUnmount struct {
	mu        sync.Mutex
	flush     func() error
	unmount   func(bool) error
	server    *fusefd.ControlServer
	completed bool
	result    error
}

func newExternalUnmount(path string, flush func() error, unmount func(bool) error) (*externalUnmount, error) {
	u := &externalUnmount{flush: flush, unmount: unmount}
	server, err := fusefd.ServeControl(path, u.request)
	if err != nil {
		return nil, err
	}
	u.server = server
	return u, nil
}

func (u *externalUnmount) request(force bool) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.completed {
		return u.result
	}
	flushErr := u.flush()
	unmountErr := u.unmount(force)
	err := errors.Join(flushErr, unmountErr)
	// A successful proxy request will stop the FUSE serve loop. Preserve any
	// flush failure so the foreground mount process exits unsuccessfully.
	if unmountErr == nil {
		u.completed = true
		u.result = err
	}
	return err
}

func (u *externalUnmount) mountResult() (bool, error) {
	if u == nil {
		return false, nil
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.completed, u.result
}

func (u *externalUnmount) close() error {
	if u == nil {
		return nil
	}
	return u.server.Close()
}

type mountResources struct {
	once            sync.Once
	closeSession    func() error
	shutdownStorage func()
	err             error
}

func (r *mountResources) close() error {
	r.once.Do(func() {
		r.err = r.closeSession()
		r.shutdownStorage()
	})
	return r.err
}

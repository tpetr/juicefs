//go:build linux

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
	"net"
	"os"

	"golang.org/x/sys/unix"
)

func authorizeControl(conn *net.UnixConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return fmt.Errorf("access clean-unmount peer credentials: %w", err)
	}
	var credentials *unix.Ucred
	var credentialErr error
	if err := raw.Control(func(fd uintptr) {
		credentials, credentialErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return fmt.Errorf("access clean-unmount socket: %w", err)
	}
	if credentialErr != nil {
		return fmt.Errorf("read clean-unmount peer credentials: %w", credentialErr)
	}
	if credentials == nil {
		return fmt.Errorf("clean-unmount peer credentials are missing")
	}
	uid := uint32(os.Geteuid())
	if credentials.Uid != uid && credentials.Uid != 0 {
		return fmt.Errorf("clean-unmount peer uid %d does not own mount process uid %d", credentials.Uid, uid)
	}
	return nil
}

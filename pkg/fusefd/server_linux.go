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
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
	"golang.org/x/sys/unix"
)

// receive receives one SCM_RIGHTS message. Payload bytes carry no FUSE state.
// The caller owns the returned descriptor; all descriptors are closed on error.
func receive(path string, timeout time.Duration) (int, error) {
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return -1, fmt.Errorf("connect pre-opened FUSE socket %q: %w", path, err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return -1, fmt.Errorf("set pre-opened FUSE transfer deadline: %w", err)
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		return -1, fmt.Errorf("access pre-opened FUSE socket: %w", err)
	}
	payload := make([]byte, 1)
	// Allow two FDs so alignment padding cannot hide a second descriptor.
	oob := make([]byte, unix.CmsgSpace(2*4))
	var n, oobn, flags int
	var recvErr error
	err = raw.Read(func(fd uintptr) bool {
		n, oobn, flags, _, recvErr = unix.Recvmsg(int(fd), payload, oob, unix.MSG_CMSG_CLOEXEC)
		return recvErr != unix.EAGAIN && recvErr != unix.EWOULDBLOCK && recvErr != unix.EINTR
	})
	if err != nil {
		return -1, fmt.Errorf("read pre-opened FUSE socket: %w", err)
	}
	if recvErr != nil {
		return -1, fmt.Errorf("receive pre-opened FUSE FD: %w", recvErr)
	}
	msgs, parseErr := unix.ParseSocketControlMessage(oob[:oobn])
	var fds []int
	valid := false
	defer func() {
		if !valid {
			for _, fd := range fds {
				_ = unix.Close(fd)
			}
		}
	}()
	for _, msg := range msgs {
		if msg.Header.Level != unix.SOL_SOCKET || msg.Header.Type != unix.SCM_RIGHTS {
			parseErr = fmt.Errorf("unexpected control message level=%d type=%d", msg.Header.Level, msg.Header.Type)
			continue
		}
		rights, err := unix.ParseUnixRights(&msg)
		fds = append(fds, rights...)
		if err != nil {
			parseErr = err
		}
	}
	if flags&unix.MSG_CTRUNC != 0 {
		return -1, fmt.Errorf("pre-opened FUSE control data was truncated")
	}
	if parseErr != nil {
		return -1, fmt.Errorf("malformed pre-opened FUSE transfer: %w", parseErr)
	}
	if n == 0 && len(fds) == 0 {
		return -1, fmt.Errorf("pre-opened FUSE peer closed before sending a descriptor")
	}
	if len(fds) != 1 {
		return -1, fmt.Errorf("pre-opened FUSE transfer: expected exactly one FD, received %d", len(fds))
	}
	// The pinned go-fuse magic mountpoint parser requires a positive FD.
	if fds[0] == 0 {
		fd, err := unix.FcntlInt(uintptr(fds[0]), unix.F_DUPFD_CLOEXEC, 1)
		if err != nil {
			return -1, fmt.Errorf("duplicate pre-opened FUSE FD: %w", err)
		}
		_ = unix.Close(fds[0])
		fds[0] = fd
	}
	valid = true
	return fds[0], nil
}

// NewServer adopts an external mount and performs go-fuse's normal INIT exchange.
// On success Serve must be called: it owns and eventually closes the descriptor.
func NewServer(fs fuse.RawFileSystem, opts *fuse.MountOptions) (*fuse.Server, error) {
	path, err := Socket()
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, fmt.Errorf("%s is not set", Env)
	}
	fd, err := receive(path, 30*time.Second)
	if err != nil {
		return nil, err
	}
	var opt fuse.MountOptions
	if opts != nil {
		opt = *opts
	}
	opt.DirectMount = false
	opt.DirectMountStrict = false
	// This absolute magic mountpoint cannot fail before FD adoption. NewServer
	// closes fd on INIT failure; Serve closes it on success. Do not close twice.
	server, err := fuse.NewServer(fs, fmt.Sprintf("/dev/fd/%d", fd), &opt)
	if err != nil {
		return nil, fmt.Errorf("initialize pre-opened FUSE FD: %w", err)
	}
	return server, nil
}

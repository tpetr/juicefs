---
title: FUSE Mount Options
sidebar_position: 5
slug: /fuse_mount_options
---

JuiceFS provides several access methods, FUSE is the common one, which is the way to mount the file system locally using the `juicefs mount` command. Users can add FUSE mount options for more granular control.

This guide describes the common FUSE mount options for JuiceFS, with two ways to add mount options:

1. Run [`juicefs mount`](../reference/command_reference.mdx#mount), and use `-o` to specify multiple options separated by commas.

   ```bash
   juicefs mount -d -o allow_other,writeback_cache sqlite3://myjfs.db ~/jfs
   ```

2. When writing `/etc/fstab` items, add FUSE options directly to the `options` field, with multiple options separated by commas.

   ```
   # <file system>       <mount point>   <type>      <options>           <dump>  <pass>
   redis://localhost:6379/1    /jfs      juicefs     _netdev,writeback_cache   0       0
   ```

## default_permissions

This option is automatically enabled when JuiceFS is mounted and does not need to be explicitly specified. It will enable the kernel's file access checks, which are performed outside the filesystem. When enabled, both the kernel checks and the file system checks must succeed before further operations.

:::tip
The kernel performs standard Unix permission checks based on mode bits, UID/GID, and directory entry ownership.
:::

## allow_other

By default FUSE only allows access to the user mounting the file system. `allow_other` option overrides this behavior to allow access for other users. When mounting JuiceFS using root, `allow_other` is automatically assumed (search for `AllowOther` in [`fuse.go`](https://github.com/juicedata/juicefs/blob/main/pkg/fuse/fuse.go)). When mounting by non-root users, you'll need to first modify `/etc/fuse.conf` and enable `user_allow_other`, and then add `allow_other` to the mount command.

## writeback_cache

:::note
This mount option requires at least version 3.15 Linux kernel
:::

FUSE supports ["writeback-cache mode"](https://www.kernel.org/doc/Documentation/filesystems/fuse-io.txt), which means the `write()` syscall can often complete rapidly. It's recommended to enable this mount option when write small data (e.g. 100 bytes) frequently.

## user_id and group_id

These options are used to specify the owner ID and owner group ID of the file system (as distinct from the UID and GID of a file or directory) for higher-level permission validation. If the allow_other option is specified, this option will not work. e.g. `sudo juicefs mount -o user_id=100,group_id=100`.

## ReadDirPlusAuto {#readdirplusauto}

Starting from JuiceFS v1.4, `ReadDirPlusAuto` is automatically enabled. This feature allows the FUSE kernel module to automatically decide whether to use the `ReadDirPlus` operation (which returns directory entries along with file attributes) instead of plain `ReadDir`. This reduces the number of subsequent `getattr` calls when listing directories, significantly improving performance for large directory listings.

This is an internal optimization and does not require any user configuration.

## debug

This option will output Debug information from the low-level library (`go-fuse`) to `juicefs.log`.

:::note
This option will output debug information for the low-level library (`go-fuse`) to `juicefs.log`. Note that this option is different from the global `-debug` option for the JuiceFS client, where the former outputs debug information for the `go-fuse` library and the latter outputs debug information for the JuiceFS client. see the documentation [Fault Diagnosis and Analysis](../administration/fault_diagnosis_and_analysis.md).
:::

## External mounter / CSI bootstrap (Linux)

`JFS_PREOPENED_FUSE_FD_COMM=/absolute/path/to/socket` enables an integration mode for a privileged external mounter, such as a CSI driver. It is not intended for ordinary CLI mounts. The driver must mount a fresh `/dev/fuse` connection on the host and expose a Unix stream socket to the workload before starting JuiceFS:

```bash
JFS_PREOPENED_FUSE_FD_COMM=/run/csi/fuse.sock juicefs mount META-URL /mnt/jfs
```

After JuiceFS connects, the driver sends **exactly one FD** using `SCM_RIGHTS`, with at least one payload byte in the same `sendmsg`. Payload bytes have no meaning and are never decoded as FUSE initialization state. Extra descriptors, truncated control data, missing descriptors, connection errors, and a peer closing before transfer cause startup to fail. JuiceFS allows 30 seconds after connecting to receive the FD; a silent peer causes a descriptive timeout error. Restrict access to the socket to the intended workload.

The FD must belong to a newly mounted connection whose kernel `FUSE_INIT` request has not been consumed. JuiceFS adopts the FD, reads that request, sends go-fuse's normal `InitOut` reply, and then invokes filesystem initialization. The workload does not need to open `/dev/fuse` or invoke a mount helper. JuiceFS owns the received descriptor and closes it on initialization failure or when serving ends; the driver should close its own copy after transfer.

This mode uses three distinct endpoints. `JFS_PREOPENED_FUSE_FD_COMM` is the FD handoff socket. Its `<handoff>.control` sibling is reserved for the CSI driver's authenticated node-side unmount proxy. The foreground JuiceFS mount process creates its own private mode-`0600` clean-unmount socket at `<handoff>.juicefs-control` by default. Set `JFS_PREOPENED_FUSE_FD_CONTROL` to retain an explicit custom JuiceFS control path; it must be a distinct writable socket path and cannot equal either the handoff socket or the driver's `<handoff>.control` socket. The mount and `umount` processes derive the same default. `juicefs umount --flush MOUNTPOINT` sends its request to the JuiceFS control socket instead of reading `MOUNTPOINT/.config`. The mount process flushes pending data, invokes `fusermount -u MOUNTPOINT` (which may be the CSI driver's authenticated proxy), waits for the FUSE serve loop to stop, closes its metadata session and object storage, and exits. `SIGTERM` follows the same lifecycle. The JuiceFS control socket is removed on normal exit.

JuiceFS runs in the foreground without its restart supervisor, mountpoint preparation, or pre-INIT readiness probes. `--background` and `--update-fstab` are rejected. The mountpoint argument identifies the external mount; the driver owns mounting, readiness checks, unmounting, and restart policy. Kernel mount options (including `allow_other`, `default_permissions`, and `max_read`) must be configured by the driver consistently with the JuiceFS options. A process restart requires a fresh mounted connection; this mode cannot resume an initialized session. SIGTERM, SIGINT, and SIGHUP flush data and exit without a local unmount or smooth restart.

Do not combine this setting with `JFS_SUPER_COMM`, `_FUSE_FD_COMM`, `JFS_SUPERVISOR`, or `_FUSE_STATE_PATH`. Those settings belong to JuiceFS's separate smooth-upgrade/state-transfer lifecycle, which exchanges two FDs and serialized `InitIn` state.

No go-fuse dependency update or vendor patch is required: the pinned `github.com/juicedata/go-fuse/v2` replacement already supports `/dev/fd/N` mountpoints followed by its ordinary `handleInit` flow. Bootstrap disables `DirectMount` and `DirectMountStrict` before using that entry point, so no mount syscall precedes FD adoption. Normal mounts and smooth upgrades retain their existing paths when the setting is absent.

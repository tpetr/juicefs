---
title: FUSE 挂载选项
sidebar_position: 5
slug: /fuse_mount_options
---

JuiceFS 文件系统为用户提供多种访问方式，FUSE 是其中较为常用的一种，即使用 `juicefs mount` 命令将文件系统挂载到本地的方式。用户可以根据需要添加 FUSE 支持的挂载选项，从而实现更细粒度的控制。

本指南介绍 JuiceFS 常用的 FUSE 挂载选项，有两种添加挂载选项的方式：

1. 手动执行 [`juicefs mount`](../reference/command_reference.mdx#mount) 命令时，通过 `-o` 选项指定，多个选项使用半角逗号分隔。

   ```bash
   juicefs mount -d -o allow_other,writeback_cache sqlite3://myjfs.db ~/jfs
   ```

2. Linux 发行版通过 `/etc/fstab` 定义自动挂载时，在 `options` 字段处直接添加选项，多个选项使用半角逗号分隔。

   ```
   # <file system>       <mount point>   <type>      <options>           <dump>  <pass>
   redis://localhost:6379/1    /jfs      juicefs     _netdev,writeback_cache   0       0
   ```

## default_permissions

JuiceFS 在挂载时会自动启用该选项，无需显式指定。该选项将启用内核的文件访问权限检查，它会在文件系统之外进行，启用后，内核检查和文件系统检查必须全部成功才允许进一步操作，该选项通常与 `allow_other` 一起使用。

:::tip
内核执行的是标准的 Unix 权限检查，基于 mode bits、UID/GID、目录所有权。
:::

## allow_other

FUSE 默认只有挂载文件系统的用户可以访问挂载点中的文件，`allow_other` 选项可以让其他用户也可以访问挂载点上的文件。当 root 用户挂载时，该选项会自动启用（在 [`fuse.go`](https://github.com/juicedata/juicefs/blob/main/pkg/fuse/fuse.go) 搜索 `AllowOther` 字样），无需显式指定。而如果是普通用户挂载，则需要修改 `/etc/fuse.conf`，在该配置文件中开启 `user_allow_other` 配置选项，才能在普通用户挂载时启用 `allow_other`。

## writeback_cache

:::note 注意
该挂载选项仅在 Linux 3.15 及以上版本内核上支持
:::

FUSE 支持[「writeback-cache 模式」](https://www.kernel.org/doc/Documentation/filesystems/fuse-io.txt)，这意味着 `write()` 系统调用通常可以非常快速地完成。当频繁写入非常小的数据（如 100 字节左右）时，建议启用此挂载选项。

## user_id 和 group_id

这两个选项用来指定文件系统的所有者 ID 和所有者组 ID（不同于文件或目录的 UID、GID），用以做更高层级的权限校验。如果指定了 allow_other 选项，此选项将失效。用法如 `sudo juicefs mount -o user_id=100,group_id=100`。

## ReadDirPlusAuto {#readdirplusauto}

从 JuiceFS v1.4 开始，`ReadDirPlusAuto` 已自动启用。该特性允许 FUSE 内核模块自动决定是否使用 `ReadDirPlus` 操作（在返回目录条目的同时返回文件属性），而非仅使用普通的 `ReadDir`。这减少了目录列表时后续的 `getattr` 调用次数，显著提升了大目录的列举性能。

这是一项内部优化，用户无需进行任何配置。

## debug

该选项会将低层类库（`go-fuse`）的 Debug 信息输出到 `juicefs.log` 中。

:::note 注意
该选项会将低层类库（`go-fuse`）的 Debug 信息输出到 `juicefs.log` 中，需要注意的是，该选项与 JuiceFS 客户端的全局 `--debug` 选项不同，前者是输出 `go-fuse` 类库的调试信息，后者是输出 JuiceFS 客户端的调试信息。详情参考文档[故障诊断和分析](../administration/fault_diagnosis_and_analysis.md)。
:::

## 外部挂载器 / CSI 引导模式（Linux）

`JFS_PREOPENED_FUSE_FD_COMM=/absolute/path/to/socket` 用于特权外部挂载器（例如 CSI 驱动）集成，不用于普通 CLI 挂载。驱动必须先在宿主机挂载一个全新的 `/dev/fuse` 连接，并向工作负载提供 Unix 流套接字：

```bash
JFS_PREOPENED_FUSE_FD_COMM=/run/csi/fuse.sock juicefs mount META-URL /mnt/jfs
```

JuiceFS 连接后，驱动通过一次 `sendmsg` 使用 `SCM_RIGHTS` 发送恰好一个 FD，同时发送至少一个载荷字节。载荷内容不作为 FUSE 初始化状态解析。多余或缺失的 FD、控制数据截断、连接失败以及发送前对端关闭都会导致启动失败。JuiceFS 在连接后最多等待 30 秒接收 FD，超时会返回明确的错误。套接字访问权限应限制为目标工作负载。

FD 必须对应尚未读取内核 `FUSE_INIT` 请求的新挂载连接。JuiceFS 接管 FD 后读取内核请求，发送 go-fuse 正常的 `InitOut` 回复，再调用文件系统初始化。工作负载无需自行打开 `/dev/fuse` 或调用挂载辅助程序。JuiceFS 在初始化失败或服务结束时关闭收到的 FD；驱动应在发送后关闭自己的副本。

此模式使用三个不同的端点。`JFS_PREOPENED_FUSE_FD_COMM` 是 FD 交接套接字；其同级路径 `<handoff>.control` 保留给 CSI 驱动的、经过认证的节点侧卸载代理。前台 JuiceFS 挂载进程默认在 `<handoff>.juicefs-control` 创建权限为 `0600` 的私有清理卸载套接字。仍可通过 `JFS_PREOPENED_FUSE_FD_CONTROL` 显式指定自定义 JuiceFS 控制路径；该路径必须是不同且可写的套接字路径，不能等于 FD 交接套接字或驱动的 `<handoff>.control` 套接字。挂载进程和 `umount` 进程会推导出同一个默认路径。`juicefs umount --flush MOUNTPOINT` 会向 JuiceFS 控制套接字发送请求，而不读取 `MOUNTPOINT/.config`。挂载进程会刷新待处理数据，调用 `fusermount -u MOUNTPOINT`（该程序可以是 CSI 驱动提供的认证代理），等待 FUSE 服务循环停止，然后关闭元数据会话和对象存储并退出。`SIGTERM` 使用相同的清理流程。正常退出时会删除 JuiceFS 控制套接字。

对于使用 **SQLite 元数据** 的 restartable-init sidecar，可将 `JFS_PREOPENED_FUSE_FD_CHECKPOINT_DIR` 设置为工作负载拥有的绝对共享可写目录。SDK 随后可执行 `juicefs checkpoint /absolute/checkpoint-dir/metadata.bin`。该命令使用 `JFS_PREOPENED_FUSE_FD_CONTROL`，不会读取 `MOUNTPOINT/.config`，且只接受配置目录中直接包含的目标文件。前台进程会将请求与卸载和关闭串行化，阻塞本地修改，刷新数据和 writeback 暂存文件，将单线程 SQLite 二进制快照写入临时文件、fsync 后原子发布。成功的 checkpoint 不会调用 `fusermount`、卸载文件系统、关闭元数据会话或对象存储、也不会停止前台进程。可重复执行 checkpoint，之后仍可正常清理卸载。其他元数据引擎会拒绝在线 checkpoint，因为它们不能提供所需的一致实时二进制快照。

此模式直接以前台服务运行，跳过重启监督进程、挂载点准备及 INIT 前的就绪检查，不允许 `--background` 和 `--update-fstab`。挂载点参数标识外部挂载，驱动负责挂载、就绪检查、卸载和重启策略。驱动设置的内核挂载选项（如 `allow_other`、`default_permissions` 和 `max_read`）必须与 JuiceFS 配置一致。进程重启需要新的挂载连接，不能恢复已初始化的会话。SIGTERM、SIGINT 和 SIGHUP 会刷写数据并退出，不执行本地卸载或平滑重启。

不能与 `JFS_SUPER_COMM`、`_FUSE_FD_COMM`、`JFS_SUPERVISOR` 或 `_FUSE_STATE_PATH` 同时使用。这些设置属于独立的平滑升级/状态传递流程，该流程交换两个 FD 及序列化的 `InitIn` 状态。

无需修改 go-fuse 依赖：当前固定版本的 `github.com/juicedata/go-fuse/v2` 已支持 `/dev/fd/N` 挂载点及后续正常的 `handleInit` 流程。此模式先关闭 `DirectMount` 和 `DirectMountStrict`，避免接管 FD 前执行挂载调用。未设置该环境变量时，普通挂载和平滑升级行为保持不变。

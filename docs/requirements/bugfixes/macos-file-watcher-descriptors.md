# Bug: macOS 文件监听耗尽文件描述符

## 现象

大型 Java / Kotlin 工作区使用一段时间后，导航失败并报告 `too many open files`。旧实现的 fsnotify kqueue 后端会为监听目录内的文件持有句柄，`relevantFile` 过滤无法阻止内核层面的分配。

## 根因

- macOS kqueue 需要为每个被监听目录内的文件打开描述符；逐目录监听会把整个源码树和生成的构建树都计入。
- 参考实现的旧关闭路径也不能可靠释放 kqueue 句柄，失败后重新初始化会保留旧句柄。
- 仅把目录替换为 FSEvents 而继续逐包设置 `WatchRoot` 仍会为每个监听根及祖先持有句柄。

## 修复

- `watch.go` 保留工作区、构建配置和动态 LSP watcher 注册语义，注册失败立即传播。
- `watch_darwin.go` 使用一个 FSEvents 流；原生路径合并为工作区根和必要的注册根，包级递归 / 非递归范围与忽略目录仍独立过滤，扩大原生根不扩大通知语义。
- 根配置批量应用，使用基线扫描前捕获的事件 ID 回放注册窗口内的事件；查询前使用同一流中的唯一 marker、`FSEventStreamFlushSync` 和串行队列屏障同步，不轮询或重新扫描整个工作区。
- 事件丢失、历史不完整或监听根失效会使会话失败，后续请求重建；不返回可能过期的导航结果。
- Linux 等平台继续使用 `watch_fsnotify.go`；所有平台共享 Java / Kotlin 文件过滤与构建变更检测。
- 原生回调只持有整数 token；关闭按 stop、invalidate、回调屏障、release 和队列回收顺序执行，并删除私有控制目录。

## 验证

- `go test -race ./...` 通过，包含新增文件、删除、构建配置变化、目录移入和原子保存回归。
- `watch_darwin_test.go` 在含大量无关文件的目录上比较监听前后的描述符增量，FSEvents 后端必须保持有界；旧 kqueue 路径会按文件增长。
- 真实 JDT LS / Kotlin Maven 与 Gradle 冒烟脚本在 macOS arm64 通过。

## 升级与边界

需要重新构建后端并 `tern plugin reload`；旧进程已经泄漏的句柄只能通过退出进程释放，升级后重启 Tern 一次。不要求提高系统 `ulimit`，也不会终止其他工具使用的语言服务。事件丢失时选择失败并重建会话，而不是带病返回旧结果。

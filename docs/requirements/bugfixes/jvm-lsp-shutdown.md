# Bug: 关闭会话直接杀 JVM 语言服务

## 现象与风险

修改前的 `session.closeServer` 先向进程组发送 `SIGKILL`，再关闭 stdin。JDT LS / Kotlin LSP 没有机会完成 LSP `shutdown`、刷新索引和释放工作区资源；若未来接入共享服务，这种关闭方式还可能影响其他客户端。

## 根因

生命周期沿用早期参考实现：查询超时或守护进程关闭时直接 kill。它满足单客户端可用性，但不符合 LSP 的关闭顺序，也没有超时后“只结束自己的进程组”的边界。

## 修复

- 关闭时先在有 1 秒超时的上下文内发送 `shutdown` 请求；收到响应后发送 `exit` 通知。
- 超时或请求失败才杀死本会话的进程组并关闭 stdin。
- 关闭 stdin 后等待 `cmd.Wait()` 和 RPC reader 退出，确保不遗留 goroutine 或管道。
- 只作用于当前插件启动的进程组，不扫描、不终止其他 JDT / Kotlin 服务。

## 验证

- `selection_test.go` 的 `TestJavaSessionShutsDownBeforeClosingTransport` 使用真实管道和子进程，断言服务依次收到 `shutdown`、`exit`，关闭在 2 秒内完成，且会话不保留旧传输。
- `go test -race ./...` 通过。
- 真实 JDT LS / Kotlin 冒烟在 `--shutdown` 路径通过，后续请求重新初始化的行为不变。

## 边界

共享服务（例如 OMP 自己启动的 JDT LS / Kotlin LSP）仍是独立进程；本修复不会把编辑器的会话并入它们，也不会在关闭时影响它们。要共享索引还需要语言服务自身支持多客户端和独立工作区视图，不能仅靠关闭顺序实现。

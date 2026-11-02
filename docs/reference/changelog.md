# Changelog

## 0.1.1 — 2026-11-02

- 对齐 Go Navigation 1.3.3：导航请求传递正向 / 反向原生 UTF-8 选区，按选区起点转换为 LSP UTF-16 位置。双击或选中 Java / Kotlin 标识符后，caret 位于选区排他末尾不再导致 `no identifier found`；折叠选区仍使用 caret，非法码点边界明确报错。
- 对齐 Go Navigation 1.3.3 的 macOS 监听修复：Java / Kotlin watcher 在 macOS 改用单一 FSEvents 原生流，不再由 fsnotify / kqueue 为每个目录和文件持有句柄；监听根合并、日志游标回放、marker 往返 + flush / dispatch 屏障和关闭顺序均已移植，事件历史丢失或监听根失效会使会话失败而不是返回过期结果。
- Linux 等非 macOS 平台保留 fsnotify fallback，继续按目录递归监听并保留 marker 屏障；所有平台共享 Java / Kotlin 源文件、构建文件和动态 LSP watcher 注册语义。
- JVM 语言服务关闭改为 LSP `shutdown` / `exit` 优先，1 秒超时后才强杀本会话的进程组；优雅释放索引和资源，不影响其他客户端。
- 发布包许可证改为读取实际二进制的依赖构建信息校验，适配 macOS 使用 purego、Linux 使用 fsnotify 的平台差异。
- 已验证：`go test -race ./...`、真实 JDT LS Java / JDK 源码导航、选中标识符与未保存 overlay、Kotlin Maven / Gradle 混合导航、四个 Unix target 的归档与 SHA-256。macOS 本机测试需完整 Xcode SDK；CommandLineTools 的损坏 SDK 会导致 cgo / race 链接失败，与插件代码无关。

## 0.1.0 — 2026-10-30

- 基于 Go Navigation 的原生界面和 Go 守护进程协议实现 Java / Kotlin 导航：定义与引用合并、源码预览、未保存缓冲区、跳转历史和过期结果保护。
- Java 使用 Eclipse JDT LS 1.63.0+ / JDK 25+，Kotlin 使用 JetBrains 官方 Kotlin LSP；通过 `JDTLS_JAVA_HOME` 将服务运行时与项目 `JAVA_HOME` 分离，等待 Maven / Gradle 项目导入后查询，按窗口与工作区复用会话。
- 项目搜索通过源文件语义大纲覆盖方法、字段与 Kotlin 函数 / 属性；按文件失效缓存，混合项目合并 Java / Kotlin 结果，排除依赖缓存、输出目录与符号链接逃逸。
- Ctrl+Alt+G / S / B 与 Go 版一致；提供共享 `navigation.luau`，更新后的 Go 插件可在同一个 VM 中承载 Go / Java / Kotlin 界面，避免重复快捷键和分裂的跳转历史，不改全局键位设置。
- 支持 UTF-16 / 显示列转换、Location / LocationLink、JDT 依赖源码与本地 jar 源码条目。
- Java 引用采用 `projectOnly`，保留依赖定义但排除整个 JDK 的内部引用扫描；真实 `String` 导航回归覆盖巨量返回与旧 JDT 的标准库搜索失败，并兼容新服务返回 `.java` 后缀的 `jdt:` URI。
- 修复继承自参考实现的断连监听边界：JSON 解码器可能将末尾换行留在 socket 中，读取到该换行不应取消请求；现在只在真正断连时取消，并保留实际 socket 行为回归。
- 明确上游边界：Kotlin 需要 JVM Maven / Gradle 项目；已验证 Kotlin→Java 与 Java 中的 Kotlin 引用，但不宣称 Java→Kotlin 完整双向互通。
- 增加四个 Unix target 的可复现发布包、SHA-256、第三方许可证、CI 和真实 JDT / Kotlin Maven / Gradle 冒烟脚本。
- 本机验证：Go 竞态测试、真实 Java 导航与 dirty overlays、Kotlin Maven / Gradle 导航、混合符号查询；原生 Tern 窗口验证 Go / Java / Kotlin 同一组 G / S / B 快捷键及共享返回历史。各平台 CI 的运行结果以实际流水线为准。

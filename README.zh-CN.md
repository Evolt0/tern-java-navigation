# Java / Kotlin Navigation — Tern 插件

[English](README.md) | **简体中文**

参考 `tern-go-navigation`，在 Tern 原生编辑器中查看定义与引用、按名称搜索当前项目的符号，预览源码后跳转。支持 `.java`、`.kt`、`.kts` 的编辑器缓冲区，不隐式保存源码。

## 功能

- 定义与调用 / 引用合并展示、位置去重；单个导航结果直接跳转。
- 搜索项目内的类、方法、构造器、字段和 Kotlin 函数 / 属性，包括未打开的文件。
- 结果显示文件、行列和源码预览；预览优先使用未保存内容。
- 当前窗口的跳转历史；编辑器位置或查询时捕获的缓冲区发生变化后拒绝过期跳转。
- 后台预热，按窗口、项目和运行环境复用语言服务；Java 与 Kotlin 按需分别启动。
- Maven reactor、Gradle settings 根目录识别；UTF-16 与 Tern 显示列转换，覆盖 Tab、中文、emoji、组合字符。

## 技术选择

桥接后端使用 Go，语义服务分别采用 [Eclipse JDT LS](https://github.com/eclipse-jdtls/eclipse.jdt.ls) 和 [JetBrains Kotlin LSP](https://github.com/Kotlin/kotlin-lsp)。这也是 omp 内置配置列出的 Java / Kotlin 服务组合，而不是自制解析器。

Go 负责短生命周期 CLI、Unix socket、文件监听和 LSP 通信；不在每次查询时启动额外 JVM。性能策略是复用服务和索引、按文件失效符号缓存，不声称某种桥接语言在所有项目上“性能最优”。混合项目只有使用两种语言的导航或合并符号搜索时，才需要两个语义服务。

## 环境

- Tern，终端可以运行 `tern`。桌面集成使用原生文件编辑器和插件 API。
- Java：**JDT LS 1.63.0+，语言服务器运行在 Java 25+**。服务器 JDK 与项目构建 JDK 分离：`JDTLS_JAVA_HOME` 指定服务器，`JAVA_HOME` 保留项目所需版本。
- Kotlin：**JetBrains 官方 Kotlin LSP 263.7084.0**，不是旧的 `fwcd/kotlin-language-server`。官方分平台发行包自带所需的 JBR；项目编译所需的 JDK 仍独立安装。
- 从源码构建后端需要 Go 1.23 或更新版本。
- 打包与发布冒烟脚本需要 Python 3.9+；普通插件运行不需要 Python，使用上游 `jdtls` Python 启动器时除外。
- macOS / Linux，arm64 / amd64。当前使用 Unix socket 与 Unix 进程信号，不支持 Windows。

Kotlin LSP 当前为 Alpha。本插件支持其 **JVM Maven / Gradle 项目**；没有构建文件的独立 Kotlin 文件会明确报错。`.kts` 依赖语言服务对所属项目和脚本类型的支持；不等于承诺任意独立脚本或 Kotlin Multiplatform。

### 安装语言服务

macOS 可使用 Homebrew：

```sh
brew install openjdk@25 jdtls
brew install JetBrains/utils/kotlin-lsp
export JDTLS_JAVA_HOME="$(brew --prefix openjdk@25)/libexec/openjdk.jdk/Contents/Home"
```

手动安装、Linux 或需要固定版本时：

- [JDT LS 1.63.0 下载目录](https://download.eclipse.org/jdtls/milestones/1.63.0/)：下载归档并验证对应 SHA-256，解压后设置 `JDTLS_HOME`。该发行版实际的 Eclipse bundles 要求 Java 25，不能沿用过时上游 README 的 Java 21 要求。
- [Kotlin LSP 263.7084.0 下载页](https://github.com/Kotlin/kotlin-lsp/releases/tag/kotlin-lsp%2Fv263.7084.0)：选择系统 / 架构对应的 standalone 包，验证对应 SHA-256 后解压，设置 `KOTLIN_LSP_HOME` 为包含 `bin/intellij-server` 的目录。

这些变量必须由 **Tern 进程继承**；只在另一个已打开的终端设置，不会改变正在运行的 Tern 的环境。

JDT 运行时依次查找 `JDTLS_JAVA_HOME` → `JAVA_HOME` → PATH / 常见 JDK 安装位置。指定服务器 JDK 不会覆盖子进程环境中的项目 `JAVA_HOME`。冒烟项目另用 JDK 21 构建；普通项目保留自己的 toolchain 即可。

查找顺序：

| 服务 | 顺序 |
| --- | --- |
| Java | `JDTLS` 可执行路径 → `JDTLS_HOME` 发行目录 → PATH 的 `jdtls` → Homebrew 常见位置 |
| Kotlin | `KOTLIN_LSP` 可执行路径 → `KOTLIN_LSP_HOME` 发行目录 → PATH 的 `kotlin-lsp` / `intellij-server` → Homebrew 常见位置 |

`JDTLS` / `KOTLIN_LSP` 是单个可执行路径，不是附带参数的 shell 命令。Kotlin 使用 `--stdio` 和独立的 `--system-path`。插件不替用户接受 EULA，不发送额外遥测；已验证的 Kotlin 发行包默认 telemetry 为 none。

## 构建与安装

在本仓库目录执行：

```sh
go build -o bin/tern-java-navigation .
tern plugin link "$PWD"
tern plugin list
```

插件 ID 为 `java-navigation`，应显示 `ready`。链接安装后不要移动或删除源码目录。不会在首次查询时自动编译后端。

更新源码后：

```sh
go build -o bin/tern-java-navigation .
tern plugin reload java-navigation
```

卸载链接：`tern plugin unlink java-navigation`。复制安装则用 `tern plugin remove java-navigation`。已有复制安装要先移除再链接；已有链接安装要先解除链接再复制安装。

## 快捷键

与 Go 版保持一致，macOS 的 Alt 对应 Option：

| 快捷键 | 操作 | 命令 |
| --- | --- | --- |
| Ctrl+Alt+G | 定义与调用 / 引用 | `Java / Kotlin: Navigate symbol` |
| Ctrl+Alt+S | 搜索项目符号 | `Java / Kotlin: Search project symbols` |
| Ctrl+Alt+B | 返回 | `Java / Kotlin: Jump back` |
| ↑ / ↓、Enter、Esc | 选择、跳转、关闭 | 原生选择器 |

单独安装时，本插件注册上述快捷键。同时安装更新后的 Go 插件时，Go 的 `jvm-navigation-host.luau` 加载本插件的 `navigation.luau`，由**同一个窗口前端**处理 Go / Java / Kotlin 的三个键、选择器与跳转历史；Java 插件不再重复注册界面。Go 仍调用原 Go 后端，Java / Kotlin 调用本仓库后端，源码 overlays 不跨语言组混用。全局键位设置不会被插件改写。

双击或选中标识符也可以导航：请求携带原生零基 UTF-8 字节选区，正向和反向选择都从较小端点开始查询；没有非空选区时使用 caret。因此选中名字后 caret 停在排他末尾不会触发 `no identifier found`，但单独把光标放在标点上仍不会被悄悄改成邻近符号。光标、选区或捕获的缓冲区变化都会阻止过期结果跳转。

双插件共存需要 Go 包包含 `jvm-navigation-host.luau`；只有旧 Go 版时请一起更新，否则 Tern 的同键默认绑定仍会冲突。安装、更新或移除任一插件后运行 `tern plugin reload`，让共同前端重新选择宿主。

单独安装的动作名为 `plugin.java-navigation.navigate`、`plugin.java-navigation.symbols`、`plugin.java-navigation.back`；共存模式沿用 `plugin.go-navigation.navigate`、`plugin.go-navigation.symbols`、`plugin.go-navigation.back`，命令面板分组显示 `Go / Java / Kotlin Navigation`。用户自己的键位覆盖仍优先于默认绑定。

## 搜索、缓存与范围

搜索框按 Enter 后提交查询，不是逐键执行 LSP。结果框可以继续过滤。匹配名称按精确、前缀、子串、子序列排序，不搜索注释或任意函数体文本。

JDT 的 `workspace/symbol` 仅覆盖类型，因此本插件使用每个源文件的 `documentSymbol` 获取方法 / 字段等声明，并缓存大纲。第一次项目搜索需要遍历项目源文件并建立大纲；后续只重取失效的文件。混合项目合并两个服务的结果；任一所需服务缺失或失败时明确报错，不返回看似完整的部分结果。

源文件列表由文件监听维护：macOS 使用单一原生 FSEvents 流，不再为每个目录 / 文件持有 kqueue 句柄；Linux 使用递归 fsnotify。两者在查询前都等待磁盘事件 flush，并在事件历史不完整时使会话失败，而不是返回按过期文件计算的结果。保存变化、创建 / 删除和缓冲区变化会失效对应索引；构建配置变化清空符号缓存。输出目录 `target`、`build`、`out`、`bin`，依赖缓存和符号链接逃逸位置不参与项目搜索。当前根目录之外的 Maven 模块或 Gradle composite build 不在插件的搜索 / overlay 范围内。

服务状态位于 `/tmp/tern-java-<uid>/<identity>/`，目录权限 0700、socket 0600。不同窗口、项目、后端二进制与工具环境相互隔离；状态不会写入插件目录。编辑器存活期间保留服务，独立 CLI 空闲 5 分钟退出；二进制更新后旧服务回收。临时目录可能被系统清理，不保证跨重启保留全部语言索引。

## 跨语言与当前边界

- **Java 文件使用 JDT LS；Kotlin 文件使用 Kotlin LSP。** 已验证 Kotlin→Java 定义、Kotlin 符号在 Java 中的引用，以及混合项目的合并符号搜索。
- **不承诺 Java→Kotlin 的完整源代码跳转或从 Java 发起的完整 Kotlin 引用查询。** JDT LS 不解析 Kotlin，官方 Kotlin 包的 Java provider 也不是完整的 Java 服务；不会用同名文本匹配伪造结果。
- 引用范围由服务决定，Kotlin 服务返回的 Java 引用可能定位到调用表达式起点，而不只是方法名。
- Java 引用使用 JDT 的 `projectOnly` 范围，包含项目源码、测试与关联项目，不扫描 JDK / 依赖内部的使用点；定义仍可跳到库源码。查询 `String` 等常用类型不会拉回整个 JDK 的巨量内部引用，此范围要求 JDT LS 1.63+。
- JDT `jdt:` 提供的源码（附加源码或服务端反编译结果）和本地 `jar:` 源码条目会原样写入私有缓存供预览 / 跳转。服务未提供源码，或本地归档只有 `.class` 条目时明确报错，不伪造反编译内容。缓存副本不是项目文件，不应编辑。
- 仅支持本地文件。没有普通符号 Command+点击，也没有独立预览面板。
- 未保存的 Java / Kotlin 源码参与查询；`pom.xml`、Gradle 配置等必须保存后交给构建导入。语言服务可能生成 Eclipse / 构建工具元数据，项目导入也可能执行构建插件；只打开可信项目。
- 后端请求上限 180 秒，编辑器进程调用上限 185 秒。首次下载依赖、导入和大项目索引可能较慢。关闭会话先发送 LSP `shutdown` / `exit`，等待最多 1 秒后才强杀本会话的进程组，便于释放索引与缓存。错误或取消会关闭相应守护进程中的会话，下次查询重新初始化。

## 排障

- Java 导航为空：先确认 Maven / Gradle 项目能解析依赖。Maven 会缓存下载失败；网络恢复后可以在项目中运行 `mvn -U compile`，然后重启服务 / Tern。JDT 的详细日志位于状态目录 `data/.metadata/.log`。
- Kotlin 导入失败：错误中包含服务日志位置；查看 `kotlin/system/log/intellij-server.log`。`noBuildSystemFound` 表示当前根目录不是受支持的构建项目。
- Gradle 找不到指定 JDK：Kotlin 服务自身的 JBR 不等于项目 toolchain。安装项目要求的 JDK；非标准目录可通过项目的 `org.gradle.java.installations.paths` 配置。
- CLI 正常但 Tern 不工作：检查 GUI 进程继承的 PATH、`JDTLS_JAVA_HOME`、`JAVA_HOME` 和语言服务变量，重新构建后端并 reload 插件。
- 选中名字后报 `no identifier found`：同时更新 `window.luau` / `navigation.luau` 和后端二进制，再 reload 插件；没有选区时把光标放在标点上仍属无效位置。
- macOS 报 `too many open files`：升级到 0.1.1 及以后，重新构建 helper、reload 插件并重启 Tern 一次，释放旧进程持有的句柄。当前 macOS 监听使用单一 FSEvents 流，不需要提高 `ulimit`。

## 开发与发布验证

```sh
go test -race ./...
go build -o bin/tern-java-navigation .
python3 scripts/package.py --check-tag v0.1.1
python3 scripts/package.py --output dist
python3 scripts/smoke_release.py dist/java-navigation-0.1.1-darwin-arm64.tar.gz
python3 scripts/smoke_kotlin.py --binary bin/tern-java-navigation --build-tool all
```

替换归档中的 target 为当前平台。冒烟需要 JDT LS 1.63.0+、由 `JDTLS_JAVA_HOME` 指定的 JDK 25+、Kotlin LSP，以及 `JAVA_HOME` 指定的项目 JDK 21；Gradle 用于创建 wrapper。项目固定 Kotlin 2.2.21 / Gradle 8.14.3，不预编译源码；Java 冒烟另验证 `java.lang.String` 定义源码、仅限项目的引用结果，以及正向 / 反向原生选区。

macOS 上 `-race` 和 cgo 构建使用系统链接器。如果 CommandLineTools SDK 报 `tapi error: malformed file`，把 `SDKROOT` 指向完整 Xcode SDK（`/Applications/Xcode.app/Contents/Developer/Platforms/MacOSX.platform/Developer/SDKs/MacOSX.sdk`）；发布构建设置 `CGO_ENABLED=0`，不需要它。

打包支持四个 Unix target，附带 SHA-256、第三方许可证与中英文文档，不捆绑 JDK 或语言服务器。安装归档前先校验 `.sha256`，再解压并运行 `tern plugin install ./java-navigation`。CI 在各目标原生 runner 上执行测试、打包与真实语言服务器冒烟；tag `vX.Y.Z` 通过后发布附件，手动不带 tag 的执行只产生构建产物。本仓库提供发布流程，不代表远程 Release 已存在。

[变更记录](docs/reference/changelog.md)

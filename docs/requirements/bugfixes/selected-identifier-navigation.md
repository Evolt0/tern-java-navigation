# Bug: 选中 Java / Kotlin 标识符后引用查询找不到标识符

## 现象

在 Tern 原生编辑器中双击或选中类名、方法名后执行导航，可能返回 `textDocument/references: no identifier found`，即使光标看起来就在名字上。

## 根因与证据

- Tern 的 `caret` 位于选区 head。正向选中完整标识符后，head 是名字的排他末尾，通常正好落在后面的 `(`、`.` 或空格上。
- 修改前的 `navigation.luau` 只发送 caret 行列，后端把它直接用于 `textDocument/definition` 和 `references`。JDT LS / Kotlin LSP 的引用查询要求位置命中标识符，位置落在标点时整个组合查询失败。
- 原生选区是零基 UTF-8 字节偏移；caret 是显示列，二者不能混用。上游 Go Navigation 1.3.3 用真实编辑器快照确认了同一行为，并提供了选区协议。

## 修复

- 请求新增可选 `selection: {anchor, head}`，直接传递原生零基 UTF-8 字节范围。
- 非空选区采用较小端点作为查询起点，正选与反选一致；折叠选区仍使用原有 caret 显示列转换。
- 后端在请求携带的当前缓冲区上计算零基 LSP 行号和 UTF-16 列，不读取磁盘副本；端点必须在文档内且位于 UTF-8 码点边界，否则返回明确错误。
- 窗口在异步读取缓冲区前、返回结果后和实际跳转前比较 selection anchor/head，防止选区改变但 caret 行列不变时应用过期结果。
- 不把任意标点查询悄悄改成邻近符号查询；错误仍原样上报。

## 验证

- `selection_test.go` 覆盖正向 / 反向选择、折叠选区、CRLF、中文、emoji、组合字符和非法 UTF-8 端点。
- `go test -race ./...` 通过。
- 真实 JDT LS 发布包冒烟新增正向 / 反向 selection 查询，结果与 caret 查询一致；Kotlin 冒烟继续覆盖未保存 overlay。

## 升级

必须同时更新后端二进制和 `window.luau` / `navigation.luau`，然后 `tern plugin reload`。只更新一端时，旧前端仍不会发送选区。

# Java / Kotlin Navigation — a Tern plugin

**English** | [简体中文](README.zh-CN.md)

Native definition/reference navigation and project symbol search for `.java`, `.kt` and `.kts` files. Based on `tern-go-navigation`: source-line previews, unsaved editor buffers, background warmup, persistent language servers and per-window jump history. Source files are never implicitly saved.

## Backend choice

The bridge is Go; semantic analysis is provided by [Eclipse JDT LS](https://github.com/eclipse-jdtls/eclipse.jdt.ls) for Java and the [official JetBrains Kotlin LSP](https://github.com/Kotlin/kotlin-lsp) for Kotlin. OMP lists the same two language servers in its built-in configuration.

Go retains the reference implementation's inexpensive CLI startup, Unix socket daemon and filesystem watcher. The important optimizations are language-server reuse, incremental buffer synchronization and per-file outline invalidation—not an unmeasured claim that one bridge language is universally fastest. Java and Kotlin servers start independently, on demand; mixed-language project search merges both.

## Requirements

- Tern with the `tern` CLI available.
- Java: **JDT LS 1.61+**. JDT LS 1.61 runs on Java 21+; 1.62 and 1.63 require Java 25. Project source level can be Java 8–25 regardless of the server runtime. The project build JDK is independent: use `JDTLS_JAVA_HOME` for the server and keep `JAVA_HOME` for your project. Gradle imports run on `JAVA_HOME`; Gradle 8.x cannot run on Java 25, so point `JAVA_HOME` at a supported JDK (for example 21) or use Gradle 9.
- Kotlin: **JetBrains Kotlin LSP 263.7084.0**, not `fwcd/kotlin-language-server`. Platform-specific standalone distributions include their required JBR; the project's own build JDK is separate.
- Go 1.23+ to build this helper from source.
- Python 3.9+ for packaging and release smoke scripts. The plugin does not require Python when using `JDTLS_HOME`; upstream's optional `jdtls` launcher itself uses Python.
- macOS or Linux, arm64 or amd64. Unix sockets and process signals currently exclude Windows.

Kotlin LSP remains Alpha. Supported Kotlin project scope is **JVM Maven/Gradle**. Standalone Kotlin files without a build project receive an explicit error. Script support depends on the server and project; this is not a promise of arbitrary standalone `.kts` or Kotlin Multiplatform support.

### Install language servers

On macOS:

```sh
brew install jdtls
brew install JetBrains/utils/kotlin-lsp
```

Homebrew's `jdtls` is 1.63 and installs a compatible OpenJDK automatically. On a machine with only Java 21, use [JDT LS 1.61.0](https://download.eclipse.org/jdtls/milestones/1.61.0/) instead; it supports `java.search.scope` and runs on 21.

For Linux or pinned installations, download and verify the adjacent SHA-256 checksums before extracting:

- [JDT LS 1.63.0 distribution](https://download.eclipse.org/jdtls/milestones/1.63.0/) (Java 25+), or [JDT LS 1.61.0](https://download.eclipse.org/jdtls/milestones/1.61.0/) (Java 21+). Set `JDTLS_HOME` to the extracted root. Upstream READMEs for 1.62/1.63 still say Java 21, but their Eclipse bundles require 25.
- [Kotlin LSP 263.7084.0 standalone distributions](https://github.com/Kotlin/kotlin-lsp/releases/tag/kotlin-lsp%2Fv263.7084.0). Set `KOTLIN_LSP_HOME` to the directory containing `bin/intellij-server`.

Environment variables must be inherited by the **Tern process**, not merely set in a separate shell after Tern has started.

Runtime selection is `JDTLS_JAVA_HOME` → the newest compatible Java found via `JAVA_HOME`, PATH and common Homebrew/JDK locations. The newest runtime is preferred so JDT LS 1.63 gets a Java 25+ JVM; explicit `JDTLS_JAVA_HOME` must satisfy the server's requirement. Setting the server override does not replace the project JDK in the child environment. The smoke fixtures use a separate JDK 21 for Maven/Gradle builds.

| Server | Discovery order |
| --- | --- |
| Java | `JDTLS` executable → `JDTLS_HOME` distribution → `jdtls` on PATH → common Homebrew locations |
| Kotlin | `KOTLIN_LSP` executable → `KOTLIN_LSP_HOME` distribution → `kotlin-lsp` / `intellij-server` on PATH → common Homebrew locations |

Executable overrides are paths, not shell command strings with arguments. Kotlin launches with `--stdio` and an isolated `--system-path`. The plugin does not accept EULAs on your behalf or enable additional telemetry; the verified Kotlin distribution defaults to telemetry off.

## Build and install

From this repository:

```sh
go build -o bin/tern-java-navigation .
tern plugin link "$PWD"
tern plugin list
```

The plugin ID is `java-navigation`; its status should be `ready`. Keep the linked directory in place. There is no automatic first-use backend build.

After updating source:

```sh
go build -o bin/tern-java-navigation .
tern plugin reload
```

Uninstall a source link with `tern plugin unlink java-navigation`; remove a copied installation with `tern plugin remove java-navigation`. Unlink before switching to a copied package, or remove a copied installation before linking source.

## Shortcuts

Identical to the Go plugin; Alt is Option on macOS:

| Shortcut | Operation |
| --- | --- |
| Ctrl+Alt+G | Definitions and calls/references together |
| Ctrl+Alt+S | Search project symbols by name |
| Ctrl+Alt+B | Jump back |
| ↑ / ↓, Enter, Esc | Select, jump, dismiss the native picker |

Definitions come first; duplicate positions are removed. A single navigation result jumps directly. References include reads, writes and type uses, not only calls. Previews use the current window's unsaved buffers, trim surrounding whitespace and preserve internal spacing. Moving the caret, changing the selection or editing a captured buffer invalidates a pending jump.

Double-clicking or selecting a name works: the request carries the native zero-based UTF-8 byte range, and navigation starts at the selection's earlier endpoint for both forward and reverse selections. Without a non-empty selection the caret is used, so punctuation next to an identifier is still not silently rewritten to that identifier.

### Coexistence with Go

Tern's duplicate default chords do not reliably fall through by language. With both updated packages installed, Go's `jvm-navigation-host.luau` loads this package's `navigation.luau`: **one frontend owns the keys, picker and history**. It invokes the existing Go helper for Go and this package's helper for Java/Kotlin. Go and JVM overlays are collected separately. No global keybind preferences are rewritten.

The Go package must include `jvm-navigation-host.luau`; update it too when using both plugins. Run `tern plugin reload` after installing, updating or removing either package. Without Go installed, this plugin registers its own frontend normally.

Standalone action IDs are `plugin.java-navigation.navigate`, `.symbols`, and `.back`. In coexistence mode the shared frontend retains `plugin.go-navigation.navigate`, `.symbols`, and `.back`, under the `Go / Java / Kotlin Navigation` palette group. User keybinding overrides still take precedence.

## Search and caching

Submit a name with Enter; this is not an LSP query on every keystroke. The result picker can filter the returned list. Names are ranked by exact, prefix, substring and subsequence matches; comments and arbitrary body text are not indexed.

JDT's `workspace/symbol` searches types only. This helper indexes each source file's `documentSymbol` outline to include methods, constructors, fields, Kotlin functions and properties, including unopened files. The first search traverses the project's source files; later searches reuse outlines until the corresponding source or buffer changes. Mixed projects query both servers; a missing or failed required server produces an error rather than an apparently complete partial list.

Workspace selection uses Gradle settings roots and explicitly enrolled Maven reactor ancestors. Source lists are maintained by filesystem notifications: macOS uses one native FSEvents stream instead of one kqueue handle per directory or file, while Linux uses recursive fsnotify watches. Both flush all pending disk events before a query and fail the session if event history is incomplete, rather than returning a result computed from stale files. Changes/create/delete and dirty-buffer updates invalidate per-file entries; build configuration changes clear symbol caches. `target`, `build`, `out`, `bin`, dependency caches and symlink escapes are excluded. Maven modules and Gradle composite builds outside the selected root are not included in plugin search/overlay scope.

State lives under `/tmp/tern-java-<uid>/<identity>/`, with private 0700 directories and 0600 sockets. Window, project, helper binary and runtime environment identities are isolated. Editor-owned servers remain alive with the editor; standalone CLI daemons expire after five idle minutes. Replacing the helper retires old services. Temporary state may be cleared by the OS; full index persistence across reboots is not guaranteed.

## Cross-language boundaries

- Java requests use JDT LS; Kotlin requests use Kotlin LSP. Kotlin→Java definitions, Java references to Kotlin declarations, and merged Java/Kotlin symbol search are exercised by real-server smoke scenarios.
- **Full Java→Kotlin source navigation and complete Kotlin references queried from Java are not promised.** JDT LS does not analyze Kotlin, and the Kotlin distribution's Java provider is incomplete. No textual same-name heuristic fabricates semantic results.
- Kotlin's Java references can start at a call expression rather than its method identifier; the server-provided range is preserved.
- Java references use JDT's `projectOnly` scope (project sources, tests and referenced projects), not JDK/dependency internals. Definitions can still target libraries. This avoids indexing or returning enormous sets of internal JDK usages when querying a common type such as `String`; JDT LS 1.61+ is required for this scope.
- `jdt:` contents supplied by the server (attached source or its decompilation) and readable local `jar:` source entries are materialized unchanged into the private cache. Missing server source and local `.class` archive entries fail explicitly; the plugin does not fabricate decompiled text. Cached copies are not project source and should not be edited.
- Local files only. No ordinary-symbol Command-click and no separate selected-item preview panel.
- Source overlays are unsaved; Maven/Gradle build configuration must be saved. Servers may generate Eclipse/build metadata, and project import can execute build plugins. Open trusted projects only.
- Backend requests have a 180-second deadline; the editor invocation allows 185 seconds. Initial downloads/imports and large-project indexing can take time. Closing a session sends LSP `shutdown` / `exit` and waits up to one second before killing only that session's process group, so indexes and caches can be released cleanly. Errors/cancellation close the daemon's sessions; the next query initializes again.

## Troubleshooting

- **Empty Java navigation:** ensure Maven/Gradle dependencies resolve. Maven caches failed downloads; after repairing networking, `mvn -U compile` can refresh them. Restart the service/Tern afterward. JDT's detailed log is `data/.metadata/.log` under its state directory.
- **JDT LS exits with `Require-Capability: osgi.ee; ... version=25`:** 1.62/1.63 need Java 25 even though their README says 21. Install Java 25 and let runtime selection find it, set `JDTLS_JAVA_HOME`, or use JDT LS 1.61 with Java 21.
- **Empty Java navigation in a Gradle project:** JDT's log (`data/.metadata/.log`) may report `Unsupported class file major version 69`, meaning Gradle ran on Java 25. The plugin passes `JAVA_HOME` as `java.import.gradle.java.home`; set `JAVA_HOME` to a JDK your Gradle version supports (21 for Gradle 8.x) while keeping the server runtime separate.
- **Kotlin import failure:** the error includes the server log path, usually `kotlin/system/log/intellij-server.log`. `noBuildSystemFound` means the root is not a supported build project.
- **Gradle cannot find a JDK:** the Kotlin server's bundled JBR is not your project toolchain. Install the requested JDK; use the project's `org.gradle.java.installations.paths` for nonstandard locations.
- **CLI works but GUI fails:** inspect Tern's inherited PATH, `JDTLS_JAVA_HOME`, `JAVA_HOME` and server overrides. Rebuild the helper and reload plugins after source changes.
- **`no identifier found` after selecting a name:** update both `window.luau`/`navigation.luau` and the backend binary, then reload the plugin. A caret placed on punctuation next to a name is still invalid without a selection.
- **`too many open files` on macOS:** upgrade from a build predating 0.1.1, rebuild the helper, reload the plugin and restart Tern once to release handles held by the old process. The current macOS watcher uses a single FSEvents stream and does not require raising `ulimit`.

## Development and releases

```sh
go test -race ./...
go build -o bin/tern-java-navigation .
python3 scripts/package.py --check-tag v0.1.2
python3 scripts/package.py --output dist
python3 scripts/smoke_release.py dist/java-navigation-0.1.2-darwin-arm64.tar.gz
python3 scripts/smoke_kotlin.py --binary bin/tern-java-navigation --build-tool all
```

Use your native target in the archive name. Smoke requires JDT LS 1.61+ (1.61 runs on Java 21; 1.62/1.63 need Java 25), the Kotlin server, and project JDK 21 via `JAVA_HOME`. Gradle creates the wrapper; fixtures pin Kotlin 2.2.21 / Gradle 8.14.3 and are not precompiled. The Java smoke also checks `java.lang.String` source navigation with project-only usage results, and forwards/reverse native selections. CI runs it with 1.63/Java 25 and, on linux-amd64, with 1.61/Java 21.

On macOS, `-race` and cgo builds use the system linker. If a CommandLineTools SDK fails with `tapi error: malformed file`, point `SDKROOT` at the full Xcode SDK (`/Applications/Xcode.app/Contents/Developer/Platforms/MacOSX.platform/Developer/SDKs/MacOSX.sdk`); release builds set `CGO_ENABLED=0` and do not need it.

Packaging supports four Unix targets with SHA-256 files, compiled dependency licenses and bilingual documentation; JDKs and language servers are not bundled. Verify an archive's checksum before extracting and running `tern plugin install ./java-navigation`.

CI builds/tests/packages on native runners and exercises real JDT/Kotlin servers. Successful `vX.Y.Z` tags publish assets; manual runs without a release tag produce artifacts only. A workflow being present does not imply a remote release has already been published.

[Changelog](docs/reference/changelog.md)

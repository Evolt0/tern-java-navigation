#!/usr/bin/env python3
"""Smoke a native release against the official Kotlin server in mixed JVM projects."""

import argparse
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import uuid

from package import native_target
from smoke_release import (archive_target, check_destinations, require, run_query,
                           validate_and_extract)


KOTLIN_VERSION = "2.2.21"
GRADLE_VERSION = "8.14.3"


def create_build(project, build_tool, env):
    project.mkdir()
    if build_tool == "gradle":
        gradle = shutil.which("gradle")
        require(bool(gradle), "Gradle must be on PATH to create the smoke project's wrapper")
        (project / "settings.gradle.kts").write_text(
            'rootProject.name = "mixed-release-smoke"\n', encoding="utf-8")
        if env.get("JAVA_HOME"):
            # The language server runs on its bundled JBR; the build's JDK is
            # independent and may be installed outside standard discovery paths.
            (project / "gradle.properties").write_text(
                "org.gradle.java.installations.paths=" + env["JAVA_HOME"] + "\n", encoding="utf-8")
        # Create the wrapper before adding plugins: this step must not compile
        # the fixture, so cross-language navigation cannot rely on class files.
        subprocess.run([gradle, "--no-daemon", "wrapper", "--gradle-version", GRADLE_VERSION],
                       cwd=project, env=env, check=True, timeout=180)
        gradlew = project / ("gradlew.bat" if os.name == "nt" else "gradlew")
        # With an isolated GRADLE_USER_HOME the first import would download the
        # distribution and Kotlin plugin inside the language-server timeout.
        # Warm both here; `help` configures the plugin without compiling sources.
        subprocess.run([str(gradlew), "--version"], cwd=project, env=env, check=True, timeout=300)
        (project / "build.gradle.kts").write_text(
            'plugins { kotlin("jvm") version "' + KOTLIN_VERSION + '" }\n'
            'repositories { mavenCentral() }\n'
            'kotlin { jvmToolchain(21) }\n', encoding="utf-8")
        subprocess.run([str(gradlew), "--no-daemon", "help"], cwd=project, env=env, check=True, timeout=300)
        return
    (project / "pom.xml").write_text(
        '<?xml version="1.0" encoding="UTF-8"?>\n'
        '<project xmlns="http://maven.apache.org/POM/4.0.0" '
        'xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" '
        'xsi:schemaLocation="http://maven.apache.org/POM/4.0.0 '
        'https://maven.apache.org/xsd/maven-4.0.0.xsd">\n'
        '  <modelVersion>4.0.0</modelVersion>\n'
        '  <groupId>example</groupId><artifactId>mixed-release-smoke</artifactId><version>1.0.0</version>\n'
        '  <properties><kotlin.version>' + KOTLIN_VERSION + '</kotlin.version>'
        '<maven.compiler.release>21</maven.compiler.release>'
        '<maven.compiler.source>21</maven.compiler.source><maven.compiler.target>21</maven.compiler.target>'
        '<project.build.sourceEncoding>UTF-8</project.build.sourceEncoding></properties>\n'
        '  <dependencies><dependency><groupId>org.jetbrains.kotlin</groupId>'
        '<artifactId>kotlin-stdlib</artifactId><version>${kotlin.version}</version></dependency></dependencies>\n'
        '  <build><plugins><plugin><groupId>org.jetbrains.kotlin</groupId>'
        '<artifactId>kotlin-maven-plugin</artifactId><version>${kotlin.version}</version>\n'
        '    <executions><execution><id>compile-kotlin</id><phase>process-sources</phase>'
        '<goals><goal>compile</goal></goals></execution></executions>\n'
        '    <configuration><jvmTarget>21</jvmTarget><sourceDirs>'
        '<sourceDir>${project.basedir}/src/main/kotlin</sourceDir>'
        '<sourceDir>${project.basedir}/src/main/java</sourceDir>'
        '</sourceDirs></configuration>\n'
        '  </plugin></plugins></build>\n'
        '</project>\n', encoding="utf-8")


def smoke(binary, project, build_tool):
    launcher = os.environ.get("KOTLIN_LSP") or shutil.which("kotlin-lsp")
    resolved = shutil.which(launcher) if launcher else None
    require(bool(resolved), "install the official kotlin-lsp server or set KOTLIN_LSP to its executable")
    env = dict(os.environ, KOTLIN_LSP=str(Path(resolved).resolve()),
               TERN_WINDOW_KEY="kotlin-release-smoke-" + uuid.uuid4().hex)
    # CI Gradle actions install init scripts and caching into GRADLE_USER_HOME.
    # The language server imports with the runner's Gradle and must not inherit
    # them: an isolated home proves the project imports on its own.
    env["GRADLE_USER_HOME"] = str(project.parent / "gradle-user-home")
    create_build(project, build_tool, env)
    kotlin = project / "src/main/kotlin/mixedsmoke"
    java = project / "src/main/java/mixedsmoke"
    kotlin.mkdir(parents=True)
    java.mkdir(parents=True)
    target = kotlin / "KotlinTarget.kt"
    caller = kotlin / "KotlinCaller.kt"
    java_caller = java / "JavaCaller.java"
    java_target = java / "JavaTarget.java"
    java_native_caller = java / "JavaNativeCaller.java"
    kotlin_java_caller = kotlin / "KotlinJavaCaller.kt"
    unopened = kotlin / "Unopened.kt"
    saved = {
        target: "package mixedsmoke\n\nclass KotlinTarget {\n    fun DiskOriginal(): Int = 7\n}\n",
        caller: ("package mixedsmoke\n\nclass KotlinCaller {\n"
                 "    fun call(): Int = KotlinTarget().DiskOriginal()\n}\n"),
        java_caller: ("package mixedsmoke;\n\npublic class JavaCaller {\n"
                      "    public int call() { return new KotlinTarget().DiskOriginal(); }\n}\n"),
        java_target: ("package mixedsmoke;\n\npublic class JavaTarget {\n"
                      "    public static int JavaNeedleMethod() { return 11; }\n}\n"),
        java_native_caller: ("package mixedsmoke;\n\npublic class JavaNativeCaller {\n"
                             "    public int call() { return JavaTarget.JavaNeedleMethod(); }\n}\n"),
        kotlin_java_caller: ("package mixedsmoke\n\nclass KotlinJavaCaller {\n"
                             "    fun call(): Int = JavaTarget.JavaNeedleMethod()\n}\n"),
        unopened: ("package mixedsmoke\n\nclass Unopened {\n"
                   "    fun ClosedKotlinNeedleMethod(): Int = 13\n}\n"),
    }
    for path, text in saved.items():
        path.write_text(text, encoding="utf-8")
    documents = {str(path): saved[path].replace("DiskOriginal", "UnsavedKotlinNeedle")
                 for path in (target, caller, java_caller)}
    documents[str(target)] = documents[str(target)].replace(
        "    fun UnsavedKotlinNeedle", "\n    fun UnsavedKotlinNeedle")
    documents[str(kotlin_java_caller)] = saved[kotlin_java_caller]
    documents[str(java_native_caller)] = saved[java_native_caller]

    def expected(path, needle, kind, name=None):
        lines = documents.get(str(path), saved[path]).splitlines()
        matches = [(index + 1, line.index(needle) + 1, line.strip())
                   for index, line in enumerate(lines) if needle in line]
        require(len(matches) == 1, "fixture symbol is not unique: " + needle)
        line, col, preview = matches[0]
        return (str(path), line, col), (kind, preview, name)

    def query_at(path, needle):
        position, _ = expected(path, needle, "reference")
        return {"path": str(path), "line": position[1], "col": position[2],
                "tabWidth": 4, "documents": documents}

    def check_kotlin_navigation(actual, entries, java_expressions):
        destinations = dict(entries)
        for destination in actual:
            path = Path(destination.get("path", ""))
            if destination.get("kind") != "reference" or path not in java_expressions:
                continue
            needle, expression = java_expressions[path]
            key, detail = expected(path, needle, "reference")
            line = documents.get(str(path), saved[path]).splitlines()[key[1] - 1]
            first_col = line.index(expression) + 1
            col = destination.get("col")
            require(destination.get("line") == key[1] and isinstance(col, int) and first_col <= col <= key[2],
                    "Java reference must start within its call expression: " + repr(destination))
            require(key in destinations, "unexpected duplicate Java reference: " + repr(destination))
            del destinations[key]
            destinations[(key[0], key[1], col)] = detail
        # Kotlin's Java provider may report the whole call expression rather
        # than its method identifier. All other location fields remain exact.
        check_destinations(actual, destinations)

    query = query_at(caller, "UnsavedKotlinNeedle")
    step = "dirty Kotlin navigation"
    try:
        actual = run_query(binary, project, env, query)
        check_kotlin_navigation(actual, [
            expected(target, "UnsavedKotlinNeedle", "definition"),
            expected(caller, "UnsavedKotlinNeedle", "reference"),
            expected(java_caller, "UnsavedKotlinNeedle", "reference"),
        ], {java_caller: ("UnsavedKotlinNeedle", "new KotlinTarget()")})
        require([item["kind"] for item in actual] == ["definition", "reference", "reference"],
                "Kotlin navigation must put definitions before references")
        step = "Kotlin to Java navigation"
        check_kotlin_navigation(run_query(binary, project, env,
                                           query_at(kotlin_java_caller, "JavaNeedleMethod")), [
            expected(java_target, "JavaNeedleMethod", "definition"),
            expected(kotlin_java_caller, "JavaNeedleMethod", "reference"),
            expected(java_native_caller, "JavaNeedleMethod", "reference"),
        ], {java_native_caller: ("JavaNeedleMethod", "JavaTarget.")})
        # Java focus uses JDT LS, which indexes Java references separately.
        step = "Java navigation"
        check_destinations(run_query(binary, project, env,
                                     query_at(java_native_caller, "JavaNeedleMethod")), dict([
            expected(java_target, "JavaNeedleMethod", "definition"),
            expected(java_native_caller, "JavaNeedleMethod", "reference"),
        ]))
        for path, needle in ((target, "UnsavedKotlinNeedle"), (unopened, "ClosedKotlinNeedleMethod"),
                             (java_target, "JavaNeedleMethod")):
            step = "symbols " + needle
            check_destinations(run_query(binary, project, env, dict(query, action="symbols", search=needle)),
                               dict([expected(path, needle, "symbol", needle)]))
        step = "stale symbol exclusion"
        check_destinations(run_query(binary, project, env,
                                     dict(query, action="symbols", search="DiskOriginal")), {})
        for path, text in saved.items():
            require(path.read_text(encoding="utf-8") == text, "unsaved overlay modified disk: " + str(path))
    except ValueError as error:
        raise ValueError(step + ": " + str(error)) from error
    finally:
        # Each focus language selects its own language-server session.
        run_query(binary, project, env, query, shutdown=True)
        run_query(binary, project, env, query_at(java_native_caller, "JavaNeedleMethod"), shutdown=True)
    print("Kotlin " + build_tool + " smoke passed: dirty Kotlin navigation, Kotlin-to-Java definitions, "
          "Java navigation, merged symbols, and shutdown")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("archive", type=Path, nargs="?", help="java-navigation-X.Y.Z-<os>-<arch>.tar.gz")
    parser.add_argument("--binary", type=Path, help="exercise an existing native executable instead of an archive")
    parser.add_argument("--build-tool", choices=("maven", "gradle", "all"), default="all")
    args = parser.parse_args()
    if (args.archive is None) == (args.binary is None):
        parser.error("provide either a release archive or --binary")
    if args.archive:
        require(archive_target(args.archive) == native_target(), "Kotlin smoke requires a matching native release")
    root = Path(tempfile.mkdtemp(prefix="kotlin-navigation-smoke-")).resolve()
    try:
        binary = args.binary.resolve() if args.binary else validate_and_extract(args.archive.resolve(), root)
        require(binary.is_file() and os.access(binary, os.X_OK), "smoke binary is not executable: " + str(binary))
        for build_tool in (("maven", "gradle") if args.build_tool == "all" else (args.build_tool,)):
            smoke(binary, root / build_tool, build_tool)
    finally:
        # Gradle or the Kotlin server can still be flushing caches just after
        # shutdown. The smoke result is already known; do not fail on cleanup.
        shutil.rmtree(root, ignore_errors=True)


def log_tail(pattern):
    logs = sorted(Path(tempfile.gettempdir()).glob(pattern),
                  key=lambda path: path.stat().st_mtime, reverse=True)
    if not logs:
        return ""
    return "\n".join(logs[0].read_text(encoding="utf-8", errors="replace").splitlines()[-100:])


def diagnostics():
    parts = []
    for label, pattern in (("JDT LS", "tern-java-*/**/data/.metadata/.log"),
                           ("Kotlin LSP", "tern-java-*/**/kotlin/system/log/intellij-server.log")):
        tail = log_tail(pattern)
        if tail:
            parts.append(label + " log:\n" + tail)
    return "\n\n".join(parts)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, tarfile.TarError, subprocess.SubprocessError) as error:
        tail = diagnostics()
        raise SystemExit("Kotlin release smoke failed: " + str(error) + (("\n\n" + tail) if tail else ""))

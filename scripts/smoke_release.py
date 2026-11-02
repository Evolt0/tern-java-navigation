#!/usr/bin/env python3
"""Validate a release archive and exercise it on a matching native host (Python 3.9+)."""

import argparse
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import subprocess
import tarfile
import tempfile
import uuid

from package import (BINARY, PLUGIN_ID, REQUIRED_FILES, TARGETS, archive_name,
                     manifest_version, native_target, sha256)


def require(condition, message):
    if not condition:
        raise ValueError(message)


def archive_target(path):
    for target in TARGETS:
        if path.name.endswith("-" + target + ".tar.gz"):
            return target
    raise ValueError("archive filename must identify a supported release target")


def validate_binary_header(header, target):
    system, architecture = target.split("-")
    if system == "darwin":
        cpu = {"arm64": 0x0100000C, "amd64": 0x01000007}[architecture]
        require(len(header) >= 32 and header[:4] == b"\xcf\xfa\xed\xfe" and
                int.from_bytes(header[4:8], "little") == cpu and
                int.from_bytes(header[12:16], "little") == 2,
                "packaged binary is not a 64-bit " + architecture + " Mach-O executable")
    else:
        machine = {"arm64": 183, "amd64": 62}[architecture]
        require(len(header) >= 32 and header[:4] == b"\x7fELF" and
                header[4:7] == b"\x02\x01\x01" and
                int.from_bytes(header[16:18], "little") in (2, 3) and
                int.from_bytes(header[18:20], "little") == machine,
                "packaged binary is not a little-endian 64-bit " + architecture + " ELF executable")


def validate_and_extract(path, destination):
    target = archive_target(path)
    checksum = path.with_name(path.name + ".sha256").read_text(encoding="ascii")
    require(re.fullmatch(r"[0-9a-f]{64}  " + re.escape(path.name) + r"\n", checksum) is not None,
            "checksum must contain one SHA-256 digest and the archive basename only")
    require(sha256(path) == checksum[:64], "archive SHA-256 mismatch")
    with tarfile.open(path, "r:gz") as archive:
        members = {}
        for member in archive.getmembers():
            name = member.name.rstrip("/") if member.isdir() else member.name
            parts = PurePosixPath(name).parts
            require(bool(parts) and parts[0] == PLUGIN_ID and
                    all(part not in (".", "..") for part in parts) and
                    name == PurePosixPath(name).as_posix() and "\\" not in name,
                    "unsafe archive path: " + member.name)
            require(name not in members, "duplicate archive entry: " + name)
            require(member.isdir() or member.isfile(), "archive links/devices are forbidden: " + name)
            expected_mode = 0o755 if member.isdir() or name == PLUGIN_ID + "/" + BINARY else 0o644
            require(member.mode == expected_mode, "incorrect archive permissions: " + name)
            require(member.uid == member.gid == member.mtime == 0 and
                    not member.uname and not member.gname,
                    "archive metadata is not normalized: " + name)
            members[name] = member
        require(PLUGIN_ID in members and members[PLUGIN_ID].isdir(), "missing archive root")
        required = set(REQUIRED_FILES) | {BINARY, "THIRD_PARTY_NOTICES.txt", "licenses/Go/LICENSE"}
        files = {name[len(PLUGIN_ID) + 1:] for name, member in members.items() if member.isfile()}
        require(required <= files, "missing release files: " + ", ".join(sorted(required - files)))
        for relative in files:
            require(relative in required or relative.startswith("licenses/"),
                    "unexpected release content: " + relative)
            require(not any(part.startswith(".") or part == "__pycache__"
                            for part in PurePosixPath(relative).parts),
                    "hidden/cache content in archive: " + relative)
            require(members[PLUGIN_ID + "/" + relative].size > 0, "empty release file: " + relative)
        for name in members:
            if name == PLUGIN_ID:
                continue
            parent = PurePosixPath(name).parent.as_posix()
            require(parent in members and members[parent].isdir(), "missing archive directory: " + parent)
        for name, member in members.items():
            if member.isdir():
                require(name == PLUGIN_ID or any(file.startswith(name + "/") for file in members
                                                if members[file].isfile()),
                        "unexpected empty directory: " + name)

        def text(relative):
            with archive.extractfile(members[PLUGIN_ID + "/" + relative]) as source:
                return source.read().decode("utf-8")

        version = manifest_version(text("plugin.toml"))
        require(path.name == archive_name(version, target), "archive name does not match the packaged manifest")
        notices = text("THIRD_PARTY_NOTICES.txt")
        license_paths = re.findall(r"^License: (.+)$", notices, re.MULTILINE)
        require("licenses/Go/LICENSE" in license_paths, "missing Go runtime license notice")
        for relative in license_paths + re.findall(r"^Notice: (.+)$", notices, re.MULTILINE):
            require(relative.startswith("licenses/") and relative in files,
                    "notice references missing license content: " + relative)
        with archive.extractfile(members[PLUGIN_ID + "/" + BINARY]) as binary:
            validate_binary_header(binary.read(32), target)
        # All paths/types have been checked; copying regular files explicitly
        # avoids tar extraction behavior differences between Python versions.
        for name, member in sorted(members.items()):
            target = destination / name
            if member.isdir():
                target.mkdir(parents=True, exist_ok=True)
            else:
                target.parent.mkdir(parents=True, exist_ok=True)
                with archive.extractfile(member) as source, target.open("wb") as output:
                    shutil.copyfileobj(source, output)
            target.chmod(member.mode)
    binary = destination / PLUGIN_ID / BINARY
    # Platform-specific files mean the compiled dependency set differs by
    # target (for example fsnotify on Linux but FSEvents bindings on macOS).
    build_info = subprocess.check_output(["go", "version", "-m", str(binary)], text=True)
    dependencies = re.findall(r"^\s*dep\s+(\S+)\s+(\S+)", build_info, re.MULTILINE)
    require(bool(dependencies), "packaged binary has no dependency build information")
    for dependency, version in dependencies:
        prefix = "licenses/" + dependency + "@" + version + "/"
        require(any(name.startswith(prefix) for name in license_paths),
                "missing compiled dependency license: " + dependency + "@" + version)
    return binary


def run_query(binary, project, env, query, shutdown=False):
    command = [str(binary)] + (["--shutdown"] if shutdown else [])
    result = subprocess.run(command, cwd=project, env=env, input=json.dumps(query),
                            text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=190)
    require(result.returncode == 0, "packaged CLI failed: " + result.stderr.strip())
    if shutdown:
        return None
    result = json.loads(result.stdout)
    require(isinstance(result, list), "packaged CLI must return a destinations array")
    return result


def check_destinations(actual, expected):
    require(len(actual) == len(expected), "unexpected destinations: " + repr(actual))
    seen = set()
    for target in actual:
        key = (target.get("path"), target.get("line"), target.get("col"))
        require(key in expected and key not in seen, "unexpected/duplicate destination: " + repr(target))
        seen.add(key)
        kind, preview, name = expected[key]
        require(target.get("kind") == kind and target.get("preview") == preview,
                "incorrect destination kind/preview: " + repr(target))
        require(target.get("url") == Path(key[0]).as_uri() + "#L{}C{}".format(key[1], key[2]),
                "incorrect destination URL: " + repr(target))
        if name:
            actual_name = target.get("name", "")
            require(actual_name == name or actual_name.endswith("." + name) or
                    actual_name.startswith(name + "(") or ("." + name + "(") in actual_name,
                    "incorrect symbol name: " + repr(target))


def smoke(binary, project):
    launcher = os.environ.get("JDTLS")
    home = os.environ.get("JDTLS_HOME")
    if launcher:
        resolved = shutil.which(launcher)
        require(bool(resolved), "JDTLS is not executable: " + launcher)
    elif home:
        require(Path(home).is_dir(), "JDTLS_HOME must identify an extracted Eclipse JDT LS distribution")
    else:
        resolved = shutil.which("jdtls")
        require(bool(resolved), "install jdtls or set JDTLS / JDTLS_HOME")
    java_home = os.environ.get("JDTLS_JAVA_HOME") or os.environ.get("JAVA_HOME")
    java = str(Path(java_home) / "bin/java") if java_home else shutil.which("java")
    require(bool(java) and shutil.which(java) is not None, "a Java runtime matching the JDT LS version must be available via JDTLS_JAVA_HOME, JAVA_HOME or PATH")
    env = dict(os.environ, TERN_WINDOW_KEY="release-smoke-" + uuid.uuid4().hex)
    if launcher or not home:
        env["JDTLS"] = str(Path(resolved).resolve())
    project.mkdir()
    (project / "pom.xml").write_text(
        '<?xml version="1.0" encoding="UTF-8"?>\n'
        '<project xmlns="http://maven.apache.org/POM/4.0.0" '
        'xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" '
        'xsi:schemaLocation="http://maven.apache.org/POM/4.0.0 '
        'https://maven.apache.org/xsd/maven-4.0.0.xsd">\n'
        '  <modelVersion>4.0.0</modelVersion>\n'
        '  <groupId>example</groupId><artifactId>release-smoke</artifactId><version>1.0.0</version>\n'
        '  <properties><maven.compiler.release>21</maven.compiler.release>'
        '<maven.compiler.source>21</maven.compiler.source><maven.compiler.target>21</maven.compiler.target>'
        '<project.build.sourceEncoding>UTF-8</project.build.sourceEncoding></properties>\n'
        '</project>\n', encoding="utf-8")
    source = project / "src/main/java/releasesmoke"
    source.mkdir(parents=True)
    target, caller, other, closed = [source / name for name in (
        "Target.java", "Caller.java", "Other.java", "ClosedNeedleType.java")]
    saved = {
        target: "package releasesmoke;\n\npublic class Target {\n    public static void DiskOriginal() {}\n}\n",
        caller: "package releasesmoke;\n\npublic class Caller {\n    public void call() { Target.DiskOriginal(); }\n}\n",
        other: "package releasesmoke;\n\npublic class Other {\n    public void call() { Target.DiskOriginal(); }\n}\n",
        closed: ("package releasesmoke;\n\npublic class ClosedNeedleType {\n"
                 "    public static int ClosedNeedleField = 7;\n"
                 "    public void ClosedNeedleMethod() {}\n}\n"),
    }
    for path, text in saved.items():
        path.write_text(text, encoding="utf-8")
    documents = {
        str(target): ("package releasesmoke;\n\npublic class Target {\n\n"
                      "    public static void UnsavedNeedleTarget() {} // unsaved\n}\n"),
        str(caller): saved[caller].replace("DiskOriginal", "UnsavedNeedleTarget"),
        str(other): saved[other].replace("DiskOriginal", "UnsavedNeedleTarget"),
    }

    def expected(path, needle, kind, symbol=None):
        lines = documents.get(str(path), saved[path]).splitlines()
        matches = [(index + 1, line.index(needle) + 1, line.strip())
                   for index, line in enumerate(lines) if needle in line]
        require(len(matches) == 1, "fixture symbol is not unique: " + needle)
        line, col, preview = matches[0]
        return (str(path), line, col), (kind, preview, symbol)

    position, _ = expected(caller, "UnsavedNeedleTarget", "reference")
    query = {"path": str(caller), "line": position[1], "col": position[2],
             "tabWidth": 4, "documents": documents}
    try:
        navigation = run_query(binary, project, env, query)
        check_destinations(navigation, dict([
            expected(target, "UnsavedNeedleTarget", "definition"),
            expected(caller, "UnsavedNeedleTarget", "reference"),
            expected(other, "UnsavedNeedleTarget", "reference"),
        ]))
        selected_start = documents[str(caller)].index("UnsavedNeedleTarget")
        for selection in (
            {"anchor": selected_start, "head": selected_start + len("UnsavedNeedleTarget")},
            {"anchor": selected_start + len("UnsavedNeedleTarget"), "head": selected_start},
        ):
            selected = dict(query, selection=selection)
            check_destinations(run_query(binary, project, env, selected), dict([
                expected(target, "UnsavedNeedleTarget", "definition"),
                expected(caller, "UnsavedNeedleTarget", "reference"),
                expected(other, "UnsavedNeedleTarget", "reference"),
            ]))
        require([item["kind"] for item in navigation] == ["definition", "reference", "reference"],
                "combined navigation must put definitions before references")
        for path, needle in ((target, "UnsavedNeedleTarget"), (closed, "ClosedNeedleMethod"),
                             (closed, "ClosedNeedleField"), (closed, "ClosedNeedleType")):
            symbols = dict(query, action="symbols", search=needle)
            check_destinations(run_query(binary, project, env, symbols),
                               dict([expected(path, needle, "symbol", needle)]))
        check_destinations(run_query(binary, project, env,
                                     dict(query, action="symbols", search="DiskOriginal")), {})
        # A JDK type used to expand reference search over the entire JDK. The
        # definition must remain navigable while usages stay in project sources.
        jdk_text = "package releasesmoke;\npublic class Caller { String value; }\n"
        jdk_col = jdk_text.splitlines()[1].index("String") + 1
        jdk_query = dict(query, line=2, col=jdk_col, documents={str(caller): jdk_text})
        jdk_targets = run_query(binary, project, env, jdk_query)
        definitions = [item for item in jdk_targets if item["kind"] == "definition"]
        references = [item for item in jdk_targets if item["kind"] == "reference"]
        require(len(definitions) == 1 and Path(definitions[0]["path"]).name == "String.java"
                and "class String" in definitions[0]["preview"], "JDK source definition missing: " + repr(jdk_targets))
        check_destinations(references, {(str(caller), 2, jdk_col):
                                      ("reference", jdk_text.splitlines()[1], None)})
        for path, text in saved.items():
            require(path.read_text(encoding="utf-8") == text, "unsaved overlay modified disk: " + str(path))
    finally:
        # Shut down the exact daemon identity before deleting its executable or
        # project; a second request also verifies shutdown is safe when stopped.
        run_query(binary, project, env, query, shutdown=True)
        run_query(binary, project, env, query, shutdown=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("archive", type=Path, help="java-navigation-X.Y.Z-<os>-<arch>.tar.gz")
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="java-navigation-smoke-") as temporary:
        root = Path(temporary).resolve()
        binary = validate_and_extract(args.archive.resolve(), root)
        target = archive_target(args.archive)
        host = native_target()
        require(target == host,
                "cannot execute " + target + " release on " + host + "; smoke requires a matching native host")
        smoke(binary, root / "project")
    print("Release smoke passed: archive, checksum, real JDT LS Maven navigation, dirty overlays, unopened symbols, selected identifiers, JDK source with project-only references, and shutdown")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, tarfile.TarError, subprocess.SubprocessError) as error:
        raise SystemExit("Release smoke failed: " + str(error))

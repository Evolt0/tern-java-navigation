package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDecodeJavaLocationVariants(t *testing.T) {
	for _, raw := range []string{
		`{"uri":"file:///A.java","range":{"start":{"line":2,"character":4},"end":{"line":2,"character":5}}}`,
		`[{"uri":"file:///A.java","range":{"start":{"line":2,"character":4},"end":{"line":2,"character":5}}}]`,
		`[{"targetUri":"file:///A.java","targetRange":{"start":{"line":0,"character":0},"end":{"line":9,"character":0}},"targetSelectionRange":{"start":{"line":2,"character":4},"end":{"line":2,"character":5}}}]`,
	} {
		locations, err := decodeLocations(json.RawMessage(raw))
		if err != nil {
			t.Fatal(err)
		}
		if len(locations) != 1 || locations[0].URI != "file:///A.java" || locations[0].Range.Start != (position{Line: 2, Character: 4}) {
			t.Fatalf("unexpected locations: %+v", locations)
		}
	}
	for _, raw := range []string{`null`, `[]`, " \nnull "} {
		locations, err := decodeLocations(json.RawMessage(raw))
		if err != nil || len(locations) != 0 {
			t.Fatalf("empty result: %+v %v", locations, err)
		}
	}
	if _, err := decodeLocations(json.RawMessage(`[{"targetUri":"file:///A.java"}]`)); err == nil {
		t.Fatal("missing selection range accepted")
	}
}

func TestJavaReadinessWaitsForImportedProjects(t *testing.T) {
	s := &session{ready: make(chan error, 1)}
	for _, status := range []string{"Starting", "ServiceReady"} {
		s.notification("language/status", json.RawMessage(fmt.Sprintf(`{"type":%q}`, status)))
	}
	select {
	case <-s.ready:
		t.Fatal("ready before project import")
	default:
	}
	s.notification("language/status", json.RawMessage(`{"type":"Started","message":"Ready"}`))
	select {
	case err := <-s.ready:
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("missing readiness")
	}
	s.notification("language/status", json.RawMessage(`{"type":"Started"}`))
	select {
	case <-s.ready:
		t.Fatal("duplicate readiness")
	default:
	}
	failed := &session{ready: make(chan error, 1)}
	failed.notification("language/status", json.RawMessage(`{"type":"Error","message":"Gradle import failed"}`))
	if err := <-failed.ready; err == nil || !strings.Contains(err.Error(), "Gradle import failed") {
		t.Fatalf("missing import error: %v", err)
	}
}

func TestJDTDistributionUsesIsolatedConfiguration(t *testing.T) {
	home := t.TempDir()
	platform := map[string]string{"darwin": "mac", "linux": "linux", "windows": "win"}[runtime.GOOS]
	if platform == "" {
		t.Skip("unsupported platform")
	}
	configuration := filepath.Join(home, "config_"+platform)
	for _, dir := range []string{configuration, filepath.Join(home, "plugins")} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(configuration, "config.ini"), []byte("osgi.bundles=reference:file:bundle.jar\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "plugins", "org.eclipse.equinox.launcher_test.jar"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	isolated := filepath.Join(state, "configuration")
	launch, err := distributionLaunch(home, "/custom/java", filepath.Join(state, "data"), isolated, javaLaunch{})
	if err != nil {
		t.Fatal(err)
	}
	if launch.executable != "/custom/java" {
		t.Fatalf("runtime: %s", launch.executable)
	}
	joined := strings.Join(launch.args, " ")
	if !strings.Contains(joined, "-configuration "+isolated) || !strings.Contains(joined, "-data "+filepath.Join(state, "data")) {
		t.Fatalf("nonisolated args: %v", launch.args)
	}
	content, err := os.ReadFile(filepath.Join(isolated, "config.ini"))
	if err != nil || !strings.Contains(string(content), "osgi.bundles") {
		t.Fatalf("configuration copy: %s %v", content, err)
	}
}

func TestJavaOverlayLifecycleInvalidatesSymbols(t *testing.T) {
	var output bytes.Buffer
	s := &session{rpc: &rpc{out: &output}, documents: make(map[string]overlay), symbolIndex: make(map[string][]projectSymbol)}
	path := filepath.Join(t.TempDir(), "A.java")
	s.projectRoots = []string{filepath.Dir(path)}
	s.symbolIndex[path] = nil
	if err := s.syncDocuments(map[string]string{path: "class A {}"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.symbolIndex[path]; ok {
		t.Fatal("open retained stale symbols")
	}
	first := output.Len()
	if err := s.syncDocuments(map[string]string{path: "class A {}"}); err != nil {
		t.Fatal(err)
	}
	if output.Len() != first {
		t.Fatal("unchanged overlay resent")
	}
	s.symbolIndex[path] = nil
	if err := s.syncDocuments(map[string]string{path: "class A { int x; }"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.symbolIndex[path]; ok {
		t.Fatal("change retained stale symbols")
	}
	s.symbolIndex[path] = nil
	if err := s.syncDocuments(nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.symbolIndex[path]; ok {
		t.Fatal("close retained stale symbols")
	}
	reader := &rpc{in: bufio.NewReader(&output)}
	for _, method := range []string{"textDocument/didOpen", "textDocument/didChange", "textDocument/didClose"} {
		message, err := reader.receive()
		if err != nil || message.Method != method {
			t.Fatalf("overlay message: %+v %v", message, err)
		}
	}
}

func classFileRPC(t *testing.T, source any) *rpc {
	t.Helper()
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()
	t.Cleanup(func() { clientRead.Close(); serverWrite.Close(); serverRead.Close(); clientWrite.Close() })
	go func() {
		server := &rpc{in: bufio.NewReader(serverRead), out: serverWrite}
		message, err := server.receive()
		if err != nil {
			return
		}
		if message.Method != "java/classFileContents" {
			t.Errorf("unexpected classfile method %s", message.Method)
		}
		_ = server.send(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": source})
	}()
	return newRPC(clientRead, clientWrite, nil)
}

func TestJavaClassFileSourcePreservesUTF16Positions(t *testing.T) {
	for _, suffix := range []string{".class", ".java"} {
		t.Run(suffix, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			source := "// source\n\t// 😀 value\n"
			s := &session{rpc: classFileRPC(t, source), sourceCache: t.TempDir()}
			uri := "jdt://contents/lib.jar/pkg/A" + suffix + "?handle"
			locations := []location{{URI: uri, Range: sourceRange{Start: position{Line: 1, Character: 7}}}}
			materialized, err := s.materializeLocations(ctx, locations)
			if err != nil {
				t.Fatal(err)
			}
			q := query{TabWidth: 4, Documents: make(map[string]string)}
			targets, err := destinations(materialized, "definition", q, make(map[string]bool))
			if err != nil {
				t.Fatal(err)
			}
			if len(targets) != 1 || targets[0].Line != 2 || targets[0].Col != 11 {
				t.Fatalf("shifted UTF16 position: %+v", targets)
			}
			if filepath.Base(targets[0].Path) != "A.java" {
				t.Fatalf("incorrect source filename: %s", targets[0].Path)
			}
			content, err := os.ReadFile(targets[0].Path)
			if err != nil || string(content) != source {
				t.Fatalf("modified classfile source: %q %v", content, err)
			}
			// No second response is supplied: repeated navigation must use this session's source cache.
			if _, err = s.materializeLocations(ctx, []location{{URI: uri}}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestJavaClassFileSourceUnavailableIsAnError(t *testing.T) {
	for _, source := range []any{nil, "", " \n"} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		s := &session{rpc: classFileRPC(t, source), sourceCache: t.TempDir()}
		_, err := s.materializeLocations(ctx, []location{{URI: "jdt://contents/missing.class"}})
		cancel()
		if err == nil || !strings.Contains(err.Error(), "source unavailable") {
			t.Fatalf("missing actionable error: %v", err)
		}
	}
}

func TestJavaWatcherTracksNewFilesAndBuildConfiguration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	root, err := canonical(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"src", "target", "build", ".gradle"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, dir, "Old.java"), []byte("class Old {}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	watcher, err := newFileWatcher(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.close()
	if got := watcher.sourceFiles(); !reflect.DeepEqual(got, []string{filepath.Join(root, "src", "Old.java")}) {
		t.Fatalf("output directories indexed: %v", got)
	}
	added := filepath.Join(root, "src", "New.java")
	if err := os.WriteFile(added, []byte("class New {}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pom.xml"), []byte("<project/>"), 0600); err != nil {
		t.Fatal(err)
	}
	changes, builds, err := watcher.changes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !builds {
		t.Fatal("build change not detected")
	}
	found := false
	for _, change := range changes {
		found = found || change.URI == fileURI(added) && change.Type == 1
	}
	if !found {
		t.Fatalf("missing created Java file: %+v", changes)
	}
	if err := os.Remove(added); err != nil {
		t.Fatal(err)
	}
	changes, _, err = watcher.changes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, change := range changes {
		found = found || change.URI == fileURI(added) && change.Type == 3
	}
	if !found {
		t.Fatalf("missing deleted Java file: %+v", changes)
	}
}

func TestJVMWatchedSourceAndBuildFiles(t *testing.T) {
	for _, name := range []string{"src/A.java", "src/Main.kt", "scripts/tool.kts", "build.gradle.kts", "settings.gradle", ".mvn/maven.config", ".settings/org.eclipse.jdt.core.prefs"} {
		if !relevantFile(name) {
			t.Errorf("not watched: %s", name)
		}
	}
	for _, name := range []string{"Main.class", "README.md", "library.jar", "go.mod"} {
		if relevantFile(name) {
			t.Errorf("unexpected watched file: %s", name)
		}
	}
	for _, name := range []string{"target", "build", ".gradle", ".m2", ".metadata", "out", "bin"} {
		if !ignoredDir(name) {
			t.Errorf("output/cache not ignored: %s", name)
		}
	}
	if ignoredDir(".mvn") || ignoredDir(".settings") || ignoredDir("src") {
		t.Fatal("source/build configuration directory ignored")
	}
}

func TestKotlinReadinessWaitsForSuccessfulImport(t *testing.T) {
	s := &session{kotlin: true, ready: make(chan error, 1)}
	s.notification("intellij/workspaceImportState", json.RawMessage(`{"phase":"IN_PROGRESS","folders":[]}`))
	s.notification("intellij/ready-for-test", json.RawMessage(`{}`))
	select {
	case <-s.ready:
		t.Fatal("ready before completed import")
	default:
	}
	s.notification("intellij/workspaceImportState", json.RawMessage(`{"phase":"FINISHED","folders":[{"folderUri":"file:///project/","status":"SUCCESS"}]}`))
	select {
	case err := <-s.ready:
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("missing import readiness")
	}
	failed := &session{kotlin: true, ready: make(chan error, 1)}
	failed.notification("intellij/workspaceImportState", json.RawMessage(`{"phase":"FINISHED","folders":[{"folderUri":"file:///project/","status":"FAILED"}]}`))
	if err := <-failed.ready; err == nil || !strings.Contains(err.Error(), "file:///project/") {
		t.Fatalf("missing folder import failure: %v", err)
	}
}

func TestKotlinOverlaysUseLanguageIDs(t *testing.T) {
	var output bytes.Buffer
	s := &session{kotlin: true, projectRoots: []string{"/project"}, rpc: &rpc{out: &output}, documents: make(map[string]overlay)}
	if err := s.syncDocuments(map[string]string{"/project/A.java": "class A {}", "/project/B.kt": "class B", "/elsewhere/C.kt": "class C"}); err != nil {
		t.Fatal(err)
	}
	reader := &rpc{in: bufio.NewReader(&output)}
	languages := make(map[string]string)
	for range 2 {
		message, err := reader.receive()
		if err != nil {
			t.Fatal(err)
		}
		var params struct {
			TextDocument struct {
				URI        string
				LanguageID string
			}
		}
		if err := json.Unmarshal(message.Params, &params); err != nil {
			t.Fatal(err)
		}
		languages[params.TextDocument.URI] = params.TextDocument.LanguageID
	}
	if languages["file:///project/A.java"] != "java" || languages["file:///project/B.kt"] != "kotlin" {
		t.Fatalf("incorrect document languages: %v", languages)
	}
	if output.Len() != 0 || len(s.documents) != 2 {
		t.Fatal("overlay outside selected workspace was opened")
	}
}

func TestArchiveSourceExtractionPreservesText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sources with spaces.jar")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	entry, err := archive.Create("pkg/Example.kt")
	if err != nil {
		t.Fatal(err)
	}
	source := "// 😀\nfun example() = 1\n"
	if _, err := io.WriteString(entry, source); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	uri := "jar:" + fileURI(path) + "!/pkg/Example.kt"
	name, content, err := archiveSource(uri)
	if err != nil || name != "Example.kt" || string(content) != source {
		t.Fatalf("archive source: %s %q %v", name, content, err)
	}
	s := &session{sourceCache: t.TempDir()}
	locations, err := s.materializeLocations(context.Background(), []location{{URI: uri, Range: sourceRange{Start: position{Line: 1, Character: 4}}}})
	if err != nil || len(locations) != 1 || locations[0].Range.Start != (position{Line: 1, Character: 4}) {
		t.Fatalf("materialized source range: %+v %v", locations, err)
	}
	for _, member := range []string{"../Escape.kt", "pkg/Example.class"} {
		if _, _, err := archiveSource("jar:" + fileURI(path) + "!/" + member); err == nil {
			t.Errorf("unsupported archive target accepted: %s", member)
		}
	}
}

func TestKotlinStandaloneImportErrorIsActionable(t *testing.T) {
	s := &session{kotlin: true, ready: make(chan error, 1)}
	s.notification("intellij/workspaceImportState", json.RawMessage(`{"phase":"FINISHED","folders":[{"folderUri":"file:///project/","status":"BLOCKED","message":"noBuildSystemFound"}]}`))
	if err := <-s.ready; err == nil || !strings.Contains(err.Error(), "Maven or Gradle") {
		t.Fatalf("unhelpful standalone error: %v", err)
	}
}

func TestJDTServerRuntimePreservesProjectJavaHome(t *testing.T) {
	root := t.TempDir()
	serverHome, projectHome := filepath.Join(root, "server"), filepath.Join(root, "project")
	for _, home := range []string{serverHome, projectHome} {
		path := filepath.Join(home, "bin", "java")
		putFile(t, path, "#!/bin/sh\nexit 0\n")
		if err := os.Chmod(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("JDTLS_JAVA_HOME", serverHome)
	t.Setenv("JAVA_HOME", projectHome)
	env, java, err := javaEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if java != filepath.Join(serverHome, "bin", "java") {
		t.Fatalf("wrong server runtime: %s", java)
	}
	projectPreserved := false
	for _, entry := range env {
		if entry == "JAVA_HOME="+projectHome {
			projectPreserved = true
		}
	}
	if !projectPreserved {
		t.Fatal("language-server override replaced the project's build JDK")
	}
}

func TestJavaGradleImportUsesProjectRuntime(t *testing.T) {
	t.Setenv("JAVA_HOME", "/opt/project-jdk")
	settings := javaSettings()["java"].(map[string]any)
	gradle := settings["import"].(map[string]any)["gradle"].(map[string]any)
	java := gradle["java"].(map[string]any)
	if java["home"] != "/opt/project-jdk" {
		t.Fatalf("Gradle import runtime = %v", java["home"])
	}
	t.Setenv("JAVA_HOME", "")
	settings = javaSettings()["java"].(map[string]any)
	gradle = settings["import"].(map[string]any)["gradle"].(map[string]any)
	if _, ok := gradle["java"]; ok {
		t.Fatal("empty JAVA_HOME must not override Buildship's JVM")
	}
}

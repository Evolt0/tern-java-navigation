package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type overlay struct {
	text    string
	version int
}

type session struct {
	root         string
	projectRoots []string
	kotlin       bool
	cmd          *exec.Cmd
	stdin        io.WriteCloser
	rpc          *rpc
	watcher      *fileWatcher
	documents    map[string]overlay
	symbolIndex  map[string][]projectSymbol
	classFiles   map[string]string
	sourceCache  string
	ready        chan error
	readyOnce    sync.Once
	closeOnce    sync.Once
	stderr       boundedLog
}

func newSession(ctx context.Context, root string, q query) (_ *session, err error) {
	s := &session{root: root, projectRoots: []string{root}, documents: make(map[string]overlay)}
	defer func() {
		if err != nil {
			s.close()
			err = fmt.Errorf("%w\n%s", err, s.stderr.String())
		}
	}()
	s.watcher, err = newFileWatcher(ctx, root)
	if err != nil {
		return nil, err
	}
	if err := s.startBackend(ctx, kotlinFile(q.Path)); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *session) settings() map[string]any {
	if s.kotlin {
		return map[string]any{}
	}
	return javaSettings()
}

func (s *session) startBackend(ctx context.Context, kotlin bool) error {
	var launch javaLaunch
	var err error
	if kotlin {
		launch, err = discoverKotlinLS(s.root)
	} else {
		launch, err = discoverJDTLS(s.root)
	}
	if err != nil {
		return err
	}
	s.kotlin = kotlin
	s.ready = make(chan error, 1)
	s.sourceCache = launch.sources
	s.cmd = exec.Command(launch.executable, launch.args...)
	s.cmd.Dir, s.cmd.Env, s.cmd.Stderr = s.root, launch.env, &s.stderr
	s.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	s.cmd.WaitDelay = time.Second
	stdout, err := s.cmd.StdoutPipe()
	if err != nil {
		return err
	}
	s.stdin, err = s.cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := s.cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", filepath.Base(launch.executable), err)
	}
	// Initialization belongs to this request; the successful server outlives it.
	stop := context.AfterFunc(ctx, func() { s.watcher.cancel(); s.kill(); _ = s.stdin.Close() })
	defer stop()
	s.rpc = newRPC(stdout, s.stdin, s.watcher.register, rpcHandlers{notify: s.notification, request: s.serverRequest})
	uri := fileURI(s.root)
	options := map[string]any{}
	if !kotlin {
		options = map[string]any{"settings": s.settings(), "workspaceFolders": []string{uri}, "extendedClientCapabilities": map[string]any{"classFileContentsSupport": true}}
	}
	result, err := s.rpc.call(ctx, "initialize", map[string]any{
		"processId": os.Getpid(), "rootUri": uri,
		"workspaceFolders": []any{map[string]any{"uri": uri, "name": filepath.Base(s.root)}},
		"capabilities": map[string]any{
			"general":      map[string]any{"positionEncodings": []string{"utf-16"}},
			"workspace":    map[string]any{"configuration": true, "workspaceFolders": true, "didChangeWatchedFiles": map[string]any{"dynamicRegistration": true, "relativePatternSupport": true}},
			"textDocument": map[string]any{"definition": map[string]any{"linkSupport": true}, "documentSymbol": map[string]any{"hierarchicalDocumentSymbolSupport": true}},
		},
		"initializationOptions": options,
	})
	if err != nil {
		return fmt.Errorf("initialize language server: %w", err)
	}
	var init struct {
		Capabilities struct{ PositionEncoding string }
	}
	if err := json.Unmarshal(result, &init); err != nil {
		return err
	}
	if enc := init.Capabilities.PositionEncoding; enc != "" && enc != "utf-16" {
		return fmt.Errorf("unsupported language-server position encoding %q", enc)
	}
	if err := s.rpc.notify("initialized", map[string]any{}); err != nil {
		return err
	}
	if !kotlin {
		if err := s.rpc.notify("workspace/didChangeConfiguration", map[string]any{"settings": s.settings()}); err != nil {
			return err
		}
	}
	// Both servers import Maven/Gradle asynchronously after initialize replies.
	select {
	case err := <-s.ready:
		if err != nil {
			return err
		}
	case <-ctx.Done():
		return fmt.Errorf("waiting for language-server project import: %w", ctx.Err())
	case <-s.rpc.done:
		return errors.New("language server exited before project import became ready")
	}
	return ctx.Err()
}

func (s *session) notification(method string, raw json.RawMessage) {
	if s.kotlin {
		if method == "intellij/workspaceImportState" {
			var state struct {
				Phase   string
				Folders []struct {
					FolderURI string
					Status    string
					Message   string
				}
			}
			if err := json.Unmarshal(raw, &state); err != nil || state.Phase != "FINISHED" {
				return
			}
			var failure error
			for _, folder := range state.Folders {
				if folder.Status != "SUCCESS" {
					if folder.Message == "noBuildSystemFound" {
						failure = fmt.Errorf("Kotlin navigation requires a Maven or Gradle project: no build system found for %s", folder.FolderURI)
					} else {
						failure = fmt.Errorf("Kotlin project import failed for %s: %s %s", folder.FolderURI, folder.Status, folder.Message)
					}
					break
				}
			}
			s.readyOnce.Do(func() { s.ready <- failure })
		} else if method == "intellij/workspaceImportStatus" {
			var status struct{ BlockedFolders []json.RawMessage }
			if err := json.Unmarshal(raw, &status); err == nil && len(status.BlockedFolders) != 0 {
				s.readyOnce.Do(func() {
					s.ready <- fmt.Errorf("Kotlin project import is blocked (a Maven or Gradle project is required): %s", raw)
				})
			}
		}
		return
	}
	if method != "language/status" {
		return
	}
	var status struct {
		Type    string
		Message string
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		return
	}
	switch status.Type {
	case "Started":
		s.readyOnce.Do(func() { s.ready <- nil })
	case "Error":
		s.readyOnce.Do(func() { s.ready <- fmt.Errorf("JDT LS project initialization failed: %s", status.Message) })
	}
}

func (s *session) serverRequest(method string, raw json.RawMessage) (any, error) {
	switch method {
	case "workspace/workspaceFolders":
		return []any{map[string]any{"uri": fileURI(s.root), "name": filepath.Base(s.root)}}, nil
	case "workspace/configuration":
		var params struct{ Items []struct{ Section string } }
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, err
		}
		values := make([]any, len(params.Items))
		for i, item := range params.Items {
			var value any = s.settings()
			if item.Section != "" {
				for _, key := range strings.Split(item.Section, ".") {
					object, ok := value.(map[string]any)
					if !ok {
						value = nil
						break
					}
					value = object[key]
				}
			}
			values[i] = value
		}
		return values, nil
	}
	return nil, fmt.Errorf("unsupported language-server request %q", method)
}

func (s *session) close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		s.closeServer()
		if s.watcher != nil {
			s.watcher.close()
		}
	})
}

func (s *session) closeServer() {
	if s.cmd != nil && s.cmd.Process != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		stop := context.AfterFunc(ctx, func() {
			s.kill()
			if s.stdin != nil {
				_ = s.stdin.Close()
			}
		})
		// Give both JVM servers a chance to flush workspace/index state and
		// release resources before closing the transport. A wedged server is
		// still bounded by the deadline and killed only within our process group.
		if s.rpc != nil {
			if _, err := s.rpc.call(ctx, "shutdown", nil); err == nil {
				_ = s.rpc.notify("exit", nil)
			}
		}
		if s.stdin != nil {
			_ = s.stdin.Close()
		}
		_ = s.cmd.Wait()
		stop()
		cancel()
	} else if s.stdin != nil {
		_ = s.stdin.Close()
	}
	// cmd.Wait already proves the real server exited, which closes its stdout
	// and ends the RPC reader. Do not require that from callers that provide
	// only the child process (for example tests or a launcher wrapper).
	s.cmd, s.stdin, s.rpc = nil, nil, nil
}

func (s *session) kill() {
	if s.cmd != nil && s.cmd.Process != nil {
		_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
		_ = s.cmd.Process.Kill()
	}
}

// Startup diagnostics remain bounded even when a long-lived server is noisy.
type boundedLog struct {
	mu   sync.Mutex
	data []byte
}

func (b *boundedLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	const limit = 8192
	if len(p) >= limit {
		b.data = append(b.data[:0], p[len(p)-limit:]...)
		return n, nil
	}
	if excess := len(b.data) + len(p) - limit; excess > 0 {
		copy(b.data, b.data[excess:])
		b.data = b.data[:len(b.data)-excess]
	}
	b.data = append(b.data, p...)
	return n, nil
}

func (b *boundedLog) String() string { b.mu.Lock(); defer b.mu.Unlock(); return string(b.data) }

func (s *session) syncDocuments(documents map[string]string) error {
	for name := range s.documents {
		if _, ok := documents[name]; !ok {
			if err := s.rpc.notify("textDocument/didClose", map[string]any{"textDocument": map[string]any{"uri": fileURI(name)}}); err != nil {
				return err
			}
			delete(s.documents, name)
			delete(s.symbolIndex, name)
		}
	}
	for name, text := range documents {
		included, scopeErr := projectSymbolDirectory(filepath.Dir(name), s.projectRoots)
		if scopeErr != nil {
			return scopeErr
		}
		if !included {
			continue
		}
		language := "java"
		if filepath.Ext(name) != ".java" {
			if !s.kotlin || !sourceFile(name) {
				continue
			}
			language = "kotlin"
		}
		previous, open := s.documents[name]
		if open && previous.text == text {
			continue
		}
		version := previous.version + 1
		var err error
		if open {
			err = s.rpc.notify("textDocument/didChange", map[string]any{"textDocument": map[string]any{"uri": fileURI(name), "version": version}, "contentChanges": []any{map[string]any{"text": text}}})
		} else {
			err = s.rpc.notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{"uri": fileURI(name), "languageId": language, "version": version, "text": text}})
		}
		if err != nil {
			return err
		}
		s.documents[name] = overlay{text: text, version: version}
		delete(s.symbolIndex, name)
	}
	return nil
}

func (s *session) navigate(ctx context.Context, q query, pos position) (_ []destination, err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("%w\n%s", err, s.stderr.String())
		}
	}()
	changes, builds, err := s.watcher.changes(ctx)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { s.watcher.cancel(); s.kill(); _ = s.stdin.Close() })
	defer stop()
	if builds {
		clear(s.symbolIndex)
		clear(s.classFiles)
	}
	for _, change := range changes {
		u, err := url.Parse(change.URI)
		if err != nil {
			return nil, err
		}
		delete(s.symbolIndex, filepath.FromSlash(u.Path))
	}
	if len(changes) != 0 {
		if err := s.rpc.notify("workspace/didChangeWatchedFiles", map[string]any{"changes": changes}); err != nil {
			return nil, err
		}
	}
	if err := s.syncDocuments(q.Documents); err != nil {
		return nil, err
	}
	if q.Action == "symbols" {
		return s.symbols(ctx, q)
	}
	if q.Action == "warmup" {
		_, err := s.rpc.call(ctx, "textDocument/documentSymbol", map[string]any{"textDocument": map[string]any{"uri": fileURI(q.Path)}})
		return []destination{}, err
	}
	params := map[string]any{"textDocument": map[string]any{"uri": fileURI(q.Path)}, "position": pos}
	out := make([]destination, 0)
	seen := make(map[string]bool)
	for _, method := range []string{"definition", "references"} {
		kind := "definition"
		if method == "references" {
			params["context"] = map[string]any{"includeDeclaration": false}
			kind = "reference"
		}
		result, err := s.rpc.call(ctx, "textDocument/"+method, params)
		if err != nil {
			return nil, err
		}
		locations, err := decodeLocations(result)
		if err != nil {
			return nil, err
		}
		locations, err = s.materializeLocations(ctx, locations)
		if err != nil {
			return nil, err
		}
		targets, err := destinations(locations, kind, q, seen)
		if err != nil {
			return nil, err
		}
		out = append(out, targets...)
	}
	return out, nil
}

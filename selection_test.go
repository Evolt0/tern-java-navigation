package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrepareQueryUsesSelectionByteOffsets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Selected.java")
	text := "class Selected {\r\n\t// 中😀 é\r\n\tvoid 目标函数() {}\r\n\tvoid caller() { /* 中😀é */ 目标函数(); }\r\n}\r\n"
	putFile(t, path, text)
	start := strings.LastIndex(text, "目标函数")
	end := start + len("目标函数")
	want := position{Line: 3, Character: 29}
	for _, tc := range []struct {
		name      string
		selection editorSelection
	}{
		{"forward", editorSelection{start, end}},
		{"reverse", editorSelection{end, start}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := query{Path: path, Line: 4, Col: 30, TabWidth: 4, Selection: &tc.selection, Documents: map[string]string{path: text}}
			_, got, err := prepareQuery(q)
			if err != nil || got != want {
				t.Fatalf("position = %+v, %v; want %+v", got, err, want)
			}
		})
	}
	collapsed := editorSelection{end, end}
	q := query{Path: path, Line: 4, Col: 9, TabWidth: 4, Selection: &collapsed, Documents: map[string]string{path: text}}
	if _, got, err := prepareQuery(q); err != nil || got != (position{Line: 3, Character: 5}) {
		t.Fatalf("collapsed selection must use the caret: %+v, %v", got, err)
	}
	for _, selection := range []editorSelection{
		{-1, end}, {start, len(text) + 1}, {start + 1, end}, {end - 1, start},
	} {
		q := query{Path: path, Line: 4, Col: 30, TabWidth: 4, Selection: &selection, Documents: map[string]string{path: text}}
		if _, _, err := prepareQuery(q); err == nil {
			t.Errorf("accepted invalid UTF-8 selection %+v", selection)
		}
	}
}

func TestJavaSessionShutsDownBeforeClosingTransport(t *testing.T) {
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()
	t.Cleanup(func() { clientRead.Close(); serverWrite.Close(); serverRead.Close(); clientWrite.Close() })
	received := make(chan string, 4)
	go func() {
		server := &rpc{in: bufio.NewReader(serverRead), out: serverWrite}
		for {
			message, err := server.receive()
			if err != nil {
				return
			}
			received <- message.Method
			if message.ID == nil {
				continue
			}
			var result any
			if err := server.send(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": result}); err != nil {
				return
			}
		}
	}()
	cmd := exec.Command("/bin/sh", "-c", "cat >/dev/null")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	s := &session{cmd: cmd, stdin: stdin, rpc: newRPC(clientRead, clientWrite, nil)}
	started := time.Now()
	s.close()
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("graceful close took %v", elapsed)
	}
	var methods []string
	deadline := time.After(time.Second)
	for len(methods) < 2 {
		select {
		case method := <-received:
			methods = append(methods, method)
		case <-deadline:
			t.Fatalf("server did not receive shutdown then exit: %v", methods)
		}
	}
	if methods[0] != "shutdown" || methods[1] != "exit" {
		t.Fatalf("server did not receive shutdown then exit: %v", methods)
	}
	if s.cmd != nil || s.stdin != nil || s.rpc != nil {
		t.Fatal("closed session retained its transport")
	}
}

func TestSelectionProtocolJSON(t *testing.T) {
	data := []byte(`{"path":"/p/A.java","line":2,"col":3,"tabWidth":4,"selection":{"anchor":10,"head":14},"documents":{}}`)
	var q query
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&q); err != nil {
		t.Fatal(err)
	}
	if q.Selection == nil || q.Selection.Anchor != 10 || q.Selection.Head != 14 {
		t.Fatalf("selection not decoded: %+v", q)
	}
	_ = os.RemoveAll(t.TempDir())
}

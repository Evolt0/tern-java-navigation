package main

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestTrailingJSONWhitespaceDoesNotCancelRequest(t *testing.T) {
	server, client := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { cancelOnDisconnect(server, cancel); close(done) }()
	defer func() { client.Close(); server.Close(); <-done }()
	if err := client.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	// A decoder may stop immediately after '}', leaving one or more trailing
	// whitespace chunks in the socket while the language server is working.
	for _, trailing := range []string{"\n", " \r\n"} {
		if _, err := client.Write([]byte(trailing)); err != nil {
			t.Fatalf("watchdog stopped draining the open connection: %v", err)
		}
		if ctx.Err() != nil {
			t.Fatal("valid JSON whitespace cancelled an active navigation request")
		}
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("disconnected client did not cancel its language-server request")
	}
}

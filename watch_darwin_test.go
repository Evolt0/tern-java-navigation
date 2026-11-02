//go:build darwin

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func openDescriptors(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Skip("cannot inspect /dev/fd on this host")
	}
	return len(entries)
}

// The kqueue backend allocated one descriptor per file inside every watched
// directory. Native FSEvents must stay bounded no matter how many irrelevant
// files share the tree.
func TestDarwinWatcherDescriptorsStayBounded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root, err := canonical(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	many := filepath.Join(root, "docs")
	if err := os.Mkdir(many, 0700); err != nil {
		t.Fatal(err)
	}
	for i := range 600 {
		if err := os.WriteFile(filepath.Join(many, fmt.Sprintf("note-%03d.txt", i)), []byte("ignored"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	putFile(t, filepath.Join(root, "src", "A.java"), "class A {}")
	runtime.GC()
	before := openDescriptors(t)
	watcher, err := newFileWatcher(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	during := openDescriptors(t)
	if delta := during - before; delta > 128 {
		watcher.close()
		t.Fatalf("%d descriptors opened for %d irrelevant files", delta, 600)
	}
	watcher.close()
	runtime.GC()
	if delta := openDescriptors(t) - before; delta > 32 {
		t.Fatalf("%d descriptors retained after watcher close", delta)
	}
}

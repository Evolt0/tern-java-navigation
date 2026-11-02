package main

import (
	"os"
	"path/filepath"
	"testing"
)

func putFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestUnicodeCoordinates(t *testing.T) {
	text := "class Demo {\r\n\t/* 中😀é */ target();\r\n}"
	pos, err := lspPosition(text, 2, 17, 4)
	if err != nil || pos != (position{Line: 1, Character: 13}) {
		t.Fatalf("position=%+v err=%v", pos, err)
	}
	if col, err := ternColumn(text, pos, 4); err != nil || col != 17 {
		t.Fatalf("col=%d err=%v", col, err)
	}
	if _, err := ternColumn(text, position{Line: 1, Character: 6}, 4); err == nil {
		t.Fatal("accepted half a surrogate pair")
	}
	if _, err := lspPosition(text, 2, 2, 4); err == nil {
		t.Fatal("accepted inside a tab")
	}
	if _, err := lspPosition(text, 20, 1, 4); err == nil {
		t.Fatal("accepted nonexistent line")
	}
}

func TestWorkspaceMavenReactorMembership(t *testing.T) {
	root, err := canonical(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "nested", "child")
	putFile(t, filepath.Join(child, "pom.xml"), "<project/>")
	file := filepath.Join(child, "src", "main", "java", "Demo.java")
	putFile(t, filepath.Join(root, "pom.xml"), "<project><modules><module>unrelated</module></modules></project>")
	if got := workspace(file); got != child {
		t.Fatalf("unrelated reactor absorbed module: %s", got)
	}
	putFile(t, filepath.Join(root, "pom.xml"), "<project><modules><module>nested/child</module></modules></project>")
	if got := workspace(file); got != root {
		t.Fatalf("reactor missing module: %s", got)
	}
}

func TestWorkspaceGradleSettings(t *testing.T) {
	root := t.TempDir()
	putFile(t, filepath.Join(root, "settings.gradle.kts"), "include(\":app\")")
	putFile(t, filepath.Join(root, "app", "build.gradle.kts"), "plugins { java }")
	if got := workspace(filepath.Join(root, "app", "src", "Main.kt")); got != root {
		t.Fatalf("got %s", got)
	}
}

func TestPreviewUsesOverlayAndDeduplicates(t *testing.T) {
	file, err := canonical(filepath.Join(t.TempDir(), "Demo.java"))
	if err != nil {
		t.Fatal(err)
	}
	putFile(t, file, "class Demo {}")
	loc := location{URI: fileURI(file), Range: sourceRange{Start: position{Line: 1, Character: 1}}}
	q := query{TabWidth: 4, Documents: map[string]string{file: "class Demo {\n\tvoid target() {} // live  preview\n}"}}
	seen := make(map[string]bool)
	got, err := destinations([]location{loc, loc}, "definition", q, seen)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Col != 5 || got[0].Line != 2 || got[0].Preview != "void target() {} // live  preview" {
		t.Fatalf("destinations=%+v", got)
	}
	refs, err := destinations([]location{loc}, "reference", q, seen)
	if err != nil || len(refs) != 0 {
		t.Fatalf("definition repeated as reference: %+v %v", refs, err)
	}
}

func TestProjectSymbolsExcludeOutsideAndOutputs(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		dir  string
		want bool
	}{
		{filepath.Join(root, "src", "main", "java"), true},
		{filepath.Join(root, "app", "src", "main", "kotlin"), true},
		{filepath.Join(root, "target", "generated-sources"), false},
		{filepath.Join(root, ".gradle"), false},
		{root + "-outside", false},
	} {
		got, err := projectSymbolDirectory(tc.dir, []string{root})
		if err != nil || got != tc.want {
			t.Errorf("%s: got %v err=%v", tc.dir, got, err)
		}
	}
}

func TestOutlinePreservesNestedDeclarationSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Demo.java")
	classPos := sourceRange{Start: position{Line: 2, Character: 6}}
	methodPos := sourceRange{Start: position{Line: 3, Character: 9}}
	outline := []documentSymbol{{Name: "example", Kind: 4, Children: []documentSymbol{{Name: "Demo", Kind: 5, SelectionRange: &classPos, Children: []documentSymbol{{Name: "findCustomer(int)", Kind: 6, SelectionRange: &methodPos}}}}}}
	got, err := flattenSymbols(outline, path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].name != "Demo" || got[1].name != "findCustomer(int)" || got[1].location.Range.Start != methodPos.Start {
		t.Fatalf("outline=%+v", got)
	}
	if symbolRank(got[1].name, "findcustomer") != 0 || symbolRank(got[1].name, "fc") != 3 || symbolRank(got[1].name, "missing") != -1 {
		t.Fatal("incorrect declaration-name matching")
	}
}

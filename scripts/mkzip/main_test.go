package main

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestRun(t *testing.T) {
	root := t.TempDir()
	write := func(rel string, content []byte, mode os.FileMode) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, content, mode); err != nil {
			t.Fatal(err)
		}
	}
	write("b.txt", []byte("two"), 0o644)
	write("a.txt", []byte("one"), 0o644)
	write("third-party/LIC", []byte("mit"), 0o644)
	write("sub/app.exe", []byte("bin"), 0o755)

	out := filepath.Join(t.TempDir(), "out.zip")
	if err := run(root, out); err != nil {
		t.Fatal(err)
	}

	zr, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()

	var names []string
	want := map[string]string{
		filepath.Base(root) + "/a.txt":           "one",
		filepath.Base(root) + "/b.txt":           "two",
		filepath.Base(root) + "/sub/app.exe":     "bin",
		filepath.Base(root) + "/third-party/LIC": "mit",
	}
	for _, f := range zr.File {
		names = append(names, f.Name)
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != want[f.Name] {
			t.Errorf("%s = %q, want %q", f.Name, data, want[f.Name])
		}
		delete(want, f.Name)
	}
	if len(want) != 0 {
		t.Errorf("missing entries: %v", want)
	}
	// Stable, sorted order.
	for i := 1; i < len(names); i++ {
		if names[i-1] >= names[i] {
			t.Fatalf("entries not sorted: %v", names)
		}
	}
}

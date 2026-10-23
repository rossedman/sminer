package fsutil

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWriteFileAtomic(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "a", "b", "f.txt") // parents created

	if err := WriteFileAtomic(path, []byte("one"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "two" {
		t.Fatalf("content = %q, %v", got, err)
	}
	st, _ := os.Stat(path)
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Errorf("perm = %v", st.Mode().Perm())
	}
	ents, _ := os.ReadDir(filepath.Dir(path))
	if len(ents) != 1 {
		t.Errorf("temp files left behind: %v", ents)
	}
}

func TestWriteFileAtomicErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	file := filepath.Join(dir, "plain")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ro := filepath.Join(dir, "ro")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}

	tests := map[string]string{
		"parent is a file":      filepath.Join(file, "child"),
		"rename onto directory": target,
	}
	if os.Geteuid() != 0 {
		tests["read-only dir"] = filepath.Join(ro, "f")
	}
	for name, path := range tests {
		t.Run(name, func(t *testing.T) {
			err := WriteFileAtomic(path, []byte("data"), 0o600)
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
	// Failure leaves no temp files and the existing directory intact.
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}

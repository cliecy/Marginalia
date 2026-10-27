// Package vaulttest builds test vaults: the shared fixture the golden files
// were generated from, and small ad-hoc vaults.
package vaulttest

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// FixturePath is the location of the fixture description written by the
// golden generator (internal/vault/testdata/fixture.json).
func FixturePath() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "testdata", "fixture.json")
}

// FixtureFiles returns the fixture's files (vault path -> content).
func FixtureFiles(t testing.TB) map[string][]byte {
	t.Helper()
	data, err := os.ReadFile(FixturePath())
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var raw struct {
		Files map[string]string `json:"files"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	files := make(map[string][]byte, len(raw.Files))
	for rel, b64 := range raw.Files {
		content, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			t.Fatalf("decode fixture %s: %v", rel, err)
		}
		files[rel] = content
	}
	return files
}

// Fixture writes the golden fixture vault into a fresh temporary folder and
// returns its path.
func Fixture(t testing.TB) string {
	t.Helper()
	return Write(t, FixtureFiles(t))
}

// Write creates a vault folder containing files and returns its path.
func Write(t testing.TB, files map[string][]byte) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "notes")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// Strings is Write for text files.
func Strings(t testing.TB, files map[string]string) string {
	t.Helper()
	m := make(map[string][]byte, len(files))
	for rel, s := range files {
		m[rel] = []byte(s)
	}
	return Write(t, m)
}

// Golden reads a JSON golden file into v.
func Golden(t testing.TB, path string, v any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("parse golden %s: %v", path, err)
	}
}

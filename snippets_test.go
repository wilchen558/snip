package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("SNIP_DIR", t.TempDir())
	s, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func mustWrite(t *testing.T, s *Store, name, body string) {
	t.Helper()
	if err := s.Write(name, body); err != nil {
		t.Fatalf("Write(%q): %v", name, err)
	}
}

func TestNamesSortedAndFiltered(t *testing.T) {
	s := newTestStore(t)
	mustWrite(t, s, "zeta", "Z")
	mustWrite(t, s, "alpha", "A")
	// Non-.md files and directories must not appear as snippets.
	os.WriteFile(filepath.Join(s.dir, "notes.txt"), []byte("x"), 0o644)
	os.Mkdir(filepath.Join(s.dir, "sub.md"), 0o755)

	got, err := s.Names()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"alpha", "zeta"}; !slices.Equal(got, want) {
		t.Errorf("Names() = %v, want %v", got, want)
	}
}

func TestSummaryIsFirstLine(t *testing.T) {
	s := newTestStore(t)
	mustWrite(t, s, "multi", "  first line  \nsecond line\nthird")
	if got, want := s.Summary("multi"), "first line"; got != want {
		t.Errorf("Summary() = %q, want %q", got, want)
	}
	if got := s.Summary("missing"); got != "" {
		t.Errorf("Summary(missing) = %q, want empty", got)
	}
}

func TestEnableDisableToggle(t *testing.T) {
	s := newTestStore(t)
	mustWrite(t, s, "a", "A")
	mustWrite(t, s, "b", "B")

	if on, _ := s.Enabled(); len(on) != 0 {
		t.Fatalf("fresh store should have nothing enabled, got %v", on)
	}
	if err := s.Enable("a", "b"); err != nil {
		t.Fatal(err)
	}
	if on, _ := s.Enabled(); !slices.Equal(on, []string{"a", "b"}) {
		t.Errorf("after Enable = %v", on)
	}
	// Enabling twice must not duplicate.
	if err := s.Enable("a"); err != nil {
		t.Fatal(err)
	}
	if on, _ := s.Enabled(); !slices.Equal(on, []string{"a", "b"}) {
		t.Errorf("re-Enable duplicated: %v", on)
	}
	if err := s.Disable("a"); err != nil {
		t.Fatal(err)
	}
	if on, _ := s.Enabled(); !slices.Equal(on, []string{"b"}) {
		t.Errorf("after Disable = %v", on)
	}

	nowOn, err := s.Toggle("a")
	if err != nil || !nowOn {
		t.Errorf("Toggle(a) = %v, %v; want true, nil", nowOn, err)
	}
	nowOn, _ = s.Toggle("a")
	if nowOn {
		t.Error("second Toggle(a) should report off")
	}
}

func TestEnabledSkipsDeletedAndComments(t *testing.T) {
	s := newTestStore(t)
	mustWrite(t, s, "live", "L")
	// A manifest referencing a snippet that no longer exists must not break
	// the hook; the stale entry is simply ignored.
	os.WriteFile(s.manifestPath(), []byte("live\n# a comment\n\nghost\nlive\n"), 0o644)

	on, err := s.Enabled()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(on, []string{"live"}) {
		t.Errorf("Enabled() = %v, want [live]", on)
	}
}

func TestComposeJoinsInManifestOrder(t *testing.T) {
	s := newTestStore(t)
	mustWrite(t, s, "one", "First body.")
	mustWrite(t, s, "two", "Second body.")
	mustWrite(t, s, "blank", "   \n  ")

	if err := s.SetEnabled([]string{"two", "one", "blank"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Compose()
	if err != nil {
		t.Fatal(err)
	}
	// Manifest order wins over alphabetical, and whitespace-only snippets
	// contribute nothing rather than a stray blank paragraph.
	if want := "Second body.\n\nFirst body."; got != want {
		t.Errorf("Compose() = %q, want %q", got, want)
	}
}

func TestComposeEmptyWhenNothingEnabled(t *testing.T) {
	s := newTestStore(t)
	mustWrite(t, s, "a", "A")
	got, err := s.Compose()
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("Compose() = %q, want empty", got)
	}
}

func TestRemoveAllDropsFromManifest(t *testing.T) {
	s := newTestStore(t)
	mustWrite(t, s, "a", "A")
	mustWrite(t, s, "b", "B")
	if err := s.Enable("a", "b"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveAll("a"); err != nil {
		t.Fatal(err)
	}
	if s.Exists("a") {
		t.Error("file still on disk after RemoveAll")
	}
	if on, _ := s.Enabled(); !slices.Equal(on, []string{"b"}) {
		t.Errorf("Enabled() = %v, want [b]", on)
	}
}

func TestSetEnabledDedupesAndPreservesOrder(t *testing.T) {
	s := newTestStore(t)
	for _, n := range []string{"a", "b", "c"} {
		mustWrite(t, s, n, strings.ToUpper(n))
	}
	if err := s.SetEnabled([]string{"c", "a", "c", "", "b"}); err != nil {
		t.Fatal(err)
	}
	if on, _ := s.Enabled(); !slices.Equal(on, []string{"c", "a", "b"}) {
		t.Errorf("Enabled() = %v, want [c a b]", on)
	}
}

// TestConcurrentEnableKeepsEveryUpdate is the regression test for the
// read-modify-write race: several sessions toggling at once must not lose one
// another's writes.
func TestConcurrentEnableKeepsEveryUpdate(t *testing.T) {
	s := newTestStore(t)
	const n = 12
	for i := range n {
		mustWrite(t, s, fmt.Sprintf("s%02d", i), "body")
	}
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := s.Enable(fmt.Sprintf("s%02d", i)); err != nil {
				t.Errorf("Enable: %v", err)
			}
		}(i)
	}
	wg.Wait()

	on, err := s.Enabled()
	if err != nil {
		t.Fatal(err)
	}
	if len(on) != n {
		t.Errorf("kept %d of %d concurrent enables: %v", len(on), n, on)
	}
}

func TestLockIsReleasedAfterMutate(t *testing.T) {
	s := newTestStore(t)
	mustWrite(t, s, "a", "A")
	if err := s.Enable("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.manifestPath() + ".lock"); !os.IsNotExist(err) {
		t.Error("lock file outlived the mutation")
	}
}

func TestStaleLockIsReclaimed(t *testing.T) {
	s := newTestStore(t)
	mustWrite(t, s, "a", "A")
	lock := s.manifestPath() + ".lock"
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// Backdate past lockStale, as a killed process would leave it.
	old := timeLongAgo()
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	if err := s.Enable("a"); err != nil {
		t.Fatalf("stale lock was not reclaimed: %v", err)
	}
	if on, _ := s.Enabled(); !slices.Equal(on, []string{"a"}) {
		t.Errorf("Enabled() = %v", on)
	}
}

func TestWriteFileAtomicReplacesContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := writeFileAtomic(path, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(path, []byte("two")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "two" {
		t.Errorf("got %q, %v; want \"two\"", b, err)
	}
	// No temp files may survive a successful write.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("leftover files: %d", len(entries))
	}
}

func timeLongAgo() time.Time { return time.Now().Add(-10 * lockStale) }

// TestNamesCannotEscapeTheStore is the regression test for a traversal bug:
// `snip rm ../victim/notes` deleted a file outside the snippet directory,
// because only `add` validated names.
func TestNamesCannotEscapeTheStore(t *testing.T) {
	s := newTestStore(t)
	outside := filepath.Join(filepath.Dir(s.dir), "victim.md")
	if err := os.WriteFile(outside, []byte("important"), 0o644); err != nil {
		t.Fatal(err)
	}

	escapes := []string{"../victim", "sub/../../victim", `..\victim`, "..", ".enabled", ""}
	for _, name := range escapes {
		if s.Exists(name) {
			t.Errorf("Exists(%q) = true; a traversing name must never resolve", name)
		}
		if err := s.RemoveAll(name); err == nil {
			t.Errorf("RemoveAll(%q) succeeded; want an error", name)
		}
		if _, err := s.Body(name); err == nil {
			t.Errorf("Body(%q) succeeded; want an error", name)
		}
		if err := s.Write(name, "x"); err == nil {
			t.Errorf("Write(%q) succeeded; want an error", name)
		}
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("file outside the store was touched: %v", err)
	}
}

// TestRemoveAllValidatesBeforeDeleting guards against a partial delete when a
// later argument is bad.
func TestRemoveAllValidatesBeforeDeleting(t *testing.T) {
	s := newTestStore(t)
	mustWrite(t, s, "keep", "K")
	if err := s.RemoveAll("keep", "../escape"); err == nil {
		t.Fatal("RemoveAll accepted a traversing name")
	}
	if !s.Exists("keep") {
		t.Error("a valid snippet was deleted before the invalid name was rejected")
	}
}

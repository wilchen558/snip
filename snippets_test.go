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

// manifestUnsafeNames are names the .enabled manifest cannot carry: it is one
// name per line, lines are trimmed before they are matched, and "#" introduces
// a comment. A name that does not survive that round trip could once be
// created and "enabled" without ever taking effect.
var manifestUnsafeNames = []string{"#urgent", " lead", "trail ", "a\nb", "\tweird"}

// TestManifestUnsafeNamesAreRejected is the regression test for `snip add
// '#urgent' …` succeeding: the file appeared, but nothing could ever enable it.
func TestManifestUnsafeNamesAreRejected(t *testing.T) {
	s := newTestStore(t)
	for _, name := range manifestUnsafeNames {
		// Plant the file directly, as an older snip would have left it, so the
		// lookup paths are exercised against a name that really is on disk.
		if err := os.WriteFile(s.Path(name), []byte("body\n"), filePerm); err != nil {
			t.Skipf("filesystem rejects %q: %v", name, err)
		}
		if err := validName(name); err == nil {
			t.Errorf("validName(%q) = nil; the manifest cannot carry this name", name)
		}
		if s.Exists(name) {
			t.Errorf("Exists(%q) = true; a name that cannot be enabled must not resolve", name)
		}
		if _, err := s.Body(name); err == nil {
			t.Errorf("Body(%q) succeeded; want an error", name)
		}
		if err := s.Write(name, "x"); err == nil {
			t.Errorf("Write(%q) succeeded; want an error", name)
		}
		if err := s.RemoveAll(name); err == nil {
			t.Errorf("RemoveAll(%q) succeeded; want an error", name)
		}
	}
}

// TestEveryListedSnippetCanBeEnabled pins the invariant the above bug broke:
// whatever Names reports must actually turn on, because the list is what the
// user picks from.
func TestEveryListedSnippetCanBeEnabled(t *testing.T) {
	s := newTestStore(t)
	mustWrite(t, s, "ok", "Body.")
	for _, name := range manifestUnsafeNames {
		os.WriteFile(s.Path(name), []byte("body\n"), filePerm)
	}
	// Files no snippet name could produce are not snippets, so only "ok" is
	// listed — and listing it is a promise that enabling it works.
	names, err := s.Names()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(names, []string{"ok"}) {
		t.Errorf("Names() = %v, want [ok]", names)
	}
	for _, name := range names {
		if err := s.Enable(name); err != nil {
			t.Fatalf("Enable(%q): %v", name, err)
		}
		if !s.IsEnabled(name) {
			t.Errorf("Names() listed %q but enabling it did not take effect", name)
		}
	}
}

// TestUnremovableStaleLockTimesOut is the regression test for a busy-loop:
// reclaiming a stale lock used to `continue` past both the deadline and the
// sleep, so a lock that could not be removed — a read-only directory, another
// user's file — span the CPU at 100% until the process was killed instead of
// failing after lockTimeout.
func TestUnremovableStaleLockTimesOut(t *testing.T) {
	s := newTestStore(t)
	mustWrite(t, s, "a", "A")
	// The point is that the wait ends, not how long it is; shorten it so the
	// suite does not spend the real timeout proving that.
	defer func(d time.Duration) { lockTimeout = d }(lockTimeout)
	lockTimeout = 20 * time.Millisecond

	lock := s.manifestPath() + ".lock"
	if err := os.WriteFile(lock, nil, filePerm); err != nil {
		t.Fatal(err)
	}
	old := timeLongAgo()
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	// Strip write permission from the directory so the reclaim cannot succeed.
	if err := os.Chmod(s.dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(s.dir, dirPerm) })
	if _, err := os.Stat(lock); err != nil {
		t.Skipf("cannot stage an unremovable lock here: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- s.Enable("a") }()
	select {
	case err := <-done:
		if err == nil {
			t.Skip("this filesystem allowed the removal; nothing to time out")
		}
	case <-time.After(10 * lockTimeout):
		t.Fatal("lock() never returned; a failed reclaim must fall through to the deadline, not loop")
	}
}

// TestSummaryReadsOnlyAPrefix pins the bound on listing cost. A summary is one
// line, but listings call Summary once per row, so reading each body in full
// made `snip` scale with the size of the corpus rather than with the rows it
// shows. The bound is observable: a first line past summaryMax comes back
// clipped, because the read stopped there.
func TestSummaryReadsOnlyAPrefix(t *testing.T) {
	s := newTestStore(t)
	// Written directly: Write itself refuses a body this large.
	body := "the summary line\n" + strings.Repeat("filler\n", 4096)
	if err := os.WriteFile(s.Path("big"), []byte(body), filePerm); err != nil {
		t.Fatal(err)
	}
	if got, want := s.Summary("big"), "the summary line"; got != want {
		t.Errorf("Summary() = %q, want %q", got, want)
	}

	// One line, longer than the cap and with no newline to stop at, so the
	// length of what comes back is exactly the length of what was read.
	long := strings.Repeat("x", 4*summaryMax)
	if err := os.WriteFile(s.Path("oneline"), []byte(long), filePerm); err != nil {
		t.Fatal(err)
	}
	if got := len(s.Summary("oneline")); got != summaryMax {
		t.Errorf("Summary() read %d bytes of a %d-byte line; want it capped at %d", got, len(long), summaryMax)
	}
}

// BenchmarkSummary records what a listing row costs. It is the regression
// guard's companion: the numbers should not move with the size of the bodies.
func BenchmarkSummary(b *testing.B) {
	b.Setenv("SNIP_DIR", b.TempDir())
	s, err := NewStore()
	if err != nil {
		b.Fatal(err)
	}
	body := "summary line\n" + strings.Repeat("filler filler filler\n", 8192)
	if err := os.WriteFile(s.Path("big"), []byte(body), filePerm); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if s.Summary("big") == "" {
			b.Fatal("empty summary")
		}
	}
}

// TestWriteRefusesAnOversizedBody guards the prompt budget: every enabled
// snippet is read and concatenated on every prompt, inside the hook's timeout,
// so `snip add notes < server.log` must fail rather than quietly bloat each
// prompt until the hook times out.
func TestWriteRefusesAnOversizedBody(t *testing.T) {
	s := newTestStore(t)
	if err := s.Write("big", strings.Repeat("x", maxSnippetBytes+1)); err == nil {
		t.Error("Write accepted a body past maxSnippetBytes")
	}
	if s.Exists("big") {
		t.Error("the oversized snippet was created anyway")
	}
	if err := s.Write("ok", strings.Repeat("x", maxSnippetBytes-1)); err != nil {
		t.Errorf("Write rejected a body within the limit: %v", err)
	}
}

// TestNonRegularFilesAreNotSnippets keeps a directory or a fifo named
// "brief.md" out of every listing: it would offer a row whose body no read can
// ever return.
func TestNonRegularFilesAreNotSnippets(t *testing.T) {
	s := newTestStore(t)
	mustWrite(t, s, "real", "R.")
	if err := os.Mkdir(s.Path("faux"), dirPerm); err != nil {
		t.Fatal(err)
	}
	if s.Exists("faux") {
		t.Error("a directory resolved as a snippet")
	}
	if _, err := s.Body("faux"); err == nil {
		t.Error("Body() read a directory")
	}
	names, err := s.Names()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(names, []string{"real"}) {
		t.Errorf("Names() = %v, want [real]", names)
	}
}

// TestProjectStoreRejectsSymlinkedSnippets is the regression test for a read
// primitive that arrives with a clone. A project store is repository-supplied,
// its bodies are pasted verbatim into every prompt, and git can commit a
// symlink: .claude/snippets/notes.md -> ~/.ssh/id_ed25519 needed nothing from
// the user but a cd into the checkout.
func TestProjectStoreRejectsSymlinkedSnippets(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(secret, []byte("PRIVATE KEY MATERIAL\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "notes"+snippetExt)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// The manifest ships in the repository too, so the snippet arrives enabled.
	if err := os.WriteFile(filepath.Join(dir, manifestName), []byte("notes\n"), filePerm); err != nil {
		t.Fatal(err)
	}

	project := newProjectStoreAt(dir, filepath.Join(t.TempDir(), projectStateDir, "p"))
	if project.Exists("notes") {
		t.Error("a symlinked project snippet resolved")
	}
	if body, err := project.Body("notes"); err == nil {
		t.Errorf("Body() followed the link and returned %q", body)
	}
	if names, _ := project.Names(); len(names) != 0 {
		t.Errorf("Names() = %v; a symlink must not be listed", names)
	}
	if on, _ := project.Enabled(); len(on) != 0 {
		t.Errorf("Enabled() = %v; the repository's manifest must not activate it", on)
	}
	text, err := project.Compose()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "PRIVATE KEY") {
		t.Fatalf("Compose() leaked the link target into the prompt: %q", text)
	}
	if text != "" {
		t.Errorf("Compose() = %q, want empty", text)
	}

	// The global store is the user's own directory, where symlinking a snippet
	// into a dotfiles repository is ordinary; that must keep working.
	global := newStoreAt(dir)
	if !global.Exists("notes") {
		t.Error("the global scope refused a symlinked snippet")
	}
}

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// captureStdout runs fn with os.Stdout redirected and returns what it printed.
// The command Run methods print directly, which is the behaviour under test.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	runErr := fn()
	os.Stdout = saved
	w.Close()

	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	r.Close()
	return sb.String(), runErr
}

// TestStatusDoesNotClaimNoneOnFailure is the regression test for a status line
// that lied. "snip: none" is a claim about the enabled set; printing it after a
// failed read told the user nothing was on when the truth was unknown, and a
// wrong "none" is the one answer they cannot spot from the status line itself.
func TestStatusDoesNotClaimNoneOnFailure(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone")
	ctx := &Context{Set: &Set{global: newStoreAt(missing)}}

	out, err := captureStdout(t, func() error { return (&statusCmd{}).Run(ctx) })
	if err == nil {
		t.Error("status reported success over an unreadable store")
	}
	if out != "" {
		t.Errorf("status printed %q after a failed read; want nothing", out)
	}
}

func TestStatusReportsNoneWhenTrulyEmpty(t *testing.T) {
	set := newTestSet(t, false)
	out, err := captureStdout(t, func() error { return (&statusCmd{}).Run(&Context{Set: set}) })
	if err != nil {
		t.Fatal(err)
	}
	if out != "snip: none" {
		t.Errorf("status = %q, want %q", out, "snip: none")
	}
}

// TestAddRefusesAnOversizedStdinBody guards the prompt budget from the command
// side: `snip add notes < server.log` must fail loudly rather than create a
// snippet that is then read and appended on every prompt.
func TestAddRefusesAnOversizedStdinBody(t *testing.T) {
	set := newTestSet(t, false)
	big := filepath.Join(t.TempDir(), "big")
	if err := os.WriteFile(big, []byte(strings.Repeat("x", 4*maxSnippetBytes)), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(big)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	saved := os.Stdin
	os.Stdin = f
	defer func() { os.Stdin = saved }()

	_, runErr := captureStdout(t, func() error {
		return (&addCmd{Name: "notes"}).Run(&Context{Set: set})
	})
	if runErr == nil {
		t.Error("add accepted a body past maxSnippetBytes")
	}
	if set.global.Exists("notes") {
		t.Error("the oversized snippet was created anyway")
	}
}

// TestPickRejectsAnEmptyList pins Pick's own precondition: every cursor move is
// modulo len(items), so an empty list would panic on a division by zero rather
// than report that there is nothing to choose.
func TestPickRejectsAnEmptyList(t *testing.T) {
	if _, ok, err := Pick(nil); err == nil || ok {
		t.Errorf("Pick(nil) = ok:%v err:%v; want an error", ok, err)
	}
}

// TestValidNameRejectsWindowsDeviceNames covers the platform the other name
// rules miss. "con.md" and "com1.md" name a device rather than a file in the
// store, and "c:x" is drive-relative, so on Windows they escape it — the same
// class of bug as `snip rm ../victim`, which is why filepath.IsLocal is in
// validName. Releases ship Windows binaries; this asserts on the one platform
// that can observe it.
func TestValidNameRejectsWindowsDeviceNames(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("device and drive-relative names are a Windows path concept")
	}
	for _, name := range []string{"con", "com1", "nul", "aux", "c:x"} {
		if err := validName(name); err == nil {
			t.Errorf("validName(%q) = nil; it does not name a file in the store", name)
		}
	}
}

// TestValidNameAcceptsOrdinarySnippetNames is the other half: the guards above
// must not start rejecting the names people actually use.
func TestValidNameAcceptsOrdinarySnippetNames(t *testing.T) {
	for _, name := range []string{"brief", "no-new-deps", "test_first", "v2", "ask-questions", "a.b", "über"} {
		if err := validName(name); err != nil {
			t.Errorf("validName(%q) = %v; want nil", name, err)
		}
	}
}

// TestScopeAdoptRejectsAnUnofferedName keeps `snip scope adopt` from becoming a
// second way to enable anything: it decides on what the repository proposed,
// and nothing else.
func TestScopeAdoptRejectsAnUnofferedName(t *testing.T) {
	set := newTestSet(t, true)
	mustWrite(t, set.project, "ticket", "Reference the ticket ID.")
	mustWrite(t, set.project, "other", "Something else.")
	shipSuggestion(t, set, "ticket")

	_, err := captureStdout(t, func() error {
		return (&scopeAdoptCmd{Names: []string{"other"}}).Run(&Context{Set: set})
	})
	if err == nil {
		t.Error("adopt accepted a name the repository never suggested")
	}
	if set.project.IsEnabled("other") {
		t.Error("the unoffered snippet was enabled anyway")
	}
	if suggested, _ := set.Suggested(); !slices.Equal(suggested, []string{"ticket"}) {
		t.Errorf("the real suggestion was disturbed: %v", suggested)
	}
}

// TestScopeAdoptOutsideAProjectSaysSo — the command is meaningless with no
// project scope, and silently doing nothing would read as success.
func TestScopeAdoptOutsideAProjectSaysSo(t *testing.T) {
	set := newTestSet(t, false)
	if _, err := captureStdout(t, func() error {
		return (&scopeAdoptCmd{}).Run(&Context{Set: set})
	}); err == nil {
		t.Error("adopt reported success outside a project")
	}
}

// TestMenuMarksSuggestionsWithoutEnablingThem covers the JSON contract the
// /snip slash command consumes: a suggestion has to be distinguishable from an
// enabled snippet, or the consumer cannot offer the choice.
func TestMenuMarksSuggestionsWithoutEnablingThem(t *testing.T) {
	set := newTestSet(t, true)
	mustWrite(t, set.global, "brief", "Be brief.")
	mustWrite(t, set.project, "ticket", "Reference the ticket ID.")
	if err := set.global.Enable("brief"); err != nil {
		t.Fatal(err)
	}
	shipSuggestion(t, set, "ticket")

	out, err := captureStdout(t, func() error { return (&menuCmd{}).Run(&Context{Set: set}) })
	if err != nil {
		t.Fatal(err)
	}
	var rows []menuRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("menu is not valid JSON: %v\n%s", err, out)
	}
	byName := map[string]menuRow{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	if got := byName["ticket"]; !got.Suggested || got.Enabled {
		t.Errorf("ticket = %+v; want suggested and not enabled", got)
	}
	if got := byName["brief"]; got.Suggested || !got.Enabled {
		t.Errorf("brief = %+v; want enabled and not suggested", got)
	}
}

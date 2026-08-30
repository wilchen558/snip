package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// newTestSet builds a global store plus a project store rooted in a temp
// directory, and chdirs there so scope discovery runs for real.
func newTestSet(t *testing.T, withProject bool) *Set {
	t.Helper()
	globalDir := t.TempDir()
	root := t.TempDir()
	t.Chdir(root)

	set := &Set{global: newStoreAt(globalDir)}
	if withProject {
		dir := filepath.Join(root, projectDirName)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		set.project = newStoreAt(dir)
		set.root = root
	}
	return set
}

func TestFindProjectDirWalksUp(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, projectDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(root, "src", "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(deep)

	gotRoot, gotDir, ok := findProjectDir(t.TempDir())
	if !ok {
		t.Fatal("findProjectDir did not find the opted-in ancestor")
	}
	// macOS temp dirs are symlinked, so compare resolved paths.
	if resolve(gotRoot) != resolve(root) {
		t.Errorf("root = %q, want %q", gotRoot, root)
	}
	if !strings.HasSuffix(gotDir, projectDirName) {
		t.Errorf("dir = %q, want suffix %q", gotDir, projectDirName)
	}
}

func TestFindProjectDirAbsentOutsideProject(t *testing.T) {
	t.Chdir(t.TempDir())
	if _, _, ok := findProjectDir(t.TempDir()); ok {
		t.Error("found a project dir where none was created")
	}
}

func TestListCoversBothScopes(t *testing.T) {
	set := newTestSet(t, true)
	mustWrite(t, set.global, "brief", "Global brief.")
	mustWrite(t, set.global, "only-global", "G.")
	mustWrite(t, set.project, "brief", "Project brief.")

	all, err := set.List()
	if err != nil {
		t.Fatal(err)
	}
	// Global first, then project; same name in both appears twice.
	want := []struct {
		name  string
		scope Scope
	}{
		{"brief", Global}, {"only-global", Global}, {"brief", Project},
	}
	if len(all) != len(want) {
		t.Fatalf("List() returned %d entries, want %d: %+v", len(all), len(want), all)
	}
	for i, w := range want {
		if all[i].Name != w.name || all[i].Scope != w.scope {
			t.Errorf("entry %d = %s(%s), want %s(%s)", i, all[i].Name, all[i].Scope, w.name, w.scope)
		}
	}
}

func TestResolvePrefersProject(t *testing.T) {
	set := newTestSet(t, true)
	mustWrite(t, set.global, "brief", "Global brief.")
	mustWrite(t, set.project, "brief", "Project brief.")

	_, scope, err := set.Resolve("brief", false)
	if err != nil {
		t.Fatal(err)
	}
	if scope != Project {
		t.Errorf("bare name resolved to %s, want project", scope)
	}

	store, scope, err := set.Resolve("brief", true)
	if err != nil {
		t.Fatal(err)
	}
	if scope != Global {
		t.Errorf("-g resolved to %s, want global", scope)
	}
	if body, _ := store.Body("brief"); body != "Global brief." {
		t.Errorf("-g gave body %q", body)
	}
}

func TestResolveFallsBackToGlobal(t *testing.T) {
	set := newTestSet(t, true)
	mustWrite(t, set.global, "only-global", "G.")
	_, scope, err := set.Resolve("only-global", false)
	if err != nil {
		t.Fatal(err)
	}
	if scope != Global {
		t.Errorf("scope = %s, want global", scope)
	}
}

func TestResolveGlobalFlagIgnoresProjectOnlySnippet(t *testing.T) {
	set := newTestSet(t, true)
	mustWrite(t, set.project, "local", "P.")
	if _, _, err := set.Resolve("local", true); err == nil {
		t.Error("-g resolved a project-only snippet; want an error")
	}
}

func TestEnablingOneScopeLeavesTheOtherAlone(t *testing.T) {
	set := newTestSet(t, true)
	mustWrite(t, set.global, "brief", "Global brief.")
	mustWrite(t, set.project, "brief", "Project brief.")

	// Toggling the bare name must hit the project manifest only.
	store, _, err := set.Resolve("brief", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Toggle("brief"); err != nil {
		t.Fatal(err)
	}
	if on, _ := set.project.Enabled(); !slices.Equal(on, []string{"brief"}) {
		t.Errorf("project enabled = %v, want [brief]", on)
	}
	if on, _ := set.global.Enabled(); len(on) != 0 {
		t.Errorf("global manifest was modified: %v", on)
	}
}

func TestComposePutsGlobalFirstThenProject(t *testing.T) {
	set := newTestSet(t, true)
	mustWrite(t, set.global, "g", "Global text.")
	mustWrite(t, set.project, "p", "Project text.")
	if err := set.global.Enable("g"); err != nil {
		t.Fatal(err)
	}
	if err := set.project.Enable("p"); err != nil {
		t.Fatal(err)
	}
	got, err := set.Compose()
	if err != nil {
		t.Fatal(err)
	}
	if want := "Global text.\n\nProject text."; got != want {
		t.Errorf("Compose() = %q, want %q", got, want)
	}
}

func TestComposeWithoutProjectScope(t *testing.T) {
	set := newTestSet(t, false)
	mustWrite(t, set.global, "g", "Global text.")
	if err := set.global.Enable("g"); err != nil {
		t.Fatal(err)
	}
	got, err := set.Compose()
	if err != nil {
		t.Fatal(err)
	}
	if got != "Global text." {
		t.Errorf("Compose() = %q", got)
	}
}

func TestEnabledLabelsTagOnlyAmbiguousNames(t *testing.T) {
	set := newTestSet(t, true)
	mustWrite(t, set.global, "brief", "G.")
	mustWrite(t, set.global, "solo", "S.")
	mustWrite(t, set.project, "brief", "P.")
	if err := set.global.Enable("brief", "solo"); err != nil {
		t.Fatal(err)
	}
	if err := set.project.Enable("brief"); err != nil {
		t.Fatal(err)
	}
	got, err := set.EnabledLabels()
	if err != nil {
		t.Fatal(err)
	}
	// "solo" exists in one scope so it stays bare; "brief" is tagged.
	if want := []string{"brief(g)", "solo", "brief(p)"}; !slices.Equal(got, want) {
		t.Errorf("EnabledLabels() = %v, want %v", got, want)
	}
}

func TestActiveScopeFollowsProjectAndFlag(t *testing.T) {
	withProject := newTestSet(t, true)
	if got := withProject.ActiveScope(false); got != Project {
		t.Errorf("in a project, ActiveScope(false) = %s, want project", got)
	}
	if got := withProject.ActiveScope(true); got != Global {
		t.Errorf("ActiveScope(true) = %s, want global", got)
	}
	bare := newTestSet(t, false)
	if got := bare.ActiveScope(false); got != Global {
		t.Errorf("outside a project, ActiveScope(false) = %s, want global", got)
	}
}

func resolve(p string) string {
	out, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p
	}
	return out
}

// TestFindProjectDirSkipsTheGlobalStore is the regression test for a bug where
// ~/.claude/snippets — the global store — matched the project marker, so every
// directory under $HOME resolved to a project rooted at $HOME.
func TestFindProjectDirSkipsTheGlobalStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home) // os.UserHomeDir reads this
	globalDir := filepath.Join(home, projectDirName)
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(home, "projects", "backend")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)

	if root, dir, ok := findProjectDir(globalDir); ok {
		t.Errorf("global store matched as a project: root=%q dir=%q", root, dir)
	}

	// Even when the global store is configured elsewhere, $HOME/.claude must
	// not become a project root.
	if root, dir, ok := findProjectDir(t.TempDir()); ok {
		t.Errorf("$HOME matched as a project: root=%q dir=%q", root, dir)
	}

	// An genuine project below it must still be found.
	own := filepath.Join(work, projectDirName)
	if err := os.MkdirAll(own, 0o755); err != nil {
		t.Fatal(err)
	}
	root, _, ok := findProjectDir(globalDir)
	if !ok || resolve(root) != resolve(work) {
		t.Errorf("real project not found: root=%q ok=%v", root, ok)
	}
}

// TestScopeInitRefusesWhereDiscoveryWillNotLook is the regression test for
// `snip scope init` in $HOME: it created ~/.claude/snippets, printed "project
// snippets now apply here", and then every command still resolved to the
// global scope — so snippets meant for a project silently landed in the user's
// global store, the very failure findProjectDir's guard exists to prevent.
func TestScopeInitRefusesWhereDiscoveryWillNotLook(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home) // os.UserHomeDir reads this
	globalDir := filepath.Join(home, projectDirName)
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(home)

	ctx := &Context{Set: &Set{global: newStoreAt(globalDir)}}
	if err := (&scopeInitCmd{}).Run(ctx); err == nil {
		t.Error("scope init in $HOME reported success; discovery never accepts $HOME as a project root")
	}
	// The marker it would have created is still not a project, which is what
	// made the success message a lie.
	if _, _, ok := findProjectDir(globalDir); ok {
		t.Error("$HOME resolved as a project root")
	}
}

// TestProjectRootRefusalAgreesWithDiscovery pins the two halves together: the
// directories "snip scope init" declines are exactly the ones findProjectDir
// skips, so init can never report success for a marker discovery ignores.
func TestProjectRootRefusalAgreesWithDiscovery(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	globalDir := filepath.Join(home, projectDirName)
	work := filepath.Join(home, "projects", "backend")
	for _, dir := range []string{globalDir, work, filepath.Join(work, projectDirName)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		dir     string
		refused bool
		why     string
	}{
		{home, true, "$HOME holds Claude Code's own configuration"},
		{work, false, "an ordinary repository"},
	} {
		refusal := projectRootRefusal(tc.dir, globalDir)
		if (refusal != nil) != tc.refused {
			t.Errorf("projectRootRefusal(%s) = %v, want refused=%v; %s", tc.dir, refusal, tc.refused, tc.why)
		}
		t.Chdir(tc.dir)
		if _, _, ok := findProjectDir(globalDir); ok == tc.refused {
			t.Errorf("at %s: discovery found=%v but init refuses=%v", tc.dir, ok, tc.refused)
		}
	}
}

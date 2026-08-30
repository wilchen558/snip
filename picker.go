package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
)

// item is one row of the interactive picker.
type item struct {
	name    string
	summary string
	on      bool
}

// rawMode puts the controlling terminal into cbreak mode via stty, so the
// picker needs no third-party terminal library. The returned func restores it.
func rawMode(tty *os.File) (func(), error) {
	saved, err := sttyOut(tty, "-g")
	if err != nil {
		return nil, err
	}
	if _, err := sttyOut(tty, "raw", "-echo"); err != nil {
		return nil, err
	}
	return func() { _, _ = sttyOut(tty, saved) }, nil
}

func sttyOut(tty *os.File, args ...string) (string, error) {
	cmd := exec.Command("stty", args...)
	cmd.Stdin = tty
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// Pick renders a checkbox list on /dev/tty and returns the chosen names.
// The bool reports whether the user confirmed; false means they cancelled.
func Pick(items []item) ([]string, bool, error) {
	// Every cursor move is modulo len(items), so an empty list would divide by
	// zero. pickCmd checks this too; Pick owns its own precondition.
	if len(items) == 0 {
		return nil, false, errors.New("no snippets to pick from")
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, false, fmt.Errorf("no terminal available (a picker needs a TTY): %w", err)
	}
	defer tty.Close()

	restore, err := rawMode(tty)
	if err != nil {
		return nil, false, fmt.Errorf("cannot set raw mode: %w", err)
	}
	// Restore the terminal even if the user hits Ctrl+C mid-picker.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case <-sig:
			restore()
			fmt.Fprint(tty, "\r\n")
			os.Exit(130)
		case <-done:
		}
	}()
	defer func() {
		close(done)
		signal.Stop(sig)
		restore()
	}()

	cur, offset := 0, 0
	// Reserve rows for the header, the trailing blank line and the shell
	// prompt, then scroll a window through the list. Without this clamp a list
	// taller than the terminal makes it scroll, desyncing the cursor rewind.
	window := len(items)
	if h := terminalHeight(tty); h > 0 && window > h-3 {
		window = h - 3
	}
	if window < 1 {
		window = 1
	}
	drawn := 0
	draw := func() {
		if drawn > 0 {
			fmt.Fprintf(tty, "\033[%dA", drawn) // rewind exactly what we printed
		}
		// Keep the cursor inside the visible window.
		if cur < offset {
			offset = cur
		}
		if cur >= offset+window {
			offset = cur - window + 1
		}
		drawn = 0
		head := "  space toggle · a all · n none · enter save · q cancel"
		if window < len(items) {
			head += fmt.Sprintf("  %s[%d-%d/%d]%s", dim, offset+1, offset+window, len(items), reset)
		}
		fmt.Fprintf(tty, "\r\033[K%s\r\n", head)
		drawn++
		for i := offset; i < offset+window && i < len(items); i++ {
			it := items[i]
			mark := " "
			if it.on {
				mark = "x"
			}
			pointer := "  "
			if i == cur {
				pointer = cyan + ">" + reset + " "
			}
			line := fmt.Sprintf("%s[%s] %-16s %s%s%s", pointer, mark, it.name, dim, truncate(it.summary, 46), reset)
			fmt.Fprintf(tty, "\r\033[K%s\r\n", line)
			drawn++
		}
		fmt.Fprint(tty, "\r\033[K\r\n")
		drawn++
	}
	draw()

	buf := make([]byte, 3)
	for {
		n, err := tty.Read(buf)
		if err != nil || n == 0 {
			return nil, false, err
		}
		switch {
		case buf[0] == 27 && n >= 3 && buf[1] == '[': // arrow keys
			switch buf[2] {
			case 'A':
				cur = (cur - 1 + len(items)) % len(items)
			case 'B':
				cur = (cur + 1) % len(items)
			}
		case buf[0] == 27, buf[0] == 'q', buf[0] == 3: // esc, q, ctrl-c
			return nil, false, nil
		case buf[0] == 'k':
			cur = (cur - 1 + len(items)) % len(items)
		case buf[0] == 'j':
			cur = (cur + 1) % len(items)
		case buf[0] == ' ':
			items[cur].on = !items[cur].on
		case buf[0] == 'a':
			for i := range items {
				items[i].on = true
			}
		case buf[0] == 'n':
			for i := range items {
				items[i].on = false
			}
		case buf[0] == '\r', buf[0] == '\n':
			var out []string
			for _, it := range items {
				if it.on {
					out = append(out, it.name)
				}
			}
			return out, true, nil
		}
		draw()
	}
}

// terminalHeight reads the row count from stty, returning 0 when unknown so
// callers fall back to rendering the whole list.
func terminalHeight(tty *os.File) int {
	out, err := sttyOut(tty, "size")
	if err != nil {
		return 0
	}
	rows, _, ok := strings.Cut(out, " ")
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(rows)
	if err != nil {
		return 0
	}
	return n
}

type pickCmd struct{}

// Run shows every snippet from both scopes in one checkbox list, then writes
// each scope's manifest from the selection.
func (*pickCmd) Run(ctx *Context) error {
	all, err := ctx.Set.List()
	if err != nil {
		return err
	}
	if len(all) == 0 {
		return fmt.Errorf("no snippets in %s", ctx.Set.global.Dir())
	}
	tagged := ctx.Set.project != nil
	items := make([]item, len(all))
	for i, sn := range all {
		label := sn.Name
		if tagged {
			label = fmt.Sprintf("%s (%s)", sn.Name, sn.Scope)
		}
		items[i] = item{name: label, summary: sn.Summary(), on: sn.Enabled()}
	}

	chosen, ok, err := Pick(items)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Println("cancelled")
		return nil
	}

	// Map the picked labels back to a per-scope selection.
	picked := make(map[string]bool, len(chosen))
	for _, label := range chosen {
		picked[label] = true
	}
	selection := map[*Store][]string{}
	for i, sn := range all {
		if picked[items[i].name] {
			selection[sn.Store] = append(selection[sn.Store], sn.Name)
		}
	}
	for _, store := range ctx.Set.stores() {
		if err := store.SetEnabled(selection[store]); err != nil {
			return err
		}
	}
	return renderList(ctx.Set)
}

package drivers

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type recorder struct {
	calls   []string
	current string // what the pane is running
}

func (r *recorder) run(_ context.Context, name string, args ...string) (string, error) {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	if len(args) > 0 && args[len(args)-1] == "#{pane_current_command}" {
		return r.current + "\n", nil
	}
	return "", nil
}

func TestTmuxNudge(t *testing.T) {
	r := &recorder{current: "claude"}
	d := &Tmux{Run: r.run}
	line := "You have 1 new handloom message. Run: handloom inbox"
	if err := d.Nudge(context.Background(), "/tmp/tmux-0/default:%3", line); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"tmux -S /tmp/tmux-0/default display-message -p -t %3 #{pane_current_command}",
		"tmux -S /tmp/tmux-0/default send-keys -t %3 -l " + line,
		"tmux -S /tmp/tmux-0/default send-keys -t %3 Enter",
	}
	if strings.Join(r.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s", strings.Join(r.calls, "\n"))
	}
}

// A pane that fell back to a shell must not be typed into: the shell would
// run the nudge as a command.
func TestTmuxRefusesShell(t *testing.T) {
	for _, sh := range []string{"bash", "zsh", "sh", "fish"} {
		r := &recorder{current: sh}
		err := (&Tmux{Run: r.run}).Nudge(context.Background(), ":%1", "x")
		if err == nil || len(r.calls) != 1 {
			t.Fatalf("%s: err %v, calls %v", sh, err, r.calls)
		}
	}
	if err := (&Tmux{Run: (&recorder{}).run}).Nudge(context.Background(), "nopane", "x"); err == nil {
		t.Fatal("bad target accepted")
	}
}

func TestHerdrNudge(t *testing.T) {
	r := &recorder{}
	if err := (&Herdr{Run: r.run}).Nudge(context.Background(), "w1:p2", "hello"); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 1 || r.calls[0] != "herdr agent prompt w1:p2 hello" {
		t.Fatalf("calls: %v", r.calls)
	}
}

func TestForAndDetect(t *testing.T) {
	if d, rest, err := For("tmux:/s:%1"); err != nil || d.Name() != "tmux" || rest != "/s:%1" {
		t.Fatalf("tmux: %v %q %v", d, rest, err)
	}
	if d, rest, err := For("herdr:w1:p2"); err != nil || d.Name() != "herdr" || rest != "w1:p2" {
		t.Fatalf("herdr: %v %q %v", d, rest, err)
	}
	if _, _, err := For("carrier:pigeon"); err == nil {
		t.Fatal("unknown driver accepted")
	}

	t.Setenv("TMUX", "/tmp/tmux-0/default,123,0")
	t.Setenv("TMUX_PANE", "%7")
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	if got := Detect(); got != "tmux:/tmp/tmux-0/default:%7" {
		t.Fatalf("tmux must win over inherited herdr variables: %q", got)
	}
	t.Setenv("TMUX_PANE", "")
	if got := Detect(); got != "herdr:w1:p1" {
		t.Fatalf("herdr: %q", got)
	}
	t.Setenv("HERDR_PANE_ID", "")
	if got := Detect(); got != "" {
		t.Fatalf("no terminal: %q", got)
	}
}

func TestAlive(t *testing.T) {
	var got [][]string
	run := func(fail bool, out string) Runner {
		return func(_ context.Context, name string, args ...string) (string, error) {
			got = append(got, append([]string{name}, args...))
			if fail {
				return out, errors.New("exit 1")
			}
			return out, nil
		}
	}
	ctx := context.Background()
	if !(&Tmux{Run: run(false, "%3 0\n")}).Alive(ctx, "/tmp/tmux-0/default:%3") {
		t.Fatal("a pane that answers is not alive")
	}
	if want := "tmux -S /tmp/tmux-0/default display-message -p -t %3 #{pane_id} #{pane_dead}"; strings.Join(got[0], " ") != want {
		t.Fatalf("tmux command %q, want %q", strings.Join(got[0], " "), want)
	}
	// tmux answers an empty line, with success, for a pane that is gone.
	if (&Tmux{Run: run(false, "\n")}).Alive(ctx, "/tmp/tmux-0/default:%3") {
		t.Fatal("an empty answer counts as alive")
	}
	if (&Tmux{Run: run(false, "%3 1\n")}).Alive(ctx, "/tmp/tmux-0/default:%3") {
		t.Fatal("a dead pane kept on screen counts as alive")
	}
	if (&Tmux{Run: run(false, "%4 0\n")}).Alive(ctx, "/tmp/tmux-0/default:%3") {
		t.Fatal("another pane's answer counts as alive")
	}
	if (&Tmux{Run: run(true, "no server")}).Alive(ctx, "/tmp/tmux-0/default:%3") {
		t.Fatal("a missing pane is alive")
	}
	if (&Tmux{Run: run(false, "")}).Alive(ctx, "nopane") {
		t.Fatal("a malformed target is alive")
	}
	if !(&Herdr{Run: run(false, `{"result":{"pane":{}}}`)}).Alive(ctx, "w1:p2") {
		t.Fatal("a herdr pane that answers is not alive")
	}
	if (&Herdr{Run: run(false, `{"error":{"code":"pane_not_found"}}`)}).Alive(ctx, "w1:p2") {
		t.Fatal("a herdr pane_not_found answer counts as alive")
	}
	if (&Herdr{Run: run(true, "")}).Alive(ctx, "w1:p2") || (&Herdr{Run: run(false, "")}).Alive(ctx, "") {
		t.Fatal("herdr error or empty target is alive")
	}
}

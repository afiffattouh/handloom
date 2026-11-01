package playbook

import (
	"reflect"
	"testing"
)

func TestMVPWorkflow(t *testing.T) {
	p, err := Compile(MVP, "Build an appointment booking tool")
	if err != nil {
		t.Fatal(err)
	}
	if p.Playbook != MVP || len(p.Steps) != 10 {
		t.Fatalf("unexpected recipe: %+v", p)
	}
	byKey := map[string]int{}
	for i, s := range p.Steps {
		byKey[s.Key] = i
		if _, ok := ProfileStarters[s.Profile]; !ok {
			t.Fatalf("no starter for %s", s.Profile)
		}
	}
	if !reflect.DeepEqual(p.Steps[byKey["release"]].DependsOn, []string{"qa", "security", "review"}) {
		t.Fatal("release bypasses independent reviews")
	}
	for _, key := range []string{"qa", "security", "review"} {
		s := p.Steps[byKey[key]]
		if s.Profile == p.Steps[byKey["frontend"]].Profile || s.Profile == p.Steps[byKey["backend"]].Profile {
			t.Fatal("author reviews its own implementation")
		}
	}
	p.Steps[0].Title = "mutated"
	fresh, err := Compile(MVP, "another product")
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Steps[0].Title == "mutated" {
		t.Fatal("recipe shared mutable state")
	}
}
func TestRejectUnknownOrEmptyBrief(t *testing.T) {
	for _, x := range [][2]string{{"enterprise", "build"}, {MVP, " "}} {
		if _, err := Compile(x[0], x[1]); err == nil {
			t.Fatal("invalid request compiled")
		}
	}
}

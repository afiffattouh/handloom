package plan

import (
	"reflect"
	"testing"
)

func TestDependencyOrder(t *testing.T) {
	p := Plan{Version: 1, MaxAttempts: 2, Steps: []Step{
		{Key: "release", Title: "Release", Profile: "ops", DependsOn: []string{"qa"}},
		{Key: "build", Title: "Build", Profile: "coder"},
		{Key: "qa", Title: "Test", Profile: "qa", DependsOn: []string{"build"}},
	}}
	order, err := p.Order()
	if err != nil || !reflect.DeepEqual(order, []int{1, 2, 0}) {
		t.Fatalf("%v %v", order, err)
	}
}

func TestRejectInvalidPlans(t *testing.T) {
	cases := map[string]func(*Plan){
		"cycle":                func(p *Plan) { p.Steps[0].DependsOn = []string{"b"}; p.Steps[1].DependsOn = []string{"a"} },
		"unknown dependency":   func(p *Plan) { p.Steps[0].DependsOn = []string{"missing"} },
		"self dependency":      func(p *Plan) { p.Steps[0].DependsOn = []string{"a"} },
		"duplicate dependency": func(p *Plan) { p.Steps[0].DependsOn = []string{"b", "b"} },
		"duplicate key":        func(p *Plan) { p.Steps[1].Key = "a" },
		"unbounded retries":    func(p *Plan) { p.MaxAttempts = 0 },
		"too many retries":     func(p *Plan) { p.MaxAttempts = 4 },
		"version":              func(p *Plan) { p.Version = 2 },
		"empty":                func(p *Plan) { p.Steps = nil },
		"missing role":         func(p *Plan) { p.Steps[0].Profile = " " },
		"invalid key":          func(p *Plan) { p.Steps[0].Key = "../a" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := Plan{Version: 1, MaxAttempts: 2, Steps: []Step{{Key: "a", Title: "A", Profile: "coder"}, {Key: "b", Title: "B", Profile: "qa"}}}
			mutate(&p)
			if _, err := p.Order(); err == nil {
				t.Fatal("invalid plan accepted")
			}
		})
	}
}

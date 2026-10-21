package link

import (
	"strings"
	"testing"

	"handloom/internal/api"
)

func tool(leaves bool, max int) *api.SpawnTool {
	return &api.SpawnTool{Addon: "echo", Tool: "say", LeavesMachine: leaves, MaxCalls: max}
}

func gone(cause string) *api.ToolUnavailable {
	return &api.ToolUnavailable{Addon: "echo", Tool: "say", Cause: cause}
}

// Every code the gate decides, one case each, and then the cases where two things are wrong at once: the
// design's order says which one is told.
func TestDecideGivesEachCodeAndKeepsTheOrder(t *testing.T) {
	cases := []struct {
		name string
		in   gateInput
		want string
	}{
		{"fine", gateInput{Tool: tool(false, 5)}, codeOK},
		{"fine and it leaves, job not confidential", gateInput{Tool: tool(true, 5)}, codeOK},
		{"the add-on is not here", gateInput{Local: localState{Missing: true}}, codeNotInstalled},
		{"the hub says it was not installed", gateInput{Unavailable: gone(api.CauseNotInstalled)}, codeNotInstalled},
		{"never granted", gateInput{}, codeDeniedGrant},
		{"confidential, tool leaves", gateInput{Tool: tool(true, 5), Confidential: true}, codeDeniedConfidential},
		{"confidential, listed unavailable by the hub", gateInput{Unavailable: gone(api.CauseDeniedConfidential), Confidential: true}, codeDeniedConfidential},
		{"confidential, tool stays", gateInput{Tool: tool(false, 5), Confidential: true}, codeOK},
		{"call limit reached", gateInput{Tool: tool(false, 3), Used: 3}, codeDeniedBudget},
		{"one call left", gateInput{Tool: tool(false, 3), Used: 2}, codeOK},
		{"changed on this computer", gateInput{Tool: tool(false, 5), Local: localState{Changed: true}}, codeChanged},
		{"granted for another manifest than the installed one", gateInput{Tool: &api.SpawnTool{Addon: "echo", Tool: "say", MaxCalls: 5, ManifestHash: "aaa"}, Local: localState{ManifestHash: "bbb"}}, codeChanged},
		{"granted for the installed manifest", gateInput{Tool: &api.SpawnTool{Addon: "echo", Tool: "say", MaxCalls: 5, ManifestHash: "aaa"}, Local: localState{ManifestHash: "aaa"}}, codeOK},
		{"changed, said by the hub", gateInput{Unavailable: gone(api.CauseChanged)}, codeChanged},
		{"cannot be started, said by the hub", gateInput{Unavailable: gone(api.CausePluginDown)}, codeDown},
		{"cannot be read here", gateInput{Tool: tool(false, 5), Local: localState{Broken: "the approvals file is damaged"}}, codeDown},

		// two things wrong: the first in the order is the answer
		{"missing beats not granted", gateInput{Local: localState{Missing: true}}, codeNotInstalled},
		{"missing beats confidential", gateInput{Tool: tool(true, 5), Confidential: true, Local: localState{Missing: true}}, codeNotInstalled},
		{"not granted beats everything after it", gateInput{Confidential: true, Used: 99, Local: localState{Changed: true, Broken: "x"}}, codeDeniedGrant},
		{"confidential beats the call limit", gateInput{Tool: tool(true, 1), Used: 1, Confidential: true}, codeDeniedConfidential},
		{"the call limit beats changed", gateInput{Tool: tool(false, 1), Used: 1, Local: localState{Changed: true}}, codeDeniedBudget},
		{"changed beats down", gateInput{Tool: tool(false, 5), Local: localState{Changed: true, Broken: "x"}}, codeChanged},
		{"confidential beats changed on the hub's word", gateInput{Unavailable: gone(api.CauseDeniedConfidential), Confidential: true, Local: localState{Changed: true}}, codeDeniedConfidential},
	}
	for _, c := range cases {
		if got := decide(c.in); got != c.want {
			t.Errorf("%s: decide gave %q, want %q", c.name, got, c.want)
		}
	}
}

// The words an agent is given never leave it guessing, and the codes the hub accepts are the ones the link has.
func TestEveryRefusalSaysTheToolIsNotAvailable(t *testing.T) {
	for _, code := range []string{codeNotInstalled, codeDeniedGrant, codeDeniedConfidential, codeDeniedBudget, codeChanged, codeDown, codeError, codeTimeout} {
		got := refusal(code, "the add-on said so")
		if !strings.Contains(got, "do not guess the answer") || !strings.HasSuffix(got, notAvailable) {
			t.Errorf("%s: %q", code, got)
		}
		if strings.Contains(strings.ToLower(got), "plugin") || strings.Contains(strings.ToLower(got), "machine") {
			t.Errorf("%s uses a word people should not see: %q", code, got)
		}
	}
	if got := refusal(codeError, "the address did not answer"); !strings.Contains(got, "The add-on reported an error: the address did not answer.") {
		t.Errorf("the add-on's message is lost: %q", got)
	}
	if got := refusal(codeDeniedBudget, "ignored"); strings.Contains(got, "ignored") {
		t.Errorf("a detail leaked into a fixed message: %q", got)
	}
}

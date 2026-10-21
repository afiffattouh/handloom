package link

import "handloom/internal/api"

// The gateway's deciding function. It is the only place that decides whether a tool call may go to an
// add-on, and it does no work of its own: everything it needs is handed in, so a table test can show every
// code and the order the design gives (docs/direction/addons.md, "How an agent reaches a tool").

// Result codes. The ones a call ends with after it reached the add-on (plugin-error, timeout) come from the
// supervisor; the rest are decided here, before the add-on is asked.
const (
	codeOK                 = "ok"
	codeNotInstalled       = "not-installed"
	codeDeniedGrant        = "denied-grant"
	codeDeniedConfidential = "denied-confidential"
	codeDeniedBudget       = "denied-budget"
	codeChanged            = "changed"
	codeDown               = "plugin-down"
	codeError              = "plugin-error"
	codeTimeout            = "timeout"
)

// localState is what the computer itself says about an add-on and a tool right now.
type localState struct {
	Missing bool   // the add-on is not installed and approved here, or it has no such tool
	Changed bool   // what is on disk is not what the owner approved
	Broken  string // the add-on cannot be read or started; the reason in words
	// ManifestHash is the hash of the manifest installed and approved here, set only when the add-on is
	// sound. A grant is good only for the manifest it was made for.
	ManifestHash string
}

// gateInput is one call, as the deciding function sees it.
type gateInput struct {
	Tool         *api.SpawnTool       // the tool in the spawn's snapshot, or nil
	Unavailable  *api.ToolUnavailable // the tool among those the profile is granted but the spawn cannot have, or nil
	Confidential bool                 // the job's flag
	Used         int                  // calls this spawn has already made to this tool
	Local        localState
}

// decide gives the result code for a call: codeOK when it may go on, else the first of these that holds.
//
//  1. not-installed: the computer has no such add-on or tool
//  2. denied-grant: the spawn's profile was not granted the tool
//  3. denied-confidential: a confidential job and the tool leaves the computer
//  4. denied-budget: the spawn has used up the tool's call limit
//  5. changed: the add-on is not what was approved, or not the manifest the grant was made for
//  6. plugin-down: the add-on is not running and could not be started
func decide(in gateInput) string {
	cause := ""
	if in.Unavailable != nil {
		cause = in.Unavailable.Cause
	}
	switch {
	case in.Local.Missing || cause == api.CauseNotInstalled:
		return codeNotInstalled
	case in.Tool == nil && in.Unavailable == nil:
		return codeDeniedGrant
	case cause == api.CauseDeniedConfidential || (in.Confidential && in.Tool != nil && in.Tool.LeavesMachine):
		return codeDeniedConfidential
	case in.Tool != nil && in.Used >= in.Tool.MaxCalls:
		return codeDeniedBudget
	case in.Local.Changed || (in.Tool != nil && in.Local.ManifestHash != "" && in.Tool.ManifestHash != in.Local.ManifestHash) || cause == api.CauseChanged:
		return codeChanged
	case in.Local.Broken != "" || cause == api.CausePluginDown:
		return codeDown
	}
	return codeOK
}

// codeWords is each code in plain words, and the one thing a person can do about it.
var codeWords = map[string]struct{ Plain, Action string }{
	codeNotInstalled:       {"This computer does not have the add-on.", "Install the add-on on this computer."},
	codeDeniedGrant:        {"This helper's profile may not use that tool.", "Grant the tool on the profile."},
	codeDeniedConfidential: {"A confidential job may not use a tool that leaves the computer.", "Use a tool that stays on the computer, or a different job."},
	codeDeniedBudget:       {"The call limit for this helper was reached.", "Raise the limit on the grant."},
	codeChanged:            {"The add-on changed since it was approved.", "Approve it again on this computer."},
	codeDown:               {"The add-on is not running and could not be started.", "Run handloom addon doctor on this computer."},
	codeError:              {"The add-on reported an error.", ""},
	codeTimeout:            {"The call took longer than its limit.", "Try again, or raise the limit."},
}

// notAvailable ends every refusal, so an agent does not make up what the tool would have said.
const notAvailable = "This tool is not available, do not guess the answer."

// refusal is the sentence an agent is given for a call that did not go well. detail is what is known about
// this case (the add-on's own message, the reason it could not start); it is left out when empty.
func refusal(code, detail string) string {
	w := codeWords[code]
	out := w.Plain
	if detail != "" && (code == codeError || code == codeDown || code == codeTimeout) {
		out = w.Plain[:len(w.Plain)-1] + ": " + detail
		if last := out[len(out)-1]; last != '.' && last != '!' && last != '?' {
			out += "."
		}
	}
	if w.Action != "" {
		out += " " + w.Action
	}
	return out + " " + notAvailable
}

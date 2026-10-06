package cli

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"handloom/internal/api"
)

// ask opens an escalation. By default it returns at once: the answer comes
// back as a message from the human, and handloom wakes the lead when it arrives.
// --wait blocks and polls for the answer, for scripts and tests.
func (e *env) ask(args []string) error {
	fs := e.flags("ask")
	var options listFlag
	fs.Var(&options, "option", "an allowed answer; repeat for more (omit for free text)")
	task := fs.Int64("task", 0, "task this question is about")
	wait := fs.Duration("wait", 0, "wait up to this long for the answer (default: do not wait)")
	pos, err := fs.need(args, 1, -1, "ask <question> [--option A --option B] [--task N] [--wait 10m]")
	if err != nil {
		return err
	}
	c, err := conn()
	if err != nil {
		return err
	}
	req := api.AskReq{Question: strings.Join(pos, " "), Options: options}
	if *task != 0 {
		req.TaskID = task
	}
	var esc api.Escalation
	if err := c.Post("/v1/escalations", req, &esc); err != nil {
		return err
	}
	deadline := time.Now().Add(*wait)
	for *wait > 0 && esc.Answer == nil && time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		if err := c.Get("/v1/escalations/"+strconv.FormatInt(esc.ID, 10), &esc); err != nil {
			return err
		}
	}
	e.print(esc, func() {
		switch {
		case esc.Answer != nil:
			fmt.Fprintf(e.out, "Answered by %s: %s\n", esc.AnsweredBy, *esc.Answer)
		case *wait > 0:
			fmt.Fprintf(e.out, "Question #%d is still open after %s. The answer will arrive as a message; end your turn and handloom wakes you.\n", esc.ID, *wait)
		default:
			fmt.Fprintf(e.out, "Question #%d sent to the human. The answer will arrive as a message from human:<name>; end your turn and handloom wakes you.\n", esc.ID)
		}
	})
	return nil
}

// answer is the human's side. Agents are refused by the hub.
func (e *env) answer(args []string) error {
	fs := e.flags("answer")
	pos, err := fs.need(args, 2, -1, "answer <id> <answer>")
	if err != nil {
		return err
	}
	id, err := strconv.ParseInt(pos[0], 10, 64)
	if err != nil {
		return usageErr("id must be an escalation number")
	}
	c, err := conn()
	if err != nil {
		return err
	}
	var esc api.Escalation
	if err := c.Post(fmt.Sprintf("/v1/escalations/%d/answer", id), api.AnswerReq{Answer: strings.Join(pos[1:], " ")}, &esc); err != nil {
		return err
	}
	e.print(esc, func() { fmt.Fprintf(e.out, "Answered #%d.\n", esc.ID) })
	return nil
}

// escalations lists open questions, or the answered ones with --status.
func (e *env) escalations(args []string) error {
	fs := e.flags("escalations")
	status := fs.String("status", "open", "open, answered or all")
	if _, err := fs.need(args, 0, 0, "escalations [--status open|answered|all]"); err != nil {
		return err
	}
	c, err := conn()
	if err != nil {
		return err
	}
	var list []api.Escalation
	if err := c.Get("/v1/escalations?status="+*status, &list); err != nil {
		return err
	}
	e.print(list, func() {
		if len(list) == 0 {
			fmt.Fprintln(e.out, "No escalations.")
			return
		}
		for _, x := range list {
			state := "open"
			if x.Answer != nil {
				state = "answered: " + *x.Answer
			}
			opts := ""
			if len(x.Options) > 0 {
				opts = "  [" + strings.Join(x.Options, " / ") + "]"
			}
			fmt.Fprintf(e.out, "#%-3d %s asks (%s): %s%s\n", x.ID, x.From, state, x.Question, opts)
		}
	})
	return nil
}

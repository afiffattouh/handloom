package cli

import (
	"fmt"
	"strconv"
	"strings"

	"handloom/internal/api"
)

// lesson lets a finished job teach: the lead proposes a short addition to one
// skill, a person accepts or rejects it, and accepting makes a new version of
// the profile that agents started afterwards have.
//
//	handloom lesson propose --profile coder --skill test-first --why "..." "text to add"
//	handloom lesson list [--status proposed|accepted|rejected] [--job N]
//	handloom lesson accept <id>
//	handloom lesson reject <id> [--reason "..."]
func (e *env) lesson(args []string) error {
	if len(args) == 0 {
		return usageErr("usage: handloom lesson propose|list|accept|reject (see `handloom help`)")
	}
	sub, rest := args[0], args[1:]
	fs := e.flags("lesson " + sub)
	prof := fs.String("profile", "", "propose: the profile whose skill should change")
	skill := fs.String("skill", "", "propose: the skill (a name from `handloom profiles`)")
	why := fs.String("why", "", "propose: what happened in the job that showed this")
	status := fs.String("status", "", "list: proposed, accepted or rejected")
	job := fs.Int("job", 0, "list: only this job")
	reason := fs.String("reason", "", "reject: why")
	pos, err := fs.parse(rest)
	if err != nil {
		return err
	}
	c, err := conn()
	if err != nil {
		return err
	}
	switch sub {
	case "propose":
		text := strings.TrimSpace(strings.Join(pos, " "))
		if *prof == "" || *skill == "" || *why == "" || text == "" {
			return usageErr("usage: handloom lesson propose --profile P --skill S --why \"what happened\" \"the sentence to add to the skill\"")
		}
		var l api.Lesson
		if err := c.Post("/v1/lessons", api.LessonReq{Profile: *prof, Skill: *skill, Text: text, Why: *why}, &l); err != nil {
			return err
		}
		e.print(l, func() {
			fmt.Fprintf(e.out, "Proposed lesson #%d for %s/%s. Nothing changes until a person accepts it (handloom lesson accept %d, or the Lessons page).\n", l.ID, l.Profile, l.Skill, l.ID)
		})
	case "list":
		q := "/v1/lessons?status=" + *status
		if *job > 0 {
			q += "&job=" + strconv.Itoa(*job)
		}
		var ls []api.Lesson
		if err := c.Get(q, &ls); err != nil {
			return err
		}
		e.print(ls, func() {
			if len(ls) == 0 {
				fmt.Fprintln(e.out, "No lessons.")
			}
			for _, l := range ls {
				fmt.Fprintf(e.out, "#%d  %-9s %s/%s  %s\n      %s\n      why: %s\n", l.ID, l.Status, l.Profile, l.Skill, ago(l.At), l.Text, l.Why)
				if l.Status == "accepted" {
					fmt.Fprintf(e.out, "      now in %s@%d, used by %d agent(s) started since\n", l.Profile, l.NewVersion, l.Used)
				}
			}
		})
	case "accept", "reject":
		if len(pos) != 1 {
			return usageErr("usage: handloom lesson %s <id>", sub)
		}
		var l api.Lesson
		if err := c.Post("/v1/lessons/"+pos[0]+"/"+sub, api.LessonDecideReq{Reason: *reason}, &l); err != nil {
			return err
		}
		e.print(l, func() {
			if l.Status == "accepted" {
				fmt.Fprintf(e.out, "Accepted lesson #%d: %s is now version %d. Agents started from %s from now on have it.\n", l.ID, l.Profile, l.NewVersion, l.Profile)
			} else {
				fmt.Fprintf(e.out, "Rejected lesson #%d.\n", l.ID)
			}
		})
	default:
		return usageErr("usage: handloom lesson propose|list|accept|reject")
	}
	return nil
}

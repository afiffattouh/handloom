package hub

import (
	"net/http"

	"handloom/internal/api"
)

type lessonView struct {
	api.Lesson
	Ago string
}

type lessonsView struct {
	Proposed, Decided []lessonView
	CanDecide         bool
}

func (q *webReq) lessonsPage(status int, errMsg string) error {
	out, err := lessonList(q.c)
	if err != nil {
		return err
	}
	v := &lessonsView{CanDecide: q.c.allow(ActLessonDecide) == nil}
	for _, l := range out.([]api.Lesson) {
		lv := lessonView{Lesson: l, Ago: ago(q.now, l.At)}
		if l.Status == "proposed" {
			v.Proposed = append(v.Proposed, lv)
		} else {
			v.Decided = append(v.Decided, lv)
		}
	}
	notice := map[string]string{"accepted": "Lesson accepted: the profile has a new version.", "rejected": "Lesson rejected."}[q.r.URL.Query().Get("done")]
	q.page(status, "lessons", pageData{Title: "Lessons", Error: errMsg, Notice: notice, Extra: v})
	return nil
}

func (h *Hub) webLessons(q *webReq) error { return q.lessonsPage(200, "") }

func (h *Hub) webLessonDecide(accept bool) func(*webReq) error {
	return func(q *webReq) error {
		if err := q.c.allow(ActLessonDecide); err != nil {
			return q.actionFailed(err)
		}
		if _, err := q.c.decideLesson(accept, q.r.PostForm.Get("reason")); err != nil {
			return q.actionFailed(err)
		}
		done := "rejected"
		if accept {
			done = "accepted"
		}
		http.Redirect(q.w, q.r, "/lessons?done="+done, http.StatusSeeOther)
		return nil
	}
}

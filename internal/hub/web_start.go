package hub

import "handloom/internal/store"

// The Get started page: the order of things as a checklist that ticks itself off. Each step's state
// is read from what the hub already holds (computers, profiles, projects, jobs), so nothing can go
// stale and a person can leave and come back.

func (h *Hub) webStart(q *webReq) error {
	l, err := q.startList()
	if err != nil {
		return err
	}
	q.page(200, "start", pageData{Title: "Get started", Extra: struct {
		*startList
		CanAct bool
	}{l, q.human.Role != store.RoleViewer}})
	return nil
}

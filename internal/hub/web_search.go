package hub

import (
	"strconv"
	"strings"

	"handloom/internal/api"
)

type searchView struct {
	Query string
	Hits  []searchHitView
	Asked bool
}

type searchHitView struct {
	api.SearchHit
	Href string
	Ago  string
	Kind string
}

var searchKinds = map[string]string{"job": "Job", "task": "Work", "handoff": "Handoff note", "answer": "Answered question"}

// webSearch searches what earlier jobs recorded on the hub. A client's notes
// stay on the machine that holds them and are searched there with
// `handloom search`, so the page says so rather than reaching for them.
func (h *Hub) webSearch(q *webReq) error {
	v := &searchView{Query: strings.TrimSpace(q.r.URL.Query().Get("q"))}
	status, errMsg := 200, ""
	if v.Query != "" {
		v.Asked = true
		out, err := searchGet(q.c)
		if err != nil {
			if ae, ok := err.(*apiError); ok {
				status, errMsg = ae.status, ae.msg
			} else {
				return err
			}
		} else {
			for _, hit := range out.([]api.SearchHit) {
				href := "/jobs/" + strconv.FormatInt(hit.Job, 10)
				v.Hits = append(v.Hits, searchHitView{SearchHit: hit, Href: href, Ago: ago(q.now, hit.At), Kind: searchKinds[hit.Kind]})
			}
		}
	}
	q.page(status, "search", pageData{Title: "Search", Error: errMsg, Extra: v})
	return nil
}

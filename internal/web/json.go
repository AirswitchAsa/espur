package web

import (
	"encoding/json"
	"net/http"
)

func jsonUnmarshalImpl(b []byte, out any) error { return json.Unmarshal(b, out) }

// healthz reports liveness for orchestrators / reverse proxies. It is a
// deliberate spec extension beyond observability.dog.md v0.1, which marked
// /healthz as out of scope; usage feedback flipped that. Cheap: queries no
// DB, just reports the in-memory adapter `Healthy()` flag.
//
// 200 if Espur is up and every registered adapter is healthy; 503 if any
// adapter reports unhealthy. The shape of the response stays the same so
// scrapers can read the body either way.
func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	type entry struct {
		Platform string `json:"platform"`
		Healthy  bool   `json:"healthy"`
	}
	out := struct {
		OK       bool    `json:"ok"`
		Adapters []entry `json:"adapters"`
	}{OK: true}
	for _, a := range s.adapters {
		h := a.Healthy()
		out.Adapters = append(out.Adapters, entry{Platform: a.Platform(), Healthy: h})
		if !h {
			out.OK = false
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if !out.OK {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(out)
}

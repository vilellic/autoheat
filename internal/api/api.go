// Package api is the HTTP interface Home Assistant calls.
package api

import (
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/vilellic/autoheat/internal/config"
	"github.com/vilellic/autoheat/internal/control"
)

// Server serves any number of rooms. Each room has its own controller
// state; different rooms are handled in parallel.
type Server struct {
	cfg *config.Config
	log *slog.Logger
	now func() time.Time

	mu    sync.Mutex
	rooms map[string]*room
}

type room struct {
	mu     sync.Mutex
	device string
	ctl    control.Room
}

func New(cfg *config.Config, log *slog.Logger) *Server {
	return &Server{cfg: cfg, log: log, now: time.Now, rooms: map[string]*room{}}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /{$}", s.handleDecide)
	mux.HandleFunc("GET /status", s.handleStatusAll)
	mux.HandleFunc("GET /status/{room}", s.handleStatusRoom)
	return mux
}

// response is the v1 answer shape, which the Home Assistant automation
// relies on: {"HVAC_mode":"heat","Temperature":23,"Fan_mode":"low"}.
type response struct {
	HVACMode    string `json:"HVAC_mode"`
	Temperature int    `json:"Temperature"`
	FanMode     string `json:"Fan_mode"`
}

func (s *Server) handleDecide(w http.ResponseWriter, r *http.Request) {
	var req request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		s.reject(w, req.Room.text, "invalid JSON: "+err.Error())
		return
	}
	p, err := s.parse(req)
	if err != nil {
		s.reject(w, req.Room.text, err.Error())
		return
	}
	for _, warning := range p.warnings {
		s.log.Warn("input tolerated", "room", req.Room.text, "warning", warning)
	}

	rm := s.room(req.Room.text)
	rm.mu.Lock()
	rm.device = p.device.Name
	d := rm.ctl.Decide(p.input, s.cfg.Params(req.Room.text), s.now())
	rm.mu.Unlock()

	resp := response{
		HVACMode:    d.Command.Mode.String(),
		Temperature: d.Command.SetTemp,
		FanMode:     p.device.FanName(d.Command.Fan),
	}
	// An unchanged fan is echoed exactly as reported, so a fan mode Autoheat
	// does not know (such as "auto") is left alone until it changes the fan.
	if d.Command.Fan == p.input.Observed.Fan && p.rawFan != "" {
		resp.FanMode = p.rawFan
	}

	in := p.input
	s.log.Info("decision",
		"room", req.Room.text,
		"target", in.Target,
		"roomTemp", in.RoomTemp,
		"level", round2(d.Estimate.Level),
		"slopePerHour", round2(d.Estimate.SlopePerHour),
		"error", round2(d.Error),
		"predicted", round2(d.Predicted),
		"observed", in.Observed.String(),
		"command", d.Command.String(),
		"changed", d.Changed,
		"reason", d.Reason,
	)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) reject(w http.ResponseWriter, room, reason string) {
	s.log.Warn("request rejected", "room", room, "reason", reason)
	http.Error(w, reason, http.StatusBadRequest)
}

func (s *Server) room(name string) *room {
	s.mu.Lock()
	defer s.mu.Unlock()
	rm, ok := s.rooms[name]
	if !ok {
		rm = &room{}
		s.rooms[name] = rm
	}
	return rm
}

type roomStatus struct {
	Room   string `json:"room"`
	Device string `json:"device"`
	control.Status
}

func (s *Server) status(name string) (roomStatus, bool) {
	s.mu.Lock()
	rm, ok := s.rooms[name]
	s.mu.Unlock()
	if !ok {
		return roomStatus{}, false
	}
	rm.mu.Lock()
	defer rm.mu.Unlock()
	return roomStatus{Room: name, Device: rm.device, Status: rm.ctl.Status()}, true
}

func (s *Server) handleStatusAll(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	names := make([]string, 0, len(s.rooms))
	for name := range s.rooms {
		names = append(names, name)
	}
	s.mu.Unlock()
	slices.Sort(names)

	all := []roomStatus{}
	for _, name := range names {
		if st, ok := s.status(name); ok {
			all = append(all, st)
		}
	}
	writeJSON(w, all)
}

func (s *Server) handleStatusRoom(w http.ResponseWriter, r *http.Request) {
	st, ok := s.status(r.PathValue("room"))
	if !ok {
		http.Error(w, "unknown room", http.StatusNotFound)
		return
	}
	writeJSON(w, st)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

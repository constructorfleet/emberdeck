package server

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
)

// Rectangle is editable lower-left-origin geometry in millimetres.
type Rectangle struct {
	XMM      float64 `json:"x_mm"`
	YMM      float64 `json:"y_mm"`
	WidthMM  float64 `json:"width_mm"`
	HeightMM float64 `json:"height_mm"`
}

// DesignerOperation describes a single initial vector engraving operation.
type DesignerOperation struct {
	Type         string  `json:"type"`
	FeedMMMin    float64 `json:"feed_mm_min"`
	PowerPercent float64 `json:"power_percent"`
}

// DesignerProject keeps editable source and references a specific profile revision.
type DesignerProject struct {
	SchemaVersion   int               `json:"schema_version"`
	ID              string            `json:"id"`
	Revision        int               `json:"revision"`
	Name            string            `json:"name"`
	ProfileID       string            `json:"profile_id"`
	ProfileRevision int               `json:"profile_revision"`
	Rectangle       Rectangle         `json:"rectangle"`
	Operation       DesignerOperation `json:"operation"`
	ProfileStale    bool              `json:"profile_stale"`
}

type projectRequest struct {
	SchemaVersion    int               `json:"schema_version"`
	Name             string            `json:"name"`
	ProfileID        string            `json:"profile_id"`
	ProfileRevision  int               `json:"profile_revision"`
	Rectangle        Rectangle         `json:"rectangle"`
	Operation        DesignerOperation `json:"operation"`
	Revision         json.RawMessage   `json:"revision"`
	ExpectedRevision int               `json:"-"`
}

func (s *Server) saveProjects(projects map[string]DesignerProject) error {
	if s.closed {
		return errors.New("server is closed")
	}
	if s.storageErr != nil {
		return s.storageErr
	}
	published, err := durableJSON(filepath.Join(s.dir, "designs.json"), projects)
	if published && err != nil {
		s.storageErr = err
	}
	if err != nil {
		return err
	}
	s.projects = projects
	return nil
}

func validStoredProject(project DesignerProject, profiles map[string]MachineProfile) bool {
	if project.SchemaVersion != 1 || project.Name == "" || project.Name != strings.TrimSpace(project.Name) || len(project.Name) > 120 ||
		!validHexID(project.ProfileID) || project.ProfileRevision < 1 || !validProjectIntrinsic(project) {
		return false
	}
	profile, ok := profiles[project.ProfileID]
	if !ok || project.ProfileRevision > profile.Revision {
		return false
	}
	if project.ProfileRevision < profile.Revision {
		return true
	}
	input := projectRequest{SchemaVersion: project.SchemaVersion, Name: project.Name, ProfileID: project.ProfileID, ProfileRevision: project.ProfileRevision, Rectangle: project.Rectangle, Operation: project.Operation}
	return validateProjectValues(input, profile) == nil
}

func validProjectIntrinsic(project DesignerProject) bool {
	g, op := project.Rectangle, project.Operation
	return finite(g.XMM) && finite(g.YMM) && finite(g.WidthMM) && finite(g.HeightMM) && finite(op.FeedMMMin) && finite(op.PowerPercent) &&
		validPrecision(g.XMM) && validPrecision(g.YMM) && validPrecision(g.WidthMM) && validPrecision(g.HeightMM) && validPrecision(op.FeedMMMin) && validPrecision(op.PowerPercent) &&
		g.XMM >= 0 && g.YMM >= 0 && g.WidthMM > 0 && g.HeightMM > 0 && g.XMM <= 10000 && g.YMM <= 10000 && g.WidthMM <= 10000 && g.HeightMM <= 10000 &&
		milli(g.XMM)+milli(g.WidthMM) <= 10_000_000 && milli(g.YMM)+milli(g.HeightMM) <= 10_000_000 &&
		op.Type == "vector-engrave" && op.FeedMMMin > 0 && op.FeedMMMin <= 100000 && op.PowerPercent > 0 && op.PowerPercent <= 100
}

func validateProjectInput(w http.ResponseWriter, r *http.Request, updating bool) (projectRequest, bool) {
	var input projectRequest
	if !decodeBounded(w, r, &input, 64<<10) {
		return input, false
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.SchemaVersion != 1 || input.Name == "" || len(input.Name) > 120 {
		fail(w, 400, "invalid designer project")
		return input, false
	}
	if !parseRevisionField(input.Revision, updating, &input.ExpectedRevision) {
		fail(w, 400, "invalid project revision")
		return input, false
	}
	g := input.Rectangle
	op := input.Operation
	if op.Type != "vector-engrave" || !finite(g.XMM) || !finite(g.YMM) || !finite(g.WidthMM) || !finite(g.HeightMM) || !finite(op.FeedMMMin) || !finite(op.PowerPercent) ||
		!validPrecision(g.XMM) || !validPrecision(g.YMM) || !validPrecision(g.WidthMM) || !validPrecision(g.HeightMM) || !validPrecision(op.FeedMMMin) || !validPrecision(op.PowerPercent) ||
		g.XMM < 0 || g.YMM < 0 || g.WidthMM <= 0 || g.HeightMM <= 0 || g.XMM > 10000 || g.YMM > 10000 || g.WidthMM > 10000 || g.HeightMM > 10000 || op.FeedMMMin <= 0 || op.FeedMMMin > 100000 || op.PowerPercent <= 0 || op.PowerPercent > 100 {
		fail(w, 400, "rectangle or vector engraving settings are outside the declared profile limits")
		return input, false
	}
	return input, true
}

func powerS(maxS int64, percent float64) int64 {
	percentMilli := milli(percent)
	return (maxS*percentMilli + 50_000) / 100_000
}

func milli(value float64) int64 { return int64(math.Round(value * 1000)) }

func copyProjects(projects map[string]DesignerProject) map[string]DesignerProject {
	copy := make(map[string]DesignerProject, len(projects))
	for id, project := range projects {
		copy[id] = project
	}
	return copy
}

func (s *Server) projectView(project DesignerProject) DesignerProject {
	profile, ok := s.profiles[project.ProfileID]
	project.ProfileStale = !ok || profile.Revision != project.ProfileRevision
	return project
}

func (s *Server) projectList() []DesignerProject {
	ids := make([]string, 0, len(s.projects))
	for id := range s.projects {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	projects := make([]DesignerProject, 0, len(ids))
	for _, id := range ids {
		projects = append(projects, s.projectView(s.projects[id]))
	}
	return projects
}

func (s *Server) listProjects(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	respond(w, 200, s.projectList())
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	input, ok := validateProjectInput(w, r, false)
	if !ok {
		return
	}
	id, err := randomID()
	if err != nil {
		fail(w, 500, "could not allocate project ID")
		return
	}
	project := DesignerProject{SchemaVersion: input.SchemaVersion, ID: id, Revision: 1, Name: input.Name, ProfileID: input.ProfileID, ProfileRevision: input.ProfileRevision, Rectangle: input.Rectangle, Operation: input.Operation}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Profile validation is repeated under the mutation lock to prevent a concurrent update.
	profile, exists := s.profiles[input.ProfileID]
	if !exists || profile.Revision != input.ProfileRevision {
		fail(w, 409, "profile is missing or its revision changed; review and select the current revision")
		return
	}
	if err := validateProjectValues(input, profile); err != nil {
		fail(w, 400, err.Error())
		return
	}
	projects := copyProjects(s.projects)
	projects[id] = project
	if err := s.saveProjects(projects); err != nil {
		fail(w, 500, "could not persist project")
		return
	}
	w.Header().Set("Location", "/api/v1/projects/"+id)
	respond(w, 201, project)
}

func validateProjectValues(input projectRequest, profile MachineProfile) error {
	g, op := input.Rectangle, input.Operation
	if op.Type != "vector-engrave" || !finite(g.XMM) || !finite(g.YMM) || !finite(g.WidthMM) || !finite(g.HeightMM) || !finite(op.FeedMMMin) || !finite(op.PowerPercent) ||
		!validPrecision(g.XMM) || !validPrecision(g.YMM) || !validPrecision(g.WidthMM) || !validPrecision(g.HeightMM) || !validPrecision(op.FeedMMMin) || !validPrecision(op.PowerPercent) ||
		g.XMM < 0 || g.YMM < 0 || g.WidthMM <= 0 || g.HeightMM <= 0 || g.XMM > 10000 || g.YMM > 10000 || g.WidthMM > 10000 || g.HeightMM > 10000 || milli(g.XMM)+milli(g.WidthMM) > milli(profile.WidthMM) || milli(g.YMM)+milli(g.HeightMM) > milli(profile.HeightMM) ||
		op.FeedMMMin < profile.MinFeed || op.FeedMMMin > profile.MaxFeed || op.PowerPercent <= 0 || op.PowerPercent > profile.MaxPowerPct || powerS(profile.MaxS, op.PowerPercent) < 1 {
		return errors.New("rectangle or vector engraving settings are outside the declared profile limits")
	}
	return nil
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	project, ok := s.projects[r.PathValue("id")]
	if !ok {
		fail(w, 404, "project not found")
		return
	}
	respond(w, 200, s.projectView(project))
}

func (s *Server) updateProject(w http.ResponseWriter, r *http.Request) {
	input, ok := validateProjectInput(w, r, true)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	current, exists := s.projects[id]
	if !exists {
		fail(w, 404, "project not found")
		return
	}
	if input.ExpectedRevision != current.Revision {
		fail(w, 409, "project revision conflict")
		return
	}
	latest, exists := s.profiles[input.ProfileID]
	if !exists || latest.Revision != input.ProfileRevision {
		fail(w, 409, "profile is missing or its revision changed; review and select the current revision")
		return
	}
	if err := validateProjectValues(input, latest); err != nil {
		fail(w, 400, err.Error())
		return
	}
	updated := DesignerProject{SchemaVersion: input.SchemaVersion, ID: id, Revision: current.Revision + 1, Name: input.Name, ProfileID: input.ProfileID, ProfileRevision: input.ProfileRevision, Rectangle: input.Rectangle, Operation: input.Operation}
	projects := copyProjects(s.projects)
	projects[id] = updated
	if err := s.saveProjects(projects); err != nil {
		fail(w, 500, "could not persist project")
		return
	}
	respond(w, 200, updated)
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Revision int `json:"revision"`
	}
	if !decodeBounded(w, r, &input, 64<<10) {
		return
	}
	if input.Revision < 1 {
		fail(w, 400, "positive revision is required")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	current, ok := s.projects[id]
	if !ok {
		fail(w, 404, "project not found")
		return
	}
	if input.Revision != current.Revision {
		fail(w, 409, "project revision conflict")
		return
	}
	projects := copyProjects(s.projects)
	delete(projects, id)
	if err := s.saveProjects(projects); err != nil {
		fail(w, 500, "could not persist project")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

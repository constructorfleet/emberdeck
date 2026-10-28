package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"
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
	UpdatedAt       *time.Time        `json:"updated_at,omitempty"`
	UpdatedBy       string            `json:"updated_by,omitempty"`
	Name            string            `json:"name"`
	ProfileID       string            `json:"profile_id"`
	ProfileRevision int               `json:"profile_revision"`
	Rectangle       Rectangle         `json:"rectangle,omitempty"`
	Operation       DesignerOperation `json:"operation,omitempty"`
	Units           string            `json:"units,omitempty"`
	Objects         []VectorObject    `json:"objects,omitempty"`
	Operations      []VectorOperation `json:"operations,omitempty"`
	ProfileStale    bool              `json:"profile_stale"`
}

func (project DesignerProject) MarshalJSON() ([]byte, error) {
	type projectJSON struct {
		SchemaVersion   int                `json:"schema_version"`
		ID              string             `json:"id"`
		Revision        int                `json:"revision"`
		UpdatedAt       *time.Time         `json:"updated_at,omitempty"`
		UpdatedBy       string             `json:"updated_by,omitempty"`
		Name            string             `json:"name"`
		ProfileID       string             `json:"profile_id"`
		ProfileRevision int                `json:"profile_revision"`
		Rectangle       *Rectangle         `json:"rectangle,omitempty"`
		Operation       *DesignerOperation `json:"operation,omitempty"`
		Units           string             `json:"units,omitempty"`
		Objects         []VectorObject     `json:"objects,omitempty"`
		Operations      *[]VectorOperation `json:"operations,omitempty"`
		ProfileStale    bool               `json:"profile_stale"`
	}
	encoded := projectJSON{SchemaVersion: project.SchemaVersion, ID: project.ID, Revision: project.Revision, UpdatedAt: project.UpdatedAt, UpdatedBy: project.UpdatedBy, Name: project.Name,
		ProfileID: project.ProfileID, ProfileRevision: project.ProfileRevision, ProfileStale: project.ProfileStale}
	if project.SchemaVersion == 1 {
		encoded.Rectangle, encoded.Operation = &project.Rectangle, &project.Operation
	} else {
		encoded.Units, encoded.Objects, encoded.Operations = project.Units, project.Objects, &project.Operations
		if *encoded.Operations == nil {
			empty := []VectorOperation{}
			encoded.Operations = &empty
		}
	}
	return json.Marshal(encoded)
}

// VectorPoint is a lower-left-origin work coordinate in millimetres.
type VectorPoint struct {
	XMM float64 `json:"x_mm"`
	YMM float64 `json:"y_mm"`
}

// VectorSubpath is a line path; closed paths return to their first point.
type VectorSubpath struct {
	Points []VectorPoint `json:"points"`
	Closed bool          `json:"closed"`
}

// VectorTransform is saved in the editable project and applies scale and
// rotation about the source geometry's center, followed by a millimetre move.
// Omitted fields use identity values so clients can send only fields they need.
type VectorTransform struct {
	TranslateXMM float64 `json:"translate_x_mm"`
	TranslateYMM float64 `json:"translate_y_mm"`
	ScaleX       float64 `json:"scale_x"`
	ScaleY       float64 `json:"scale_y"`
	RotationDeg  float64 `json:"rotation_deg"`
}

func (transform *VectorTransform) UnmarshalJSON(data []byte) error {
	type transformInput struct {
		TranslateXMM *float64 `json:"translate_x_mm"`
		TranslateYMM *float64 `json:"translate_y_mm"`
		ScaleX       *float64 `json:"scale_x"`
		ScaleY       *float64 `json:"scale_y"`
		RotationDeg  *float64 `json:"rotation_deg"`
	}
	var input transformInput
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return err
	}
	*transform = VectorTransform{ScaleX: 1, ScaleY: 1}
	if input.TranslateXMM != nil {
		transform.TranslateXMM = *input.TranslateXMM
	}
	if input.TranslateYMM != nil {
		transform.TranslateYMM = *input.TranslateYMM
	}
	if input.ScaleX != nil {
		transform.ScaleX = *input.ScaleX
	}
	if input.ScaleY != nil {
		transform.ScaleY = *input.ScaleY
	}
	if input.RotationDeg != nil {
		transform.RotationDeg = *input.RotationDeg
	}
	return nil
}

// VectorObject keeps editable geometry independent from machine operation settings.
type VectorObject struct {
	Kind        string           `json:"kind"`
	ID          string           `json:"id"`
	OperationID string           `json:"operation_id,omitempty"`
	Text        *VectorText      `json:"text,omitempty"`
	Transform   *VectorTransform `json:"transform,omitempty"`
	Subpaths    []VectorSubpath  `json:"subpaths"`
}

// VectorOperation is an explicitly ordered vector cutting or engraving layer.
type VectorOperation struct {
	ID           string  `json:"id"`
	Type         string  `json:"type"`
	FeedMMMin    float64 `json:"feed_mm_min"`
	PowerPercent float64 `json:"power_percent"`
}

type projectRequest struct {
	SchemaVersion    int               `json:"schema_version"`
	Name             string            `json:"name"`
	ProfileID        string            `json:"profile_id"`
	ProfileRevision  int               `json:"profile_revision"`
	Rectangle        Rectangle         `json:"rectangle"`
	Operation        DesignerOperation `json:"operation"`
	Units            string            `json:"units,omitempty"`
	Objects          []VectorObject    `json:"objects,omitempty"`
	Operations       []VectorOperation `json:"operations,omitempty"`
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
		s.failStorage(err)
	}
	if err != nil {
		return err
	}
	s.projects = projects
	return nil
}

func validStoredProject(project DesignerProject, profiles map[string]MachineProfile) bool {
	if (project.SchemaVersion != 1 && project.SchemaVersion != 2) || project.Name == "" || project.Name != strings.TrimSpace(project.Name) || len(project.Name) > 120 ||
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
	return validateProjectValues(projectRequestFromProject(project), profile) == nil
}

func validProjectIntrinsic(project DesignerProject) bool {
	if project.SchemaVersion == 2 {
		return validVectorProjectIntrinsic(project)
	}
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
	if (input.SchemaVersion != 1 && input.SchemaVersion != 2) || input.Name == "" || len(input.Name) > 120 {
		fail(w, 400, "invalid designer project")
		return input, false
	}
	if !parseRevisionField(input.Revision, updating, &input.ExpectedRevision) {
		fail(w, 400, "invalid project revision")
		return input, false
	}
	if input.SchemaVersion == 2 {
		for i := range input.Objects {
			if input.Objects[i].Kind != "text" {
				continue
			}
			if input.Objects[i].Text == nil {
				fail(w, 400, "text source is required")
				return input, false
			}
			paths, err := generateMonolineText(*input.Objects[i].Text)
			if err != nil {
				fail(w, 400, err.Error())
				return input, false
			}
			// Server-generated paths are canonical so clients cannot submit text
			// source that disagrees with the geometry saved or later dispatched.
			input.Objects[i].Subpaths = paths
		}
	}
	project := DesignerProject{SchemaVersion: input.SchemaVersion, Name: input.Name, ProfileID: input.ProfileID, ProfileRevision: input.ProfileRevision, Rectangle: input.Rectangle, Operation: input.Operation, Units: input.Units, Objects: input.Objects, Operations: input.Operations}
	if !validProjectIntrinsic(project) {
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
	now := time.Now().UTC()
	project := DesignerProject{SchemaVersion: input.SchemaVersion, ID: id, Revision: 1, UpdatedAt: &now, UpdatedBy: requestPrincipal(r).Name, Name: input.Name, ProfileID: input.ProfileID, ProfileRevision: input.ProfileRevision, Rectangle: input.Rectangle, Operation: input.Operation, Units: input.Units, Objects: input.Objects, Operations: input.Operations}
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
	if input.SchemaVersion == 2 {
		project := DesignerProject{SchemaVersion: 2, Name: input.Name, ProfileID: input.ProfileID, ProfileRevision: input.ProfileRevision, Units: input.Units, Objects: input.Objects, Operations: input.Operations}
		return validateVectorProjectValues(project, profile, false)
	}
	g, op := input.Rectangle, input.Operation
	if op.Type != "vector-engrave" || !finite(g.XMM) || !finite(g.YMM) || !finite(g.WidthMM) || !finite(g.HeightMM) || !finite(op.FeedMMMin) || !finite(op.PowerPercent) ||
		!validPrecision(g.XMM) || !validPrecision(g.YMM) || !validPrecision(g.WidthMM) || !validPrecision(g.HeightMM) || !validPrecision(op.FeedMMMin) || !validPrecision(op.PowerPercent) ||
		g.XMM < 0 || g.YMM < 0 || g.WidthMM <= 0 || g.HeightMM <= 0 || g.XMM > 10000 || g.YMM > 10000 || g.WidthMM > 10000 || g.HeightMM > 10000 || milli(g.XMM)+milli(g.WidthMM) > milli(profile.WidthMM) || milli(g.YMM)+milli(g.HeightMM) > milli(profile.HeightMM) ||
		op.FeedMMMin < profile.MinFeed || op.FeedMMMin > profile.MaxFeed || op.PowerPercent <= 0 || op.PowerPercent > profile.MaxPowerPct || powerS(profile.MaxS, op.PowerPercent) < 1 {
		return errors.New("rectangle or vector engraving settings are outside the declared profile limits")
	}
	return nil
}

func projectRequestFromProject(project DesignerProject) projectRequest {
	return projectRequest{SchemaVersion: project.SchemaVersion, Name: project.Name, ProfileID: project.ProfileID, ProfileRevision: project.ProfileRevision, Rectangle: project.Rectangle, Operation: project.Operation, Units: project.Units, Objects: project.Objects, Operations: project.Operations}
}

func validVectorProjectIntrinsic(project DesignerProject) bool {
	if project.Units != "mm" || len(project.Objects) == 0 || len(project.Objects) > 500 || len(project.Operations) > 100 {
		return false
	}
	operationIDs := make(map[string]string, len(project.Operations))
	for _, operation := range project.Operations {
		_, duplicate := operationIDs[operation.ID]
		if !validObjectID(operation.ID) || duplicate ||
			(operation.Type != "vector-engrave" && operation.Type != "vector-cut") ||
			!finite(operation.FeedMMMin) || !finite(operation.PowerPercent) ||
			!validPrecision(operation.FeedMMMin) || !validPrecision(operation.PowerPercent) ||
			operation.FeedMMMin <= 0 || operation.FeedMMMin > 100000 || operation.PowerPercent <= 0 || operation.PowerPercent > 100 {
			return false
		}
		operationIDs[operation.ID] = operation.Type
	}
	objectIDs := make(map[string]bool, len(project.Objects))
	totalPoints := 0
	for _, object := range project.Objects {
		if !validObjectID(object.ID) || objectIDs[object.ID] ||
			(object.Kind != "rectangle" && object.Kind != "ellipse" && object.Kind != "line" && object.Kind != "path" && object.Kind != "text") ||
			len(object.Subpaths) == 0 || len(object.Subpaths) > 500 || !validVectorTransform(object.Transform) {
			return false
		}
		if object.Kind == "text" {
			if object.Text == nil {
				return false
			}
			generated, err := generateMonolineText(*object.Text)
			if err != nil || !sameVectorSubpaths(object.Subpaths, generated) {
				return false
			}
		} else if object.Text != nil {
			return false
		}
		if object.Kind == "rectangle" && (len(object.Subpaths) != 1 || !object.Subpaths[0].Closed || len(object.Subpaths[0].Points) != 4) ||
			object.Kind == "ellipse" && (len(object.Subpaths) != 1 || !object.Subpaths[0].Closed || len(object.Subpaths[0].Points) < 8) ||
			object.Kind == "line" && (len(object.Subpaths) != 1 || object.Subpaths[0].Closed || len(object.Subpaths[0].Points) != 2) {
			return false
		}
		objectIDs[object.ID] = true
		if object.OperationID != "" {
			if _, exists := operationIDs[object.OperationID]; !exists {
				return false
			}
		}
		for _, path := range object.Subpaths {
			minimum := 2
			if path.Closed {
				minimum = 3
			}
			if len(path.Points) < minimum {
				return false
			}
			totalPoints += len(path.Points)
			if totalPoints > 4000 {
				return false
			}
			for i, point := range path.Points {
				if !finite(point.XMM) || !finite(point.YMM) || !validPrecision(point.XMM) || !validPrecision(point.YMM) || point.XMM < 0 || point.YMM < 0 || point.XMM > 10000 || point.YMM > 10000 {
					return false
				}
				if i > 0 && milli(point.XMM) == milli(path.Points[i-1].XMM) && milli(point.YMM) == milli(path.Points[i-1].YMM) {
					return false
				}
			}
			if path.Closed {
				first, last := path.Points[0], path.Points[len(path.Points)-1]
				if milli(first.XMM) == milli(last.XMM) && milli(first.YMM) == milli(last.YMM) {
					return false
				}
			}
		}
		transformed := transformedVectorSubpaths(object)
		for _, path := range transformed {
			for i, point := range path.Points {
				if !finite(point.XMM) || !finite(point.YMM) || !validPrecision(point.XMM) || !validPrecision(point.YMM) || point.XMM < 0 || point.YMM < 0 || point.XMM > 10000 || point.YMM > 10000 {
					return false
				}
				if i > 0 && milli(point.XMM) == milli(path.Points[i-1].XMM) && milli(point.YMM) == milli(path.Points[i-1].YMM) {
					return false
				}
			}
			if path.Closed {
				first, last := path.Points[0], path.Points[len(path.Points)-1]
				if milli(first.XMM) == milli(last.XMM) && milli(first.YMM) == milli(last.YMM) {
					return false
				}
			}
			if object.Kind != "text" && object.OperationID != "" && operationIDs[object.OperationID] == "vector-cut" && (!path.Closed || !nonzeroPathArea(path.Points)) {
				return false
			}
		}
	}
	return true
}

func sameVectorSubpaths(left, right []VectorSubpath) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].Closed != right[i].Closed || len(left[i].Points) != len(right[i].Points) {
			return false
		}
		for j := range left[i].Points {
			if left[i].Points[j] != right[i].Points[j] {
				return false
			}
		}
	}
	return true
}

func validVectorTransform(transform *VectorTransform) bool {
	if transform == nil {
		return true
	}
	return finite(transform.TranslateXMM) && finite(transform.TranslateYMM) &&
		finite(transform.ScaleX) && finite(transform.ScaleY) && finite(transform.RotationDeg) &&
		validPrecision(transform.TranslateXMM) && validPrecision(transform.TranslateYMM) &&
		validPrecision(transform.ScaleX) && validPrecision(transform.ScaleY) && validPrecision(transform.RotationDeg) &&
		transform.TranslateXMM >= -10000 && transform.TranslateXMM <= 10000 &&
		transform.TranslateYMM >= -10000 && transform.TranslateYMM <= 10000 &&
		transform.ScaleX >= 0.01 && transform.ScaleX <= 100 &&
		transform.ScaleY >= 0.01 && transform.ScaleY <= 100 &&
		transform.RotationDeg >= -360 && transform.RotationDeg <= 360
}

func transformedVectorSubpaths(object VectorObject) []VectorSubpath {
	if object.Transform == nil {
		return object.Subpaths
	}
	var minX, maxX, minY, maxY float64
	firstPoint := true
	for _, path := range object.Subpaths {
		for _, point := range path.Points {
			if firstPoint {
				minX, maxX, minY, maxY = point.XMM, point.XMM, point.YMM, point.YMM
				firstPoint = false
				continue
			}
			minX = math.Min(minX, point.XMM)
			maxX = math.Max(maxX, point.XMM)
			minY = math.Min(minY, point.YMM)
			maxY = math.Max(maxY, point.YMM)
		}
	}
	if firstPoint {
		return object.Subpaths
	}
	centerX, centerY := (minX+maxX)/2, (minY+maxY)/2
	transform := *object.Transform
	if transform.ScaleX == 0 {
		transform.ScaleX = 1
	}
	if transform.ScaleY == 0 {
		transform.ScaleY = 1
	}
	angle := transform.RotationDeg * math.Pi / 180
	cosine, sine := math.Cos(angle), math.Sin(angle)
	round := func(value float64) float64 { return math.Round(value*1000) / 1000 }
	transformed := make([]VectorSubpath, len(object.Subpaths))
	for pathIndex, path := range object.Subpaths {
		transformed[pathIndex] = VectorSubpath{Closed: path.Closed, Points: make([]VectorPoint, len(path.Points))}
		for pointIndex, point := range path.Points {
			x := (point.XMM - centerX) * transform.ScaleX
			y := (point.YMM - centerY) * transform.ScaleY
			transformed[pathIndex].Points[pointIndex] = VectorPoint{
				XMM: round(centerX + cosine*x - sine*y + transform.TranslateXMM),
				YMM: round(centerY + sine*x + cosine*y + transform.TranslateYMM),
			}
		}
	}
	return transformed
}

func validObjectID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func nonzeroPathArea(points []VectorPoint) bool {
	var twiceArea int64
	for i, point := range points {
		next := points[(i+1)%len(points)]
		twiceArea += milli(point.XMM)*milli(next.YMM) - milli(next.XMM)*milli(point.YMM)
	}
	return twiceArea != 0
}

func validateVectorProjectValues(project DesignerProject, profile MachineProfile, requireAssigned bool) error {
	if !validVectorProjectIntrinsic(project) {
		return errors.New("invalid vector project geometry or operations")
	}
	operations := make(map[string]VectorOperation, len(project.Operations))
	for _, operation := range project.Operations {
		if operation.FeedMMMin < profile.MinFeed || operation.FeedMMMin > profile.MaxFeed || operation.PowerPercent > profile.MaxPowerPct || powerS(profile.MaxS, operation.PowerPercent) < 1 {
			return errors.New("vector operation feed or power is outside the declared profile limits")
		}
		operations[operation.ID] = operation
	}
	for _, object := range project.Objects {
		for _, path := range transformedVectorSubpaths(object) {
			for _, point := range path.Points {
				if milli(point.XMM) > milli(profile.WidthMM) || milli(point.YMM) > milli(profile.HeightMM) {
					return errors.New("vector geometry is outside the declared workspace")
				}
			}
		}
		if object.OperationID == "" {
			if requireAssigned {
				return errors.New("assign every vector object to an operation before generation")
			}
			continue
		}
		if object.Kind == "text" && operations[object.OperationID].Type == "vector-cut" {
			return errors.New("text objects can only be engraved")
		}
		for _, path := range object.Subpaths {
			if operations[object.OperationID].Type == "vector-cut" && !path.Closed {
				return errors.New("vector cutting requires closed paths")
			}
		}
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
		respond(w, http.StatusConflict, map[string]any{"error": "project revision conflict", "current": s.projectView(current)})
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
	now := time.Now().UTC()
	updated := DesignerProject{SchemaVersion: input.SchemaVersion, ID: id, Revision: current.Revision + 1, UpdatedAt: &now, UpdatedBy: requestPrincipal(r).Name, Name: input.Name, ProfileID: input.ProfileID, ProfileRevision: input.ProfileRevision, Rectangle: input.Rectangle, Operation: input.Operation, Units: input.Units, Objects: input.Objects, Operations: input.Operations}
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

package server

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ConstructorFleet/emberdeck/driver"
)

const rectangleGeneratorVersion = "rectangle-v1"
const vectorGeneratorVersion = "vector-v1"

// JobProvenance is an immutable value snapshot of designer inputs at generation.
type JobProvenance struct {
	GeneratorVersion string          `json:"generator_version"`
	Project          DesignerProject `json:"project"`
	Profile          MachineProfile  `json:"profile"`
}

func (s *Server) generateProject(w http.ResponseWriter, r *http.Request) {
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
	project, ok := s.projects[r.PathValue("id")]
	if !ok {
		fail(w, 404, "project not found")
		return
	}
	if project.Revision != input.Revision {
		fail(w, 409, "project revision conflict")
		return
	}
	profile, ok := s.profiles[project.ProfileID]
	if !ok || profile.Revision != project.ProfileRevision {
		fail(w, 409, "profile is missing or its revision changed; review and save the project before generation")
		return
	}
	if err := validateProjectValues(projectRequestFromProject(project), profile); err != nil {
		fail(w, 409, "saved project no longer validates against its profile")
		return
	}
	program := generateRectangleProgram(project, profile)
	generatorVersion := rectangleGeneratorVersion
	if project.SchemaVersion == 2 {
		if err := validateVectorProjectValues(project, profile, true); err != nil {
			fail(w, 409, "saved vector project cannot be generated: "+err.Error())
			return
		}
		program = generateVectorProgram(project, profile)
		generatorVersion = vectorGeneratorVersion
	}
	count, err := driver.Validate(program)
	if err != nil {
		fail(w, http.StatusUnprocessableEntity, "generated program failed GRBL validation: "+err.Error())
		return
	}
	id, err := randomID()
	if err != nil {
		fail(w, 500, "could not allocate job ID")
		return
	}
	job := Job{ID: id, Name: project.Name + ".gcode", Gcode: program, State: "queued", Created: time.Now().UTC(), Total: count,
		Provenance: &JobProvenance{GeneratorVersion: generatorVersion, Project: project, Profile: profile}}
	jobs := s.copyJobs()
	jobs[id] = job
	if err := s.save(jobs); err != nil {
		fail(w, 500, "could not persist generated job")
		return
	}
	w.Header().Set("Location", "/api/v1/jobs/"+id)
	respond(w, http.StatusCreated, job)
}

func generateVectorProgram(project DesignerProject, profile MachineProfile) string {
	lines := []string{"M5", "G21", "G90", "G94"}
	for _, operation := range project.Operations {
		for _, object := range project.Objects {
			if object.OperationID != operation.ID {
				continue
			}
			for _, path := range object.Subpaths {
				points := path.Points
				first := points[0]
				lines = append(lines, "G0 X"+formatMilli(milli(first.XMM))+" Y"+formatMilli(milli(first.YMM)))
				// Dynamic laser mode keeps output disabled when motion is stopped,
				// including feed holds; cutting uses the same fail-dark mode.
				lines = append(lines, "M4 S0")
				power := powerS(profile.MaxS, operation.PowerPercent)
				feed := formatMilli(milli(operation.FeedMMMin))
				for i := 1; i < len(points); i++ {
					point := points[i]
					move := "G1 X" + formatMilli(milli(point.XMM)) + " Y" + formatMilli(milli(point.YMM))
					if i == 1 {
						move += " F" + feed + " S" + strconv.FormatInt(power, 10)
					}
					lines = append(lines, move)
				}
				if path.Closed {
					lines = append(lines, "G1 X"+formatMilli(milli(first.XMM))+" Y"+formatMilli(milli(first.YMM)))
				}
				lines = append(lines, "M5")
			}
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

func generateRectangleProgram(project DesignerProject, profile MachineProfile) string {
	g := project.Rectangle
	x0, y0 := milli(g.XMM), milli(g.YMM)
	x1, y1 := x0+milli(g.WidthMM), y0+milli(g.HeightMM)
	feed := milli(project.Operation.FeedMMMin)
	power := powerS(profile.MaxS, project.Operation.PowerPercent)
	lines := []string{
		"M5",
		"G21",
		"G90",
		"G94",
		"G0 X" + formatMilli(x0) + " Y" + formatMilli(y0),
		"M4 S0",
		"G1 X" + formatMilli(x1) + " Y" + formatMilli(y0) + " F" + formatMilli(feed) + " S" + strconv.FormatInt(power, 10),
		"G1 X" + formatMilli(x1) + " Y" + formatMilli(y1),
		"G1 X" + formatMilli(x0) + " Y" + formatMilli(y1),
		"G1 X" + formatMilli(x0) + " Y" + formatMilli(y0),
		"M5",
	}
	return strings.Join(lines, "\n") + "\n"
}

func formatMilli(value int64) string {
	whole, fraction := value/1000, value%1000
	if fraction == 0 {
		return strconv.FormatInt(whole, 10)
	}
	decimal := strconv.Itoa(int(fraction))
	if fraction < 100 {
		decimal = "0" + decimal
	}
	if fraction < 10 {
		decimal = "0" + decimal
	}
	decimal = strings.TrimRight(decimal, "0")
	return strconv.FormatInt(whole, 10) + "." + decimal
}

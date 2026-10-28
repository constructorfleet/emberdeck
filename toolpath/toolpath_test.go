package toolpath

import (
	"math"
	"testing"
)

func TestParseBuildsLinearLaserAndTravelSegments(t *testing.T) {
	program := Parse("G21 G90 G94\nG0 X0 Y0\nM4 S250\nG1 X10 Y0 F600\nG1 X10 Y5\nM5\nG0 X0 Y0\n")
	if !program.Supported || len(program.Segments) != 3 {
		t.Fatalf("parsed program = %#v", program)
	}
	want := []Segment{
		{SourceLine: 4, Start: Point{X: 0, Y: 0}, End: Point{X: 10, Y: 0}, FeedMMMin: 600, LaserOn: true},
		{SourceLine: 5, Start: Point{X: 10, Y: 0}, End: Point{X: 10, Y: 5}, FeedMMMin: 600, LaserOn: true},
		{SourceLine: 7, Start: Point{X: 10, Y: 5}, End: Point{X: 0, Y: 0}, FeedMMMin: 600, Rapid: true},
	}
	for i, segment := range program.Segments {
		if !sameSegment(segment, want[i]) {
			t.Fatalf("segment %d = %#v, want %#v", i, segment, want[i])
		}
	}
}

func TestParseSupportsInchesAndIncrementalMoves(t *testing.T) {
	program := Parse("G20 G90\nG0 X1 Y2\nG91\nM3 S100\nG1 X0.5 Y-1 F10\n")
	if !program.Supported || len(program.Segments) != 1 {
		t.Fatalf("parsed program = %#v", program)
	}
	got := program.Segments[0]
	want := Segment{SourceLine: 5, Start: Point{X: 25.4, Y: 50.8}, End: Point{X: 38.1, Y: 25.4}, FeedMMMin: 254, LaserOn: true}
	if !sameSegment(got, want) {
		t.Fatalf("segment = %#v, want %#v", got, want)
	}
}

func TestParseMarksArcsAndUnknownInitialTravel(t *testing.T) {
	program := Parse("G21 G90\nG2 X20 Y0 I5 J0\nG1 X30 Y0\n")
	if program.Supported {
		t.Fatal("arc must be reported as unsupported")
	}
	if len(program.Segments) != 1 || program.Segments[0].SourceLine != 3 || len(program.Warnings) == 0 {
		t.Fatalf("unsupported arc was drawn as exact geometry: %#v", program)
	}

	partial := Parse("G1 X10 Y10 F300\nG1 X20 Y10\n")
	if !partial.Supported || len(partial.Segments) != 1 {
		t.Fatalf("partial absolute path = %#v", partial)
	}
	if partial.Segments[0].Start != (Point{X: 10, Y: 10}) || partial.Segments[0].End != (Point{X: 20, Y: 10}) {
		t.Fatalf("second segment = %#v", partial.Segments[0])
	}
	if len(partial.Warnings) == 0 {
		t.Fatal("unknown first move origin must be reported")
	}
}

func TestParseDoesNotGuessUnknownMotionAndRejectsOversizedSource(t *testing.T) {
	program := Parse("G0 X0 Y0\nG84 X10 Y10\nG1 X20 Y10\n")
	if program.Supported || program.Complete || len(program.Segments) != 0 {
		t.Fatalf("unknown motion was treated as linear geometry: %#v", program)
	}

	oversized := Parse(string(make([]byte, MaxProgramBytes+1)))
	if oversized.Supported || oversized.Complete || len(oversized.Segments) != 0 || len(oversized.Warnings) == 0 {
		t.Fatalf("oversized source was not rejected: %#v", oversized)
	}
}

func TestParseMarksWorkCoordinateChangesAndUnmodeledMCodesUnsupported(t *testing.T) {
	for _, source := range []string{
		"G21 G90\nG0 X0 Y0\nG54\nG1 X10 Y0\n",
		"G21 G90\nG0 X0 Y0\nM6 T1\nG1 X10 Y0\n",
	} {
		program := Parse(source)
		if program.Supported || len(program.Warnings) == 0 {
			t.Fatalf("unmodeled coordinate/program command was accepted: %#v", program)
		}
	}
}

func TestProgramBoundsUseEveryKnownSegmentEndpoint(t *testing.T) {
	program := Parse("G21 G90\nG0 X0 Y0\nG1 X12 Y4 F300\nG0 X-2 Y9\n")
	bounds, ok := ProgramBounds(program)
	if !ok || bounds.Min != (Point{X: -2, Y: 0}) || bounds.Max != (Point{X: 12, Y: 9}) {
		t.Fatalf("bounds = %#v, known = %v", bounds, ok)
	}
	partial := Parse("M4 S100\nG1 X10 Y10\nG1 X20 Y20")
	if _, ok := ProgramBounds(partial); !ok || partial.Complete {
		t.Fatal("known subset bounds should remain distinct from complete program bounds")
	}
}

func TestTrackerUsesReportedPositionAndLabelsRepeatedPathsAmbiguous(t *testing.T) {
	program := Parse("G21 G90\nG0 X0 Y0\nM4 S200\nG1 X10 Y0 F300\nG1 X10 Y10\n")
	tracker := NewTracker(program)
	progress := tracker.Update(Point{X: 5, Y: 0})
	if progress.Completed != 0 || progress.Active == nil || *progress.Active != 0 || progress.Confidence != "estimated" {
		t.Fatalf("mid-segment report = %#v", progress)
	}
	progress = tracker.Update(Point{X: 10, Y: 0})
	if progress.Completed != 1 || progress.Confidence != "ambiguous" || progress.Active != nil {
		t.Fatalf("segment junction report = %#v", progress)
	}
	progress = tracker.Update(Point{X: 10, Y: 5})
	if progress.Completed < 1 {
		t.Fatalf("completed path count moved backwards after ambiguous motion: %#v", progress)
	}

	repeated := NewTracker(Parse("G21 G90\nG0 X0 Y0\nM4 S200\nG1 X10 Y0 F300\nG0 X0 Y0\nG1 X10 Y0\n"))
	progress = repeated.Update(Point{X: 5, Y: 0})
	if progress.Completed != 0 || progress.Active != nil || progress.Confidence != "ambiguous" {
		t.Fatalf("overlapping repeated path report = %#v", progress)
	}
}

func TestTrackerNeverAdvancesFromAcknowledgementsOrUnsupportedGeometry(t *testing.T) {
	program := Parse("G21 G90\nG0 X0 Y0\nM4 S200\nG1 X10 Y0 F300\n")
	tracker := NewTracker(program)
	unknown := tracker.Update(Point{X: 50, Y: 50})
	if unknown.Completed != 0 || unknown.Active != nil || unknown.Confidence != "unknown" {
		t.Fatalf("off-path position was treated as execution: %#v", unknown)
	}
	unsupported := NewTracker(Parse("G0 X0 Y0\nG2 X10 Y0 I5 J0\n"))
	if got := unsupported.Update(Point{X: 10, Y: 0}); got.Confidence != "unknown" || got.Completed != 0 {
		t.Fatalf("unsupported arc was tracked as linear geometry: %#v", got)
	}
}

func TestTrackerCorrelatesControllerLineMarkersWhenProgramProvidesNWords(t *testing.T) {
	program := Parse("N10 G21 G90\nN20 G0 X0 Y0\nN30 M4 S250\nN40 G1 X10 Y0 F300\nN50 G1 X20 Y0\n")
	tracker := NewTracker(program)
	progress := tracker.UpdateLineNumber(50)
	if progress.Confidence != "reported" || progress.Completed != 1 || progress.Active == nil || *progress.Active != 1 {
		t.Fatalf("line marker progress = %#v", progress)
	}
	if progress := tracker.UpdateLineNumber(40); progress.Completed != 1 {
		t.Fatalf("progress moved backwards on delayed marker: %#v", progress)
	}
}

func TestTrackerCompletesOnlyAfterControllerIdle(t *testing.T) {
	tracker := NewTracker(Parse("G21 G90\nG0 X0 Y0\nM4 S250\nG1 X10 Y0 F300\n"))
	if progress := tracker.Update(Point{X: 5, Y: 0}); progress.Completed != 0 {
		t.Fatalf("mid-move progress = %#v", progress)
	}
	finished := tracker.Finish()
	if finished.Completed != finished.Total || finished.Confidence != "controller_idle" {
		t.Fatalf("Idle completion progress = %#v", finished)
	}
}

func sameSegment(a, b Segment) bool {
	close := func(x, y float64) bool { return math.Abs(x-y) < 0.000001 }
	return a.SourceLine == b.SourceLine && a.LineNumber == b.LineNumber && a.HasLineNumber == b.HasLineNumber &&
		close(a.Start.X, b.Start.X) && close(a.Start.Y, b.Start.Y) &&
		close(a.End.X, b.End.X) && close(a.End.Y, b.End.Y) && close(a.FeedMMMin, b.FeedMMMin) &&
		a.Rapid == b.Rapid && a.LaserOn == b.LaserOn
}

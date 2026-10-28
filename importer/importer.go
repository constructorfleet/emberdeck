// Package importer defines the versioned interface for design and job importers.
// Importers are trusted in-process code; this package does not sandbox them.
package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"regexp"
	"sort"
	"strings"
)

const ProtocolVersion = 1

const (
	MaxSourceBytes  = 1 << 20
	MaxOptionsBytes = 1 << 20
	MaxOutputBytes  = 4 << 20
	MaxWarnings     = 64
)

type OutputType string

const (
	OutputEditableDesign OutputType = "editable_design"
	OutputGCodeJob       OutputType = "gcode_job"
)

// SourceFormat describes one accepted file format, independent of the
// importer implementation that handles it.
type SourceFormat struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Extensions []string `json:"extensions"`
	MediaTypes []string `json:"media_types"`
}

// Manifest advertises an importer's version, supported inputs and outputs,
// and its optional JSON configuration schema.
type Manifest struct {
	ProtocolVersion int             `json:"protocol_version"`
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Version         string          `json:"version"`
	SourceFormats   []SourceFormat  `json:"source_formats"`
	OutputTypes     []OutputType    `json:"output_types"`
	OptionsSchema   json.RawMessage `json:"options_schema,omitempty"`
}

// Request contains bounded source bytes, the selected format and output, and
// an optional JSON object conforming to the importer's OptionsSchema.
type Request struct {
	SourceFormat string          `json:"source_format"`
	OutputType   OutputType      `json:"output_type"`
	Source       []byte          `json:"source"`
	Options      json.RawMessage `json:"options,omitempty"`
}

type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Lossy   bool   `json:"lossy"`
}

// Provenance is filled by Registry after a successful import, so callers can
// identify the exact importer and settings that produced the output.
type Provenance struct {
	ImporterID      string          `json:"importer_id"`
	ImporterVersion string          `json:"importer_version"`
	SourceFormat    string          `json:"source_format"`
	Options         json.RawMessage `json:"options,omitempty"`
}

type Result struct {
	OutputType OutputType      `json:"output_type"`
	Payload    json.RawMessage `json:"payload"`
	Warnings   []Warning       `json:"warnings,omitempty"`
	Provenance Provenance      `json:"provenance"`
}

// Importer converts one supported source representation into one declared
// output. Implementations must honor context cancellation and return JSON
// payloads no larger than MaxOutputBytes.
type Importer interface {
	Manifest() Manifest
	Import(context.Context, Request) (Result, error)
}

type Registry struct {
	byID map[string]registration
	ids  []string
}

type registration struct {
	implementation Importer
	manifest       Manifest
}

var identifier = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// NewRegistry validates metadata once and returns a stable registry sorted by
// importer ID. Duplicate IDs or invalid manifests fail closed.
func NewRegistry(importers ...Importer) (*Registry, error) {
	registry := &Registry{byID: make(map[string]registration, len(importers))}
	for _, implementation := range importers {
		if implementation == nil {
			return nil, errors.New("importer is nil")
		}
		manifest := implementation.Manifest()
		if err := validateManifest(manifest); err != nil {
			return nil, fmt.Errorf("importer %q: %w", manifest.ID, err)
		}
		if _, exists := registry.byID[manifest.ID]; exists {
			return nil, fmt.Errorf("duplicate importer ID %q", manifest.ID)
		}
		registry.byID[manifest.ID] = registration{implementation: implementation, manifest: cloneManifest(manifest)}
		registry.ids = append(registry.ids, manifest.ID)
	}
	sort.Strings(registry.ids)
	return registry, nil
}

// List returns defensive copies of registered capability metadata.
func (r *Registry) List() []Manifest {
	if r == nil {
		return nil
	}
	result := make([]Manifest, 0, len(r.ids))
	for _, id := range r.ids {
		result = append(result, cloneManifest(r.byID[id].manifest))
	}
	return result
}

// Import selects an importer by manifest ID, validates the request and output,
// and attaches the manifest and selected options as provenance.
func (r *Registry) Import(ctx context.Context, id string, request Request) (Result, error) {
	if ctx == nil {
		return Result{}, errors.New("import context is required")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if r == nil {
		return Result{}, errors.New("importer registry is required")
	}
	entry, ok := r.byID[id]
	if !ok {
		return Result{}, fmt.Errorf("unknown importer %q", id)
	}
	manifest := entry.manifest
	if err := validateRequest(manifest, request); err != nil {
		return Result{}, err
	}
	request.Source = append([]byte(nil), request.Source...)
	request.Options = append(json.RawMessage(nil), request.Options...)
	result, err := entry.implementation.Import(ctx, request)
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if !containsOutput(manifest.OutputTypes, result.OutputType) || result.OutputType != request.OutputType {
		return Result{}, fmt.Errorf("importer %q returned undeclared output type %q", id, result.OutputType)
	}
	if len(result.Payload) == 0 || len(result.Payload) > MaxOutputBytes || !json.Valid(result.Payload) {
		return Result{}, fmt.Errorf("importer %q returned an empty, oversized, or invalid JSON payload", id)
	}
	if len(result.Warnings) > MaxWarnings {
		return Result{}, fmt.Errorf("importer %q returned more than %d warnings", id, MaxWarnings)
	}
	for _, warning := range result.Warnings {
		if strings.TrimSpace(warning.Code) == "" || len(warning.Code) > 100 || strings.TrimSpace(warning.Message) == "" || len(warning.Message) > 2048 {
			return Result{}, fmt.Errorf("importer %q returned an invalid warning", id)
		}
	}
	result.Payload = append(json.RawMessage(nil), result.Payload...)
	result.Warnings = append([]Warning(nil), result.Warnings...)
	result.Provenance = Provenance{
		ImporterID:      manifest.ID,
		ImporterVersion: manifest.Version,
		SourceFormat:    request.SourceFormat,
		Options:         append(json.RawMessage(nil), request.Options...),
	}
	return result, nil
}

func validateManifest(manifest Manifest) error {
	if manifest.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("unsupported protocol version %d", manifest.ProtocolVersion)
	}
	if !identifier.MatchString(manifest.ID) || strings.TrimSpace(manifest.Name) == "" || len(manifest.Name) > 120 || strings.TrimSpace(manifest.Version) == "" || len(manifest.Version) > 64 {
		return errors.New("invalid importer identity or version")
	}
	if len(manifest.SourceFormats) == 0 || len(manifest.SourceFormats) > 32 || len(manifest.OutputTypes) == 0 || len(manifest.OutputTypes) > 8 {
		return errors.New("manifest must declare bounded source formats and output types")
	}
	formats := make(map[string]bool, len(manifest.SourceFormats))
	for _, format := range manifest.SourceFormats {
		if !identifier.MatchString(format.ID) || strings.TrimSpace(format.Name) == "" || len(format.Name) > 120 || formats[format.ID] || len(format.Extensions) == 0 || len(format.Extensions) > 32 || len(format.MediaTypes) == 0 || len(format.MediaTypes) > 32 {
			return fmt.Errorf("invalid or duplicate source format %q", format.ID)
		}
		formats[format.ID] = true
		for _, extension := range format.Extensions {
			if len(extension) < 2 || len(extension) > 16 || extension[0] != '.' || strings.ContainsAny(extension, "/\\\x00") {
				return fmt.Errorf("invalid extension %q", extension)
			}
		}
		for _, mediaType := range format.MediaTypes {
			parsed, _, err := mime.ParseMediaType(mediaType)
			if err != nil || parsed != mediaType || len(mediaType) > 128 {
				return fmt.Errorf("invalid media type %q", mediaType)
			}
		}
	}
	outputs := make(map[OutputType]bool, len(manifest.OutputTypes))
	for _, output := range manifest.OutputTypes {
		if !identifier.MatchString(string(output)) || outputs[output] {
			return fmt.Errorf("invalid or duplicate output type %q", output)
		}
		outputs[output] = true
	}
	if len(manifest.OptionsSchema) > MaxOptionsBytes {
		return errors.New("options schema exceeds 1 MiB")
	}
	if len(manifest.OptionsSchema) != 0 {
		var schema map[string]json.RawMessage
		if err := json.Unmarshal(manifest.OptionsSchema, &schema); err != nil || schema == nil {
			return errors.New("options schema must be a JSON object")
		}
	}
	return nil
}

func validateRequest(manifest Manifest, request Request) error {
	if len(request.Source) == 0 || len(request.Source) > MaxSourceBytes {
		return fmt.Errorf("source must contain 1 byte to %d bytes", MaxSourceBytes)
	}
	if len(request.Options) > MaxOptionsBytes {
		return fmt.Errorf("options exceed %d bytes", MaxOptionsBytes)
	}
	if len(request.Options) != 0 {
		var options map[string]json.RawMessage
		if err := json.Unmarshal(request.Options, &options); err != nil || options == nil {
			return errors.New("import options must be one JSON object")
		}
	}
	formatFound := false
	for _, format := range manifest.SourceFormats {
		if format.ID == request.SourceFormat {
			formatFound = true
			break
		}
	}
	if !formatFound {
		return fmt.Errorf("importer %q does not support source format %q", manifest.ID, request.SourceFormat)
	}
	if !containsOutput(manifest.OutputTypes, request.OutputType) {
		return fmt.Errorf("importer %q does not support output type %q", manifest.ID, request.OutputType)
	}
	return nil
}

func containsOutput(outputs []OutputType, target OutputType) bool {
	for _, output := range outputs {
		if output == target {
			return true
		}
	}
	return false
}

func cloneManifest(manifest Manifest) Manifest {
	manifest.SourceFormats = append([]SourceFormat(nil), manifest.SourceFormats...)
	for i := range manifest.SourceFormats {
		manifest.SourceFormats[i].Extensions = append([]string(nil), manifest.SourceFormats[i].Extensions...)
		manifest.SourceFormats[i].MediaTypes = append([]string(nil), manifest.SourceFormats[i].MediaTypes...)
	}
	manifest.OutputTypes = append([]OutputType(nil), manifest.OutputTypes...)
	manifest.OptionsSchema = append(json.RawMessage(nil), manifest.OptionsSchema...)
	return manifest
}

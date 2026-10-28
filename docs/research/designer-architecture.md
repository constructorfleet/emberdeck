# Browser laser designer: architecture review

**Status:** version 1 rectangle engraving and version 2 multi-path vector projects with saved per-object translation, X/Y scale, rotation, multi-selection, and group alignment are implemented; this document also retains the source review and remaining architecture for [issue #4](https://github.com/constructorfleet/emberdeck/issues/4). The full issue remains open. Issue #4 asks for creation/import, editing, operations, profile-aware preview, editable projects, generated jobs, explicit dispatch, public-seam geometry tests, browser workflow tests, and illustrated operator instructions. It specifically requires separate milestones for vector cutting, vector engraving, and raster engraving.

## Implemented rectangle milestone

The embedded editor creates/selects declared GRBL profiles, saves and reopens a
versioned rectangle project, edits numeric position/dimensions and engraving
feed/commanded power, and previews its declared millimetre envelope. Generation
creates a separate queued G-code job with immutable project/profile provenance;
the operator reviews it and uses the existing explicit readiness/start controls.
Profile revision changes require review and resave before generation.
See the [API contract](../api.md) and [illustrated guide](../operator-guide.md).

This version 1 milestone has one rectangle outline and one vector-engraving
operation. The separate version 2 milestone below adds multiple objects and a
bounded SVG importer. Text import and raster engraving remain unavailable. The
static preview is not execution monitoring for
[issue #7](https://github.com/constructorfleet/emberdeck/issues/7).

## Implemented multi-path vector milestone

Schema version 2 adds rectangle, ellipse, and line primitives stored as bounded
millimetre path points. Operators can create multiple shapes, edit their numeric
position/size, assign them to ordered vector-engrave/vector-cut operation
layers, edit/reorder/delete layers, preview operation types in the declared
profile, save/reopen the source, and generate a separate queued job. Generation
requires every path assigned; open cuts and closed zero-area cuts are rejected.
Both operation types use GRBL dynamic laser mode (`M4`) so output is disabled
when motion is stopped or held. This is a software contract, not physical
qualification. Generated jobs carry immutable schema-v2 source/profile snapshots
and use the existing explicit operator confirmation and dispatch path. The
static SVG importer stores accepted geometry in this same editable path model;
imported paths remain unassigned until an operator chooses an operation layer.
No SVG style or machine setting is inferred.

SVG `C/c` cubic and `Q/q` quadratic curves are flattened to editable
millimetre polylines with a 0.05 mm chord tolerance after element transforms and
viewport scaling, capped at 4,000 points and 16 subdivision levels. The importer
rejects nested groups, `S/s` and `T/t` smooth commands, `A/a` arcs, styles, text,
and active or external content. V2 stores per-object translation, nonuniform
scale, and rotation around the source bounds center; profile validation,
preview, and generation apply the same rounded transform. Group alignment uses
the transformed point bounds of selected objects, adding only the necessary
rounded translation to each object; normal profile validation remains in force.
The v2 model does not provide arbitrary path-node editing, text, undo/redo, kerf
compensation, tabs, passes, or raster engraving. Ellipses are polygonal and the
editor does not claim live execution progress. Version 1 projects and
`rectangle-v1` jobs remain supported with their original data and G-code.

## Pre-designer baseline

At reviewed commit `ccd366d`, the browser was an authenticated job library: it uploaded text G-code to `POST /api/v1/jobs`, displayed stored programs, and required explicit operator confirmation to start a queued job. Upload was limited to 4 MiB and stored the supplied program verbatim ([API contract at review](https://github.com/constructorfleet/emberdeck/blob/ccd366dd500d6fd65f84aacf8050e3f3371dac72/docs/api.md), [console markup at review](https://github.com/constructorfleet/emberdeck/blob/ccd366dd500d6fd65f84aacf8050e3f3371dac72/internal/server/assets/index.html)). Those upload/dispatch controls remain. The driver still validates an entire program before streaming; each executable line is limited to 79 bytes, and completion requires GRBL `Idle` ([driver contract](../driver.md)). The rectangle generator extends that baseline without adding general CAD/CAM or raster support.

## SketchForge review and reuse choice

At reviewed commit [`26ea268`](https://github.com/constructorfleet/SketchForge-3D/tree/26ea2687bc42927ccc0f6f140546ec15e37efdd3), SketchForge-3D is a browser editor for 3D design and STL import/export. Patterns worth adapting conceptually:

- **Direct manipulation with numeric properties:** the editor has selection-aware transform overlays and separate property controls ([`TransformOverlay.tsx`](https://github.com/constructorfleet/SketchForge-3D/blob/26ea2687bc42927ccc0f6f140546ec15e37efdd3/apps/web/src/components/workplane/TransformOverlay.tsx), [`ShapeInspector.tsx`](https://github.com/constructorfleet/SketchForge-3D/blob/26ea2687bc42927ccc0f6f140546ec15e37efdd3/apps/web/src/components/workplane/ShapeInspector.tsx)). For a 2D laser workspace, keep drag handles for quick placement but expose exact X/Y, width/height, and rotation fields.
- **A scene list alongside canvas selection:** `SceneInspector` supports row selection, additive selection, rename, and deletion ([source](https://github.com/constructorfleet/SketchForge-3D/blob/26ea2687bc42927ccc0f6f140546ec15e37efdd3/apps/web/src/components/workplane/SceneInspector.tsx)). Adapt the list as an object/operation layer tree with visibility, ordering, and selection.
- **Explicit project persistence and import/export paths:** the editor separates project state, project assets, and project file operations ([`projectStore.ts`](https://github.com/constructorfleet/SketchForge-3D/blob/26ea2687bc42927ccc0f6f140546ec15e37efdd3/apps/web/src/lib/engine/projectStore.ts), [`projectFiles.cjs`](https://github.com/constructorfleet/SketchForge-3D/blob/26ea2687bc42927ccc0f6f140546ec15e37efdd3/apps/desktop/projectFiles.cjs)). Preserve editable design source separately from derived machine output.
- **Undo/redo and edit history:** editor history is explicit in the main editor and its history helpers ([editor source](https://github.com/constructorfleet/SketchForge-3D/blob/26ea2687bc42927ccc0f6f140546ec15e37efdd3/apps/web/src/components/SketchForgeEditor.tsx)). Use command/snapshot history for designer edits, but do not serialize that history into generated G-code.

The repository's [`LICENSE`](https://github.com/constructorfleet/SketchForge-3D/blob/26ea2687bc42927ccc0f6f140546ec15e37efdd3/LICENSE) is GNU AGPL v3. AGPLv3 has source and notice obligations on conveying covered works, and section 13 requires a network user interacting remotely with a modified version to receive an offer to download its Corresponding Source ([license text, sections 5–6 and 13](https://github.com/constructorfleet/SketchForge-3D/blob/26ea2687bc42927ccc0f6f140546ec15e37efdd3/LICENSE)). Practical choice: implement EmberDeck's own small 2D editor using these interaction ideas; do not copy SketchForge code, assets, or bundled editor components absent an intentional AGPL-compatible distribution and network-source plan. This is a project-level engineering recommendation, not legal advice. Recheck the license and dependency licenses if selecting any component/library.

## Proposed design and generation model

Keep the UI in the existing Go binary's embedded HTML/CSS/JavaScript assets. Define a versioned, JSON **editable project** as the canonical source: document units (`mm` initially), page/work-area reference, objects with stable IDs and local geometry, transforms, style/text data, and ordered operation assignments. Store projects in a dedicated authenticated project API and persistence file, not as pseudo-jobs. A generated job is a separate immutable record containing source project ID/revision, profile revision, generator version, warnings/validation result, and exact G-code. Editing the source later never silently changes a queued/running program.

Make model geometry device-independent and in millimetres. Treat the design origin as the lower-left of the machine work area; the canvas is a view transform only (screen Y down, model Y up), and must never alter coordinates in the document. Define one profile transform from model coordinates to configured machine coordinates, including work-area width/height and any explicit machine origin offset. A profile warning is required for any transformed geometry outside the envelope; generation is blocked until corrected or explicitly resolved through a documented policy. Do not infer physical origin, homing, travel limits, or safe focus from the viewport.

Suggested starter objects are rectangle, circle/ellipse, line, polyline/path, and text. Text should be converted to vector outlines at generation and save the resolved font/outline in the project so another host does not silently substitute fonts. The implemented static SVG importer accepts line commands, cubic/quadratic Bezier paths, lines, polylines, polygons, rectangles, circles/ellipses, and element transforms. It rejects scripts, event attributes, foreign objects, external URLs/references, embedded raster images, CSS imports, animation, and unsupported filters; it imposes file-size, numeric-range, point-count, and curve-subdivision limits. Nested groups and text remain future work. Parse without network fetches and normalize accepted geometry into the same editable millimetre path model.

Represent operations explicitly and independently of SVG stroke styling: initial milestones are **vector cutting** (closed/open paths only under an explicit policy; cutting settings validated against profile material bounds) and **vector engraving** (line-following paths with separate power/speed bounds). An operation layer owns order, tool mode, speed, power, and optional pass count; define whether duplicated/overlapping paths are intentional and warn on exact duplicates. The implemented importer adaptively flattens cubic and quadratic Bezier curves to a 0.05 mm chord tolerance in millimetres, caps path points and subdivision depth, and validates emitted geometry against the work envelope. Elliptical arcs remain unsupported. Toolpath preview must distinguish powered vector motion from laser-off travel and display travel/order. Raster engraving is a later independent investigation: it needs image decoding, dithering/scan strategy, power modulation, and hardware-specific validation, and is not available in this proposal.

Initial input: native versioned EmberDeck project JSON and the bounded SVG subset. Initial output: generated plain-text G-code compatible only with the implemented GRBL 1.1 streaming contract; optionally provide a downloadable project and G-code file. Reject unsupported controller commands/features rather than claiming generic CNC or Ruida support. Generation should be a public pure seam, for example `POST /api/v1/projects/{id}/generate` with a profile ID/revision returning a validation report and immutable job ID; geometry normalization and toolpath generation should also have public Go package APIs. Dispatch remains the existing `/jobs/{id}/start` path with the existing explicit confirmation, readiness check, preflight plugins, and worker lease behavior. Do not auto-dispatch on save or generation.

## Verification and delivery evidence

Test public geometry/toolpath functions for unit conversion, origin transforms, affine SVG transforms, curve flattening tolerance, deterministic operation order, laser-off travel, power/speed bounds, duplicate paths, segment limits, and out-of-bounds rejection. Test the project and generation HTTP APIs for malformed/versioned documents, SVG security rejection, persistence/reload, immutable generated G-code, and separation of edits from already-generated jobs. Browser acceptance should cover create shapes/text, select and transform numerically, undo/redo, import valid and rejected SVG, assign cut/engrave operations, set profile dimensions, inspect bounds warnings and toolpath modes, save/reopen project, generate and inspect a job, then explicitly confirm and dispatch through the existing operator controls. The current Playwright suite already demonstrates browser-to-HTTP operator-flow coverage and is an appropriate extension point ([`e2e/console.spec.js`](https://github.com/constructorfleet/emberdeck/blob/ccd366dd500d6fd65f84aacf8050e3f3371dac72/e2e/console.spec.js)).

Implementation milestones should be evidenced independently: (1) project model, create/edit, saved-project API and screenshots; (2) SVG subset import and geometry tests; (3) vector engraving preview/generation; (4) vector cutting validation/generation and GRBL simulator dispatch; (5) illustrated operator guide screenshots captured from the implemented UI. Raster engraving remains future evaluation. Screenshot guide work belongs after UI behavior stabilizes; the repository already uses Playwright screenshot capture for operator documentation ([contribution guidance](https://github.com/constructorfleet/emberdeck/blob/ccd366dd500d6fd65f84aacf8050e3f3371dac72/CONTRIBUTING.md)).

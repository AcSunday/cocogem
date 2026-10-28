package workflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/nativecli"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// readScope freezes exactly what an attempt may retrieve. IDs and content
// digests are captured at dispatch; a later model call cannot add authority.
type readScope struct {
	messages  map[string]scopedMessage
	artifacts map[string][]scopedArtifact
	sources   map[string]scopedSource
	history   []string
}

type scopedSource struct {
	digest string
	ref    string
}

type scopedMessage struct {
	digest  string
	version int
	format  string
}

type scopedArtifact struct {
	version int
	digest  string
}

func (s *readScope) empty() bool {
	return len(s.messages) == 0 && len(s.artifacts) == 0
}

// buildReadScope records committed upstream messages/artifacts, explicit
// references and the selected historical window. In-flight siblings, other
// runs and other threads are absent by construction.
func buildReadScope(tx *store.Tx, run w.Run, step w.Step, historyIDs []string, sources []w.Source) (*readScope, error) {
	scope := &readScope{messages: map[string]scopedMessage{}, artifacts: map[string][]scopedArtifact{}, sources: map[string]scopedSource{}, history: slices.Clone(historyIDs)}
	messages, err := tx.MessagesMetadata(run.ThreadID)
	if err != nil {
		return nil, err
	}
	selectedHistory := map[string]bool{}
	for _, id := range historyIDs {
		selectedHistory[id] = true
	}
	explicitMessage := map[string]bool{}
	explicitArtifact := map[string]map[int]bool{}
	for _, ref := range step.ContextRefs {
		id, version := splitArtifactRef(ref)
		if version > 0 {
			if explicitArtifact[id] == nil {
				explicitArtifact[id] = map[int]bool{}
			}
			explicitArtifact[id][version] = true
			continue
		}
		explicitMessage[id] = true
	}
	for _, ref := range run.Input.ArtifactRefs {
		if ref.ID == "" || ref.Version <= 0 {
			continue
		}
		if explicitArtifact[ref.ID] == nil {
			explicitArtifact[ref.ID] = map[int]bool{}
		}
		explicitArtifact[ref.ID][ref.Version] = true
	}
	// The planning root's message is a directive, not evidence: selected plan
	// steps depend on it for scheduling but may never read its content.
	planningRoot := planningRootStep(run)
	dependencies := map[string]bool{}
	for _, id := range step.DependsOn {
		dependencies[id] = true
	}
	for _, message := range messages {
		if message.ThreadID != run.ThreadID || message.Status != "completed" || message.StepID == step.ID {
			continue
		}
		allowed := message.StepID != planningRoot && (dependencies[message.StepID] || explicitMessage[message.ID] || selectedHistory[message.ID])
		if !allowed {
			continue
		}
		digest, version, format, err := messageDigest(tx, message.ID)
		if err != nil {
			return nil, err
		}
		scope.messages[message.ID] = scopedMessage{digest: digest, version: version, format: format}
	}
	artifacts, err := tx.ArtifactsMetadata(run.ThreadID)
	if err != nil {
		return nil, err
	}
	for _, artifact := range artifacts {
		if artifact.ThreadID != run.ThreadID {
			continue
		}
		allowed := artifact.StepID != planningRoot && (dependencies[artifact.StepID] || explicitArtifact[artifact.ID][artifact.Version])
		if !allowed {
			continue
		}
		exact, err := tx.Artifact(artifact.ID, artifact.Version)
		if err != nil {
			return nil, err
		}
		scope.artifacts[artifact.ID] = append(scope.artifacts[artifact.ID], scopedArtifact{version: artifact.Version, digest: artifactDigest(exact)})
	}
	for id := range scope.artifacts {
		slices.SortFunc(scope.artifacts[id], func(a, b scopedArtifact) int { return a.version - b.version })
	}
	for _, source := range sources {
		digest, err := sourceDigest(tx, source)
		if err != nil {
			return nil, err
		}
		scope.sources[source.ID] = scopedSource{digest: digest, ref: sourceRef(source, digest)}
	}
	return scope, nil
}

// sourceRef binds a capture's immutable coordinates and content digest, never
// its random capture ID alone: a duplicate capture of the same bytes is not a
// new input.
func sourceRef(source w.Source, digest string) string {
	return fmt.Sprintf("source:%s:%d-%d:%s:%s:%s:%s", source.Kind, source.StartLine, source.EndLine, source.URL, source.Path, source.SHA256, digest)
}

func splitArtifactRef(ref string) (string, int) {
	id := ref
	version := 0
	if at := strings.LastIndexByte(ref, '@'); at > 0 {
		id = ref[:at]
		_, _ = fmt.Sscanf(ref[at+1:], "%d", &version)
	}
	return id, version
}

func contentDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// canonicalArtifact renders only the immutable deliverable content. IDs,
// version, revision, feedback and creation time are identity/audit fields, so
// an identical payload under a new UUID is not new progress.
func canonicalArtifact(a w.Artifact) string {
	value := struct {
		Type        string      `json:"type"`
		Title       string      `json:"title"`
		Body        string      `json:"body"`
		Findings    []w.Finding `json:"findings"`
		SourceIDs   []string    `json:"source_ids"`
		Limitations []string    `json:"limitations"`
	}{a.Type, a.Title, a.Body, a.Findings, a.SourceIDs, a.Limitations}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func artifactDigest(a w.Artifact) string { return contentDigest(canonicalArtifact(a)) }

// checkpointArtifactDigest is the progress digest: it hashes deliverable
// content, but rewrites every source UUID to its canonical capture reference
// (coordinates plus content hash). A same-content capture under a new UUID is
// therefore not new progress, while any real finding or limitation change is.
func checkpointArtifactDigest(tx *store.Tx, a w.Artifact) (string, error) {
	type finding struct {
		Title     string   `json:"title"`
		Detail    string   `json:"detail"`
		Status    string   `json:"status"`
		Severity  string   `json:"severity,omitempty"`
		SourceIDs []string `json:"source_ids"`
	}
	value := struct {
		Type        string    `json:"type"`
		Title       string    `json:"title"`
		Body        string    `json:"body"`
		Findings    []finding `json:"findings"`
		SourceIDs   []string  `json:"source_ids"`
		Limitations []string  `json:"limitations"`
	}{Type: a.Type, Title: a.Title, Body: a.Body, Limitations: a.Limitations}
	refs, err := canonicalSourceRefs(tx, a.SourceIDs)
	if err != nil {
		return "", err
	}
	value.SourceIDs = refs
	for _, f := range a.Findings {
		refs, err := canonicalSourceRefs(tx, f.SourceIDs)
		if err != nil {
			return "", err
		}
		value.Findings = append(value.Findings, finding{Title: f.Title, Detail: f.Detail, Status: f.Status, Severity: f.Severity, SourceIDs: refs})
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return contentDigest(string(encoded)), nil
}

func canonicalSourceRefs(tx *store.Tx, ids []string) ([]string, error) {
	refs := make([]string, 0, len(ids))
	for _, id := range ids {
		source, err := tx.Source(id)
		if err != nil {
			return nil, err
		}
		digest := source.ContentDigest
		if digest == "" {
			digest = contentDigest(source.Content)
		}
		refs = append(refs, sourceRef(source, digest))
	}
	sort.Strings(refs)
	return slices.Compact(refs), nil
}

// sourceDigest uses the capture-time digest when the metadata projection kept
// it, otherwise streams one exact capture from the store.
func sourceDigest(tx *store.Tx, source w.Source) (string, error) {
	if source.ContentDigest != "" {
		return source.ContentDigest, nil
	}
	exact, err := tx.Source(source.ID)
	if err != nil {
		return "", err
	}
	return contentDigest(exact.Content), nil
}

// messageDigest hashes one complete body without retaining it in memory.
func messageDigest(tx *store.Tx, id string) (string, int, string, error) {
	return tx.MessageDigest(id)
}

// readContext implements the workflow-owned bounded retrieval tool. Every
// call revalidates membership and the workspace root before reading.
func (e *Engine) readContext(ctx context.Context, run w.Run, step w.Step, scope *readScope, raw json.RawMessage) (string, error) {
	var in struct {
		Kind     string `json:"kind"`
		ID       string `json:"id"`
		Version  int    `json:"version"`
		Field    string `json:"field"`
		Cursor   string `json:"cursor"`
		MaxBytes int    `json:"max_bytes"`
	}
	if err := tools.Decode(raw, &in); err != nil {
		return "", err
	}
	if in.Kind != "message" && in.Kind != "artifact" {
		return "", errors.New("read_context kind must be message or artifact")
	}
	field := in.Field
	if field == "" {
		field = "body"
	}
	if field != "body" && field != "artifact_json" {
		return "", errors.New("read_context field must be body or artifact_json")
	}
	if in.Kind == "message" && field != "body" {
		return "", errors.New("read_context field=artifact_json applies only to artifacts")
	}
	if in.Version < 0 {
		return "", errors.New("read_context version must be a positive integer when provided")
	}
	maxBytes := in.MaxBytes
	if maxBytes == 0 {
		maxBytes = w.MaxTextPageBytes
	}
	if maxBytes < 1 || maxBytes > w.MaxTextPageBytes {
		return "", fmt.Errorf("read_context max_bytes must be between 1 and %d", w.MaxTextPageBytes)
	}
	if scope == nil {
		return "", errors.New("read_context has no frozen input scope")
	}
	var requestErr error
	if err := e.Store.Read(ctx, func(tx *store.Tx) error {
		if err := e.validateMember(tx, run, step.AgentID); err != nil {
			requestErr = err
		}
		return nil
	}); err != nil {
		return "", err
	}
	if requestErr != nil {
		return "", requestErr
	}
	switch in.Kind {
	case "message":
		entry, ok := scope.messages[in.ID]
		if !ok {
			return "", errors.New("message is outside this attempt's committed context")
		}
		var message w.Message
		var content string
		var absolute, total int
		var hasMore bool
		var nextCursor string
		if err := e.Store.Read(ctx, func(tx *store.Tx) error {
			var err error
			message, err = tx.MessageMeta(in.ID)
			if err != nil {
				return err
			}
			format := message.Format()
			total = message.ContentBytes
			if total != entry.version || format != entry.format {
				return errors.New("message content changed after this attempt was dispatched")
			}
			seq, offset := 1, 0
			if in.Cursor != "" {
				value, decodeErr := decodeMessageCursor(in.Cursor)
				if decodeErr != nil {
					return decodeErr
				}
				// The cursor carries the digest computed for this immutable
				// body, so later pages never re-hash the whole message.
				if value.ID != in.ID || value.Format != format || value.Version != total || value.Digest != entry.digest {
					return errors.New("cursor belongs to another message version")
				}
				seq, offset = value.Seq, value.Offset
			} else {
				digest, _, _, digestErr := messageDigest(tx, in.ID)
				if digestErr != nil {
					return digestErr
				}
				if digest != entry.digest {
					return errors.New("message content changed after this attempt was dispatched")
				}
			}
			var err2 error
			content, seq, offset, absolute, hasMore, err2 = tx.MessageWindow(in.ID, seq, offset, maxBytes)
			if err2 != nil {
				return err2
			}
			if hasMore {
				raw, _ := json.Marshal(messageCursor{ID: in.ID, Format: format, Version: total, Digest: entry.digest, Seq: seq, Offset: offset})
				nextCursor = base64.RawURLEncoding.EncodeToString(raw)
			}
			return nil
		}); err != nil {
			return "", err
		}
		if message.ThreadID != run.ThreadID || message.Status != "completed" {
			return "", errors.New("message is no longer committed in this conversation")
		}
		result := map[string]any{"kind": "message", "id": message.ID, "content": content, "offset": absolute, "total_bytes": total, "has_more": hasMore, "next_cursor": nextCursor, "committed": true, "content_format": message.Format(), "agent_id": message.AgentID, "step_id": message.StepID}
		return encodeWire(result)
	case "artifact":
		versions := scope.artifacts[in.ID]
		if len(versions) == 0 {
			return "", errors.New("artifact is outside this attempt's committed context")
		}
		version := in.Version
		if version <= 0 {
			if len(versions) != 1 {
				return "", errors.New("artifact version is required: multiple selected versions exist for this ID")
			}
			version = versions[0].version
		}
		var digest string
		for _, candidate := range versions {
			if candidate.version == version {
				digest = candidate.digest
			}
		}
		if digest == "" {
			return "", errors.New("artifact version is outside this attempt's committed context")
		}
		var artifact w.Artifact
		if err := e.Store.Read(ctx, func(tx *store.Tx) error {
			var err error
			artifact, err = tx.Artifact(in.ID, version)
			return err
		}); err != nil {
			return "", err
		}
		if artifact.ThreadID != run.ThreadID {
			return "", errors.New("artifact belongs to another conversation")
		}
		if artifactDigest(artifact) != digest {
			return "", errors.New("artifact content changed after this attempt was dispatched")
		}
		if field == "artifact_json" {
			// The whole deliverable (body, findings, source IDs, limitations)
			// canonicalized as one paginated JSON value. The cursor binds the
			// exact version digest, so every page belongs to this version.
			canonical := canonicalArtifact(artifact)
			page, err := w.PageText(w.TextRef{Kind: "artifact", ID: artifact.ID, Version: artifact.Version}, canonical, in.Cursor, maxBytes)
			if err != nil {
				return "", err
			}
			return encodeWire(map[string]any{"kind": "artifact", "id": artifact.ID, "version": artifact.Version, "field": "artifact_json", "content_format": "json", "content": page.Content, "offset": page.Offset, "total_bytes": page.TotalBytes, "has_more": page.HasMore, "next_cursor": page.NextCursor, "committed": true, "content_sha256": digest})
		}
		page, err := w.PageText(w.TextRef{Kind: "artifact", ID: artifact.ID, Version: artifact.Version}, artifact.Body, in.Cursor, maxBytes)
		if err != nil {
			return "", err
		}
		result := map[string]any{"kind": "artifact", "id": artifact.ID, "version": artifact.Version, "field": "body", "type": artifact.Type, "title": artifact.Title, "content": page.Content, "offset": page.Offset, "total_bytes": page.TotalBytes, "has_more": page.HasMore, "next_cursor": page.NextCursor, "committed": true, "content_sha256": digest, "source_ids": artifact.SourceIDs, "limitations": artifact.Limitations, "findings": artifact.Findings}
		encoded, err := encodeWire(result)
		if err != nil {
			// The body page stays retrievable; the full structured deliverable
			// is still available field=artifact_json rather than silently lost.
			delete(result, "findings")
			delete(result, "source_ids")
			delete(result, "limitations")
			result["structured_omitted"] = true
			result["structured_field"] = "artifact_json"
			return encodeWire(result)
		}
		return encoded, nil
	}
	return "", errors.New("unsupported read_context kind")
}

func encodeWire(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	if len(encoded) > w.MaxTextPageWireBytes {
		return "", errors.New("read result exceeds the 64 KiB wire budget; request a smaller page")
	}
	return string(encoded), nil
}

type messageCursor struct {
	ID      string `json:"id"`
	Format  string `json:"format"`
	Version int    `json:"version"`
	Digest  string `json:"digest"`
	Seq     int    `json:"seq"`
	Offset  int    `json:"offset"`
}

// decodeMessageCursor accepts only one complete, bounded cursor object.
func decodeMessageCursor(token string) (messageCursor, error) {
	var value messageCursor
	if token == "" || len(token) > 512 {
		return value, errors.New("cursor belongs to another message version")
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return value, errors.New("cursor belongs to another message version")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, errors.New("cursor belongs to another message version")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return value, errors.New("cursor belongs to another message version")
	}
	if value.Seq < 1 || value.Offset < 0 || value.ID == "" || value.Digest == "" {
		return value, errors.New("cursor belongs to another message version")
	}
	return value, nil
}

// sourceListPage caps one list_sources page by item count; the encoded JSON
// size is checked separately so bounded fields can still exceed the wire limit.
const sourceListPage = 20

type sourceListCursor struct {
	Offset int    `json:"offset"`
	Scope  string `json:"scope"`
}

// evidenceStrength classifies a capture without reading it. Native web is a
// CLI-processed observation, not raw page text; native search is only a lead.
func evidenceStrength(kind string) string {
	switch kind {
	case "file", "web", "native_file", "diff", "test":
		return "original"
	case "native_web":
		return "observation"
	default:
		// Search leads and unknown future kinds never support a claim.
		return "lead"
	}
}

func sourceListScope(sources []w.Source) string {
	ids := make([]string, 0, len(sources))
	for _, source := range sources {
		ids = append(ids, source.ID)
	}
	return contentDigest(strings.Join(ids, "\n"))
}

func boundedField(value string, limit int) string {
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return value
}

func (e *Engine) listSources(ctx context.Context, run w.Run, sources []w.Source, raw json.RawMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var in struct {
		Cursor string `json:"cursor"`
	}
	if err := tools.Decode(raw, &in); err != nil {
		return "", err
	}
	visible := make([]w.Source, 0, len(sources))
	for _, source := range sources {
		if source.ThreadID != "" && source.ThreadID != run.ThreadID {
			continue
		}
		visible = append(visible, source)
	}
	offset := 0
	scope := sourceListScope(visible)
	if in.Cursor != "" {
		if len(in.Cursor) > 512 {
			return "", errors.New("source cursor is invalid")
		}
		decoded, err := base64.RawURLEncoding.DecodeString(in.Cursor)
		if err != nil {
			return "", errors.New("source cursor is invalid")
		}
		var cursor sourceListCursor
		decoder := json.NewDecoder(bytes.NewReader(decoded))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&cursor) != nil || decoder.Decode(new(any)) != io.EOF {
			return "", errors.New("source cursor is invalid")
		}
		// The cursor carries the exact next offset chosen for the actual
		// encoded byte size, so it is not required to be a multiple of a
		// fixed item count.
		if cursor.Offset < 0 || cursor.Offset > len(visible) || cursor.Scope != scope {
			return "", errors.New("source cursor belongs to a different source list")
		}
		offset = cursor.Offset
	}
	// Only the next page's candidates need projection; the remaining sources
	// stay metadata-only until their own cursor request.
	capacity := min(sourceListPage, len(visible)-offset)
	items := make([]map[string]any, 0, capacity)
	for _, source := range visible[offset : offset+capacity] {
		strength := evidenceStrength(source.Kind)
		if strength == "original" && tools.IsSearchResultURL(source.URL) {
			strength = "lead"
		}
		item := map[string]any{
			"source_id":         source.ID,
			"kind":              source.Kind,
			"title":             boundedField(source.Title, 300),
			"url":               boundedField(source.URL, 500),
			"path":              boundedField(source.Path, 300),
			"captured_at":       source.CapturedAt,
			"truncated":         source.Truncated,
			"evidence_strength": strength,
		}
		if source.Kind == "test" {
			item["exit_code"] = source.ExitCode
			item["timed_out"] = source.TimedOut
		}
		items = append(items, item)
	}
	page := func(count int) (string, error) {
		end := offset + count
		result := map[string]any{"sources": items[:count], "has_more": end < len(visible), "next_cursor": ""}
		if end < len(visible) {
			cursor, err := json.Marshal(sourceListCursor{Offset: end, Scope: scope})
			if err != nil {
				return "", err
			}
			result["next_cursor"] = base64.RawURLEncoding.EncodeToString(cursor)
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return "", err
		}
		if len(encoded) > w.MaxTextPageWireBytes {
			return "", errors.New("read result exceeds the 64 KiB wire budget; request a smaller page")
		}
		return string(encoded), nil
	}
	// Find the largest page (up to the item cap) whose exact JSON bytes fit.
	// A page that does not fit never drops the remaining entries: the cursor
	// resumes from the last included source.
	low, high, best := 1, min(sourceListPage, len(items)), ""
	if high == 0 {
		return page(0)
	}
	for low <= high {
		middle := (low + high) / 2
		encoded, err := page(middle)
		if err != nil {
			high = middle - 1
			continue
		}
		best = encoded
		low = middle + 1
	}
	if best == "" {
		return "", errors.New("single source metadata exceeds the 64 KiB wire budget")
	}
	return best, nil
}

// readSource pages the exact captured version, never the current file or web
// page, and keeps capture metadata next to a legal JSON payload.
func (e *Engine) readSource(ctx context.Context, run w.Run, sources []w.Source, raw json.RawMessage) (string, error) {
	var in struct {
		SourceID string `json:"source_id"`
		Cursor   string `json:"cursor"`
		MaxBytes int    `json:"max_bytes"`
	}
	if err := tools.Decode(raw, &in); err != nil {
		return "", err
	}
	allowed := false
	for _, source := range sources {
		if source.ID == in.SourceID {
			allowed = true
		}
	}
	if !allowed {
		return "", errors.New("captured source is outside this step's input scope")
	}
	maxBytes := in.MaxBytes
	if maxBytes == 0 {
		maxBytes = w.MaxTextPageBytes
	}
	if maxBytes < 1 || maxBytes > w.MaxTextPageBytes {
		return "", fmt.Errorf("read_source max_bytes must be between 1 and %d", w.MaxTextPageBytes)
	}
	var source w.Source
	if err := e.Store.Read(ctx, func(tx *store.Tx) error {
		var err error
		source, err = tx.Source(in.SourceID)
		return err
	}); err != nil {
		return "", err
	}
	if source.ThreadID != run.ThreadID {
		return "", errors.New("captured source belongs to another conversation")
	}
	page, err := w.PageText(w.TextRef{Kind: "source", ID: source.ID}, source.Content, in.Cursor, maxBytes)
	if err != nil {
		return "", err
	}
	result := map[string]any{"id": source.ID, "source_id": source.ID, "kind": source.Kind, "title": source.Title, "url": source.URL, "path": source.Path, "start_line": source.StartLine, "end_line": source.EndLine, "sha256": source.SHA256, "captured_at": source.CapturedAt, "source_truncated": source.Truncated, "content": page.Content, "offset": page.Offset, "total_bytes": page.TotalBytes, "preview_truncated": page.HasMore, "has_more": page.HasMore, "next_cursor": page.NextCursor}
	return encodeWire(result)
}

// InputPlanDigest covers canonical inputs only: role/instruction/task,
// exact references with content digests, and the actual model/connection/CLI.
// Attempt/session/time/cursor/timeout policy never enter it.
func inputPlanDigest(tx *store.Tx, step w.Step, run w.Run, modelName string, connection agent.ConnectionInfo, adapter agent.AdapterInfo, scope *readScope, toolNames []string, sources []w.Source) (string, error) {
	type plan struct {
		AgentID           string   `json:"agent_id"`
		AgentName         string   `json:"agent_name"`
		Handle            string   `json:"handle"`
		Role              string   `json:"role"`
		RoleProfile       string   `json:"role_profile"`
		Capabilities      []string `json:"capabilities"`
		Instruction       string   `json:"instruction"`
		OriginalTask      string   `json:"original_task"`
		Workflow          string   `json:"workflow"`
		EngineeringAction string   `json:"engineering_action,omitempty"`
		ResearchScope     string   `json:"research_scope"`
		SourceURLs        []string `json:"source_urls"`
		Executor          string   `json:"executor"`
		Model             string   `json:"model"`
		ResolvedModel     string   `json:"resolved_model"`
		Connection        string   `json:"connection"`
		CLIIdentity       string   `json:"cli_identity"`
		CLIProgram        string   `json:"cli_program"`
		Adapter           string   `json:"adapter"`
		Tools             []string `json:"tools"`
		NativeTools       []string `json:"native_tools"`
		Skills            []string `json:"skills"`
		Roster            []string `json:"roster"`
		System            string   `json:"system_identity"`
		Refs              []string `json:"refs"`
	}
	member := w.Agent{ID: step.AgentID}
	if index := slices.IndexFunc(run.Agents, func(a w.Agent) bool { return a.ID == step.AgentID }); index >= 0 {
		member = run.Agents[index]
	}
	in := effectiveInput(run)
	value := plan{AgentID: member.ID, AgentName: member.DisplayName, Handle: member.Handle, Role: step.Role, RoleProfile: member.RoleDescription, Capabilities: slices.Clone(member.Capabilities), Instruction: step.Instruction, OriginalTask: run.Input.Message, Workflow: in.Workflow, EngineeringAction: in.EngineeringAction, ResearchScope: in.ResearchScope, SourceURLs: slices.Clone(run.Input.SourceURLs), Executor: adapter.ID, Model: modelName, ResolvedModel: connection.ResolvedModel, Connection: connection.Identity, CLIIdentity: adapter.Identity, CLIProgram: adapter.Program, Adapter: adapter.Adapter, Tools: slices.Clone(toolNames), NativeTools: nativecli.NativeTools(adapter.ID, toolNames, in.ResearchScope), System: contentDigest(systemIdentity(run, step, member))}
	for _, id := range enabledSkills(run) {
		value.Skills = append(value.Skills, id+"@"+run.SkillVersions[id])
	}
	roster := slices.Clone(run.Agents)
	slices.SortFunc(roster, func(a, b w.Agent) int { return strings.Compare(a.ID, b.ID) })
	for _, a := range roster {
		current, err := tx.WorkspaceAgent(run.WorkspaceID, a.ID)
		enabled := err == nil && !current.Disabled
		effective := []string{}
		if enabled {
			for _, name := range a.Tools {
				if slices.Contains(current.Tools, name) {
					effective = append(effective, name)
				}
			}
		}
		slices.Sort(effective)
		value.Roster = append(value.Roster, fmt.Sprintf("%s:%s:%s:%t:%s", a.ID, a.Executor, a.Model, enabled, strings.Join(effective, ",")))
	}
	for id, entry := range scope.messages {
		value.Refs = append(value.Refs, "message:"+id+":"+entry.digest)
	}
	for id, versions := range scope.artifacts {
		for _, version := range versions {
			value.Refs = append(value.Refs, fmt.Sprintf("artifact:%s@%d:%s", id, version.version, version.digest))
		}
	}
	for _, source := range sources {
		digest, err := sourceDigest(tx, source)
		if err != nil {
			return "", err
		}
		value.Refs = append(value.Refs, sourceRef(source, digest))
	}
	value.Refs = append(value.Refs, step.ContextRefs...)
	slices.Sort(value.Tools)
	slices.Sort(value.SourceURLs)
	slices.Sort(value.Skills)
	sort.Strings(value.Refs)
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return contentDigest(string(encoded)), nil
}

// planDigestFor re-derives the canonical input digest from the current
// persisted inputs and verified runtime identity. Timeouts, attempts, session
// IDs and clocks are deliberately absent.
func (e *Engine) planDigestFor(tx *store.Tx, run w.Run, step w.Step, adapter agent.AdapterInfo, connection agent.ConnectionInfo, sourceIDs []string) (string, error) {
	previews, err := tx.MessagePreviews(run.ThreadID, 6000)
	if err != nil {
		return "", err
	}
	selected := recentHistory(previews, run.ID, 12, 16000)
	historyIDs := make([]string, 0, len(selected))
	for _, message := range selected {
		historyIDs = append(historyIDs, message.ID)
	}
	sources, err := sourcesByIDs(tx, run.ThreadID, sourceIDs)
	if err != nil {
		return "", err
	}
	scope, err := buildReadScope(tx, run, step, historyIDs, sources)
	if err != nil {
		return "", err
	}
	member := w.Agent{ID: step.AgentID}
	if index := slices.IndexFunc(run.Agents, func(a w.Agent) bool { return a.ID == step.AgentID }); index >= 0 {
		member = run.Agents[index]
	}
	return inputPlanDigest(tx, step, run, member.Model, connection, adapter, scope, e.allowedTools(run, member, step), sources)
}

// sourcesByIDs reconstructs exactly the capture metadata an attempt was
// dispatched with. Missing IDs fail closed rather than shrinking the plan.
func sourcesByIDs(tx *store.Tx, threadID string, ids []string) ([]w.Source, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	available, err := tx.SourceMetadata(threadID)
	if err != nil {
		return nil, err
	}
	byID := map[string]w.Source{}
	for _, source := range available {
		byID[source.ID] = source
	}
	out := make([]w.Source, 0, len(ids))
	for _, id := range ids {
		source, ok := byID[id]
		if !ok {
			return nil, errors.New("a source captured for this attempt is no longer available")
		}
		out = append(out, source)
	}
	return out, nil
}

// checkpointDigest hashes only persisted content: message bodies, exact
// source captures and committed or draft deliverables. It deduplicates the
// content set so a repeated capture under a new UUID is not new progress, and
// attributes captures to this logical step instead of every sibling step.
func checkpointDigest(tx *store.Tx, runID, threadID, stepID string, sourceIDs []string, draft *w.Artifact) (string, error) {
	messages, err := tx.MessagesMetadata(threadID)
	if err != nil {
		return "", err
	}
	lines := map[string]bool{}
	for _, message := range messages {
		if message.StepID != stepID {
			continue
		}
		digest, version, _, err := messageDigest(tx, message.ID)
		if err != nil {
			return "", err
		}
		if version == 0 {
			continue
		}
		lines["message:"+digest] = true
	}
	sources, err := tx.SourceMetadata(threadID)
	if err != nil {
		return "", err
	}
	for _, source := range sources {
		if source.RunID != runID {
			continue
		}
		if source.CaptureStepID != "" {
			if source.CaptureStepID != stepID {
				continue
			}
		} else if !slices.Contains(sourceIDs, source.ID) {
			// Legacy captures have no attribution; only those visible in this
			// attempt's frozen snapshot contribute.
			continue
		}
		digest, err := sourceDigest(tx, source)
		if err != nil {
			return "", err
		}
		lines[sourceRef(source, digest)] = true
	}
	artifacts, err := tx.ArtifactsMetadata(threadID)
	if err != nil {
		return "", err
	}
	for _, artifact := range artifacts {
		if artifact.StepID != stepID {
			continue
		}
		exact, err := tx.Artifact(artifact.ID, artifact.Version)
		if err != nil {
			return "", err
		}
		digest, err := checkpointArtifactDigest(tx, exact)
		if err != nil {
			return "", err
		}
		lines["artifact:"+digest] = true
	}
	// Drafts stay on their original attempts; omitting one from a resumed
	// attempt must not masquerade as new checkpoint content.
	attempts, err := tx.StepAttempts(stepID)
	if err != nil {
		return "", err
	}
	for _, attempt := range attempts {
		if attempt.DraftArtifact == nil {
			continue
		}
		digest, err := checkpointArtifactDigest(tx, *attempt.DraftArtifact)
		if err != nil {
			return "", err
		}
		lines["draft:"+digest] = true
	}
	if draft != nil {
		digest, err := checkpointArtifactDigest(tx, *draft)
		if err != nil {
			return "", err
		}
		lines["draft:"+digest] = true
	}
	sorted := make([]string, 0, len(lines))
	for line := range lines {
		sorted = append(sorted, line)
	}
	sort.Strings(sorted)
	return contentDigest(strings.Join(sorted, "\n")), nil
}

// sealHandoff adds exact refs and content digests without adding full bodies.
func sealHandoff(tx *store.Tx, handoff *Handoff) error {
	handoff.MessageRefs = []string{}
	handoff.ArtifactRefs = []w.ArtifactRef{}
	handoff.Digests = map[string]string{}
	for _, message := range handoff.Messages {
		digest, _, _, err := messageDigest(tx, message.ID)
		if err != nil {
			return err
		}
		handoff.MessageRefs = append(handoff.MessageRefs, message.ID)
		handoff.Digests["message:"+message.ID] = digest
	}
	for _, artifact := range handoff.Artifacts {
		exact, err := tx.Artifact(artifact.ID, artifact.Version)
		if err != nil {
			return err
		}
		handoff.ArtifactRefs = append(handoff.ArtifactRefs, w.ArtifactRef{ID: artifact.ID, Version: artifact.Version})
		handoff.Digests[fmt.Sprintf("artifact:%s@%d", artifact.ID, artifact.Version)] = artifactDigest(exact)
	}
	for _, id := range handoff.SourceIDs {
		source, err := tx.Source(id)
		if err != nil {
			return err
		}
		handoff.Digests["source:"+id] = contentDigest(source.Content)
	}
	return nil
}

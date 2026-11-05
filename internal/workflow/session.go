package workflow

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

type sessionInput struct {
	run        w.Run
	step       w.Step
	member     w.Agent
	adapter    agent.AdapterInfo
	connection agent.ConnectionInfo
	attempt    w.Attempt
	sources    []w.Source
}

func prepareExecutionSession(tx *store.Tx, in sessionInput) (w.ExecutionSession, error) {
	if in.run.TaskID == "" {
		return w.ExecutionSession{}, nil
	}
	visibilityEpoch := 0
	grants, err := tx.Grants(in.run.WorkspaceID)
	if err != nil {
		return w.ExecutionSession{}, err
	}
	for _, grant := range grants {
		if grant.SubjectMemberID == in.member.ID && grant.Status == "active" {
			visibilityEpoch = max(visibilityEpoch, grant.Epoch)
		}
	}
	exposed := make([]string, 0, len(in.sources))
	for _, source := range in.sources {
		digest, err := sourceDigest(tx, source)
		if err != nil {
			return w.ExecutionSession{}, err
		}
		exposed = append(exposed, "source_id:"+source.ID+":"+digest)
	}
	contextProtocol := "legacy-context/v1"
	if in.run.ContextRef != "" {
		manifest, err := tx.ContextManifest(in.run.ContextRef)
		if err != nil {
			return w.ExecutionSession{}, err
		}
		contextProtocol = "contextplan/v1;fresh-full-input"
		exposed = append(exposed, "context:"+manifest.ID+":"+manifest.ScopeDigest)
		for _, item := range manifest.Items {
			exposed = append(exposed, item.Ref+":"+item.Digest)
		}
		appended, err := tx.ContextScopeEntries(manifest.ID)
		if err != nil {
			return w.ExecutionSession{}, err
		}
		for _, item := range appended {
			exposed = append(exposed, item.Ref+":"+item.Digest)
		}
	}
	slices.Sort(exposed)
	exposed = slices.Compact(exposed)
	skills := []string{}
	for _, id := range enabledSkills(in.run) {
		skills = append(skills, id+"@"+in.run.SkillVersions[id])
	}
	slices.Sort(skills)
	grantActions := slices.Clone(in.run.GrantActions)
	slices.Sort(grantActions)
	adapterCapabilities := slices.Clone(in.adapter.Capabilities)
	slices.Sort(adapterCapabilities)
	managed := in.run.EffectiveExecutionMode != "" && in.run.EffectiveExecutionMode != w.ExecutionControlled &&
		slices.Contains(in.adapter.Capabilities, "session_resume_ack=supported")
	if managed {
		contextProtocol = "contextplan/v1;delta-v1"
	}
	fingerprintInput := struct {
		CLI                   string   `json:"cli"`
		Adapter               string   `json:"adapter"`
		CLIProgram            string   `json:"cli_program"`
		CLIIdentity           string   `json:"cli_identity"`
		CLIVersion            string   `json:"cli_version"`
		AdapterCapabilities   []string `json:"adapter_capabilities"`
		ResolvedModel         string   `json:"resolved_model"`
		ConnectionIdentity    string   `json:"connection_identity"`
		MemberID              string   `json:"member_id"`
		Role                  string   `json:"role"`
		Skills                []string `json:"skills"`
		TaskID                string   `json:"task_id"`
		RequestedMode         string   `json:"requested_execution_mode"`
		EffectiveMode         string   `json:"effective_execution_mode"`
		UnenforcedConstraints []string `json:"unenforced_constraints"`
		ExecutionRef          string   `json:"execution_ref"`
		ExecutionRoot         string   `json:"execution_root"`
		GrantSnapshot         string   `json:"grant_snapshot"`
		GrantActions          []string `json:"grant_actions"`
		ContextProtocol       string   `json:"context_protocol"`
	}{
		CLI: in.member.Executor, Adapter: in.adapter.Adapter, CLIProgram: in.adapter.Program,
		CLIIdentity: in.adapter.Identity, CLIVersion: in.adapter.Version, AdapterCapabilities: adapterCapabilities,
		ResolvedModel: in.connection.ResolvedModel, ConnectionIdentity: in.connection.Identity,
		MemberID: in.member.ID, Role: in.step.Role, Skills: skills, TaskID: in.run.TaskID,
		RequestedMode: in.run.RequestedExecutionMode, EffectiveMode: in.run.EffectiveExecutionMode,
		UnenforcedConstraints: in.run.UnenforcedConstraints,
		ExecutionRef:          in.run.ExecutionRef, ExecutionRoot: in.run.ProjectRoot(),
		GrantSnapshot: in.run.GrantSnapshot, GrantActions: grantActions, ContextProtocol: contextProtocol,
	}
	encoded, err := json.Marshal(fingerprintInput)
	if err != nil {
		return w.ExecutionSession{}, err
	}
	fingerprint := contentDigest(string(encoded))
	contextEpoch := 1
	existing, err := tx.Sessions(in.run.TaskID)
	if err != nil {
		return w.ExecutionSession{}, err
	}
	for _, session := range existing {
		if session.MemberID == in.member.ID {
			contextEpoch = max(contextEpoch, session.ContextEpoch+1)
		}
	}
	now := w.Now()
	prepared := w.ExecutionSession{
		ID: w.NewID(), TaskID: in.run.TaskID, MemberID: in.member.ID, Role: in.step.Role,
		ExecutionBranch: in.run.ExecutionRef, Fingerprint: fingerprint,
		ContextEpoch: contextEpoch, VisibilityEpoch: visibilityEpoch,
		ExposedRefs: exposed, ExposureDigest: contentDigest(strings.Join(exposed, "\n")),
		Adapter: in.adapter.Adapter, Acknowledgement: "resume_unsupported", Status: "fresh_only",
		CreatedAt: now, UpdatedAt: now,
	}
	if managed {
		prepared.Status, prepared.Acknowledgement = "active", "awaiting_runner_ack"
	}
	for _, session := range existing {
		if !managed || !sessionReuseEligible(session, fingerprint, visibilityEpoch) ||
			session.MemberID != in.member.ID || session.Role != in.step.Role || session.ExecutionBranch != in.run.ExecutionRef || session.NativeSessionID == "" {
			continue
		}
		prepared.ID, prepared.NativeSessionID = session.ID, session.NativeSessionID
		prepared.CreatedAt = session.CreatedAt
		prepared.AcknowledgedExposedRefs = slices.Clone(session.AcknowledgedExposedRefs)
		if prepared.AcknowledgedExposedRefs == nil {
			// Sessions written before the separate receipt ledger already proved
			// their initial input. Keep that compatibility without losing new deltas.
			prepared.AcknowledgedExposedRefs = slices.Clone(session.ExposedRefs)
		}
		prepared.ExposedRefs = append(prepared.ExposedRefs, session.ExposedRefs...)
		slices.Sort(prepared.ExposedRefs)
		prepared.ExposedRefs = slices.Compact(prepared.ExposedRefs)
		prepared.ExposureDigest = contentDigest(strings.Join(prepared.ExposedRefs, "\n"))
		break
	}
	return prepared, nil
}

func sessionReuseEligible(session w.ExecutionSession, fingerprint string, visibilityEpoch int) bool {
	acknowledged := session.Acknowledgement == "fresh_acknowledged" || session.Acknowledgement == "resume_acknowledged"
	return session.Status == "active" && acknowledged && session.Fingerprint == fingerprint && session.VisibilityEpoch == visibilityEpoch
}

func updateNativeSession(tx *store.Tx, attempt w.Attempt, nativeSessionID string) error {
	if attempt.ExecutionSessionRef == "" {
		return nil
	}
	session, err := tx.Session(attempt.ExecutionSessionRef)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if attempt.SessionMode == "resume" {
		if session.NativeSessionID == "" || nativeSessionID != session.NativeSessionID {
			return errors.New("native CLI resumed a different session identity")
		}
		session.Acknowledgement = "resume_acknowledged"
	} else if session.Status == "active" {
		session.Acknowledgement = "fresh_acknowledged"
	} else {
		session.Acknowledgement = "fresh_started_resume_unsupported"
	}
	session.NativeSessionID = nativeSessionID
	if session.Status == "active" {
		session.AcknowledgedExposedRefs = slices.Clone(session.ExposedRefs)
	}
	session.UpdatedAt = w.Now()
	return tx.SaveSession(session)
}

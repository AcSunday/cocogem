package store

import (
	"database/sql"
	"errors"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

// InsertTask creates the durable identity; revisions and budget are committed by
// the caller in the same transaction.
func (t *Tx) InsertTask(v w.Task) error {
	return t.exec("INSERT INTO tasks(id,workspace_id,parent_id,thread_id,current_revision,root_task_id,budget_account_id,status,updated_at,body) VALUES(?,?,?,?,?,?,?,?,?,?)", v.ID, v.WorkspaceID, nullable(v.ParentID), nullable(v.ThreadID), v.CurrentRevision, v.RootTaskID, v.BudgetAccountID, v.Status, v.UpdatedAt, body(v))
}

// SaveTask advances mutable projections with CAS on the task revision.
func (t *Tx) SaveTask(v w.Task, expectedRevision int) error {
	result, err := t.tx.ExecContext(t.ctx, "UPDATE tasks SET parent_id=?,thread_id=?,current_revision=?,status=?,updated_at=?,body=? WHERE id=? AND current_revision=?", nullable(v.ParentID), nullable(v.ThreadID), v.CurrentRevision, v.Status, v.UpdatedAt, body(v), v.ID, expectedRevision)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return w.ErrConflict
	}
	return nil
}

// Task reads the current task projection.
func (t *Tx) Task(id string) (w.Task, error) {
	return get[w.Task](t, "SELECT body FROM tasks WHERE id=?", id)
}

// Tasks lists root and child tasks for project navigation.
func (t *Tx) Tasks(workspaceID string) ([]w.Task, error) {
	return list[w.Task](t, "SELECT body FROM tasks WHERE workspace_id=? ORDER BY updated_at DESC,id", workspaceID)
}

// AllTasks returns the global task board across workspaces.
func (t *Tx) AllTasks() ([]w.Task, error) {
	return list[w.Task](t, "SELECT body FROM tasks ORDER BY updated_at DESC,id")
}

// LatestTaskRun exposes the most recent execution status for task-board filters.
func (t *Tx) LatestTaskRun(taskID string) (w.Run, error) {
	return get[w.Run](t, "SELECT body FROM runs WHERE task_id=? ORDER BY created_at DESC,id DESC LIMIT 1", taskID)
}

// InsertTaskRevision persists an immutable accepted goal version.
func (t *Tx) InsertTaskRevision(v w.TaskRevision) error {
	return t.exec("INSERT INTO task_revisions(task_id,revision,body) VALUES(?,?,?)", v.TaskID, v.Revision, body(v))
}

// TaskRevision reads an exact goal version.
func (t *Tx) TaskRevision(taskID string, revision int) (w.TaskRevision, error) {
	return get[w.TaskRevision](t, "SELECT body FROM task_revisions WHERE task_id=? AND revision=?", taskID, revision)
}

// TaskRevisions returns every historical goal, newest first.
func (t *Tx) TaskRevisions(taskID string) ([]w.TaskRevision, error) {
	return list[w.TaskRevision](t, "SELECT body FROM task_revisions WHERE task_id=? ORDER BY revision DESC", taskID)
}

// InsertBudgetAccount creates the one stable root ledger.
func (t *Tx) InsertBudgetAccount(v w.BudgetAccount) error {
	return t.exec("INSERT INTO budget_accounts(id,root_task_id,revision,body) VALUES(?,?,?,?)", v.ID, v.RootTaskID, v.Revision, body(v))
}

// BudgetAccount reads a root ledger.
func (t *Tx) BudgetAccount(id string) (w.BudgetAccount, error) {
	return get[w.BudgetAccount](t, "SELECT body FROM budget_accounts WHERE id=?", id)
}

// SaveBudgetAccount updates usage or policy with an explicit revision CAS.
func (t *Tx) SaveBudgetAccount(v w.BudgetAccount, expectedRevision int) error {
	result, err := t.tx.ExecContext(t.ctx, "UPDATE budget_accounts SET revision=?,body=? WHERE id=? AND revision=?", v.Revision, body(v), v.ID, expectedRevision)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return w.ErrConflict
	}
	return nil
}

// SaveProjectRecord inserts an immutable project record version.
func (t *Tx) SaveProjectRecord(v w.ProjectRecord) error {
	return t.exec("INSERT INTO project_records(id,workspace_id,revision,status,body) VALUES(?,?,?,?,?)", v.ID, v.WorkspaceID, v.Revision, v.Status, body(v))
}

// ProjectRecord reads an exact version.
func (t *Tx) ProjectRecord(id string, revision int) (w.ProjectRecord, error) {
	return get[w.ProjectRecord](t, "SELECT body FROM project_records WHERE id=? AND revision=?", id, revision)
}

// ProjectRecords lists all versions; project-state selection is deterministic in
// the domain service and never substitutes newest for currently active blindly.
func (t *Tx) ProjectRecords(workspaceID string) ([]w.ProjectRecord, error) {
	return list[w.ProjectRecord](t, "SELECT body FROM project_records WHERE workspace_id=? ORDER BY id,revision", workspaceID)
}

// InsertCompletion appends an immutable completion judgment.
func (t *Tx) InsertCompletion(v w.CompletionRecord) error {
	return t.exec("INSERT INTO completion_records(id,task_id,task_revision,evaluation_generation,body) VALUES(?,?,?,?,?)", v.ID, v.TaskID, v.TaskRevision, v.EvaluationGeneration, body(v))
}

// Completions returns current and historical acceptance evidence.
func (t *Tx) Completions(taskID string) ([]w.CompletionRecord, error) {
	return list[w.CompletionRecord](t, "SELECT body FROM completion_records WHERE task_id=? ORDER BY rowid", taskID)
}

// SaveDependency inserts one exact dependency edge.
func (t *Tx) SaveDependency(v w.TaskDependency) error {
	return t.exec("INSERT INTO task_dependencies(id,producer_task_id,consumer_task_id,completion_ref,artifact_ref,body) VALUES(?,?,?,?,?,?)", v.ID, v.ProducerTaskID, v.ConsumerTaskID, v.CompletionRef, v.ArtifactRef, body(v))
}

// Dependencies lists the workspace DAG through two bounded queries by the caller.
func (t *Tx) Dependencies(workspaceID string) ([]w.TaskDependency, error) {
	return list[w.TaskDependency](t, "SELECT d.body FROM task_dependencies d JOIN tasks c ON c.id=d.consumer_task_id WHERE c.workspace_id=? ORDER BY d.rowid", workspaceID)
}

// SaveDependencyValidity marks revalidation without deleting the original edge.
func (t *Tx) SaveDependencyValidity(v w.TaskDependency) error {
	return t.exec("UPDATE task_dependencies SET body=? WHERE id=?", body(v), v.ID)
}

// SaveGrant creates or advances a persistent grant epoch.
func (t *Tx) SaveGrant(v w.Grant) error {
	return t.exec("INSERT INTO grants(id,workspace_id,subject_member_id,revision,epoch,status,body) VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision,epoch=excluded.epoch,status=excluded.status,body=excluded.body", v.ID, v.WorkspaceID, v.SubjectMemberID, v.Revision, v.Epoch, v.Status, body(v))
}

// Grant reads one authorization.
func (t *Tx) Grant(id string) (w.Grant, error) {
	return get[w.Grant](t, "SELECT body FROM grants WHERE id=?", id)
}

// Grants lists current and revoked grants for audit and effective-scope checks.
func (t *Tx) Grants(workspaceID string) ([]w.Grant, error) {
	return list[w.Grant](t, "SELECT body FROM grants WHERE workspace_id=? ORDER BY rowid", workspaceID)
}

// PendingGrants returns revocations that still require stop reconciliation.
func (t *Tx) PendingGrants() ([]w.Grant, error) {
	return list[w.Grant](t, "SELECT body FROM grants WHERE status IN ('revoking','revocation_uncertain') ORDER BY rowid")
}

// SaveExecution creates or updates an execution directory projection.
func (t *Tx) SaveExecution(v w.WorkspaceExecution) error {
	return t.exec("INSERT INTO workspace_executions(id,workspace_id,task_id,canonical_root,lease_epoch,lease_expires_at,status,body) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET lease_epoch=excluded.lease_epoch,lease_expires_at=excluded.lease_expires_at,status=excluded.status,body=excluded.body", v.ID, v.WorkspaceID, v.TaskID, v.CanonicalRoot, v.LeaseEpoch, v.LeaseExpiresAt, v.Status, body(v))
}

// AcquireExecution atomically rejects exact, parent/child and canonical-path
// conflicts across every workspace and every process using this database.
func (t *Tx) AcquireExecution(v w.WorkspaceExecution) error {
	result, err := t.tx.ExecContext(t.ctx, `INSERT INTO workspace_executions(id,workspace_id,task_id,canonical_root,lease_epoch,lease_expires_at,status,body)
		SELECT ?,?,?,?,?,?,?,? WHERE NOT EXISTS (
			SELECT 1 FROM workspace_executions WHERE status IN ('active','quarantined','uncertain')
			AND (canonical_root=? OR canonical_root LIKE rtrim(?, '/')||'/%' OR ? LIKE rtrim(canonical_root, '/')||'/%')
		)`, v.ID, v.WorkspaceID, v.TaskID, v.CanonicalRoot, v.LeaseEpoch, v.LeaseExpiresAt, v.Status, body(v), v.CanonicalRoot, v.CanonicalRoot, v.CanonicalRoot)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		return w.ErrConflict
	}
	return err
}

// SaveExecutionEpoch refuses stale lease owners from overwriting a newer epoch.
func (t *Tx) SaveExecutionEpoch(v w.WorkspaceExecution, expectedEpoch int) error {
	result, err := t.tx.ExecContext(t.ctx, "UPDATE workspace_executions SET lease_epoch=?,lease_expires_at=?,status=?,body=? WHERE id=? AND lease_epoch=? AND json_extract(body,'$.lease_owner')=?", v.LeaseEpoch, v.LeaseExpiresAt, v.Status, body(v), v.ID, expectedEpoch, v.LeaseOwner)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		return w.ErrConflict
	}
	return err
}

// Execution reads one execution directory.
func (t *Tx) Execution(id string) (w.WorkspaceExecution, error) {
	return get[w.WorkspaceExecution](t, "SELECT body FROM workspace_executions WHERE id=?", id)
}

// Executions lists task directories and leases.
func (t *Tx) Executions(workspaceID string) ([]w.WorkspaceExecution, error) {
	return list[w.WorkspaceExecution](t, "SELECT body FROM workspace_executions WHERE workspace_id=? ORDER BY rowid", workspaceID)
}

// SaveSession persists a disposable native execution cache and its exposure.
func (t *Tx) SaveSession(v w.ExecutionSession) error {
	return t.exec("INSERT INTO execution_sessions(id,task_id,member_id,fingerprint,visibility_epoch,status,body) VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET visibility_epoch=excluded.visibility_epoch,status=excluded.status,body=excluded.body", v.ID, v.TaskID, v.MemberID, v.Fingerprint, v.VisibilityEpoch, v.Status, body(v))
}

// Session reads one managed native session.
func (t *Tx) Session(id string) (w.ExecutionSession, error) {
	return get[w.ExecutionSession](t, "SELECT body FROM execution_sessions WHERE id=?", id)
}

// Sessions lists task-local native sessions, never user terminal history.
func (t *Tx) Sessions(taskID string) ([]w.ExecutionSession, error) {
	return list[w.ExecutionSession](t, "SELECT body FROM execution_sessions WHERE task_id=? ORDER BY rowid", taskID)
}

// SaveEffect creates or reconciles a host-managed effect.
func (t *Tx) SaveEffect(v w.EffectRecord) error {
	return t.exec("INSERT INTO effect_records(id,task_id,idempotency_key,request_digest,status,body) VALUES(?,?,?,?,?,?) ON CONFLICT(task_id,idempotency_key) DO UPDATE SET status=excluded.status,body=excluded.body", v.ID, v.TaskID, v.IdempotencyKey, v.RequestDigest, v.Status, body(v))
}

// EffectByKey supports idempotent recovery and conflicting-digest rejection.
func (t *Tx) EffectByKey(taskID, key string) (w.EffectRecord, error) {
	return get[w.EffectRecord](t, "SELECT body FROM effect_records WHERE task_id=? AND idempotency_key=?", taskID, key)
}

// Effect reads one host-managed side-effect record by its durable identity.
func (t *Tx) Effect(id string) (w.EffectRecord, error) {
	return get[w.EffectRecord](t, "SELECT body FROM effect_records WHERE id=?", id)
}

// Effects lists all effects relevant to task completion and recovery.
func (t *Tx) Effects(taskID string) ([]w.EffectRecord, error) {
	return list[w.EffectRecord](t, "SELECT body FROM effect_records WHERE task_id=? ORDER BY rowid", taskID)
}

// SaveInbox inserts or advances a steering item.
func (t *Tx) SaveInbox(v w.InboxItem) error {
	return t.exec("INSERT INTO inbox_items(id,task_id,idempotency_key,expected_revision,status,body) VALUES(?,?,?,?,?,?) ON CONFLICT(task_id,idempotency_key) DO UPDATE SET status=excluded.status,body=excluded.body", v.ID, v.TaskID, v.IdempotencyKey, v.ExpectedRevision, v.Status, body(v))
}

// InboxByKey reads an idempotent steering submission.
func (t *Tx) InboxByKey(taskID, key string) (w.InboxItem, error) {
	return get[w.InboxItem](t, "SELECT body FROM inbox_items WHERE task_id=? AND idempotency_key=?", taskID, key)
}

// Inbox lists persistent steering state for one task.
func (t *Tx) Inbox(taskID string) ([]w.InboxItem, error) {
	return list[w.InboxItem](t, "SELECT body FROM inbox_items WHERE task_id=? ORDER BY rowid", taskID)
}

// TaskAction reads a durable control-plane idempotency receipt.
func (t *Tx) TaskAction(taskID, requestID string) (w.TaskActionReceipt, error) {
	return get[w.TaskActionReceipt](t, "SELECT body FROM task_action_receipts WHERE task_id=? AND request_id=?", taskID, requestID)
}

// InsertTaskAction writes one control receipt in the same transaction as its projection.
func (t *Tx) InsertTaskAction(v w.TaskActionReceipt) error {
	return t.exec("INSERT INTO task_action_receipts(task_id,request_id,request_digest,body) VALUES(?,?,?,?)", v.TaskID, v.RequestID, v.RequestDigest, body(v))
}

// SaveTaskTransferReceipt advances only the two-phase transfer receipt. Other
// control actions remain immutable after acceptance.
func (t *Tx) SaveTaskTransferReceipt(v w.TaskActionReceipt) error {
	result, err := t.tx.ExecContext(t.ctx, "UPDATE task_action_receipts SET body=? WHERE task_id=? AND request_id=? AND request_digest=?", body(v), v.TaskID, v.RequestID, v.RequestDigest)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err == nil && updated != 1 {
		return w.ErrConflict
	}
	return err
}

// SaveQuestion creates or resolves a revision-bound question.
func (t *Tx) SaveQuestion(v w.Question) error {
	return t.exec("INSERT INTO questions(id,task_id,task_revision,status,body) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET status=excluded.status,body=excluded.body", v.ID, v.TaskID, v.TaskRevision, v.Status, body(v))
}

// Question reads one exact question.
func (t *Tx) Question(id string) (w.Question, error) {
	return get[w.Question](t, "SELECT body FROM questions WHERE id=?", id)
}

// Questions lists open and historical questions.
func (t *Tx) Questions(taskID string) ([]w.Question, error) {
	return list[w.Question](t, "SELECT body FROM questions WHERE task_id=? ORDER BY rowid", taskID)
}

// SaveCollaborationMessage inserts the immutable envelope exactly once.
func (t *Tx) SaveCollaborationMessage(v w.CollaborationMessage) error {
	return t.exec("INSERT INTO collaboration_messages(id,sender_task_id,request_id,target_task_id,body_digest,body) VALUES(?,?,?,?,?,?)", v.ID, v.SenderTaskID, v.RequestID, v.TargetTaskID, v.BodyDigest, body(v))
}

// CollaborationMessageByRequest supports stable idempotent replies.
func (t *Tx) CollaborationMessageByRequest(senderTaskID, requestID string) (w.CollaborationMessage, error) {
	return get[w.CollaborationMessage](t, "SELECT body FROM collaboration_messages WHERE sender_task_id=? AND request_id=?", senderTaskID, requestID)
}

// CollaborationMessage reads one envelope.
func (t *Tx) CollaborationMessage(id string) (w.CollaborationMessage, error) {
	return get[w.CollaborationMessage](t, "SELECT body FROM collaboration_messages WHERE id=?", id)
}

// CollaborationMessages lists incoming or outgoing history with causal order.
func (t *Tx) CollaborationMessages(taskID string) ([]w.CollaborationMessage, error) {
	return list[w.CollaborationMessage](t, "SELECT body FROM collaboration_messages WHERE sender_task_id=? OR target_task_id=? ORDER BY rowid", taskID, taskID)
}

// SaveReceipt advances one recipient's delivery state.
func (t *Tx) SaveReceipt(v w.DeliveryReceipt) error {
	return t.exec("INSERT INTO delivery_receipts(message_id,target_task_id,status,body) VALUES(?,?,?,?) ON CONFLICT(message_id,target_task_id) DO UPDATE SET status=excluded.status,body=excluded.body", v.MessageID, v.TargetTaskID, v.Status, body(v))
}

// Receipts lists per-recipient delivery observations.
func (t *Tx) Receipts(messageID string) ([]w.DeliveryReceipt, error) {
	return list[w.DeliveryReceipt](t, "SELECT body FROM delivery_receipts WHERE message_id=? ORDER BY target_task_id", messageID)
}

// PendingReceiptCount applies durable per-recipient backpressure.
func (t *Tx) PendingReceiptCount(taskID string) (int, error) {
	var count int
	err := t.tx.QueryRowContext(t.ctx, "SELECT COUNT(*) FROM delivery_receipts WHERE target_task_id=? AND status IN ('queued','dispatching','presented','presentation_unknown')", taskID).Scan(&count)
	return count, err
}

// SaveWakeup creates or settles one wait generation.
func (t *Tx) SaveWakeup(v w.Wakeup) error {
	return t.exec("INSERT INTO wakeups(id,task_id,task_revision,wait_generation,wake_source,event_id,status,body) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET status=excluded.status,body=excluded.body", v.ID, v.TaskID, v.TaskRevision, v.WaitGeneration, v.WakeSource, v.EventID, v.Status, body(v))
}

// WakeupBySourceEvent reads one idempotent event generation.
func (t *Tx) WakeupBySourceEvent(source, eventID string) (w.Wakeup, error) {
	return get[w.Wakeup](t, "SELECT body FROM wakeups WHERE wake_source=? AND event_id=? ORDER BY rowid DESC LIMIT 1", source, eventID)
}

// Wakeups lists pending and historical wake registrations.
func (t *Tx) Wakeups(taskID string) ([]w.Wakeup, error) {
	return list[w.Wakeup](t, "SELECT body FROM wakeups WHERE task_id=? ORDER BY rowid", taskID)
}

func (t *Tx) Wakeup(id string) (w.Wakeup, error) {
	return get[w.Wakeup](t, "SELECT body FROM wakeups WHERE id=?", id)
}

func (t *Tx) PendingWakeups() ([]w.Wakeup, error) {
	return list[w.Wakeup](t, "SELECT body FROM wakeups WHERE status='pending' ORDER BY rowid")
}

func (t *Tx) SaveWakeEvent(v w.WakeEvent) error {
	return t.exec("INSERT INTO wake_events(wake_source,event_id,body) VALUES(?,?,?) ON CONFLICT(wake_source,event_id) DO NOTHING", v.WakeSource, v.EventID, body(v))
}

func (t *Tx) WakeEvent(source, eventID string) (w.WakeEvent, error) {
	return get[w.WakeEvent](t, "SELECT body FROM wake_events WHERE wake_source=? AND event_id=?", source, eventID)
}

// SaveMemory appends a versioned derived memory record.
func (t *Tx) SaveMemory(v w.MemoryRecord) error {
	return t.exec("INSERT INTO memory_records(id,workspace_id,revision,body) VALUES(?,?,?,?)", v.ID, v.WorkspaceID, v.Revision, body(v))
}

// Memories returns all versions for visibility-aware selection.
func (t *Tx) Memories(workspaceID string) ([]w.MemoryRecord, error) {
	return list[w.MemoryRecord](t, "SELECT body FROM memory_records WHERE workspace_id=? ORDER BY id,revision", workspaceID)
}

// SaveClaim appends one immutable claim revision.
func (t *Tx) SaveClaim(v w.Claim) error {
	return t.exec("INSERT INTO claims(id,workspace_id,task_id,revision,body) VALUES(?,?,?,?,?)", v.ID, v.WorkspaceID, v.TaskID, v.Revision, body(v))
}

// Claims lists research propositions and counterevidence.
func (t *Tx) Claims(taskID string) ([]w.Claim, error) {
	return list[w.Claim](t, "SELECT body FROM claims WHERE task_id=? ORDER BY id,revision", taskID)
}

// SaveDataset appends one immutable dataset manifest revision.
func (t *Tx) SaveDataset(v w.DatasetManifest) error {
	return t.exec("INSERT INTO dataset_manifests(id,workspace_id,task_id,revision,body) VALUES(?,?,?,?,?)", v.ID, v.WorkspaceID, v.TaskID, v.Revision, body(v))
}

// Dataset reads one exact manifest version.
func (t *Tx) Dataset(id string, revision int) (w.DatasetManifest, error) {
	return get[w.DatasetManifest](t, "SELECT body FROM dataset_manifests WHERE id=? AND revision=?", id, revision)
}

// Datasets lists exact data versions used by research runs.
func (t *Tx) Datasets(taskID string) ([]w.DatasetManifest, error) {
	return list[w.DatasetManifest](t, "SELECT body FROM dataset_manifests WHERE task_id=? ORDER BY id,revision", taskID)
}

// SaveContextManifest persists the exact host-computed startup scope and brief.
func (t *Tx) SaveContextManifest(v w.ContextManifest) error {
	return t.exec("INSERT INTO context_manifests(id,attempt_id,task_id,body) VALUES(?,?,?,?)", v.ID, nullable(v.AttemptID), v.TaskID, body(v))
}

// ContextManifest reads an immutable execution-segment input manifest.
func (t *Tx) ContextManifest(id string) (w.ContextManifest, error) {
	return get[w.ContextManifest](t, "SELECT body FROM context_manifests WHERE id=?", id)
}

// SaveReadReceipt records only bytes the host actually returned to an execution.
func (t *Tx) SaveReadReceipt(v w.ReadReceipt) error {
	return t.exec("INSERT INTO read_receipts(id,manifest_id,body) VALUES(?,?,?)", v.ID, v.ManifestID, body(v))
}

// ReadReceipts lists exact delivered pages for one startup manifest.
func (t *Tx) ReadReceipts(manifestID string) ([]w.ReadReceipt, error) {
	return list[w.ReadReceipt](t, "SELECT body FROM read_receipts WHERE manifest_id=? ORDER BY rowid", manifestID)
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// isUniqueConstraint keeps persistence errors independent of driver concrete types.
func isUniqueConstraint(err error) bool {
	if err == nil || errors.Is(err, sql.ErrNoRows) {
		return false
	}
	var sqliteError interface{ Code() int }
	if errors.As(err, &sqliteError) {
		code := sqliteError.Code()
		return code == 1555 || code == 2067
	}
	return false
}

// TaskRuns lists every run bound to one task in creation order. It is the
// bounded projection TaskDetail uses instead of loading a full thread.
func (t *Tx) TaskRuns(taskID string) ([]w.Run, error) {
	return list[w.Run](t, "SELECT body FROM runs WHERE task_id=? ORDER BY created_at,id", taskID)
}

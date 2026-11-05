CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS workspaces(id TEXT PRIMARY KEY, revision INTEGER NOT NULL CHECK(revision > 0), body TEXT NOT NULL CHECK(json_valid(body)));
CREATE TABLE IF NOT EXISTS global_agents(id TEXT PRIMARY KEY, revision INTEGER NOT NULL CHECK(revision > 0), body TEXT NOT NULL CHECK(json_valid(body)));
CREATE TABLE IF NOT EXISTS settings(id TEXT PRIMARY KEY, revision INTEGER NOT NULL CHECK(revision > 0), body TEXT NOT NULL CHECK(json_valid(body)));
CREATE TABLE IF NOT EXISTS workspace_members(workspace_id TEXT NOT NULL REFERENCES workspaces(id), agent_id TEXT NOT NULL REFERENCES global_agents(id), revision INTEGER NOT NULL CHECK(revision > 0), body TEXT NOT NULL CHECK(json_valid(body)), PRIMARY KEY(workspace_id,agent_id));
CREATE INDEX IF NOT EXISTS member_workspaces ON workspace_members(agent_id,workspace_id);
CREATE TABLE IF NOT EXISTS threads(id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), updated_at TEXT NOT NULL, revision INTEGER NOT NULL CHECK(revision > 0), body TEXT NOT NULL CHECK(json_valid(body)));
CREATE TABLE IF NOT EXISTS runs(id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), thread_id TEXT NOT NULL REFERENCES threads(id), task_id TEXT NOT NULL DEFAULT '', status TEXT NOT NULL CHECK(status IN ('queued','running','canceling','succeeded','failed','canceled','interrupted','yielded')), idempotency_key TEXT NOT NULL, request_hash TEXT NOT NULL, created_at TEXT NOT NULL, body TEXT NOT NULL CHECK(json_valid(body)), UNIQUE(thread_id,idempotency_key));
CREATE UNIQUE INDEX IF NOT EXISTS one_active_run_per_thread ON runs(thread_id) WHERE status IN ('queued','running','canceling');
CREATE UNIQUE INDEX IF NOT EXISTS one_active_run_per_task ON runs(task_id) WHERE task_id<>'' AND status IN ('queued','running','canceling');
CREATE INDEX IF NOT EXISTS queue_order ON runs(status,created_at,id);
CREATE TABLE IF NOT EXISTS steps(id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id), status TEXT NOT NULL CHECK(status IN ('pending','running','succeeded','failed','canceled','skipped','interrupted','yielded')), position INTEGER NOT NULL, body TEXT NOT NULL CHECK(json_valid(body)));
CREATE TABLE IF NOT EXISTS messages(id TEXT PRIMARY KEY, thread_id TEXT NOT NULL REFERENCES threads(id), run_id TEXT NOT NULL REFERENCES runs(id), position INTEGER NOT NULL, body TEXT NOT NULL CHECK(json_valid(body)));
CREATE INDEX IF NOT EXISTS message_order ON messages(thread_id,position);
CREATE TABLE IF NOT EXISTS message_chunks(message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED, seq INTEGER NOT NULL CHECK(seq > 0), content TEXT NOT NULL CHECK(length(CAST(content AS BLOB)) BETWEEN 1 AND 8192), PRIMARY KEY(message_id,seq));
CREATE TABLE IF NOT EXISTS events(run_id TEXT NOT NULL REFERENCES runs(id), seq INTEGER NOT NULL CHECK(seq>0), type TEXT NOT NULL, body TEXT NOT NULL CHECK(json_valid(body)), PRIMARY KEY(run_id,seq));
CREATE TABLE IF NOT EXISTS sources(id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), thread_id TEXT NOT NULL REFERENCES threads(id), run_id TEXT NOT NULL REFERENCES runs(id), body TEXT NOT NULL CHECK(json_valid(body)));
CREATE TABLE IF NOT EXISTS artifacts(id TEXT NOT NULL, version INTEGER NOT NULL CHECK(version>0), thread_id TEXT NOT NULL REFERENCES threads(id), run_id TEXT NOT NULL REFERENCES runs(id), revision INTEGER NOT NULL CHECK(revision>0), body TEXT NOT NULL CHECK(json_valid(body)), PRIMARY KEY(id,version));
CREATE TABLE IF NOT EXISTS attempts(id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id), step_id TEXT NOT NULL REFERENCES steps(id), number INTEGER NOT NULL CHECK(number>0), status TEXT NOT NULL CHECK(status IN ('running','succeeded','failed','canceled','interrupted','uncertain','blocked','yielded')), body TEXT NOT NULL CHECK(json_valid(body)), UNIQUE(step_id,number));
CREATE INDEX IF NOT EXISTS attempt_order ON attempts(run_id,step_id,number);
CREATE INDEX IF NOT EXISTS active_attempts ON attempts(status) WHERE status='running';
CREATE TABLE IF NOT EXISTS run_claims(run_id TEXT PRIMARY KEY REFERENCES runs(id), owner_id TEXT NOT NULL, owner_pid INTEGER NOT NULL, owner_start TEXT NOT NULL, claimed_at TEXT NOT NULL, body TEXT NOT NULL CHECK(json_valid(body)));
INSERT OR IGNORE INTO schema_migrations(version,applied_at) VALUES(1,strftime('%Y-%m-%dT%H:%M:%fZ','now'));
INSERT OR IGNORE INTO schema_migrations(version,applied_at) VALUES(2,strftime('%Y-%m-%dT%H:%M:%fZ','now'));
INSERT OR IGNORE INTO schema_migrations(version,applied_at) VALUES(3,strftime('%Y-%m-%dT%H:%M:%fZ','now'));
INSERT OR IGNORE INTO schema_migrations(version,applied_at) VALUES(4,strftime('%Y-%m-%dT%H:%M:%fZ','now'));

-- M1–M4 project collaboration kernel. JSON bodies preserve the same immutable
-- version snapshots as the original workflow store; indexed columns are only
-- CAS, authorization, queue and projection keys.
CREATE TABLE IF NOT EXISTS project_records(id TEXT NOT NULL, workspace_id TEXT NOT NULL REFERENCES workspaces(id), revision INTEGER NOT NULL CHECK(revision>0), status TEXT NOT NULL, body TEXT NOT NULL CHECK(json_valid(body)), PRIMARY KEY(id,revision));
CREATE INDEX IF NOT EXISTS project_record_current ON project_records(workspace_id,id,revision DESC);
CREATE TABLE IF NOT EXISTS tasks(id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), parent_id TEXT, thread_id TEXT NOT NULL REFERENCES threads(id), current_revision INTEGER NOT NULL CHECK(current_revision>0), root_task_id TEXT NOT NULL, budget_account_id TEXT NOT NULL, status TEXT NOT NULL CHECK(status IN ('draft','ready','active','waiting','paused','completed','canceled')), updated_at TEXT NOT NULL, body TEXT NOT NULL CHECK(json_valid(body)));
CREATE INDEX IF NOT EXISTS task_workspace ON tasks(workspace_id,updated_at DESC,id);
CREATE INDEX IF NOT EXISTS task_children ON tasks(parent_id,status);
CREATE TABLE IF NOT EXISTS task_revisions(task_id TEXT NOT NULL REFERENCES tasks(id), revision INTEGER NOT NULL CHECK(revision>0), body TEXT NOT NULL CHECK(json_valid(body)), PRIMARY KEY(task_id,revision));
CREATE TABLE IF NOT EXISTS task_dependencies(id TEXT PRIMARY KEY, producer_task_id TEXT NOT NULL REFERENCES tasks(id), consumer_task_id TEXT NOT NULL REFERENCES tasks(id), completion_ref TEXT NOT NULL DEFAULT '', artifact_ref TEXT NOT NULL DEFAULT '', body TEXT NOT NULL CHECK(json_valid(body)), UNIQUE(producer_task_id,consumer_task_id,completion_ref,artifact_ref));
CREATE TABLE IF NOT EXISTS budget_accounts(id TEXT PRIMARY KEY, root_task_id TEXT NOT NULL UNIQUE REFERENCES tasks(id), revision INTEGER NOT NULL CHECK(revision>0), body TEXT NOT NULL CHECK(json_valid(body)));
CREATE TABLE IF NOT EXISTS completion_records(id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id), task_revision INTEGER NOT NULL, evaluation_generation INTEGER NOT NULL, body TEXT NOT NULL CHECK(json_valid(body)));
CREATE INDEX IF NOT EXISTS completion_task ON completion_records(task_id,task_revision,evaluation_generation);
CREATE TABLE IF NOT EXISTS grants(id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), subject_member_id TEXT NOT NULL, revision INTEGER NOT NULL CHECK(revision>0), epoch INTEGER NOT NULL CHECK(epoch>0), status TEXT NOT NULL CHECK(status IN ('active','revoking','revoked','revocation_uncertain')), body TEXT NOT NULL CHECK(json_valid(body)));
CREATE INDEX IF NOT EXISTS grant_current ON grants(workspace_id,subject_member_id,status,epoch DESC);
CREATE TABLE IF NOT EXISTS workspace_executions(id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), task_id TEXT NOT NULL REFERENCES tasks(id), canonical_root TEXT NOT NULL, lease_epoch INTEGER NOT NULL CHECK(lease_epoch>0), lease_expires_at TEXT NOT NULL, status TEXT NOT NULL, body TEXT NOT NULL CHECK(json_valid(body)));
CREATE UNIQUE INDEX IF NOT EXISTS one_active_execution_root ON workspace_executions(canonical_root) WHERE status IN ('active','quarantined','uncertain');
CREATE TABLE IF NOT EXISTS execution_sessions(id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id), member_id TEXT NOT NULL, fingerprint TEXT NOT NULL, visibility_epoch INTEGER NOT NULL, status TEXT NOT NULL, body TEXT NOT NULL CHECK(json_valid(body)));
CREATE INDEX IF NOT EXISTS session_reuse ON execution_sessions(task_id,member_id,fingerprint,status);
CREATE TABLE IF NOT EXISTS effect_records(id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id), idempotency_key TEXT NOT NULL, request_digest TEXT NOT NULL, status TEXT NOT NULL, body TEXT NOT NULL CHECK(json_valid(body)), UNIQUE(task_id,idempotency_key));
CREATE INDEX IF NOT EXISTS pending_effects ON effect_records(task_id,status);
CREATE TABLE IF NOT EXISTS inbox_items(id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id), idempotency_key TEXT NOT NULL, expected_revision INTEGER NOT NULL, status TEXT NOT NULL, body TEXT NOT NULL CHECK(json_valid(body)), UNIQUE(task_id,idempotency_key));
CREATE TABLE IF NOT EXISTS task_action_receipts(task_id TEXT NOT NULL REFERENCES tasks(id), request_id TEXT NOT NULL, request_digest TEXT NOT NULL, body TEXT NOT NULL CHECK(json_valid(body)), PRIMARY KEY(task_id,request_id));
CREATE TABLE IF NOT EXISTS questions(id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id), task_revision INTEGER NOT NULL, status TEXT NOT NULL, body TEXT NOT NULL CHECK(json_valid(body)));
CREATE TABLE IF NOT EXISTS collaboration_messages(id TEXT PRIMARY KEY, sender_task_id TEXT NOT NULL REFERENCES tasks(id), request_id TEXT NOT NULL, target_task_id TEXT NOT NULL REFERENCES tasks(id), body_digest TEXT NOT NULL, body TEXT NOT NULL CHECK(json_valid(body)), UNIQUE(sender_task_id,request_id));
CREATE INDEX IF NOT EXISTS collaboration_target ON collaboration_messages(target_task_id,id);
CREATE TABLE IF NOT EXISTS delivery_receipts(message_id TEXT NOT NULL REFERENCES collaboration_messages(id), target_task_id TEXT NOT NULL REFERENCES tasks(id), status TEXT NOT NULL, body TEXT NOT NULL CHECK(json_valid(body)), PRIMARY KEY(message_id,target_task_id));
CREATE TABLE IF NOT EXISTS wakeups(id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id), task_revision INTEGER NOT NULL, wait_generation INTEGER NOT NULL, wake_source TEXT NOT NULL, event_id TEXT NOT NULL, status TEXT NOT NULL, body TEXT NOT NULL CHECK(json_valid(body)), UNIQUE(wake_source,event_id,task_revision));
CREATE INDEX IF NOT EXISTS wakeup_pending ON wakeups(status,task_id,wait_generation);
CREATE TABLE IF NOT EXISTS wake_events(wake_source TEXT NOT NULL, event_id TEXT NOT NULL, body TEXT NOT NULL CHECK(json_valid(body)), PRIMARY KEY(wake_source,event_id));
CREATE TABLE IF NOT EXISTS context_manifests(id TEXT PRIMARY KEY, attempt_id TEXT, task_id TEXT NOT NULL REFERENCES tasks(id), body TEXT NOT NULL CHECK(json_valid(body)));
CREATE TABLE IF NOT EXISTS context_scope_entries(manifest_id TEXT NOT NULL REFERENCES context_manifests(id), ref TEXT NOT NULL, body TEXT NOT NULL CHECK(json_valid(body)), PRIMARY KEY(manifest_id,ref));
CREATE TABLE IF NOT EXISTS read_receipts(id TEXT PRIMARY KEY, manifest_id TEXT NOT NULL REFERENCES context_manifests(id), body TEXT NOT NULL CHECK(json_valid(body)));
CREATE TABLE IF NOT EXISTS memory_records(id TEXT NOT NULL, workspace_id TEXT NOT NULL REFERENCES workspaces(id), revision INTEGER NOT NULL CHECK(revision>0), body TEXT NOT NULL CHECK(json_valid(body)), PRIMARY KEY(id,revision));
CREATE TABLE IF NOT EXISTS claims(id TEXT NOT NULL, workspace_id TEXT NOT NULL REFERENCES workspaces(id), task_id TEXT NOT NULL REFERENCES tasks(id), revision INTEGER NOT NULL CHECK(revision>0), body TEXT NOT NULL CHECK(json_valid(body)), PRIMARY KEY(id,revision));
CREATE TABLE IF NOT EXISTS dataset_manifests(id TEXT NOT NULL, workspace_id TEXT NOT NULL REFERENCES workspaces(id), task_id TEXT NOT NULL REFERENCES tasks(id), revision INTEGER NOT NULL CHECK(revision>0), body TEXT NOT NULL CHECK(json_valid(body)), PRIMARY KEY(id,revision));
INSERT OR IGNORE INTO schema_migrations(version,applied_at) VALUES(5,strftime('%Y-%m-%dT%H:%M:%fZ','now'));

-- First input may create a project and conversation before Run idempotency exists.
CREATE TABLE IF NOT EXISTS bootstrap_receipts(key TEXT PRIMARY KEY, request_digest TEXT NOT NULL, workspace_id TEXT NOT NULL REFERENCES workspaces(id), thread_id TEXT NOT NULL REFERENCES threads(id));
INSERT OR IGNORE INTO schema_migrations(version,applied_at) VALUES(6,strftime('%Y-%m-%dT%H:%M:%fZ','now'));

-- Uploads precede Runs and retain failed originals; a parsed import is published
-- atomically with the first Run that explicitly selects it.
CREATE TABLE IF NOT EXISTS source_imports(id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), thread_id TEXT NOT NULL REFERENCES threads(id), body TEXT NOT NULL CHECK(json_valid(body)));
CREATE INDEX IF NOT EXISTS source_import_threads ON source_imports(thread_id);
INSERT OR IGNORE INTO schema_migrations(version,applied_at) VALUES(7,strftime('%Y-%m-%dT%H:%M:%fZ','now'));

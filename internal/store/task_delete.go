package store

import (
	"fmt"
	"strings"
)

// DeleteTaskRows removes the task-scoped aggregate without touching the
// conversation. Callers must delete the associated threads after validating
// that no task outside the deletion set shares them.
func (t *Tx) DeleteTaskRows(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	list := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	queries := []string{
		fmt.Sprintf("DELETE FROM delivery_receipts WHERE target_task_id IN (%s) OR message_id IN (SELECT id FROM collaboration_messages WHERE sender_task_id IN (%s))", list, list),
		fmt.Sprintf("DELETE FROM collaboration_messages WHERE sender_task_id IN (%s) OR target_task_id IN (%s)", list, list),
		fmt.Sprintf("DELETE FROM wakeups WHERE task_id IN (%s)", list),
		fmt.Sprintf("DELETE FROM inbox_items WHERE task_id IN (%s)", list),
		fmt.Sprintf("DELETE FROM task_action_receipts WHERE task_id IN (%s)", list),
		fmt.Sprintf("DELETE FROM task_creation_receipts WHERE task_id IN (%s)", list),
		fmt.Sprintf("DELETE FROM questions WHERE task_id IN (%s)", list),
		fmt.Sprintf("DELETE FROM effect_records WHERE task_id IN (%s)", list),
		fmt.Sprintf("DELETE FROM execution_sessions WHERE task_id IN (%s)", list),
		fmt.Sprintf("DELETE FROM workspace_executions WHERE task_id IN (%s)", list),
		fmt.Sprintf("DELETE FROM completion_records WHERE task_id IN (%s)", list),
		fmt.Sprintf("DELETE FROM task_dependencies WHERE producer_task_id IN (%s) OR consumer_task_id IN (%s)", list, list),
		fmt.Sprintf("DELETE FROM read_receipts WHERE manifest_id IN (SELECT id FROM context_manifests WHERE task_id IN (%s))", list),
		fmt.Sprintf("DELETE FROM context_scope_entries WHERE manifest_id IN (SELECT id FROM context_manifests WHERE task_id IN (%s))", list),
		fmt.Sprintf("DELETE FROM context_manifests WHERE task_id IN (%s)", list),
		fmt.Sprintf("DELETE FROM claims WHERE task_id IN (%s)", list),
		fmt.Sprintf("DELETE FROM dataset_manifests WHERE task_id IN (%s)", list),
		fmt.Sprintf("DELETE FROM task_revisions WHERE task_id IN (%s)", list),
		fmt.Sprintf("DELETE FROM budget_accounts WHERE root_task_id IN (%s)", list),
		fmt.Sprintf("DELETE FROM tasks WHERE id IN (%s)", list),
	}
	for _, query := range queries {
		args := make([]any, strings.Count(query, "?"))
		for i := range args {
			args[i] = ids[i%len(ids)]
		}
		if err := t.exec(query, args...); err != nil {
			return err
		}
	}
	return nil
}

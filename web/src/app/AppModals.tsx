import type {
  Agent,
  Artifact,
  Run,
  Settings,
  SettingsInput,
  Source,
  System,
  Thread,
  ThreadView,
  Workspace,
} from "../api/client";
import type { WorkflowKey } from "../components/Composer";
import { ArtifactReader } from "../components/ArtifactReader";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { SettingsDialog, SnapshotDialog } from "../components/SettingsDialog";
import { ThreadForm } from "../components/forms/ThreadForm";
import { ThreadRenameForm } from "../components/forms/ThreadRenameForm";
import { WorkspaceForm } from "../components/forms/WorkspaceForm";
import { WorkspaceMembersForm } from "../components/forms/WorkspaceMembersForm";
import { api } from "../api/client";
import type { Appearance } from "../components/AppearanceSelect";
import type { ExecutionMode } from "./useGlobalSettings";
import { errorText } from "../lib/presentation";

export type ModalState =
  | { kind: "workspace" | "edit-workspace" | "agents" | "settings" }
  | { kind: "thread" }
  | { kind: "edit-thread"; thread?: Thread }
  | { kind: "snapshot"; run: Run }
  | { kind: "artifact" }
  | { kind: "rename-thread"; thread: Thread }
  | { kind: "delete-workspace"; workspace: Workspace };

/**
 * Every dialog the shell can open. Rendering is exclusive by construction:
 * `modal` holds a single state, so two dialogs can never stack.
 */
export function AppModals({
  modal,
  close,
  system,
  setSystem,
  workspace,
  workspaceID,
  agents,
  globalAgents,
  view,
  settings,
  setSettings,
  saveSettings,
  executionMode,
  onExecutionModeChange,
  permissionLoading,
  permissionError,
  permissionThreadID,
  permissionThreadTitle,
  artifact,
  threads,
  lock,
  navigate,
  loadSpaces,
  loadMembers,
  loadThreads,
  loadGlobalAgents,
  setView,
  setWorkflowChoices,
  setArtifactRef,
  setPanelOpen,
  setError,
  openSource,
  saveFeedback,
  appearance,
  onAppearance,
  setModal,
}: {
  modal: ModalState | null;
  setModal: (value: ModalState | null) => void;
  close: () => void;
  system?: System;
  setSystem: (value: System) => void;
  workspace?: Workspace;
  workspaceID: string;
  agents: Agent[];
  globalAgents: Agent[];
  view?: ThreadView;
  settings?: Settings;
  setSettings: (value: Settings) => void;
  artifact?: Artifact;
  threads: Thread[];
  lock: () => Promise<void>;
  navigate: (ws: string, th?: string) => void;
  loadSpaces: () => Promise<Workspace[]>;
  loadMembers: (id: string) => Promise<void>;
  loadThreads: (id: string) => Promise<void>;
  loadGlobalAgents: () => Promise<void>;
  setView: (
    update: (current: ThreadView | undefined) => ThreadView | undefined,
  ) => void;
  setWorkflowChoices: (
    update: (
      current: Record<string, WorkflowKey>,
    ) => Record<string, WorkflowKey>,
  ) => void;
  setArtifactRef: (value?: Artifact) => void;
  setPanelOpen: (value: boolean) => void;
  setError: (value: string) => void;
  openSource: (value: Source | string) => void;
  saveFeedback: (value: string) => Promise<void>;
  appearance: Appearance;
  onAppearance: (value: Appearance) => void;
  /** Field-merge settings save shared with the Composer permission select. */
  saveSettings?: (value: SettingsInput) => Promise<Settings>;
  executionMode?: ExecutionMode;
  onExecutionModeChange?: (mode: ExecutionMode) => Promise<unknown>;
  permissionLoading?: boolean;
  permissionError?: string;
  permissionThreadID?: string;
  permissionThreadTitle?: string;
}) {
  return (
    <>
      {(modal?.kind === "workspace" || modal?.kind === "edit-workspace") &&
        system && (
          <WorkspaceForm
            current={modal.kind === "edit-workspace" ? workspace : undefined}
            system={system}
            close={close}
            onDelete={
              modal.kind === "edit-workspace" && workspace
                ? () => setModal({ kind: "delete-workspace", workspace })
                : undefined
            }
            save={async (value) => {
              const saved = await api<Workspace>(
                value.id
                  ? `/api/v1/workspaces/${value.id}`
                  : "/api/v1/workspaces",
                value.id ? "PATCH" : "POST",
                {
                  name: value.name,
                  description: value.description,
                  root: value.root,
                  skill_ids: value.skill_ids,
                },
                value.id ? { "If-Match": String(value.revision) } : {},
              );
              await loadSpaces();
              close();
              if (!value.id) navigate(saved.id);
            }}
          />
        )}
      {modal?.kind === "agents" && workspaceID && (
        <WorkspaceMembersForm
          agents={agents}
          catalog={globalAgents}
          workspaceID={workspaceID}
          refresh={() => loadMembers(workspaceID)}
          close={close}
        />
      )}
      {(modal?.kind === "thread" || modal?.kind === "edit-thread") &&
        workspace && (
          <ThreadForm
            current={
              modal.kind === "edit-thread"
                ? (modal.thread ?? view?.thread)
                : undefined
            }
            agents={agents.filter((agent) => !agent.disabled)}
            defaultCoordinatorID={settings?.default_coordinator_id}
            close={close}
            save={async (value) => {
              const th = await api<Thread>(
                value.id
                  ? `/api/v1/threads/${value.id}`
                  : `/api/v1/workspaces/${workspaceID}/threads`,
                value.id ? "PATCH" : "POST",
                {
                  title: value.title,
                  agent_ids: value.agent_ids,
                  coordinator_id: value.coordinator_id,
                  workflow: value.workflow,
                },
                value.id ? { "If-Match": String(value.revision) } : {},
              );
              setWorkflowChoices((current) => ({
                ...current,
                [th.id]: th.workflow as WorkflowKey,
              }));
              if (value.id)
                setView((current) =>
                  current?.thread.id === th.id
                    ? { ...current, thread: th }
                    : current,
                );
              close();
              await loadThreads(workspaceID);
              if (!value.id) navigate(workspaceID, th.id);
            }}
          />
        )}
      {modal?.kind === "snapshot" && (
        <SnapshotDialog run={modal.run} close={close} />
      )}
      {modal?.kind === "settings" && system && (
        <SettingsDialog
          system={system}
          setSystem={setSystem}
          close={close}
          lock={lock}
          settings={settings}
          saveSettings={saveSettings}
          agents={globalAgents}
          saveAgent={async (value) => {
            const saved = await api<Agent>(
              value.id ? `/api/v1/agents/${value.id}` : "/api/v1/agents",
              value.id ? "PATCH" : "POST",
              value,
              value.id ? { "If-Match": String(value.revision) } : {},
            );
            await loadGlobalAgents();
            if (workspaceID) await loadMembers(workspaceID);
            return saved;
          }}
          deleteAgent={async (agent) => {
            await api(
              `/api/v1/agents/${agent.id}`,
              "DELETE",
              undefined,
              { "If-Match": String(agent.revision) },
            );
            // The profile is gone even if a follow-up refresh fails; keep the
            // delete confirmed and let the next navigation reload stale lists.
            await loadGlobalAgents().catch(() => {});
            if (workspaceID) await loadMembers(workspaceID).catch(() => {});
            setView((current) => {
              if (!current || !current.thread.agent_ids?.includes(agent.id))
                return current;
              const agentIDs = current.thread.agent_ids.filter(
                (id) => id !== agent.id,
              );
              return {
                ...current,
                thread: {
                  ...current.thread,
                  agent_ids: agentIDs,
                  coordinator_id:
                    current.thread.coordinator_id === agent.id
                      ? (agentIDs[0] ?? "")
                      : current.thread.coordinator_id,
                  revision: current.thread.revision + 1,
                },
              };
            });
            await api<Settings>("/api/v1/settings")
              .then(setSettings)
              .catch(() => {});
          }}
          onTrashChanged={() => loadThreads(workspaceID)}
          appearance={appearance}
          onAppearance={onAppearance}
          executionMode={executionMode}
          onExecutionModeChange={onExecutionModeChange}
          permissionLoading={permissionLoading}
          permissionError={permissionError}
          permissionThreadID={permissionThreadID}
          permissionThreadTitle={permissionThreadTitle}
        />
      )}
      {modal?.kind === "artifact" && artifact && (
        <ArtifactReader
          artifact={artifact}
          sources={view?.sources ?? []}
          close={close}
          onFeedback={(value) => void saveFeedback(value)}
          onReference={() => {
            setArtifactRef(artifact);
            close();
            setPanelOpen(false);
            requestAnimationFrame(() =>
              document.getElementById("composer-input")?.focus(),
            );
          }}
          onSource={(id) => {
            close();
            openSource(id);
          }}
          onError={(error) => setError(errorText(error))}
        />
      )}
      {modal?.kind === "rename-thread" && (
        <ThreadRenameForm
          thread={modal.thread}
          close={close}
          save={async (title) => {
            // The title-only update leaves members, coordinator and workflow untouched.
            const saved = await api<Thread>(
              `/api/v1/threads/${modal.thread.id}`,
              "PATCH",
              { title },
              { "If-Match": String(modal.thread.revision) },
            );
            setView((current) =>
              current?.thread.id === saved.id
                ? { ...current, thread: saved }
                : current,
            );
            close();
            await loadThreads(workspaceID);
          }}
        />
      )}
      {modal?.kind === "delete-workspace" && (
        <ConfirmDialog
          title="删除工作空间"
          action="删除工作空间"
          close={close}
          confirm={async () => {
            await api(`/api/v1/workspaces/${modal.workspace.id}`, "DELETE");
            close();
            const spaces = await loadSpaces();
            navigate(spaces[0]?.id ?? "");
          }}
        >
          <p>
            “{modal.workspace.name}”及其中的 {threads.length}{" "}
            个会话、成员关联、来源和产物会被永久删除，无法恢复。全局成员档案会保留。
          </p>
        </ConfirmDialog>
      )}
    </>
  );
}

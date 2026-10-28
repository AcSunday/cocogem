import { useEffect, useState } from "react";
import type { ComponentProps } from "react";
import { api } from "../../api/client";
import type { Agent } from "../../api/client";
import { errorText } from "../../lib/presentation";
import { Modal } from "../Modal";
import { MembersForm } from "./MembersForm";

export function MembersSettings({
  refresh,
  ...props
}: ComponentProps<typeof MembersForm> & { refresh: () => Promise<void> }) {
  const [catalog, setCatalog] = useState<Agent[]>();
  const [error, setError] = useState("");
  const load = async () => {
    const result = await api<{ items: Agent[] }>("/api/v1/agents");
    setCatalog(result.items ?? []);
  };
  useEffect(() => {
    let canceled = false;
    void api<{ items: Agent[] }>("/api/v1/agents")
      .then((result) => {
        if (!canceled) setCatalog(result.items ?? []);
      })
      .catch((error) => {
        if (!canceled) setError(errorText(error));
      });
    return () => {
      canceled = true;
    };
  }, []);
  if (!catalog)
    return (
      <Modal title="成员与称呼" close={props.close}>
        {error ? (
          <p role="alert">{error}</p>
        ) : (
          <p role="status">正在读取全局成员…</p>
        )}
      </Modal>
    );
  return (
    <MembersForm
      {...props}
      catalog={catalog}
      save={async (value) => {
        const saved = await props.save(value);
        await load();
        return saved;
      }}
      saveMembership={async (agent, member, grant) => {
        await api(
          `/api/v1/workspaces/${props.workspaceID}/members/${agent.id}`,
          member ? "PATCH" : "PUT",
          grant,
          member ? { "If-Match": String(member.revision) } : {},
        );
        await refresh();
      }}
      unlink={async (member) => {
        await api(
          `/api/v1/workspaces/${props.workspaceID}/members/${member.agent_id}`,
          "DELETE",
          undefined,
          { "If-Match": String(member.revision) },
        );
        await refresh();
      }}
    />
  );
}

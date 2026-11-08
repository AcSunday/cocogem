import { render, screen } from "@testing-library/react";
import { beforeAll, expect, it, vi } from "vitest";
import type { Artifact } from "../api/client";
import { ArtifactReader } from "./ArtifactReader";

beforeAll(() => {
  HTMLDialogElement.prototype.showModal = function () { this.open = true; };
  HTMLDialogElement.prototype.close = function () { this.open = false; };
});

it("downloads the exact delivered file version", () => {
  const artifact = {
    id: "artifact", version: 3, revision: 1, thread_id: "thread", run_id: "run", step_id: "step",
    type: "note", title: "修订稿", body: "已完成修订", findings: [], source_ids: [], limitations: [],
    feedback: "unreviewed", feedback_at: "", created_at: "2026-11-05T00:00:00Z",
    files: [{source_id: "output-source", name: "修改后的论文.docx", media_type: "application/octet-stream", size: 2048, sha256: "exact-digest"}],
  } as Artifact;
  render(<ArtifactReader artifact={artifact} sources={[]} close={vi.fn()} onFeedback={vi.fn()} onReference={vi.fn()} onSource={vi.fn()} onError={vi.fn()} />);
  expect(screen.getByRole("link", {name: /下载 修改后的论文.docx/})).toHaveAttribute("href", "/api/v1/artifacts/artifact/versions/3/files/output-source");
});

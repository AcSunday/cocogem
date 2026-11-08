import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, expect, it, vi } from "vitest";
import { TaskAcceptanceDialog } from "./TaskAcceptanceDialog";

beforeAll(() => {
  HTMLDialogElement.prototype.showModal = function () {
    this.open = true;
  };
  HTMLDialogElement.prototype.close = function () {
    this.open = false;
  };
});

afterEach(cleanup);

const candidates = [
  { ref: "artifact:report@v2", label: "研究报告 · v2" },
  { ref: "message:reply", label: "会话交付 · 2026-11-04 08:00" },
];

const criteria = [
  {
    criterion_id: "outcome",
    statement: "结论有来源",
    required: true,
  },
];

it("submits an exact candidate and the outcome evidence", async () => {
  const close = vi.fn();
  const submit = vi.fn().mockResolvedValue(undefined);
  const user = userEvent.setup();
  render(
    <TaskAcceptanceDialog
      goal="完成可追溯研究"
      criteria={criteria}
      candidates={candidates}
      close={close}
      submit={submit}
    />,
  );

  expect(screen.getByRole("dialog", { name: "人工验收" })).toBeVisible();
  expect(screen.getByRole("radio", { name: "研究报告 · v2" })).toBeChecked();
  const evidence = screen.getByRole("checkbox", { name: "研究报告 · v2" });
  expect(evidence).toBeChecked();
  await user.selectOptions(
    screen.getByRole("combobox", { name: "判断：结论有来源" }),
    "passed",
  );
  await user.click(screen.getByRole("button", { name: "提交验收" }));

  await waitFor(() =>
    expect(submit).toHaveBeenCalledWith({
      candidate_refs: ["artifact:report@v2"],
      results: [
        {
          criterion_id: "outcome",
          result: "passed",
          evidence: ["artifact:report@v2"],
          limitations: [],
        },
      ],
    }),
  );
  expect(close).toHaveBeenCalledOnce();
});

it("reuses the same outcome evidence when the exact candidate changes", async () => {
  const close = vi.fn();
  const submit = vi.fn().mockResolvedValue(undefined);
  const user = userEvent.setup();
  render(
    <TaskAcceptanceDialog
      goal="完成可追溯研究"
      criteria={criteria}
      candidates={candidates}
      close={close}
      submit={submit}
    />,
  );

  await user.click(screen.getByRole("radio", { name: /会话交付/ }));
  const checkboxes = screen.getAllByRole("checkbox");
  expect(checkboxes[0]).not.toBeChecked();
  expect(checkboxes[1]).toBeChecked();
  await user.selectOptions(
    screen.getByRole("combobox", { name: "判断：结论有来源" }),
    "passed",
  );
  await user.click(screen.getByRole("button", { name: "提交验收" }));

  await waitFor(() =>
    expect(submit).toHaveBeenCalledWith(
      expect.objectContaining({
        candidate_refs: ["message:reply"],
        results: [
          expect.objectContaining({
            evidence: ["message:reply"],
            result: "passed",
          }),
        ],
      }),
    ),
  );
});

it("requires evidence for a passed criterion and keeps invalid input unsaved", async () => {
  const close = vi.fn();
  const submit = vi.fn();
  const user = userEvent.setup();
  render(
    <TaskAcceptanceDialog
      goal="完成可追溯研究"
      criteria={criteria}
      candidates={candidates}
      close={close}
      submit={submit}
    />,
  );

  await user.selectOptions(
    screen.getByRole("combobox", { name: "判断：结论有来源" }),
    "passed",
  );
  await user.click(screen.getByRole("checkbox", { name: "研究报告 · v2" }));
  expect(
    screen.getByText("标记为“通过”的验收项至少需要选择一项证据。"),
  ).toBeVisible();
  expect(screen.getByRole("button", { name: "提交验收" })).toBeDisabled();
  expect(submit).not.toHaveBeenCalled();
});

it("keeps the exact candidate and judgement when saving fails", async () => {
  const close = vi.fn();
  const submit = vi.fn().mockRejectedValue(new Error("保存失败"));
  const user = userEvent.setup();
  render(
    <TaskAcceptanceDialog
      goal="完成可追溯研究"
      criteria={criteria}
      candidates={candidates}
      close={close}
      submit={submit}
    />,
  );

  await user.click(screen.getByRole("radio", { name: /会话交付/ }));
  await user.selectOptions(
    screen.getByRole("combobox", { name: "判断：结论有来源" }),
    "passed",
  );
  await user.click(screen.getByRole("button", { name: "提交验收" }));

  expect(await screen.findByRole("alert")).toHaveTextContent("保存失败");
  expect(close).not.toHaveBeenCalled();
  expect(screen.getByRole("radio", { name: /会话交付/ })).toBeChecked();
  expect(
    screen.getByRole("combobox", { name: "判断：结论有来源" }),
  ).toHaveValue("passed");
});

it("does not treat an optional unchecked criterion as blocking completion", async () => {
  const close = vi.fn();
  const submit = vi.fn().mockResolvedValue(undefined);
  const user = userEvent.setup();
  render(
    <TaskAcceptanceDialog
      goal="完成可追溯研究"
      criteria={[
        ...criteria,
        {
          criterion_id: "note",
          statement: "附注可读",
          required: false,
        },
      ]}
      candidates={candidates}
      close={close}
      submit={submit}
    />,
  );

  await user.selectOptions(
    screen.getByRole("combobox", { name: "判断：结论有来源" }),
    "passed",
  );
  expect(
    screen.queryByText(/项必需验收未通过或未核实/),
  ).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "提交验收" }));
  await waitFor(() =>
    expect(submit).toHaveBeenCalledWith(
      expect.objectContaining({
        results: [
          expect.objectContaining({
            criterion_id: "outcome",
            result: "passed",
          }),
          expect.objectContaining({
            criterion_id: "note",
            result: "inconclusive",
          }),
        ],
      }),
    ),
  );
});

it("submits once even when the form is delivered twice", async () => {
  let finish: () => void = () => {};
  const close = vi.fn();
  const submit = vi.fn(
    () =>
      new Promise<void>((resolve) => {
        finish = resolve;
      }),
  );
  render(
    <TaskAcceptanceDialog
      goal="完成可追溯研究"
      criteria={criteria}
      candidates={candidates}
      close={close}
      submit={submit}
    />,
  );

  const form = screen.getByRole("form", { name: "人工验收表单" });
  fireEvent.submit(form);
  fireEvent.submit(form);
  expect(submit).toHaveBeenCalledOnce();
  finish();
  await waitFor(() => expect(close).toHaveBeenCalledOnce());
});

it("blocks every dismiss path while a save is pending", async () => {
  let finish: () => void = () => {};
  const close = vi.fn();
  const submit = vi.fn(
    () =>
      new Promise<void>((resolve) => {
        finish = resolve;
      }),
  );
  const user = userEvent.setup();
  render(
    <TaskAcceptanceDialog
      goal="完成可追溯研究"
      criteria={criteria}
      candidates={candidates}
      close={close}
      submit={submit}
    />,
  );

  await user.selectOptions(
    screen.getByRole("combobox", { name: "判断：结论有来源" }),
    "passed",
  );
  fireEvent.submit(screen.getByRole("form", { name: "人工验收表单" }));
  expect(submit).toHaveBeenCalledOnce();

  const dialog = screen.getByRole("dialog", { name: "人工验收" });
  await user.click(screen.getByRole("button", { name: "关闭窗口" }));
  fireEvent(dialog, new Event("cancel", { cancelable: true }));
  fireEvent.pointerDown(dialog);
  fireEvent.click(dialog);

  expect(close).not.toHaveBeenCalled();
  expect(screen.getByRole("dialog", { name: "人工验收" })).toBeVisible();
  expect(submit).toHaveBeenCalledOnce();

  finish();
  await waitFor(() => expect(close).toHaveBeenCalledOnce());
  expect(submit).toHaveBeenCalledOnce();
});

it("blocks submission while the basis is expired", async () => {
  const close = vi.fn();
  const refresh = vi.fn();
  const submit = vi.fn();
  render(
    <TaskAcceptanceDialog
      goal="完成可追溯研究"
      criteria={criteria}
      candidates={candidates}
      expired
      refresh={refresh}
      close={close}
      submit={submit}
    />,
  );

  expect(screen.getByRole("alert")).toHaveTextContent("验收依据已更新");
  const form = screen.getByRole("form", { name: "人工验收表单" });
  fireEvent.submit(form);
  expect(submit).not.toHaveBeenCalled();
  expect(screen.getByRole("button", { name: "验收依据已变化" })).toBeDisabled();
  fireEvent.click(screen.getByRole("button", { name: "重新核对" }));
  expect(refresh).toHaveBeenCalledOnce();
  expect(close).not.toHaveBeenCalled();
});

it("closing without saving never submits or defaults to passed", async () => {
  const close = vi.fn();
  const submit = vi.fn();
  const user = userEvent.setup();
  render(
    <TaskAcceptanceDialog
      goal="完成可追溯研究"
      criteria={criteria}
      candidates={candidates}
      close={close}
      submit={submit}
    />,
  );

  const dialog = screen.getByRole("dialog", { name: "人工验收" });
  expect(
    within(dialog).getByRole("combobox", { name: "判断：结论有来源" }),
  ).toHaveValue("inconclusive");
  await user.click(screen.getByRole("button", { name: "取消" }));
  expect(close).toHaveBeenCalledOnce();
  expect(submit).not.toHaveBeenCalled();
});

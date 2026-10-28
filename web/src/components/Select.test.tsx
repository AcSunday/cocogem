import { useState } from "react";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { Select } from "./Select";

function Example({ required = false }: { required?: boolean }) {
  const [value, setValue] = useState("");
  return (
    <>
      <label>
        本地资料目录
        <Select value={value} onChange={setValue} required={required}>
          <option value="">暂不绑定</option>
          <option value="disabled" disabled>
            不可用目录
          </option>
          <option value="docs">项目文档</option>
          <option value="research">Research</option>
        </Select>
      </label>
      <button type="button">外部按钮</button>
    </>
  );
}
afterEach(cleanup);

it("selects with keyboard, skips disabled choices and restores focus without submitting", async () => {
  const submit = vi.fn((event) => event.preventDefault());
  render(
    <form onSubmit={submit}>
      <Example />
    </form>,
  );
  const user = userEvent.setup();
  const control = screen.getByRole("combobox", { name: "本地资料目录" });
  await user.click(control);
  expect(control).toHaveAttribute("aria-expanded", "true");
  await user.keyboard("{ArrowDown}{Enter}");
  expect(control).toHaveTextContent("项目文档");
  expect(control).toHaveFocus();
  expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  expect(submit).not.toHaveBeenCalled();
  await user.click(control);
  await user.keyboard("r{Enter}");
  expect(control).toHaveTextContent("Research");
});

it("dismisses with Escape and outside clicks while keeping the value", async () => {
  render(<Example />);
  const user = userEvent.setup();
  const control = screen.getByRole("combobox");
  await user.click(control);
  await user.keyboard("{ArrowDown}{Escape}");
  expect(control).toHaveTextContent("暂不绑定");
  await user.click(control);
  await user.click(screen.getByRole("button", { name: "外部按钮" }));
  expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
});

it("preserves required form validation and focuses the visible control", async () => {
  const submit = vi.fn((event) => event.preventDefault());
  render(
    <form onSubmit={submit}>
      <Example required />
      <button>保存</button>
    </form>,
  );
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "保存" }));
  const control = screen.getByRole("combobox");
  expect(submit).not.toHaveBeenCalled();
  expect(control).toHaveFocus();
  expect(control).toHaveAttribute("aria-invalid", "true");
  await user.click(control);
  await user.click(screen.getByRole("option", { name: "项目文档" }));
  await user.click(screen.getByRole("button", { name: "保存" }));
  expect(submit).toHaveBeenCalledOnce();
});

it("scrolls keyboard choices inside the menu without scrolling its ancestors", async () => {
  render(<Example />);
  const user = userEvent.setup();
  const control = screen.getByRole("combobox");
  await user.click(control);
  const list = screen.getByRole("listbox");
  Object.defineProperty(list, "clientHeight", { value: 72 });
  for (const [index, option] of screen.getAllByRole("option").entries()) {
    Object.defineProperty(option, "offsetTop", { value: index * 36 });
    Object.defineProperty(option, "offsetHeight", { value: 36 });
  }
  await user.keyboard("{End}");
  expect(list.scrollTop).toBe(72);
  await user.keyboard("{Home}");
  expect(list.scrollTop).toBe(0);
  expect(control).toHaveAttribute("aria-expanded", "true");
});

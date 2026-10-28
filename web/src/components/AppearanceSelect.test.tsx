import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { AppearanceSelect } from "./AppearanceSelect";

afterEach(cleanup);

it("shows the current choice and changes it from the keyboard", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  render(<AppearanceSelect value="system" onChange={onChange} />);
  expect(screen.getByText("外观")).toBeInTheDocument();
  const trigger = screen.getByRole("combobox", { name: "外观模式" });
  expect(trigger).toHaveTextContent("跟随系统");
  trigger.focus();
  await user.keyboard("{Enter}{ArrowDown}{Enter}");
  expect(onChange).toHaveBeenCalledWith("light");
  expect(trigger).toHaveFocus();
});

it("opens from the label and hands pointer choices to the app", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  const view = render(<AppearanceSelect value="system" onChange={onChange} />);
  await user.click(screen.getByText("外观"));
  await user.click(screen.getByRole("option", { name: "深色" }));
  expect(onChange).toHaveBeenCalledWith("dark");
  view.rerender(<AppearanceSelect value="dark" onChange={onChange} />);
  expect(screen.getByRole("combobox", { name: "外观模式" })).toHaveTextContent(
    "深色",
  );
});

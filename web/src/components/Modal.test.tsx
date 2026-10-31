import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeAll, expect, it, vi } from "vitest";
import { Modal } from "./Modal";

beforeAll(() => {
  HTMLDialogElement.prototype.showModal = function () {
    this.open = true;
  };
  HTMLDialogElement.prototype.close = function () {
    this.open = false;
  };
});

afterEach(cleanup);

it("ignores the file picker's cancel and synthetic backdrop events", () => {
  const close = vi.fn();
  render(
    <Modal title="设置" close={close}>
      <input type="file" aria-label="上传头像" />
    </Modal>,
  );
  const dialog = document.querySelector("dialog")!;
  screen.getByLabelText("上传头像").focus();

  fireEvent(dialog, new Event("cancel", { cancelable: true }));
  expect(close).not.toHaveBeenCalled();

  // The browser can replay a click on the dialog without a press on it.
  fireEvent.click(dialog);
  expect(close).not.toHaveBeenCalled();

  // A real backdrop press still closes.
  fireEvent.pointerDown(dialog);
  fireEvent.click(dialog);
  expect(close).toHaveBeenCalledOnce();
});

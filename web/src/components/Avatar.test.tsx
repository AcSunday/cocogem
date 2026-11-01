import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { Avatar } from "./Avatar";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const png = "data:image/png;base64,iVBORw0KGgo=";
const jpeg = "data:image/jpeg;base64,/9j/4AAQ";
const tone = (element: Element | null) =>
  element?.className.match(/avatar-tone-\d+/)?.[0];

it("renders only decoded PNG/JPEG data URIs and falls back to the initial", () => {
  const { container } = render(
    <>
      <Avatar name="布偶猫" avatar={png} />
      <Avatar name="缅因猫" avatar={jpeg} />
      <Avatar name="暹罗猫" avatar="data:image/webp;base64,AAAA" />
      <Avatar name="三花猫" avatar="data:image/svg+xml;base64,AAAA" />
      <Avatar name="橘猫" avatar="https://example.test/a.svg" />
      <Avatar name="黑猫" avatar="🦊" />
    </>,
  );
  const images = [...container.querySelectorAll("img")];
  expect(images.map((image) => image.getAttribute("src"))).toEqual([png, jpeg]);
  for (const image of images) expect(tone(image.parentElement)).toBeUndefined();
  for (const name of ["暹罗猫", "三花猫", "橘猫", "黑猫"]) {
    expect(screen.getByText([...name][0])).toBeInTheDocument();
  }
  expect(screen.queryByText("🦊")).toBeNull();
});

it("keeps the name's color across sizes, rerenders and surrounding whitespace", () => {
  const { container, rerender } = render(
    <>
      <Avatar name="布偶猫" avatar="remote/path.png" />
      <Avatar name=" 布偶猫 " small />
    </>,
  );
  const original = tone(container.firstElementChild);
  expect(original).toBeDefined();
  expect(tone(container.lastElementChild)).toBe(original);
  expect(container.lastElementChild?.className).toContain("avatar-sm");
  expect(container.firstElementChild).toHaveTextContent("布");
  rerender(<Avatar name="布偶猫" />);
  expect(tone(container.firstElementChild)).toBe(original);
});

it("uses the full name so matching initials can have different colors", () => {
  const { container } = render(
    <>
      <Avatar name="阿青" />
      <Avatar name="阿蓝" />
    </>,
  );
  expect(container.firstElementChild).toHaveTextContent("阿");
  expect(container.lastElementChild).toHaveTextContent("阿");
  expect(tone(container.firstElementChild)).not.toBe(
    tone(container.lastElementChild),
  );
});

it("normalizes Unicode names and handles blank names without browser storage", () => {
  const read = vi.spyOn(Storage.prototype, "getItem");
  const write = vi.spyOn(Storage.prototype, "setItem");
  const { container } = render(
    <>
      <Avatar name="Éclair" />
      <Avatar name={"E\u0301clair"} />
      <Avatar name="   " />
      <Avatar name="🐱猫" />
    </>,
  );
  expect(tone(container.children[0])).toBe(tone(container.children[1]));
  expect(container.children[2]).toHaveTextContent("?");
  expect(tone(container.children[2])).toBeDefined();
  expect(container.children[3]).toHaveTextContent("🐱");
  expect(read).not.toHaveBeenCalled();
  expect(write).not.toHaveBeenCalled();
});

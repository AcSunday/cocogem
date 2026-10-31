import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { Avatar } from "./Avatar";

afterEach(cleanup);

const png = "data:image/png;base64,iVBORw0KGgo=";
const jpeg = "data:image/jpeg;base64,/9j/4AAQ";

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
  for (const name of ["暹罗猫", "三花猫", "橘猫", "黑猫"]) {
    expect(screen.getByText([...name][0])).toBeInTheDocument();
  }
  expect(screen.queryByText("🦊")).toBeNull();
});

it("keeps the legacy props for index tones and small sizing", () => {
  const { container } = render(
    <Avatar name="布偶猫" index={2} small avatar="remote/path.png" />,
  );
  const avatar = container.firstElementChild;
  expect(avatar?.className).toContain("tone-2");
  expect(avatar?.className).toContain("avatar-sm");
  expect(avatar).toHaveTextContent("布");
});

import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

export async function chooseOption(
  user: ReturnType<typeof userEvent.setup>,
  control: HTMLElement,
  value: string,
) {
  await user.click(control);
  const option = screen
    .getAllByRole("option")
    .find((item) => item.dataset.value === value);
  if (!option) throw new Error(`Missing option: ${value}`);
  await user.click(option);
}

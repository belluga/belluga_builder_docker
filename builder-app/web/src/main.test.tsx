import { screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";

vi.mock("./projects", () => ({ Projects: () => <p>Registered Project directory</p> }));
vi.mock("./viewer", () => ({ Viewer: () => <p>Retired fixed viewer</p> }));

it("opens the registered Project directory at the root entrypoint", async () => {
  history.replaceState({}, "", "/");
  document.body.innerHTML = '<div id="root"></div>';
  await import("./main");
  expect(await screen.findByText("Registered Project directory")).toBeInTheDocument();
  expect(screen.queryByText("Retired fixed viewer")).not.toBeInTheDocument();
});

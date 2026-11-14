import { screen } from "@testing-library/react";
import { shellCopy } from "../copy/shell";
import { meta } from "../test/fixtures";
import { renderWithTheme, stubApi } from "../test/render";
import { ModeBanner } from "./ModeBanner";

const mount = () => renderWithTheme(<ModeBanner />);

it("shows the local-mode notice once /meta says local", async () => {
  stubApi({ "/api/v1/meta": { ...meta, authMode: "local" } });
  mount();
  expect(await screen.findByText(shellCopy.localMode)).toBeInTheDocument();
  expect(screen.getByTestId("mode-banner")).toHaveAttribute("role", "note");
});

it("shows nothing in secure mode or while /meta loads", async () => {
  stubApi({ "/api/v1/meta": { ...meta, authMode: "secure" } });
  mount();
  expect(screen.queryByTestId("mode-banner")).toBeNull();
  await new Promise((r) => setTimeout(r, 50));
  expect(screen.queryByTestId("mode-banner")).toBeNull();
});

import { screen, waitFor } from "@testing-library/react";
import { shellCopy } from "../copy/shell";
import { meta } from "../test/fixtures";
import { renderWithTheme, stubApi } from "../test/render";
import { ModeBanner } from "./ModeBanner";

const mount = () => renderWithTheme(<ModeBanner />);
const stamp = (mode?: string) => {
  if (mode) document.documentElement.dataset.authMode = mode;
  else delete document.documentElement.dataset.authMode;
};

afterEach(() => {
  stamp();
  vi.unstubAllGlobals();
});

it("shows the local-mode notice once /meta says local", async () => {
  stubApi({ "/api/v1/meta": { ...meta, authMode: "local" } });
  mount();
  expect(await screen.findByText(shellCopy.localMode)).toBeInTheDocument();
  expect(screen.getByTestId("mode-banner")).toHaveAttribute("role", "note");
});

it("shows nothing when /meta says secure", async () => {
  stubApi({ "/api/v1/meta": { ...meta, authMode: "secure" } });
  mount();
  await waitFor(() => expect(fetch).toHaveBeenCalledWith("/api/v1/meta", expect.anything()));
  await waitFor(() => expect(screen.queryByTestId("mode-banner")).toBeNull());
});

it("shows nothing while /meta loads and the page carries no stamp", () => {
  stubApi({ "/api/v1/meta": { ...meta, authMode: "local" } });
  mount();
  expect(screen.queryByTestId("mode-banner")).toBeNull();
});

it("occupies its space on first render, before /meta resolves, when the page is stamped local", () => {
  stamp("local");
  stubApi({ "/api/v1/meta": { ...meta, authMode: "local" } });
  mount();
  // Synchronous: no await, so /meta has not resolved.
  expect(screen.getByText(shellCopy.localMode)).toBeInTheDocument();
});

it("shows nothing when the page is stamped secure, whatever /meta says", () => {
  stamp("secure");
  stubApi({ "/api/v1/meta": { ...meta, authMode: "local" } });
  mount();
  expect(screen.queryByTestId("mode-banner")).toBeNull();
});

import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { SWRConfig } from "swr";
import { resetSignedInMarkerForTests } from "../auth/signedIn";
import { renderWithTheme, stubApi, testSWR } from "../test/render";
import type { CorePages } from "./extension";
import { Shell } from "./Shell";
import { INTRO_MS, SignInIntro } from "./SignInIntro";

const pages: CorePages = {
  ClusterLanding: () => <p>cluster landing</p>,
  InstallPage: () => <p>install page</p>,
  FindingsPage: () => <p>findings page</p>,
  ResourcePage: () => <p>resource page</p>,
};

// A fresh page load at url, with the overview's data answering at once.
function load(url: string) {
  resetSignedInMarkerForTests();
  window.history.pushState({}, "", url);
  stubApi({ "/api/v1/installs": [], "/api/v1/meta": { version: "dev", synced: true, kinds: [] } });
  return render(
    <SWRConfig value={testSWR()}>
      <Shell pages={pages} />
    </SWRConfig>,
  );
}

const reducedMotion = (on: boolean) =>
  vi.stubGlobal("matchMedia", (q: string) => ({
    matches: on && q.includes("reduce"),
    media: q,
    addEventListener: () => {},
    removeEventListener: () => {},
  }));

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  window.history.pushState({}, "", "/");
});

it("plays after a fresh sign-in, removes the marker, and loads the page underneath", async () => {
  load("/about?x=1#signed-in");
  expect(screen.getByTestId("signin-intro")).toHaveAttribute("aria-hidden", "true");
  expect(window.location.hash).toBe("");
  expect(window.location.pathname + window.location.search).toBe("/about?x=1");
  // The page renders while the intro plays.
  expect(await screen.findByRole("heading", { level: 1 })).toBeInTheDocument();
});

it("does not play on a reload or a visit without the marker", () => {
  const first = load("/about#signed-in");
  expect(screen.getByTestId("signin-intro")).toBeInTheDocument();
  first.unmount();
  load(window.location.pathname);
  expect(screen.queryByTestId("signin-intro")).toBeNull();
});

it("keeps another fragment and does not play", () => {
  load("/about#section");
  expect(screen.queryByTestId("signin-intro")).toBeNull();
  expect(window.location.hash).toBe("#section");
});

it("skips the animation under reduced motion, still removing the marker", () => {
  reducedMotion(true);
  load("/about#signed-in");
  expect(screen.queryByTestId("signin-intro")).toBeNull();
  expect(window.location.hash).toBe("");
});

it("ends by itself after the intro and leaves focus on the page", () => {
  // The intro stays short: a longer one would add time to every sign-in.
  expect(INTRO_MS).toBeLessThanOrEqual(1500);
  vi.useFakeTimers();
  renderWithTheme(<SignInIntro />);
  expect(screen.getByTestId("signin-intro")).toBeInTheDocument();
  act(() => vi.advanceTimersByTime(INTRO_MS - 1));
  expect(screen.getByTestId("signin-intro")).toBeInTheDocument();
  act(() => vi.advanceTimersByTime(1));
  expect(screen.queryByTestId("signin-intro")).toBeNull();
  expect(document.activeElement).toBe(document.body);
});

it.each([
  ["a click", () => fireEvent.pointerDown(screen.getByTestId("signin-intro"))],
  // The key is used up: fireEvent returns false when the default was prevented, so Space does not scroll the page.
  ["a key", () => expect(fireEvent.keyDown(document.body, { key: " " })).toBe(false)],
])("skips on %s", async (_, skip) => {
  renderWithTheme(<SignInIntro />);
  act(() => {
    skip();
  });
  // A short fade, well before the intro would have ended.
  await waitFor(() => expect(screen.queryByTestId("signin-intro")).toBeNull(), { timeout: INTRO_MS / 3 });
});

it("lets Tab skip the intro without cancelling Tab", async () => {
  renderWithTheme(
    <>
      <button type="button">first</button>
      <SignInIntro />
    </>,
  );
  // Tab keeps its default, so keyboard and screen reader navigation work through the intro.
  let tabbed = false;
  act(() => {
    tabbed = fireEvent.keyDown(document.body, { key: "Tab" });
  });
  expect(tabbed).toBe(true);
  await waitFor(() => expect(screen.queryByTestId("signin-intro")).toBeNull(), { timeout: INTRO_MS / 3 });
});

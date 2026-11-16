import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router-dom";
import { SWRConfig } from "swr";
import installs from "../../../../../pkg/api/testdata/golden/installs.json";
import { resetAuthForTests } from "../auth/authState";
import { listCopy } from "../copy/explorer";
import { shellCopy } from "../copy/shell";
import { RoutesPage } from "../pages/explore/RoutesPage";
import { meta } from "../test/fixtures";
import { pseudoRules, renderWithTheme, stubApi, testSWR } from "../test/render";
import { NamespaceScope } from "./NamespaceScope";
import { getNamespaceScope, NAMESPACE_SCOPE_KEY, setNamespaceScope } from "./namespaceScopeStore";
import { ABOUT_PATH } from "./paths";
import { Shell } from "./Shell";

const ID = "kgw.kgateway";
const copy = shellCopy.namespaceScope;
const choices = { install: ID, namespaces: ["infra", "shop"], collectedAt: new Date().toISOString() };

function mount() {
  return renderWithTheme(<NamespaceScope installId={ID} />);
}
const trigger = () => screen.getByRole("button", { name: new RegExp(`^${copy.label}`) });
const panel = () => screen.getByRole("dialog", { name: copy.label });
const box = (name: string) => within(panel()).getByRole("checkbox", { name });

beforeEach(() => {
  localStorage.clear();
  setNamespaceScope([]);
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

it("defaults to all namespaces and lists the server's choices", async () => {
  stubApi({ [`/api/v1/installs/${ID}/namespaces`]: choices });
  mount();
  expect(trigger()).toHaveTextContent(copy.all);
  // Nothing is fetched until the panel opens.
  expect(vi.mocked(fetch)).not.toHaveBeenCalled();
  await userEvent.click(trigger());
  expect(trigger()).toHaveAttribute("aria-expanded", "true");
  expect(box(copy.all)).toBeChecked();
  await waitFor(() => expect(box("shop")).not.toBeChecked());
  expect(box("infra")).not.toBeChecked();
});

it("chooses several namespaces, and All namespaces clears them", async () => {
  stubApi({ [`/api/v1/installs/${ID}/namespaces`]: choices });
  mount();
  await userEvent.click(trigger());
  await userEvent.click(await within(panel()).findByRole("checkbox", { name: "shop" }));
  expect(getNamespaceScope()).toEqual(["shop"]);
  expect(trigger()).toHaveTextContent("shop");
  await userEvent.click(box("infra"));
  expect(getNamespaceScope()).toEqual(["infra", "shop"]);
  expect(trigger()).toHaveTextContent(copy.count(2));
  expect(box(copy.all)).not.toBeChecked();
  await userEvent.click(box("shop"));
  expect(getNamespaceScope()).toEqual(["infra"]);
  await userEvent.click(box(copy.all));
  expect(getNamespaceScope()).toEqual([]);
  expect(localStorage.getItem(NAMESPACE_SCOPE_KEY)).toBeNull();
});

it("adds a typed namespace, and rejects a name that is not one", async () => {
  stubApi({ [`/api/v1/installs/${ID}/namespaces`]: choices });
  mount();
  await userEvent.click(trigger());
  const input = within(panel()).getByRole("textbox", { name: copy.inputLabel });
  expect(input).toHaveFocus();
  expect(input).toHaveAttribute("placeholder", copy.placeholder);
  await userEvent.type(input, "Not_Valid{Enter}");
  expect(within(panel()).getByText(copy.invalid)).toBeInTheDocument();
  expect(getNamespaceScope()).toEqual([]);
  await userEvent.clear(input);
  await userEvent.type(input, "team-a{Enter}");
  expect(getNamespaceScope()).toEqual(["team-a"]);
  expect(input).toHaveValue("");
  // A typed namespace the server does not list is still a choice, checked.
  expect(box("team-a")).toBeChecked();
});

it("says so when there is nothing to choose", async () => {
  stubApi({ [`/api/v1/installs/${ID}/namespaces`]: { ...choices, namespaces: [] } });
  mount();
  await userEvent.click(trigger());
  expect(await within(panel()).findByText(copy.empty)).toBeInTheDocument();
});

it("closes on Escape and returns focus to the button", async () => {
  stubApi({ [`/api/v1/installs/${ID}/namespaces`]: choices });
  mount();
  await userEvent.click(trigger());
  await userEvent.keyboard("{Escape}");
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(trigger()).toHaveFocus();
});

it("has hover, active and focus styles, the app tooltip and no native title", async () => {
  stubApi({ [`/api/v1/installs/${ID}/namespaces`]: choices });
  mount();
  const b = trigger();
  expect(b).not.toHaveAttribute("title");
  expect(pseudoRules(b, ":hover").length).toBeGreaterThan(0);
  expect(pseudoRules(b, ":active").length).toBeGreaterThan(0);
  expect(pseudoRules(b, ":focus-visible").join("")).toMatch(/outline/);
  await userEvent.tab();
  expect(b).toHaveFocus();
  expect(await screen.findByRole("tooltip")).toHaveTextContent(`${copy.label}: ${copy.all}`);
  await userEvent.click(b);
  const row = (await within(panel()).findByRole("checkbox", { name: "shop" })).closest("label") as HTMLElement;
  expect(pseudoRules(row, ":hover").length).toBeGreaterThan(0);
  expect(pseudoRules(row, ":active").length).toBeGreaterThan(0);
  expect(pseudoRules(row, ":focus-within").length).toBeGreaterThan(0);
});

describe("in the header", () => {
  const pages = {
    ClusterLanding: () => null,
    InstallPage: () => <p>install</p>,
    FindingsPage: () => null,
    ResourcePage: () => null,
  };
  afterEach(() => {
    delete document.documentElement.dataset.authMode;
    resetAuthForTests();
  });

  it.each(["local", "secure"])("shows on install pages only, in %s mode", async (mode) => {
    if (mode === "secure") document.documentElement.dataset.authMode = mode;
    stubApi({
      "/auth/status": { mode, signIn: "ready" },
      "/auth/me": { username: "alice@example.com", groups: [] },
      "/api/v1/meta": meta,
      "/api/v1/installs": installs.body,
    });
    window.history.pushState({}, "", "/i/kgw.kgateway");
    const { unmount } = render(
      <SWRConfig value={testSWR()}>
        <Shell pages={pages} />
      </SWRConfig>,
    );
    // Present from the first paint, before any data, so nothing shifts when it loads.
    expect(within(screen.getByRole("banner")).getByTestId("namespace-scope")).toBeInTheDocument();
    unmount();
    window.history.pushState({}, "", ABOUT_PATH);
    render(
      <SWRConfig value={testSWR()}>
        <Shell pages={pages} />
      </SWRConfig>,
    );
    expect(within(screen.getByRole("banner")).queryByTestId("namespace-scope")).toBeNull();
  });
});

it("marks an active scope on the button with its count, not colour alone", async () => {
  stubApi({ [`/api/v1/installs/${ID}/namespaces`]: choices });
  mount();
  expect(screen.queryByTestId("scope-count")).toBeNull();
  act(() => setNamespaceScope(["infra", "shop"]));
  expect(screen.getByTestId("scope-count")).toHaveAttribute("data-count", "2");
  expect(trigger()).toHaveAttribute("data-scoped", "true");
  await userEvent.hover(trigger());
  expect(await screen.findByRole("tooltip")).toHaveTextContent(`${copy.label}: infra, shop`);
});

it("a scoped list page that comes back empty says so and offers all namespaces", async () => {
  const lists = ["routes", "gateways", "listenersets", "paths", "backends", "findings", "processes"];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) => {
      const u = new URL(url, "http://ui");
      if (u.pathname === "/api/v1/meta")
        return new Response(
          JSON.stringify({ ...meta, filters: Object.fromEntries(lists.map((l) => [l, ["namespaces"]])) }),
        );
      if (u.pathname.endsWith("/routes"))
        return new Response(
          JSON.stringify({ install: ID, items: [], total: 0, collectedAt: new Date().toISOString() }),
        );
      return new Response(JSON.stringify({ error: "not found" }), { status: 404 });
    }),
  );
  setNamespaceScope(["shop"]);
  renderWithTheme(
    <Routes>
      <Route path="/i/:id/routes" element={<RoutesPage />} />
    </Routes>,
    { path: `/i/${ID}/routes` },
  );
  expect(await screen.findByText("No routes in these namespaces.")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: copy.showAll }));
  expect(getNamespaceScope()).toEqual([]);
  expect(await screen.findByText(listCopy.empty("routes"))).toBeInTheDocument();
});

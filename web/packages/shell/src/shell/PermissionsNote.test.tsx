import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SWRConfig } from "swr";
import { resetAuthForTests } from "../auth/authState";
import { shellCopy } from "../copy/shell";
import { meta } from "../test/fixtures";
import { pseudoRules, renderWithTheme, stubApi, testSWR } from "../test/render";
import { PermissionsNote } from "./PermissionsNote";
import { Shell } from "./Shell";

const stamp = (mode?: string) => {
  if (mode) document.documentElement.dataset.authMode = mode;
  else delete document.documentElement.dataset.authMode;
};
const mount = (compact = false) => renderWithTheme(<PermissionsNote compact={compact} />);

afterEach(() => {
  stamp();
  vi.unstubAllGlobals();
});

it("shows the note in secure mode when /meta says the view is limited", async () => {
  stamp("secure");
  stubApi({ "/api/v1/meta": { ...meta, limitedView: true } });
  mount();
  const note = await screen.findByRole("note");
  expect(note).toHaveTextContent(shellCopy.permissionsNote);
  // Info style: an icon, not colour alone.
  expect(note.querySelector("svg")).not.toBeNull();
});

it("reserves its slot in secure mode before /meta answers", () => {
  stamp("secure");
  stubApi({ "/api/v1/meta": { ...meta, limitedView: true } });
  mount();
  // Synchronous: /meta has not resolved, yet the slot is there.
  expect(screen.getByTestId("permissions-note-slot")).toBeInTheDocument();
  expect(screen.queryByRole("note")).toBeNull();
});

it("shows no note for a full reader, keeping the slot", async () => {
  stamp("secure");
  stubApi({ "/api/v1/meta": meta });
  mount();
  await waitFor(() => expect(fetch).toHaveBeenCalledWith("/api/v1/meta", expect.anything()));
  expect(screen.getByTestId("permissions-note-slot")).toBeInTheDocument();
  expect(screen.queryByText(shellCopy.permissionsNote)).toBeNull();
});

it("renders nothing in local mode, whatever /meta says", async () => {
  stamp("local");
  stubApi({ "/api/v1/meta": { ...meta, authMode: "local", limitedView: true } });
  mount();
  await waitFor(() => expect(fetch).toHaveBeenCalledWith("/api/v1/meta", expect.anything()));
  expect(screen.queryByTestId("permissions-note-slot")).toBeNull();
  expect(screen.queryByText(shellCopy.permissionsNote)).toBeNull();
});

it("is an icon with the app's tooltip below COMPACT", async () => {
  stamp("secure");
  stubApi({ "/api/v1/meta": { ...meta, limitedView: true } });
  mount(true);
  const note = await screen.findByRole("note", { name: shellCopy.permissionsNote });
  expect(note).not.toHaveTextContent(shellCopy.permissionsNote);
  expect(note).not.toHaveAttribute("title");
  await userEvent.tab();
  expect(note).toHaveFocus();
  expect(await screen.findByRole("tooltip")).toHaveTextContent(shellCopy.permissionsNote);
});

it("has hover and focus styles", async () => {
  stamp("secure");
  stubApi({ "/api/v1/meta": { ...meta, limitedView: true } });
  mount(true);
  const note = await screen.findByRole("note");
  expect(pseudoRules(note, ":hover").join("")).toMatch(/background/);
  expect(pseudoRules(note, ":focus-visible").join("")).toMatch(/outline/);
});

describe("in the header", () => {
  const pages = {
    ClusterLanding: () => null,
    InstallPage: () => <p>install</p>,
    FindingsPage: () => null,
    ResourcePage: () => null,
  };
  const hidden = {
    id: "kgw.kgateway",
    displayName: "",
    version: "",
    edition: "",
    controllerNames: [],
    readiness: { state: "unknown" as const },
    detailsHidden: true,
    health: { state: "healthy" as const, counts: {} },
    groupRows: 0,
    computedAt: new Date().toISOString(),
  };
  beforeEach(() => resetAuthForTests());

  it("shows the note, the ID as a neutral name and the details note, and no version or readiness", async () => {
    stamp("secure");
    stubApi({
      "/auth/status": { mode: "secure", signIn: "ready" },
      "/auth/me": { username: "alice@example.com", groups: [] },
      "/api/v1/meta": { ...meta, limitedView: true },
      "/api/v1/installs": [hidden],
    });
    window.history.pushState({}, "", "/i/kgw.kgateway");
    render(
      <SWRConfig value={testSWR()}>
        <Shell pages={pages} />
      </SWRConfig>,
    );
    const banner = screen.getByRole("banner");
    expect(within(banner).getByTestId("permissions-note-slot")).toBeInTheDocument();
    expect(await within(banner).findByText(shellCopy.permissionsNote)).toBeInTheDocument();
    expect(await within(banner).findByText(shellCopy.detailsHidden)).toBeInTheDocument();
    expect(banner).toHaveTextContent("kgw.kgateway");
    expect(within(banner).queryByTestId("version")).toBeNull();
    expect(within(banner).queryByTestId("controller-readiness")).toBeNull();
  });
});

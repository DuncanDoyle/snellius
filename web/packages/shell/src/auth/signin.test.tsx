import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SWRConfig } from "swr";
import { authCopy, SECURE_MODE_DOCS } from "../copy/auth";
import { Shell } from "../shell/Shell";
import { meta } from "../test/fixtures";
import { renderWithTheme, stubApi, testSWR } from "../test/render";
import { resetAuthForTests } from "./authState";
import * as nav from "./navigate";
import { SignInFailedPage } from "./SignInPages";

const stamp = (mode?: string) => {
  if (mode) document.documentElement.dataset.authMode = mode;
  else delete document.documentElement.dataset.authMode;
};
const pages = {
  ClusterLanding: () => <p>landing</p>,
  InstallPage: () => null,
  FindingsPage: () => null,
  ResourcePage: () => null,
};
const login = "/auth/login?return=%2F";
const me = { username: "alice@example.com", groups: ["system:authenticated", "platform-admins"] };

beforeEach(() => {
  resetAuthForTests();
  vi.spyOn(nav, "navigate").mockImplementation(() => {});
});
afterEach(() => {
  stamp();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  window.history.pushState({}, "", "/");
});

const mount = () =>
  render(
    <SWRConfig value={testSWR()}>
      <Shell pages={pages} />
    </SWRConfig>,
  );

describe("status pages", () => {
  beforeEach(() => stamp("secure"));

  it("not-configured shows the setup page with the docs link", async () => {
    stubApi({ "/auth/status": { mode: "secure", signIn: "not-configured" }, "/api/v1/meta": meta });
    mount();
    expect(await screen.findByRole("heading", { name: authCopy.notConfiguredTitle })).toBeInTheDocument();
    expect(screen.getByText(authCopy.notConfiguredBody)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: authCopy.notConfiguredLink })).toHaveAttribute("href", SECURE_MODE_DOCS);
  });

  it("unavailable shows the provider page; Try again reloads", async () => {
    const reload = vi.fn();
    vi.stubGlobal("location", { ...window.location, reload, pathname: "/" });
    stubApi({ "/auth/status": { mode: "secure", signIn: "unavailable" }, "/api/v1/meta": meta });
    mount();
    expect(await screen.findByRole("heading", { name: authCopy.unavailableTitle })).toBeInTheDocument();
    expect(screen.getByText(authCopy.unavailableBody)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: authCopy.tryAgain }));
    expect(reload).toHaveBeenCalled();
  });

  it("a 401 sign-in-required shows Taking you to sign-in with the redirect started", async () => {
    stubApi({
      "/auth/status": { mode: "secure", signIn: "ready" },
      "/api/v1/meta": [401, { error: "sign-in-required", login }],
      "/api/v1/installs": [401, { error: "sign-in-required", login }],
      "/auth/me": [401, { error: "sign-in-required", login }],
    });
    mount();
    expect(await screen.findByText(authCopy.redirecting)).toBeInTheDocument();
    expect(nav.navigate).toHaveBeenCalledTimes(1);
    expect(nav.navigate).toHaveBeenCalledWith(login);
  });

  it("a 401 session-ended shows the signed-out page; Sign in again goes to the login URL", async () => {
    const ended = [401, { error: "session-ended", login: "/auth/login?return=%2Fi%2Fa" }];
    stubApi({
      "/auth/status": { mode: "secure", signIn: "ready" },
      "/api/v1/meta": ended,
      "/api/v1/installs": ended,
      "/auth/me": ended,
    });
    mount();
    expect(await screen.findByRole("heading", { name: authCopy.endedTitle })).toBeInTheDocument();
    expect(nav.navigate).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: authCopy.signInAgain }));
    expect(nav.navigate).toHaveBeenCalledWith("/auth/login?return=%2Fi%2Fa");
  });

  it("the 503 secure-mode-incomplete answer keeps the existing connecting state", async () => {
    stubApi({
      "/auth/status": { mode: "secure", signIn: "ready" },
      "/auth/me": me,
      "/api/v1/meta": meta,
      "/api/v1/installs": [503, { error: "secure-mode-incomplete" }],
    });
    mount();
    expect(await screen.findByText("secure-mode-incomplete")).toBeInTheDocument();
    expect(nav.navigate).not.toHaveBeenCalled();
  });
});

describe("/signin-failed", () => {
  it.each(["audience", "unauthenticated"])("reason=%s says the cluster does not accept the sign-in", async (reason) => {
    renderWithTheme(<SignInFailedPage />, { path: `/signin-failed?reason=${reason}` });
    expect(screen.getByRole("heading", { name: authCopy.notAcceptedTitle })).toBeInTheDocument();
    expect(screen.getByText(authCopy.notAcceptedBody)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: authCopy.tryAgain }));
    expect(nav.navigate).toHaveBeenCalledWith(login);
  });

  it("reason=invalid says sign-in failed, without the cluster text", () => {
    renderWithTheme(<SignInFailedPage />, { path: "/signin-failed?reason=invalid" });
    expect(screen.getByRole("heading", { name: authCopy.failedTitle })).toBeInTheDocument();
    expect(screen.queryByText(authCopy.notAcceptedBody)).toBeNull();
  });

  it("the shell shows it as a route, even when /api answers 401, without redirecting", async () => {
    stamp("secure");
    window.history.pushState({}, "", "/signin-failed?reason=audience");
    stubApi({
      "/auth/status": { mode: "secure", signIn: "ready" },
      "/api/v1/meta": [401, { error: "sign-in-required", login }],
      "/api/v1/installs": [401, { error: "sign-in-required", login }],
    });
    mount();
    expect(await screen.findByRole("heading", { name: authCopy.notAcceptedTitle })).toBeInTheDocument();
    await waitFor(() => expect(fetch).toHaveBeenCalledWith("/api/v1/meta", expect.anything()));
    expect(nav.navigate).not.toHaveBeenCalled();
    expect(fetch).not.toHaveBeenCalledWith("/auth/me", expect.anything());
  });
});

describe("header menu", () => {
  beforeEach(() => stamp("secure"));
  const secureApi = (extra: Record<string, unknown> = {}) =>
    stubApi({
      "/auth/status": { mode: "secure", signIn: "ready" },
      "/auth/me": me,
      "/api/v1/meta": meta,
      "/api/v1/installs": [],
      ...extra,
    });

  it("shows the username; Account details lists username and Kubernetes groups", async () => {
    secureApi();
    mount();
    await userEvent.click(await screen.findByRole("button", { name: me.username }));
    await userEvent.click(screen.getByRole("menuitem", { name: authCopy.accountDetails }));
    const dialog = screen.getByRole("dialog", { name: authCopy.accountDetails });
    expect(within(dialog).getByText(me.username)).toBeInTheDocument();
    expect(within(dialog).getByText(authCopy.groupsLabel)).toBeInTheDocument();
    for (const g of me.groups) expect(within(dialog).getByText(g)).toBeInTheDocument();
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("Sign out posts to /auth/logout and navigates to the returned URL", async () => {
    const next = "https://idp.example.com/logout?client_id=x";
    const base = vi.fn(async (url: string, init?: RequestInit) => {
      if (url === "/auth/logout") return new Response(JSON.stringify({ next }), { status: 200 });
      const routes: Record<string, unknown> = {
        "/auth/status": { mode: "secure", signIn: "ready" },
        "/auth/me": me,
        "/api/v1/meta": meta,
        "/api/v1/installs": [],
      };
      void init;
      return new Response(JSON.stringify(routes[url] ?? { error: "not found" }), { status: routes[url] ? 200 : 404 });
    });
    vi.stubGlobal("fetch", base);
    mount();
    await userEvent.click(await screen.findByRole("button", { name: me.username }));
    await userEvent.click(screen.getByRole("menuitem", { name: authCopy.signOut }));
    await waitFor(() => expect(nav.navigate).toHaveBeenCalledWith(next));
    expect(base).toHaveBeenCalledWith("/auth/logout", expect.objectContaining({ method: "POST" }));
  });

  it("reserves the header slot before the identity loads", () => {
    stubApi({ "/auth/status": { mode: "secure", signIn: "ready" }, "/api/v1/meta": meta, "/api/v1/installs": [] });
    const { container } = mount();
    // Synchronous: /auth/me has not answered, yet the slot is there.
    expect(container.querySelector("header [data-testid='shimmer']")).not.toBeNull();
  });

  it("local mode renders no menu and asks for no sign-in endpoint", async () => {
    stamp("local");
    stubApi({ "/api/v1/meta": meta, "/api/v1/installs": [] });
    mount();
    await waitFor(() => expect(fetch).toHaveBeenCalledWith("/api/v1/meta", expect.anything()));
    expect(fetch).not.toHaveBeenCalledWith("/auth/status", expect.anything());
    expect(fetch).not.toHaveBeenCalledWith("/auth/me", expect.anything());
    expect(screen.queryByRole("button", { name: me.username })).toBeNull();
  });
});

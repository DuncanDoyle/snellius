import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SWRConfig } from "swr";
import { apiFetch } from "../api/client";
import { authCopy } from "../copy/auth";
import { Shell } from "../shell/Shell";
import { meta } from "../test/fixtures";
import { stubApi, testSWR } from "../test/render";
import { authState, handleUnauthorized, resetAuthForTests } from "./authState";
import * as nav from "./navigate";

const pages = {
  ClusterLanding: () => <p>landing</p>,
  InstallPage: () => null,
  FindingsPage: () => null,
  ResourcePage: () => null,
};
const me = { username: "alice@example.com", groups: ["g1"] };
const login = "/auth/login?return=%2F";
const mount = () =>
  render(
    <SWRConfig value={testSWR()}>
      <Shell pages={pages} />
    </SWRConfig>,
  );
const routes = {
  "/auth/status": { mode: "secure", signIn: "ready" },
  "/auth/me": me,
  "/api/v1/meta": meta,
  "/api/v1/installs": [],
};

beforeEach(() => {
  document.documentElement.dataset.authMode = "secure";
  sessionStorage.clear();
  resetAuthForTests();
  vi.spyOn(nav, "navigate").mockImplementation(() => {});
});
afterEach(() => {
  delete document.documentElement.dataset.authMode;
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

async function openDetails() {
  const user = userEvent.setup();
  const menuButton = await screen.findByRole("button", { name: me.username });
  await user.click(menuButton);
  await user.click(screen.getByRole("menuitem", { name: authCopy.accountDetails }));
  return { user, menuButton, dialog: await screen.findByRole("dialog") };
}

describe("account details focus", () => {
  it("keeps Tab and Shift+Tab inside the dialog", async () => {
    stubApi(routes);
    mount();
    const { user } = await openDetails();
    const close = screen.getByRole("button", { name: authCopy.close });
    expect(close).toHaveFocus();
    await user.tab();
    expect(close).toHaveFocus();
    await user.tab({ shift: true });
    expect(close).toHaveFocus();
  });

  it("pulls focus back in when it is outside the dialog", async () => {
    stubApi(routes);
    mount();
    const { user, menuButton } = await openDetails();
    menuButton.focus();
    await user.tab();
    expect(screen.getByRole("button", { name: authCopy.close })).toHaveFocus();
  });

  it("Escape closes it after a press on non-focusable panel text, and focus returns to the menu button", async () => {
    stubApi(routes);
    mount();
    const { user, menuButton } = await openDetails();
    await user.click(screen.getByText(authCopy.groupsLabel));
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(menuButton).toHaveFocus();
  });

  it("the close button returns focus to the menu button", async () => {
    stubApi(routes);
    mount();
    const { user, menuButton } = await openDetails();
    await user.click(screen.getByRole("button", { name: authCopy.close }));
    expect(menuButton).toHaveFocus();
  });
});

describe("sign-out", () => {
  const withLogout = (res: () => Response) =>
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string) =>
        url === "/auth/logout"
          ? res()
          : new Response(JSON.stringify((routes as Record<string, unknown>)[url] ?? {}), { status: 200 }),
      ),
    );
  const signOut = async () => {
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: me.username }));
    await user.click(screen.getByRole("menuitem", { name: authCopy.signOut }));
  };

  it.each([
    "javascript:alert(1)",
    "JavaScript:alert(1)",
    "//evil.test/x",
    "data:text/html,x",
    "not a url",
    "/\t/evil.com",
    "/\n/evil.com",
    "/\r/evil.com",
    " /evil",
  ])("refuses next=%s and goes to /", async (next) => {
    withLogout(() => new Response(JSON.stringify({ next }), { status: 200 }));
    mount();
    await signOut();
    await waitFor(() => expect(nav.navigate).toHaveBeenCalledWith("/"));
  });

  it("follows an https next and a relative one", () => {
    expect(nav.safeNext("https://idp.example.com/out?x=1")).toBe("https://idp.example.com/out?x=1");
    expect(nav.safeNext("/")).toBe("/");
    // Percent-encoded whitespace stays a local path; the parser does not decode it into "//".
    expect(nav.safeNext("/%09/evil.com")).toBe("/%09/evil.com");
    expect(new URL("/%09/evil.com", window.location.origin).origin).toBe(window.location.origin);
  });

  it.each(["/\t/evil.com", "/\n/evil.com", "/\r/evil.com", " /evil", "//evil.com", "/\\evil.com", "x"])(
    "isLocalPath rejects %j",
    (v) => {
      expect(nav.isLocalPath(v)).toBe(false);
    },
  );

  it("isLocalPath accepts plain and percent-encoded paths", () => {
    expect(nav.isLocalPath("/auth/login?return=%2Fi%2Fa")).toBe(true);
    expect(nav.isLocalPath("/%09/evil.com")).toBe(true);
  });

  it("shows an error and stays put on a non-2xx logout", async () => {
    withLogout(() => new Response(JSON.stringify({ error: "origin-refused" }), { status: 403 }));
    mount();
    await signOut();
    expect(await screen.findByText("You're still signed in. Try signing out again.")).toBeInTheDocument();
    expect(nav.navigate).not.toHaveBeenCalled();
    // Reopening the menu clears the notice.
    await userEvent.click(screen.getByRole("button", { name: me.username }));
    expect(screen.queryByText(authCopy.signOutFailed)).toBeNull();
  });

  it("a sign-in-required that arrives after Sign out shows the signed-out page and never starts sign-in", async () => {
    let release: (() => void) | undefined;
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) => {
        if (url === "/auth/logout")
          return Promise.resolve(new Response(JSON.stringify({ next: "/" }), { status: 200 }));
        // Sent before the logout and answered after it, when the server no longer knows the cookie.
        if (url === "/api/v1/held")
          return new Promise<Response>((resolve) => {
            release = () =>
              resolve(new Response(JSON.stringify({ error: "sign-in-required", login }), { status: 401 }));
          });
        const body = (routes as Record<string, unknown>)[url] ?? {};
        return Promise.resolve(new Response(JSON.stringify(body), { status: 200 }));
      }),
    );
    const view = mount();
    const late = apiFetch("/api/v1/held").catch(() => {});
    await vi.waitFor(() => expect(release).toBeDefined());
    await signOut();
    await waitFor(() => expect(nav.navigate).toHaveBeenCalledWith("/"));
    release?.();
    await late;
    expect(await screen.findByRole("heading", { name: authCopy.endedTitle })).toBeInTheDocument();
    view.unmount();
    expect(sessionStorage.getItem("snellius.signedOut")).toBe("1");
    expect(sessionStorage.getItem("snellius.hadSession")).toBeNull();
    // The page logout leads to hears sign-in-required too, and still does not redirect.
    resetAuthForTests();
    handleUnauthorized("sign-in-required", login);
    expect(authState.get()).toEqual({ kind: "ended", login });
    expect(nav.navigate).not.toHaveBeenCalledWith(login);
  });
});

describe("sign-in redirect loop", () => {
  it("a second redirect within 10 s shows Sign-in failed instead of navigating", async () => {
    sessionStorage.setItem("snellius.signinRedirectAt", String(Date.now() - 2000));
    stubApi({
      ...routes,
      "/api/v1/meta": [401, { error: "sign-in-required", login }],
      "/api/v1/installs": [401, { error: "sign-in-required", login }],
      "/auth/me": [401, { error: "sign-in-required", login }],
    });
    mount();
    expect(await screen.findByRole("heading", { name: authCopy.failedTitle })).toBeInTheDocument();
    expect(nav.navigate).not.toHaveBeenCalled();
  });

  it.each(["/\t/evil.com", "/\n/evil.com"])("a login URL of %j is not followed", (u) => {
    handleUnauthorized("sign-in-required", u);
    expect(nav.navigate).not.toHaveBeenCalled();
  });

  it("a redirect after 10 s goes ahead", async () => {
    stubApi(routes);
    sessionStorage.setItem("snellius.signinRedirectAt", String(Date.now() - 11_000));
    handleUnauthorized("sign-in-required", login);
    await vi.waitFor(() => expect(nav.navigate).toHaveBeenCalledWith(login));
  });

  it("works when storage throws", async () => {
    stubApi(routes);
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    handleUnauthorized("sign-in-required", login);
    await vi.waitFor(() => expect(nav.navigate).toHaveBeenCalledWith(login));
  });
});

it("concurrent requests after the session ends show the signed-out page without the old username, and never navigate", async () => {
  // Like the server, every request with the ended cookie hears session-ended.
  let ended = false;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) => {
      if (ended && url !== "/auth/status")
        return new Response(JSON.stringify({ error: "session-ended", login }), { status: 401 });
      return new Response(JSON.stringify((routes as Record<string, unknown>)[url] ?? {}), { status: 200 });
    }),
  );
  mount();
  expect(await screen.findByRole("button", { name: me.username })).toBeInTheDocument();
  ended = true;
  await Promise.all(["/api/v1/installs", "/api/v1/meta", "/auth/me"].map((p) => apiFetch(p).catch(() => {})));
  expect(await screen.findByRole("heading", { name: authCopy.endedTitle })).toBeInTheDocument();
  expect(screen.queryByText(me.username)).toBeNull();
  expect(nav.navigate).not.toHaveBeenCalled();
});

describe("identity revalidation", () => {
  // /auth/me answers with whatever groups the server holds now.
  const live = () => {
    const server = { groups: ["g1"] };
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string) => {
        const body = url === "/auth/me" ? { ...me, groups: server.groups } : (routes as Record<string, unknown>)[url];
        return new Response(JSON.stringify(body ?? {}), { status: body ? 200 : 404 });
      }),
    );
    return server;
  };
  const meCalls = () => vi.mocked(fetch).mock.calls.filter(([u]) => u === "/auth/me").length;
  const setVisibility = (v: DocumentVisibilityState) =>
    Object.defineProperty(document, "visibilityState", { configurable: true, get: () => v });
  afterEach(() => setVisibility("visible"));

  it("Account details shows the groups the server holds when it opens", async () => {
    const server = live();
    mount();
    expect(await screen.findByRole("button", { name: me.username })).toBeInTheDocument();
    server.groups = ["g2"];
    const { dialog } = await openDetails();
    expect(await within(dialog).findByText("g2")).toBeInTheDocument();
    expect(within(dialog).queryByText("g1")).toBeNull();
  });

  it("window focus revalidates while visible, never while hidden", async () => {
    live();
    // No 5 s focus throttle after mount, so the first focus counts.
    render(
      <SWRConfig value={{ ...testSWR(), focusThrottleInterval: 0 }}>
        <Shell pages={pages} />
      </SWRConfig>,
    );
    expect(await screen.findByRole("button", { name: me.username })).toBeInTheDocument();
    const before = meCalls();
    setVisibility("hidden");
    await act(async () => {
      window.dispatchEvent(new Event("focus"));
    });
    expect(meCalls()).toBe(before);
    setVisibility("visible");
    await act(async () => {
      window.dispatchEvent(new Event("focus"));
    });
    await waitFor(() => expect(meCalls()).toBe(before + 1));
  });
});

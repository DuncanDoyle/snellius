import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ApiError, apiFetch } from "../api/client";
import { retryEveryPoll } from "../api/hooks";
import { authCopy } from "../copy/auth";
import { renderWithTheme } from "../test/render";
import { authState, resetAuthForTests } from "./authState";
import * as nav from "./navigate";
import { SessionEndedPage } from "./SignInPages";

const login = "/auth/login?return=%2Fi%2Fa";
const reply = (error: string) => vi.fn(async () => new Response(JSON.stringify({ error, login }), { status: 401 }));

beforeEach(() => {
  sessionStorage.clear();
  resetAuthForTests();
  vi.spyOn(nav, "navigate").mockImplementation(() => {});
});
afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

it("sign-in-required sends the browser to the login URL, once per page load", async () => {
  vi.stubGlobal("fetch", reply("sign-in-required"));
  await expect(apiFetch("/api/v1/installs")).rejects.toBeInstanceOf(ApiError);
  await expect(apiFetch("/api/v1/meta")).rejects.toBeInstanceOf(ApiError);
  await vi.waitFor(() => expect(nav.navigate).toHaveBeenCalledWith(login));
  expect(nav.navigate).toHaveBeenCalledTimes(1);
  expect(authState.get()).toEqual({ kind: "redirecting" });
});

it("does not follow a login URL that leaves this origin", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () => new Response(JSON.stringify({ error: "sign-in-required", login: "//evil.test/x" }), { status: 401 }),
    ),
  );
  await expect(apiFetch("/api/v1/installs")).rejects.toBeInstanceOf(ApiError);
  expect(nav.navigate).not.toHaveBeenCalled();
});

it("session-ended records the ended state and does not redirect", async () => {
  vi.stubGlobal("fetch", reply("session-ended"));
  await expect(apiFetch("/api/v1/installs")).rejects.toBeInstanceOf(ApiError);
  expect(nav.navigate).not.toHaveBeenCalled();
  expect(authState.get()).toEqual({ kind: "ended", login });
});

it("a 503 secure-mode-incomplete stays an ordinary error", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => new Response(JSON.stringify({ error: "secure-mode-incomplete" }), { status: 503 })),
  );
  await expect(apiFetch("/api/v1/installs")).rejects.toMatchObject({ status: 503 });
  expect(authState.get()).toEqual({ kind: "none" });
});

it("retryEveryPoll does not retry a 401", () => {
  vi.useFakeTimers();
  const revalidate = vi.fn();
  retryEveryPoll(new ApiError(401, "sign-in-required"), "k", {} as never, revalidate, {} as never);
  vi.advanceTimersByTime(60_000);
  expect(revalidate).not.toHaveBeenCalled();
  vi.useRealTimers();
});

it("does not redirect from the sign-in failure page", async () => {
  window.history.pushState({}, "", "/signin-failed?reason=invalid");
  vi.stubGlobal("fetch", reply("sign-in-required"));
  await expect(apiFetch("/api/v1/meta")).rejects.toBeInstanceOf(ApiError);
  expect(nav.navigate).not.toHaveBeenCalled();
  window.history.pushState({}, "", "/");
});

describe("401s after the session ends (secure mode)", () => {
  // Answers like the server, decided when the request arrives: 200 while the session lives, then session-ended
  // for every request with the cookie, or sign-in-required once the notice is evicted. Held answers wait for release.
  function backend() {
    const server = { ended: false, evicted: false, hold: false };
    const held: (() => void)[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) => {
        if (url === "/auth/status")
          return Promise.resolve(new Response(JSON.stringify({ mode: "secure", signIn: "ready" })));
        const error = server.evicted ? "sign-in-required" : "session-ended";
        const res = server.ended
          ? new Response(JSON.stringify({ error, login }), { status: 401 })
          : new Response("{}", { status: 200 });
        if (!server.hold) return Promise.resolve(res);
        return new Promise<Response>((resolve) => held.push(() => resolve(res)));
      }),
    );
    return { server, held };
  }
  const flush = () => new Promise((r) => setTimeout(r, 0));
  const rejects = (path: string) => expect(apiFetch(path)).rejects.toBeInstanceOf(ApiError);
  // A protected request succeeds, so this tab has had a session.
  const haveSession = () => apiFetch("/api/v1/meta");

  beforeEach(() => {
    document.documentElement.dataset.authMode = "secure";
  });
  afterEach(() => {
    delete document.documentElement.dataset.authMode;
  });

  it("concurrent requests all hear session-ended: signed out, nothing navigates", async () => {
    const { server, held } = backend();
    await haveSession();
    server.ended = true;
    server.hold = true;
    const all = ["/api/v1/installs", "/api/v1/meta", "/auth/me", "/api/v1/routes"].map(rejects);
    await vi.waitFor(() => expect(held).toHaveLength(4));
    for (const release of held.reverse()) release();
    await Promise.all(all);
    await flush();
    expect(authState.get()).toEqual({ kind: "ended", login });
    expect(nav.navigate).not.toHaveBeenCalled();
  });

  it("reversed answers across notice eviction: signed out, nothing navigates", async () => {
    const { server, held } = backend();
    await haveSession();
    server.ended = true;
    server.hold = true;
    const a = rejects("/api/v1/installs");
    await vi.waitFor(() => expect(held).toHaveLength(1));
    server.evicted = true;
    const b = rejects("/api/v1/meta");
    await vi.waitFor(() => expect(held).toHaveLength(2));
    // B's sign-in-required is delivered first, then A's session-ended.
    held[1]?.();
    await b;
    await flush();
    expect(authState.get()).toEqual({ kind: "ended", login });
    held[0]?.();
    await a;
    await flush();
    expect(authState.get()).toEqual({ kind: "ended", login });
    expect(nav.navigate).not.toHaveBeenCalled();
  });

  it("a tab that never had a session: a held session-ended still wins over an earlier sign-in-required", async () => {
    const { server, held } = backend();
    server.ended = true;
    server.hold = true;
    const a = rejects("/api/v1/installs");
    await vi.waitFor(() => expect(held).toHaveLength(1));
    server.evicted = true;
    const b = rejects("/api/v1/meta");
    await vi.waitFor(() => expect(held).toHaveLength(2));
    held[1]?.();
    await b;
    held[0]?.();
    await a;
    await flush();
    expect(authState.get()).toEqual({ kind: "ended", login });
    expect(nav.navigate).not.toHaveBeenCalled();
  });

  it("a tab that had a session hears only sign-in-required: signed out, nothing navigates", async () => {
    const { server } = backend();
    await haveSession();
    server.ended = true;
    server.evicted = true;
    await Promise.all([rejects("/api/v1/installs"), rejects("/auth/me")]);
    await flush();
    expect(authState.get()).toEqual({ kind: "ended", login });
    expect(nav.navigate).not.toHaveBeenCalled();
  });

  it("a tab that never had a session is redirected once", async () => {
    const { server } = backend();
    server.ended = true;
    server.evicted = true;
    await Promise.all([rejects("/api/v1/installs"), rejects("/api/v1/meta")]);
    await vi.waitFor(() => expect(nav.navigate).toHaveBeenCalledWith(login));
    expect(nav.navigate).toHaveBeenCalledTimes(1);
  });

  it("the flag survives a reload and is cleared by Sign in again", async () => {
    const { server } = backend();
    await haveSession();
    expect(sessionStorage.getItem("snellius.hadSession")).toBe("1");
    resetAuthForTests(); // a reload: storage stays
    server.ended = true;
    server.evicted = true;
    await rejects("/api/v1/installs");
    await flush();
    expect(authState.get()).toEqual({ kind: "ended", login });
    expect(nav.navigate).not.toHaveBeenCalled();
    renderWithTheme(<SessionEndedPage login={login} />);
    await userEvent.click(screen.getByRole("button", { name: authCopy.signInAgain }));
    expect(nav.navigate).toHaveBeenCalledWith(login);
    expect(sessionStorage.getItem("snellius.hadSession")).toBeNull();
    resetAuthForTests(); // a reload without the flag: sign-in-required redirects again
    await rejects("/api/v1/installs");
    await vi.waitFor(() => expect(nav.navigate).toHaveBeenCalledTimes(2));
    expect(authState.get()).toEqual({ kind: "redirecting" });
  });

  it("when storage throws, the flag lives for the page load only", async () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    const { server } = backend();
    await haveSession();
    server.ended = true;
    server.evicted = true;
    await rejects("/api/v1/installs");
    await flush();
    expect(authState.get()).toEqual({ kind: "ended", login });
    expect(nav.navigate).not.toHaveBeenCalled();
    resetAuthForTests();
    await rejects("/api/v1/installs");
    await vi.waitFor(() => expect(nav.navigate).toHaveBeenCalledWith(login));
  });
});

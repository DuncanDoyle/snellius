import { ApiError, apiFetch } from "../api/client";
import { retryEveryPoll } from "../api/hooks";
import { authState, resetAuthForTests } from "./authState";
import * as nav from "./navigate";

const login = "/auth/login?return=%2Fi%2Fa";
const reply = (error: string) => vi.fn(async () => new Response(JSON.stringify({ error, login }), { status: 401 }));

beforeEach(() => {
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

describe("concurrent 401s after the session ends (secure mode)", () => {
  // The backend answers session-ended once, then sign-in-required; each answer arrives when its release runs.
  function oneShotBackend() {
    let served = 0;
    const releases: (() => void)[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) => {
        if (url === "/auth/status")
          return Promise.resolve(new Response(JSON.stringify({ mode: "secure", signIn: "ready" })));
        const error = served++ === 0 ? "session-ended" : "sign-in-required";
        return new Promise<Response>((resolve) =>
          releases.push(() => resolve(new Response(JSON.stringify({ error, login }), { status: 401 }))),
        );
      }),
    );
    return releases;
  }
  const flush = () => new Promise((r) => setTimeout(r, 0));

  beforeEach(() => {
    document.documentElement.dataset.authMode = "secure";
    sessionStorage.clear();
  });
  afterEach(() => {
    delete document.documentElement.dataset.authMode;
  });

  it("session-ended first: a later sign-in-required does not navigate", async () => {
    const releases = oneShotBackend();
    const a = expect(apiFetch("/api/v1/installs")).rejects.toBeInstanceOf(ApiError);
    const b = expect(apiFetch("/api/v1/meta")).rejects.toBeInstanceOf(ApiError);
    await vi.waitFor(() => expect(releases).toHaveLength(2));
    releases[0]?.();
    await a;
    releases[1]?.();
    await b;
    await flush();
    expect(authState.get()).toEqual({ kind: "ended", login });
    expect(nav.navigate).not.toHaveBeenCalled();
  });

  it("sign-in-required first: the session-ended that follows wins and nothing navigates", async () => {
    const releases = oneShotBackend();
    const ended = expect(apiFetch("/api/v1/installs")).rejects.toBeInstanceOf(ApiError);
    const required = expect(apiFetch("/api/v1/meta")).rejects.toBeInstanceOf(ApiError);
    await vi.waitFor(() => expect(releases).toHaveLength(2));
    releases[1]?.();
    await required;
    releases[0]?.();
    await ended;
    await flush();
    expect(authState.get()).toEqual({ kind: "ended", login });
    expect(nav.navigate).not.toHaveBeenCalled();
  });

  it("sign-in-required alone still navigates once", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string) =>
        url === "/auth/status"
          ? new Response(JSON.stringify({ mode: "secure", signIn: "ready" }))
          : new Response(JSON.stringify({ error: "sign-in-required", login }), { status: 401 }),
      ),
    );
    await expect(apiFetch("/api/v1/meta")).rejects.toBeInstanceOf(ApiError);
    await vi.waitFor(() => expect(nav.navigate).toHaveBeenCalledWith(login));
    expect(nav.navigate).toHaveBeenCalledTimes(1);
  });
});

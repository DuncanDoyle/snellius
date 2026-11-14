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
  expect(nav.navigate).toHaveBeenCalledTimes(1);
  expect(nav.navigate).toHaveBeenCalledWith(login);
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

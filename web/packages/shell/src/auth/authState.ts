import { useSyncExternalStore } from "react";
import { isSecureMode } from "./mode";
import { isLocalPath, navigate } from "./navigate";

/** What the server last told the page about the sign-in: nothing, a redirect under way, or a session that ended. */
export type AuthState =
  | { kind: "none" }
  | { kind: "redirecting" }
  | { kind: "ended"; login: string }
  | { kind: "loop" };

export type SignInState = "ready" | "not-configured" | "unavailable";

export interface StatusResponse {
  mode: "local" | "secure";
  signIn?: SignInState;
}

/** The SPA route the sign-in callback redirects to on failure. */
export const SIGNIN_FAILED_PATH = "/signin-failed";

let state: AuthState = { kind: "none" };
let redirected = false;
let status: Promise<StatusResponse> | undefined;
let inFlight = 0;
let drained: (() => void)[] = [];
const listeners = new Set<() => void>();

function set(next: AuthState) {
  state = next;
  for (const l of listeners) l();
}

export const authState = { get: () => state };

/** The current sign-in state, for components. */
export function useAuthState(): AuthState {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => state,
  );
}

/** GET /auth/status once per page load; protected requests and the sign-in redirect wait for it. */
export function signInStatus(): Promise<StatusResponse> {
  status ??= fetch("/auth/status", { headers: { Accept: "application/json" } }).then(async (res) => {
    if (!res.ok) throw new Error(res.statusText || `status ${res.status}`);
    return (await res.json()) as StatusResponse;
  });
  return status;
}

/** Counts one request whose 401 could change the sign-in state; call the returned function when it settles. */
export function trackAuthRequest(): () => void {
  inFlight++;
  return () => {
    inFlight--;
    if (inFlight > 0) return;
    for (const d of drained.splice(0)) d();
  };
}

/** The longest a sign-in redirect waits for concurrent requests, any of which may carry the one session-ended. */
const SETTLE_MAX_MS = 1000;

function requestsSettled(): Promise<void> {
  return new Promise((resolve) => {
    // A tick first, so answers already delivered are handled before the check.
    setTimeout(() => {
      if (inFlight === 0) return resolve();
      drained.push(resolve);
      setTimeout(resolve, SETTLE_MAX_MS);
    }, 0);
  });
}

const LAST_REDIRECT_KEY = "snellius.signinRedirectAt";
const LOOP_WINDOW_MS = 10_000;

/** True when the previous sign-in redirect was under 10 s ago (it survives page loads). */
function redirectedRecently(): boolean {
  try {
    const last = Number(sessionStorage.getItem(LAST_REDIRECT_KEY));
    return last > 0 && Date.now() - last < LOOP_WINDOW_MS;
  } catch {
    // Storage can throw (blocked, private window); the per-load guard still applies.
    return false;
  }
}

function recordRedirect() {
  try {
    sessionStorage.setItem(LAST_REDIRECT_KEY, String(Date.now()));
  } catch {
    // See redirectedRecently.
  }
}

/** Sign-in can be followed in local mode, when it is ready, or when /auth/status itself fails (the login route then explains). */
async function signInReady(): Promise<boolean> {
  if (!isSecureMode()) return true;
  try {
    return (await signInStatus()).signIn === "ready";
  } catch {
    return true;
  }
}

// A function, so TypeScript does not narrow state across the awaits below.
const ended = () => state.kind === "ended";

async function redirect(login: string) {
  if (!(await signInReady()) || ended()) return;
  if (redirectedRecently()) return set({ kind: "loop" });
  set({ kind: "redirecting" });
  await requestsSettled();
  // An ended session is the server's word; it stays until the user signs in again.
  if (ended()) return;
  recordRedirect();
  navigate(login);
}

/** Applies a 401 answer: redirect to sign-in (once per page load) or record that the session ended, which then wins. */
export function handleUnauthorized(error: string, login: string | undefined): void {
  if (!login || !isLocalPath(login)) return;
  // The failure page stays put; sending the browser back to sign-in would loop.
  if (window.location.pathname === SIGNIN_FAILED_PATH) return;
  if (error === "session-ended") {
    set({ kind: "ended", login });
  } else if (error === "sign-in-required" && !redirected && state.kind !== "ended") {
    redirected = true;
    void redirect(login);
  }
}

/** Clears the state between tests. */
export function resetAuthForTests(): void {
  redirected = false;
  status = undefined;
  inFlight = 0;
  drained = [];
  set({ kind: "none" });
}

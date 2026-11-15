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

/** The longest a sign-in redirect waits for concurrent requests, any of which may carry a session-ended. */
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

const HAD_SESSION_KEY = "snellius.hadSession";
const SIGNED_OUT_KEY = "snellius.signedOut";
// The flags for this page load; they stand in for storage when it throws.
let hadSession = false;
let signedOut = false;

function writeFlag(key: string, on: boolean) {
  try {
    if (on) sessionStorage.setItem(key, "1");
    else sessionStorage.removeItem(key);
  } catch {
    // See redirectedRecently; the in-memory flag still applies.
  }
}

function readFlag(key: string): boolean {
  try {
    return sessionStorage.getItem(key) === "1";
  } catch {
    return false;
  }
}

/** Records that this tab had a session: a protected request succeeded. Kept across reloads. */
export function markHadSession(): void {
  if (hadSession) return;
  hadSession = true;
  writeFlag(HAD_SESSION_KEY, true);
}

/** Records a sign-out in place of the session, so a late sign-in-required shows the signed-out page. */
export function markSignedOut(): void {
  hadSession = false;
  signedOut = true;
  writeFlag(HAD_SESSION_KEY, false);
  writeFlag(SIGNED_OUT_KEY, true);
}

/** Clears both flags; "Sign in again" calls it before it leaves for sign-in. */
export function clearSessionFlags(): void {
  hadSession = false;
  signedOut = false;
  writeFlag(HAD_SESSION_KEY, false);
  writeFlag(SIGNED_OUT_KEY, false);
}

/** A tab that had a session, or signed out, never redirects to sign-in by itself. */
function knownSession(): boolean {
  return hadSession || signedOut || readFlag(HAD_SESSION_KEY) || readFlag(SIGNED_OUT_KEY);
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

/**
 * Applies a 401 answer: record that the session ended, which then wins, or redirect to sign-in (once per page load).
 * In a tab that had a session, sign-in-required also shows the signed-out page: answers can arrive in any order.
 */
export function handleUnauthorized(error: string, login: string | undefined): void {
  if (!login || !isLocalPath(login)) return;
  // The failure page stays put; sending the browser back to sign-in would loop.
  if (window.location.pathname === SIGNIN_FAILED_PATH) return;
  if (error === "session-ended" || (error === "sign-in-required" && knownSession())) {
    set({ kind: "ended", login });
  } else if (error === "sign-in-required" && !redirected && state.kind !== "ended") {
    redirected = true;
    void redirect(login);
  }
}

/** Clears the page-load state between tests; sessionStorage stays, so it also stands in for a reload. */
export function resetAuthForTests(): void {
  redirected = false;
  hadSession = false;
  signedOut = false;
  status = undefined;
  inFlight = 0;
  drained = [];
  set({ kind: "none" });
}

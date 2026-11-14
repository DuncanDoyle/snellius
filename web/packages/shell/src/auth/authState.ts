import { useSyncExternalStore } from "react";
import { navigate } from "./navigate";

/** What the server last told the page about the sign-in: nothing, a redirect under way, or a session that ended. */
export type AuthState =
  | { kind: "none" }
  | { kind: "redirecting" }
  | { kind: "ended"; login: string }
  | { kind: "loop" };

/** The SPA route the sign-in callback redirects to on failure. */
export const SIGNIN_FAILED_PATH = "/signin-failed";

let state: AuthState = { kind: "none" };
let redirected = false;
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

/** Only a path on this origin is followed: "/x", never "//host" or "/\host". */
const isLocalPath = (u: string) => u.startsWith("/") && !u.startsWith("//") && !u.startsWith("/\\");

const LAST_REDIRECT_KEY = "snellius.signinRedirectAt";
const LOOP_WINDOW_MS = 10_000;

/** True when the previous sign-in redirect was under 10 s ago (it survives page loads); records this one. */
function redirectedRecently(): boolean {
  try {
    const last = Number(sessionStorage.getItem(LAST_REDIRECT_KEY));
    const now = Date.now();
    sessionStorage.setItem(LAST_REDIRECT_KEY, String(now));
    return last > 0 && now - last < LOOP_WINDOW_MS;
  } catch {
    // Storage can throw (blocked, private window); the per-load guard still applies.
    return false;
  }
}

/** Applies a 401 answer: redirect to sign-in (once per page load) or record that the session ended. */
export function handleUnauthorized(error: string, login: string | undefined): void {
  if (!login || !isLocalPath(login)) return;
  // The failure page stays put; sending the browser back to sign-in would loop.
  if (window.location.pathname === SIGNIN_FAILED_PATH) return;
  if (error === "session-ended") {
    set({ kind: "ended", login });
  } else if (error === "sign-in-required" && !redirected) {
    redirected = true;
    if (redirectedRecently()) {
      set({ kind: "loop" });
      return;
    }
    set({ kind: "redirecting" });
    navigate(login);
  }
}

/** Clears the state between tests. */
export function resetAuthForTests(): void {
  redirected = false;
  set({ kind: "none" });
}

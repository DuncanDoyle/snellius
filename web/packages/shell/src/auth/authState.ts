import { useSyncExternalStore } from "react";
import { navigate } from "./navigate";

/** What the server last told the page about the sign-in: nothing, a redirect under way, or a session that ended. */
export type AuthState = { kind: "none" } | { kind: "redirecting" } | { kind: "ended"; login: string };

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

/** Applies a 401 answer: redirect to sign-in (once per page load) or record that the session ended. */
export function handleUnauthorized(error: string, login: string | undefined): void {
  if (!login || !isLocalPath(login)) return;
  // The failure page stays put; sending the browser back to sign-in would loop.
  if (window.location.pathname === SIGNIN_FAILED_PATH) return;
  if (error === "session-ended") {
    set({ kind: "ended", login });
  } else if (error === "sign-in-required" && !redirected) {
    redirected = true;
    set({ kind: "redirecting" });
    navigate(login);
  }
}

/** Clears the state between tests. */
export function resetAuthForTests(): void {
  redirected = false;
  set({ kind: "none" });
}

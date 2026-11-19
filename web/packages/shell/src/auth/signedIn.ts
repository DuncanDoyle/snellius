/** The fragment the server adds to the return path after a fresh sign-in. */
export const SIGNED_IN_HASH = "#signed-in";

let taken: boolean | undefined;

/** True once per page load when the URL carries the signed-in marker; removes it before the router reads the URL. */
export function takeSignedInMarker(): boolean {
  if (taken !== undefined) return taken;
  taken = window.location.hash === SIGNED_IN_HASH;
  if (taken) {
    const { pathname, search } = window.location;
    window.history.replaceState(window.history.state, "", pathname + search);
  }
  return taken;
}

/** Forgets this page load's answer, so a test can stand in for the next load. */
export function resetSignedInMarkerForTests(): void {
  taken = undefined;
}

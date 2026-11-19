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

/** The sessionStorage key that marks this tab as having seen the local-mode intro. */
export const ARRIVED_KEY = "snellius.arrived";

let arrived: boolean | undefined;

/**
 * True once per tab, on its first page load: the arrival a local-mode user has instead of a sign-in.
 * Storage that cannot be read or written counts as already arrived, so the intro never repeats.
 */
export function takeFirstArrival(): boolean {
  if (arrived !== undefined) return arrived;
  try {
    arrived = window.sessionStorage.getItem(ARRIVED_KEY) === null;
    window.sessionStorage.setItem(ARRIVED_KEY, "1");
  } catch {
    arrived = false;
  }
  return arrived;
}

/** Forgets this page load's answers, so a test can stand in for the next load. */
export function resetSignedInMarkerForTests(): void {
  taken = undefined;
  arrived = undefined;
}

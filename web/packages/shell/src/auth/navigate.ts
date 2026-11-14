/** Sends the browser to url; a seam so tests can observe the navigation. */
export function navigate(url: string): void {
  window.location.assign(url);
}

/** The logout `next` URL if it is a path on this origin or an http(s) URL; "/" otherwise. */
export function safeNext(next: string | undefined): string {
  if (!next) return "/";
  if (next.startsWith("/")) return next.startsWith("//") || next.startsWith("/\\") ? "/" : next;
  try {
    const { protocol } = new URL(next);
    return protocol === "https:" || protocol === "http:" ? next : "/";
  } catch {
    return "/";
  }
}

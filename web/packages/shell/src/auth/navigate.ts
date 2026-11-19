/** Sends the browser to url; a seam so tests can observe the navigation. */
export function navigate(url: string): void {
  window.location.assign(url);
}

// biome-ignore lint/suspicious/noControlCharactersInRegex: matching control characters is the point
const UNSAFE = /[\u0000-\u001F\u007F\s]/;

/** True for a path on this origin: no control characters or whitespace (the URL parser would strip them), no "//" or "/\". */
export function isLocalPath(value: string): boolean {
  if (UNSAFE.test(value) || !value.startsWith("/") || value.startsWith("//") || value.startsWith("/\\")) return false;
  try {
    return new URL(value, window.location.origin).origin === window.location.origin;
  } catch {
    return false;
  }
}

/** The logout `next` URL if it is a path on this origin or an http(s) URL; "/" otherwise. */
export function safeNext(next: string | undefined): string {
  if (!next || UNSAFE.test(next)) return "/";
  if (next.startsWith("/")) return isLocalPath(next) ? next : "/";
  try {
    const { protocol } = new URL(next);
    return protocol === "https:" || protocol === "http:" ? next : "/";
  } catch {
    return "/";
  }
}

/** Sends the browser to url; a seam so tests can observe the navigation. */
export function navigate(url: string): void {
  window.location.assign(url);
}

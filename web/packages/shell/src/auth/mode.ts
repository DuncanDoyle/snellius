/** True when index.html stamped the page secure; known before any request, so nothing shifts. */
export const isSecureMode = () => document.documentElement.dataset.authMode === "secure";

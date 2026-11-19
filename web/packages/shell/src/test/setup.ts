import "@testing-library/jest-dom/vitest";
import { ARRIVED_KEY } from "../auth/signedIn";

// Every tab has already arrived, so the local-mode intro stays out of tests that are not about it.
// Tests that run in node have no window, and no intro either.
beforeEach(() => {
  if (typeof window !== "undefined") window.sessionStorage.setItem(ARRIVED_KEY, "1");
});

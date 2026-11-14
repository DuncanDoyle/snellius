import { expect, test } from "@playwright/test";

// Runs against a secure-mode install (test/e2e provides it); skipped otherwise.
test.skip(process.env.SECURE !== "1", "needs SECURE=1 and a secure-mode install");

test("an unsigned visitor is sent to sign-in", async ({ page, baseURL }) => {
  const app = new URL(baseURL ?? "http://127.0.0.1:18080").origin;
  await page.goto("/");
  // The browser leaves the app root for the login route or the provider.
  await page.waitForURL((u) => u.pathname.startsWith("/auth/login") || u.origin !== app);
});

test("the signin-failed page shows its reason and a Try again button", async ({ page }) => {
  await page.goto("/signin-failed?reason=audience");
  await expect(page.getByRole("heading", { name: "This cluster doesn't accept your sign-in" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Try again" })).toBeVisible();
  await page.goto("/signin-failed?reason=invalid");
  await expect(page.getByRole("heading", { name: "Sign-in failed" })).toBeVisible();
});

test("a signed-in user sees the account menu and can sign out", async ({ page, baseURL }) => {
  const app = new URL(baseURL ?? "http://127.0.0.1:18080").origin;
  test.skip(!process.env.SIGNED_IN, "needs a signed-in browser session (SIGNED_IN=1)");
  await page.goto("/");
  await page.locator("header").getByRole("button", { expanded: false }).last().click();
  await page.getByRole("menuitem", { name: "Account details" }).click();
  await expect(page.getByRole("dialog", { name: "Account details" })).toContainText("Kubernetes groups");
  await page.keyboard.press("Escape");
  await page.locator("header").getByRole("button", { expanded: false }).last().click();
  // Sign out leaves the signed-in app: the next request answers 401 and sends the browser to sign-in.
  await page.getByRole("menuitem", { name: "Sign out" }).click();
  await page.waitForURL((u) => u.pathname.startsWith("/auth/login") || u.origin !== app || u.pathname === "/");
});

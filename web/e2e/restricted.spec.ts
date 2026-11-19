import { expect, type Page, test } from "@playwright/test";
import { columnCopy, listCopy } from "../packages/shell/src/copy/explorer";
import { stateLabel } from "../packages/shell/src/health/state";

// Runs against a secure-mode install (test/e2e/setup.sh): alice may read HTTPRoutes in shop, bob has no RBAC.
test.skip(process.env.SECURE !== "1", "needs SECURE=1 and a secure-mode install");

const routes = "/i/kgw-a.kgateway/routes";

/** Signs in through the test issuer's user picker and waits for the app. */
async function signInAs(page: Page, user: string, baseURL: string | undefined) {
  const app = new URL(baseURL ?? "http://127.0.0.1:18080").origin;
  await page.goto("/");
  await page.getByRole("button", { name: user, exact: true }).click();
  await page.waitForURL((u) => u.origin === app && !u.pathname.startsWith("/auth/"));
}

test("alice's route list shows the routes she can read, with both signals", async ({ page, baseURL }) => {
  await signInAs(page, "alice", baseURL);
  await page.goto(routes);
  const table = page.getByRole("table", { name: listCopy.routes.title });
  await expect(table).toHaveAttribute("aria-busy", "false");
  await expect(table.getByRole("columnheader", { name: columnCopy.configuration })).toBeVisible();
  await expect(table.getByRole("columnheader", { name: columnCopy.trafficPath })).toBeVisible();
  const rows = table.locator("tbody > tr[data-row]");
  const name = (part: string) => `td[data-col="name"] [data-part="${part}"]`;
  // Every route-name cell is a shop route: nothing from a namespace she cannot read.
  const count = await rows.count();
  expect(count).toBeGreaterThan(0);
  await expect(rows.locator(name("namespace"))).toHaveText(Array<string>(count).fill("shop/"));
  const good = rows.filter({ has: page.locator(name("name"), { hasText: /^good$/ }) });
  await expect(good).toHaveCount(1);
  // Both signals render a state on her row, not only their headers.
  const states = (["healthy", "warning", "error", "unknown"] as const).map(stateLabel);
  const state = new RegExp(`^(${states.join("|")})$`);
  await expect(good.locator('td[data-col="config"]')).toHaveText(state);
  await expect(good.locator('td[data-col="path"]')).toHaveText(state);
});

test("bob's route list is empty and his install shows its details hidden", async ({ page, baseURL }) => {
  await signInAs(page, "bob", baseURL);
  await page.goto(routes);
  const table = page.getByRole("table", { name: listCopy.routes.title });
  await expect(table).toHaveAttribute("aria-busy", "false");
  await expect(table.getByTestId("empty-row")).toBeVisible();
  await expect(table.locator("tbody > tr[data-row]")).toHaveCount(0);
  await expect(page.getByText("shop/good")).toHaveCount(0);
  await expect(page.getByTestId("details-hidden").first()).toBeVisible();
});

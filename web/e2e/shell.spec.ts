import { expect, test } from "@playwright/test";
import { shellCopy } from "../packages/shell/src/copy/shell";

// The e2e server runs without sign-in, so every page carries the local-mode notice.
test("local mode shows the banner on the landing page and an install page", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByTestId("mode-banner")).toHaveText(shellCopy.localMode);
  await page.goto("/i/kgw-a.kgateway");
  await expect(page.getByTestId("mode-banner")).toHaveText(shellCopy.localMode);
});

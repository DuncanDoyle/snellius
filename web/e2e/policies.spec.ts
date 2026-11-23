import { expect, type Locator, type Page, test } from "@playwright/test";
import type { PoliciesResponse, PolicyDetail, PolicyRow } from "../packages/shell/src/api/types";
import { navCopy } from "../packages/shell/src/copy/explorer";
import { policiesCopy, policyDetailCopy } from "../packages/shell/src/copy/policies";
import { policyPath } from "../packages/shell/src/explore/objectPath";
import { featureName, targetText } from "../packages/shell/src/pages/policies/policyText";

// The Policies list and policy detail (Phase 8 sub-phase 8.2). The checks read the API response the page renders,
// so they hold for any fixture with at least one policy. E2E_INSTALL picks the install (default: the e2e matrix's).
const id = process.env.E2E_INSTALL ?? "kgw-a.kgateway";
const install = `/i/${id}`;
const api = `/api/v1/installs/${id}`;

async function get<T>(page: Page, path: string): Promise<T> {
  const res = await page.request.get(`${api}/${path}`);
  expect(res.ok(), `GET ${path}`).toBeTruthy();
  return (await res.json()) as T;
}

/** The table's data rows once it is no longer loading. */
async function dataRows(page: Page): Promise<Locator> {
  const table = page.getByRole("table", { name: policiesCopy.title });
  await expect(table).toHaveAttribute("aria-busy", "false");
  return table.locator("tbody > tr[data-row]");
}

const refText = (r: PolicyRow) => `${r.ref.namespace}/${r.ref.name}`;

test("Policies opens from the install nav, lists the API's rows by namespace and name, and filters", async ({
  page,
}) => {
  const all = await get<PoliciesResponse>(page, "policies");
  test.skip(all.items.length === 0, "the install has no policies");
  await page.goto(install);
  await page.getByRole("navigation", { name: navCopy.label }).getByRole("link", { name: policiesCopy.title }).click();
  await expect(page).toHaveURL(new RegExp(`${install}/policies$`));
  await expect(page.getByTestId("policies-sort-note")).toHaveText(policiesCopy.sortNote);
  const rows = await dataRows(page);
  await expect(rows).toHaveCount(all.items.length);
  for (const [i, r] of all.items.entries()) {
    const row = rows.nth(i);
    await expect(row.locator('td[data-col="name"]')).toHaveText(refText(r));
    for (const t of r.targets) await expect(row.locator('td[data-col="targets"]')).toContainText(targetText(t));
  }

  // Has problems: the rows the server returns for hasProblems=true.
  await page
    .getByRole("group", { name: policiesCopy.show })
    .getByRole("button", { name: policiesCopy.hasProblems })
    .click();
  await expect(page).toHaveURL(/hasProblems=true/);
  const problems = await get<PoliciesResponse>(page, "policies?hasProblems=true");
  await expect(await dataRows(page)).toHaveCount(problems.items.length);
  await page
    .getByRole("group", { name: policiesCopy.show })
    .getByRole("button", { name: policiesCopy.hasProblems })
    .click();

  // A kind chip narrows to that kind.
  const kind = all.facets.kinds[0];
  if (!kind) throw new Error("no kind facet");
  await page
    .getByRole("group", { name: policiesCopy.kinds })
    .getByRole("button", { name: kind.kind, exact: true })
    .click();
  await expect(page).toHaveURL(/kind=/);
  await expect(await dataRows(page)).toHaveCount(Math.min(kind.policies, 50));
});

test("a policy row opens its detail: targets, status per ancestor as written, configuration and YAML", async ({
  page,
}) => {
  const all = await get<PoliciesResponse>(page, "policies");
  const first = all.items[0];
  test.skip(!first, "the install has no policies");
  if (!first) return;
  await page.goto(`${install}/policies`);
  await (await dataRows(page)).first().locator('td[data-col="kind"]').click();
  const path = policyPath(id, first.ref);
  await expect(page).toHaveURL(new RegExp(`${path.replace(/[.]/g, "\\.")}(\\?|$)`));
  await expect(page.getByRole("heading", { name: policyDetailCopy.summary })).toBeVisible();
  const g = first.ref.group || "-";
  const d = await get<PolicyDetail>(page, `policies/${g}/${first.ref.kind}/${first.ref.namespace}/${first.ref.name}`);
  await expect(page.getByTestId("policy-target")).toHaveCount(d.targets.length);
  await expect(page.getByTestId("policy-ancestor")).toHaveCount(d.ancestors.length);
  await expect(page.getByTestId("policy-feature")).toHaveCount(d.features.length);
  for (const f of d.features) await expect(page.getByRole("heading", { name: featureName(f) })).toBeVisible();
  // Conditions read as the controller wrote them.
  for (const [i, a] of d.ancestors.entries())
    for (const c of a.conditions) {
      const shown = page.getByTestId("policy-ancestor").nth(i).getByTestId(`condition-${c.type}`);
      await expect(shown).toContainText(`${c.type}: ${c.status}`);
      if (c.reason) await expect(shown).toContainText(c.reason);
    }
  // Every value carries a source tag.
  await expect(page.getByTestId("source-tag").first()).toBeVisible();

  await page.getByRole("tab", { name: "YAML" }).click();
  await expect(page).toHaveURL(/tab=yaml/);
  await expect(page.getByTestId("yaml")).toContainText(`name: ${first.ref.name}`);
});

import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes, useLocation } from "react-router-dom";
import gatewayGolden from "../../../../../../pkg/api/testdata/golden/gateway-detail-policies.json";
import listGolden from "../../../../../../pkg/api/testdata/golden/policies.json";
import detailGolden from "../../../../../../pkg/api/testdata/golden/policy-detail.json";
import routeGolden from "../../../../../../pkg/api/testdata/golden/route-detail-policies.json";
import type { GatewayDetail, LinkedRef, PoliciesResponse, PolicyDetail, PolicyRow, RouteDetail } from "../../api/types";
import * as copy from "../../copy/policies";
import { renderWithTheme } from "../../test/render";
import { PoliciesPage } from "./PoliciesPage";
import { PolicyPage } from "./PolicyPage";
import { PolicyRelationsSection } from "./PolicyRelationsSection";

const ID = "kgw.kgateway";
const API = `/api/v1/installs/${ID}`;
const now = () => new Date().toISOString();
/**
 * The body with each policy ref's link in the D-136 form, the policy endpoint's path. Goldens written before the API
 * change carry "detail"; this leaves the path form as it is.
 */
function withPolicyLinks<T>(body: T): T {
  const kinds = new Set([
    "TrafficPolicy",
    "ListenerPolicy",
    "BackendConfigPolicy",
    "DirectResponse",
    "BackendTLSPolicy",
  ]);
  const walk = (v: unknown): void => {
    if (Array.isArray(v)) v.forEach(walk);
    else if (v && typeof v === "object") {
      const o = v as Record<string, unknown>;
      if (o.link === "detail" && typeof o.kind === "string" && kinds.has(o.kind))
        o.link = `${API}/policies/${o.group || "-"}/${o.kind}/${o.namespace}/${o.name}`;
      Object.values(o).forEach(walk);
    }
  };
  const out = structuredClone(body);
  walk(out);
  return out;
}
const list = (): PoliciesResponse => ({
  ...withPolicyLinks(listGolden.body as PoliciesResponse),
  collectedAt: now(),
});
const detail = (): PolicyDetail => ({ ...withPolicyLinks(detailGolden.body as PolicyDetail), collectedAt: now() });

/** Stubs fetch by pathname; anything else is a 404. */
function serve(routes: Record<string, (u: URL) => unknown>) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) => {
      const u = new URL(url, "http://ui");
      const f = routes[u.pathname];
      return f ? new Response(JSON.stringify(f(u)), { status: 200 }) : new Response("{}", { status: 404 });
    }),
  );
}
const calls = (suffix: string) =>
  vi
    .mocked(fetch)
    .mock.calls.map((c) => new URL(String(c[0]), "http://ui"))
    .filter((u) => u.pathname.endsWith(suffix));

function Where() {
  const loc = useLocation();
  return <output data-testid="location">{`${loc.pathname}${loc.search}`}</output>;
}

function mount(path: string) {
  return renderWithTheme(
    <>
      <Routes>
        <Route path="/i/:id/policies" element={<PoliciesPage />} />
        <Route path="/i/:id/policies/:group/:kind/:ns/:name" element={<PolicyPage />} />
        <Route path="*" element={null} />
      </Routes>
      <Where />
    </>,
    { path },
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

// region Copy
/** Every string Snellius writes about policies, including the functions' output for sample input. */
function snelliusStrings(): string[] {
  const out: string[] = [];
  const walk = (v: unknown) => {
    if (typeof v === "string") out.push(v);
    else if (Array.isArray(v)) v.forEach(walk);
    else if (v && typeof v === "object") Object.values(v).forEach(walk);
  };
  walk([
    copy.policiesCopy,
    copy.policyColumnCopy,
    copy.checkCopy,
    copy.acceptedCopy.yes,
    copy.acceptedCopy.no,
    copy.acceptedCopy.none,
    copy.acceptedCopy.other("Unknown"),
    copy.attachedCopy,
    copy.sourceCopy,
    copy.sourcePrefix,
    copy.policyDetailCopy,
    copy.notCurrentCopy,
    copy.readingNoteCopy,
    copy.selectorText("HTTPRoute", { app: "cart" }, "rule"),
    copy.refTargetText("HTTPRoute", "cart", "rule"),
    copy.ancestorText({ kind: "Service", namespace: "shop", name: "checkout", port: 80 }),
    copy.relationsCopy,
    copy.relationsCopy.parentNote("route"),
    copy.relationsCopy.selectorLink("shop"),
    copy.relationsCopy.section("http"),
    copy.relationsCopy.port(80),
  ]);
  return out;
}

it("no Snellius-written policy label says effective, applied or active (design §3.2)", () => {
  const strings = snelliusStrings();
  expect(strings.length).toBeGreaterThan(40);
  expect(strings.filter((s) => /\b(effective|applied|active)\b/i.test(s))).toEqual([]);
});

it("the rendered list and detail use none of those words outside controller messages and YAML", async () => {
  // A controller message that uses the words stays verbatim; only Snellius's own text is checked.
  const d = detail();
  const first = d.ancestors[0];
  if (first?.conditions[0]) first.conditions[0].message = "Policy applied and active";
  serve({ [`${API}/policies`]: list, [`${API}/policies/gateway.kgateway.dev/TrafficPolicy/shop/tp`]: () => d });
  const { unmount } = mount(`/i/${ID}/policies`);
  const table = await screen.findByRole("table", { name: copy.policiesCopy.title });
  await waitFor(() => expect(table).toHaveAttribute("aria-busy", "false"));
  expect(document.body.textContent).not.toMatch(/\b(effective|applied|active)\b/i);
  unmount();
  mount(`/i/${ID}/policies/gateway.kgateway.dev/TrafficPolicy/shop/tp`);
  expect(await screen.findByText("Policy applied and active")).toBeInTheDocument();
  const own = [...document.querySelectorAll("main, body > div")]
    .map((el) => el.cloneNode(true) as Element)
    .map((el) => {
      for (const n of el.querySelectorAll('[data-testid^="condition-"] .message, [data-testid="feature-yaml"]'))
        n.remove();
      return el.textContent ?? "";
    })
    .join(" ");
  expect(own).not.toMatch(/\b(effective|applied|active)\b/i);
});

// region List
it("lists the golden rows with targets as written, the sort note, and source tags on the column headers", async () => {
  serve({ [`${API}/policies`]: list });
  mount(`/i/${ID}/policies`);
  const table = await screen.findByRole("table", { name: copy.policiesCopy.title });
  await waitFor(() => expect(table).toHaveAttribute("aria-busy", "false"));
  expect(screen.getByTestId("policies-sort-note")).toHaveTextContent(copy.policiesCopy.sortNote);
  const rows = within(table).getAllByRole("row").slice(1);
  expect(rows).toHaveLength(list().items.length);
  // The target the check says is missing is named so.
  expect(table).toHaveTextContent("HTTPRoute gone: not found");
  const head = within(table).getAllByRole("columnheader");
  expect(head.map((h) => h.textContent)).toEqual([
    "Policy",
    "Kind",
    "TargetsSource: Declared",
    "AcceptedSource: Inferred",
    "Reported attached onSource: Reported",
    "FeaturesSource: Declared",
  ]);
});

it("the chips set the list's query, and a row opens the policy detail with its group", async () => {
  serve({ [`${API}/policies`]: list });
  mount(`/i/${ID}/policies`);
  const table = await screen.findByRole("table", { name: copy.policiesCopy.title });
  await waitFor(() => expect(table).toHaveAttribute("aria-busy", "false"));
  await userEvent.click(screen.getByRole("button", { name: copy.policiesCopy.hasProblems }));
  await waitFor(() => expect(calls("/policies").at(-1)?.searchParams.get("hasProblems")).toBe("true"));
  const kinds = screen.getByRole("group", { name: copy.policiesCopy.kinds });
  await userEvent.click(within(kinds).getByRole("button", { name: "TrafficPolicy" }));
  await waitFor(() =>
    expect(calls("/policies").at(-1)?.searchParams.getAll("kind")).toEqual(["gateway.kgateway.dev/TrafficPolicy"]),
  );
  const features = screen.getByRole("group", { name: copy.policiesCopy.features });
  await userEvent.click(within(features).getByRole("button", { name: "timeouts" }));
  await waitFor(() => expect(calls("/policies").at(-1)?.searchParams.getAll("feature")).toEqual(["timeouts"]));
  // No sort or text search is sent: the server lists by namespace and name only.
  const q = calls("/policies").at(-1);
  expect(q?.searchParams.has("sort")).toBe(false);
  expect(q?.searchParams.has("q")).toBe(false);
  const row = (list().items as PolicyRow[]).find((r) => r.ref.kind === "TrafficPolicy");
  await userEvent.click(within(table).getByRole("link", { name: `${row?.ref.namespace}/${row?.ref.name}` }));
  expect(screen.getByTestId("location").textContent).toBe(
    `/i/${ID}/policies/gateway.kgateway.dev/TrafficPolicy/${row?.ref.namespace}/${row?.ref.name}`,
  );
});

// region Detail
it("the detail shows targets with checks, status per ancestor verbatim with marks and notes, configuration and annotations", async () => {
  serve({ [`${API}/policies/gateway.kgateway.dev/TrafficPolicy/shop/tp`]: detail });
  mount(`/i/${ID}/policies/gateway.kgateway.dev/TrafficPolicy/shop/tp`);
  await screen.findByRole("heading", { name: copy.policyDetailCopy.summary });
  const targets = screen.getAllByTestId("policy-target");
  expect(targets[0]).toHaveTextContent("Found");
  expect(targets[1]).toHaveTextContent("Not found");
  const [current, other] = screen.getAllByTestId("policy-ancestor");
  // Merged on a Gateway carries the gateway-wide note (fixed copy, design §6.3 item 2).
  expect(within(current as HTMLElement).getByTestId("reading-note")).toHaveTextContent(
    copy.readingNoteCopy["gateway-wide"] as string,
  );
  expect(within(other as HTMLElement).getByTestId("not-current")).toHaveTextContent("Other controller");
  // A ref the viewer may not get is text, not a link.
  expect(within(other as HTMLElement).queryByRole("link")).toBeNull();
  expect(screen.getByTestId("feature-yaml")).toHaveTextContent("request: 5s");
  expect(screen.getByTestId("policy-annotation")).toHaveTextContent(copy.policyDetailCopy.annotationNote);
  // Every value carries a source tag.
  const sources = screen.getAllByTestId("source-tag").map((t) => t.getAttribute("data-source"));
  expect(new Set(sources)).toEqual(new Set(["declared", "inferred", "controller-reported"]));
});

it("a hidden or absent policy shows the not-in-scope state", async () => {
  serve({});
  mount(`/i/${ID}/policies/gateway.kgateway.dev/TrafficPolicy/shop/nope`);
  expect(await screen.findByText("Not in this install's scope")).toBeInTheDocument();
});

// region Subject sections
const asRef = (r: { group: string; kind: string; namespace?: string; name: string }): LinkedRef => ({
  ...r,
  link: "detail",
});

it("a Gateway lists policies targeting it and reported attached here, each linked to its policy page with a source", () => {
  const d = withPolicyLinks(gatewayGolden.body as unknown as GatewayDetail);
  renderWithTheme(<PolicyRelationsSection installId={ID} subject={asRef(d.ref)} relations={d.policyRelations} />);
  const targeting = screen.getByTestId("relations-targeting");
  const attached = screen.getByTestId("relations-attached-here");
  expect(screen.queryByTestId("relations-targeting-parent")).toBeNull();
  for (const [group, rels] of [
    [targeting, d.policyRelations.targeting],
    [attached, d.policyRelations.attachedHere ?? []],
  ] as const) {
    const items = within(group).getAllByTestId("policy-relation");
    expect(items).toHaveLength(rels.length);
    for (const [i, r] of rels.entries()) {
      const link = within(items[i] as HTMLElement).getByRole("link", {
        name: `${r.policy.namespace}/${r.policy.name}`,
      });
      expect(link.getAttribute("href")).toBe(
        `/i/${ID}/policies/${r.policy.group}/${r.policy.kind}/${r.policy.namespace}/${r.policy.name}`,
      );
      expect(within(items[i] as HTMLElement).getAllByTestId("source-tag")[0]).toHaveAttribute("data-source", r.source);
    }
  }
  // Merged on a Gateway: the gateway-wide note under the item, inferred.
  expect(within(attached).getByTestId("relation-note")).toHaveTextContent(
    copy.readingNoteCopy["gateway-wide"] as string,
  );
  expect(within(attached).getByTestId("relation-condition")).toHaveTextContent("Attached: True");
  // The selector note links to the Policies list filtered to the namespace.
  const hint = screen.getByTestId("relations-selectors");
  expect(hint).toHaveTextContent(copy.relationsCopy.selectorNote);
  expect(within(hint).getByRole("link").getAttribute("href")).toBe(
    `/i/${ID}/policies?namespace=${d.policyRelations.selectors.namespace}`,
  );
});

it("a route shows policies naming a parent with the fixed note and the path walked, and links selectors by kind", () => {
  const d = withPolicyLinks(routeGolden.body as unknown as RouteDetail);
  d.policyRelations.selectors.kinds = [{ group: "gateway.kgateway.dev", kind: "TrafficPolicy" }];
  renderWithTheme(<PolicyRelationsSection installId={ID} subject={asRef(d.ref)} relations={d.policyRelations} />);
  const parent = screen.getByTestId("relations-targeting-parent");
  expect(parent).toHaveTextContent(copy.relationsCopy.parentNote("route"));
  const via = within(parent).getAllByTestId("relation-via")[0] as HTMLElement;
  for (const h of d.policyRelations.targetingParent?.[0]?.via ?? [])
    expect(via).toHaveTextContent(`${h.ref.kind} ${h.ref.namespace}/${h.ref.name}`);
  expect(screen.queryByTestId("relations-attached-here")).toBeNull();
  const href = within(screen.getByTestId("relations-selectors")).getByRole("link").getAttribute("href") ?? "";
  const q = new URL(href, "http://ui").searchParams;
  expect(q.get("namespace")).toBe(d.policyRelations.selectors.namespace);
  expect(q.get("kind")).toBe("gateway.kgateway.dev/TrafficPolicy");
});

it("an empty group says so, and an older body without relations shows no section", () => {
  const d = withPolicyLinks(gatewayGolden.body as unknown as GatewayDetail);
  const { unmount } = renderWithTheme(
    <PolicyRelationsSection
      installId={ID}
      subject={asRef(d.ref)}
      relations={{ ...d.policyRelations, targeting: [], attachedHere: [] }}
    />,
  );
  expect(within(screen.getByTestId("relations-targeting")).getByText(copy.relationsCopy.none)).toBeInTheDocument();
  unmount();
  renderWithTheme(<PolicyRelationsSection installId={ID} subject={asRef(d.ref)} />);
  expect(screen.queryByTestId("policy-relations")).toBeNull();
});

it("an ancestor waiting for status is marked so and left out of the summaries", async () => {
  const d = detail();
  const second = d.ancestors[1];
  if (second) second.notCurrent = "pending";
  serve({ [`${API}/policies/gateway.kgateway.dev/TrafficPolicy/shop/tp`]: () => d });
  mount(`/i/${ID}/policies/gateway.kgateway.dev/TrafficPolicy/shop/tp`);
  await screen.findByRole("heading", { name: copy.policyDetailCopy.summary });
  expect(screen.getByTestId("not-current")).toHaveTextContent(copy.notCurrentCopy.pending as string);
});

// region Review fixes (8.2 review #3, #9)
it("a policy the viewer may not open by name is text, not a link, in the list and in relations (8.2 review #3)", async () => {
  const l = list();
  const named = l.items[0] as PolicyRow;
  const { link: _, ...unlinked } = named.ref;
  l.items[0] = { ...named, ref: unlinked as LinkedRef };
  serve({ [`${API}/policies`]: () => l });
  const { unmount } = mount(`/i/${ID}/policies`);
  const table = await screen.findByRole("table", { name: copy.policiesCopy.title });
  await waitFor(() => expect(table).toHaveAttribute("aria-busy", "false"));
  const row = within(table).getAllByRole("row")[1] as HTMLElement;
  expect(within(row).queryByRole("link")).toBeNull();
  expect(row).toHaveTextContent(`${named.ref.namespace}/${named.ref.name}`);
  // The row itself opens nothing either.
  expect(row).not.toHaveAttribute("data-link");
  unmount();
  const d = withPolicyLinks(gatewayGolden.body as unknown as GatewayDetail);
  const rel = d.policyRelations.targeting[0];
  if (rel) delete (rel.policy as Partial<LinkedRef>).link;
  renderWithTheme(<PolicyRelationsSection installId={ID} subject={asRef(d.ref)} relations={d.policyRelations} />);
  const item = within(screen.getByTestId("relations-targeting")).getAllByTestId("policy-relation")[0] as HTMLElement;
  expect(within(item).queryByRole("link")).toBeNull();
});

it("every tag follows the API's source fields (8.2 review #9)", async () => {
  // List: a failing target check carries its own inferred tag; an empty attached-on summary is inferred.
  serve({ [`${API}/policies`]: list });
  const { unmount } = mount(`/i/${ID}/policies`);
  const table = await screen.findByRole("table", { name: copy.policiesCopy.title });
  await waitFor(() => expect(table).toHaveAttribute("aria-busy", "false"));
  const gone = within(table)
    .getByText(/HTTPRoute gone/)
    .closest("td") as HTMLElement;
  expect(within(gone).getByTestId("source-tag")).toHaveAttribute("data-source", "inferred");
  const emptyAttached = [...table.querySelectorAll('td[data-col="attachedOn"]')].find((td) =>
    td.textContent?.includes(copy.attachedCopy.none),
  ) as HTMLElement;
  expect(within(emptyAttached).getByTestId("source-tag")).toHaveAttribute("data-source", "inferred");
  unmount();
  // Detail: an empty attached-on summary is inferred; a stale condition carries staleSource.
  const d = detail();
  d.attachedOn = [];
  const other = d.ancestors[1];
  if (other) other.staleSource = "inferred";
  serve({ [`${API}/policies/gateway.kgateway.dev/TrafficPolicy/shop/tp`]: () => d });
  mount(`/i/${ID}/policies/gateway.kgateway.dev/TrafficPolicy/shop/tp`);
  await screen.findByRole("heading", { name: copy.policyDetailCopy.summary });
  expect(within(screen.getByTestId("policy-attached-on")).getByTestId("source-tag")).toHaveAttribute(
    "data-source",
    "inferred",
  );
  const stale = within(screen.getAllByTestId("policy-ancestor")[1] as HTMLElement).getByTestId("stale-source");
  expect(within(stale).getByTestId("source-tag")).toHaveAttribute("data-source", "inferred");
});

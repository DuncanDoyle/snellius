import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes, useLocation } from "react-router-dom";
import listGolden from "../../../../../../pkg/api/testdata/golden/policies.json";
import detailGolden from "../../../../../../pkg/api/testdata/golden/policy-detail.json";
import type { PoliciesResponse, PolicyDetail, PolicyRow } from "../../api/types";
import * as copy from "../../copy/policies";
import { renderWithTheme } from "../../test/render";
import { PoliciesPage } from "./PoliciesPage";
import { PolicyPage } from "./PolicyPage";

const ID = "kgw.kgateway";
const API = `/api/v1/installs/${ID}`;
const now = () => new Date().toISOString();
const list = (): PoliciesResponse => ({
  ...(structuredClone(listGolden.body) as PoliciesResponse),
  collectedAt: now(),
});
const detail = (): PolicyDetail => ({ ...(structuredClone(detailGolden.body) as PolicyDetail), collectedAt: now() });

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

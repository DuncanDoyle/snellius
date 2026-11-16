import { act, screen, waitFor } from "@testing-library/react";
import { Route, Routes, useLocation } from "react-router-dom";
import type { MetaResponse } from "../api/types";
import { listCopy } from "../copy/explorer";
import { shellCopy } from "../copy/shell";
import { type ListName, useListPage } from "../explore/listQuery";
import { renderWithTheme } from "../test/render";
import { getNamespaceScope, NAMESPACE_SCOPE_KEY, reloadNamespaceScope, setNamespaceScope } from "./namespaceScopeStore";

const ID = "kgw.kgateway";
const LISTS: ListName[] = ["routes", "gateways", "listenersets", "paths", "backends", "findings", "processes"];
const meta = (filters: Record<string, string[]>): MetaResponse => ({
  version: "dev",
  synced: true,
  platform: "Kubernetes",
  capabilities: [],
  kinds: [],
  authMode: "local",
  filters,
});
const scoped = Object.fromEntries(LISTS.map((l) => [l, ["namespaces"]]));

/** Serves /meta with filters, once gate resolves when given; every other request is a 404. */
function serveMeta(filters: Record<string, string[]>, gate?: Promise<void>) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) => {
      if (url !== "/api/v1/meta") return new Response(JSON.stringify({ error: "not found" }), { status: 404 });
      await gate;
      return new Response(JSON.stringify(meta(filters)));
    }),
  );
}

// The queries each list's hook was asked to send, by list; an empty install ID holds the request.
const asked: Record<string, URLSearchParams[]> = {};
// The last useListPage result, by list.
const pages: Record<string, ReturnType<typeof useListPage>> = {};
function Probe({ list }: { list: ListName }) {
  pages[list] = useListPage(list, (id, q) => {
    if (id) asked[list] = [...(asked[list] ?? []), q];
    return { data: { install: ID, items: [], total: 0, collectedAt: "" }, cursorRejected: false, isLaggy: false };
  });
  const loc = useLocation();
  return <output data-testid={`loc-${list}`}>{loc.search}</output>;
}
const last = (list: string) => asked[list]?.at(-1);

function mount(list: ListName, search = "") {
  return renderWithTheme(
    <Routes>
      <Route path="/i/:id/x" element={<Probe list={list} />} />
    </Routes>,
    { path: `/i/${ID}/x${search}` },
  );
}

beforeEach(() => {
  localStorage.clear();
  setNamespaceScope([]);
  for (const k of Object.keys(asked)) delete asked[k];
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

it("keeps the choice across a reload", () => {
  setNamespaceScope(["shop", "infra"]);
  expect(JSON.parse(localStorage.getItem(NAMESPACE_SCOPE_KEY) ?? "")).toEqual(["infra", "shop"]);
  reloadNamespaceScope();
  expect(getNamespaceScope()).toEqual(["infra", "shop"]);
  // All namespaces clears the stored key.
  setNamespaceScope([]);
  expect(localStorage.getItem(NAMESPACE_SCOPE_KEY)).toBeNull();
});

it("ignores a stored value that is not a list of namespace names", () => {
  localStorage.setItem(NAMESPACE_SCOPE_KEY, '["shop","Not_Valid",3]');
  reloadNamespaceScope();
  expect(getNamespaceScope()).toEqual(["shop"]);
  localStorage.setItem(NAMESPACE_SCOPE_KEY, "{");
  reloadNamespaceScope();
  expect(getNamespaceScope()).toEqual([]);
});

it("keeps the choice for the page load when storage throws", () => {
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
    throw new Error("blocked");
  });
  vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
    throw new Error("blocked");
  });
  expect(() => setNamespaceScope(["shop"])).not.toThrow();
  expect(getNamespaceScope()).toEqual(["shop"]);
  reloadNamespaceScope();
  expect(getNamespaceScope()).toEqual([]);
});

it.each(LISTS)("%s sends the scope when the server lists the filter", async (list) => {
  serveMeta(scoped);
  setNamespaceScope(["shop", "infra"]);
  mount(list);
  await waitFor(() => expect(last(list)?.get("namespaces")).toBe("infra,shop"));
});

it.each(LISTS)("%s sends no scope by default", async (list) => {
  serveMeta(scoped);
  mount(list);
  await waitFor(() => expect(vi.mocked(fetch)).toHaveBeenCalled());
  await act(async () => {});
  expect(last(list)?.has("namespaces")).toBe(false);
});

it("sends no scope to a server that does not list the filter", async () => {
  serveMeta({ routes: ["listener"] });
  setNamespaceScope(["shop"]);
  mount("routes");
  await waitFor(() => expect(vi.mocked(fetch)).toHaveBeenCalled());
  await act(async () => {});
  expect(last("routes")?.has("namespaces")).toBe(false);
});

it("a scope change returns to the first page", async () => {
  serveMeta(scoped);
  mount("routes", "?cursor=c1&sort=name.desc");
  await waitFor(() => expect(last("routes")?.get("cursor")).toBe("c1"));
  act(() => setNamespaceScope(["shop"]));
  await waitFor(() => expect(screen.getByTestId("loc-routes").textContent).toBe("?sort=name.desc"));
  expect(last("routes")?.get("namespaces")).toBe("shop");
  expect(last("routes")?.has("cursor")).toBe(false);
  // No request carries the old cursor with the new scope.
  expect(asked.routes?.some((q) => q.get("namespaces") === "shop" && q.has("cursor"))).toBe(false);
});

it("with a saved scope, holds the list until /meta loads, then sends the scope and keeps the URL cursor", async () => {
  let release: (() => void) | undefined;
  serveMeta(
    scoped,
    new Promise<void>((r) => {
      release = r;
    }),
  );
  setNamespaceScope(["shop"]);
  mount("routes", "?cursor=c1");
  await act(async () => {});
  expect(asked.routes).toBeUndefined();
  await act(async () => release?.());
  await waitFor(() => expect(last("routes")?.get("namespaces")).toBe("shop"));
  expect(asked.routes?.every((q) => q.get("namespaces") === "shop")).toBe(true);
  expect(last("routes")?.get("cursor")).toBe("c1");
  expect(screen.getByTestId("loc-routes").textContent).toBe("?cursor=c1");
});

it("without a saved scope, sends the list before /meta loads", async () => {
  serveMeta(scoped, new Promise<void>(() => {}));
  mount("routes");
  await waitFor(() => expect(asked.routes?.length).toBeGreaterThan(0));
  expect(last("routes")?.has("namespaces")).toBe(false);
});

it("sends the list without a scope when /meta fails", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => new Response(JSON.stringify({ error: "boom" }), { status: 400 })),
  );
  setNamespaceScope(["shop"]);
  mount("routes");
  await waitFor(() => expect(asked.routes?.length).toBeGreaterThan(0));
  expect(last("routes")?.has("namespaces")).toBe(false);
});

it.each(LISTS)("%s says when the scope empties it and offers all namespaces", async (list) => {
  serveMeta(scoped);
  setNamespaceScope(["shop"]);
  mount(list);
  await waitFor(() => expect(pages[list]?.emptyActionLabel).toBe(shellCopy.namespaceScope.showAll));
  expect(pages[list]?.emptyTitle("routes")).toBe(shellCopy.namespaceScope.emptyList("routes"));
  act(() => pages[list]?.onEmptyAction?.());
  expect(getNamespaceScope()).toEqual([]);
  await waitFor(() => expect(pages[list]?.emptyTitle("routes")).toBe(listCopy.empty("routes")));
  expect(pages[list]?.onEmptyAction).toBeUndefined();
});

it("a filter's empty state wins over the scope's", async () => {
  serveMeta(scoped);
  setNamespaceScope(["shop"]);
  mount("routes", "?q=x");
  await waitFor(() => expect(last("routes")?.get("namespaces")).toBe("shop"));
  expect(pages.routes?.emptyTitle("routes")).toBe(listCopy.emptyFiltered("routes"));
  expect(pages.routes?.emptyActionLabel).toBe(listCopy.clearFilters);
});

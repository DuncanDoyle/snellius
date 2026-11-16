import { act, screen, waitFor } from "@testing-library/react";
import { Route, Routes, useLocation } from "react-router-dom";
import type { MetaResponse } from "../api/types";
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

/** Serves /meta with filters; every other request is a 404. */
function serveMeta(filters: Record<string, string[]>) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) =>
      url === "/api/v1/meta"
        ? new Response(JSON.stringify(meta(filters)))
        : new Response(JSON.stringify({ error: "not found" }), { status: 404 }),
    ),
  );
}

// The queries each list's hook was asked, by list.
const asked: Record<string, URLSearchParams[]> = {};
function Probe({ list }: { list: ListName }) {
  useListPage(list, (_id, q) => {
    asked[list] = [...(asked[list] ?? []), q];
    return { cursorRejected: false, isLaggy: false };
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

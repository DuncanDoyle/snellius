import { act, screen, waitFor } from "@testing-library/react";
import { Route, Routes } from "react-router-dom";
import metaGolden from "../../../../../pkg/api/testdata/golden/meta.json";
import routeGolden from "../../../../../pkg/api/testdata/golden/route-detail.json";
import routesGolden from "../../../../../pkg/api/testdata/golden/routes.json";
import { authCopy } from "../copy/auth";
import { RoutePage } from "../pages/explore/RoutePage";
import { RoutesPage } from "../pages/explore/RoutesPage";
import { renderWithTheme } from "../test/render";
import { ApiError } from "./client";
import { apiErrorState } from "./errorState";
import { POLL_MS } from "./hooks";
import type { RouteDetail, RouteList } from "./types";

const ID = "kgw.kgateway";
const GW = "gateway.networking.k8s.io";
const API = `/api/v1/installs/${ID}`;
const now = () => new Date().toISOString();
const routes = (): RouteList => ({ ...(routesGolden.body as RouteList), collectedAt: now() });
const mid = (): RouteDetail => structuredClone({ ...(routeGolden.body as RouteDetail), collectedAt: now() });
const AUTHZ: [number, unknown] = [503, { error: "authz-unavailable" }];

type Reply = unknown | [number, unknown];
/** Stubs fetch: the page's own path answers through own, the rest from the goldens. */
function serve(path: string, own: () => Reply) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) => {
      const u = new URL(url, "http://ui");
      const r =
        u.pathname === path
          ? own()
          : u.pathname === "/api/v1/meta"
            ? metaGolden.body
            : u.pathname === `${API}/routes`
              ? routes()
              : undefined;
      if (r === undefined) return new Response(JSON.stringify({ error: "not found" }), { status: 404 });
      const [status, body] = Array.isArray(r) && typeof r[0] === "number" ? (r as [number, unknown]) : [200, r];
      return new Response(JSON.stringify(body), { status });
    }),
  );
}

function mount(path: string) {
  renderWithTheme(
    <Routes>
      <Route path="/i/:id/routes" element={<RoutesPage />} />
      <Route path="/i/:id/routes/:group/:kind/:ns/:name" element={<RoutePage />} />
    </Routes>,
    { path },
  );
}

/** Runs one poll interval, so the next request (a poll or an error retry) is answered. */
async function nextPoll() {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(POLL_MS + 100);
  });
}

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

it.each([
  [new ApiError(503, "authz-unavailable", undefined, "authz-unavailable"), "authz-unavailable"],
  [new ApiError(503, "cache not synced", undefined, "cache not synced"), "connecting"],
  [new ApiError(503, "secure-mode-incomplete", undefined, "secure-mode-incomplete"), "connecting"],
  [new ApiError(503, "Service Unavailable"), "connecting"],
  [new ApiError(404, "not found", undefined, "not found"), "not-found"],
  [new ApiError(500, "boom", undefined, "boom"), "failed"],
  [new ApiError(0, "Cannot reach the UI backend"), "failed"],
  [new Error("authz-unavailable"), "failed"],
  [undefined, "failed"],
])("apiErrorState(%o) is %s", (error, want) => {
  expect(apiErrorState(error)).toBe(want);
});

// One list page (through ListFrame) and one detail page (through the detail frame).
const pages = [
  { name: "routes list", url: `/i/${ID}/routes`, path: `${API}/routes`, body: routes, loaded: "rejected" },
  {
    name: "route detail",
    url: `/i/${ID}/routes/${GW}/HTTPRoute/shop/mid`,
    path: `${API}/routes/${GW}/HTTPRoute/shop/mid`,
    body: mid,
    loaded: "Route parents",
  },
];
const shown = (p: (typeof pages)[number]) =>
  p.name === "routes list" ? screen.findByText(p.loaded) : screen.findByRole("navigation", { name: p.loaded });

describe.each(pages)("$name", (p) => {
  it("shows the permission-check state before any data, then recovers on a good poll", async () => {
    let answer: Reply = AUTHZ;
    serve(p.path, () => answer);
    vi.useFakeTimers({ shouldAdvanceTime: true });
    mount(p.url);
    expect(await screen.findByText(authCopy.authzUnavailableTitle)).toBeInTheDocument();
    expect(screen.getByText(authCopy.authzUnavailableBody)).toBeInTheDocument();
    expect(screen.queryByText("Waiting for the cluster")).toBeNull();
    answer = p.body();
    await nextPoll();
    expect(await shown(p)).toBeInTheDocument();
    expect(screen.queryByText(authCopy.authzUnavailableTitle)).toBeNull();
  });

  it("shows the permission-check state after a successful poll, then recovers", async () => {
    let answer: () => Reply = p.body;
    serve(p.path, () => answer());
    vi.useFakeTimers({ shouldAdvanceTime: true });
    mount(p.url);
    await shown(p);
    answer = () => AUTHZ;
    await nextPoll();
    expect(await screen.findByTestId("page-notice")).toHaveTextContent(
      `${authCopy.authzUnavailableTitle}. ${authCopy.authzUnavailableBody}`,
    );
    answer = p.body;
    await nextPoll();
    await waitFor(() => expect(screen.queryByTestId("page-notice")).toBeNull());
    expect(await shown(p)).toBeInTheDocument();
  });

  it("a plain 503 still shows the connecting state", async () => {
    serve(p.path, () => [503, { error: "connecting to cluster: cache not synced" }]);
    mount(p.url);
    expect(await screen.findByText("Waiting for the cluster")).toBeInTheDocument();
    expect(screen.getByText("connecting to cluster: cache not synced")).toBeInTheDocument();
    expect(screen.queryByText(authCopy.authzUnavailableTitle)).toBeNull();
  });
});

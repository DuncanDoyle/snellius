import type { LinkedRef, PathRow } from "../../api/types";
import type { HealthState } from "../../theme/tokens";
import { refParam } from "../explore/detailParts";
import { buildGraph } from "./graph";

const ref = (kind: string, name: string): LinkedRef => ({
  group: kind === "Service" ? "" : "gateway.networking.k8s.io",
  kind,
  namespace: "shop",
  name,
  link: "detail",
});

/** One path through route api to an idle Service, whose backend finding lifts the route on the rows alone. */
const rows = (): PathRow[] => [
  {
    gateway: ref("Gateway", "edge"),
    listener: { name: "http", protocol: "HTTP", port: 80 },
    hostnames: [],
    routes: [ref("HTTPRoute", "api")],
    delegated: false,
    rule: 0,
    matches: [],
    action: "backend",
    backend: ref("Service", "cart"),
    weight: 1,
    share: 100,
    shareState: "shown",
    mirror: false,
    problem: {
      state: "warning",
      signals: [{ kind: "finding", code: "KGWUI-023", ref: ref("Service", "cart"), state: "warning" }],
    },
  },
];
const route = (g: ReturnType<typeof buildGraph>) => g.nodes.find((n) => n.kind === "route");

it("a route node takes the route's summary health over the path rows' state", () => {
  const health = new Map<string, HealthState>([[refParam(ref("HTTPRoute", "api")), "healthy"]]);
  expect(route(buildGraph(rows()))?.state).toBe("warning");
  expect(route(buildGraph(rows(), new Set(), { routeHealth: health }))?.state).toBe("healthy");
});

it("a route without a known summary keeps the state its rows give", () => {
  expect(route(buildGraph(rows(), new Set(), { routeHealth: new Map() }))?.state).toBe("warning");
});

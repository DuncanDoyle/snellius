import { useMemo } from "react";
import useSWR from "swr";
import { apiFetch } from "../../api/client";
import { forInstall, withQuery } from "../../api/exploreHooks";
import { installApi, live } from "../../api/hooks";
import type { LinkedRef, Page, PathRow, RouteRow } from "../../api/types";
import type { HealthState } from "../../theme/tokens";
import { refParam } from "../explore/detailParts";

/** At most this many delegating routes are asked for their delegated routes; the rest keep the rows' state. */
export const CHILD_QUERIES = 20;

/** The routes that delegate on some path, root-first and without repeats: a route followed by another in a chain. */
export function delegatingRoutes(rows: readonly PathRow[]): LinkedRef[] {
  const seen = new Map<string, LinkedRef>();
  for (const r of rows) for (const p of r.routes.slice(0, -1)) seen.set(refParam(p), p);
  return [...seen.values()].slice(0, CHILD_QUERIES);
}

/**
 * Summary health of the routes delegated to by the given routes (GET routes?parent=<route> lists the routes
 * with an edge to it), so a route linked only through delegation reads as its page does.
 */
export function useChildRouteHealth(installId: string, parents: readonly LinkedRef[]) {
  const key = installId && parents.length > 0 ? ["childRouteHealth", installId, ...parents.map(refParam)] : null;
  const { data } = useSWR<(Page<RouteRow> | undefined)[]>(
    key,
    () =>
      Promise.all(
        parents.map((p) =>
          apiFetch<Page<RouteRow>>(
            withQuery(`${installApi(installId)}/routes`, new URLSearchParams({ parent: refParam(p), limit: "200" })),
          ).catch(() => undefined),
        ),
      ),
    { ...live, keepPreviousData: true },
  );
  return useMemo(() => {
    const out = new Map<string, HealthState>();
    for (const page of data ?? []) {
      const ok = page && forInstall(page, installId);
      for (const r of ok?.items ?? []) out.set(refParam(r.ref), r.health);
    }
    return out;
  }, [data, installId]);
}

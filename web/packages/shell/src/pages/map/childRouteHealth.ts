import { useEffect, useMemo, useRef } from "react";
import useSWR from "swr";
import { ApiError, apiFetch } from "../../api/client";
import { forInstall, withQuery } from "../../api/exploreHooks";
import { installApi, live } from "../../api/hooks";
import type { LinkedRef, Page, PathRow, RouteRow } from "../../api/types";
import type { HealthState } from "../../theme/tokens";
import { refParam } from "../explore/detailParts";

/** At most this many delegating routes are asked for their delegated routes; the rest keep the rows' state. */
export const CHILD_QUERIES = 20;

/** The server admits few uncached permission checks at once, so child queries run this many at a time. */
const IN_FLIGHT = 2;
/** A 503 is retried this many times, waiting backoff.ms times the attempt number (tests set it to 0). */
const RETRIES = 2;
export const backoff = { ms: 300 };

/** Waits ms, or until the signal aborts. */
const sleep = (ms: number, signal: AbortSignal) =>
  new Promise<void>((resolve) => {
    const t = setTimeout(resolve, ms);
    signal.addEventListener(
      "abort",
      () => {
        clearTimeout(t);
        resolve();
      },
      { once: true },
    );
  });

/** apiFetch that retries a 503 a few times with a short backoff, then throws it. An aborted signal ends it. */
async function fetchRetrying<T>(path: string, signal: AbortSignal): Promise<T> {
  for (let attempt = 1; ; attempt++) {
    try {
      return await apiFetch<T>(path, signal);
    } catch (e) {
      if (signal.aborted || !(e instanceof ApiError && e.status === 503) || attempt > RETRIES) throw e;
      await sleep(backoff.ms * attempt, signal);
      if (signal.aborted) throw e;
    }
  }
}

/** Runs the tasks with at most limit in flight, keeping the results in order; an aborted signal starts no more. */
async function pooled<T>(tasks: readonly (() => Promise<T>)[], limit: number, signal: AbortSignal): Promise<T[]> {
  const out: T[] = new Array(tasks.length);
  let next = 0;
  const worker = async () => {
    while (next < tasks.length && !signal.aborted) {
      const i = next++;
      out[i] = await (tasks[i] as () => Promise<T>)();
    }
  };
  await Promise.all(Array.from({ length: Math.min(limit, tasks.length) }, worker));
  return out;
}

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
  // Only the newest pool runs: starting one, or unmounting, aborts the previous so pools never add up.
  const current = useRef<AbortController | null>(null);
  useEffect(() => () => current.current?.abort(), []);
  const { data } = useSWR<(Page<RouteRow> | undefined)[]>(
    key,
    () => {
      current.current?.abort();
      const ctl = new AbortController();
      current.current = ctl;
      const { signal } = ctl;
      return pooled(
        parents.map(
          (p) => () =>
            fetchRetrying<Page<RouteRow>>(
              withQuery(`${installApi(installId)}/routes`, new URLSearchParams({ parent: refParam(p), limit: "200" })),
              signal,
            ).catch(() => undefined),
        ),
        IN_FLIGHT,
        signal,
      );
    },
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

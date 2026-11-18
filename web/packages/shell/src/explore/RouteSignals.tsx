import type { RouteConfigSignal, RoutePathSignal, RowReason } from "../api/types";
import { columnCopy, rowReasonCopy } from "../copy/explorer";
import { HealthBadge } from "../health/HealthBadge";
import type { HealthState } from "../theme/tokens";
import type { ObjectColumn } from "./ObjectTable";

/** Whether a path reason is the one that already says the path is incomplete. */
const isIncompleteReason = (r?: RowReason) => r?.kind === "path-incomplete";

/** The note a fault-showing path adds when the viewer could not read all of it (design §8.1). */
export function pathNote(path: RoutePathSignal): string | undefined {
  return path.incomplete && !isIncompleteReason(path.reason) ? rowReasonCopy.incompleteNote : undefined;
}

/** The signals' names, in the header, the strip and the columns. */
export const signalLabel = { config: columnCopy.configuration, path: columnCopy.trafficPath } as const;

const RANK: Record<HealthState, number> = { healthy: 0, unknown: 1, warning: 2, error: 3 };

/** The signal that decides the route's problem: the worse of the two, the configuration on a tie; none when both are healthy. */
export function decidingSignal(
  config: RouteConfigSignal,
  path: RoutePathSignal,
): { id: "config" | "path"; state: HealthState; reason?: RowReason } | undefined {
  if (RANK[config.state] === 0 && RANK[path.state] === 0) return undefined;
  return RANK[config.state] >= RANK[path.state]
    ? { id: "config", state: config.state, reason: config.reason }
    : { id: "path", state: path.state, reason: path.reason };
}

/** A route's configuration signal: state and, when not healthy, the reason on hover and focus. */
export function ConfigSignalBadge({ signal }: { signal: RouteConfigSignal }) {
  return <HealthBadge state={signal.state} reason={signal.reason} />;
}

/** A route's traffic path signal, with the incomplete note after a readable fault. */
export function PathSignalBadge({ signal }: { signal: RoutePathSignal }) {
  return <HealthBadge state={signal.state} reason={signal.reason} note={pathNote(signal)} />;
}

/** The Configuration and Traffic path columns of a route table, placed after Status. */
export function signalColumns<T extends { config: RouteConfigSignal; path: RoutePathSignal }>(): ObjectColumn<T>[] {
  return [
    {
      id: "config",
      header: columnCopy.configuration,
      width: "140px",
      cell: (r) => <ConfigSignalBadge signal={r.config} />,
    },
    {
      id: "path",
      header: columnCopy.trafficPath,
      width: "140px",
      cell: (r) => <PathSignalBadge signal={r.path} />,
    },
  ];
}

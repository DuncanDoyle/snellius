import { useMemo } from "react";
import { useRoutes } from "../../../api/exploreHooks";
import type { LinkedRef, RouteRow } from "../../../api/types";
import { columnCopy, routeCopy } from "../../../copy/explorer";
import type { ObjectColumn } from "../../../explore/ObjectTable";
import { HealthBadge } from "../../../health/HealthBadge";
import { expectedRows, refParam, SubList } from "../detailParts";
import { Muted, NameCell, OneLine } from "../listParts";

// region Helpers
const NOUN = ["route", "routes"] as const;
const rowRef = (r: RouteRow) => r.ref;

function columns(installId: string): ObjectColumn<RouteRow>[] {
  return [
    { id: "health", header: columnCopy.status, width: "120px", cell: (r) => <HealthBadge state={r.health} /> },
    { id: "name", header: columnCopy.route, cell: (r) => <NameCell r={r.ref} installId={installId} /> },
    {
      id: "hostnames",
      header: columnCopy.hostnames,
      width: "24%",
      secondary: true,
      cell: (r) =>
        r.hostnames.length ? <OneLine text={r.hostnames.join(", ")} /> : <Muted>{columnCopy.inherited}</Muted>,
    },
  ];
}

// region Component
interface ChildRoutesProps {
  installId: string;
  route: LinkedRef;
  /** Visible reported children: the rows parent=<route> lists (declared candidates are never children). */
  expected: number;
}

/** Routes under this one, paged and loaded with the page (design §5.3, §10.8); a section card's body. */
export function ChildRoutes({ installId, route, expected }: ChildRoutesProps) {
  const query = useMemo(() => ({ parent: refParam(route) }), [route]);
  const cols = useMemo(() => columns(installId), [installId]);
  if (expected === 0) return <Muted>{routeCopy.noChildRoutes}</Muted>;
  return (
    <SubList
      installId={installId}
      label={routeCopy.childRoutes}
      noun={NOUN}
      useList={useRoutes}
      query={query}
      columns={cols}
      rowRef={rowRef}
      emptyTitle={routeCopy.noChildRoutes}
      skeletonRows={expectedRows(expected)}
    />
  );
}

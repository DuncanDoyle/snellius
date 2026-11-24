import type { SerializedStyles } from "@emotion/react";
import styled from "@emotion/styled";
import { useMemo } from "react";
import type { Page, PolicyRow } from "../../api/types";
import { ancestorText, attachedCopy, policiesCopy, policyColumnCopy, policyDetailCopy } from "../../copy/policies";
import type { SortState } from "../../explore/listQuery";
import { type ObjectColumn, ObjectTable } from "../../explore/ObjectTable";
import { SourceTag } from "../../explore/SourceTag";
import { HealthIcon } from "../../health/HealthIcon";
import { Muted, NameCell, OneLine } from "../explore/listParts";
import { acceptedState, acceptedText, checkFails, featureName, targetCellText } from "./policyText";

// region Styles
/** An icon and its text on one line; the text shrinks first. */
const IconLine = styled.span`
  display: flex;
  align-items: center;
  gap: ${(p) => p.theme.space(1.5)};
  min-width: 0;
  > :last-child {
    min-width: 0;
  }
`;

// region Helpers
function columns(installId: string): ObjectColumn<PolicyRow>[] {
  return [
    {
      id: "name",
      header: policyColumnCopy.policy,
      // The API links only a policy the viewer may open by name; otherwise the name is text (8.2 review #3).
      cell: (r) => <NameCell r={r.ref} installId={installId} />,
    },
    {
      id: "kind",
      header: policyColumnCopy.kind,
      width: "13%",
      cell: (r) => <OneLine text={r.ref.kind} />,
    },
    {
      id: "targets",
      header: policyColumnCopy.targets,
      headerExtra: <SourceTag source="declared" />,
      width: "22%",
      cell: (r) => {
        if (!r.targets.length) return <Muted>{policyDetailCopy.noTargets}</Muted>;
        const text = r.targets.map(targetCellText).join(" · ");
        const failed = r.targets.find(checkFails);
        // The header tags the targets as written; a failing check is a verdict with its own source (8.2 review #9).
        return (
          <IconLine>
            {failed ? <HealthIcon state="error" /> : null}
            <OneLine text={text} />
            {failed?.check ? <SourceTag source={failed.check.source} /> : null}
          </IconLine>
        );
      },
    },
    {
      id: "accepted",
      header: policyColumnCopy.accepted,
      headerExtra: <SourceTag source="inferred" />,
      width: "18%",
      cell: (r) => (
        <IconLine data-testid="policy-accepted">
          <HealthIcon state={acceptedState(r.accepted)} />
          <OneLine text={acceptedText(r.accepted)} />
        </IconLine>
      ),
    },
    {
      id: "attachedOn",
      header: policyColumnCopy.attachedOn,
      headerExtra: <SourceTag source="controller-reported" />,
      width: "17%",
      secondary: true,
      cell: (r) =>
        r.attachedOn.length ? (
          <OneLine text={r.attachedOn.map(ancestorText).join(" · ")} />
        ) : (
          // No current ancestor reports it attached: Snellius's summary, not a report (8.2 review #9).
          <IconLine>
            <Muted>{attachedCopy.none}</Muted>
            <SourceTag source="inferred" />
          </IconLine>
        ),
    },
    {
      id: "features",
      header: policyColumnCopy.features,
      headerExtra: <SourceTag source="declared" />,
      width: "15%",
      secondary: true,
      cell: (r) => <OneLine text={r.features.map(featureName).join(", ")} />,
    },
  ];
}

const rowRef = (r: PolicyRow) => r.ref;

// region Component
interface PolicyTableProps {
  installId: string;
  page?: Page<PolicyRow>;
  busy: boolean;
  onClearFilters?: () => void;
  clearLabel?: string;
  onPage: (cursor: string) => void;
  emptyTitle: string;
  stylesOverride?: SerializedStyles;
}

const NO_SORT: SortState = { key: "name", desc: false };

/** One row per readable policy, by namespace and name (design §6.2); a row opens the policy detail. */
export function PolicyTable({
  installId,
  page,
  busy,
  onClearFilters,
  clearLabel,
  onPage,
  emptyTitle,
  stylesOverride,
}: PolicyTableProps) {
  const cols = useMemo(() => columns(installId), [installId]);
  return (
    <ObjectTable
      label={policiesCopy.title}
      installId={installId}
      noun={policiesCopy.noun}
      columns={cols}
      rowRef={rowRef}
      page={page}
      loading={!page}
      busy={busy}
      emptyTitle={emptyTitle}
      onClearFilters={onClearFilters}
      clearLabel={clearLabel}
      // The server sorts by namespace and name only; no column sorts.
      sort={NO_SORT}
      onSort={() => {}}
      onPage={onPage}
      stylesOverride={stylesOverride}
    />
  );
}

import type { SerializedStyles } from "@emotion/react";
import styled from "@emotion/styled";
import { ArrowDownAZ } from "lucide-react";
import { usePolicies } from "../../api/exploreHooks";
import type { PoliciesResponse, PolicyRow } from "../../api/types";
import { policiesCopy } from "../../copy/policies";
import { trafficCopy } from "../../copy/traffic";
import { ListFrame } from "../../explore/ListFrame";
import { listValues, useListPage } from "../../explore/listQuery";
import { type FilterOption, StateFilter } from "../../explore/StateFilter";
import { Narrow, narrowOptions } from "../traffic/narrow";
import { StaleGate } from "../traffic/StaleGate";
import { PolicyTable } from "./PolicyTable";
import { kindValue } from "./policyText";

// region Styles
const SortNote = styled.p`
  display: flex;
  align-items: center;
  gap: ${(p) => p.theme.space(1.5)};
  margin: -${(p) => p.theme.space(1)} 0 ${(p) => p.theme.space(3)};
  font-size: ${(p) => p.theme.font.size.sm};
  color: ${(p) => p.theme.color.textMuted};
`;

// region Helpers
const PROBLEMS = "problems";

/** Kind chips from the facets: the kind, with its group where two groups share it. */
function kindOptions(d: PoliciesResponse | undefined, selected: string[]): FilterOption[] {
  const kinds = d?.facets.kinds ?? [];
  const shared = (k: string) => kinds.filter((x) => x.kind === k).length > 1;
  const labels = new Map(
    kinds.map((k) => [kindValue(k), shared(k.kind) ? `${k.kind} (${k.group || "core"})` : k.kind]),
  );
  return narrowOptions(kinds.map(kindValue), selected).map((o) => ({
    ...o,
    label: labels.get(o.value) ?? o.value.split("/").pop() ?? o.value,
  }));
}

// region Component
interface PoliciesPageProps {
  stylesOverride?: SerializedStyles;
}

/**
 * The install's readable policies as declared, with what the controller reports (Phase 8 design §6.2). Sorted by
 * namespace and name, never by precedence. Filters and cursor live in the URL; the server filters and pages.
 */
export function PoliciesPage({ stylesOverride }: PoliciesPageProps = {}) {
  const l = useListPage<PolicyRow, PoliciesResponse>("policies", usePolicies);
  const kinds = listValues(l.params, "kind");
  const features = listValues(l.params, "feature");
  const namespaces = listValues(l.params, "namespace");
  const facets = l.data?.facets;
  const pressed = l.params.get("hasProblems") === "true" ? [PROBLEMS] : [];
  return (
    <ListFrame
      title={policiesCopy.title}
      installId={l.id}
      noun={policiesCopy.noun}
      collectedAt={l.data?.collectedAt}
      total={l.data?.total}
      error={l.error}
      loaded={!!l.data}
      lead={
        <SortNote data-testid="policies-sort-note">
          <ArrowDownAZ size={14} aria-hidden="true" />
          {policiesCopy.sortNote}
        </SortNote>
      }
      filters={
        <StateFilter
          label={policiesCopy.show}
          options={[{ value: PROBLEMS, label: policiesCopy.hasProblems }]}
          selected={pressed}
          onChange={(v) => l.setFilter({ hasProblems: v.includes(PROBLEMS) ? "true" : null })}
        />
      }
      onClearFilters={l.filtered ? l.clearFilters : undefined}
      stylesOverride={stylesOverride}
    >
      <Narrow data-testid="narrow-to">
        <span className="label">{trafficCopy.narrow}</span>
        <span className="label" aria-hidden="true">
          {policiesCopy.kindsChips}
        </span>
        <StateFilter
          label={policiesCopy.kinds}
          options={kindOptions(l.data, kinds)}
          selected={kinds}
          onChange={(v) => l.setFilter({ kind: v.join(",") })}
        />
        <span className="label" aria-hidden="true">
          {policiesCopy.featuresChips}
        </span>
        <StateFilter
          label={policiesCopy.features}
          options={narrowOptions(
            (facets?.features ?? []).map((f) => f.key),
            features,
          )}
          selected={features}
          onChange={(v) => l.setFilter({ feature: v.join(",") })}
        />
        <span className="label" aria-hidden="true">
          {policiesCopy.namespacesChips}
        </span>
        <StateFilter
          label={policiesCopy.namespaces}
          options={narrowOptions(
            (facets?.namespaces ?? []).map((n) => n.namespace),
            namespaces,
          )}
          selected={namespaces}
          onChange={(v) => l.setFilter({ namespace: v.join(",") })}
        />
      </Narrow>
      <StaleGate collectedAt={l.data?.collectedAt} failed={!!l.error}>
        <PolicyTable
          installId={l.id}
          page={l.data}
          busy={l.busy}
          onClearFilters={l.onEmptyAction}
          clearLabel={l.emptyActionLabel}
          onPage={l.setCursor}
          emptyTitle={l.emptyTitle(policiesCopy.noun[1])}
        />
      </StaleGate>
    </ListFrame>
  );
}

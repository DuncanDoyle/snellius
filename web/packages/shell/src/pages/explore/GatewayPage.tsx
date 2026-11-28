import type { SerializedStyles } from "@emotion/react";
import { Workflow } from "lucide-react";
import { useCallback, useMemo } from "react";
import { useLocation, useNavigate, useParams } from "react-router-dom";
import { useGatewayDetail, useListenerSets } from "../../api/exploreHooks";
import { useFeatureCheck, useListFilter } from "../../api/hooks";
import type { LinkedRef, ListenerSetRow } from "../../api/types";
import { columnCopy, dataPlaneCopy, detailCopy, gatewayCopy, processesCopy } from "../../copy/explorer";
import { mapCopy } from "../../copy/map";
import { relationsCopy } from "../../copy/policies";
import { trafficCopy } from "../../copy/traffic";
import { ConditionList } from "../../explore/ConditionList";
import type { ObjectColumn } from "../../explore/ObjectTable";
import { firstFailing } from "../../explore/ProblemStrip";
import { HealthBadge } from "../../health/HealthBadge";
import { useInstallId } from "../../shell/extension";
import { installPath } from "../../shell/paths";
import { InlineLink } from "../../states/controls";
import { MapTab } from "../map/MapTab";
import { PolicyRelationsBody, policyRelationsCount } from "../policies/PolicyRelationsSection";
import type { DetailSection } from "./DetailSections";
import { DetailFrame, type ExtraTab, expectedRows, Facts, refParam, SubList } from "./detailParts";
import { DataPlaneBody } from "./gateway/DataPlaneSection";
import { ListenerTable } from "./gateway/ListenerTable";
import { ProxiesBody } from "./gateway/ProxiesSection";
import { Muted, NameCell } from "./listParts";
import {
  AttachedRoutesBody,
  attachedRoutesTitle,
  FailedRoutesBody,
  failedRoutesCount,
  type ListenerPick,
} from "./RouteSections";

// region Helpers
const GW = "gateway.networking.k8s.io";
const LS_NOUN = ["ListenerSet", "ListenerSets"] as const;

function lsColumns(installId: string): ObjectColumn<ListenerSetRow>[] {
  return [
    { id: "health", header: columnCopy.status, width: "120px", cell: (r) => <HealthBadge state={r.health} /> },
    { id: "name", header: columnCopy.listenerSet, cell: (r) => <NameCell r={r.ref} installId={installId} /> },
    { id: "kind", header: columnCopy.kind, width: "140px", secondary: true, cell: (r) => r.ref.kind },
    { id: "listeners", header: columnCopy.listeners, width: "100px", cell: (r) => r.listeners },
  ];
}
const lsRef = (r: ListenerSetRow) => r.ref;

// region Component
interface GatewayPageProps {
  stylesOverride?: SerializedStyles;
}

/** One Gateway in the D-148 section order: summary, conditions, listeners, ListenerSets, routes, runtime, policies; a Map tab. */
export function GatewayPage({ stylesOverride }: GatewayPageProps = {}) {
  const id = useInstallId();
  const { ns = "", name = "" } = useParams();
  const { data, error } = useGatewayDetail(id, ns, name);
  // A controller without a data plane or process view gets one line instead of those sections (design §10).
  const has = useFeatureCheck(id);
  const dataPlane = has("dataplane") !== false;
  const processes = has("processes") !== false;
  const ref = useMemo<LinkedRef>(
    () => ({ group: GW, kind: "Gateway", namespace: ns, name, link: "detail" }),
    [ns, name],
  );
  const extraTabs = useMemo<ExtraTab[]>(
    () => [{ id: "map", label: mapCopy.tab, icon: Workflow, content: <MapTab installId={id} gateway={ref} /> }],
    [id, ref],
  );
  const lsQuery = useMemo(() => ({ parent: refParam(ref) }), [ref]);
  const lsCols = useMemo(() => lsColumns(id), [id]);
  // ?listener= selects a listener: the one detail param besides ?tab (design §8.1). Selecting pushes, so back clears it.
  const location = useLocation();
  const navigate = useNavigate();
  const selected = new URLSearchParams(location.search).get("listener") ?? undefined;
  const supported = useListFilter("routes", "listener");
  const onSelect = useCallback(
    (name?: string) => {
      const q = new URLSearchParams(location.search);
      if ((q.get("listener") ?? undefined) === name) return;
      if (name) q.set("listener", name);
      else q.delete("listener");
      const s = q.toString();
      navigate({ search: s ? `?${s}` : "", hash: location.hash });
    },
    [location.search, location.hash, navigate],
  );
  // An older server rejects listener= (strict params), so it gets no selector and the URL value is ignored.
  // While /meta loads the selector keeps its place (ready false), so the route sections never move.
  const pick = useMemo<ListenerPick | undefined>(
    () =>
      supported !== false && data
        ? {
            listeners: data.listeners,
            selected,
            onSelect,
            ready: !!supported,
            ruleSet: data.ruleSet ?? "",
            ruleSetLabel: data.ruleSetLabel,
          }
        : undefined,
    [supported, data, selected, onSelect],
  );
  const sections = useMemo<DetailSection[] | undefined>(() => {
    if (!data) return undefined;
    const relations = data.policyRelations;
    const lsTotal = data.listenerSets ?? data.attachedListenerSets;
    const failedTotal = failedRoutesCount(data.routeCounts);
    const out: DetailSection[] = [
      {
        id: "gw-summary",
        group: "status",
        title: detailCopy.summary,
        render: () => (
          <Facts>
            <dt>{columnCopy.class}</dt>
            <dd>{data.class}</dd>
            <dt>{columnCopy.addresses}</dt>
            <dd>{data.addresses.length ? data.addresses.join(", ") : <Muted>{columnCopy.none}</Muted>}</dd>
            <dt>{trafficCopy.title}</dt>
            <dd>
              <InlineLink to={`${installPath(id, "traffic")}?${new URLSearchParams({ gateway: `${ns}/${name}` })}`}>
                {trafficCopy.gatewayEntry}
              </InlineLink>
            </dd>
          </Facts>
        ),
      },
      {
        id: "gw-conditions",
        group: "status",
        title: detailCopy.conditions,
        count: data.conditions.length,
        muted: !data.conditions.length,
        render: () => <ConditionList conditions={data.conditions} generation={data.generation} />,
      },
      {
        id: "gw-listeners",
        group: "does",
        title: gatewayCopy.listeners,
        count: data.listeners.length,
        muted: !data.listeners.length,
        render: () => (
          <ListenerTable
            listeners={data.listeners}
            generation={data.generation}
            installId={id}
            policies={relations?.targeting}
          />
        ),
      },
      {
        id: "gw-listenersets",
        group: "does",
        title: gatewayCopy.listenerSets,
        count: lsTotal,
        muted: lsTotal === 0,
        render: () => (
          <>
            {data.attachedListenerSets !== undefined ? (
              <Muted>{gatewayCopy.attachedListenerSets(data.attachedListenerSets)}</Muted>
            ) : null}
            <SubList
              installId={id}
              label={gatewayCopy.listenerSets}
              noun={LS_NOUN}
              useList={useListenerSets}
              query={lsQuery}
              columns={lsCols}
              rowRef={lsRef}
              emptyTitle={gatewayCopy.noListenerSets}
              // The list's own total for this caller; an older body falls back to the status count.
              skeletonRows={expectedRows(lsTotal ?? 1)}
            />
          </>
        ),
      },
      {
        id: "attached-routes",
        group: "does",
        // The selection the body uses: none where the server has no listener filter.
        title: attachedRoutesTitle(pick?.selected),
        // A listener selection lists other rows than the response's total counts.
        count: pick?.selected ? undefined : data.routeCounts.accepted,
        muted: !pick?.selected && data.routeCounts.accepted === 0,
        render: () => <AttachedRoutesBody installId={id} parent={ref} counts={data.routeCounts} pick={pick} />,
      },
      {
        id: "failed-routes",
        group: "does",
        title: gatewayCopy.notAttached,
        count: failedTotal,
        muted: failedTotal === 0,
        render: () => <FailedRoutesBody installId={id} parent={ref} counts={data.routeCounts} />,
      },
    ];
    // Sent for a Gateway the install owns; it arrives with the detail, so nothing below moves.
    if (dataPlane && data.dataPlane) {
      const dp = data.dataPlane;
      out.push({
        id: "gw-dataplane",
        group: "runtime",
        title: dataPlaneCopy.title,
        render: () => <DataPlaneBody installId={id} dataPlane={dp} />,
      });
    }
    if (processes && data.proxies) {
      const proxies = data.proxies;
      out.push({
        id: "gw-proxies",
        group: "runtime",
        title: processesCopy.proxies,
        render: () => <ProxiesBody gateway={ref} proxies={proxies} />,
      });
    }
    if (!dataPlane && !processes)
      out.push({
        id: "gw-dataplane",
        group: "runtime",
        title: dataPlaneCopy.title,
        render: () => <Muted>{dataPlaneCopy.notAvailable}</Muted>,
      });
    // An older server sends no relations: no Policies section.
    if (relations) {
      const n = policyRelationsCount(relations);
      out.push({
        id: "policy-relations",
        group: "actsOn",
        title: relationsCopy.title,
        count: n,
        muted: n === 0,
        render: () => <PolicyRelationsBody installId={id} subject={ref} relations={relations} />,
      });
    }
    return out;
  }, [data, id, ns, name, ref, lsQuery, lsCols, pick, dataPlane, processes]);
  return (
    <DetailFrame
      installId={id}
      refValue={ref}
      noun="Gateway"
      data={data}
      error={error}
      problem={data && { condition: firstFailing(data.conditions), conditionsId: "gw-conditions" }}
      sections={sections ?? []}
      extraTabs={extraTabs}
      stylesOverride={stylesOverride}
    />
  );
}

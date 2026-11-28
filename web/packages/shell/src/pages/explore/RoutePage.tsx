import type { SerializedStyles } from "@emotion/react";
import styled from "@emotion/styled";
import { useMemo } from "react";
import { useParams, useSearchParams } from "react-router-dom";
import { useRouteDetail } from "../../api/exploreHooks";
import { useInstallFeature } from "../../api/hooks";
import type { LinkedRef, ParentRef, RouteDetail } from "../../api/types";
import { detailCopy, effectiveCopy, routeCopy, traceCopy } from "../../copy/explorer";
import { relationsCopy } from "../../copy/policies";
import { trafficCopy } from "../../copy/traffic";
import { DelegationNotCheckedView } from "../../explore/DelegationNotChecked";
import { firstFailing } from "../../explore/ProblemStrip";
import { useInstallId } from "../../shell/extension";
import { installPath } from "../../shell/paths";
import { InlineLink } from "../../states/controls";
import { PolicyRelationsBody, policyRelationsCount } from "../policies/PolicyRelationsSection";
import type { DetailSection } from "./DetailSections";
import { DetailFrame, Facts, refParam } from "./detailParts";
import { Muted } from "./listParts";
import { ChildRoutes } from "./route/ChildRoutes";
import { EffectiveMatches } from "./route/EffectiveMatches";
import { ParentCard } from "./route/ParentCard";
import { linkedRule, RuleList } from "./route/RuleList";
import { RuleTable } from "./route/RuleTable";
import { isRouteRef, sameParentRef } from "./route/refs";
import { Serves } from "./route/Serves";
import { UpChain } from "./route/UpChain";

// region Styles
const Cards = styled.div`
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(min(100%, 320px), 1fr));
  gap: ${(p) => p.theme.space(3)};
`;

// region Helpers
/** Whether a parent route names d, in its spec or its status. */
const isChild = (d: RouteDetail) => d.parentRefs.some(isRouteRef) || d.parents.some((p) => isRouteRef(p.target));

/** A child takes its hostnames from a parent route; a route under a listener from the listener. */
function hostnameText(d: RouteDetail) {
  if (d.hostnames.length) return d.hostnames.join(", ");
  return <Muted>{isChild(d) ? routeCopy.inheritsParent : routeCopy.inheritsListener}</Muted>;
}

/** Which parent routes may delegate here, from the spec's parentRefs (computed-rules §5.3). */
function permissionText(d: RouteDetail) {
  if (!d.parentRefs.length) return routeCopy.allowsAny;
  const routes = d.parentRefs.filter(isRouteRef).map((p) => `${p.namespace}/${p.name}`);
  return routes.length ? routeCopy.allows(routes) : routeCopy.allowsNone;
}

/** The route's one parent Gateway, for the strip's link; none when it has several, as the rejecting one is not named. */
function soleGateway(d: RouteDetail): LinkedRef | undefined {
  const gws = d.parentRefs.filter((p) => p.kind === "Gateway" && p.target).map((p) => p.target as LinkedRef);
  return gws.length === 1 ? gws[0] : undefined;
}

const refKey = (p: ParentRef) => [p.group, p.kind, p.namespace, p.name, p.sectionName, p.port].join("/");

/** Status entries' refs that no spec parentRef names, once each: a child's implicit parent (design §5.1). */
function unlistedRefs(d: RouteDetail): ParentRef[] {
  const out: ParentRef[] = [];
  for (const s of d.parents) {
    const p = s.parentRef;
    if (!d.parentRefs.some((r) => sameParentRef(r, p)) && !out.some((r) => sameParentRef(r, p))) out.push(p);
  }
  return out;
}

// region Component
interface RoutePageProps {
  stylesOverride?: SerializedStyles;
}

/** The refs a route's data links: spec and status parents, and up-chain steps. */
function linkedRefs(d: RouteDetail): LinkedRef[] {
  const out: LinkedRef[] = [];
  for (const p of d.parentRefs) if (p.target) out.push(p.target);
  for (const p of d.parents) out.push(p.target);
  for (const a of d.ancestors) for (const s of a.steps) if (s.ref) out.push(s.ref);
  return out;
}

/** One route of any kind, in the D-148 section order: where it sits, summary, rules, policies and child routes. */
export function RoutePage({ stylesOverride }: RoutePageProps = {}) {
  const id = useInstallId();
  const { group = "", kind = "", ns = "", name = "" } = useParams();
  const [params] = useSearchParams();
  const ref = useMemo<LinkedRef>(() => ({ group, kind, namespace: ns, name, link: "detail" }), [group, kind, ns, name]);
  const { data, error } = useRouteDetail(id, ref);
  const hasDelegation = !!data?.rules.some((r) => r.backendRefs.some((b) => b.delegation));
  // Delegation copy and blocks need a rule set that computes it and an HTTPRoute; the tab is the kgateway web pack's.
  const delegation = useInstallFeature(id, "delegation") !== false && kind === "HTTPRoute";
  // The detail says when it withholds delegation analysis (D-114); a child's paths and effective matches are part of it.
  const withheld = !!data?.delegationNotChecked;
  const notChecked = withheld && delegation && !!data && isChild(data);
  const unlisted = useMemo(() => (data ? unlistedRefs(data) : []), [data]);
  // The parents and up-chain this page links decide the links of the same objects in the trail.
  const trailRefs = useMemo(() => (data ? linkedRefs(data) : undefined), [data]);
  // ?rule= scrolls to its rule, so the Rules section is current and a section hash is not scrolled to.
  const ruleFocus = data && linkedRule(params, data.rules) !== undefined ? "route-rules" : undefined;

  const sections = useMemo<DetailSection[] | undefined>(() => {
    if (!data) return undefined;
    const parentCount = data.parentRefs.length + unlisted.length;
    const relations = data.policyRelations;
    const out: DetailSection[] = [
      {
        id: "route-serves",
        group: "where",
        title: traceCopy.serves,
        // No count while the viewer cannot see delegation and nothing is listed; none from an older server.
        count: !data.serves || (notChecked && !data.serves.length) ? undefined : data.serves.length,
        muted: !!data.serves && !data.serves.length && !notChecked,
        render: () => (
          <>
            <UpChain installId={id} self={ref} paths={data.ancestors} truncated={data.ancestorsTruncated} />
            {data.serves ? (
              <Serves installId={id} serves={data.serves} hostnames={kind !== "TCPRoute"} notChecked={notChecked} />
            ) : null}
          </>
        ),
      },
      {
        id: "route-parents",
        group: "where",
        title: routeCopy.parents,
        count: parentCount,
        muted: parentCount === 0,
        render: () =>
          parentCount ? (
            <Cards>
              {data.parentRefs.map((p, i) => (
                <ParentCard
                  key={refKey(p)}
                  installId={id}
                  parentRef={p}
                  entries={data.parents.filter((s) => sameParentRef(s.parentRef, p))}
                  generation={data.generation}
                  hasDelegation={hasDelegation}
                  attachment={data.attachments?.find((a) => a.index === i)?.result}
                />
              ))}
              {unlisted.map((p) => (
                <ParentCard
                  key={`status/${refKey(p)}`}
                  installId={id}
                  parentRef={p}
                  entries={data.parents.filter((s) => sameParentRef(s.parentRef, p))}
                  generation={data.generation}
                  hasDelegation={hasDelegation}
                  origin={data.parentRefs.length ? "unlisted" : "implicit"}
                />
              ))}
            </Cards>
          ) : (
            <Muted>{routeCopy.noParentRefs}</Muted>
          ),
      },
      {
        id: "route-summary",
        group: "status",
        title: detailCopy.summary,
        render: () => (
          <Facts>
            {/* A TCPRoute has no hostnames. */}
            {kind === "TCPRoute" ? null : (
              <>
                <dt>{routeCopy.hostnames}</dt>
                <dd>{hostnameText(data)}</dd>
              </>
            )}
            {delegation ? (
              <>
                <dt>{routeCopy.parentRoutes}</dt>
                <dd>{permissionText(data)}</dd>
              </>
            ) : null}
            <dt>{trafficCopy.title}</dt>
            <dd>
              <InlineLink to={`${installPath(id, "traffic")}?${new URLSearchParams({ route: refParam(ref) })}`}>
                {trafficCopy.routeEntry}
              </InlineLink>
            </dd>
          </Facts>
        ),
      },
      {
        id: "route-rules",
        group: "does",
        title: routeCopy.rules,
        count: data.rules.length,
        muted: !data.rules.length,
        render: () => (
          <DelegationNotCheckedView.Provider value={withheld}>
            {data.rules.length ? (
              <RuleTable installId={id} kind={kind} rules={data.rules} serves={data.serves} />
            ) : (
              <RuleList installId={id} kind={kind} rules={data.rules} serves={data.serves} />
            )}
          </DelegationNotCheckedView.Provider>
        ),
      },
    ];
    if (delegation && (data.effective?.length || notChecked))
      out.push({
        id: "route-effective",
        group: "does",
        title: effectiveCopy.title,
        count: notChecked ? undefined : (data.effective?.length ?? 0),
        render: () => (
          <EffectiveMatches
            installId={id}
            edges={data.effective ?? []}
            truncated={data.effectiveTruncated}
            notChecked={notChecked}
          />
        ),
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
    if (delegation)
      out.push({
        id: "child-routes",
        group: "related",
        title: routeCopy.childRoutes,
        count: data.childCount,
        muted: data.childCount === 0,
        render: () => <ChildRoutes installId={id} route={data.ref} expected={data.childCount} />,
      });
    return out;
  }, [data, unlisted, notChecked, withheld, delegation, hasDelegation, id, ref, kind]);

  return (
    <DetailFrame
      installId={id}
      refValue={ref}
      noun={kind}
      data={data}
      error={error}
      // Conditions live per parent; only this install's controller's entries count.
      problem={
        data && {
          condition: firstFailing(data.parents.filter((p) => p.ours).flatMap((p) => p.conditions)),
          conditionsId: "route-parents",
          gateway: soleGateway(data),
        }
      }
      sections={sections ?? []}
      sectionFocus={ruleFocus}
      trailRefs={trailRefs}
      stylesOverride={stylesOverride}
    />
  );
}

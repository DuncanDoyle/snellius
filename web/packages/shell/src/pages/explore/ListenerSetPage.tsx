import type { SerializedStyles } from "@emotion/react";
import styled from "@emotion/styled";
import { dump } from "js-yaml";
import { useMemo } from "react";
import { useParams } from "react-router-dom";
import { useListenerSetDetail } from "../../api/exploreHooks";
import type { AllowedListeners, Condition, LinkedRef } from "../../api/types";
import { detailCopy, gatewayCopy, listenerSetCopy } from "../../copy/explorer";
import { relationsCopy } from "../../copy/policies";
import { ConditionList } from "../../explore/ConditionList";
import { LinkedName } from "../../explore/LinkedName";
import { firstFailing } from "../../explore/ProblemStrip";
import { ResourceName } from "../../resource/ResourceName";
import { YamlBlock } from "../../resource/ResourceYaml";
import { useInstallId } from "../../shell/extension";
import { PolicyRelationsBody, policyRelationsCount } from "../policies/PolicyRelationsSection";
import type { DetailSection } from "./DetailSections";
import { DetailFrame, Facts } from "./detailParts";
import { ListenerTable } from "./gateway/ListenerTable";
import { Muted } from "./listParts";
import { AttachedRoutesBody, attachedRoutesTitle, FailedRoutesBody, failedRoutesCount } from "./RouteSections";

// region Styles
const Allowed = styled.div`
  margin-top: ${(p) => p.theme.space(2)};
  padding: ${(p) => p.theme.space(2)} ${(p) => p.theme.space(3)};
  border: 1px solid ${(p) => p.theme.color.border};
  border-radius: ${(p) => p.theme.radius.md};
  background: ${(p) => p.theme.color.surface};
  font-size: ${(p) => p.theme.font.size.sm};
  h3 {
    margin: 0 0 ${(p) => p.theme.space(1)};
    font-size: ${(p) => p.theme.font.size.sm};
    font-weight: ${(p) => p.theme.font.weight.semibold};
  }
  pre {
    padding: ${(p) => p.theme.space(2)};
  }
  p {
    margin: ${(p) => p.theme.space(1)} 0 0;
    line-height: 1.5;
  }
  code {
    font-family: ${(p) => p.theme.font.mono};
    font-size: ${(p) => p.theme.font.size.code};
  }
`;

// region Helpers
const isNotAllowed = (c: Condition) => c.type === "Accepted" && c.status === "False" && c.reason === "NotAllowed";

function AllowedListenersBlock({ a }: { a: AllowedListeners }) {
  return (
    <Allowed data-testid="allowed-listeners">
      <h3>
        {listenerSetCopy.allowedTitle} <code>{listenerSetCopy.allowedField}</code>
      </h3>
      {a.set ? (
        <YamlBlock>{dump(a.value ?? {}, { noRefs: true, lineWidth: 140 })}</YamlBlock>
      ) : (
        <p>{listenerSetCopy.allowedUnset}</p>
      )}
      {a.crdField === "missing" ? (
        <p data-testid="crd-missing">
          {listenerSetCopy.crdMissingBefore(a.crdChannel ?? "", a.crdBundleVersion ?? "")}{" "}
          <code>{listenerSetCopy.allowedField}</code> {listenerSetCopy.crdMissingAfter}
        </p>
      ) : a.crdHint && a.crdField !== "defined" ? (
        <p>
          {listenerSetCopy.crdHintBefore} <code>{listenerSetCopy.allowedField}</code> {listenerSetCopy.crdHintAfter}
        </p>
      ) : null}
    </Allowed>
  );
}

// region Component
interface ListenerSetPageProps {
  stylesOverride?: SerializedStyles;
}

/** One ListenerSet: its parent Gateway, conditions, listeners and routes (design §8.5). */
export function ListenerSetPage({ stylesOverride }: ListenerSetPageProps = {}) {
  const id = useInstallId();
  const { group = "", kind = "", ns = "", name = "" } = useParams();
  const ref = useMemo<LinkedRef>(() => ({ group, kind, namespace: ns, name, link: "detail" }), [group, kind, ns, name]);
  const { data, error } = useListenerSetDetail(id, ref);
  const allowed = data?.parentAllowedListeners;
  // The parent's server link decides its crumb in the trail.
  const parentRef = useMemo(() => (data ? [data.parent] : undefined), [data]);
  // parentFound is false for a hidden parent too, so the page never says it does not exist (D-013).
  const parentName = useMemo(
    () =>
      data ? (
        data.parentFound ? (
          <LinkedName refValue={data.parent} installId={id} />
        ) : (
          <>
            <ResourceName namespace={data.parent.namespace} name={data.parent.name} />{" "}
            <Muted>{listenerSetCopy.notAvailable}</Muted>
          </>
        )
      ) : null,
    [data, id],
  );
  const sections = useMemo<DetailSection[] | undefined>(() => {
    if (!data) return undefined;
    const relations = data.policyRelations;
    const out: DetailSection[] = [
      {
        id: "ls-parent",
        group: "where",
        title: listenerSetCopy.reachedThrough,
        render: () => (
          <Facts>
            <dt>{listenerSetCopy.parentGateway}</dt>
            <dd>{parentName}</dd>
          </Facts>
        ),
      },
      {
        id: "ls-conditions",
        group: "status",
        title: detailCopy.conditions,
        count: data.conditions.length,
        muted: !data.conditions.length,
        render: () => (
          <ConditionList
            conditions={data.conditions}
            generation={data.generation}
            empty={data.experimentalHint}
            extra={(c) =>
              data.parentFound && allowed && isNotAllowed(c) ? <AllowedListenersBlock a={allowed} /> : null
            }
          />
        ),
      },
      {
        id: "ls-listeners",
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
        id: "attached-routes",
        group: "does",
        title: attachedRoutesTitle(),
        count: data.routeCounts.accepted,
        muted: data.routeCounts.accepted === 0,
        render: () => <AttachedRoutesBody installId={id} parent={data.ref} counts={data.routeCounts} />,
      },
      {
        id: "failed-routes",
        group: "does",
        title: gatewayCopy.notAttached,
        count: failedRoutesCount(data.routeCounts),
        muted: failedRoutesCount(data.routeCounts) === 0,
        render: () => <FailedRoutesBody installId={id} parent={data.ref} counts={data.routeCounts} />,
      },
    ];
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
  }, [data, parentName, allowed, id, ref]);
  return (
    <DetailFrame
      trailRefs={parentRef}
      installId={id}
      refValue={ref}
      noun="ListenerSet"
      data={data}
      error={error}
      // The fix for a rejected ListenerSet is often on its parent Gateway, so the strip names it.
      problem={
        data && {
          condition: firstFailing(data.conditions),
          conditionsId: "ls-conditions",
          parent: { label: listenerSetCopy.parentGateway, name: parentName },
        }
      }
      sections={sections ?? []}
      stylesOverride={stylesOverride}
    />
  );
}

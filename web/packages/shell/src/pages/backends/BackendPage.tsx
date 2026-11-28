import type { SerializedStyles } from "@emotion/react";
import { useMemo } from "react";
import { useParams } from "react-router-dom";
import { useBackendDetail } from "../../api/exploreHooks";
import type { BackendDetail, LinkedRef } from "../../api/types";
import { explanationCopy, exposureCopy, factCopy, portChainCopy } from "../../copy/backends";
import { detailCopy } from "../../copy/explorer";
import { relationsCopy } from "../../copy/policies";
import { pathColumnCopy } from "../../copy/traffic";
import { ConditionList } from "../../explore/ConditionList";
import { useInstallId, useVocabulary } from "../../shell/extension";
import type { DetailSection } from "../explore/DetailSections";
import { DetailFrame, Facts } from "../explore/detailParts";
import { PolicyRelationsBody, policyRelationsCount } from "../policies/PolicyRelationsSection";
import { EndpointsSection } from "./EndpointsSection";
import { Explanations } from "./Explanations";
import { ConsumersBody, ExposureSection } from "./ExposureSection";
import { PortChain } from "./PortChain";
import { StatusLine } from "./StatusLine";

// region Helpers
const isService = (r: { group: string; kind: string }) => r.group === "" && r.kind === "Service";

/** The refs the page links, for the trail: the routes and Gateways of its paths, and its consumers. */
function linkedRefs(d: BackendDetail): LinkedRef[] {
  const out: LinkedRef[] = [];
  for (const r of d.exposure.rows) out.push(r.gateway, ...r.routes);
  for (const c of d.consumers) out.push(c.consumer);
  return out;
}

// region Component
interface BackendPageProps {
  stylesOverride?: SerializedStyles;
}

/**
 * How one backend is exposed (Phase 6 design §8.2): the impact, its paths and other users, endpoints per port, the
 * port chain, why refs to it do not resolve, and its conditions or cached Service facts. A Service has no YAML tab:
 * the cache keeps its facts, not the object.
 */
export function BackendPage({ stylesOverride }: BackendPageProps = {}) {
  const id = useInstallId();
  const vocab = useVocabulary();
  const { group = "", kind = "", ns = "", name = "" } = useParams();
  const g = group === "-" ? "" : group;
  const ref = useMemo<LinkedRef>(() => ({ group: g, kind, namespace: ns, name, link: "none" }), [g, kind, ns, name]);
  const { data, error, mutate } = useBackendDetail(id, ref);
  const frameData = useMemo(
    () => (data ? { health: data.problem.state, collectedAt: data.collectedAt } : undefined),
    [data],
  );
  const trailRefs = useMemo(() => (data ? linkedRefs(data) : undefined), [data]);
  const service = isService(ref);
  const typeLabel = vocab.backendKinds.find((k) => k.group === g && k.kind === kind)?.typeLabel;
  const generation = Math.max(0, ...(data?.conditions ?? []).map((c) => c.observedGeneration ?? 0));
  // Counts come from the detail response only; the endpoints list has no total of its own, so it gets no badge.
  const sections = useMemo<DetailSection[] | undefined>(() => {
    if (!data) return undefined;
    const out: DetailSection[] = [
      {
        id: "backend-facts",
        group: "status",
        title: service ? factCopy.service : factCopy.facts(kind),
        render: () => (
          <Facts>
            <dt>{factCopy.kind}</dt>
            <dd>{data.ref.kind}</dd>
            {data.type ? (
              <>
                <dt>{typeLabel ?? factCopy.type}</dt>
                <dd>{data.type}</dd>
              </>
            ) : null}
            {data.service ? (
              <>
                <dt>{factCopy.serviceType}</dt>
                <dd>{data.service.type}</dd>
                <dt>{factCopy.ports}</dt>
                <dd>{data.service.ports.map((p) => (p.name ? `${p.port} (${p.name})` : `${p.port}`)).join(", ")}</dd>
              </>
            ) : null}
          </Facts>
        ),
      },
    ];
    if (!service) {
      const n = data.conditions?.length ?? 0;
      out.push({
        id: "backend-conditions",
        group: "status",
        title: detailCopy.conditions,
        count: n,
        muted: n === 0,
        render: () => <ConditionList conditions={data.conditions ?? []} generation={generation} />,
      });
    }
    if (data.portChain.length)
      out.push({
        id: "backend-ports",
        group: "does",
        title: portChainCopy.title,
        count: data.portChain.length,
        render: () => <PortChain chains={data.portChain} />,
      });
    out.push({
      id: "backend-endpoints",
      group: "does",
      title: pathColumnCopy.endpoints,
      render: () => (
        <EndpointsSection
          sources={data.sources}
          noSuchPorts={data.portChain.filter((c) => c.noSuchPort).map((c) => c.refPort)}
          onRetry={() => mutate()}
        />
      ),
    });
    if (data.explanations.length)
      out.push({
        id: "backend-explanations",
        group: "does",
        title: explanationCopy.title,
        count: data.explanations.length,
        render: () => <Explanations installId={id} explanations={data.explanations} />,
      });
    // An older server sends no relations: no Policies section.
    if (data.policyRelations) {
      const relations = data.policyRelations;
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
    out.push({
      id: "backend-exposure",
      group: "related",
      title: exposureCopy.title,
      count: data.exposure.summary.paths,
      muted: data.exposure.summary.paths === 0,
      render: () => <ExposureSection installId={id} backend={ref} exposure={data.exposure} />,
    });
    if (data.consumers.length)
      out.push({
        id: "backend-consumers",
        group: "related",
        title: exposureCopy.others,
        count: data.consumers.length,
        render: () => <ConsumersBody installId={id} consumers={data.consumers} />,
      });
    return out;
  }, [data, id, ref, kind, service, typeLabel, generation, mutate]);
  return (
    <DetailFrame
      installId={id}
      refValue={ref}
      noun={kind}
      data={frameData}
      error={error}
      // The status line is this page's problem strip: it renders above the sections.
      ownProblem
      banner={data ? <StatusLine detail={data} /> : undefined}
      sections={sections ?? []}
      trailRefs={trailRefs}
      yaml={!service}
      stylesOverride={stylesOverride}
    />
  );
}

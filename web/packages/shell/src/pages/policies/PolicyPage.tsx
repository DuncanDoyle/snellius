import type { SerializedStyles } from "@emotion/react";
import styled from "@emotion/styled";
import { dump } from "js-yaml";
import { useMemo } from "react";
import { useParams } from "react-router-dom";
import { usePolicyDetail } from "../../api/exploreHooks";
import type {
  Condition,
  LinkedRef,
  PolicyAncestor,
  PolicyDetail,
  PolicyFeature,
  PolicySource,
  PolicyTarget,
} from "../../api/types";
import {
  ancestorText,
  attachedCopy,
  checkCopy,
  notCurrentCopy,
  policiesCopy,
  policyDetailCopy,
  readingNoteCopy,
} from "../../copy/policies";
import { joinSentences } from "../../explore/ComputedBadge";
import { ConditionList, conditionState } from "../../explore/ConditionList";
import { LinkedName } from "../../explore/LinkedName";
import { SourceTag } from "../../explore/SourceTag";
import { HealthBadge } from "../../health/HealthBadge";
import { HealthIcon } from "../../health/HealthIcon";
import { YamlBlock } from "../../resource/ResourceYaml";
import { useInstallId } from "../../shell/extension";
import type { HealthState } from "../../theme/tokens";
import type { DetailSection } from "../explore/DetailSections";
import { DetailFrame, Facts } from "../explore/detailParts";
import { acceptedState, acceptedText, checkState, featureName, targetText } from "./policyText";

// region Styles
/** A value and the tags after it, wrapping as one line. */
const Line = styled.span`
  display: inline-flex;
  flex-wrap: wrap;
  align-items: center;
  gap: ${(p) => p.theme.space(1)} ${(p) => p.theme.space(2)};
  min-width: 0;
  overflow-wrap: anywhere;
`;
const List = styled.ul`
  margin: 0;
  padding: 0;
  list-style: none;
  display: grid;
  gap: ${(p) => p.theme.space(2)};
`;
const Card = styled.li`
  display: grid;
  gap: ${(p) => p.theme.space(2)};
  padding: ${(p) => p.theme.space(3)};
  border: 1px solid ${(p) => p.theme.color.border};
  border-radius: ${(p) => p.theme.radius.md};
  background: ${(p) => p.theme.color.surface};
  min-width: 0;
  .head {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: ${(p) => p.theme.space(1)} ${(p) => p.theme.space(2)};
    font-weight: ${(p) => p.theme.font.weight.semibold};
  }
  .muted,
  .note {
    color: ${(p) => p.theme.color.textMuted};
    font-size: ${(p) => p.theme.font.size.sm};
    font-weight: ${(p) => p.theme.font.weight.regular};
    overflow-wrap: anywhere;
  }
  code {
    font-family: ${(p) => p.theme.font.mono};
    font-size: ${(p) => p.theme.font.size.code};
  }
  h3 {
    margin: 0;
    font-size: ${(p) => p.theme.font.size.base};
  }
`;
/** Marks an entry left out of the summaries; text and border, never colour alone. */
const Mark = styled.span`
  display: inline-flex;
  align-items: center;
  height: 20px;
  padding: 0 6px;
  border: 1px solid ${(p) => p.theme.color.status.warning.fg};
  border-radius: ${(p) => p.theme.radius.sm};
  color: ${(p) => p.theme.color.status.warning.fg};
  font-size: ${(p) => p.theme.font.size.xs};
  font-weight: ${(p) => p.theme.font.weight.semibold};
`;
const None = styled.p`
  margin: 0;
  color: ${(p) => p.theme.color.textMuted};
`;
const Note = styled.p`
  margin: 0;
  color: ${(p) => p.theme.color.textMuted};
  font-size: ${(p) => p.theme.font.size.sm};
`;
const Yaml = styled(YamlBlock)`
  padding: ${(p) => p.theme.space(3)};
`;

// region Helpers
/** The refs the page links, for the trail: targets and ancestors. */
function linkedRefs(d: PolicyDetail): LinkedRef[] {
  return [...d.targets.flatMap((t) => (t.ref ? [t.ref] : [])), ...d.ancestors.map((a) => a.ref)];
}

// region Parts
/** An object's health from the explore index, with its source; shown only when the viewer may get the object. */
function HealthRow({ state, source }: { state?: HealthState; source?: PolicySource }) {
  if (!state) return null;
  return (
    <>
      <dt>{policyDetailCopy.health}</dt>
      <dd>
        <Line>
          <HealthBadge state={state} />
          {source ? <SourceTag source={source} /> : null}
        </Line>
      </dd>
    </>
  );
}

function TargetItem({ installId, t }: { installId: string; t: PolicyTarget }) {
  return (
    <Card data-testid="policy-target">
      <span className="head">
        {t.ref && !t.selector ? (
          <Line>
            {t.kind} <LinkedName refValue={t.ref} installId={installId} />
            {t.sectionName ? <span className="muted">{`(section ${t.sectionName})`}</span> : null}
          </Line>
        ) : (
          <span>{targetText(t)}</span>
        )}
        <SourceTag source={t.source} />
      </span>
      {t.check || t.health ? (
        <Facts>
          {t.check ? (
            <>
              <dt>{policyDetailCopy.check}</dt>
              <dd>
                <Line data-testid="target-check">
                  <HealthIcon state={checkState(t)} />
                  {checkCopy[t.check.outcome]}
                  <SourceTag source={t.check.source} />
                </Line>
              </dd>
            </>
          ) : null}
          <HealthRow state={t.health} source={t.healthSource} />
        </Facts>
      ) : null}
    </Card>
  );
}

function AncestorItem({ installId, a, generation }: { installId: string; a: PolicyAncestor; generation: number }) {
  const reading = (c: Condition) => a.readings.find((r) => r.type === c.type);
  return (
    <Card data-testid="policy-ancestor">
      <span className="head">
        {a.summary ? (
          <Line>
            {policyDetailCopy.summaryEntry}
            <SourceTag source={a.summarySource} />
          </Line>
        ) : (
          <Line>
            {a.ref.kind} <LinkedName refValue={a.ref} installId={installId} />
            {a.sectionName ? <span className="muted">{`(section ${a.sectionName})`}</span> : null}
            {!a.sectionName && a.port ? <span className="muted">{`(port ${a.port})`}</span> : null}
          </Line>
        )}
        {a.summary ? null : <SourceTag source={a.source} />}
      </span>
      {a.summary ? <Note>{policyDetailCopy.summaryEntryTip}</Note> : null}
      <Facts>
        <dt>{policyDetailCopy.controller}</dt>
        <dd>
          <Line>
            <code>{a.controller}</code>
            {/* The summary entry's head carries the pack's reading, so the reported tag sits here. */}
            {a.summary ? <SourceTag source={a.source} /> : null}
          </Line>
        </dd>
        <HealthRow state={a.health} source={a.healthSource} />
        {a.notCurrent ? (
          <>
            <dt>{policyDetailCopy.summaries}</dt>
            <dd>
              <Line>
                <Mark data-testid="not-current">{notCurrentCopy[a.notCurrent] ?? a.notCurrent}</Mark>
                <span className="muted">{joinSentences([...(a.why ? [a.why] : []), policyDetailCopy.leftOut])}</span>
                <SourceTag source={a.currentSource} />
              </Line>
            </dd>
          </>
        ) : null}
      </Facts>
      <ConditionList
        conditions={a.conditions}
        generation={generation}
        stateOf={(c) => (reading(c)?.warning && !c.stale ? "warning" : conditionState(c))}
        staleExtra={<SourceTag source={a.staleSource} />}
        extra={(c) => {
          const r = reading(c);
          return r?.note ? (
            <Line className="note" data-testid="reading-note">
              {readingNoteCopy[r.note] ?? r.note}
              <SourceTag source={r.source} />
            </Line>
          ) : null;
        }}
      />
    </Card>
  );
}

function FeatureItem({ f, raw }: { f: PolicyFeature; raw: boolean }) {
  const yaml = useMemo(() => dump(f.value ?? null, { noRefs: true, lineWidth: 140 }), [f.value]);
  return (
    <Card data-testid="policy-feature">
      <span className="head">
        <h3>{featureName(f)}</h3>
        {f.label && !raw ? <code className="muted">{f.key}</code> : null}
        <SourceTag source={f.source} />
      </span>
      <Yaml data-testid="feature-yaml">{yaml}</Yaml>
    </Card>
  );
}

// region Component
interface PolicyPageProps {
  stylesOverride?: SerializedStyles;
}

/**
 * One policy as declared and as its status reports it (Phase 8 design §6.3): targets with their checks, status per
 * ancestor verbatim, configuration by feature and precedence annotations. Every value shows its source.
 */
export function PolicyPage({ stylesOverride }: PolicyPageProps = {}) {
  const id = useInstallId();
  const { group = "", kind = "", ns = "", name = "" } = useParams();
  const g = group === "-" ? "" : group;
  const ref = useMemo<LinkedRef>(() => ({ group: g, kind, namespace: ns, name, link: "none" }), [g, kind, ns, name]);
  const { data, error } = usePolicyDetail(id, ref);
  const frameData = useMemo(() => (data ? { collectedAt: data.collectedAt } : undefined), [data]);
  const trailRefs = useMemo(() => (data ? linkedRefs(data) : undefined), [data]);
  const sections = useMemo<DetailSection[] | undefined>(() => {
    if (!data) return undefined;
    const out: DetailSection[] = [
      {
        id: "policy-summary",
        group: "status",
        title: policyDetailCopy.summary,
        render: () => (
          <Facts>
            <dt>{policyDetailCopy.accepted}</dt>
            <dd>
              <Line data-testid="policy-accepted">
                <HealthIcon state={acceptedState(data.accepted)} />
                {acceptedText(data.accepted)}
                {data.acceptedSource ? <SourceTag source={data.acceptedSource} /> : null}
              </Line>
            </dd>
            <dt>{policyDetailCopy.attachedOn}</dt>
            <dd>
              <Line data-testid="policy-attached-on">
                {data.attachedOn.length ? data.attachedOn.map(ancestorText).join(" · ") : attachedCopy.none}
                {data.attachedOnSource ? <SourceTag source={data.attachedOnSource} /> : null}
              </Line>
            </dd>
            <dt>{policyDetailCopy.problems}</dt>
            <dd>
              <Line data-testid="policy-problems">
                {data.problems.has ? policyDetailCopy.yes : policyDetailCopy.no}
                <SourceTag source={data.problems.source} />
              </Line>
            </dd>
          </Facts>
        ),
      },
      {
        id: "policy-status",
        group: "status",
        title: policyDetailCopy.status,
        count: data.ancestors.length,
        muted: !data.ancestors.length,
        render: () =>
          data.ancestors.length ? (
            <List>
              {data.ancestors.map((a) => (
                <AncestorItem
                  // Two entries may name one ancestor, under different controllers.
                  key={`${a.controller}|${a.ref.kind}|${a.ref.namespace ?? ""}|${a.ref.name}|${a.sectionName ?? ""}|${a.port ?? ""}`}
                  installId={id}
                  a={a}
                  generation={data.generation}
                />
              ))}
            </List>
          ) : (
            <None>{policyDetailCopy.noStatus}</None>
          ),
      },
      {
        id: "policy-targets",
        group: "does",
        title: policyDetailCopy.targets,
        count: data.targets.length,
        muted: !data.targets.length,
        render: () =>
          data.targets.length ? (
            <List>
              {data.targets.map((t) => (
                <TargetItem key={t.index} installId={id} t={t} />
              ))}
            </List>
          ) : (
            <None>{policyDetailCopy.noTargets}</None>
          ),
      },
      {
        id: "policy-config",
        group: "does",
        title: policyDetailCopy.configuration,
        count: data.features.length,
        muted: !data.features.length,
        render: () => (
          <>
            {data.interpreted ? null : <Note>{policyDetailCopy.rawKeys}</Note>}
            {data.features.length ? (
              <List>
                {data.features.map((f) => (
                  <FeatureItem key={f.key} f={f} raw={!data.interpreted} />
                ))}
              </List>
            ) : (
              <None>{policyDetailCopy.noFeatures}</None>
            )}
          </>
        ),
      },
    ];
    if (data.annotations.length)
      out.push({
        id: "policy-annotations",
        group: "related",
        title: policyDetailCopy.annotations,
        count: data.annotations.length,
        render: () => (
          <List>
            {data.annotations.map((a) => (
              <Card key={a.key} data-testid="policy-annotation">
                <Line>
                  <code>{`${a.key}: ${a.value}`}</code>
                  <SourceTag source={a.source} />
                </Line>
                <Note>{policyDetailCopy.annotationNote}</Note>
              </Card>
            ))}
          </List>
        ),
      });
    return out;
  }, [data, id]);
  return (
    <DetailFrame
      installId={id}
      refValue={ref}
      noun={policiesCopy.noun[0]}
      data={frameData}
      error={error}
      ownProblem
      sections={sections ?? []}
      trailRefs={trailRefs}
      stylesOverride={stylesOverride}
    />
  );
}

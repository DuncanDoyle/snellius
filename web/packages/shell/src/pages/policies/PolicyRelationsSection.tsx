import type { SerializedStyles } from "@emotion/react";
import styled from "@emotion/styled";
import type { LinkedRef, PolicyRelation, PolicyRelationHop, PolicyRelations } from "../../api/types";
import { readingNoteCopy, relationsCopy } from "../../copy/policies";
import { conditionState } from "../../explore/ConditionList";
import { LinkedName } from "../../explore/LinkedName";
import { SourceTag } from "../../explore/SourceTag";
import { HealthIcon } from "../../health/HealthIcon";
import { installPath } from "../../shell/paths";
import { InlineLink } from "../../states/controls";
import { Section } from "../explore/detailParts";
import { kindValue } from "./policyText";

// region Styles
const Block = styled(Section)<{ stylesOverride?: SerializedStyles }>`
  h3 {
    margin: ${(p) => p.theme.space(3)} 0 ${(p) => p.theme.space(1)};
    font-size: ${(p) => p.theme.font.size.md};
    font-weight: ${(p) => p.theme.font.weight.semibold};
  }
  ${(p) => p.stylesOverride}
`;
const Note = styled.p`
  margin: 0 0 ${(p) => p.theme.space(1)};
  color: ${(p) => p.theme.color.textMuted};
  font-size: ${(p) => p.theme.font.size.sm};
`;
const Items = styled.ul`
  margin: 0;
  padding: 0;
  list-style: none;
  border: 1px solid ${(p) => p.theme.color.border};
  border-radius: ${(p) => p.theme.radius.md};
  background: ${(p) => p.theme.color.surface};
  li {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: ${(p) => p.theme.space(1)} ${(p) => p.theme.space(2)};
    min-width: 0;
    padding: ${(p) => p.theme.space(2)} ${(p) => p.theme.space(3)};
    overflow-wrap: anywhere;
  }
  li + li {
    border-top: 1px solid ${(p) => p.theme.color.border};
  }
  .kind {
    color: ${(p) => p.theme.color.textMuted};
  }
  .muted {
    color: ${(p) => p.theme.color.textMuted};
    font-size: ${(p) => p.theme.font.size.sm};
  }
  .reason {
    font-family: ${(p) => p.theme.font.mono};
    font-size: ${(p) => p.theme.font.size.code};
    color: ${(p) => p.theme.color.textMuted};
  }
  .note {
    flex-basis: 100%;
    color: ${(p) => p.theme.color.textMuted};
    font-size: ${(p) => p.theme.font.size.sm};
  }
`;
const SelectorNote = styled(Note)`
  margin-top: ${(p) => p.theme.space(3)};
`;
/** Underlined, so the link in the sentence does not rely on colour (WCAG 1.4.1). */
const NoteLink = styled(InlineLink)`
  text-decoration: underline;
`;
const None = styled.p`
  margin: 0;
  color: ${(p) => p.theme.color.textMuted};
`;

// region Helpers
const at = (x: { sectionName?: string; port?: number }) =>
  x.sectionName ? relationsCopy.section(x.sectionName) : x.port ? relationsCopy.port(x.port) : "";

const hopText = (h: PolicyRelationHop) => {
  const where = at(h);
  return where ? ` (${where})` : "";
};

/** A ListenerSet kind's subject noun for the parent note; anything else here is a route. */
const parentNoun = (subject: LinkedRef) => (subject.kind.endsWith("ListenerSet") ? "ListenerSet" : "route");

/** The Policies list filtered to the namespace and the kinds that select by label (design §5.4). */
function selectorsLink(installId: string, r: PolicyRelations): string {
  const q = new URLSearchParams({ namespace: r.selectors.namespace });
  if (r.selectors.kinds.length) q.set("kind", r.selectors.kinds.map(kindValue).join(","));
  return `${installPath(installId, "policies")}?${q}`;
}

// region Parts
function Item({ installId, r }: { installId: string; r: PolicyRelation }) {
  const where = at(r);
  const c = r.condition;
  return (
    <li data-testid="policy-relation">
      <span className="kind">{r.policy.kind}</span>
      {/* Opens where the API's link says; a policy the viewer may not open by name is text (8.2 review #3). */}
      <LinkedName refValue={r.policy} installId={installId} />
      {where ? <span className="muted">({where})</span> : null}
      {r.via?.length ? (
        <span className="muted" data-testid="relation-via">
          {relationsCopy.via}{" "}
          {r.via.map((h, i) => (
            <span key={`${h.ref.kind}/${h.ref.namespace ?? ""}/${h.ref.name}`}>
              {i ? " → " : ""}
              {h.ref.kind} <LinkedName refValue={h.ref} installId={installId} />
              {hopText(h)}
            </span>
          ))}
        </span>
      ) : null}
      {c ? (
        <span className="muted" data-testid="relation-condition">
          <HealthIcon state={conditionState(c)} size={14} /> {`${c.type}: ${c.status}`}{" "}
          {c.reason ? <span className="reason">{c.reason}</span> : null}
        </span>
      ) : null}
      <SourceTag source={r.source} />
      {r.note ? (
        <span className="note" data-testid="relation-note">
          {readingNoteCopy[r.note] ?? r.note} {r.noteSource ? <SourceTag source={r.noteSource} /> : null}
        </span>
      ) : null}
    </li>
  );
}

function Group({
  installId,
  id,
  title,
  note,
  items,
}: {
  installId: string;
  id: string;
  title: string;
  note?: string;
  items: PolicyRelation[];
}) {
  return (
    <div data-testid={`relations-${id}`}>
      <h3 id={`policy-rel-${id}`}>{title}</h3>
      {note ? <Note>{note}</Note> : null}
      {items.length ? (
        <Items aria-labelledby={`policy-rel-${id}`}>
          {items.map((r) => (
            <Item
              key={`${r.policy.group}/${r.policy.kind}/${r.policy.namespace ?? ""}/${r.policy.name}/${at(r)}/${(r.via ?? []).map((h) => h.ref.name).join(">")}`}
              installId={installId}
              r={r}
            />
          ))}
        </Items>
      ) : (
        <None>{relationsCopy.none}</None>
      )}
    </div>
  );
}

// region Component
interface PolicyRelationsSectionProps {
  installId: string;
  /** The page's object, for the parent note's noun. */
  subject: LinkedRef;
  relations?: PolicyRelations;
  stylesOverride?: SerializedStyles;
}

/**
 * The policies related to a Gateway, ListenerSet, route or backend (design §6.5): those targeting it, those naming
 * a parent it declares, and those kgateway reports attached here. Groups the response leaves out are not shown.
 */
export function PolicyRelationsSection({ installId, subject, relations, stylesOverride }: PolicyRelationsSectionProps) {
  // An older server sends no relations.
  if (!relations) return null;
  return (
    <Block aria-labelledby="policy-relations" data-testid="policy-relations" stylesOverride={stylesOverride}>
      <h2 id="policy-relations">{relationsCopy.title}</h2>
      <Group installId={installId} id="targeting" title={relationsCopy.targeting} items={relations.targeting} />
      {relations.targetingParent ? (
        <Group
          installId={installId}
          id="targeting-parent"
          title={relationsCopy.targetingParent}
          note={relationsCopy.parentNote(parentNoun(subject))}
          items={relations.targetingParent}
        />
      ) : null}
      {relations.attachedHere ? (
        <Group
          installId={installId}
          id="attached-here"
          title={relationsCopy.attachedHere}
          items={relations.attachedHere}
        />
      ) : null}
      <SelectorNote data-testid="relations-selectors">
        {relationsCopy.selectorNote}{" "}
        <NoteLink to={selectorsLink(installId, relations)}>
          {relationsCopy.selectorLink(relations.selectors.namespace)}
        </NoteLink>
      </SelectorNote>
    </Block>
  );
}

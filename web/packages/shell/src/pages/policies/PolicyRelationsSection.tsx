import styled from "@emotion/styled";
import type { LinkedRef, PolicyRelation, PolicyRelationHop, PolicyRelations } from "../../api/types";
import { readingNoteCopy, relationsCopy } from "../../copy/policies";
import { conditionState } from "../../explore/ConditionList";
import { LinkedName } from "../../explore/LinkedName";
import { SourceTag } from "../../explore/SourceTag";
import { HealthIcon } from "../../health/HealthIcon";
import { installPath } from "../../shell/paths";
import { InlineLink } from "../../states/controls";
import { kindValue } from "./policyText";

// region Styles
const GroupTitle = styled.h3`
  margin: ${(p) => p.theme.space(3)} 0 ${(p) => p.theme.space(1)};
  font-size: ${(p) => p.theme.font.size.md};
  font-weight: ${(p) => p.theme.font.weight.semibold};
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
function Item({ installId, r, hideSection }: { installId: string; r: PolicyRelation; hideSection?: boolean }) {
  const where = hideSection ? "" : at(r);
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

interface GroupProps {
  installId: string;
  id: string;
  title: string;
  note?: string;
  items: PolicyRelation[];
  /** Leaves out each item's section, where the context already names it. */
  hideSection?: boolean;
  "data-testid"?: string;
}

/** One relation group: a title and its policies, or "None.". Listener rows reuse it for their section's policies. */
export function PolicyRelationGroup({ installId, id, title, note, items, hideSection, ...rest }: GroupProps) {
  return (
    <div data-testid={rest["data-testid"] ?? `relations-${id}`}>
      <GroupTitle id={`policy-rel-${id}`}>{title}</GroupTitle>
      {note ? <Note>{note}</Note> : null}
      {items.length ? (
        <Items aria-labelledby={`policy-rel-${id}`}>
          {items.map((r) => (
            <Item
              key={`${r.policy.group}/${r.policy.kind}/${r.policy.namespace ?? ""}/${r.policy.name}/${at(r)}/${(r.via ?? []).map((h) => h.ref.name).join(">")}`}
              installId={installId}
              r={r}
              hideSection={hideSection}
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
/** The number of policy relations listed, over every group the response sends. */
export function policyRelationsCount(r: PolicyRelations): number {
  return r.targeting.length + (r.targetingParent?.length ?? 0) + (r.attachedHere?.length ?? 0);
}

interface PolicyRelationsBodyProps {
  installId: string;
  /** The page's object, for the parent note's noun. */
  subject: LinkedRef;
  relations: PolicyRelations;
}

/** The relation groups and the selector note, without a section or heading: a section card's body. */
export function PolicyRelationsBody({ installId, subject, relations }: PolicyRelationsBodyProps) {
  return (
    <div data-testid="policy-relations">
      <PolicyRelationGroup
        installId={installId}
        id="targeting"
        title={relationsCopy.targeting}
        items={relations.targeting}
      />
      {relations.targetingParent ? (
        <PolicyRelationGroup
          installId={installId}
          id="targeting-parent"
          title={relationsCopy.targetingParent}
          note={relationsCopy.parentNote(parentNoun(subject))}
          items={relations.targetingParent}
        />
      ) : null}
      {relations.attachedHere ? (
        <PolicyRelationGroup
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
    </div>
  );
}

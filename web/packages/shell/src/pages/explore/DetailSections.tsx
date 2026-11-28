import { css } from "@emotion/react";
import styled from "@emotion/styled";
import { ChevronDown } from "lucide-react";
import { type MouseEvent, type ReactNode, useEffect, useId, useMemo, useRef, useState } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { sectionsCopy } from "../../copy/explorer";
import { CHIP_PRESSED_ACTIVE_PCT, CHIP_PRESSED_HOVER_PCT, CHIP_PRESSED_PCT } from "../../shell/constants";
import { useMedia } from "../../shell/useMedia";
import { Shimmer } from "../../states/Shimmer";

// region Constants
/** The page's groups, in the order every detail page shows them (design §5). */
export const SECTION_GROUPS = ["where", "status", "does", "runtime", "actsOn", "related"] as const;
export type SectionGroup = (typeof SECTION_GROUPS)[number];

/** Nav entry tints: hover is the pressed tint at half strength; contrast-tested in DetailSections.test.tsx. */
export const NAV_HOVER_PCT = 6;
export const NAV_PRESS_PCT = 12;

/** Below this width the nav folds into the "Sections" control above the cards. */
const NARROW_BELOW = 960;
// Fits the common titles with their counts; longer ones are cut and show in full on hover.
const NAV_W = 176;
const BAR_H = 48;
// The frame sets --detail-sticky-top to what stays fixed above the nav; nothing does today, so it defaults to 0.
const STICKY_TOP = "var(--detail-sticky-top, 0px)";

/** Scroll margin that keeps an anchor below the sticky offset and the narrow "Sections" bar; cards and ?rule= rows use it. */
export const scrollClearance = (space: (n: number) => string) =>
  `calc(${STICKY_TOP} + var(--detail-sections-bar, 0px) + ${space(4)})`;

// region Styles
const Layout = styled.div<{ narrow: boolean }>`
  display: grid;
  grid-template-columns: ${(p) => (p.narrow ? "minmax(0, 1fr)" : `${NAV_W}px minmax(0, 1fr)`)};
  gap: ${(p) => p.theme.space(4)} ${(p) => p.theme.space(5)};
  align-items: start;
  --detail-sections-bar: ${(p) => (p.narrow ? `${BAR_H}px` : "0px")};
`;
const Rail = styled.div`
  position: sticky;
  top: calc(${STICKY_TOP} + ${(p) => p.theme.space(4)});
  max-height: calc(100vh - ${STICKY_TOP} - ${(p) => p.theme.space(8)});
  overflow-y: auto;
  --nav-base: ${(p) => p.theme.color.bg};
`;
const Bar = styled.div`
  position: sticky;
  top: ${STICKY_TOP};
  z-index: 2;
  display: flex;
  align-items: center;
  height: ${BAR_H}px;
  box-sizing: border-box;
  background: ${(p) => p.theme.color.bg};
`;
const Toggle = styled.button`
  all: unset;
  box-sizing: border-box;
  display: inline-flex;
  align-items: center;
  gap: ${(p) => p.theme.space(2)};
  height: 36px;
  padding: 0 ${(p) => p.theme.space(3)};
  border: 1px solid ${(p) => p.theme.color.border};
  border-radius: ${(p) => p.theme.radius.md};
  background: ${(p) => p.theme.color.surface};
  color: ${(p) => p.theme.color.text};
  font-size: ${(p) => p.theme.font.size.md};
  cursor: pointer;
  &:hover {
    background: color-mix(in srgb, ${(p) => p.theme.color.text} 7%, ${(p) => p.theme.color.surface});
  }
  &:active {
    background: color-mix(in srgb, ${(p) => p.theme.color.text} 14%, ${(p) => p.theme.color.surface});
  }
  &:focus-visible {
    outline: 2px solid ${(p) => p.theme.color.focus};
    outline-offset: 2px;
  }
  &[aria-expanded="true"] svg {
    transform: rotate(180deg);
  }
`;
const Panel = styled.div`
  position: absolute;
  top: calc(100% - 4px);
  left: 0;
  width: min(320px, 100%);
  max-height: 60vh;
  overflow-y: auto;
  box-sizing: border-box;
  padding: ${(p) => p.theme.space(2)};
  background: ${(p) => p.theme.color.surfaceRaised};
  border: 1px solid ${(p) => p.theme.color.border};
  border-radius: ${(p) => p.theme.radius.md};
  box-shadow: 0 6px 20px rgb(0 0 0 / 0.25);
  --nav-base: ${(p) => p.theme.color.surfaceRaised};
`;
const NavRoot = styled.nav`
  display: grid;
  gap: ${(p) => p.theme.space(3)};
  font-size: ${(p) => p.theme.font.size.md};
  .group {
    padding: 0 ${(p) => p.theme.space(2)};
    margin-bottom: ${(p) => p.theme.space(1)};
    color: ${(p) => p.theme.color.textMuted};
    font-size: ${(p) => p.theme.font.size.xs};
    font-weight: ${(p) => p.theme.font.weight.semibold};
    letter-spacing: 0.04em;
    text-transform: uppercase;
  }
  /* Tracks may shrink below the longest label, so titles cut instead of overflowing the column. */
  &,
  ul {
    grid-template-columns: minmax(0, 1fr);
  }
  ul {
    display: grid;
    gap: 2px;
    margin: 0;
    padding: 0;
    list-style: none;
  }
`;
const Entry = styled.a`
  display: flex;
  align-items: center;
  gap: ${(p) => p.theme.space(2)};
  min-height: 28px;
  padding: 0 ${(p) => p.theme.space(2)};
  border-radius: ${(p) => p.theme.radius.sm};
  color: ${(p) => p.theme.color.text};
  text-decoration: none;
  .title {
    flex: 1 1 auto;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  &[data-muted="true"] {
    color: ${(p) => p.theme.color.textMuted};
  }
  &:hover {
    background: color-mix(in srgb, ${(p) => p.theme.color.text} ${NAV_HOVER_PCT}%, var(--nav-base));
  }
  /* Pressed and current entries show muted titles and counts in full text, for contrast on the stronger fills. */
  &:active,
  &[aria-current="location"] {
    color: ${(p) => p.theme.color.text};
  }
  &:active .count,
  &[aria-current="location"] .count {
    color: inherit;
  }
  &:active {
    background: color-mix(in srgb, ${(p) => p.theme.color.text} ${NAV_PRESS_PCT}%, var(--nav-base));
  }
  &:focus-visible {
    outline: 2px solid ${(p) => p.theme.color.focus};
    outline-offset: -2px;
  }
  /* Current: accent fill and bold title, not colour alone; the entry's width is fixed, so nothing moves. */
  &[aria-current="location"] {
    background: color-mix(in srgb, ${(p) => p.theme.color.accentText} ${CHIP_PRESSED_PCT}%, var(--nav-base));
  }
  &[aria-current="location"] .title {
    font-weight: ${(p) => p.theme.font.weight.semibold};
  }
  &[aria-current="location"]:hover {
    background: color-mix(in srgb, ${(p) => p.theme.color.accentText} ${CHIP_PRESSED_HOVER_PCT}%, var(--nav-base));
  }
  &[aria-current="location"]:active {
    background: color-mix(in srgb, ${(p) => p.theme.color.accentText} ${CHIP_PRESSED_ACTIVE_PCT}%, var(--nav-base));
  }
`;
const Badge = styled.span`
  flex: none;
  box-sizing: border-box;
  display: inline-block;
  min-width: 3.5ch;
  padding: 0 ${(p) => p.theme.space(1.5)};
  border-radius: 999px;
  color: ${(p) => p.theme.color.textMuted};
  font-size: ${(p) => p.theme.font.size.sm};
  font-weight: ${(p) => p.theme.font.weight.regular};
  font-variant-numeric: tabular-nums;
  line-height: 1.5;
  text-align: center;
  &[data-loading="true"] {
    background: color-mix(in srgb, ${(p) => p.theme.color.text} 10%, transparent);
  }
`;
const Cards = styled.div`
  display: grid;
  gap: ${(p) => p.theme.space(4)};
  min-width: 0;
`;
const Card = styled.section`
  min-width: 0;
  scroll-margin-top: ${(p) => scrollClearance(p.theme.space)};
  background: ${(p) => p.theme.color.surface};
  border: 1px solid ${(p) => p.theme.color.border};
  border-radius: ${(p) => p.theme.radius.md};
  > header {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: ${(p) => p.theme.space(1)} ${(p) => p.theme.space(3)};
    min-height: 44px;
    box-sizing: border-box;
    padding: ${(p) => p.theme.space(2)} ${(p) => p.theme.space(4)};
    border-bottom: 1px solid ${(p) => p.theme.color.border};
  }
  > header h2 {
    margin: 0;
    font-size: ${(p) => p.theme.font.size.base};
    font-weight: ${(p) => p.theme.font.weight.semibold};
  }
  > header .count {
    background: color-mix(in srgb, ${(p) => p.theme.color.text} 8%, ${(p) => p.theme.color.surface});
  }
  > .body {
    min-width: 0;
    padding: ${(p) => p.theme.space(4)};
  }
`;

// region Helpers
/** One section of a detail page: its card and its nav entry come from this one value (design §10.1). */
export interface DetailSection {
  /** Element id and URL hash, e.g. "route-rules". */
  id: string;
  group: SectionGroup;
  title: string;
  /** Absent: no badge. "loading": a fixed-width placeholder until the count arrives. */
  count?: number | "loading";
  /** Applies but is empty (count 0): the entry is muted, the card stays. */
  muted?: boolean;
  /** The card body. */
  render: () => ReactNode;
}

/** Sections in group order; within a group, in the order the page lists them. */
function inGroupOrder(sections: DetailSection[]): DetailSection[] {
  return [...sections].sort((a, b) => SECTION_GROUPS.indexOf(a.group) - SECTION_GROUPS.indexOf(b.group));
}

function Count({ value }: { value: DetailSection["count"] }) {
  if (value === undefined) return null;
  if (value === "loading")
    return (
      <Badge className="count" data-testid="count-placeholder" data-loading="true" aria-hidden="true">
        {"\u00a0"}
      </Badge>
    );
  return <Badge className="count">{value}</Badge>;
}

// The section id in a URL hash; a malformed escape (a bad shared link) falls back to the raw text.
function decodeHash(hash: string): string {
  try {
    return decodeURIComponent(hash.slice(1));
  } catch {
    return hash.slice(1);
  }
}

// The hash for a section id, encoded so decodeHash returns the id on reload.
const hashOf = (id: string) => `#${encodeURIComponent(id)}`;

// Whether the page is scrolled to its end, where a short last card can never reach the top.
function atBottom(): boolean {
  const doc = document.documentElement;
  return doc.scrollHeight > window.innerHeight && window.innerHeight + window.scrollY >= doc.scrollHeight - 2;
}

interface NavProps {
  label: string;
  sections: DetailSection[];
  current?: string;
  onJump: (e: MouseEvent<HTMLAnchorElement>, id: string) => void;
}

// A cut title shows in full on hover; one that fits gets no tooltip.
function fullTitle(e: MouseEvent<HTMLAnchorElement>) {
  const t = e.currentTarget.querySelector<HTMLElement>(".title");
  if (t && t.scrollWidth > t.clientWidth) e.currentTarget.title = t.textContent ?? "";
  else e.currentTarget.removeAttribute("title");
}

function SectionNav({ label, sections, current, onJump }: NavProps) {
  const groups = SECTION_GROUPS.map((g) => [g, sections.filter((s) => s.group === g)] as const).filter(
    ([, list]) => list.length > 0,
  );
  return (
    <NavRoot aria-label={label}>
      {groups.map(([g, list]) => (
        <div key={g}>
          <div className="group">{sectionsCopy.groups[g]}</div>
          <ul>
            {list.map((s) => (
              <li key={s.id}>
                <Entry
                  href={hashOf(s.id)}
                  aria-current={s.id === current ? "location" : undefined}
                  data-muted={s.muted ? "true" : undefined}
                  onClick={(e) => onJump(e, s.id)}
                  onMouseEnter={fullTitle}
                >
                  <span className="title">{s.title}</span>
                  <Count value={s.count} />
                </Entry>
              </li>
            ))}
          </ul>
        </div>
      ))}
    </NavRoot>
  );
}

// region Component
/** Nav entries the skeleton holds while the page's data loads. */
const SKELETON_ENTRIES = 6;
const SkeletonEntry = styled.div`
  display: flex;
  align-items: center;
  height: 28px;
  padding: 0 ${(p) => p.theme.space(2)};
  > * {
    flex: 1 1 auto;
  }
`;
const SkeletonCards = styled.div`
  display: grid;
  gap: ${(p) => p.theme.space(3)};
  min-width: 0;
`;

/** The loading page: the nav's width (or the narrow bar) and a fixed number of entries, so cards never move sideways. */
export function DetailSectionsSkeleton() {
  const narrow = useMedia(`(max-width: ${NARROW_BELOW - 1}px)`);
  return (
    <Layout narrow={narrow} data-testid="detail-sections-skeleton">
      {narrow ? (
        <Bar>
          <Shimmer h={36} stylesOverride={css({ width: 140 })} />
        </Bar>
      ) : (
        <div>
          {Array.from({ length: SKELETON_ENTRIES }, (_, i) => (
            // biome-ignore lint/suspicious/noArrayIndexKey: fixed placeholders with no identity.
            <SkeletonEntry key={i} data-testid="nav-placeholder">
              <Shimmer h={14} />
            </SkeletonEntry>
          ))}
        </div>
      )}
      <SkeletonCards>
        <Shimmer h={20} />
        <Shimmer h={200} />
      </SkeletonCards>
    </Layout>
  );
}

interface DetailSectionsProps {
  sections: DetailSection[];
  /** The nav's accessible name, e.g. "HTTPRoute sections". */
  label: string;
  /** A section that scrolls itself to a deeper target (a route's ?rule=): it becomes current and the hash is skipped. */
  focus?: string;
}

/** A detail page's section cards with a sticky section nav, both from one list (design §6, D-148). */
export function DetailSections({ sections, label, focus }: DetailSectionsProps) {
  const ordered = useMemo(() => inGroupOrder(sections), [sections]);
  const narrow = useMedia(`(max-width: ${NARROW_BELOW - 1}px)`);
  const { pathname, search, hash } = useLocation();
  const navigate = useNavigate();
  const [current, setCurrent] = useState(ordered[0]?.id);
  const [open, setOpen] = useState(false);
  const [vh, setVh] = useState(() => window.innerHeight);
  const baseId = useId();
  const bar = useRef<HTMLDivElement>(null);
  const toggle = useRef<HTMLButtonElement>(null);
  // The latest list, for listeners that outlive a render.
  const latest = useRef(ordered);
  latest.current = ordered;
  // A jumped-to section stays current until the user scrolls, even when it cannot reach the top.
  const pinned = useRef<string | undefined>(undefined);
  // The scroll position the jump settled at; a scroll elsewhere (a scrollbar drag too) releases the pin.
  const pinY = useRef(0);
  // The hash already scrolled to or pushed by a click, so each hash scrolls once.
  const resolved = useRef<string | undefined>(undefined);
  // The focus already applied, so a later hash (a nav click, Back) is handled as usual.
  const focused = useRef<string | undefined>(undefined);
  const ids = ordered.map((s) => s.id).join("\n");

  const jump = (id: string) => {
    pinned.current = id;
    setCurrent(id);
    document.getElementById(id)?.scrollIntoView?.({ block: "start" });
    pinY.current = window.scrollY;
  };
  const onJump = (e: MouseEvent<HTMLAnchorElement>, id: string) => {
    if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    e.preventDefault();
    setOpen(false);
    jump(id);
    // A new history entry that changes only the hash, so Back returns to the previous section.
    if (hash !== hashOf(id)) {
      resolved.current = hashOf(id);
      navigate({ pathname, search, hash: hashOf(id) });
    }
  };

  // A hash from a link, Back or Forward scrolls to its section once; a section that appears later is scrolled to then.
  // A focus wins over the hash present when it applies: its section does the scrolling, so only the nav follows.
  // biome-ignore lint/correctness/useExhaustiveDependencies: runs on hash, focus or section id changes; jump reads refs and state setters.
  useEffect(() => {
    if (focus && focused.current !== focus && latest.current.some((s) => s.id === focus)) {
      focused.current = focus;
      resolved.current = hash || undefined;
      pinned.current = focus;
      pinY.current = window.scrollY;
      setCurrent(focus);
      return;
    }
    // A hashless entry forgets the last hash, so Back to it scrolls again.
    if (!hash) resolved.current = undefined;
    if (!hash || resolved.current === hash) return;
    const id = decodeHash(hash);
    if (!latest.current.some((s) => s.id === id)) return;
    resolved.current = hash;
    jump(id);
  }, [hash, ids, focus]);

  useEffect(() => {
    const resize = () => setVh(window.innerHeight);
    window.addEventListener("resize", resize, { passive: true });
    return () => window.removeEventListener("resize", resize);
  }, []);

  // Scroll-spy: the first card in a band below the sticky offset is current; at the page end, the last card.
  // Rebuilt when the offset (narrow bar) or window height changes; the band is in px, as spec % resolve against width.
  // biome-ignore lint/correctness/useExhaustiveDependencies: re-observes on section ids, breakpoint or height changes.
  useEffect(() => {
    const cards = latest.current.map((s) => document.getElementById(s.id)).filter((el) => el !== null);
    const first = cards[0];
    if (!first || typeof IntersectionObserver === "undefined") return;
    const offset = Number.parseFloat(getComputedStyle(first).scrollMarginTop) || 0;
    const inBand = new Set<string>();
    const update = () => {
      if (pinned.current) return;
      const list = latest.current;
      const next = atBottom() ? list.at(-1)?.id : list.find((s) => inBand.has(s.id))?.id;
      if (next) setCurrent(next);
    };
    const io = new IntersectionObserver(
      (entries) => {
        for (const e of entries) e.isIntersecting ? inBand.add(e.target.id) : inBand.delete(e.target.id);
        update();
      },
      { rootMargin: `-${Math.max(0, offset - 4)}px 0px -${Math.round(vh * 0.6)}px 0px` },
    );
    for (const el of cards) io.observe(el);
    const release = () => {
      pinned.current = undefined;
    };
    const userScroll = ["wheel", "touchmove", "keydown"] as const;
    for (const t of userScroll) window.addEventListener(t, release, { passive: true });
    const onScroll = () => {
      if (pinned.current && Math.abs(window.scrollY - pinY.current) > 2) release();
      update();
    };
    window.addEventListener("scroll", onScroll, { passive: true });
    return () => {
      io.disconnect();
      for (const t of userScroll) window.removeEventListener(t, release);
      window.removeEventListener("scroll", onScroll);
    };
  }, [ids, narrow, vh]);

  // The open list folds on Escape or a click outside it.
  useEffect(() => {
    if (!open) return;
    const outside = (e: PointerEvent) => {
      if (!bar.current?.contains(e.target as Node)) setOpen(false);
    };
    const esc = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      setOpen(false);
      toggle.current?.focus();
    };
    document.addEventListener("pointerdown", outside);
    document.addEventListener("keydown", esc);
    return () => {
      document.removeEventListener("pointerdown", outside);
      document.removeEventListener("keydown", esc);
    };
  }, [open]);

  const nav = <SectionNav label={label} sections={ordered} current={current} onJump={onJump} />;
  return (
    <Layout narrow={narrow} data-testid="detail-sections">
      {narrow ? (
        <Bar ref={bar}>
          <Toggle
            ref={toggle}
            type="button"
            aria-expanded={open}
            aria-controls={`${baseId}-panel`}
            onClick={() => setOpen((o) => !o)}
          >
            {sectionsCopy.sections}
            <ChevronDown size={16} aria-hidden="true" />
          </Toggle>
          {open ? <Panel id={`${baseId}-panel`}>{nav}</Panel> : null}
        </Bar>
      ) : (
        <Rail>{nav}</Rail>
      )}
      <Cards>
        {ordered.map((s) => (
          <Card key={s.id} id={s.id} aria-labelledby={`${baseId}-${s.id}`} data-muted={s.muted ? "true" : undefined}>
            <header>
              <h2 id={`${baseId}-${s.id}`}>{s.title}</h2>
              <Count value={s.count} />
            </header>
            <div className="body">{s.render()}</div>
          </Card>
        ))}
      </Cards>
    </Layout>
  );
}

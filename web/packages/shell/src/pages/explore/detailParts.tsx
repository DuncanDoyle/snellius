import type { SerializedStyles } from "@emotion/react";
import styled from "@emotion/styled";
import { FileCode, LayoutList, type LucideIcon } from "lucide-react";
import { createContext, type ReactNode, useEffect, useId, useMemo, useState } from "react";
import type { ApiError } from "../../api/client";
import { useNow } from "../../api/clock";
import { ApiErrorState } from "../../api/errorState";
import { isStale } from "../../api/hooks";
import type { Candidate, DetailReason, LinkedRef, Page, RouteConfigSignal, RoutePathSignal } from "../../api/types";
import { detailCopy, listCopy, sectionsCopy } from "../../copy/explorer";
import { CandidateBanner } from "../../explore/CandidateBanner";
import { CopyCommandButton } from "../../explore/CopyCommandButton";
import { DetailTabs } from "../../explore/DetailTabs";
import { type ObjectColumn, ObjectTable } from "../../explore/ObjectTable";
import { PROBLEM_ID, ProblemStrip, type ProblemStripProps } from "../../explore/ProblemStrip";
import { decidingSignal, pathNote, signalLabel } from "../../explore/RouteSignals";
import { StaleView } from "../../health/staleView";
import { CopyNameButton } from "../../resource/CopyNameButton";
import { ResourceName } from "../../resource/ResourceName";
import { ResourceYaml } from "../../resource/ResourceYaml";
import { TrailRefsProvider, useReportTrailRefs, useTrailRefsCollector } from "../../shell/Breadcrumbs";
import {
  type SideSectionContribution,
  Slot,
  useShownDetailTabs,
  useSideSectionContributions,
} from "../../shell/contributions";
import { DetailColumns, layout, useTwoColumns } from "../../shell/layout";
import { OBJECT_HEADER_H, ObjectHeader } from "../../shell/ObjectHeader";
import { installPath } from "../../shell/paths";
import { useUrlParams } from "../../shell/urlParams";
import { BackLink } from "../../states/controls";
import { PageNotice } from "../../states/PageNotice";
import { PageState } from "../../states/PageState";
import { Shimmer } from "../../states/Shimmer";
import type { HealthState } from "../../theme/tokens";
import { type DetailSection, DetailSections, DetailSectionsSkeleton } from "./DetailSections";
import { useShownPage } from "./listParts";

// region Styles
/** Height of the detail header, fixed from the first render: the name comes from the URL, the rest fills in. */
export const HEADER_H = OBJECT_HEADER_H;

const Frame = styled.div<{ stylesOverride?: SerializedStyles }>`
  max-width: ${layout.maxWidth}px;
  margin: 0 auto;
  padding: ${(p) => p.theme.space(4)};
  ${(p) => p.stylesOverride}
`;
export const Section = styled.section`
  margin-bottom: ${(p) => p.theme.space(5)};
  h2 {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: ${(p) => p.theme.space(1)} ${(p) => p.theme.space(3)};
    margin: 0 0 ${(p) => p.theme.space(2)};
    font-size: ${(p) => p.theme.font.size.base};
    font-weight: ${(p) => p.theme.font.weight.semibold};
  }
  h2 .note {
    color: ${(p) => p.theme.color.textMuted};
    font-size: ${(p) => p.theme.font.size.sm};
    font-weight: ${(p) => p.theme.font.weight.regular};
  }
`;
/** Label and value pairs under a detail header. */
export const Facts = styled.dl`
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: 4px ${(p) => p.theme.space(3)};
  margin: 0;
  font-size: ${(p) => p.theme.font.size.md};
  dt {
    color: ${(p) => p.theme.color.textMuted};
  }
  dd {
    margin: 0;
    min-width: 0;
    overflow-wrap: anywhere;
  }
`;
const Stack = styled.div`
  display: grid;
  gap: ${(p) => p.theme.space(3)};
`;
const Overview = styled.div`
  display: grid;
  gap: ${(p) => p.theme.space(4)};
  min-width: 0;
`;
const Nothing = styled.p`
  margin: 0;
  color: ${(p) => p.theme.color.textMuted};
  font-size: ${(p) => p.theme.font.size.md};
`;

// region Helpers
const TABS = [
  { id: "overview", label: detailCopy.overview, icon: LayoutList },
  { id: "yaml", label: detailCopy.yaml, icon: FileCode },
];
// Core tab ids are reserved, so a contribution never takes one over.
const CORE_TAB_IDS = TABS.map((t) => t.id);

/** A page's own tab after Overview and YAML, e.g. the route's Delegation tab (`?tab=delegation`). */
export interface ExtraTab {
  id: string;
  label: string;
  icon?: LucideIcon;
  /** Rendered only once data has loaded. */
  content: ReactNode;
}

/** group/Kind/namespace/name, as the list endpoints take a parent (design §7.2). */
export const refParam = (r: LinkedRef) => [r.group, r.kind, r.namespace ?? "", r.name].join("/");

/** The frame's URL params, so a section keeps its state in the page's one {@link useUrlParams}. */
export const DetailParams = createContext<ReturnType<typeof useUrlParams> | null>(null);

// region Component
interface DetailFrameProps {
  installId: string;
  /** The object from the URL, so the header renders before the data. */
  refValue: LinkedRef;
  noun: string;
  data?: {
    /** Absent for an object Snellius gives no health, such as a policy: no badge and no problem strip. */
    health?: HealthState;
    candidate?: Candidate;
    collectedAt: string;
    reason?: DetailReason;
    /** A route's two signals: the header shows both and the strip names the one that decides. */
    config?: RouteConfigSignal;
    path?: RoutePathSignal;
  };
  error?: ApiError;
  /** The page's part of the problem strip, shown over the main column while the state is not healthy. */
  problem?: Pick<ProblemStripProps, "condition" | "conditionsId" | "parent" | "gateway">;
  /** The page draws its own strip with id PROBLEM_ID, e.g. the backend status line; the badge still links to it. */
  ownProblem?: boolean;
  /** The Overview tab's sections, nav and cards from one list; contributed sections follow in "Related" (D-148). */
  sections?: DetailSection[];
  /** A section that a deeper link scrolls into itself, such as a route's ?rule=: it is current, the hash is not used. */
  sectionFocus?: string;
  /** The Overview tab's main column when there are no sections; rendered only once data has loaded. */
  children?: ReactNode;
  /** The Overview tab's side column, e.g. conditions and related objects from 1600 px. Contributed sections follow it. */
  side?: ReactNode;
  /** The page fills the side column from 1600 px ({@link useTwoColumns}), so the skeleton holds a place for it there. */
  sideWhenWide?: boolean;
  /** Tabs after YAML; a ?tab= naming none of them shows Overview. */
  extraTabs?: ExtraTab[];
  /** Refs the page's data links, for the trail above the header. */
  trailRefs?: readonly LinkedRef[];
  /** False for an object the cache keeps only facts of, such as a Service: no YAML tab. */
  yaml?: boolean;
  stylesOverride?: SerializedStyles;
}

/** A detail page: candidate banner, header, Overview and YAML tabs (`?tab=yaml`), and the detail states (design §8.6). */
export function DetailFrame({
  installId,
  refValue,
  noun,
  data,
  error,
  problem,
  ownProblem,
  sections,
  sectionFocus,
  children,
  side,
  sideWhenWide,
  extraTabs,
  trailRefs,
  yaml = true,
  stylesOverride,
}: DetailFrameProps) {
  const wide = useTwoColumns();
  const urlParams = useUrlParams();
  const [params, update] = urlParams;
  const coreIds = useMemo(() => [...CORE_TAB_IDS, ...(extraTabs ?? []).map((t) => t.id)], [extraTabs]);
  const asked = params.get("tab");
  const contributed = useShownDetailTabs(installId, refValue, data, coreIds, asked);
  const sideSections = useSideSectionContributions(refValue);
  const tabs = useMemo(
    () => [
      ...TABS.filter((t) => yaml || t.id !== "yaml"),
      ...[...(extraTabs ?? []), ...contributed].map(({ id, label, icon }) => ({ id, label, icon })),
    ],
    [extraTabs, contributed, yaml],
  );
  const tab = tabs.find((t) => t.id === asked)?.id ?? "overview";
  const extra = extraTabs?.find((t) => t.id === tab);
  const own = contributed.find((t) => t.id === tab);
  const ctx = { installId, ref: refValue, detail: data };
  const sideId = useId();
  const objectKey = `${installId}|${refParam(refValue)}`;
  const errorTitle = listCopy.cannotLoad(noun);
  const now = useNow();
  // Stale data or a failed poll turns every state on the page unknown, as on the lists (design §6.3).
  const stale = !!data && (!!error || isStale(data.collectedAt, now));
  // The trail resolves crumbs through refs the page holds and refs its sub-tables report.
  const by = data?.candidate?.by;
  const heldRefs = useMemo(() => [...(trailRefs ?? []), ...(by ? [by] : [])], [trailRefs, by]);
  const [pageRefs, reportRefs] = useTrailRefsCollector(heldRefs);
  // The strip explains the server's state; a stale view replaces that state, so it goes too.
  const deciding = data?.config && data.path ? decidingSignal(data.config, data.path) : undefined;
  const problemShown =
    !!data && !stale && ((!!data.health && data.health !== "healthy") || !!deciding) && tab === "overview";
  // The configuration's reason carries the finding id only on the page's own reason.
  const stripReason =
    deciding?.id === "config" && data?.reason?.kind === "finding" ? data.reason : (deciding?.reason ?? data?.reason);
  // A hidden object 404s as an absent one does (D-013).
  if (error?.status === 404 && !data)
    return (
      <PageState
        kind="empty"
        title={listCopy.notInScope}
        message={
          <>
            {refValue.kind} <ResourceName namespace={refValue.namespace} name={refValue.name} />
            <br />
            <BackLink to={installPath(installId)}>{listCopy.backToInstall}</BackLink>
          </>
        }
      />
    );
  const strip =
    data && problemShown && !ownProblem ? (
      <ProblemStrip
        installId={installId}
        state={deciding?.state ?? data.health ?? "unknown"}
        reason={stripReason}
        signal={deciding ? signalLabel[deciding.id] : undefined}
        incomplete={data.path?.incomplete}
        pathReason={data.path?.reason}
        {...problem}
      />
    ) : null;
  // A contribution that throws or renders nothing keeps its card, with this line in it.
  const nothing = <Nothing>{sectionsCopy.nothingToShow}</Nothing>;
  const contributedSection = (s: SideSectionContribution) => (
    <Slot key={s.id} id={`side-section:${s.id}`} resetKey={objectKey} fallback={nothing} render={() => s.render(ctx)} />
  );
  const overview = data ? (
    extra ? (
      extra.content
    ) : own ? (
      <Slot key={own.id} id={`detail-tab:${own.id}`} resetKey={objectKey} render={() => own.render(ctx)} />
    ) : sections ? (
      <Overview>
        {strip}
        <DetailSections
          label={sectionsCopy.nav(refValue.kind)}
          focus={sectionFocus}
          sections={[
            ...sections,
            // Contributions go last in "Related", under ext- ids so none can take a core section's anchor.
            ...sideSections.map(
              (s): DetailSection => ({
                id: `ext-${s.id}`,
                group: "related",
                title: s.title,
                render: () => <div data-slot="side-section">{contributedSection(s)}</div>,
              }),
            ),
          ]}
        />
      </Overview>
    ) : (
      <DetailColumns
        main={
          <>
            {strip}
            {children}
          </>
        }
        side={
          side || sideSections.length ? (
            <>
              {side}
              {sideSections.map((s) => (
                <Section key={s.id} aria-labelledby={`${sideId}-${s.id}`} data-slot="side-section">
                  <h2 id={`${sideId}-${s.id}`}>{s.title}</h2>
                  {contributedSection(s)}
                </Section>
              ))}
            </>
          ) : null
        }
      />
    )
  ) : error ? (
    <ApiErrorState error={error} title={errorTitle} />
  ) : (
    <div aria-busy="true" data-testid="detail-skeleton">
      {sections && tab === "overview" ? (
        <DetailSectionsSkeleton />
      ) : (
        <DetailColumns
          main={
            <Stack>
              <Shimmer h={20} />
              <Shimmer h={200} />
            </Stack>
          }
          // The same split as the loaded page, so nothing moves when data arrives.
          side={(sideSections.length || (wide && sideWhenWide)) && tab === "overview" ? <Shimmer h={120} /> : null}
        />
      )}
    </div>
  );
  return (
    <StaleView.Provider value={stale}>
      <TrailRefsProvider value={reportRefs}>
        <Frame data-testid="page-frame" stylesOverride={stylesOverride}>
          {data && error ? <PageNotice error={error} title={errorTitle} /> : null}
          {data?.candidate ? <CandidateBanner candidate={data.candidate} installId={installId} /> : null}
          <ObjectHeader
            kind={{ group: refValue.group, kind: refValue.kind, label: refValue.kind }}
            name={refValue.name}
            namespace={refValue.namespace}
            state={data?.health}
            signals={
              data?.config && data.path
                ? [
                    {
                      id: "config",
                      label: signalLabel.config,
                      state: data.config.state,
                      reason: data.config.reason,
                    },
                    {
                      id: "path",
                      label: signalLabel.path,
                      state: data.path.state,
                      reason: data.path.reason,
                      note: pathNote(data.path),
                    },
                  ]
                : undefined
            }
            stateHref={problemShown ? `#${PROBLEM_ID}` : undefined}
            collectedAt={data?.collectedAt}
            trailRefs={pageRefs}
            current={data ? "shown" : "pending"}
            actions={
              <>
                <CopyNameButton resource={refValue} />
                <CopyCommandButton refValue={refValue} />
              </>
            }
          />
          <DetailTabs
            label={detailCopy.views(refValue.kind)}
            tabs={tabs}
            selected={tab}
            onSelect={(t) => update({ tab: t === "overview" ? null : t })}
          >
            <DetailParams.Provider value={urlParams}>
              {tab === "yaml" ? <ResourceYaml installId={installId} refValue={refValue} /> : overview}
            </DetailParams.Provider>
          </DetailTabs>
        </Frame>
      </TrailRefsProvider>
    </StaleView.Provider>
  );
}

type ListHook<T> = (
  id: string,
  query: URLSearchParams,
) => { data?: Page<T>; error?: ApiError; cursorRejected: boolean; isLaggy: boolean };

interface SubListProps<T> {
  installId: string;
  label: string;
  noun: readonly [string, string];
  useList: ListHook<T>;
  /** The fixed query, e.g. parent and attachment; a change starts again at page one. */
  query: Record<string, string>;
  columns: ObjectColumn<T>[];
  rowRef: (row: T) => LinkedRef;
  emptyTitle: string;
  /** The expected row count, so the skeleton has the loaded table's height. */
  skeletonRows: number;
  showDenied?: boolean;
}

const NO_SORT = { key: "name", desc: false };
/** The server's default page size (design §7.2): a sub-table never shows more rows than this. */
export const PAGE_LIMIT = 50;
/** Skeleton rows for an expected count: at most one page. */
export const expectedRows = (n: number) => Math.min(Math.max(0, n), PAGE_LIMIT);

/** A paged list under a detail page, through the list endpoint; its cursor is local, so the page URL stays short. */
export function SubList<T extends { health: HealthState }>({
  installId,
  label,
  noun,
  useList,
  query,
  columns,
  rowRef,
  emptyTitle,
  skeletonRows,
  showDenied,
}: SubListProps<T>) {
  const key = new URLSearchParams(query).toString();
  const [cursor, setCursor] = useState<{ key: string; c: string }>();
  const c = cursor?.key === key ? cursor.c : undefined;
  const q = useMemo(() => {
    const out = new URLSearchParams(key);
    if (c) out.set("cursor", c);
    return out;
  }, [key, c]);
  const l = useList(installId, q);
  useEffect(() => {
    if (l.cursorRejected) setCursor(undefined);
  }, [l.cursorRejected]);
  const page = useShownPage(l.data, !!l.error);
  // Rows from the previous query are not this page's refs while it reloads.
  useReportTrailRefs(useMemo(() => (l.isLaggy ? undefined : page?.items.map(rowRef)), [l.isLaggy, page, rowRef]));
  return (
    <>
      {l.error ? <PageNotice error={l.error} title={listCopy.cannotLoad(noun[1])} /> : null}
      <ObjectTable
        label={label}
        installId={installId}
        noun={noun}
        columns={columns}
        rowRef={rowRef}
        page={page}
        loading={!l.data && !l.error}
        busy={l.isLaggy}
        emptyTitle={emptyTitle}
        skeletonRows={skeletonRows}
        showDenied={showDenied}
        sort={NO_SORT}
        onSort={() => {}}
        onPage={(next) => setCursor({ key, c: next })}
      />
    </>
  );
}

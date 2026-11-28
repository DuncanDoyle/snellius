import styled from "@emotion/styled";
import { EyeOff } from "lucide-react";
import { useMemo } from "react";
import { useNavigate } from "react-router-dom";
import type { BackendConsumer, LinkedRef, Page, PathRow, PathsResponse } from "../../api/types";
import { isSecureMode } from "../../auth/mode";
import { exposureCopy } from "../../copy/backends";
import { summaryCopy } from "../../copy/traffic";
import { LinkedName } from "../../explore/LinkedName";
import { installPath } from "../../shell/paths";
import { InlineLink } from "../../states/controls";
import { backendParam } from "../explore/route/refs";
import { PathTable } from "../traffic/PathTable";

// region Styles
/** The objects other than routes that use the backend: a section card's body. */
const ConsumersBox = styled.div`
  font-size: ${(p) => p.theme.font.size.sm};
  ul {
    display: grid;
    gap: ${(p) => p.theme.space(1)};
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .muted {
    color: ${(p) => p.theme.color.textMuted};
  }
  .mono {
    font-family: ${(p) => p.theme.font.mono};
    font-size: ${(p) => p.theme.font.size.code};
  }
`;
const Head = styled.p`
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: ${(p) => p.theme.space(1)} ${(p) => p.theme.space(3)};
  margin: 0 0 ${(p) => p.theme.space(2)};
  font-size: ${(p) => p.theme.font.size.sm};
  color: ${(p) => p.theme.color.textMuted};
`;
const Hidden = styled.p`
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 0 0 ${(p) => p.theme.space(2)};
  font-size: ${(p) => p.theme.font.size.sm};
  color: ${(p) => p.theme.color.textMuted};
`;

// region Helpers
/** The backend's own Traffic column and endpoints repeat this page, so they are left out. */
const OMIT = ["target", "endpoints"] as const;
const NO_SORT = { key: "problem", desc: false };

// region Component
interface ExposureSectionProps {
  installId: string;
  backend: LinkedRef;
  exposure: PathsResponse;
}

/**
 * How this backend is exposed (Phase 6 design §8.2): its paths, the first page in Traffic's order. Further pages
 * open in Traffic, filtered by backend=. A section card's body.
 */
export function ExposureSection({ installId, backend, exposure }: ExposureSectionProps) {
  const navigate = useNavigate();
  const traffic = `${installPath(installId, "traffic")}?${new URLSearchParams({ backend: backendParam(backend) })}`;
  const page = useMemo<Page<PathRow>>(
    () => ({
      install: exposure.install,
      items: exposure.rows,
      total: exposure.summary.paths,
      next: exposure.next,
      prev: exposure.prev,
      delegationNotChecked: exposure.delegationNotChecked,
      collectedAt: exposure.collectedAt,
    }),
    [exposure],
  );
  return (
    <>
      <Head>
        <span>{summaryCopy.paths(exposure.summary.paths)}</span>
        <InlineLink to={traffic}>{exposureCopy.openTraffic}</InlineLink>
      </Head>
      {exposure.summary.hidden && !isSecureMode() ? (
        <Hidden>
          <EyeOff size={14} aria-hidden="true" />
          {summaryCopy.hidden}
        </Hidden>
      ) : null}
      <PathTable
        installId={installId}
        label={exposureCopy.table}
        page={page}
        busy={false}
        sort={NO_SORT}
        onSort={() => {}}
        onPage={(cursor) => navigate(`${traffic}&${new URLSearchParams({ cursor })}`)}
        emptyTitle={exposureCopy.none}
        omit={OMIT}
        sortable={false}
      />
    </>
  );
}

/** The objects other than routes that use this backend, each with its role and field. A section card's body. */
export function ConsumersBody({ installId, consumers }: { installId: string; consumers: BackendConsumer[] }) {
  return (
    <ConsumersBox>
      <ul>
        {consumers.map((c) => (
          <li key={`${c.consumer.kind}/${c.consumer.namespace}/${c.consumer.name}/${c.field}`} data-testid="consumer">
            {exposureCopy.usedBy(c.consumer.kind)} <LinkedName refValue={c.consumer} installId={installId} />{" "}
            {exposureCopy.role(c.role)} <span className="muted">{exposureCopy.field}</span>{" "}
            <span className="mono">{c.port ? `${c.field} :${c.port}` : c.field}</span>
          </li>
        ))}
      </ul>
    </ConsumersBox>
  );
}

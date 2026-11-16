import type { SerializedStyles } from "@emotion/react";
import styled from "@emotion/styled";
import type { InstallHeader } from "../api/types";
import { shellCopy } from "../copy/shell";
import { ResourceName } from "../resource/ResourceName";

// region Styles
const Hidden = styled.span`
  display: inline-flex;
  align-items: baseline;
  gap: 8px;
  min-width: 0;
`;
/** Dimmed by opacity, so it reads on the header band and on the page alike. */
const Note = styled.span`
  font-size: ${(p) => p.theme.font.size.sm};
  font-style: italic;
  opacity: 0.85;
  white-space: nowrap;
`;

// region Helpers
type Named = Pick<InstallHeader, "id" | "displayName" | "namespace" | "name" | "detailsHidden">;

/** An install's name as plain text: its display name, else its ID. */
export function installLabel(i: Named): string {
  return i.displayName || i.id;
}

// region Component
interface InstallNameProps {
  install: Named;
  stylesOverride?: SerializedStyles;
}

/**
 * An install's name: namespace/name in the resource style where the platform has both, else its display name (D-016).
 * Without readable details the ID stands as a neutral name, with a note saying why (design §7.4).
 */
export function InstallName({ install: i, stylesOverride }: InstallNameProps) {
  if (i.detailsHidden && !i.displayName)
    return (
      <Hidden>
        <ResourceName name={i.id} stylesOverride={stylesOverride} />
        <Note data-testid="details-hidden" data-part="details-hidden">
          {shellCopy.detailsHidden}
        </Note>
      </Hidden>
    );
  if (i.namespace && i.name)
    return <ResourceName namespace={i.namespace} name={i.name} stylesOverride={stylesOverride} />;
  return <ResourceName name={installLabel(i)} stylesOverride={stylesOverride} />;
}

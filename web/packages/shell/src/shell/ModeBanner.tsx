import styled from "@emotion/styled";
import { TriangleAlert } from "lucide-react";
import { useMeta } from "../api/hooks";
import { shellCopy } from "../copy/shell";

// region Styles
const Banner = styled.div`
  display: flex;
  align-items: flex-start;
  gap: ${(p) => p.theme.space(2)};
  padding: ${(p) => p.theme.space(2)} ${(p) => p.theme.space(4)};
  font-size: ${(p) => p.theme.font.size.sm};
  line-height: 1.5;
  color: ${(p) => p.theme.color.text};
  background: ${(p) => p.theme.color.status.warning.stripBg};
  border: 1px solid ${(p) => p.theme.color.status.warning.stripBorder};
  overflow-wrap: anywhere;
  svg {
    flex: none;
    margin-top: 3px;
    color: ${(p) => p.theme.color.status.warning.fg};
  }
`;

// region Component
/** Warns on every page that the server runs without sign-in; it never dismisses and renders once /meta says local. */
export function ModeBanner() {
  const { data: meta } = useMeta();
  if (meta?.authMode !== "local") return null;
  return (
    <Banner role="note" data-testid="mode-banner">
      <TriangleAlert size={16} aria-hidden="true" />
      <span>{shellCopy.localMode}</span>
    </Banner>
  );
}

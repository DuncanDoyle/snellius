import type { SerializedStyles } from "@emotion/react";
import styled from "@emotion/styled";
import type { PolicySource } from "../api/types";
import { sourceCopy, sourcePrefix } from "../copy/policies";
import { Tooltip } from "../shell/Tooltip";
import { VisuallyHidden } from "../states/VisuallyHidden";

// region Styles
/** Outlined and dashed for anything Snellius worked out, so the source reads without colour. */
const Tag = styled.span<{
  $derived: boolean;
  stylesOverride?: SerializedStyles;
}>`
  display: inline-flex;
  flex: none;
  align-items: center;
  height: 18px;
  padding: 0 6px;
  box-sizing: border-box;
  border: 1px ${(p) => (p.$derived ? "dashed" : "solid")} ${(p) => p.theme.color.borderStrong};
  border-radius: ${(p) => p.theme.radius.sm};
  color: ${(p) => p.theme.color.textMuted};
  font-size: ${(p) => p.theme.font.size.xs};
  font-weight: ${(p) => p.theme.font.weight.regular};
  letter-spacing: normal;
  text-transform: none;
  white-space: nowrap;
  cursor: help;
  &:hover {
    color: ${(p) => p.theme.color.text};
    border-color: ${(p) => p.theme.color.text};
  }
  &:active {
    color: ${(p) => p.theme.color.accentText};
    border-color: ${(p) => p.theme.color.accentText};
  }
  &:focus-visible {
    outline: 2px solid ${(p) => p.theme.color.focus};
    outline-offset: 2px;
  }
  ${(p) => p.stylesOverride}
`;

// region Component
interface SourceTagProps {
  source: PolicySource;
  stylesOverride?: SerializedStyles;
}

/** Names where a policy value comes from (design §3.2); the tooltip says what the source means. */
export function SourceTag({ source, stylesOverride }: SourceTagProps) {
  const c = sourceCopy[source] ?? sourceCopy.unknown;
  return (
    <Tooltip label={c.tip} wrap>
      {/* Focusable so keyboard users can read the tooltip. */}
      <Tag
        tabIndex={0}
        data-testid="source-tag"
        data-source={source}
        $derived={source === "inferred" || source === "unknown"}
        stylesOverride={stylesOverride}
      >
        <VisuallyHidden>{`${sourcePrefix}: `}</VisuallyHidden>
        {c.label}
      </Tag>
    </Tooltip>
  );
}

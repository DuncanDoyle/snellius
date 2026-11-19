import type { SerializedStyles } from "@emotion/react";
import styled from "@emotion/styled";
import { CircleDashed } from "lucide-react";

// region Styles
const Note = styled.span<{ stylesOverride?: SerializedStyles }>`
  display: inline-flex;
  align-items: center;
  gap: ${(p) => p.theme.space(1.5)};
  font-size: ${(p) => p.theme.font.size.sm};
  white-space: nowrap;
  color: ${(p) => p.theme.color.textMuted};
  ${(p) => p.stylesOverride}
`;

// region Component
interface NotCheckedProps {
  /** Why the viewer gets no result: its tooltip. */
  reason: string;
  label: string;
  stylesOverride?: SerializedStyles;
}

/** A result the viewer's permissions do not cover: neither fine nor unknown (D-106). */
export function NotChecked({ reason, label, stylesOverride }: NotCheckedProps) {
  return (
    <Note title={reason} data-testid="not-checked" stylesOverride={stylesOverride}>
      <CircleDashed size={14} aria-hidden="true" />
      {label}
    </Note>
  );
}

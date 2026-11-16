import { css } from "@emotion/react";
import styled from "@emotion/styled";
import { ScanEye } from "lucide-react";
import { useMeta } from "../api/hooks";
import { isSecureMode } from "../auth/mode";
import { shellCopy } from "../copy/shell";
import { HEADER_HOVER_PCT, HEADER_PRESS_PCT, headerTint } from "./headerTint";
import { Tooltip } from "./Tooltip";
import { useMedia } from "./useMedia";

// region Helpers
/** Below this width the note is an icon even above COMPACT: the text would truncate to a few letters. */
const NARROW = "(max-width: 1299px)";

// region Styles
/**
 * Reserved in secure mode from the first paint, at least the icon's width, so nothing shifts. It gives way
 * before the identity and the navs when the bar is full: the text truncates, down to the icon.
 */
const Slot = styled.div`
  display: flex;
  flex: 0 1000 auto;
  align-items: center;
  justify-content: flex-end;
  min-width: 32px;
`;
const Note = styled.span`
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  height: 32px;
  padding: 0 8px;
  border: 1px solid ${(p) => p.theme.color.borderStrong};
  border-radius: ${(p) => p.theme.radius.sm};
  color: ${(p) => p.theme.color.headerText};
  font-size: ${(p) => p.theme.font.size.sm};
  cursor: default;
  &:hover {
    background: ${(p) => headerTint(p.theme, HEADER_HOVER_PCT)};
    border-color: ${(p) => p.theme.color.headerText};
  }
  &:active {
    background: ${(p) => headerTint(p.theme, HEADER_PRESS_PCT)};
  }
  &:focus-visible {
    outline: 2px solid ${(p) => p.theme.color.focus};
    outline-offset: 2px;
  }
  svg {
    flex: none;
  }
`;
const Text = styled.span`
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
`;
const shrinkable = css`
  min-width: 0;
`;

// region Component
/**
 * Secure mode: tells a viewer who cannot list every kind everywhere that they see what their permissions allow.
 * Only /meta's limitedView decides it, never hidden objects. Below COMPACT it is an icon; the tooltip always has the text.
 */
export function PermissionsNote({ compact }: { compact: boolean }) {
  const { data: meta } = useMeta();
  // Secure mode adds the user menu, so the text also gives way a little above COMPACT.
  const iconOnly = useMedia(NARROW) || compact;
  if (!isSecureMode()) return null;
  const text = shellCopy.permissionsNote;
  return (
    <Slot data-testid="permissions-note-slot">
      {meta?.limitedView ? (
        <Tooltip label={text} placement="center" wrap stylesOverride={shrinkable}>
          <Note role="note" aria-label={text} tabIndex={0}>
            {/* Not the About button's info icon, so the two read apart. */}
            <ScanEye size={14} aria-hidden="true" />
            {iconOnly ? null : <Text aria-hidden="true">{text}</Text>}
          </Note>
        </Tooltip>
      ) : null}
    </Slot>
  );
}

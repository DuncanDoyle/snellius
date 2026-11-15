import type { SerializedStyles } from "@emotion/react";
import styled from "@emotion/styled";
import type { ApiError } from "../api/client";
import { apiErrorState } from "../api/errorState";
import { authCopy } from "../copy/auth";
import { HealthIcon } from "../health/HealthIcon";
import { useVocabulary } from "../shell/extension";
import type { HealthState } from "../theme/tokens";

// region Styles
// Status is the icon plus a full status-tinted border and background; never a left stripe.
const Box = styled.div<{ $state: HealthState; stylesOverride?: SerializedStyles }>`
  display: flex;
  align-items: center;
  gap: ${(p) => p.theme.space(2)};
  margin-bottom: ${(p) => p.theme.space(3)};
  padding: ${(p) => p.theme.space(2)} ${(p) => p.theme.space(3)};
  border-radius: ${(p) => p.theme.radius.md};
  background: ${(p) => p.theme.color.status[p.$state].stripBg};
  border: 1px solid ${(p) => p.theme.color.status[p.$state].stripBorder};
  color: ${(p) => p.theme.color.text};
  font-size: ${(p) => p.theme.font.size.sm};
  overflow-wrap: anywhere;
  ${(p) => p.stylesOverride}
`;

// region Component
interface PageNoticeProps {
  /** The error of the latest poll; the page keeps showing the data it loaded before. */
  error: ApiError;
  /** The page's error title, e.g. "Cannot load findings". */
  title: string;
  /** Replaces the "title: error" text with fixed wording. */
  text?: string;
  stylesOverride?: SerializedStyles;
}

/** Inline notice over data that is still shown after a poll failed. */
export function PageNotice({ error, title, text, stylesOverride }: PageNoticeProps) {
  const state = apiErrorState(error);
  // Both 503 kinds clear by themselves on a later poll.
  const connecting = state === "connecting" || state === "authz-unavailable";
  const { connecting: waiting } = useVocabulary();
  const own =
    state === "authz-unavailable"
      ? `${authCopy.authzUnavailableTitle}. ${authCopy.authzUnavailableBody}`
      : connecting
        ? `${waiting}: ${error.message}`
        : `${title}: ${error.message}`;
  return (
    <Box
      $state={connecting ? "unknown" : "error"}
      role={connecting ? "status" : "alert"}
      data-testid="page-notice"
      stylesOverride={stylesOverride}
    >
      <HealthIcon state={connecting ? "unknown" : "error"} notice />
      {text ?? own}
    </Box>
  );
}

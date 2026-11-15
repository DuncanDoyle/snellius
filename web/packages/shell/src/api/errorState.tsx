import type { SerializedStyles } from "@emotion/react";
import { authCopy } from "../copy/auth";
import { useVocabulary } from "../shell/extension";
import { PageState } from "../states/PageState";
import { ApiError } from "./client";

/** What a failed request means for the page showing it. */
export type ApiErrorKind = "authz-unavailable" | "connecting" | "not-found" | "failed";

/** Classifies a request error: the server could not check permissions, is still connecting (other 503s), 404 or anything else. */
export function apiErrorState(error: unknown): ApiErrorKind {
  if (!(error instanceof ApiError)) return "failed";
  if (error.status === 503) return error.code === "authz-unavailable" ? "authz-unavailable" : "connecting";
  return error.status === 404 ? "not-found" : "failed";
}

// region Component
interface ApiErrorStateProps {
  error: ApiError;
  /** The page's error title, e.g. "Cannot load routes". */
  title: string;
  stylesOverride?: SerializedStyles;
}

/** The full-page state of a request that failed before anything loaded; polling goes on behind it. */
export function ApiErrorState({ error, title, stylesOverride }: ApiErrorStateProps) {
  const vocab = useVocabulary();
  switch (apiErrorState(error)) {
    case "authz-unavailable":
      return (
        <PageState
          kind="connecting"
          title={authCopy.authzUnavailableTitle}
          message={authCopy.authzUnavailableBody}
          stylesOverride={stylesOverride}
        />
      );
    case "connecting":
      return (
        <PageState kind="connecting" title={vocab.connecting} message={error.message} stylesOverride={stylesOverride} />
      );
    default:
      return (
        <PageState
          kind="error"
          title={title}
          message={error.message}
          copyText={error.message}
          stylesOverride={stylesOverride}
        />
      );
  }
}

import type { SerializedStyles } from "@emotion/react";
import { authCopy } from "../copy/auth";
import { useVocabulary } from "../shell/extension";
import { PageState } from "../states/PageState";
import { ApiError } from "./client";

/** What a failed request means for the page showing it. */
export type ApiErrorKind = "authz-unavailable" | "authz-busy" | "connecting" | "not-found" | "failed";

/** Classifies a request error: the server could not check permissions, is too busy to check them yet, is still connecting (other 503s), 404 or anything else. */
export function apiErrorState(error: unknown): ApiErrorKind {
  if (!(error instanceof ApiError)) return "failed";
  if (error.status === 503) {
    if (error.code === "authz-unavailable" || error.code === "authz-busy") return error.code;
    return "connecting";
  }
  return error.status === 404 ? "not-found" : "failed";
}

/** The fixed copy of a permission-check 503, or undefined for other kinds. */
function authzCopy(kind: ApiErrorKind): { title: string; body: string } | undefined {
  if (kind === "authz-unavailable")
    return { title: authCopy.authzUnavailableTitle, body: authCopy.authzUnavailableBody };
  if (kind === "authz-busy") return { title: authCopy.authzBusyTitle, body: authCopy.authzBusyBody };
  return undefined;
}

/** The notice text of a permission-check 503, or undefined for other kinds. */
export function authzNotice(kind: ApiErrorKind): string | undefined {
  const c = authzCopy(kind);
  return c && `${c.title}. ${c.body}`;
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
  const kind = apiErrorState(error);
  const authz = authzCopy(kind);
  if (authz) {
    return <PageState kind="connecting" title={authz.title} message={authz.body} stylesOverride={stylesOverride} />;
  }
  switch (kind) {
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

import { handleUnauthorized, signInStatus, trackAuthRequest } from "../auth/authState";
import { isSecureMode } from "../auth/mode";
import { observeServerDate, trackRequest } from "./clock";
/** Error thrown for non-2xx API responses. */
export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
    /** The sign-in URL a 401 carries. */
    public login?: string,
  ) {
    super(message);
  }
}

/** Fetches JSON from the backend API. */
export async function apiFetch<T>(path: string): Promise<T> {
  return (await apiFetchWithHeaders<T>(path)).body;
}

/** apiFetch, plus the response headers. */
export async function apiFetchWithHeaders<T>(path: string): Promise<{ body: T; headers: Headers }> {
  const settled = trackRequest();
  const authSettled = trackAuthRequest();
  try {
    // In secure mode, protected paths wait for /auth/status, so a 401 never outruns the sign-in pages.
    if (isSecureMode() && (path.startsWith("/api/") || path === "/auth/me")) await signInStatus().catch(() => {});
    return await request<T>(path);
  } finally {
    settled();
    authSettled();
  }
}

async function request<T>(path: string): Promise<{ body: T; headers: Headers }> {
  let res: Response;
  try {
    res = await fetch(path, { headers: { Accept: "application/json" } });
  } catch (e) {
    throw new ApiError(0, `Cannot reach the UI backend: ${(e as Error).message}`);
  }
  if (!res.ok) {
    let msg = res.statusText;
    let login: string | undefined;
    try {
      const body = (await res.json()) as { error?: string; login?: string };
      msg = body.error ?? msg;
      login = body.login;
    } catch {
      // body was not JSON
    }
    if (res.status === 401) handleUnauthorized(msg, login);
    throw new ApiError(res.status, msg, login);
  }
  // Only answers carrying data set the clock their timestamps are read against.
  observeServerDate(res.headers.get("Date"));
  try {
    return { body: (await res.json()) as T, headers: res.headers };
  } catch {
    throw new ApiError(res.status, "The UI backend sent an unreadable response");
  }
}

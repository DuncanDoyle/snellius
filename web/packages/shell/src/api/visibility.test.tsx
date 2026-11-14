import { act, screen, waitFor } from "@testing-library/react";
import { renderWithTheme } from "../test/render";
import { ApiError } from "./client";
import { POLL_MS, retryEveryPoll, useInstalls } from "./hooks";

const setVisibility = (v: "visible" | "hidden") => {
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => v });
  document.dispatchEvent(new Event("visibilitychange"));
};

afterEach(() => {
  setVisibility("visible");
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

function Probe() {
  const { data, error } = useInstalls();
  return <p>{error ? `error ${error.status}` : data ? `n=${data.length}` : "loading"}</p>;
}

it("a scheduled retry waits for a hidden tab to become visible", () => {
  vi.useFakeTimers();
  const revalidate = vi.fn();
  setVisibility("hidden");
  retryEveryPoll(new ApiError(503, "down"), "k", {} as never, revalidate, {} as never);
  vi.advanceTimersByTime(POLL_MS * 4);
  expect(revalidate).not.toHaveBeenCalled();
  setVisibility("visible");
  expect(revalidate).toHaveBeenCalledTimes(1);
});

it("a retry scheduled while visible does not fire if the tab hides before it is due", () => {
  vi.useFakeTimers();
  const revalidate = vi.fn();
  retryEveryPoll(new ApiError(503, "down"), "k", {} as never, revalidate, {} as never);
  vi.advanceTimersByTime(POLL_MS - 100);
  setVisibility("hidden");
  vi.advanceTimersByTime(POLL_MS * 3);
  expect(revalidate).not.toHaveBeenCalled();
  setVisibility("visible");
  expect(revalidate).toHaveBeenCalledTimes(1);
});

it("polling stops while hidden and resumes when visible; no fetch fires while hidden after an error", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  let calls = 0;
  let ok = false;
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => {
      calls++;
      return ok
        ? new Response(JSON.stringify([{ id: "a.one" }]), { status: 200 })
        : new Response(JSON.stringify({ error: "down" }), { status: 503 });
    }),
  );
  renderWithTheme(<Probe />);
  await waitFor(() => expect(screen.getByText("error 503")).toBeInTheDocument());
  setVisibility("hidden");
  const seen = calls;
  await act(async () => {
    await vi.advanceTimersByTimeAsync(POLL_MS * 5);
  });
  expect(calls).toBe(seen);
  ok = true;
  await act(async () => {
    setVisibility("visible");
  });
  await waitFor(() => expect(screen.getByText("n=1")).toBeInTheDocument());
});

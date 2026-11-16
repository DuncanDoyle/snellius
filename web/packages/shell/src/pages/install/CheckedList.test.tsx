import { screen, within } from "@testing-library/react";
import type { CheckResult } from "../../api/types";
import { diagnosticsCopy } from "../../copy/explorer";
import { renderWithTheme } from "../../test/render";
import { CheckedList } from "./CheckedList";

const now = Date.parse("2026-10-17T12:00:00Z");

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(now);
});

afterEach(() => {
  vi.useRealTimers();
});

function check(extra: Partial<CheckResult> = {}): CheckResult {
  return { name: "backend endpoints", ok: true, required: false, collectedAt: new Date(now).toISOString(), ...extra };
}

const held = check({
  hold: { until: "2026-10-17T12:00:25Z", count: 2, reason: "Waiting for backends to get ready endpoints" },
});

function item() {
  return within(screen.getByRole("list", { name: "Checks" })).getByRole("listitem");
}

it("an OK check with a hold is deciding, with how many and when", () => {
  renderWithTheme(<CheckedList checks={[held]} />);
  expect(item()).toHaveTextContent(/^Deciding: backend endpoints\s*Deciding on 2 objects, in 25s$/);
  expect(within(item()).getByTestId("check-deciding")).toBeInTheDocument();
  expect(within(item()).queryByTestId("health-icon-healthy")).toBeNull();
  expect(within(item()).queryByTestId("health-icon-unknown")).toBeNull();
});

it("an OK check without a hold is healthy", () => {
  renderWithTheme(<CheckedList checks={[check()]} />);
  expect(item()).toHaveTextContent(/^Healthy: backend endpoints$/);
  expect(within(item()).getByTestId("health-icon-healthy")).toBeInTheDocument();
});

it("stale data shows a held check as unknown", () => {
  renderWithTheme(<CheckedList checks={[held]} stale />);
  expect(item()).toHaveTextContent(/^Unknown: backend endpoints$/);
  expect(within(item()).getByTestId("health-icon-unknown")).toBeInTheDocument();
  expect(within(item()).queryByTestId("check-deciding")).toBeNull();
});

it("a check a restricted viewer may not see is Not checked, not healthy", () => {
  renderWithTheme(<CheckedList checks={[check({ notAssessed: true })]} />);
  expect(item()).toHaveTextContent(/^backend endpoints\s*Not checked$/);
  expect(within(item()).getByTestId("check-not-assessed")).toBeInTheDocument();
  expect(within(item()).getByText("Not checked")).toHaveAttribute("title", diagnosticsCopy.notCheckedReason);
  expect(within(item()).queryByTestId("health-icon-healthy")).toBeNull();
  expect(within(item()).queryByTestId("health-icon-unknown")).toBeNull();
});

it("stale data leaves a Not checked check as it is", () => {
  renderWithTheme(<CheckedList checks={[check({ notAssessed: true })]} stale />);
  expect(within(item()).getByTestId("check-not-assessed")).toBeInTheDocument();
  expect(within(item()).queryByTestId("health-icon-unknown")).toBeNull();
});

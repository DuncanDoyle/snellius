import { screen, within } from "@testing-library/react";
import { hrefPath, renderWithTheme } from "../test/render";
import { ProblemStrip } from "./ProblemStrip";

const ID = "kgw.kgateway";
const reviews = { group: "", kind: "Service", namespace: "demo", name: "reviews", link: "backend" as const };

it("a backend problem links the backend and quotes its finding's message", () => {
  renderWithTheme(
    <ProblemStrip
      installId={ID}
      state="warning"
      reason={{
        kind: "backend-problem",
        code: "KGWUI-023",
        message: "This backend has no ready endpoints",
        backend: reviews,
      }}
    />,
  );
  const line = screen.getByTestId("problem-reason");
  expect(line).toHaveTextContent("Backenddemo/reviewsThis backend has no ready endpoints");
  expect(hrefPath(within(line).getByRole("link", { name: "demo/reviews" }))).toBe(
    `/i/${ID}/backends/-/Service/demo/reviews`,
  );
  expect(within(line).queryByRole("link", { name: "Open finding" })).toBeNull();
});

it("a backend the caller may not view is neither named nor linked", () => {
  renderWithTheme(
    <ProblemStrip installId={ID} state="warning" reason={{ kind: "backend-problem", code: "KGWUI-023" }} />,
  );
  const line = screen.getByTestId("problem-reason");
  expect(line).toHaveTextContent(/^KGWUI-023: A backend you can't view has a problem\.$/);
  expect(within(line).queryByRole("link")).toBeNull();
});

const edge = {
  group: "gateway.networking.k8s.io",
  kind: "Gateway",
  namespace: "infra",
  name: "edge",
  link: "detail" as const,
};

it("names the deciding signal before the reason and links the rejecting Gateway", () => {
  renderWithTheme(
    <ProblemStrip
      installId={ID}
      state="warning"
      signal="Traffic path"
      reason={{ kind: "gateway-rejecting" }}
      gateway={edge}
    />,
  );
  const line = screen.getByTestId("problem-reason");
  expect(within(line).getByTestId("problem-signal")).toHaveTextContent("Traffic path");
  expect(line).toHaveTextContent("The parent Gateway rejects config.");
  expect(hrefPath(within(line).getByRole("link", { name: /View Gateway/ }))).toBe(`/i/${ID}/gateways/infra/edge`);
});

it("an incomplete path alone says only that the viewer cannot check it", () => {
  renderWithTheme(
    <ProblemStrip
      installId={ID}
      state="unknown"
      signal="Traffic path"
      reason={{ kind: "path-incomplete" }}
      incomplete
    />,
  );
  expect(screen.getByTestId("problem-reason")).toHaveTextContent("You don't have permission to check the whole path.");
  expect(screen.queryByTestId("problem-incomplete")).toBeNull();
});

it("a readable fault on an incomplete path adds that the assessment is incomplete", () => {
  renderWithTheme(
    <ProblemStrip
      installId={ID}
      state="error"
      signal="Traffic path"
      reason={{ kind: "path-problem", code: "KGWUI-013" }}
      incomplete
    />,
  );
  expect(screen.getByTestId("problem-reason")).toHaveTextContent("Traffic path has errors");
  expect(screen.getByTestId("problem-incomplete")).toHaveTextContent(
    "Assessment incomplete with your current permissions.",
  );
});

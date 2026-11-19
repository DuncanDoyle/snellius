import { screen } from "@testing-library/react";
import { shellCopy } from "../copy/shell";
import { overview } from "../test/fixtures";
import { renderWithTheme } from "../test/render";
import { InstallName, installLabel } from "./InstallName";

it("shows namespace/name when the details are readable", () => {
  renderWithTheme(<InstallName install={overview().header} />);
  expect(screen.getByText("kgateway")).toBeInTheDocument();
  expect(screen.queryByText(shellCopy.detailsHidden)).toBeNull();
});

it("shows the ID as a neutral name and says the details are hidden", () => {
  const hidden = {
    id: "kgw.kgateway",
    displayName: "",
    version: "",
    edition: "",
    controllerNames: [],
    readiness: { state: "unknown" as const },
    detailsHidden: true,
  };
  renderWithTheme(<InstallName install={hidden} />);
  expect(screen.getByText("kgw.kgateway")).toBeInTheDocument();
  expect(screen.getByText(shellCopy.detailsHidden)).toBeInTheDocument();
  expect(installLabel(hidden)).toBe("kgw.kgateway");
});

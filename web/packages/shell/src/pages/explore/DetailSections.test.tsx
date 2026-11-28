import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { BrowserRouter } from "react-router-dom";
import { sectionsCopy } from "../../copy/explorer";
import { CHIP_PRESSED_ACTIVE_PCT } from "../../shell/constants";
import { contrastRatio } from "../../theme/contrast";
import { ThemeProvider } from "../../theme/ThemeProvider";
import { controlRoomDark, controlRoomLight, defaultThemes } from "../../theme/themes";
import { type DetailSection, DetailSections, NAV_HOVER_PCT, NAV_PRESS_PCT, SECTION_GROUPS } from "./DetailSections";

// region Helpers
const LABEL = "Route sections";

const sec = (id: string, group: DetailSection["group"], extra: Partial<DetailSection> = {}): DetailSection => ({
  id,
  group,
  title: id,
  render: () => <p>{`${id} body`}</p>,
  ...extra,
});

// A browser router, so section clicks reach window.location and the history stack.
const mount = (sections: DetailSection[]) =>
  render(
    <ThemeProvider themes={defaultThemes}>
      <BrowserRouter>
        <DetailSections sections={sections} label={LABEL} />
      </BrowserRouter>
    </ThemeProvider>,
  );

// One observer instance per mount; fire() hands it entries for the named cards.
let observer: { cb: IntersectionObserverCallback; els: Element[] } | undefined;
class FakeObserver {
  constructor(cb: IntersectionObserverCallback) {
    observer = { cb, els: [] };
  }
  observe(el: Element) {
    observer?.els.push(el);
  }
  unobserve() {}
  disconnect() {}
}
const fire = (visible: string[]) =>
  act(() => {
    const entries = (observer?.els ?? []).map((el) => ({ target: el, isIntersecting: visible.includes(el.id) }));
    observer?.cb(entries as unknown as IntersectionObserverEntry[], {} as IntersectionObserver);
  });

const nav = () => screen.getByRole("navigation", { name: LABEL });
const link = (name: string) => within(nav()).getByRole("link", { name: new RegExp(`^${name}`) });

let scrolled: Element[] = [];
beforeEach(() => {
  window.history.replaceState(null, "", "/i/kgw/routes/x?tab=overview");
  scrolled = [];
  observer = undefined;
  Element.prototype.scrollIntoView = vi.fn(function (this: Element) {
    scrolled.push(this);
  });
  vi.stubGlobal("IntersectionObserver", FakeObserver);
});
afterEach(() => {
  vi.unstubAllGlobals();
});

// Same result as CSS color-mix(in srgb, a w, b) for #RRGGBB inputs.
function mix(a: string, b: string, w: number): string {
  const ch = (h: string, i: number) => Number.parseInt(h.slice(1 + 2 * i, 3 + 2 * i), 16);
  const out = [0, 1, 2].map((i) => Math.round(ch(a, i) * w + ch(b, i) * (1 - w)));
  return `#${out.map((v) => v.toString(16).padStart(2, "0")).join("")}`;
}

// region Tests
it("renders cards in group order, then list order", () => {
  mount([
    sec("route-policies", "actsOn"),
    sec("route-summary", "status"),
    sec("route-rules", "does"),
    sec("route-serves", "where"),
    sec("route-effective", "does"),
    sec("route-parents", "where"),
  ]);
  expect(screen.getAllByRole("region").map((r) => r.id)).toEqual([
    "route-serves",
    "route-parents",
    "route-summary",
    "route-rules",
    "route-effective",
    "route-policies",
  ]);
  expect(SECTION_GROUPS).toEqual(["where", "status", "does", "runtime", "actsOn", "related"]);
});

it("nav lists every section with its group label and count", () => {
  mount([
    sec("route-rules", "does", { title: "Rules", count: 12 }),
    sec("route-parents", "where", { title: "Parents", count: 1 }),
  ]);
  const n = nav();
  expect(within(n).getByText(sectionsCopy.groups.where)).toBeInTheDocument();
  expect(within(n).getByText(sectionsCopy.groups.does)).toBeInTheDocument();
  for (const g of ["status", "runtime", "actsOn", "related"] as const)
    expect(within(n).queryByText(sectionsCopy.groups[g])).not.toBeInTheDocument();
  const links = within(n).getAllByRole("link");
  expect(links.map((a) => a.getAttribute("href"))).toEqual(["#route-parents", "#route-rules"]);
  expect(links.map((a) => a.textContent)).toEqual(["Parents1", "Rules12"]);
  // The card header carries the same count.
  expect(within(screen.getByRole("region", { name: /Rules/ })).getByText("12")).toBeInTheDocument();
});

it("an absent count shows no badge", () => {
  mount([sec("route-summary", "status", { title: "Summary" })]);
  expect(link("Summary").textContent).toBe("Summary");
  expect(screen.queryByTestId("count-placeholder")).not.toBeInTheDocument();
});

it("loading count renders a placeholder in nav and header", () => {
  mount([sec("route-rules", "does", { count: "loading" })]);
  expect(screen.getAllByTestId("count-placeholder")).toHaveLength(2);
});

it("zero count is muted but present", () => {
  mount([
    sec("route-policies", "actsOn", { title: "Policies", count: 0, muted: true, render: () => <p>No policies</p> }),
  ]);
  expect(screen.getByRole("region", { name: /Policies/ })).toHaveTextContent("No policies");
  expect(link("Policies")).toHaveAttribute("data-muted", "true");
  expect(link("Policies")).toHaveTextContent("0");
});

it("clicking a nav entry scrolls to the card and sets the hash", () => {
  mount([sec("route-rules", "does"), sec("route-policies", "actsOn")]);
  fireEvent.click(link("route-policies"));
  expect(scrolled).toEqual([document.getElementById("route-policies")]);
  expect(window.location.hash).toBe("#route-policies");
  // Only the hash changes; the query string stays.
  expect(window.location.search).toBe("?tab=overview");
  expect(link("route-policies")).toHaveAttribute("aria-current", "location");
});

it("highlights the section in view", () => {
  mount([sec("route-rules", "does"), sec("route-policies", "actsOn")]);
  expect(link("route-rules")).toHaveAttribute("aria-current", "location");
  fire(["route-policies"]);
  expect(link("route-policies")).toHaveAttribute("aria-current", "location");
  expect(link("route-rules")).not.toHaveAttribute("aria-current");
  // With several in the band, the first in page order wins.
  fire(["route-rules", "route-policies"]);
  expect(link("route-rules")).toHaveAttribute("aria-current", "location");
});

it("jumps to a tall card and to the last, short card", () => {
  mount([sec("route-rules", "does"), sec("route-policies", "actsOn"), sec("route-children", "related")]);
  fireEvent.click(link("route-rules"));
  expect(scrolled.at(-1)?.id).toBe("route-rules");
  expect(link("route-rules")).toHaveAttribute("aria-current", "location");
  fireEvent.click(link("route-children"));
  expect(scrolled.at(-1)?.id).toBe("route-children");
  // The short last card never reaches the band, yet it stays current.
  fire(["route-policies"]);
  expect(link("route-children")).toHaveAttribute("aria-current", "location");
  // After the user scrolls on their own, the band decides again; at the page bottom the last card wins.
  fireEvent.wheel(window);
  fire(["route-rules"]);
  expect(link("route-rules")).toHaveAttribute("aria-current", "location");
  Object.defineProperty(document.documentElement, "scrollHeight", { configurable: true, value: 3000 });
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
  Object.defineProperty(window, "scrollY", { configurable: true, value: 2200 });
  fireEvent.scroll(window);
  expect(link("route-children")).toHaveAttribute("aria-current", "location");
  Object.defineProperty(window, "scrollY", { configurable: true, value: 0 });
});

it("Back and Forward move between section hashes", async () => {
  mount([sec("route-rules", "does"), sec("route-policies", "actsOn")]);
  fireEvent.click(link("route-rules"));
  fireEvent.click(link("route-policies"));
  expect(window.location.hash).toBe("#route-policies");
  scrolled = [];
  act(() => window.history.back());
  await waitFor(() => expect(window.location.hash).toBe("#route-rules"));
  await waitFor(() => expect(scrolled.at(-1)?.id).toBe("route-rules"));
  expect(link("route-rules")).toHaveAttribute("aria-current", "location");
  act(() => window.history.forward());
  await waitFor(() => expect(scrolled.at(-1)?.id).toBe("route-policies"));
  expect(window.location.search).toBe("?tab=overview");
});

it("a hash in the URL scrolls to its section on mount", () => {
  window.history.replaceState(null, "", "/i/kgw/routes/x?tab=overview#route-policies");
  mount([sec("route-rules", "does"), sec("route-policies", "actsOn")]);
  expect(scrolled.map((e) => e.id)).toEqual(["route-policies"]);
  expect(link("route-policies")).toHaveAttribute("aria-current", "location");
});

it("narrow window shows the Sections control with the same entries", () => {
  vi.stubGlobal("matchMedia", (q: string) => ({
    matches: q.includes("max-width"),
    addEventListener: () => {},
    removeEventListener: () => {},
  }));
  const sections = [sec("route-serves", "where"), sec("route-rules", "does"), sec("route-policies", "actsOn")];
  mount(sections);
  expect(screen.queryByRole("navigation", { name: LABEL })).not.toBeInTheDocument();
  const toggle = screen.getByRole("button", { name: sectionsCopy.sections });
  expect(toggle).toHaveAttribute("aria-expanded", "false");
  fireEvent.click(toggle);
  expect(toggle).toHaveAttribute("aria-expanded", "true");
  const hrefs = within(nav())
    .getAllByRole("link")
    .map((a) => a.getAttribute("href"));
  expect(hrefs).toEqual(["#route-serves", "#route-rules", "#route-policies"]);
  // Choosing an entry jumps there and folds the list again.
  fireEvent.click(link("route-rules"));
  expect(scrolled.at(-1)?.id).toBe("route-rules");
  expect(toggle).toHaveAttribute("aria-expanded", "false");
  // Cards keep the same order as on a wide window.
  expect(screen.getAllByRole("region").map((r) => r.id)).toEqual(sections.map((s) => s.id));
});

describe.each([controlRoomDark, controlRoomLight])("$name nav entries meet WCAG AA", (t) => {
  const c = t.color;
  // Entries sit on the page in the wide nav and on the raised panel in the narrow one.
  it.each([
    ["page", c.bg],
    ["panel", c.surfaceRaised],
  ])("on the %s", (_, base) => {
    const hover = mix(c.text, base, NAV_HOVER_PCT / 100);
    const fills = [
      hover,
      mix(c.text, base, NAV_PRESS_PCT / 100),
      mix(c.accentText, base, CHIP_PRESSED_ACTIVE_PCT / 100),
    ];
    for (const fill of fills) expect(contrastRatio(c.text, fill)).toBeGreaterThanOrEqual(4.5);
    // Muted titles and counts turn to full text when pressed or current, so only hover carries muted text.
    expect(contrastRatio(c.textMuted, base)).toBeGreaterThanOrEqual(4.5);
    expect(contrastRatio(c.textMuted, hover)).toBeGreaterThanOrEqual(4.5);
  });
});

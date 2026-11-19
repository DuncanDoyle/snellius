import { keyframes } from "@emotion/react";
import styled from "@emotion/styled";
import { type CSSProperties, useEffect, useState } from "react";
import { isSecureMode } from "../auth/mode";
import { takeFirstArrival, takeSignedInMarker } from "../auth/signedIn";
import { useShellExtension } from "./extension";

// region Styles
/**
 * Full length of the intro; the overlay fades out over its last FADE_MS. The drawing ends at about
 * 1.27 s, so the finished logo holds for about 0.7 s before the fade.
 */
export const INTRO_MS = 2250;
const FADE_MS = 300;
const SKIP_FADE_MS = 150;
/** Keys a skip uses up, because they would otherwise scroll the page or activate a control. */
const CONSUMED_KEYS = new Set([" ", "Enter"]);

const draw = keyframes`from { stroke-dashoffset: 1; } to { stroke-dashoffset: 0; }`;
// Once drawn, the outline is one solid stroke, so the dash's ends leave no notch at the apex.
const solid = keyframes`to { stroke-dasharray: none; }`;
// A line stays hidden until it draws, or its round cap shows as a dot.
const trace = keyframes`from { stroke-dashoffset: 1; visibility: visible; } to { stroke-dashoffset: 0; visibility: visible; }`;
const appear = keyframes`from { opacity: 0; } to { opacity: 1; }`;
const fadeOut = keyframes`to { opacity: 0; }`;
const ripple = keyframes`from { transform: scale(0.48); opacity: 0.9; } to { transform: scale(2.15); opacity: 0; }`;
const rise = keyframes`from { opacity: 0; transform: translateY(48px); } to { opacity: 1; transform: none; }`;

const easeInOut = "cubic-bezier(0.45, 0, 0.55, 1)";
const easeIn = "cubic-bezier(0.32, 0, 0.67, 0)";
const easeOut = "cubic-bezier(0.33, 1, 0.68, 1)";
const easeOutStrong = "cubic-bezier(0.22, 1, 0.36, 1)";

// The logo's own colours per mode, as in the light and dark symbol files.
const ink = {
  dark: { line: "#FFFFFF", fill: "#07365B", halo: "#D8EDFF" },
  light: { line: "#0B2545", fill: "#DDF4FA", halo: "#0B2545" },
};

const Overlay = styled.div<{ leaving: boolean }>`
  position: fixed;
  inset: 0;
  z-index: 2000;
  display: grid;
  place-items: center;
  padding: ${(p) => p.theme.space(4)};
  background: ${(p) => p.theme.color.bg};
  cursor: pointer;
  animation: ${fadeOut} ${(p) => (p.leaving ? `${SKIP_FADE_MS}ms 0ms` : `${FADE_MS}ms ${INTRO_MS - FADE_MS}ms`)} ease-out
    forwards;
  ${(p) => p.leaving && "pointer-events: none;"}
`;

// The timing follows the launch film's opening, compressed to fit INTRO_MS.
const Optics = styled.svg`
  width: min(560px, 100%);
  height: auto;
  overflow: visible;
  .trace { fill: none; stroke-width: 12; stroke-linecap: round; stroke-dasharray: 1; visibility: hidden; animation: ${trace} forwards; }
  .beam, .beam-halo { stroke: ${(p) => ink[p.theme.mode].line}; animation-duration: 0.32s; animation-delay: 0.29s; animation-timing-function: ${easeIn}; }
  .beam-halo { stroke: ${(p) => ink[p.theme.mode].halo}; stroke-width: 25; opacity: 0.19; }
  .ray, .ray-halo { animation-duration: 0.36s; animation-timing-function: ${easeOut}; animation-delay: calc(0.64s + var(--i) * 0.09s); }
  .ray-halo { stroke: var(--band); stroke-width: 30; opacity: 0.32; }
  .ray { stroke: ${(p) => (p.theme.mode === "dark" ? "var(--bright)" : "var(--band)")}; animation-delay: calc(0.66s + var(--i) * 0.09s); }
  .prism-outline { stroke: ${(p) => ink[p.theme.mode].line}; stroke-dasharray: 1; animation: ${draw} 0.47s ${easeInOut} 0.02s both, ${solid} 0s linear 0.49s forwards; }
  .prism-fill, .prism-eye { fill: ${(p) => ink[p.theme.mode].fill}; animation: ${appear} 0.26s ${easeOut} 0.25s both; }
  .prism-eye { stroke: ${(p) => ink[p.theme.mode].line}; }
  .prism-band { animation: ${appear} 0.17s ${easeOut} both; animation-delay: calc(0.58s + var(--i) * 0.047s); }
  .arrival { fill: none; stroke: ${(p) => ink[p.theme.mode].line}; stroke-width: 4; opacity: 0; transform-box: fill-box; transform-origin: center; animation: ${ripple} 0.39s ${easeOut} 0.6s forwards; }
  .wordmark { fill: ${(p) => p.theme.color.text}; font-family: ${(p) => p.theme.font.ui}; font-weight: ${(p) => p.theme.font.weight.bold}; animation: ${rise} 0.53s ${easeOutStrong} 0.64s both; }
`;

// region Helpers
const PRISM = "M128 19 Q132 19 135 25 L244 217 Q250 230 237 230 H19 Q6 230 13 217 L121 25 Q124 19 128 19 Z";
const BANDS: [string, string][] = [
  ["159,83 174,109", "#0789D2"],
  ["174,109 193,140", "#0FB8F1"],
  ["193,140 205,165", "#0877AE"],
  ["205,165 219,186", "#11C9BB"],
  ["219,186 231,209", "#078B9E"],
  ["231,209 239,222", "#FFE42A"],
  ["239,222 236,230", "#FFB729"],
];
const RAYS: { d: string; band: string; bright: string }[] = [
  { d: "M392 173L765 30", band: "#0789D2", bright: "#45B4F8" },
  { d: "M420 210L813 137", band: "#0FB8F1", bright: "#66E1FF" },
  { d: "M462 280L813 319", band: "#11C9BB", bright: "#67E8D8" },
  { d: "M494 333L757 444", band: "#FFE42A", bright: "#FFED71" },
];
const vars = (v: Record<string, string | number>) => v as CSSProperties;

/**
 * Takes the signed-in marker (so the URL is clean either way) and plays the intro unless motion is reduced:
 * after a fresh sign-in in secure mode, and on a tab's first page load in local mode, which has no sign-in.
 */
export function shouldPlayIntro(): boolean {
  const fresh = takeSignedInMarker();
  const arrival = isSecureMode() ? fresh : takeFirstArrival();
  return arrival && !window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;
}

// region Component
/**
 * The prism intro after a fresh sign-in: an overlay over the already rendering app that fades out by itself.
 * A click or any key skips it; it is decorative, so assistive technology never sees it.
 */
export function SignInIntro() {
  const { brand } = useShellExtension();
  const [phase, setPhase] = useState<"playing" | "leaving" | "done">("playing");
  useEffect(() => {
    if (phase === "done") return;
    const timer = setTimeout(() => setPhase("done"), phase === "playing" ? INTRO_MS : SKIP_FADE_MS);
    if (phase === "leaving") return () => clearTimeout(timer);
    // Space and Enter are used up so they don't also scroll or activate; every other key, Tab included, keeps its effect.
    const skip = (e: Event) => {
      if (e instanceof KeyboardEvent && CONSUMED_KEYS.has(e.key) && !(e.ctrlKey || e.metaKey || e.altKey)) {
        e.preventDefault();
        e.stopPropagation();
      }
      setPhase("leaving");
    };
    window.addEventListener("pointerdown", skip, true);
    window.addEventListener("keydown", skip, true);
    return () => {
      clearTimeout(timer);
      window.removeEventListener("pointerdown", skip, true);
      window.removeEventListener("keydown", skip, true);
    };
  }, [phase]);
  if (phase === "done") return null;
  return (
    <Overlay data-testid="signin-intro" aria-hidden="true" leaving={phase === "leaving"}>
      <Optics viewBox="0 0 850 560">
        <path d="M9 273H238" className="trace beam-halo" pathLength={1} />
        <path d="M9 273H238" className="trace beam" pathLength={1} />
        {RAYS.map((r, i) => (
          <g key={r.d} style={vars({ "--band": r.band, "--bright": r.bright, "--i": i })}>
            <path d={r.d} className="trace ray-halo" pathLength={1} />
            <path d={r.d} className="trace ray" pathLength={1} />
          </g>
        ))}
        <g transform="translate(161 38) scale(1.43)">
          <path className="prism-fill" d={PRISM} />
          {BANDS.map(([pts, fill], i) => (
            <polygon key={pts} className="prism-band" points={`54,164 ${pts}`} fill={fill} style={vars({ "--i": i })} />
          ))}
          <path
            className="prism-outline"
            pathLength={1}
            d={PRISM}
            fill="none"
            strokeWidth={11}
            strokeLinejoin="round"
          />
          <circle className="prism-eye" cx={54} cy={164} r={20} strokeWidth={11} />
        </g>
        <circle className="arrival" cx={238} cy={273} r={32} />
        <text className="wordmark" x={425} y={540} textAnchor="middle" fontSize={88}>
          {brand.productName}
        </text>
      </Optics>
    </Overlay>
  );
}

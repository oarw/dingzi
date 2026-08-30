# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Stack

Go backend, single binary. Web UI is served from `embed`ded static assets with
**no build step and no CDN** — hand-written HTML/CSS/vanilla JS. Sparklines are
inline SVG written by hand. (Confirmed: the no-CDN rule is a security decision,
recorded in HANDOFF.md §2A.5. The no-build-step choice follows from the
single-binary requirement.)

## Users

Self-hosting operators running a small fleet of VPS instances — individual
developers and hobbyist sysadmins, not enterprise SRE teams. Fleet size is
roughly 5–50 machines. Primary language is Chinese; the UI ships in Chinese.

They are migrating off 哪吒 v0 (Nezha v0) because it accumulated too many
operational bugs. They already know what a monitoring panel is and what every
field means — this is a replacement for a tool they use daily, not an
introduction to the category.

*Inferred from the request and the fleet sizes the architecture targets: the
5–50 range and the hobbyist-vs-enterprise split were not stated explicitly.*

## Product Purpose

Answer one question at a glance: **is anything in my fleet in trouble right
now?** Secondary: is any machine about to blow through its monthly bandwidth
quota.

Success is the operator learning a machine is unhealthy without clicking
anything. If they must open a detail page to discover a problem, the panel
failed.

## Positioning

Things a neighboring panel could not truthfully claim:

- **WebSocket over HTTPS transport**, not gRPC — works behind Cloudflare and
  nginx with zero special configuration, and shares one port with the web UI.
- **Single binary, single port, no CGO** — `GOOS=linux go build` cross-compiles
  with no C toolchain.
- **Memory bounded by construction** — fixed-size ring buffers mean resident
  memory scales with machine count, never with uptime or traffic.
- **No remote command execution**, deliberately. A compromised panel cannot
  become fleet-wide RCE.

## Operating Context

- The panel is often **left open on a screen or wall-mounted TV** for long
  stretches, not opened per-task. Glanceability outranks interaction depth.
- Monitored machines are **budget VPS instances with monthly bandwidth caps**,
  which is why traffic accounting has a configurable billing period and count
  mode rather than a single cumulative number.
- Machines are geographically spread; names in real fleets tend to encode
  region and role (`tokyo-1`, `sfo-web`, `fra-db`).
- Operators reach the panel from **both desktop and phone** — desktop for
  working sessions, phone for checking in after an alert.

## Capabilities and Constraints

**Ten fields per machine, all required on the main list** (named explicitly by
the author): CPU, memory, swap, disk, network speed, traffic + quota, system
info, load average, uptime, online status.

Confirmed decisions:

- **Card-style blocks** for the machine list, in a responsive grid (author's
  choice over a denser single-row table).
- **Monthly traffic accumulation with configurable quota** per machine:
  `LimitBytes`, `ResetDay` (1–31, clamped at month end), `CountMode`
  (`sum | out | max`, default `sum`).
- **Theme follows the OS**, with a three-state manual override (auto/light/dark)
  in `localStorage`.
- **No `exec` task type.** Reachability checks only (ping / TCP / HTTP).
- Secrets are **generated, never chosen** — first boot prints a random admin
  password and agent key.

Undecided (must not be invented): metric retention and downsampling
parameters; whether monitors are assigned to specific agents or run on all;
the alert-rule expression format; online-terminal implementation.

## Brand Commitments

- Name: **Dingzi / 钉子**. A nail — small, driven in, holds.
- MIT licensed, open source.
- UI copy in Chinese.

## Evidence on Hand

**No real fleet data exists.** All machine names, metrics, and traffic figures
in mockups are synthetic and must be labeled as such. Nothing may imply real
uptime records, real customers, or benchmark results.

Real and citable: the architecture decisions and their rationale, recorded in
`README.md` and `HANDOFF.md`.

## Product Principles

1. **Delete the decision, don't prettify it.** Every configuration option is a
   decision pushed onto the operator and a place to get it wrong. Generate
   secrets, follow the OS theme, auto-register machines.
2. **Glanceable beats explorable.** The panel is an instrument read from across
   a room, not an app to navigate.
3. **Unequal weight for unequal information.** Saturation metrics signal
   trouble and must be loud; static facts are reference and must be quiet.
   Treating all ten fields identically is what makes existing panels hard to
   scan.
4. **Bounded by construction, not by tuning.** Anything that can grow without
   limit is a bug, not a configuration opportunity.
5. **Safe by default, dangerous only on request.** Blast radius is a design
   parameter.

## Accessibility & Inclusion

- **Status must never rely on color alone** — an offline or over-threshold
  machine carries a shape or text marker as well. Operators reading a panel
  from across a room, and color-blind operators, both depend on this.
- Readable at a distance: the primary state indicators need to survive being
  viewed on a wall display.
- Keyboard reachable; respects `prefers-reduced-motion`.

*Inferred: no specific standard (WCAG level) was named by the author.*

# Robotics / ROS 2 Positioning Upgrade — Design

**Date:** 2026-06-28
**Author:** Joannis Orlandos (with Claude)
**Status:** Design — pending implementation plan

## Problem

"Make our robotics page clear that it's ROS 2 compatible, and the best way for
robotics." Two surfaces carry the robotics story today, and each has the opposite
gap:

- **Marketing — `/solutions/robots`**
  (`cloud-marketing-configurator/marketing/src/app/solutions/robots/page.tsx`):
  generic, aspirational robotics copy ("Autonomous Navigation", "Precision
  Manipulation") that **never mentions ROS 2** and is not grounded in anything
  WendyOS actually does. It reads as boilerplate, not "the best way to build ROS 2
  robots." (Note: the live URL is `/solutions/robots`; `wendy.dev/robotics` 404s.)

- **Docs — "Robotics" section**
  (`wendyos/go/internal/cli/assets/docs/integrations/`): `ros2.mdx` is an
  excellent CLI command reference and `foxglove.mdx` is a "coming soon" preview,
  but there is **no overview/landing page** that positions WendyOS for robotics,
  and ROS 2 *compatibility* is buried inside a "How it works" paragraph rather than
  stated as a headline promise.

This is a **positioning + structure** upgrade, not a missing-features upgrade.

## Goals

1. Make ROS 2 compatibility an explicit, up-front fact on both surfaces.
2. Position WendyOS as the best way to take ROS 2 robots **from prototype to
   production fleet**.
3. Keep the two surfaces telling one consistent story.

## Non-goals

- No new marketing UI components (reuse `SolutionHero`, `SolutionFeatures`,
  `ProfessionalServicesCta`).
- No new docs framework work; stay within the existing Fumadocs setup.
- No changes to the `/solutions/robots` **URL** (avoid breaking inbound links).
- No unrelated refactors of either site.

## Positioning spine (shared across both surfaces)

**Lead message:** *ROS 2, from prototype to production fleet — on the open stack
you already use.*

Three proof pillars (priority order):

1. **Open, standard ROS 2** — real upstream ROS 2, any RMW (CycloneDDS / Fast DDS),
   Apache-2.0, no lock-in; works with the tools you already use (Foxglove, rosbag).
2. **Deploy in one command** — ship containerized ROS 2 stacks to NVIDIA Jetson and
   Raspberry Pi with `wendy run`; no flashing, no `source setup.bash`, no toolchain
   hell.
3. **Production from day one** — OTA updates, multi-device fleets, fine-grained
   entitlements (GPU / camera / etc.).
   - *Supporting proof:* operate live ROS 2 remotely with **no SSH** — nodes,
     topics, echo, params, services, rosbags, Foxglove — exactly what the docs
     already document.

The headline leads with the **prototype → production** lifecycle; deploy speed and
fleet/OTA/security are the body that backs it up.

## Design

### Surface 1 — Marketing: `/solutions/robots`

Reuse existing components; this is a content rewrite plus one nav edit.

**Hero (`SolutionHero` props):**
- `subtitle`: `Robotics · ROS 2`
- `title`: lifecycle-led headline naming ROS 2 (e.g. "ROS 2 robots, from prototype
  to production fleet").
- `description`: 2–3 sentences grounded in real capability — standard ROS 2,
  one-command deploy to Jetson/Pi, fleet + OTA.
- `ctaText` / `ctaHref`: point the primary CTA at the docs Robotics overview
  (`/docs/integrations` overview page) rather than the generic installer. Secondary
  "Contact Sales" CTA unchanged.

**Features (`SolutionFeatures`, 6 items, mapped to the pillars):**
1. **Native, standard ROS 2** — any RMW (CycloneDDS / Fast DDS), Apache-2.0, no
   lock-in. (icon: e.g. `Network`/`Boxes`)
2. **One-command deploy** — containerized ROS 2 stacks to Jetson & Pi, no flashing.
   (icon: `Rocket`)
3. **Fleet management + OTA** — update entire robot fleets centrally. (icon:
   `Layers`)
4. **Fine-grained entitlements** — scoped access to GPU, camera, audio, storage.
   (icon: `Fingerprint`)
5. **Inspect & debug live, no SSH** — nodes, topics, echo, params, rosbags from your
   machine; links to `/docs/integrations/ros2`. (icon: `Terminal`/`Activity`)
6. **Visualize in Foxglove** — bridge live topics into Foxglove Studio; links to
   `/docs/integrations/foxglove`. (icon: `LineChart`/`Eye`)

Feature `title`/`description` should reuse the pillar language above. Icons drawn
from `lucide-react` (already the convention).

**Section heading:** retitle the features block to match the spine (e.g. title
"From prototype to production", description naming ROS 2).

**Navbar (`src/components/layout/navbar.tsx`):**
- Rename the "Use Cases" sub-item label `Robots` → `Robotics`.
- Update its `description` from "Automation and physical assistance" to ROS 2-forward
  copy (e.g. "Build & ship ROS 2 robots").
- Leave `href: '/solutions/robots'` unchanged.

### Surface 2 — Docs: Robotics section

Directory: `wendyos/go/internal/cli/assets/docs/integrations/`

**New overview/landing page** (`index.mdx` for the section, or `overview.mdx` if an
index isn't supported by the Fumadocs `prepare-content.mjs` flow — see Open
Questions):
- Frontmatter title "Robotics on WendyOS" (or "ROS 2 on WendyOS"), description naming
  ROS 2.
- Opens by stating ROS 2 compatibility plainly (standard upstream ROS 2, supported
  distros, any RMW).
- Lays out the three pillars.
- Routes the reader through the lifecycle: **Deploy**
  (`/docs/guides/multi-app-deployments`) → **Operate** (`/docs/integrations/ros2`) →
  **Visualize** (`/docs/integrations/foxglove`).

**`ros2.mdx`:** add a short **compatibility callout** near the top — supported ROS 2
distro(s), RMW implementations, and "standard upstream ROS 2" — so compatibility is a
headline fact, not buried in "How it works."

**`meta.json`:**
- Add the overview page as the **first** entry in `pages`.
- Tighten `description` to name ROS 2, e.g. "Build, deploy, and operate ROS 2 robots
  with WendyOS."
- Keep `title: "Robotics"`.

## Consistency / cross-linking

- Marketing hero + features CTAs deep-link into the docs Robotics pages
  (`/docs/integrations`, `/docs/integrations/ros2`, `/docs/integrations/foxglove`).
- Docs overview page uses the same three-pillar language as the marketing page.
- Both name ROS 2 in the first screenful.

## Open questions / verify during implementation

1. **Fumadocs index page support:** confirm whether `prepare-content.mjs` +
   `meta.json` render an `index.mdx` as the section landing page, or whether a named
   page (`overview.mdx`) is required and how it's ordered. Inspect
   `docs/scripts/prepare-content.mjs` and `docs/source.config.ts`.
2. **ROS 2 distro/RMW facts:** confirm the exact supported ROS 2 distro(s) and RMW
   implementations to state in the compatibility callout (check `Examples/ROS2`,
   `frameworks.ros2` config handling, and agent sidecar code) rather than asserting.
3. **Docs link base from marketing:** confirm marketing→docs links resolve (`/docs/…`
   as used by sibling solution pages) for the Robotics deep links.
4. **Foxglove "coming soon":** the marketing Foxglove feature should be phrased so it
   doesn't over-promise while the docs page is still flagged "coming soon"
   (PR #1165). Phrase as available/forthcoming consistently with the docs page.

## Affected files

**Marketing repo** (`cloud-marketing-configurator/marketing/`):
- `src/app/solutions/robots/page.tsx` — hero + features rewrite
- `src/components/layout/navbar.tsx` — label + description edit

**WendyOS repo** (`wendyos/go/internal/cli/assets/docs/integrations/`):
- `index.mdx` (or `overview.mdx`) — new overview/landing page
- `ros2.mdx` — compatibility callout
- `meta.json` — description + page ordering

## Success criteria

- Both surfaces name "ROS 2" within the first screenful.
- Marketing robots page presents the three pillars, grounded in real WendyOS
  capability, with working deep links into docs.
- Docs Robotics section has an overview page that states compatibility and routes
  deploy → operate → visualize.
- No broken links; `/solutions/robots` URL unchanged; docs build succeeds.

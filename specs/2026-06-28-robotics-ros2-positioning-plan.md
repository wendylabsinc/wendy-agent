# Robotics / ROS 2 Positioning Upgrade — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make WendyOS's robotics story clearly ROS 2-compatible and positioned as "the best way for robotics," consistently across the marketing site and the docs.

**Architecture:** Pure content/positioning change across two repos. The marketing `/solutions/robots` page is rewritten (reusing existing `SolutionHero`/`SolutionFeatures`/`ProfessionalServicesCta` components — no new components). The docs "Robotics" section gains an overview/landing page, a ROS 2 compatibility callout on `ros2.mdx`, and an updated `meta.json`. Both surfaces share one positioning spine: *ROS 2, from prototype to production fleet — on the open stack you already use.*

**Tech Stack:** Next.js 16 + React 19 + Tailwind + lucide-react (marketing); Fumadocs 16 + MDX (docs). Both npm.

## Global Constraints

- **Two separate git repos.** Marketing tasks run in `/Users/joannisorlandos/git/wendy/cloud-marketing-configurator/marketing/` and commit there. Docs tasks run in `/Users/joannisorlandos/git/wendy/wendyos/` and commit there. Never mix changes from the two repos in one commit.
- **Do not change the `/solutions/robots` URL** — only its content and nav label. Inbound links must keep working.
- **Lead message (both surfaces):** "ROS 2, from prototype to production fleet — on the open stack you already use." Three pillars in priority order: (1) open, standard ROS 2; (2) deploy in one command; (3) production from day one (fleet/OTA/entitlements). Supporting proof: operate live ROS 2 with no SSH.
- **Only state verifiable ROS 2 facts.** Default distro is **Humble** (`ROS2DefaultDistro = "humble"`); standard upstream `ros:<distro>` containers; RMW options are **CycloneDDS (default), Fast DDS, RTI Connext, Gurum** (per `go/internal/shared/appconfig/ros2.go`). Do NOT claim other distros are tested.
- **Marketing `SolutionFeatures` descriptions render as plain `<p>` text** — no markdown/backticks. Do not use backticks or code formatting in feature `description` strings; they would render literally.
- **Icons come from `lucide-react`** (already the convention). Only use icon names that exist in lucide-react.
- Use 2-space indentation in TSX/JSON to match existing files.

---

## File Structure

**Marketing repo** (`cloud-marketing-configurator/marketing/`):
- `src/app/solutions/robots/page.tsx` — rewrite hero + features content (modify).
- `src/components/layout/navbar.tsx` — rename "Robots" nav label → "Robotics" + ROS 2 description (modify).

**WendyOS repo** (`wendyos/go/internal/cli/assets/docs/integrations/`):
- `index.mdx` — new Robotics section overview/landing page (create).
- `ros2.mdx` — add ROS 2 compatibility callout near top (modify).
- `meta.json` — add `index` first in `pages`, update `description` (modify).

Tasks 1–2 are the docs repo; Tasks 3–4 are the marketing repo. They are independent and may be done in either order.

---

### Task 1: Docs — Robotics overview page + meta.json

**Files:**
- Create: `go/internal/cli/assets/docs/integrations/index.mdx`
- Modify: `go/internal/cli/assets/docs/integrations/meta.json`

**Working dir / repo:** `/Users/joannisorlandos/git/wendy/wendyos/`

**Interfaces:**
- Produces: a section landing page reachable at `/docs/integrations`, linked from the marketing page in Task 3 (`ctaHref="/docs/integrations"`). Sibling pages remain at `/docs/integrations/ros2` and `/docs/integrations/foxglove`.

- [ ] **Step 1: Create the overview page**

Create `go/internal/cli/assets/docs/integrations/index.mdx` with exactly:

```mdx
---
title: Robotics
description: Build, deploy, and operate ROS 2 robots with WendyOS — standard, upstream ROS 2 from prototype to production fleet.
---

WendyOS is built for ROS 2 robots — and takes them all the way from a prototype on
your desk to a fleet in production. It runs **standard, upstream ROS 2** in
containers, deploys to NVIDIA Jetson and Raspberry Pi with one command, and lets you
inspect, debug, and update everything remotely over one secure connection.

## Why WendyOS for ROS 2

- **Open and standard.** Real upstream ROS 2 in stock containers, with your choice of
  middleware — CycloneDDS (default), Fast DDS, RTI Connext, or Gurum. Apache-2.0, no
  lock-in.
- **Deploy in one command.** Ship a containerized ROS 2 stack to a device with
  `wendy run` — no flashing, no `source /opt/ros/<distro>/setup.bash`, no toolchain
  setup.
- **Production from day one.** Over-the-air updates, multi-device fleets, and
  fine-grained entitlements that scope each app's access to GPU, camera, audio, and
  storage.

## The workflow

1. **Deploy** — declare ROS 2 in your `wendy.json` with a `frameworks.ros2` block and
   ship the stack. See [Multi-app deployments](/docs/guides/multi-app-deployments).
2. **Operate** — inspect nodes and topics, echo messages, set parameters, and record
   rosbags on the running device with no SSH. See
   [Wendy for ROS 2](/docs/integrations/ros2).
3. **Visualize** — bridge live topics into Foxglove Studio for plots and panels. See
   [Wendy for Foxglove](/docs/integrations/foxglove).
```

- [ ] **Step 2: Update meta.json**

Replace the entire contents of `go/internal/cli/assets/docs/integrations/meta.json` with:

```json
{
  "title": "Robotics",
  "description": "Build, deploy, and operate ROS 2 robots with WendyOS",
  "pages": ["index", "ros2", "foxglove"]
}
```

- [ ] **Step 3: Verify the docs build processes the new page**

Run:

```bash
cd /Users/joannisorlandos/git/wendy/wendyos/go/internal/cli/assets/docs && npm run types:check
```

Expected: the `pretypes:check` hook runs `prepare-content.mjs` (copies `integrations/*.mdx` and `meta.json` into `content/docs/integrations/`), then `tsc --noEmit` passes with no errors. Confirm `content/docs/integrations/index.mdx` now exists:

```bash
ls /Users/joannisorlandos/git/wendy/wendyos/go/internal/cli/assets/docs/content/docs/integrations/
```

Expected: lists `index.mdx`, `ros2.mdx`, `foxglove.mdx`, `meta.json`.

- [ ] **Step 4: Commit**

```bash
cd /Users/joannisorlandos/git/wendy/wendyos
git add go/internal/cli/assets/docs/integrations/index.mdx go/internal/cli/assets/docs/integrations/meta.json
git commit -m "docs(robotics): add ROS 2 overview landing page

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Docs — ROS 2 compatibility callout on ros2.mdx

**Files:**
- Modify: `go/internal/cli/assets/docs/integrations/ros2.mdx` (insert after the opening paragraph, before `## How it works` — currently around line 10)

**Working dir / repo:** `/Users/joannisorlandos/git/wendy/wendyos/`

**Interfaces:**
- Consumes: nothing. Standalone edit. `<Callout>` is already used in this file (no new import needed).

- [ ] **Step 1: Insert the compatibility callout**

In `go/internal/cli/assets/docs/integrations/ros2.mdx`, immediately after the opening paragraph that ends `…the same secure connection the CLI already uses.` and before the `## How it works` heading, insert a blank line then exactly:

```mdx
<Callout type="info">
**Standard, upstream ROS 2.** WendyOS runs real ROS 2 from stock containers — Humble
by default, configurable per app. Choose your middleware with `frameworks.ros2.rmw`:
CycloneDDS (default), Fast DDS, RTI Connext, or Gurum.
</Callout>
```

- [ ] **Step 2: Verify build still passes**

Run:

```bash
cd /Users/joannisorlandos/git/wendy/wendyos/go/internal/cli/assets/docs && npm run types:check
```

Expected: `prepare-content.mjs` runs and `tsc --noEmit` passes with no errors (valid MDX, callout component resolves).

- [ ] **Step 3: Commit**

```bash
cd /Users/joannisorlandos/git/wendy/wendyos
git add go/internal/cli/assets/docs/integrations/ros2.mdx
git commit -m "docs(robotics): foreground ROS 2 compatibility on ros2 page

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Marketing — rewrite the robots solution page

**Files:**
- Modify: `src/app/solutions/robots/page.tsx` (full rewrite of hero + features content)

**Working dir / repo:** `/Users/joannisorlandos/git/wendy/cloud-marketing-configurator/marketing/`

**Interfaces:**
- Consumes: existing `SolutionHero` (`title`, `subtitle`, `description`, `ctaText`, `ctaHref` props), `SolutionFeatures` (`title`, `description`, `features: {title, description, icon}[]`), `ProfessionalServicesCta` (no props). `ctaHref="/docs/integrations"` relies on Task 1's overview page.
- Produces: the upgraded `/solutions/robots` page.

- [ ] **Step 1: Rewrite the page**

Replace the entire contents of `src/app/solutions/robots/page.tsx` with exactly:

```tsx
import { Activity, Boxes, Fingerprint, Layers, LineChart, Rocket } from 'lucide-react';
import { SolutionHero } from '@/components/sections/solutions/solution-hero';
import { SolutionFeatures } from '@/components/sections/solutions/solution-features';
import { ProfessionalServicesCta } from '@/components/sections/professional-services-cta';

export default function RobotsPage() {
  return (
    <>
      <SolutionHero
        subtitle="Robotics · ROS 2"
        title="ROS 2 robots, from prototype to production fleet"
        description="WendyOS runs standard, upstream ROS 2 and takes it all the way to production. Deploy containerized ROS 2 stacks to NVIDIA Jetson and Raspberry Pi with a single command, then inspect, debug, and update entire fleets over the air. No flashing, no toolchain hell, no lock-in."
        ctaText="Explore ROS 2 on WendyOS"
        ctaHref="/docs/integrations"
      />
      <SolutionFeatures
        title="From prototype to production"
        description="Everything you need to build, ship, and operate ROS 2 robots with WendyOS."
        features={[
          {
            title: 'Standard, open ROS 2',
            description:
              'Real upstream ROS 2 in stock containers, with your choice of middleware — CycloneDDS, Fast DDS, RTI Connext, or Gurum. Apache-2.0 and no lock-in.',
            icon: Boxes,
          },
          {
            title: 'Deploy in one command',
            description:
              'Ship a containerized ROS 2 stack to NVIDIA Jetson or Raspberry Pi with a single command — no flashing, no sourcing setup scripts, no toolchain setup.',
            icon: Rocket,
          },
          {
            title: 'Fleet management & OTA',
            description:
              'Roll out and update entire fleets of robots over the air from one place, with safe, atomic updates.',
            icon: Layers,
          },
          {
            title: 'Fine-grained entitlements',
            description:
              'Grant each app exactly the access it needs — GPU, camera, audio, storage — and nothing more.',
            icon: Fingerprint,
          },
          {
            title: 'Inspect & debug live — no SSH',
            description:
              'Explore nodes and topics, echo messages, read and set parameters, and record rosbags on a running device, straight from your machine.',
            icon: Activity,
          },
          {
            title: 'Visualize in Foxglove',
            description:
              "Bridge a device's live ROS 2 topics into Foxglove Studio for real-time plots and panels.",
            icon: LineChart,
          },
        ]}
      />
      <ProfessionalServicesCta />
    </>
  );
}
```

- [ ] **Step 2: Verify lint + types pass**

Run:

```bash
cd /Users/joannisorlandos/git/wendy/cloud-marketing-configurator/marketing && npm run lint
```

Expected: no errors. (All six icons — `Activity`, `Boxes`, `Fingerprint`, `Layers`, `LineChart`, `Rocket` — exist in lucide-react and are all used, so no unused-import warnings.)

- [ ] **Step 3: Verify the page renders in a dev build (visual check)**

Run:

```bash
cd /Users/joannisorlandos/git/wendy/cloud-marketing-configurator/marketing && npm run build
```

Expected: build succeeds and includes the `/solutions/robots` route. Confirm the rendered page leads with "ROS 2 robots, from prototype to production fleet" and shows six feature cards.

- [ ] **Step 4: Commit**

```bash
cd /Users/joannisorlandos/git/wendy/cloud-marketing-configurator/marketing
git add src/app/solutions/robots/page.tsx
git commit -m "feat(solutions): reposition robots page around ROS 2, prototype to production"
```

---

### Task 4: Marketing — nav label + description

**Files:**
- Modify: `src/components/layout/navbar.tsx` (the "Robots" sub-item under Solutions → Use Cases)

**Working dir / repo:** `/Users/joannisorlandos/git/wendy/cloud-marketing-configurator/marketing/`

**Interfaces:**
- Consumes: the existing navigation data structure (the "Use Cases" `items` array). `href: '/solutions/robots'` stays unchanged.

- [ ] **Step 1: Update the Robots nav entry**

In `src/components/layout/navbar.tsx`, find the "Use Cases" sub-item with `title: 'Robots'` and `href: '/solutions/robots'`. Change only its `title` and `description` (leave `href` and `icon` unchanged):

Find:

```tsx
        {
          title: 'Robots',
          href: '/solutions/robots',
          description: 'Automation and physical assistance',
          icon: Code2,
        },
```

Replace with:

```tsx
        {
          title: 'Robotics',
          href: '/solutions/robots',
          description: 'Build & ship ROS 2 robots',
          icon: Code2,
        },
```

(If the surrounding indentation differs from above, match the file's existing indentation rather than the indentation shown here.)

- [ ] **Step 2: Verify lint passes**

Run:

```bash
cd /Users/joannisorlandos/git/wendy/cloud-marketing-configurator/marketing && npm run lint
```

Expected: no errors.

- [ ] **Step 3: Commit**

```bash
cd /Users/joannisorlandos/git/wendy/cloud-marketing-configurator/marketing
git add src/components/layout/navbar.tsx
git commit -m "feat(nav): rename Robots to Robotics with ROS 2 framing"
```

---

## Self-Review

**Spec coverage:**
- Positioning spine (lead message + 3 pillars) → Task 1 overview page, Task 3 hero/features. ✓
- Marketing hero rewrite → Task 3 Step 1. ✓
- Marketing 6-feature rewrite mapped to pillars → Task 3 Step 1. ✓
- Marketing CTA → docs → Task 3 (`ctaHref="/docs/integrations"`). ✓
- Navbar rename + ROS 2 description → Task 4. ✓
- Docs new overview/landing page (index first in meta) → Task 1. ✓
- Docs `ros2.mdx` compatibility callout → Task 2. ✓
- Docs `meta.json` description + ordering → Task 1 Step 2. ✓
- Spec open questions resolved before planning: Fumadocs index handling (Task 1 uses `index.mdx` + `["index","ros2","foxglove"]`, matching the existing `advanced` section pattern); ROS 2 distro/RMW facts (Humble default, CycloneDDS/Fast DDS/Connext/Gurum, all stated only as verified); docs link base (`/docs/integrations` per sibling pages). ✓
- Foxglove not over-promised: marketing feature says "Bridge…into Foxglove Studio" (capability framing) and links nowhere that contradicts the docs "coming soon" note; the docs overview links to the existing foxglove page which keeps its own status callout. ✓

**Placeholder scan:** No TBD/TODO; all copy, JSON, and TSX is complete and literal. ✓

**Type consistency:** `SolutionHero`/`SolutionFeatures`/`ProfessionalServicesCta` prop usage matches their definitions (verified in components). All six lucide icons are imported and used. meta.json `pages` values match the actual filenames (`index`, `ros2`, `foxglove`). ✓

## Success criteria

- Marketing `/solutions/robots` and the docs `/docs/integrations` overview both name "ROS 2" in the first screenful and present the three pillars.
- Marketing CTA deep-links into the docs Robotics overview; `/solutions/robots` URL unchanged.
- `npm run types:check` (docs) and `npm run lint` + `npm run build` (marketing) all pass.
- Nav shows "Robotics" with ROS 2 framing.

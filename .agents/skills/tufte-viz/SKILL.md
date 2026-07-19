---
name: tufte-viz
description: Use when designing, critiquing, or improving data visualizations -- applies Tufte's principles (data-ink, lie factor, chartjunk, small multiples) and implements them in seaborn
---

# Tufte Visualization Ideation

Apply Edward Tufte's principles to design clear, honest, high-density data visualizations. In this project, **implement in seaborn** (objects interface preferred) — never matplotlib-only, never bare pandas `.plot()`.

## When to Use

- Designing new charts, dashboards, or reports
- Critiquing or improving existing visualizations
- Reviewing notebooks or `ad_hoc/` plots for graphical integrity
- Deciding between visualization approaches (e.g. small multiples vs. overlay)
- Reducing chartjunk or improving data-ink ratio

## Workflow

### For new visualizations

1. **Clarify the data story**
   - What comparisons matter? (Tufte: "compared to what?")
   - What is the single key insight to communicate?
   - Who is the audience and what decision does this inform?

2. **Select approach** using Tufte principles
   - High comparison need → small multiples (`so.Plot().facet()` or `sns.FacetGrid`)
   - Dense data → tables, sparklines, or stripplot
   - Time-series → line charts with minimal grid (`despine`, no top/right spines)
   - Part-to-whole → avoid pie charts; prefer ordered bar or table
   - Distributions → strip/swarm/box, not 3D histograms

3. **Design with data-ink in mind**
   - Start minimal, add only what earns its ink
   - Default to grayscale (`palette="gray"`); use color only to encode a variable
   - Strip default seaborn theme cruft (`sns.despine()`, remove gridlines, prune ticks)

4. **Apply the Tufte test** (see `references/tufte-principles.md`)

5. **Implement in seaborn** (see `references/seaborn-patterns.md`)
   - Save plots to `data/` or `ad_hoc/` — never leave only in notebook state
   - Pure functions: pandas for transform, seaborn for render, no side effects in logic blocks

### For critiquing visualizations

1. **Check graphical integrity**
   - Compute lie factor if proportions look off (`visual_ratio / data_ratio`, target ≈ 1.0)
   - Verify baselines (bar charts must start at 0; line charts may not)
   - Look for 3D distortion, dual axes, truncated scales

2. **Identify chartjunk**
   - Decorative gradients, drop shadows, 3D bars
   - Heavy gridlines competing with data
   - Moiré patterns from default seaborn themes
   - Redundant legends when direct labels would work

3. **Evaluate data-ink ratio**
   - What can be erased without losing information?
   - What is redundant (e.g. both color AND legend AND title saying the same thing)?

4. **Suggest improvements** with specific before/after seaborn snippets

## References

Load only what the current task needs:

- `references/tufte-principles.md` — core principles from *The Visual Display of Quantitative Information*: lie factor, data-ink, chartjunk, small multiples, integrity.
- `references/analytical-design.md` — extensions from *Envisioning Information*, *Visual Explanations*, *Beautiful Evidence*: the 6 principles of analytical design, sparklines, layering & separation, micro/macro, range-frames, causality, confections. Load when designing dashboards, dense displays, sparklines, or explanatory graphics.
- `references/seaborn-patterns.md` — concrete seaborn (objects interface) recipes for each Tufte principle: despine, small multiples via `so.Plot().facet()`, sparklines, range-frames, palette choices, theme. Load when actually writing the plotting code.

## Quick Checklist

Before declaring a visualization done:

- [ ] Lie Factor ≈ 1.0 (no visual distortion of data proportions)
- [ ] Maximum data-ink ratio (every pixel earns its place)
- [ ] Zero decorative chartjunk
- [ ] Clear labeling — title, axes, units, source
- [ ] Answers "compared to what?"
- [ ] Shows causality or mechanism where relevant
- [ ] Multivariate where the data is multivariate (not over-reduced)
- [ ] Words, numbers, images integrated — not segregated
- [ ] Reveals multiple levels of detail (micro + macro)
- [ ] Layering: primary data dominates, secondary recedes
- [ ] Appropriate data density (Tufte: more numbers per unit area)
- [ ] Implemented in seaborn (objects interface preferred), saved to disk, reproducible from a script

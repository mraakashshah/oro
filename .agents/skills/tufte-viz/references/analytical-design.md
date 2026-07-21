# Analytical Design: Advanced Tufte Principles

Sources: *Envisioning Information* (1990), *Visual Explanations* (1997), *Beautiful Evidence* (2006).

Load this reference when designing dashboards, dense displays, sparklines, or explanatory graphics — beyond the basic chart critique covered in `tufte-principles.md`.

## The Six Principles of Analytical Design

From *Beautiful Evidence*:

1. **Show comparisons, contrasts, differences.** Every analytical graphic answers "compared to what?" If you can't articulate the comparison, the chart isn't analytical — it's decoration.
2. **Show causality, mechanism, explanation, systematic structure.** Reveal how, not just what. A scatter plot of A vs. B is weaker than one that also shows the intervention or process linking them.
3. **Show multivariate data — more than 1 or 2 variables.** Real problems are multivariate. Oversimplification to a single dimension hides the actual structure.
4. **Completely integrate words, numbers, images, diagrams.** Don't segregate text into captions, numbers into tables, and pictures into figures. Integrated displays let the eye move fluidly between modes.
5. **Thoroughly describe the evidence.** Provide a detailed title, indicate authors and sponsors, document the data sources, show complete measurement scales, point out relevant issues.
6. **Analytical presentations ultimately stand or fall depending on the quality, relevance, and integrity of their content.** Design cannot rescue bad evidence.

## Sparklines

Tufte's invention: word-sized, intense, simple, data-rich graphics designed to be embedded in the flow of text or numbers.

**Properties:**
- ~10–20 px tall, line-height of surrounding text
- No axes, no labels, no gridlines
- Optional: dot at most recent value, light band for normal range
- Show **shape and trajectory** — sacrifice precision for density

**Where to use:**
- Inline with a number: `Revenue: $4.2M ▁▂▃▅▇▆▄▃ ↑`
- One-per-row in a table of metrics
- Dashboard tiles where 20+ metrics must fit on one screen

## Layering and Separation

Multiple data series in one space, distinguished by visual weight rather than separate panels.

**Avoiding the 1+1=3 effect:** When two lines cross, your eye perceives a third "phantom" object at the intersection. Mitigate with:
- Lightening one line (gray vs. black)
- Different stroke weights
- Transparency
- Carefully chosen hue separation

**Hierarchy:** Primary data dominates (dark, full opacity). Secondary data (reference lines, annotations, comparisons) recedes (light gray, thin). Tertiary structure (axes, ticks) recedes further.

## Micro/Macro Design

Effective displays reward both distant viewing (overall pattern) and close inspection (individual values). The Vietnam Veterans Memorial is Tufte's exemplar: from afar a single dark wedge, up close 58,000 individual names.

**For data graphics:**
- Maps: country-level pattern from across the room, county-level detail up close
- Tables: column sums visible at a glance, individual cells legible when reading
- Dashboards: tile-level status visible scanning, drill-down detail on focus

Test: take a screenshot, scale to 25%. Does the macro story survive? Then zoom to 200%. Does the micro detail hold up?

## Range-Frames and Dot-Dash Plots

Convert non-data chart elements into information-carrying elements.

**Range-frame:** axis lines extend only over the observed data range. The axis itself communicates the min and max.

**Dot-dash plot:** marginal ticks on the axes mark the location of every data point. The axes become rugs / strip plots, showing the marginal distributions for free.

Both maximize data-ink by making axes carry data instead of merely framing it.

## Information Dimensions

Beyond x and y, add dimensions through:

- **Color** (hue for category, value for quantity)
- **Size** (area or radius, never linear-vs-area confusion)
- **Shape** (limited; max ~5 distinguishable shapes)
- **Position** (jitter, layering)
- **Time** (animation, small multiples across time)
- **Layering** (foreground/background hierarchy)

Tufte explicitly rejects decorative 3D, drop shadows, gradients, and pseudo-perspective as **chartjunk** that adds no information dimension.

## Causality

Causal claims require showing **intervention, mechanism, and response** together, in one display. The classic example: Snow's cholera map showed not just deaths (response) but the Broad Street pump (intervention) and the spatial proximity (mechanism) — in a single integrated graphic.

For data work:
- A/B test results: show variant, treatment, and outcome on one chart
- Time-series with interventions: annotate the intervention on the line itself
- Regression: show predictors, fitted relationship, and residuals together

## Confections

A *confection* is an assembly of disparate visual elements — diagrams, text, numbers, images — into a unified explanation. Examples: a baseball box score, a weather forecast page, an experimental physics figure.

When the story is complex and multi-mode, build a confection. Don't fragment it into a slide deck.

**Design rules for confections:**
- One unifying layout grid
- Consistent typography across modes
- Words placed adjacent to the visual they describe (not in a caption below)
- Numbers integrated into the visual, not extracted to a separate table

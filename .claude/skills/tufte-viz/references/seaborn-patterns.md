# Seaborn Patterns for Tufte-Style Visualizations

Concrete seaborn recipes for each Tufte principle. Prefer the **objects interface** (`seaborn.objects as so`) — it composes, it's functional, and it maps cleanly onto Tufte's layering model. Fall back to `seaborn.axisgrid` / function API when the objects interface lacks a primitive (currently: KDE, regression overlays, jointplots).

Reference: https://seaborn.pydata.org/tutorial/introduction.html

## Setup

```python
import pandas as pd
import seaborn as sns
import seaborn.objects as so
import matplotlib.pyplot as plt

# Tufte-friendly defaults: minimal theme, no top/right spines, light grid only when needed.
sns.set_theme(
    style="ticks",         # ticks, not whitegrid -- gridlines compete with data
    context="paper",       # smallest text, highest data-ink ratio
    font="DejaVu Sans",
    rc={
        "axes.spines.top": False,
        "axes.spines.right": False,
        "axes.grid": False,
        "axes.edgecolor": "#444",
        "axes.labelcolor": "#222",
        "xtick.color": "#444",
        "ytick.color": "#444",
        "figure.dpi": 150,
    },
)
```

If you must use the function API, follow every plot with `sns.despine()`.

## Pattern 1: Maximize Data-Ink

**Avoid:** default seaborn `whitegrid` theme with thick spines and full gridlines.

**Prefer:**

```python
(
    so.Plot(df, x="year", y="revenue")
    .add(so.Line(color="#222"))
    .add(so.Dot(color="#222", pointsize=4), data=df.tail(1))  # emphasize most recent
    .label(x="", y="Revenue ($M)", title="Revenue, 2015–2025")
    .theme({"axes.spines.top": False, "axes.spines.right": False})
)
```

- No legend if there's one series
- No gridlines unless precise readout is required
- Dot the latest value (Tufte: sparkline endpoint convention)

## Pattern 2: Small Multiples

The single most powerful Tufte technique. Use `.facet()` in the objects interface.

```python
(
    so.Plot(df, x="month", y="value", color="metric")
    .add(so.Line())
    .facet(col="region", wrap=4)
    .share(y=True)                  # MUST share scale across panels
    .layout(size=(10, 4))
    .label(x="", y="", title="Monthly value by region")
)
```

**Rules:**
- Same y-scale across panels (`.share(y=True)`) — otherwise comparison is broken
- Same encoding everywhere
- Strip panel borders; let whitespace separate the multiples
- 4–20 panels typical; >20 → consider a different display

Function API equivalent: `sns.FacetGrid` + `g.map_dataframe(...)`. Older but stable.

## Pattern 3: Sparklines

Seaborn doesn't ship a sparkline primitive — compose one:

```python
def sparkline(values, ax, last_dot=True):
    ax.plot(values, color="#222", linewidth=1)
    if last_dot:
        ax.plot(len(values) - 1, values[-1], "o", color="#c00", markersize=3)
    ax.set_axis_off()
    ax.margins(y=0.1)
    return ax

# Grid of sparklines, one per metric
fig, axes = plt.subplots(len(metrics), 1, figsize=(3, 0.3 * len(metrics)))
for ax, (name, series) in zip(axes, metrics.items()):
    sparkline(series.values, ax)
    ax.text(-0.05, 0.5, name, transform=ax.transAxes, ha="right", va="center", fontsize=8)
```

Save to `data/sparklines.png` or `ad_hoc/<analysis>/sparklines.png`.

## Pattern 4: Range-Frames and Dot-Dash Plots

Restrict axes to observed data range and add marginal ticks:

```python
fig, ax = plt.subplots(figsize=(5, 4))
sns.scatterplot(data=df, x="x", y="y", color="#222", s=15, ax=ax)
sns.rugplot(data=df, x="x", y="y", color="#222", lw=0.5, height=0.02, ax=ax)
sns.despine(trim=True, ax=ax)       # trim=True clips spines to data range
```

`trim=True` is the closest off-the-shelf approximation of a range-frame.

## Pattern 5: Palettes (Color Discipline)

**Default to grayscale.** Only use color when it encodes a variable.

```python
# Sequential (ordered quantitative): light → dark
sns.color_palette("rocket", as_cmap=True)
sns.color_palette("mako", as_cmap=True)

# Diverging (signed data with meaningful zero)
sns.color_palette("vlag", as_cmap=True)
sns.color_palette("icefire", as_cmap=True)

# Categorical (qualitative, ≤8 categories)
sns.color_palette("deep")
sns.color_palette("muted")
sns.color_palette("colorblind")     # prefer this -- safest

# Grayscale baseline
sns.color_palette("gray", n_colors=5)
```

Avoid `rainbow`, `jet`, `hsv` — they introduce perceptual lie factor (non-monotonic luminance).

## Pattern 6: Layering and Hierarchy

In the objects interface, layers compose left-to-right and render bottom-to-top:

```python
(
    so.Plot(df, x="x", y="y")
    .add(so.Band(color="#ddd"), so.Est())       # background: confidence band, recedes
    .add(so.Line(color="#999"), so.Agg())        # secondary: mean line, mid weight
    .add(so.Dots(color="#222", pointsize=3))    # primary: raw data, dominant
)
```

The eye reads dark + sharp first. Background context is light + diffuse.

## Pattern 7: Avoiding Chartjunk

Things to **remove** from default seaborn output:

| Default | Tufte fix |
|---|---|
| Thick colored bars with no separation | Thin bars, gray, separated by whitespace |
| Full grid (`whitegrid` theme) | `ticks` theme + `despine()` |
| Legend duplicating direct labels | `.label(color=None)` and label endpoints directly |
| Wide pastel pie chart | Bar chart ordered by value |
| Default 6.4×4.8 figure aspect | Match aspect to data shape (often wider) |
| Title baked into the plot via `.set_title()` | Title + subtitle in the surrounding doc/HTML when possible |

## Pattern 8: Multivariate Without Junk

Encode multiple variables with **position, value, hue, size** — never with 3D.

```python
(
    so.Plot(df, x="gdp", y="life_expectancy")
    .add(so.Dots(),
         color="continent",          # categorical hue
         pointsize="population",     # quantitative size
         alpha="literacy_rate")      # quantitative alpha
    .scale(x="log", pointsize=(2, 30))
    .label(x="GDP per capita (log)", y="Life expectancy (years)")
)
```

Four variables, two axes, zero perspective tricks.

## Pattern 9: Save, Don't Display

Per the project rules: **never leave plots only in notebook state**.

```python
plot = so.Plot(...).add(...)
plot.save("data/cleaned/figure_revenue_by_region.png", dpi=200, bbox_inches="tight")
# Or for the function/axes API:
fig.savefig("ad_hoc/my_analysis/figure.png", dpi=200, bbox_inches="tight")
plt.close(fig)
```

Every saved figure should be reproducible from a script in `ad_hoc/` or `src/`. Notebooks are for exploration; scripts are the source of truth.

## Anti-Patterns (Stop If You Catch Yourself)

- Calling `.plot()` directly on a pandas DataFrame — bypasses seaborn, produces matplotlib defaults
- Using `seaborn.set()` instead of `seaborn.set_theme()` (deprecated)
- Adding a legend when there's one series
- Using `jet` / `rainbow` colormaps for sequential data
- Truncating a bar chart's y-axis to make differences look bigger (lie factor > 1)
- 3D anything
- `plt.show()` in a script — save the figure instead

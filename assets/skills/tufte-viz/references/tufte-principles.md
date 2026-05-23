# Tufte's Core Principles for Data Visualization

Source: *The Visual Display of Quantitative Information* (Edward Tufte, 1983/2001).

## 1. Graphical Excellence

> "Graphical excellence is that which gives to the viewer the greatest number of ideas in the shortest time with the least ink in the smallest space."

Excellence is **complex ideas communicated with clarity, precision, and efficiency**. A graphic should encourage the eye to compare different pieces of data and serve a clear analytical purpose: describe, explore, tabulate, or decorate (in that order of priority — decoration last).

## 2. Graphical Integrity

The single non-negotiable principle. Visual representation must not distort the underlying data.

**Lie Factor**

```
Lie Factor = (size of effect shown in graphic) / (size of effect in data)
```

Target ≈ 1.0. Anything outside [0.95, 1.05] is suspect.

**Six principles of graphical integrity:**

1. Representation of numbers, physically measured on the surface of the graphic, should be **directly proportional** to the quantities represented.
2. **Clear, detailed, thorough labeling** defeats graphical distortion and ambiguity.
3. Show **data variation**, not design variation.
4. In time-series of money, use **deflated and standardized units** — not nominal dollars.
5. The number of information-carrying (variable) dimensions in the graphic should not exceed the number of dimensions in the data. (No 3D bars for 1D data.)
6. Graphics must not quote data **out of context**.

**Common violations**

- 3D effects on 2D data (perspective inflates the rear values)
- Truncated y-axes on bar charts (always start bars at 0)
- Inconsistent scales across small multiples
- Area encoded with diameter (doubles the apparent effect)

## 3. Data-Ink Ratio

```
Data-Ink Ratio = data-ink / total ink used to print the graphic
                = 1 - proportion of graphic that can be erased
                  without loss of information
```

**Five-step refinement:**

1. Above all else, show the data.
2. Maximize the data-ink ratio.
3. Erase non-data-ink.
4. Erase redundant data-ink.
5. Revise and edit.

Defaults from most plotting libraries violate this. Strip them.

## 4. Chartjunk

Three categories of non-informative ink that must be eliminated:

1. **Unintentional optical art** — moiré patterns, vibrating fills, dense crosshatching. Default heatmap palettes often produce this.
2. **The grid** — heavy gridlines compete with data. Use them sparingly, light gray, behind the data.
3. **The duck** — when the design dominates the data (named after a duck-shaped building). Decorative illustrations that swamp the numbers.

> "The interior decoration of graphics generates a lot of ink that does not tell the viewer anything new."

## 5. Small Multiples

> "At the heart of quantitative reasoning is a single question: Compared to what?"

Small multiples answer it. A series of identical-design graphics, varied only by the data shown, lets the eye detect patterns across groups, time, or conditions. Properties:

- Same scale across panels (otherwise comparison is broken)
- Same encoding, layout, and typography
- Dense — many panels in a small area
- Often more informative than a single complex chart

## 6. Data Density & Information Resolution

```
Data Density = number of entries in data matrix / area of graphic
```

Most published graphics are **vastly under-dense**. Newsprint charts often have data densities of 0.1–1 entries/cm². Tufte's targets are 10–100+ entries/cm² (think sparklines, weather maps, stock tables).

Techniques to increase density:

- Sparklines (word-sized graphics inline with text)
- Condensed tables with mini-charts
- Layered transparency for overlap
- Smaller marks, tighter layouts

## 7. Multifunctioning Graphical Elements

A single element should do multiple jobs:

- Data points that also serve as labels
- Axis ticks placed only at observed data values (range-frames, dot-dash plots)
- Numbers in the plot that double as axis annotation

Every element pulling double duty reduces total ink without reducing information.

## 8. Aesthetics and Technique

Visual simplicity, data complexity. The hierarchy must be unambiguous: **data first, structure second, decoration never**.

> "What is to be sought in designs for the display of information is the clear portrayal of complexity. Not the complication of the simple."

## The Tufte Test

Before publishing a graphic, ask:

1. Does it show the data?
2. Does it induce the viewer to think about substance, not methodology, design, or chartjunk?
3. Does it avoid distorting what the data say?
4. Does it present many numbers in a small space?
5. Does it make large data sets coherent?
6. Does it encourage the eye to compare different pieces of data?
7. Does it reveal the data at several levels of detail (overview + fine structure)?
8. Does it serve a clear purpose: description, exploration, tabulation, or decoration?
9. Is it closely integrated with statistical and verbal descriptions?

If any answer is no, revise.

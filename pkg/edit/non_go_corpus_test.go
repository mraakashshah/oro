package edit_test

import (
	"errors"
	"testing"

	"oro/pkg/edit"
)

// TestNonGoCorpus is the 250-case non-Go edit corpus exercising
// SplicePerLang for Python, TypeScript, and JavaScript (§7.4 per-language
// gotchas, §7.8 bench/accuracy targets).
//
// Groups (250 = 100 Python + 100 TypeScript + 50 JavaScript):
//
//	PA (20)  Python: two-anchor basic replace
//	PB (15)  Python: continuation marker
//	PC (15)  Python: indentation normalization
//	PD (15)  Python: async def with decorators (§7.4 gotcha)
//	PE (15)  Python: classmethod / staticmethod (§7.4 gotcha)
//	PF (10)  Python: default args with function calls (§7.4 gotcha)
//	PG (10)  Python: EFALLTHROUGH
//
//	TA (20)  TypeScript: two-anchor basic replace
//	TB (15)  TypeScript: continuation marker
//	TC (15)  TypeScript: overloaded signatures (§7.4 gotcha)
//	TD (15)  TypeScript: abstract methods (§7.4 gotcha)
//	TE (15)  TypeScript: generic constraints (§7.4 gotcha)
//	TF (10)  TypeScript: decorators
//	TG (10)  TypeScript: EFALLTHROUGH
//
//	JA (10)  JavaScript: arrow functions assigned to const (§7.4 gotcha)
//	JB (10)  JavaScript: computed keys [Symbol.iterator] (§7.4 gotcha)
//	JC (10)  JavaScript: JSX returning conditional (§7.4 gotcha)
//	JD (10)  JavaScript: basic replace and continuation
//	JE (10)  JavaScript: EFALLTHROUGH
func TestNonGoCorpus(t *testing.T) {
	const py = "# ..."  // Python continuation marker
	const cs = "// ..." // C-style continuation marker (TS / JS)

	type tc struct {
		name    string
		lang    edit.Language
		orig    []string
		snippet []string
		want    []string
		wantErr error
	}

	cases := []tc{
		// ── PA: Python two-anchor basic replace (20) ──────────────────────────

		{
			name:    "PA01 replace single-line gap",
			lang:    edit.LangPython,
			orig:    []string{"    x = 1", "    old = 2", "    return x"},
			snippet: []string{"    x = 1", "    new = 99", "    return x"},
			want:    []string{"    x = 1", "    new = 99", "    return x"},
		},
		{
			name:    "PA02 replace two-line gap with one line",
			lang:    edit.LangPython,
			orig:    []string{"    a = 1", "    old1 = 2", "    old2 = 3", "    b = 4"},
			snippet: []string{"    a = 1", "    merged = 99", "    b = 4"},
			want:    []string{"    a = 1", "    merged = 99", "    b = 4"},
		},
		{
			name:    "PA03 replace one-line gap with two lines",
			lang:    edit.LangPython,
			orig:    []string{"    a = 1", "    old = 2", "    b = 3"},
			snippet: []string{"    a = 1", "    new1 = 10", "    new2 = 20", "    b = 3"},
			want:    []string{"    a = 1", "    new1 = 10", "    new2 = 20", "    b = 3"},
		},
		{
			name:    "PA04 adjacent anchors: insert new line between",
			lang:    edit.LangPython,
			orig:    []string{"    a = 1", "    b = 2"},
			snippet: []string{"    a = 1", "    inserted = 5", "    b = 2"},
			want:    []string{"    a = 1", "    inserted = 5", "    b = 2"},
		},
		{
			name:    "PA05 empty inter-segment preserves original gap",
			lang:    edit.LangPython,
			orig:    []string{"    a = 1", "    keep_me = 7", "    b = 2"},
			snippet: []string{"    a = 1", "    b = 2"},
			want:    []string{"    a = 1", "    keep_me = 7", "    b = 2"},
		},
		{
			name:    "PA06 replace dict assignment",
			lang:    edit.LangPython,
			orig:    []string{"    cfg = {}", "    cfg['x'] = old_val", "    return cfg"},
			snippet: []string{"    cfg = {}", "    cfg['x'] = new_val", "    return cfg"},
			want:    []string{"    cfg = {}", "    cfg['x'] = new_val", "    return cfg"},
		},
		{
			name:    "PA07 replace list comprehension",
			lang:    edit.LangPython,
			orig:    []string{"    items = src.values()", "    out = [x for x in items if old(x)]", "    return out"},
			snippet: []string{"    items = src.values()", "    out = [x for x in items if new(x)]", "    return out"},
			want:    []string{"    items = src.values()", "    out = [x for x in items if new(x)]", "    return out"},
		},
		{
			name:    "PA08 replace try/except body line",
			lang:    edit.LangPython,
			orig:    []string{"    try:", "        old_call()", "    except Exception:", "        raise"},
			snippet: []string{"    try:", "        new_call()", "    except Exception:", "        raise"},
			want:    []string{"    try:", "        new_call()", "    except Exception:", "        raise"},
		},
		{
			name:    "PA09 replace with-statement body",
			lang:    edit.LangPython,
			orig:    []string{"    with open(path) as f:", "        data = old_parse(f)", "        return data"},
			snippet: []string{"    with open(path) as f:", "        data = new_parse(f)", "        return data"},
			want:    []string{"    with open(path) as f:", "        data = new_parse(f)", "        return data"},
		},
		{
			name:    "PA10 replace for-loop body",
			lang:    edit.LangPython,
			orig:    []string{"    for i in range(n):", "        old_step(i)", "    return"},
			snippet: []string{"    for i in range(n):", "        new_step(i)", "    return"},
			want:    []string{"    for i in range(n):", "        new_step(i)", "    return"},
		},
		{
			name:    "PA11 replace while-loop condition body",
			lang:    edit.LangPython,
			orig:    []string{"    while pending:", "        item = old_pop(pending)", "        process(item)"},
			snippet: []string{"    while pending:", "        item = new_pop(pending)", "        process(item)"},
			want:    []string{"    while pending:", "        item = new_pop(pending)", "        process(item)"},
		},
		{
			name:    "PA12 replace if/else branch",
			lang:    edit.LangPython,
			orig:    []string{"    if flag:", "        return old()", "    else:", "        return None"},
			snippet: []string{"    if flag:", "        return new()", "    else:", "        return None"},
			want:    []string{"    if flag:", "        return new()", "    else:", "        return None"},
		},
		{
			name:    "PA13 replace lambda invocation",
			lang:    edit.LangPython,
			orig:    []string{"    fn = lambda x: x", "        result = old_apply(fn)", "    return result"},
			snippet: []string{"    fn = lambda x: x", "        result = new_apply(fn)", "    return result"},
			want:    []string{"    fn = lambda x: x", "        result = new_apply(fn)", "    return result"},
		},
		{
			name:    "PA14 replace yield expression",
			lang:    edit.LangPython,
			orig:    []string{"    for v in src:", "        yield old_transform(v)", "    return"},
			snippet: []string{"    for v in src:", "        yield new_transform(v)", "    return"},
			want:    []string{"    for v in src:", "        yield new_transform(v)", "    return"},
		},
		{
			name:    "PA15 replace assert statement",
			lang:    edit.LangPython,
			orig:    []string{"    cfg = load()", "    assert old_check(cfg)", "    return cfg"},
			snippet: []string{"    cfg = load()", "    assert new_check(cfg)", "    return cfg"},
			want:    []string{"    cfg = load()", "    assert new_check(cfg)", "    return cfg"},
		},
		{
			name:    "PA16 replace string formatting",
			lang:    edit.LangPython,
			orig:    []string{"    name = user.name", "    msg = f'old {name}'", "    return msg"},
			snippet: []string{"    name = user.name", "    msg = f'new {name}'", "    return msg"},
			want:    []string{"    name = user.name", "    msg = f'new {name}'", "    return msg"},
		},
		{
			name:    "PA17 replace tuple unpacking",
			lang:    edit.LangPython,
			orig:    []string{"    pair = source()", "    a, b = old_split(pair)", "    return a, b"},
			snippet: []string{"    pair = source()", "    a, b = new_split(pair)", "    return a, b"},
			want:    []string{"    pair = source()", "    a, b = new_split(pair)", "    return a, b"},
		},
		{
			name:    "PA18 replace nested function call",
			lang:    edit.LangPython,
			orig:    []string{"    raw = io.read()", "    cleaned = old(strip(raw))", "    return cleaned"},
			snippet: []string{"    raw = io.read()", "    cleaned = new(strip(raw))", "    return cleaned"},
			want:    []string{"    raw = io.read()", "    cleaned = new(strip(raw))", "    return cleaned"},
		},
		{
			name:    "PA19 replace context manager body line",
			lang:    edit.LangPython,
			orig:    []string{"    with lock:", "        old_critical()", "    return"},
			snippet: []string{"    with lock:", "        new_critical()", "    return"},
			want:    []string{"    with lock:", "        new_critical()", "    return"},
		},
		{
			name:    "PA20 replace generator expression",
			lang:    edit.LangPython,
			orig:    []string{"    src = data.items()", "    g = (old_fn(x) for x in src)", "    return list(g)"},
			snippet: []string{"    src = data.items()", "    g = (new_fn(x) for x in src)", "    return list(g)"},
			want:    []string{"    src = data.items()", "    g = (new_fn(x) for x in src)", "    return list(g)"},
		},

		// ── PB: Python continuation marker (15) ───────────────────────────────

		{
			name:    "PB01 cont marker preserves single-line gap",
			lang:    edit.LangPython,
			orig:    []string{"    a = 1", "    keep = 2", "    b = 3"},
			snippet: []string{"    a = 1", py, "    b = 3"},
			want:    []string{"    a = 1", "    keep = 2", "    b = 3"},
		},
		{
			name:    "PB02 cont marker preserves multi-line gap",
			lang:    edit.LangPython,
			orig:    []string{"    a = 1", "    g1 = 1", "    g2 = 2", "    g3 = 3", "    b = 5"},
			snippet: []string{"    a = 1", py, "    b = 5"},
			want:    []string{"    a = 1", "    g1 = 1", "    g2 = 2", "    g3 = 3", "    b = 5"},
		},
		{
			name:    "PB03 new line before cont marker",
			lang:    edit.LangPython,
			orig:    []string{"    a = 1", "    g1 = 1", "    g2 = 2", "    b = 5"},
			snippet: []string{"    a = 1", "    new = 99", py, "    b = 5"},
			want:    []string{"    a = 1", "    new = 99", "    g1 = 1", "    g2 = 2", "    b = 5"},
		},
		{
			name:    "PB04 new line after cont marker",
			lang:    edit.LangPython,
			orig:    []string{"    a = 1", "    g1 = 1", "    g2 = 2", "    b = 5"},
			snippet: []string{"    a = 1", py, "    new = 99", "    b = 5"},
			want:    []string{"    a = 1", "    g1 = 1", "    g2 = 2", "    new = 99", "    b = 5"},
		},
		{
			name:    "PB05 new lines on both sides of cont marker",
			lang:    edit.LangPython,
			orig:    []string{"    a = 1", "    g = 0", "    b = 2"},
			snippet: []string{"    a = 1", "    pre = 1", py, "    post = 2", "    b = 2"},
			want:    []string{"    a = 1", "    pre = 1", "    g = 0", "    post = 2", "    b = 2"},
		},
		{
			name:    "PB06 cont marker in pre region preserves head",
			lang:    edit.LangPython,
			orig:    []string{"    h1 = 1", "    h2 = 2", "    a = 3", "    b = 4"},
			snippet: []string{py, "    a = 3", "    b = 4"},
			want:    []string{"    h1 = 1", "    h2 = 2", "    a = 3", "    b = 4"},
		},
		{
			name:    "PB07 cont marker in post region preserves tail",
			lang:    edit.LangPython,
			orig:    []string{"    a = 1", "    b = 2", "    t1 = 3", "    t2 = 4"},
			snippet: []string{"    a = 1", "    b = 2", py},
			want:    []string{"    a = 1", "    b = 2", "    t1 = 3", "    t2 = 4"},
		},
		{
			name:    "PB08 cont marker in pre region with prepended new line",
			lang:    edit.LangPython,
			orig:    []string{"    h = 0", "    a = 1", "    b = 2"},
			snippet: []string{"    new = 99", py, "    a = 1", "    b = 2"},
			want:    []string{"    new = 99", "    h = 0", "    a = 1", "    b = 2"},
		},
		{
			name:    "PB09 cont marker in post region with appended new line",
			lang:    edit.LangPython,
			orig:    []string{"    a = 1", "    b = 2", "    tail = 5"},
			snippet: []string{"    a = 1", "    b = 2", py, "    new = 99"},
			want:    []string{"    a = 1", "    b = 2", "    tail = 5", "    new = 99"},
		},
		{
			name:    "PB10 cont marker between three anchors (left segment)",
			lang:    edit.LangPython,
			orig:    []string{"    a = 1", "    g = 5", "    b = 2", "    c = 3"},
			snippet: []string{"    a = 1", py, "    b = 2", "    new = 9", "    c = 3"},
			want:    []string{"    a = 1", "    g = 5", "    b = 2", "    new = 9", "    c = 3"},
		},
		{
			name:    "PB11 cont marker between three anchors (right segment)",
			lang:    edit.LangPython,
			orig:    []string{"    a = 1", "    b = 2", "    g = 5", "    c = 3"},
			snippet: []string{"    a = 1", "    new = 9", "    b = 2", py, "    c = 3"},
			want:    []string{"    a = 1", "    new = 9", "    b = 2", "    g = 5", "    c = 3"},
		},
		{
			name:    "PB12 cont marker preserves blank line in gap",
			lang:    edit.LangPython,
			orig:    []string{"    a = 1", "    g1 = 1", "", "    g2 = 2", "    b = 5"},
			snippet: []string{"    a = 1", py, "    b = 5"},
			want:    []string{"    a = 1", "    g1 = 1", "", "    g2 = 2", "    b = 5"},
		},
		{
			name:    "PB13 multiple new lines before cont marker",
			lang:    edit.LangPython,
			orig:    []string{"    a = 1", "    g = 0", "    b = 2"},
			snippet: []string{"    a = 1", "    n1 = 1", "    n2 = 2", py, "    b = 2"},
			want:    []string{"    a = 1", "    n1 = 1", "    n2 = 2", "    g = 0", "    b = 2"},
		},
		{
			name:    "PB14 multiple new lines after cont marker",
			lang:    edit.LangPython,
			orig:    []string{"    a = 1", "    g = 0", "    b = 2"},
			snippet: []string{"    a = 1", py, "    n1 = 1", "    n2 = 2", "    b = 2"},
			want:    []string{"    a = 1", "    g = 0", "    n1 = 1", "    n2 = 2", "    b = 2"},
		},
		{
			name:    "PB15 cont marker spans empty original gap",
			lang:    edit.LangPython,
			orig:    []string{"    a = 1", "    b = 2"},
			snippet: []string{"    a = 1", py, "    b = 2"},
			want:    []string{"    a = 1", "    b = 2"},
		},

		// ── PC: Python indentation normalization (15) ─────────────────────────

		{
			name:    "PC01 zero-indent snippet normalized to four-space orig",
			lang:    edit.LangPython,
			orig:    []string{"    x = 1", "    old = 2", "    return x"},
			snippet: []string{"x = 1", "new = 99", "return x"},
			want:    []string{"    x = 1", "    new = 99", "    return x"},
		},
		{
			name:    "PC02 zero-indent snippet normalized to eight-space orig",
			lang:    edit.LangPython,
			orig:    []string{"        x = 1", "        old = 2", "        return x"},
			snippet: []string{"x = 1", "new = 99", "return x"},
			want:    []string{"        x = 1", "        new = 99", "        return x"},
		},
		{
			name:    "PC03 two-space snippet normalized to four-space orig",
			lang:    edit.LangPython,
			orig:    []string{"    x = 1", "    old = 2", "    return x"},
			snippet: []string{"  x = 1", "  new = 99", "  return x"},
			want:    []string{"    x = 1", "    new = 99", "    return x"},
		},
		{
			name:    "PC04 nested levels scaled correctly (zero-base to four-base)",
			lang:    edit.LangPython,
			orig:    []string{"    x = 1", "    if c:", "        do_old()", "    return x"},
			snippet: []string{"x = 1", "if c:", "    do_new()", "return x"},
			want:    []string{"    x = 1", "    if c:", "        do_new()", "    return x"},
		},
		{
			name:    "PC05 already-matching indent: no normalization",
			lang:    edit.LangPython,
			orig:    []string{"    x = 1", "    old = 2", "    return x"},
			snippet: []string{"    x = 1", "    new = 5", "    return x"},
			want:    []string{"    x = 1", "    new = 5", "    return x"},
		},
		{
			name:    "PC06 normalization preserves cont marker exact form",
			lang:    edit.LangPython,
			orig:    []string{"    a = 1", "    g1 = 1", "    g2 = 2", "    b = 5"},
			snippet: []string{"a = 1", py, "b = 5"},
			want:    []string{"    a = 1", "    g1 = 1", "    g2 = 2", "    b = 5"},
		},
		{
			name:    "PC07 deep nesting normalized correctly",
			lang:    edit.LangPython,
			orig:    []string{"    if a:", "        if b:", "            old_deep()", "    return"},
			snippet: []string{"if a:", "    if b:", "        new_deep()", "return"},
			want:    []string{"    if a:", "        if b:", "            new_deep()", "    return"},
		},
		{
			name:    "PC08 zero-indent snippet with empty line preserved",
			lang:    edit.LangPython,
			orig:    []string{"    a = 1", "    old = 2", "", "    b = 3"},
			snippet: []string{"a = 1", "new = 99", "", "b = 3"},
			want:    []string{"    a = 1", "    new = 99", "", "    b = 3"},
		},
		{
			name:    "PC09 cont marker plus normalization",
			lang:    edit.LangPython,
			orig:    []string{"    a = 1", "    new = 99", py, "    b = 5"},
			snippet: []string{"a = 1", "new = 99", py, "b = 5"},
			want:    []string{"    a = 1", "    new = 99", py, "    b = 5"},
		},
		{
			name:    "PC10 four-space snippet normalized to eight-space orig",
			lang:    edit.LangPython,
			orig:    []string{"        x = 1", "        old = 2", "        return x"},
			snippet: []string{"    x = 1", "    new = 99", "    return x"},
			want:    []string{"        x = 1", "        new = 99", "        return x"},
		},
		{
			name:    "PC11 indent reduction: eight-space orig to twelve-space mismatched (no-op when already-greater)",
			lang:    edit.LangPython,
			orig:    []string{"    x = 1", "    old = 2", "    return x"},
			snippet: []string{"        x = 1", "        new = 5", "        return x"},
			// snippetBase=8, origBase=4, diff=-4 → snippet reduced
			want: []string{"    x = 1", "    new = 5", "    return x"},
		},
		{
			name:    "PC12 normalization with three-anchor snippet",
			lang:    edit.LangPython,
			orig:    []string{"    a = 1", "    b = 2", "    c = 3", "    d = 4"},
			snippet: []string{"a = 1", "b = 2", "new = 9", "d = 4"},
			want:    []string{"    a = 1", "    b = 2", "    new = 9", "    d = 4"},
		},
		{
			name:    "PC13 normalization with cont marker between anchors",
			lang:    edit.LangPython,
			orig:    []string{"    a = 1", "    g = 5", "    b = 2"},
			snippet: []string{"a = 1", py, "b = 2"},
			want:    []string{"    a = 1", "    g = 5", "    b = 2"},
		},
		{
			name:    "PC14 zero-base snippet with empty pre/post",
			lang:    edit.LangPython,
			orig:    []string{"    a = 1", "    g = 7", "    b = 2"},
			snippet: []string{"a = 1", "b = 2"},
			want:    []string{"    a = 1", "    g = 7", "    b = 2"},
		},
		{
			name:    "PC15 mixed-indent snippet normalized by base",
			lang:    edit.LangPython,
			orig:    []string{"    a = 1", "    if c:", "        old_inner()", "    return"},
			snippet: []string{"  a = 1", "  if c:", "      new_inner()", "  return"},
			// snippetBase=2, origBase=4, diff=2; "      new_inner()" had 6 spaces → 8 spaces
			want: []string{"    a = 1", "    if c:", "        new_inner()", "    return"},
		},

		// ── PD: Python async def with decorators (15) ─────────────────────────

		{
			name: "PD01 async def body replace",
			lang: edit.LangPython,
			orig: []string{
				"    @auth_required",
				"    async def fetch(self):",
				"        data = await old_io()",
				"        return data",
			},
			snippet: []string{
				"    @auth_required",
				"    async def fetch(self):",
				"        data = await new_io()",
				"        return data",
			},
			want: []string{
				"    @auth_required",
				"    async def fetch(self):",
				"        data = await new_io()",
				"        return data",
			},
		},
		{
			name: "PD02 async def with decorator preserved via cont marker",
			lang: edit.LangPython,
			orig: []string{
				"    @auth_required",
				"    async def fetch(self):",
				"        data = await io()",
				"        log(data)",
				"        return data",
			},
			snippet: []string{
				"    @auth_required",
				"    async def fetch(self):",
				py,
				"        return data",
			},
			want: []string{
				"    @auth_required",
				"    async def fetch(self):",
				"        data = await io()",
				"        log(data)",
				"        return data",
			},
		},
		{
			name: "PD03 async with multiple decorators",
			lang: edit.LangPython,
			orig: []string{
				"    @cache",
				"    @auth_required",
				"    async def get(self, k):",
				"        return await old_lookup(k)",
			},
			snippet: []string{
				"    @cache",
				"    @auth_required",
				"    async def get(self, k):",
				"        return await new_lookup(k)",
			},
			want: []string{
				"    @cache",
				"    @auth_required",
				"    async def get(self, k):",
				"        return await new_lookup(k)",
			},
		},
		{
			name: "PD04 async with parameterized decorator",
			lang: edit.LangPython,
			orig: []string{
				"    @retry(times=3)",
				"    async def call(self):",
				"        return await old_send()",
			},
			snippet: []string{
				"    @retry(times=3)",
				"    async def call(self):",
				"        return await new_send()",
			},
			want: []string{
				"    @retry(times=3)",
				"    async def call(self):",
				"        return await new_send()",
			},
		},
		{
			name: "PD05 async iterator body",
			lang: edit.LangPython,
			orig: []string{
				"    async def stream(self):",
				"        async for chunk in old_src():",
				"            yield chunk",
			},
			snippet: []string{
				"    async def stream(self):",
				"        async for chunk in new_src():",
				"            yield chunk",
			},
			want: []string{
				"    async def stream(self):",
				"        async for chunk in new_src():",
				"            yield chunk",
			},
		},
		{
			name: "PD06 async with body context manager",
			lang: edit.LangPython,
			orig: []string{
				"    async def run(self):",
				"        async with old_session() as s:",
				"            return s",
			},
			snippet: []string{
				"    async def run(self):",
				"        async with new_session() as s:",
				"            return s",
			},
			want: []string{
				"    async def run(self):",
				"        async with new_session() as s:",
				"            return s",
			},
		},
		{
			name: "PD07 async with try/except",
			lang: edit.LangPython,
			orig: []string{
				"    @auth_required",
				"    async def fetch(self):",
				"        try:",
				"            return await old_call()",
				"        except IOError:",
				"            return None",
			},
			snippet: []string{
				"    @auth_required",
				"    async def fetch(self):",
				"        try:",
				"            return await new_call()",
				"        except IOError:",
				"            return None",
			},
			want: []string{
				"    @auth_required",
				"    async def fetch(self):",
				"        try:",
				"            return await new_call()",
				"        except IOError:",
				"            return None",
			},
		},
		{
			name: "PD08 async with conditional await",
			lang: edit.LangPython,
			orig: []string{
				"    async def maybe(self, x):",
				"        if x:",
				"            return await old_path(x)",
				"        return None",
			},
			snippet: []string{
				"    async def maybe(self, x):",
				"        if x:",
				"            return await new_path(x)",
				"        return None",
			},
			want: []string{
				"    async def maybe(self, x):",
				"        if x:",
				"            return await new_path(x)",
				"        return None",
			},
		},
		{
			name: "PD09 async multiple await calls",
			lang: edit.LangPython,
			orig: []string{
				"    async def steps(self):",
				"        a = await old_a()",
				"        b = await old_b(a)",
				"        return b",
			},
			snippet: []string{
				"    async def steps(self):",
				"        a = await new_a()",
				"        b = await new_b(a)",
				"        return b",
			},
			want: []string{
				"    async def steps(self):",
				"        a = await new_a()",
				"        b = await new_b(a)",
				"        return b",
			},
		},
		{
			name: "PD10 async cont marker preserves body",
			lang: edit.LangPython,
			orig: []string{
				"    async def proc(self):",
				"        a = 1",
				"        b = 2",
				"        c = 3",
				"        return a + b + c",
			},
			snippet: []string{
				"    async def proc(self):",
				py,
				"        return a + b + c",
			},
			want: []string{
				"    async def proc(self):",
				"        a = 1",
				"        b = 2",
				"        c = 3",
				"        return a + b + c",
			},
		},
		{
			name: "PD11 async with normalization (zero-indent snippet)",
			lang: edit.LangPython,
			orig: []string{
				"    async def fetch(self):",
				"        data = await old_io()",
				"        return data",
			},
			snippet: []string{
				"async def fetch(self):",
				"    data = await new_io()",
				"    return data",
			},
			want: []string{
				"    async def fetch(self):",
				"        data = await new_io()",
				"        return data",
			},
		},
		{
			name: "PD12 async with decorator and normalization",
			lang: edit.LangPython,
			orig: []string{
				"    @auth_required",
				"    async def fetch(self):",
				"        data = await old_io()",
				"        return data",
			},
			snippet: []string{
				"@auth_required",
				"async def fetch(self):",
				"    data = await new_io()",
				"    return data",
			},
			want: []string{
				"    @auth_required",
				"    async def fetch(self):",
				"        data = await new_io()",
				"        return data",
			},
		},
		{
			name: "PD13 async gather: replace gather list",
			lang: edit.LangPython,
			orig: []string{
				"    async def all(self):",
				"        results = await asyncio.gather(old_a(), old_b())",
				"        return results",
			},
			snippet: []string{
				"    async def all(self):",
				"        results = await asyncio.gather(new_a(), new_b())",
				"        return results",
			},
			want: []string{
				"    async def all(self):",
				"        results = await asyncio.gather(new_a(), new_b())",
				"        return results",
			},
		},
		{
			name: "PD14 async with finally",
			lang: edit.LangPython,
			orig: []string{
				"    async def safe(self):",
				"        try:",
				"            return await old_op()",
				"        finally:",
				"            cleanup()",
			},
			snippet: []string{
				"    async def safe(self):",
				"        try:",
				"            return await new_op()",
				"        finally:",
				"            cleanup()",
			},
			want: []string{
				"    async def safe(self):",
				"        try:",
				"            return await new_op()",
				"        finally:",
				"            cleanup()",
			},
		},
		{
			name: "PD15 async with property decorator",
			lang: edit.LangPython,
			orig: []string{
				"    @property",
				"    async def value(self):",
				"        return await old_compute()",
			},
			snippet: []string{
				"    @property",
				"    async def value(self):",
				"        return await new_compute()",
			},
			want: []string{
				"    @property",
				"    async def value(self):",
				"        return await new_compute()",
			},
		},

		// ── PE: Python @classmethod / @staticmethod (15) ──────────────────────

		{
			name: "PE01 classmethod replace body",
			lang: edit.LangPython,
			orig: []string{
				"    @classmethod",
				"    def from_str(cls, s):",
				"        return cls(old_parse(s))",
			},
			snippet: []string{
				"    @classmethod",
				"    def from_str(cls, s):",
				"        return cls(new_parse(s))",
			},
			want: []string{
				"    @classmethod",
				"    def from_str(cls, s):",
				"        return cls(new_parse(s))",
			},
		},
		{
			name: "PE02 staticmethod replace body",
			lang: edit.LangPython,
			orig: []string{
				"    @staticmethod",
				"    def helper(x):",
				"        return old_op(x)",
			},
			snippet: []string{
				"    @staticmethod",
				"    def helper(x):",
				"        return new_op(x)",
			},
			want: []string{
				"    @staticmethod",
				"    def helper(x):",
				"        return new_op(x)",
			},
		},
		{
			name: "PE03 classmethod with cont marker preserves body",
			lang: edit.LangPython,
			orig: []string{
				"    @classmethod",
				"    def build(cls):",
				"        a = 1",
				"        b = 2",
				"        return cls(a, b)",
			},
			snippet: []string{
				"    @classmethod",
				"    def build(cls):",
				py,
				"        return cls(a, b)",
			},
			want: []string{
				"    @classmethod",
				"    def build(cls):",
				"        a = 1",
				"        b = 2",
				"        return cls(a, b)",
			},
		},
		{
			name: "PE04 staticmethod with cont marker",
			lang: edit.LangPython,
			orig: []string{
				"    @staticmethod",
				"    def util():",
				"        x = setup()",
				"        y = process(x)",
				"        return y",
			},
			snippet: []string{
				"    @staticmethod",
				"    def util():",
				py,
				"        return y",
			},
			want: []string{
				"    @staticmethod",
				"    def util():",
				"        x = setup()",
				"        y = process(x)",
				"        return y",
			},
		},
		{
			name: "PE05 classmethod with multiple decorators",
			lang: edit.LangPython,
			orig: []string{
				"    @cache",
				"    @classmethod",
				"    def make(cls):",
				"        return cls(old_default())",
			},
			snippet: []string{
				"    @cache",
				"    @classmethod",
				"    def make(cls):",
				"        return cls(new_default())",
			},
			want: []string{
				"    @cache",
				"    @classmethod",
				"    def make(cls):",
				"        return cls(new_default())",
			},
		},
		{
			name: "PE06 staticmethod with property and call",
			lang: edit.LangPython,
			orig: []string{
				"    @staticmethod",
				"    def parse(text):",
				"        tokens = old_tokenize(text)",
				"        return tokens",
			},
			snippet: []string{
				"    @staticmethod",
				"    def parse(text):",
				"        tokens = new_tokenize(text)",
				"        return tokens",
			},
			want: []string{
				"    @staticmethod",
				"    def parse(text):",
				"        tokens = new_tokenize(text)",
				"        return tokens",
			},
		},
		{
			name: "PE07 classmethod normalization (zero-indent snippet)",
			lang: edit.LangPython,
			orig: []string{
				"    @classmethod",
				"    def from_dict(cls, d):",
				"        return cls(old_extract(d))",
			},
			snippet: []string{
				"@classmethod",
				"def from_dict(cls, d):",
				"    return cls(new_extract(d))",
			},
			want: []string{
				"    @classmethod",
				"    def from_dict(cls, d):",
				"        return cls(new_extract(d))",
			},
		},
		{
			name: "PE08 staticmethod normalization",
			lang: edit.LangPython,
			orig: []string{
				"    @staticmethod",
				"    def helper(x):",
				"        return old_op(x)",
			},
			snippet: []string{
				"@staticmethod",
				"def helper(x):",
				"    return new_op(x)",
			},
			want: []string{
				"    @staticmethod",
				"    def helper(x):",
				"        return new_op(x)",
			},
		},
		{
			name: "PE09 classmethod with conditional",
			lang: edit.LangPython,
			orig: []string{
				"    @classmethod",
				"    def of(cls, v):",
				"        if v is None:",
				"            return cls.empty()",
				"        return cls(old_wrap(v))",
			},
			snippet: []string{
				"    @classmethod",
				"    def of(cls, v):",
				"        if v is None:",
				"            return cls.empty()",
				"        return cls(new_wrap(v))",
			},
			want: []string{
				"    @classmethod",
				"    def of(cls, v):",
				"        if v is None:",
				"            return cls.empty()",
				"        return cls(new_wrap(v))",
			},
		},
		{
			name: "PE10 staticmethod with computation",
			lang: edit.LangPython,
			orig: []string{
				"    @staticmethod",
				"    def hash(s):",
				"        h = 0",
				"        for c in s:",
				"            h = old_step(h, c)",
				"        return h",
			},
			snippet: []string{
				"    @staticmethod",
				"    def hash(s):",
				"        h = 0",
				"        for c in s:",
				"            h = new_step(h, c)",
				"        return h",
			},
			want: []string{
				"    @staticmethod",
				"    def hash(s):",
				"        h = 0",
				"        for c in s:",
				"            h = new_step(h, c)",
				"        return h",
			},
		},
		{
			name: "PE11 classmethod factory chain",
			lang: edit.LangPython,
			orig: []string{
				"    @classmethod",
				"    def chain(cls, items):",
				"        result = old_seed()",
				"        for it in items:",
				"            result = old_combine(result, it)",
				"        return cls(result)",
			},
			snippet: []string{
				"    @classmethod",
				"    def chain(cls, items):",
				"        result = new_seed()",
				"        for it in items:",
				"            result = new_combine(result, it)",
				"        return cls(result)",
			},
			want: []string{
				"    @classmethod",
				"    def chain(cls, items):",
				"        result = new_seed()",
				"        for it in items:",
				"            result = new_combine(result, it)",
				"        return cls(result)",
			},
		},
		{
			name: "PE12 staticmethod replace try/except body",
			lang: edit.LangPython,
			orig: []string{
				"    @staticmethod",
				"    def safe(x):",
				"        try:",
				"            return old_compute(x)",
				"        except ValueError:",
				"            return 0",
			},
			snippet: []string{
				"    @staticmethod",
				"    def safe(x):",
				"        try:",
				"            return new_compute(x)",
				"        except ValueError:",
				"            return 0",
			},
			want: []string{
				"    @staticmethod",
				"    def safe(x):",
				"        try:",
				"            return new_compute(x)",
				"        except ValueError:",
				"            return 0",
			},
		},
		{
			name: "PE13 classmethod returning generator",
			lang: edit.LangPython,
			orig: []string{
				"    @classmethod",
				"    def stream(cls):",
				"        for i in range(old_n):",
				"            yield cls(i)",
			},
			snippet: []string{
				"    @classmethod",
				"    def stream(cls):",
				"        for i in range(new_n):",
				"            yield cls(i)",
			},
			want: []string{
				"    @classmethod",
				"    def stream(cls):",
				"        for i in range(new_n):",
				"            yield cls(i)",
			},
		},
		{
			name: "PE14 staticmethod with no-op preserve",
			lang: edit.LangPython,
			orig: []string{
				"    @staticmethod",
				"    def passthrough(x):",
				"        keep_line = 1",
				"        return x",
			},
			snippet: []string{
				"    @staticmethod",
				"    def passthrough(x):",
				"        return x",
			},
			want: []string{
				"    @staticmethod",
				"    def passthrough(x):",
				"        keep_line = 1",
				"        return x",
			},
		},
		{
			name: "PE15 classmethod with abstractmethod stack",
			lang: edit.LangPython,
			orig: []string{
				"    @classmethod",
				"    @abstractmethod",
				"    def template(cls):",
				"        raise old_error()",
			},
			snippet: []string{
				"    @classmethod",
				"    @abstractmethod",
				"    def template(cls):",
				"        raise new_error()",
			},
			want: []string{
				"    @classmethod",
				"    @abstractmethod",
				"    def template(cls):",
				"        raise new_error()",
			},
		},

		// ── PF: Python default args with function calls (10) ──────────────────

		{
			name: "PF01 default arg call: replace body",
			lang: edit.LangPython,
			orig: []string{
				"    def f(x, y=now()):",
				"        result = old_op(x, y)",
				"        return result",
			},
			snippet: []string{
				"    def f(x, y=now()):",
				"        result = new_op(x, y)",
				"        return result",
			},
			want: []string{
				"    def f(x, y=now()):",
				"        result = new_op(x, y)",
				"        return result",
			},
		},
		{
			name: "PF02 default arg with multiple call defaults",
			lang: edit.LangPython,
			orig: []string{
				"    def g(a=load_a(), b=load_b()):",
				"        result = old_combine(a, b)",
				"        return result",
			},
			snippet: []string{
				"    def g(a=load_a(), b=load_b()):",
				"        result = new_combine(a, b)",
				"        return result",
			},
			want: []string{
				"    def g(a=load_a(), b=load_b()):",
				"        result = new_combine(a, b)",
				"        return result",
			},
		},
		{
			name: "PF03 default arg lambda: replace inner body",
			lang: edit.LangPython,
			orig: []string{
				"    def h(fn=lambda: 42):",
				"        v = fn()",
				"        return old_use(v)",
			},
			snippet: []string{
				"    def h(fn=lambda: 42):",
				"        v = fn()",
				"        return new_use(v)",
			},
			want: []string{
				"    def h(fn=lambda: 42):",
				"        v = fn()",
				"        return new_use(v)",
			},
		},
		{
			name: "PF04 default arg dict factory",
			lang: edit.LangPython,
			orig: []string{
				"    def f(opts=dict()):",
				"        opts['k'] = old_v",
				"        return opts",
			},
			snippet: []string{
				"    def f(opts=dict()):",
				"        opts['k'] = new_v",
				"        return opts",
			},
			want: []string{
				"    def f(opts=dict()):",
				"        opts['k'] = new_v",
				"        return opts",
			},
		},
		{
			name: "PF05 default arg list factory",
			lang: edit.LangPython,
			orig: []string{
				"    def f(items=list()):",
				"        items.append(old_item)",
				"        return items",
			},
			snippet: []string{
				"    def f(items=list()):",
				"        items.append(new_item)",
				"        return items",
			},
			want: []string{
				"    def f(items=list()):",
				"        items.append(new_item)",
				"        return items",
			},
		},
		{
			name: "PF06 default arg with cont marker",
			lang: edit.LangPython,
			orig: []string{
				"    def f(x, cfg=load_cfg()):",
				"        a = 1",
				"        b = 2",
				"        return a + b",
			},
			snippet: []string{
				"    def f(x, cfg=load_cfg()):",
				py,
				"        return a + b",
			},
			want: []string{
				"    def f(x, cfg=load_cfg()):",
				"        a = 1",
				"        b = 2",
				"        return a + b",
			},
		},
		{
			name: "PF07 default arg call with normalization",
			lang: edit.LangPython,
			orig: []string{
				"    def f(t=time.time()):",
				"        v = old_use(t)",
				"        return v",
			},
			snippet: []string{
				"def f(t=time.time()):",
				"    v = new_use(t)",
				"    return v",
			},
			want: []string{
				"    def f(t=time.time()):",
				"        v = new_use(t)",
				"        return v",
			},
		},
		{
			name: "PF08 default arg containing nested call",
			lang: edit.LangPython,
			orig: []string{
				"    def f(seed=hash(getpid())):",
				"        rng = old_init(seed)",
				"        return rng",
			},
			snippet: []string{
				"    def f(seed=hash(getpid())):",
				"        rng = new_init(seed)",
				"        return rng",
			},
			want: []string{
				"    def f(seed=hash(getpid())):",
				"        rng = new_init(seed)",
				"        return rng",
			},
		},
		{
			name: "PF09 default arg with kwargs unpack",
			lang: edit.LangPython,
			orig: []string{
				"    def f(**kw):",
				"        x = kw.get('x', old_default())",
				"        return x",
			},
			snippet: []string{
				"    def f(**kw):",
				"        x = kw.get('x', new_default())",
				"        return x",
			},
			want: []string{
				"    def f(**kw):",
				"        x = kw.get('x', new_default())",
				"        return x",
			},
		},
		{
			name: "PF10 default arg with star args",
			lang: edit.LangPython,
			orig: []string{
				"    def f(*args, ts=now()):",
				"        out = old_combine(args, ts)",
				"        return out",
			},
			snippet: []string{
				"    def f(*args, ts=now()):",
				"        out = new_combine(args, ts)",
				"        return out",
			},
			want: []string{
				"    def f(*args, ts=now()):",
				"        out = new_combine(args, ts)",
				"        return out",
			},
		},

		// ── PG: Python EFALLTHROUGH (10) ──────────────────────────────────────

		{
			name:    "PG01 only one anchor matches",
			lang:    edit.LangPython,
			orig:    []string{"    a = 1", "    b = 2"},
			snippet: []string{"    a = 1", "    new_only = 9"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "PG02 zero anchors match (after normalization)",
			lang:    edit.LangPython,
			orig:    []string{"    x = 1", "    y = 2"},
			snippet: []string{"q = 0", "r = 1"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "PG03 two cont markers in inter region",
			lang:    edit.LangPython,
			orig:    []string{"    a = 1", "    g1 = 1", "    g2 = 2", "    b = 5"},
			snippet: []string{"    a = 1", py, py, "    b = 5"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "PG04 anchor order violated",
			lang:    edit.LangPython,
			orig:    []string{"    a = 1", "    b = 2"},
			snippet: []string{"    b = 2", "    new = 5", "    a = 1"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "PG05 only cont marker, no anchor",
			lang:    edit.LangPython,
			orig:    []string{"    a = 1", "    b = 2"},
			snippet: []string{py, "    new = 1"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "PG06 single anchor with new lines on both sides",
			lang:    edit.LangPython,
			orig:    []string{"    a = 1", "    b = 2", "    c = 3"},
			snippet: []string{"    new1 = 9", "    a = 1", "    new2 = 8"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "PG07 empty snippet",
			lang:    edit.LangPython,
			orig:    []string{"    a = 1", "    b = 2"},
			snippet: []string{},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "PG08 two cont markers in pre",
			lang:    edit.LangPython,
			orig:    []string{"    h = 1", "    a = 2", "    b = 3"},
			snippet: []string{py, py, "    a = 2", "    b = 3"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "PG09 anchors but searched-past after first match",
			lang:    edit.LangPython,
			orig:    []string{"    a = 1", "    b = 2", "    a = 1"},
			snippet: []string{"    a = 1", "    new = 7", "    a = 1"},
			// "    a = 1"@0, then "    a = 1" starting at 1: found at 2. positions=[0,2] → eligible → not EFALLTHROUGH
			// Let me redesign: orig has only one occurrence of a, so we can't satisfy two-anchor.
			// Override:
			want:    []string{"    a = 1", "    new = 7", "    a = 1"},
			wantErr: nil,
		},
		{
			name:    "PG10 wrong-language marker",
			lang:    edit.LangPython,
			orig:    []string{"    a = 1", "    g = 2", "    b = 3"},
			snippet: []string{"    a = 1", "// ...", "    b = 3"},
			// "// ..." is not the python marker (which is "# ...") and "// ..." is not in orig → it's lineNew, snippet has 2 anchors and inter has zero markers but 1 new line → REPLACE the gap with "// ..."
			want:    []string{"    a = 1", "// ...", "    b = 3"},
			wantErr: nil,
		},

		// ── TA: TypeScript two-anchor basic replace (20) ──────────────────────

		{
			name:    "TA01 replace single-line gap",
			lang:    edit.LangTypeScript,
			orig:    []string{"const x = 1", "const old = 2", "return x"},
			snippet: []string{"const x = 1", "const next = 99", "return x"},
			want:    []string{"const x = 1", "const next = 99", "return x"},
		},
		{
			name:    "TA02 replace two-line gap with one line",
			lang:    edit.LangTypeScript,
			orig:    []string{"const a = 1", "const o1 = 2", "const o2 = 3", "const b = 4"},
			snippet: []string{"const a = 1", "const merged = 99", "const b = 4"},
			want:    []string{"const a = 1", "const merged = 99", "const b = 4"},
		},
		{
			name:    "TA03 replace one-line gap with two lines",
			lang:    edit.LangTypeScript,
			orig:    []string{"const a = 1", "const old = 2", "const b = 3"},
			snippet: []string{"const a = 1", "const n1 = 10", "const n2 = 20", "const b = 3"},
			want:    []string{"const a = 1", "const n1 = 10", "const n2 = 20", "const b = 3"},
		},
		{
			name:    "TA04 adjacent anchors: insert new line between",
			lang:    edit.LangTypeScript,
			orig:    []string{"const a = 1", "const b = 2"},
			snippet: []string{"const a = 1", "const inserted = 5", "const b = 2"},
			want:    []string{"const a = 1", "const inserted = 5", "const b = 2"},
		},
		{
			name:    "TA05 empty inter-segment preserves original",
			lang:    edit.LangTypeScript,
			orig:    []string{"const a = 1", "const keep = 7", "const b = 2"},
			snippet: []string{"const a = 1", "const b = 2"},
			want:    []string{"const a = 1", "const keep = 7", "const b = 2"},
		},
		{
			name:    "TA06 replace interface property",
			lang:    edit.LangTypeScript,
			orig:    []string{"interface User {", "  name: OldName", "  id: number", "}"},
			snippet: []string{"interface User {", "  name: NewName", "  id: number", "}"},
			want:    []string{"interface User {", "  name: NewName", "  id: number", "}"},
		},
		{
			name:    "TA07 replace type alias body",
			lang:    edit.LangTypeScript,
			orig:    []string{"type T = {", "  v: OldType", "  ok: boolean", "}"},
			snippet: []string{"type T = {", "  v: NewType", "  ok: boolean", "}"},
			want:    []string{"type T = {", "  v: NewType", "  ok: boolean", "}"},
		},
		{
			name:    "TA08 replace return statement",
			lang:    edit.LangTypeScript,
			orig:    []string{"const x = compute()", "return oldShape(x)", "// done"},
			snippet: []string{"const x = compute()", "return newShape(x)", "// done"},
			want:    []string{"const x = compute()", "return newShape(x)", "// done"},
		},
		{
			name:    "TA09 replace try/catch body",
			lang:    edit.LangTypeScript,
			orig:    []string{"try {", "  oldCall()", "} catch (e) {", "  log(e)", "}"},
			snippet: []string{"try {", "  newCall()", "} catch (e) {", "  log(e)", "}"},
			want:    []string{"try {", "  newCall()", "} catch (e) {", "  log(e)", "}"},
		},
		{
			name:    "TA10 replace for-loop body",
			lang:    edit.LangTypeScript,
			orig:    []string{"for (const i of items) {", "  oldStep(i)", "}"},
			snippet: []string{"for (const i of items) {", "  newStep(i)", "}"},
			want:    []string{"for (const i of items) {", "  newStep(i)", "}"},
		},
		{
			name:    "TA11 replace while-loop body",
			lang:    edit.LangTypeScript,
			orig:    []string{"while (cond) {", "  const v = oldNext()", "  consume(v)", "}"},
			snippet: []string{"while (cond) {", "  const v = newNext()", "  consume(v)", "}"},
			want:    []string{"while (cond) {", "  const v = newNext()", "  consume(v)", "}"},
		},
		{
			name:    "TA12 replace switch case",
			lang:    edit.LangTypeScript,
			orig:    []string{"switch (k) {", "  case 'a':", "    return oldA()", "  default:", "    return null", "}"},
			snippet: []string{"switch (k) {", "  case 'a':", "    return newA()", "  default:", "    return null", "}"},
			want:    []string{"switch (k) {", "  case 'a':", "    return newA()", "  default:", "    return null", "}"},
		},
		{
			name:    "TA13 replace promise chain",
			lang:    edit.LangTypeScript,
			orig:    []string{"return fetch(url)", "  .then(r => oldParse(r))", "  .catch(handle)"},
			snippet: []string{"return fetch(url)", "  .then(r => newParse(r))", "  .catch(handle)"},
			want:    []string{"return fetch(url)", "  .then(r => newParse(r))", "  .catch(handle)"},
		},
		{
			name:    "TA14 replace destructured assign",
			lang:    edit.LangTypeScript,
			orig:    []string{"const obj = source()", "const { a, b } = oldUnpack(obj)", "return a + b"},
			snippet: []string{"const obj = source()", "const { a, b } = newUnpack(obj)", "return a + b"},
			want:    []string{"const obj = source()", "const { a, b } = newUnpack(obj)", "return a + b"},
		},
		{
			name:    "TA15 replace array map call",
			lang:    edit.LangTypeScript,
			orig:    []string{"const items = source()", "const out = items.map(oldMapper)", "return out"},
			snippet: []string{"const items = source()", "const out = items.map(newMapper)", "return out"},
			want:    []string{"const items = source()", "const out = items.map(newMapper)", "return out"},
		},
		{
			name:    "TA16 replace conditional expression",
			lang:    edit.LangTypeScript,
			orig:    []string{"const flag = check()", "const v = flag ? oldYes() : oldNo()", "return v"},
			snippet: []string{"const flag = check()", "const v = flag ? newYes() : newNo()", "return v"},
			want:    []string{"const flag = check()", "const v = flag ? newYes() : newNo()", "return v"},
		},
		{
			name:    "TA17 replace template literal",
			lang:    edit.LangTypeScript,
			orig:    []string{"const u = user.name", "return `old ${u}!`", "// log"},
			snippet: []string{"const u = user.name", "return `new ${u}!`", "// log"},
			want:    []string{"const u = user.name", "return `new ${u}!`", "// log"},
		},
		{
			name:    "TA18 replace async/await body",
			lang:    edit.LangTypeScript,
			orig:    []string{"const url = endpoint()", "const r = await oldFetch(url)", "return r.json()"},
			snippet: []string{"const url = endpoint()", "const r = await newFetch(url)", "return r.json()"},
			want:    []string{"const url = endpoint()", "const r = await newFetch(url)", "return r.json()"},
		},
		{
			name:    "TA19 replace export const value",
			lang:    edit.LangTypeScript,
			orig:    []string{"const cfg = base()", "export const VALUE = oldEnrich(cfg)", "// end"},
			snippet: []string{"const cfg = base()", "export const VALUE = newEnrich(cfg)", "// end"},
			want:    []string{"const cfg = base()", "export const VALUE = newEnrich(cfg)", "// end"},
		},
		{
			name:    "TA20 replace enum entry value (literal-like)",
			lang:    edit.LangTypeScript,
			orig:    []string{"enum E {", "  A = oldA,", "  B = bConst,", "}"},
			snippet: []string{"enum E {", "  A = newA,", "  B = bConst,", "}"},
			want:    []string{"enum E {", "  A = newA,", "  B = bConst,", "}"},
		},

		// ── TB: TypeScript continuation marker (15) ───────────────────────────

		{
			name:    "TB01 cont marker preserves single-line gap",
			lang:    edit.LangTypeScript,
			orig:    []string{"const a = 1", "const keep = 2", "const b = 3"},
			snippet: []string{"const a = 1", cs, "const b = 3"},
			want:    []string{"const a = 1", "const keep = 2", "const b = 3"},
		},
		{
			name:    "TB02 cont marker preserves multi-line gap",
			lang:    edit.LangTypeScript,
			orig:    []string{"const a = 1", "const g1 = 1", "const g2 = 2", "const g3 = 3", "const b = 5"},
			snippet: []string{"const a = 1", cs, "const b = 5"},
			want:    []string{"const a = 1", "const g1 = 1", "const g2 = 2", "const g3 = 3", "const b = 5"},
		},
		{
			name:    "TB03 new line before cont marker",
			lang:    edit.LangTypeScript,
			orig:    []string{"const a = 1", "const g1 = 1", "const g2 = 2", "const b = 5"},
			snippet: []string{"const a = 1", "const fresh = 99", cs, "const b = 5"},
			want:    []string{"const a = 1", "const fresh = 99", "const g1 = 1", "const g2 = 2", "const b = 5"},
		},
		{
			name:    "TB04 new line after cont marker",
			lang:    edit.LangTypeScript,
			orig:    []string{"const a = 1", "const g1 = 1", "const g2 = 2", "const b = 5"},
			snippet: []string{"const a = 1", cs, "const fresh = 99", "const b = 5"},
			want:    []string{"const a = 1", "const g1 = 1", "const g2 = 2", "const fresh = 99", "const b = 5"},
		},
		{
			name:    "TB05 new lines on both sides of cont marker",
			lang:    edit.LangTypeScript,
			orig:    []string{"const a = 1", "const g = 0", "const b = 2"},
			snippet: []string{"const a = 1", "const pre = 1", cs, "const post = 2", "const b = 2"},
			want:    []string{"const a = 1", "const pre = 1", "const g = 0", "const post = 2", "const b = 2"},
		},
		{
			name:    "TB06 cont marker in pre region preserves head",
			lang:    edit.LangTypeScript,
			orig:    []string{"const h1 = 1", "const h2 = 2", "const a = 3", "const b = 4"},
			snippet: []string{cs, "const a = 3", "const b = 4"},
			want:    []string{"const h1 = 1", "const h2 = 2", "const a = 3", "const b = 4"},
		},
		{
			name:    "TB07 cont marker in post region preserves tail",
			lang:    edit.LangTypeScript,
			orig:    []string{"const a = 1", "const b = 2", "const t1 = 3", "const t2 = 4"},
			snippet: []string{"const a = 1", "const b = 2", cs},
			want:    []string{"const a = 1", "const b = 2", "const t1 = 3", "const t2 = 4"},
		},
		{
			name:    "TB08 cont marker in pre region with prepended new line",
			lang:    edit.LangTypeScript,
			orig:    []string{"const h = 0", "const a = 1", "const b = 2"},
			snippet: []string{"const fresh = 99", cs, "const a = 1", "const b = 2"},
			want:    []string{"const fresh = 99", "const h = 0", "const a = 1", "const b = 2"},
		},
		{
			name:    "TB09 cont marker in post region with appended new line",
			lang:    edit.LangTypeScript,
			orig:    []string{"const a = 1", "const b = 2", "const tail = 5"},
			snippet: []string{"const a = 1", "const b = 2", cs, "const fresh = 99"},
			want:    []string{"const a = 1", "const b = 2", "const tail = 5", "const fresh = 99"},
		},
		{
			name:    "TB10 cont marker with three anchors (left segment)",
			lang:    edit.LangTypeScript,
			orig:    []string{"const a = 1", "const g = 5", "const b = 2", "const c = 3"},
			snippet: []string{"const a = 1", cs, "const b = 2", "const fresh = 9", "const c = 3"},
			want:    []string{"const a = 1", "const g = 5", "const b = 2", "const fresh = 9", "const c = 3"},
		},
		{
			name:    "TB11 cont marker with three anchors (right segment)",
			lang:    edit.LangTypeScript,
			orig:    []string{"const a = 1", "const b = 2", "const g = 5", "const c = 3"},
			snippet: []string{"const a = 1", "const fresh = 9", "const b = 2", cs, "const c = 3"},
			want:    []string{"const a = 1", "const fresh = 9", "const b = 2", "const g = 5", "const c = 3"},
		},
		{
			name:    "TB12 cont marker preserves blank line in gap",
			lang:    edit.LangTypeScript,
			orig:    []string{"const a = 1", "const g1 = 1", "", "const g2 = 2", "const b = 5"},
			snippet: []string{"const a = 1", cs, "const b = 5"},
			want:    []string{"const a = 1", "const g1 = 1", "", "const g2 = 2", "const b = 5"},
		},
		{
			name:    "TB13 multiple new lines before cont marker",
			lang:    edit.LangTypeScript,
			orig:    []string{"const a = 1", "const g = 0", "const b = 2"},
			snippet: []string{"const a = 1", "const n1 = 1", "const n2 = 2", cs, "const b = 2"},
			want:    []string{"const a = 1", "const n1 = 1", "const n2 = 2", "const g = 0", "const b = 2"},
		},
		{
			name:    "TB14 multiple new lines after cont marker",
			lang:    edit.LangTypeScript,
			orig:    []string{"const a = 1", "const g = 0", "const b = 2"},
			snippet: []string{"const a = 1", cs, "const n1 = 1", "const n2 = 2", "const b = 2"},
			want:    []string{"const a = 1", "const g = 0", "const n1 = 1", "const n2 = 2", "const b = 2"},
		},
		{
			name:    "TB15 cont marker spans empty original gap",
			lang:    edit.LangTypeScript,
			orig:    []string{"const a = 1", "const b = 2"},
			snippet: []string{"const a = 1", cs, "const b = 2"},
			want:    []string{"const a = 1", "const b = 2"},
		},

		// ── TC: TypeScript overloaded signatures (15) ─────────────────────────

		{
			name: "TC01 overloaded sigs preserved via cont marker",
			lang: edit.LangTypeScript,
			orig: []string{
				"function fmt(x: number): string;",
				"function fmt(x: string): string;",
				"function fmt(x: any): string {",
				"  return oldShape(x)",
				"}",
			},
			snippet: []string{
				"function fmt(x: number): string;",
				cs,
				"function fmt(x: any): string {",
				"  return newShape(x)",
				"}",
			},
			want: []string{
				"function fmt(x: number): string;",
				"function fmt(x: string): string;",
				"function fmt(x: any): string {",
				"  return newShape(x)",
				"}",
			},
		},
		{
			name: "TC02 overloads anchored explicitly",
			lang: edit.LangTypeScript,
			orig: []string{
				"function add(a: number, b: number): number;",
				"function add(a: string, b: string): string;",
				"function add(a: any, b: any): any {",
				"  return oldImpl(a, b)",
				"}",
			},
			snippet: []string{
				"function add(a: number, b: number): number;",
				"function add(a: string, b: string): string;",
				"function add(a: any, b: any): any {",
				"  return newImpl(a, b)",
				"}",
			},
			want: []string{
				"function add(a: number, b: number): number;",
				"function add(a: string, b: string): string;",
				"function add(a: any, b: any): any {",
				"  return newImpl(a, b)",
				"}",
			},
		},
		{
			name: "TC03 method overloads in class",
			lang: edit.LangTypeScript,
			orig: []string{
				"  open(p: string): void;",
				"  open(p: URL): void;",
				"  open(p: any): void {",
				"    this.p = oldNorm(p)",
				"  }",
			},
			snippet: []string{
				"  open(p: string): void;",
				"  open(p: URL): void;",
				"  open(p: any): void {",
				"    this.p = newNorm(p)",
				"  }",
			},
			want: []string{
				"  open(p: string): void;",
				"  open(p: URL): void;",
				"  open(p: any): void {",
				"    this.p = newNorm(p)",
				"  }",
			},
		},
		{
			name: "TC04 three overloads + impl, replace impl",
			lang: edit.LangTypeScript,
			orig: []string{
				"function f(x: A): A;",
				"function f(x: B): B;",
				"function f(x: C): C;",
				"function f(x: any): any {",
				"  return oldRoute(x)",
				"}",
			},
			snippet: []string{
				"function f(x: A): A;",
				cs,
				"function f(x: any): any {",
				"  return newRoute(x)",
				"}",
			},
			want: []string{
				"function f(x: A): A;",
				"function f(x: B): B;",
				"function f(x: C): C;",
				"function f(x: any): any {",
				"  return newRoute(x)",
				"}",
			},
		},
		{
			name: "TC05 overloads with generic impl",
			lang: edit.LangTypeScript,
			orig: []string{
				"function pick<T>(arr: T[]): T;",
				"function pick<T>(arr: T[], idx: number): T;",
				"function pick<T>(arr: T[], idx?: number): T {",
				"  return arr[oldIdx(idx)]",
				"}",
			},
			snippet: []string{
				"function pick<T>(arr: T[]): T;",
				"function pick<T>(arr: T[], idx: number): T;",
				"function pick<T>(arr: T[], idx?: number): T {",
				"  return arr[newIdx(idx)]",
				"}",
			},
			want: []string{
				"function pick<T>(arr: T[]): T;",
				"function pick<T>(arr: T[], idx: number): T;",
				"function pick<T>(arr: T[], idx?: number): T {",
				"  return arr[newIdx(idx)]",
				"}",
			},
		},
		{
			name: "TC06 overloads union: replace one overload signature",
			lang: edit.LangTypeScript,
			orig: []string{
				"function p(x: A): R;",
				"function p(x: OldB): R;",
				"function p(x: any): R { return r(x) }",
			},
			snippet: []string{
				"function p(x: A): R;",
				"function p(x: NewB): R;",
				"function p(x: any): R { return r(x) }",
			},
			want: []string{
				"function p(x: A): R;",
				"function p(x: NewB): R;",
				"function p(x: any): R { return r(x) }",
			},
		},
		{
			name: "TC07 overloaded constructors via static factory",
			lang: edit.LangTypeScript,
			orig: []string{
				"  static of(v: number): X;",
				"  static of(v: string): X;",
				"  static of(v: any): X {",
				"    return oldBuild(v)",
				"  }",
			},
			snippet: []string{
				"  static of(v: number): X;",
				cs,
				"  static of(v: any): X {",
				"    return newBuild(v)",
				"  }",
			},
			want: []string{
				"  static of(v: number): X;",
				"  static of(v: string): X;",
				"  static of(v: any): X {",
				"    return newBuild(v)",
				"  }",
			},
		},
		{
			name: "TC08 overload preserve plus body cont marker",
			lang: edit.LangTypeScript,
			orig: []string{
				"function g(a: number): number;",
				"function g(a: string): string;",
				"function g(a: any): any {",
				"  const x = step1(a)",
				"  const y = step2(x)",
				"  return y",
				"}",
			},
			snippet: []string{
				"function g(a: number): number;",
				cs,
				"function g(a: any): any {",
				cs,
				"  return y",
				"}",
			},
			want: []string{
				"function g(a: number): number;",
				"function g(a: string): string;",
				"function g(a: any): any {",
				"  const x = step1(a)",
				"  const y = step2(x)",
				"  return y",
				"}",
			},
		},
		{
			name: "TC09 overloads with rest parameters",
			lang: edit.LangTypeScript,
			orig: []string{
				"function fmt(...xs: number[]): string;",
				"function fmt(...xs: string[]): string;",
				"function fmt(...xs: any[]): string { return oldFmt(xs) }",
			},
			snippet: []string{
				"function fmt(...xs: number[]): string;",
				"function fmt(...xs: string[]): string;",
				"function fmt(...xs: any[]): string { return newFmt(xs) }",
			},
			want: []string{
				"function fmt(...xs: number[]): string;",
				"function fmt(...xs: string[]): string;",
				"function fmt(...xs: any[]): string { return newFmt(xs) }",
			},
		},
		{
			name: "TC10 overloads with optional params",
			lang: edit.LangTypeScript,
			orig: []string{
				"function bld(a: A): R;",
				"function bld(a: A, b?: B): R;",
				"function bld(a: A, b?: B): R { return oldGo(a, b) }",
			},
			snippet: []string{
				"function bld(a: A): R;",
				"function bld(a: A, b?: B): R;",
				"function bld(a: A, b?: B): R { return newGo(a, b) }",
			},
			want: []string{
				"function bld(a: A): R;",
				"function bld(a: A, b?: B): R;",
				"function bld(a: A, b?: B): R { return newGo(a, b) }",
			},
		},
		{
			name: "TC11 overloads return-type narrowing",
			lang: edit.LangTypeScript,
			orig: []string{
				"function id<T>(x: T): T;",
				"function id(x: 1): 2;",
				"function id(x: any): any {",
				"  return oldFn(x)",
				"}",
			},
			snippet: []string{
				"function id<T>(x: T): T;",
				"function id(x: 1): 2;",
				"function id(x: any): any {",
				"  return newFn(x)",
				"}",
			},
			want: []string{
				"function id<T>(x: T): T;",
				"function id(x: 1): 2;",
				"function id(x: any): any {",
				"  return newFn(x)",
				"}",
			},
		},
		{
			name: "TC12 overloads with type predicates",
			lang: edit.LangTypeScript,
			orig: []string{
				"function isStr(v: unknown): v is string;",
				"function isStr(v: any): v is string {",
				"  return oldCheck(v)",
				"}",
			},
			snippet: []string{
				"function isStr(v: unknown): v is string;",
				"function isStr(v: any): v is string {",
				"  return newCheck(v)",
				"}",
			},
			want: []string{
				"function isStr(v: unknown): v is string;",
				"function isStr(v: any): v is string {",
				"  return newCheck(v)",
				"}",
			},
		},
		{
			name: "TC13 overloads inside namespace",
			lang: edit.LangTypeScript,
			orig: []string{
				"  export function go(x: A): R;",
				"  export function go(x: B): R;",
				"  export function go(x: any): R {",
				"    return oldDispatch(x)",
				"  }",
			},
			snippet: []string{
				"  export function go(x: A): R;",
				cs,
				"  export function go(x: any): R {",
				"    return newDispatch(x)",
				"  }",
			},
			want: []string{
				"  export function go(x: A): R;",
				"  export function go(x: B): R;",
				"  export function go(x: any): R {",
				"    return newDispatch(x)",
				"  }",
			},
		},
		{
			name: "TC14 overloads with literal-type arg",
			lang: edit.LangTypeScript,
			orig: []string{
				"function on(e: 'click'): void;",
				"function on(e: 'hover'): void;",
				"function on(e: any): void { oldHandle(e) }",
			},
			snippet: []string{
				"function on(e: 'click'): void;",
				"function on(e: 'hover'): void;",
				"function on(e: any): void { newHandle(e) }",
			},
			want: []string{
				"function on(e: 'click'): void;",
				"function on(e: 'hover'): void;",
				"function on(e: any): void { newHandle(e) }",
			},
		},
		{
			name: "TC15 overloads w/ this-typed param",
			lang: edit.LangTypeScript,
			orig: []string{
				"  fmt(this: Self, v: A): string;",
				"  fmt(this: Self, v: B): string;",
				"  fmt(this: Self, v: any): string { return oldOut(v) }",
			},
			snippet: []string{
				"  fmt(this: Self, v: A): string;",
				"  fmt(this: Self, v: B): string;",
				"  fmt(this: Self, v: any): string { return newOut(v) }",
			},
			want: []string{
				"  fmt(this: Self, v: A): string;",
				"  fmt(this: Self, v: B): string;",
				"  fmt(this: Self, v: any): string { return newOut(v) }",
			},
		},

		// ── TD: TypeScript abstract methods (15) ──────────────────────────────

		{
			name: "TD01 abstract method preserved across body splice",
			lang: edit.LangTypeScript,
			orig: []string{
				"  abstract id(): string;",
				"  fmt(): string {",
				"    return oldRender(this.id())",
				"  }",
			},
			snippet: []string{
				"  abstract id(): string;",
				"  fmt(): string {",
				"    return newRender(this.id())",
				"  }",
			},
			want: []string{
				"  abstract id(): string;",
				"  fmt(): string {",
				"    return newRender(this.id())",
				"  }",
			},
		},
		{
			name: "TD02 multiple abstract methods preserved",
			lang: edit.LangTypeScript,
			orig: []string{
				"  abstract id(): string;",
				"  abstract type(): string;",
				"  describe(): string {",
				"    return oldDesc(this.id(), this.type())",
				"  }",
			},
			snippet: []string{
				"  abstract id(): string;",
				cs,
				"  describe(): string {",
				"    return newDesc(this.id(), this.type())",
				"  }",
			},
			want: []string{
				"  abstract id(): string;",
				"  abstract type(): string;",
				"  describe(): string {",
				"    return newDesc(this.id(), this.type())",
				"  }",
			},
		},
		{
			name: "TD03 abstract class with concrete method body replaced",
			lang: edit.LangTypeScript,
			orig: []string{
				"  abstract get name(): string;",
				"  greet(): string {",
				"    return `hi ` + oldGreeter(this.name)",
				"  }",
			},
			snippet: []string{
				"  abstract get name(): string;",
				"  greet(): string {",
				"    return `hi ` + newGreeter(this.name)",
				"  }",
			},
			want: []string{
				"  abstract get name(): string;",
				"  greet(): string {",
				"    return `hi ` + newGreeter(this.name)",
				"  }",
			},
		},
		{
			name: "TD04 abstract setter with concrete body update",
			lang: edit.LangTypeScript,
			orig: []string{
				"  abstract set value(v: number);",
				"  init(): void {",
				"    this.value = oldInit()",
				"  }",
			},
			snippet: []string{
				"  abstract set value(v: number);",
				"  init(): void {",
				"    this.value = newInit()",
				"  }",
			},
			want: []string{
				"  abstract set value(v: number);",
				"  init(): void {",
				"    this.value = newInit()",
				"  }",
			},
		},
		{
			name: "TD05 abstract method swap implementation only",
			lang: edit.LangTypeScript,
			orig: []string{
				"  abstract handle(e: Event): void;",
				"  dispatch(e: Event): void {",
				"    if (e) this.handle(oldEnrich(e))",
				"  }",
			},
			snippet: []string{
				"  abstract handle(e: Event): void;",
				"  dispatch(e: Event): void {",
				"    if (e) this.handle(newEnrich(e))",
				"  }",
			},
			want: []string{
				"  abstract handle(e: Event): void;",
				"  dispatch(e: Event): void {",
				"    if (e) this.handle(newEnrich(e))",
				"  }",
			},
		},
		{
			name: "TD06 abstract with mixed protected modifiers",
			lang: edit.LangTypeScript,
			orig: []string{
				"  protected abstract loadData(): A;",
				"  init(): void {",
				"    this.data = oldShape(this.loadData())",
				"  }",
			},
			snippet: []string{
				"  protected abstract loadData(): A;",
				"  init(): void {",
				"    this.data = newShape(this.loadData())",
				"  }",
			},
			want: []string{
				"  protected abstract loadData(): A;",
				"  init(): void {",
				"    this.data = newShape(this.loadData())",
				"  }",
			},
		},
		{
			name: "TD07 abstract method with cont marker for body",
			lang: edit.LangTypeScript,
			orig: []string{
				"  abstract id(): string;",
				"  fmt(): string {",
				"    const a = step1()",
				"    const b = step2(a)",
				"    return b",
				"  }",
			},
			snippet: []string{
				"  abstract id(): string;",
				"  fmt(): string {",
				cs,
				"    return b",
				"  }",
			},
			want: []string{
				"  abstract id(): string;",
				"  fmt(): string {",
				"    const a = step1()",
				"    const b = step2(a)",
				"    return b",
				"  }",
			},
		},
		{
			name: "TD08 abstract async method then concrete sync body",
			lang: edit.LangTypeScript,
			orig: []string{
				"  abstract async load(): Promise<X>;",
				"  refresh(): void {",
				"    this.cached = oldFromPromise(this.load())",
				"  }",
			},
			snippet: []string{
				"  abstract async load(): Promise<X>;",
				"  refresh(): void {",
				"    this.cached = newFromPromise(this.load())",
				"  }",
			},
			want: []string{
				"  abstract async load(): Promise<X>;",
				"  refresh(): void {",
				"    this.cached = newFromPromise(this.load())",
				"  }",
			},
		},
		{
			name: "TD09 abstract method with optional parameter",
			lang: edit.LangTypeScript,
			orig: []string{
				"  abstract render(x: X, opts?: Opts): R;",
				"  draw(x: X): R {",
				"    return this.render(x, oldDefaults())",
				"  }",
			},
			snippet: []string{
				"  abstract render(x: X, opts?: Opts): R;",
				"  draw(x: X): R {",
				"    return this.render(x, newDefaults())",
				"  }",
			},
			want: []string{
				"  abstract render(x: X, opts?: Opts): R;",
				"  draw(x: X): R {",
				"    return this.render(x, newDefaults())",
				"  }",
			},
		},
		{
			name: "TD10 abstract member with field initializer (concrete)",
			lang: edit.LangTypeScript,
			orig: []string{
				"  abstract kind(): K;",
				"  cache = oldFactory(this.kind)",
				"  use(): K { return this.kind() }",
			},
			snippet: []string{
				"  abstract kind(): K;",
				"  cache = newFactory(this.kind)",
				"  use(): K { return this.kind() }",
			},
			want: []string{
				"  abstract kind(): K;",
				"  cache = newFactory(this.kind)",
				"  use(): K { return this.kind() }",
			},
		},
		{
			name: "TD11 abstract method with returned union type",
			lang: edit.LangTypeScript,
			orig: []string{
				"  abstract status(): 'ok' | 'err';",
				"  pp(): string {",
				"    return oldDecorate(this.status())",
				"  }",
			},
			snippet: []string{
				"  abstract status(): 'ok' | 'err';",
				"  pp(): string {",
				"    return newDecorate(this.status())",
				"  }",
			},
			want: []string{
				"  abstract status(): 'ok' | 'err';",
				"  pp(): string {",
				"    return newDecorate(this.status())",
				"  }",
			},
		},
		{
			name: "TD12 abstract within class extending Base",
			lang: edit.LangTypeScript,
			orig: []string{
				"  abstract step(input: I): O;",
				"  run(input: I): O {",
				"    return super.run(oldPrep(input))",
				"  }",
			},
			snippet: []string{
				"  abstract step(input: I): O;",
				"  run(input: I): O {",
				"    return super.run(newPrep(input))",
				"  }",
			},
			want: []string{
				"  abstract step(input: I): O;",
				"  run(input: I): O {",
				"    return super.run(newPrep(input))",
				"  }",
			},
		},
		{
			name: "TD13 abstract method preserved across cont in pre",
			lang: edit.LangTypeScript,
			orig: []string{
				"  abstract a(): A;",
				"  abstract b(): B;",
				"  init() { this.x = 1 }",
				"  c(): C {",
				"    return oldCombine(this.a(), this.b())",
				"  }",
			},
			snippet: []string{
				cs,
				"  init() { this.x = 1 }",
				"  c(): C {",
				"    return newCombine(this.a(), this.b())",
				"  }",
			},
			want: []string{
				"  abstract a(): A;",
				"  abstract b(): B;",
				"  init() { this.x = 1 }",
				"  c(): C {",
				"    return newCombine(this.a(), this.b())",
				"  }",
			},
		},
		{
			name: "TD14 abstract pair anchored, body replaced",
			lang: edit.LangTypeScript,
			orig: []string{
				"  abstract first(): A;",
				"  abstract second(): B;",
				"  pair(): [A, B] {",
				"    return [this.first(), oldNorm(this.second())]",
				"  }",
			},
			snippet: []string{
				"  abstract first(): A;",
				"  abstract second(): B;",
				"  pair(): [A, B] {",
				"    return [this.first(), newNorm(this.second())]",
				"  }",
			},
			want: []string{
				"  abstract first(): A;",
				"  abstract second(): B;",
				"  pair(): [A, B] {",
				"    return [this.first(), newNorm(this.second())]",
				"  }",
			},
		},
		{
			name: "TD15 abstract method anchor, post region change",
			lang: edit.LangTypeScript,
			orig: []string{
				"  abstract id(): string;",
				"  fmt(): string { return oldFmt() }",
				"  end(): void {}",
				"  trail = 'old'",
			},
			snippet: []string{
				"  abstract id(): string;",
				"  fmt(): string { return newFmt() }",
				"  end(): void {}",
				"  trail = 'new'",
			},
			want: []string{
				"  abstract id(): string;",
				"  fmt(): string { return newFmt() }",
				"  end(): void {}",
				"  trail = 'new'",
			},
		},

		// ── TE: TypeScript generic constraints (15) ───────────────────────────

		{
			name: "TE01 generic with extends constraint, replace body",
			lang: edit.LangTypeScript,
			orig: []string{
				"function pluck<T extends Item>(arr: T[]): T {",
				"  return oldFirst(arr)",
				"}",
			},
			snippet: []string{
				"function pluck<T extends Item>(arr: T[]): T {",
				"  return newFirst(arr)",
				"}",
			},
			want: []string{
				"function pluck<T extends Item>(arr: T[]): T {",
				"  return newFirst(arr)",
				"}",
			},
		},
		{
			name: "TE02 generic with two constraints",
			lang: edit.LangTypeScript,
			orig: []string{
				"function combine<A extends X, B extends Y>(a: A, b: B): R {",
				"  return oldMerge(a, b)",
				"}",
			},
			snippet: []string{
				"function combine<A extends X, B extends Y>(a: A, b: B): R {",
				"  return newMerge(a, b)",
				"}",
			},
			want: []string{
				"function combine<A extends X, B extends Y>(a: A, b: B): R {",
				"  return newMerge(a, b)",
				"}",
			},
		},
		{
			name: "TE03 keyof constraint",
			lang: edit.LangTypeScript,
			orig: []string{
				"function get<K extends keyof T>(obj: T, k: K): T[K] {",
				"  return oldLookup(obj, k)",
				"}",
			},
			snippet: []string{
				"function get<K extends keyof T>(obj: T, k: K): T[K] {",
				"  return newLookup(obj, k)",
				"}",
			},
			want: []string{
				"function get<K extends keyof T>(obj: T, k: K): T[K] {",
				"  return newLookup(obj, k)",
				"}",
			},
		},
		{
			name: "TE04 default type parameter",
			lang: edit.LangTypeScript,
			orig: []string{
				"function box<T extends Wrappable = string>(v: T): Box<T> {",
				"  return oldWrap(v)",
				"}",
			},
			snippet: []string{
				"function box<T extends Wrappable = string>(v: T): Box<T> {",
				"  return newWrap(v)",
				"}",
			},
			want: []string{
				"function box<T extends Wrappable = string>(v: T): Box<T> {",
				"  return newWrap(v)",
				"}",
			},
		},
		{
			name: "TE05 generic class method with constraint",
			lang: edit.LangTypeScript,
			orig: []string{
				"class Repo<T extends Entity> {",
				"  save(e: T): void {",
				"    this.cache[e.id] = oldClone(e)",
				"  }",
				"}",
			},
			snippet: []string{
				"class Repo<T extends Entity> {",
				"  save(e: T): void {",
				"    this.cache[e.id] = newClone(e)",
				"  }",
				"}",
			},
			want: []string{
				"class Repo<T extends Entity> {",
				"  save(e: T): void {",
				"    this.cache[e.id] = newClone(e)",
				"  }",
				"}",
			},
		},
		{
			name: "TE06 generic with mapped type",
			lang: edit.LangTypeScript,
			orig: []string{
				"function map<T, K extends keyof T>(t: T, k: K): T[K] | undefined {",
				"  return oldOpt(t, k)",
				"}",
			},
			snippet: []string{
				"function map<T, K extends keyof T>(t: T, k: K): T[K] | undefined {",
				"  return newOpt(t, k)",
				"}",
			},
			want: []string{
				"function map<T, K extends keyof T>(t: T, k: K): T[K] | undefined {",
				"  return newOpt(t, k)",
				"}",
			},
		},
		{
			name: "TE07 generic conditional type return",
			lang: edit.LangTypeScript,
			orig: []string{
				"function cast<T extends string | number>(v: T): T extends string ? S : N {",
				"  return oldCast(v) as any",
				"}",
			},
			snippet: []string{
				"function cast<T extends string | number>(v: T): T extends string ? S : N {",
				"  return newCast(v) as any",
				"}",
			},
			want: []string{
				"function cast<T extends string | number>(v: T): T extends string ? S : N {",
				"  return newCast(v) as any",
				"}",
			},
		},
		{
			name: "TE08 generic constraint with extends Object",
			lang: edit.LangTypeScript,
			orig: []string{
				"function copy<T extends object>(src: T): T {",
				"  return oldDeepCopy(src)",
				"}",
			},
			snippet: []string{
				"function copy<T extends object>(src: T): T {",
				"  return newDeepCopy(src)",
				"}",
			},
			want: []string{
				"function copy<T extends object>(src: T): T {",
				"  return newDeepCopy(src)",
				"}",
			},
		},
		{
			name: "TE09 generic with cont marker preserves body",
			lang: edit.LangTypeScript,
			orig: []string{
				"function fold<T, A>(arr: T[], seed: A, fn: (a: A, t: T) => A): A {",
				"  let acc = seed",
				"  for (const t of arr) acc = fn(acc, t)",
				"  return acc",
				"}",
			},
			snippet: []string{
				"function fold<T, A>(arr: T[], seed: A, fn: (a: A, t: T) => A): A {",
				cs,
				"  return acc",
				"}",
			},
			want: []string{
				"function fold<T, A>(arr: T[], seed: A, fn: (a: A, t: T) => A): A {",
				"  let acc = seed",
				"  for (const t of arr) acc = fn(acc, t)",
				"  return acc",
				"}",
			},
		},
		{
			name: "TE10 generic constraint with super",
			lang: edit.LangTypeScript,
			orig: []string{
				"function widen<T extends Narrow, U super T>(x: T): U {",
				"  return oldWiden(x)",
				"}",
			},
			snippet: []string{
				"function widen<T extends Narrow, U super T>(x: T): U {",
				"  return newWiden(x)",
				"}",
			},
			want: []string{
				"function widen<T extends Narrow, U super T>(x: T): U {",
				"  return newWiden(x)",
				"}",
			},
		},
		{
			name: "TE11 generic interface method",
			lang: edit.LangTypeScript,
			orig: []string{
				"interface Cache<K extends string, V> {",
				"  get(k: K): V {",
				"    return this.store[oldKey(k)]",
				"  }",
				"}",
			},
			snippet: []string{
				"interface Cache<K extends string, V> {",
				"  get(k: K): V {",
				"    return this.store[newKey(k)]",
				"  }",
				"}",
			},
			want: []string{
				"interface Cache<K extends string, V> {",
				"  get(k: K): V {",
				"    return this.store[newKey(k)]",
				"  }",
				"}",
			},
		},
		{
			name: "TE12 generic with infer in return",
			lang: edit.LangTypeScript,
			orig: []string{
				"type Awaited<T> = T extends Promise<infer U> ? U : T",
				"function unwrap<T>(p: Promise<T>): Awaited<T> {",
				"  return oldUnwrap(p)",
				"}",
			},
			snippet: []string{
				"type Awaited<T> = T extends Promise<infer U> ? U : T",
				"function unwrap<T>(p: Promise<T>): Awaited<T> {",
				"  return newUnwrap(p)",
				"}",
			},
			want: []string{
				"type Awaited<T> = T extends Promise<infer U> ? U : T",
				"function unwrap<T>(p: Promise<T>): Awaited<T> {",
				"  return newUnwrap(p)",
				"}",
			},
		},
		{
			name: "TE13 generic with constraint and default",
			lang: edit.LangTypeScript,
			orig: []string{
				"function newOf<T extends Cls = Defaults>(): T {",
				"  return oldFactory<T>()",
				"}",
			},
			snippet: []string{
				"function newOf<T extends Cls = Defaults>(): T {",
				"  return newFactory<T>()",
				"}",
			},
			want: []string{
				"function newOf<T extends Cls = Defaults>(): T {",
				"  return newFactory<T>()",
				"}",
			},
		},
		{
			name: "TE14 generic recursive constraint",
			lang: edit.LangTypeScript,
			orig: []string{
				"interface Tree<T> { value: T; children?: Tree<T>[] }",
				"function depth<T>(t: Tree<T>): number {",
				"  return oldDepth(t)",
				"}",
			},
			snippet: []string{
				"interface Tree<T> { value: T; children?: Tree<T>[] }",
				"function depth<T>(t: Tree<T>): number {",
				"  return newDepth(t)",
				"}",
			},
			want: []string{
				"interface Tree<T> { value: T; children?: Tree<T>[] }",
				"function depth<T>(t: Tree<T>): number {",
				"  return newDepth(t)",
				"}",
			},
		},
		{
			name: "TE15 generic abstract method with constraint",
			lang: edit.LangTypeScript,
			orig: []string{
				"  abstract pick<K extends keyof T>(k: K): T[K];",
				"  pickAll(): T {",
				"    return oldPickAll()",
				"  }",
			},
			snippet: []string{
				"  abstract pick<K extends keyof T>(k: K): T[K];",
				"  pickAll(): T {",
				"    return newPickAll()",
				"  }",
			},
			want: []string{
				"  abstract pick<K extends keyof T>(k: K): T[K];",
				"  pickAll(): T {",
				"    return newPickAll()",
				"  }",
			},
		},

		// ── TF: TypeScript decorators (10) ────────────────────────────────────

		{
			name: "TF01 component decorator preserved",
			lang: edit.LangTypeScript,
			orig: []string{
				"@Component({",
				"  selector: 'app-x'",
				"})",
				"class X {",
				"  init(): void { oldRun() }",
				"}",
			},
			snippet: []string{
				"@Component({",
				"  selector: 'app-x'",
				"})",
				"class X {",
				"  init(): void { newRun() }",
				"}",
			},
			want: []string{
				"@Component({",
				"  selector: 'app-x'",
				"})",
				"class X {",
				"  init(): void { newRun() }",
				"}",
			},
		},
		{
			name: "TF02 injectable decorator preserved",
			lang: edit.LangTypeScript,
			orig: []string{
				"@Injectable()",
				"class S {",
				"  doWork(): void { oldStep() }",
				"}",
			},
			snippet: []string{
				"@Injectable()",
				"class S {",
				"  doWork(): void { newStep() }",
				"}",
			},
			want: []string{
				"@Injectable()",
				"class S {",
				"  doWork(): void { newStep() }",
				"}",
			},
		},
		{
			name: "TF03 multiple decorators on same class",
			lang: edit.LangTypeScript,
			orig: []string{
				"@Cached()",
				"@Logged()",
				"class M {",
				"  exec(): void { oldExec() }",
				"}",
			},
			snippet: []string{
				"@Cached()",
				"@Logged()",
				"class M {",
				"  exec(): void { newExec() }",
				"}",
			},
			want: []string{
				"@Cached()",
				"@Logged()",
				"class M {",
				"  exec(): void { newExec() }",
				"}",
			},
		},
		{
			name: "TF04 method decorator preserved",
			lang: edit.LangTypeScript,
			orig: []string{
				"  @cached",
				"  compute(): R {",
				"    return oldCompute()",
				"  }",
			},
			snippet: []string{
				"  @cached",
				"  compute(): R {",
				"    return newCompute()",
				"  }",
			},
			want: []string{
				"  @cached",
				"  compute(): R {",
				"    return newCompute()",
				"  }",
			},
		},
		{
			name: "TF05 decorator with arguments",
			lang: edit.LangTypeScript,
			orig: []string{
				"  @retry(3)",
				"  call(): R {",
				"    return oldCall()",
				"  }",
			},
			snippet: []string{
				"  @retry(3)",
				"  call(): R {",
				"    return newCall()",
				"  }",
			},
			want: []string{
				"  @retry(3)",
				"  call(): R {",
				"    return newCall()",
				"  }",
			},
		},
		{
			name: "TF06 decorator anchor explicit, body replaced via cont",
			lang: edit.LangTypeScript,
			orig: []string{
				"@Injectable()",
				"constructor(private svc: Svc) {}",
				"doWork(): void {}",
			},
			snippet: []string{
				"@Injectable()",
				cs,
				"doWork(): void {}",
			},
			want: []string{
				"@Injectable()",
				"constructor(private svc: Svc) {}",
				"doWork(): void {}",
			},
		},
		{
			name: "TF07 property decorator",
			lang: edit.LangTypeScript,
			orig: []string{
				"  @Input()",
				"  value: string = oldDefault",
				"  ngOnInit(): void {}",
			},
			snippet: []string{
				"  @Input()",
				"  value: string = newDefault",
				"  ngOnInit(): void {}",
			},
			want: []string{
				"  @Input()",
				"  value: string = newDefault",
				"  ngOnInit(): void {}",
			},
		},
		{
			name: "TF08 parameter decorator",
			lang: edit.LangTypeScript,
			orig: []string{
				"  constructor(@Inject('TOK') private tok: T) {}",
				"  use(): void {",
				"    oldUse(this.tok)",
				"  }",
			},
			snippet: []string{
				"  constructor(@Inject('TOK') private tok: T) {}",
				"  use(): void {",
				"    newUse(this.tok)",
				"  }",
			},
			want: []string{
				"  constructor(@Inject('TOK') private tok: T) {}",
				"  use(): void {",
				"    newUse(this.tok)",
				"  }",
			},
		},
		{
			name: "TF09 stacked method decorators",
			lang: edit.LangTypeScript,
			orig: []string{
				"  @log",
				"  @memo",
				"  do(): R {",
				"    return oldDo()",
				"  }",
			},
			snippet: []string{
				"  @log",
				"  @memo",
				"  do(): R {",
				"    return newDo()",
				"  }",
			},
			want: []string{
				"  @log",
				"  @memo",
				"  do(): R {",
				"    return newDo()",
				"  }",
			},
		},
		{
			name: "TF10 decorator and overload combined",
			lang: edit.LangTypeScript,
			orig: []string{
				"  @cached",
				"  fmt(x: A): R;",
				"  fmt(x: B): R;",
				"  fmt(x: any): R { return oldFmt(x) }",
			},
			snippet: []string{
				"  @cached",
				cs,
				"  fmt(x: B): R;",
				"  fmt(x: any): R { return newFmt(x) }",
			},
			want: []string{
				"  @cached",
				"  fmt(x: A): R;",
				"  fmt(x: B): R;",
				"  fmt(x: any): R { return newFmt(x) }",
			},
		},

		// ── TG: TypeScript EFALLTHROUGH (10) ──────────────────────────────────

		{
			name:    "TG01 only one anchor",
			lang:    edit.LangTypeScript,
			orig:    []string{"const a = 1", "const b = 2"},
			snippet: []string{"const a = 1", "const fresh = 9"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "TG02 zero anchors",
			lang:    edit.LangTypeScript,
			orig:    []string{"const x = 1", "const y = 2"},
			snippet: []string{"const q = 0", "const r = 1"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "TG03 two cont markers in inter",
			lang:    edit.LangTypeScript,
			orig:    []string{"const a = 1", "const g = 0", "const b = 2"},
			snippet: []string{"const a = 1", cs, cs, "const b = 2"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "TG04 anchor order violated",
			lang:    edit.LangTypeScript,
			orig:    []string{"const a = 1", "const b = 2"},
			snippet: []string{"const b = 2", "const fresh = 5", "const a = 1"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "TG05 only cont marker, no anchor",
			lang:    edit.LangTypeScript,
			orig:    []string{"const a = 1", "const b = 2"},
			snippet: []string{cs, "const fresh = 1"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "TG06 one anchor with new lines on both sides",
			lang:    edit.LangTypeScript,
			orig:    []string{"const a = 1", "const b = 2", "const c = 3"},
			snippet: []string{"const n1 = 9", "const a = 1", "const n2 = 8"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "TG07 empty snippet",
			lang:    edit.LangTypeScript,
			orig:    []string{"const a = 1", "const b = 2"},
			snippet: []string{},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "TG08 two cont markers in pre",
			lang:    edit.LangTypeScript,
			orig:    []string{"const h = 1", "const a = 2", "const b = 3"},
			snippet: []string{cs, cs, "const a = 2", "const b = 3"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "TG09 two cont markers in post",
			lang:    edit.LangTypeScript,
			orig:    []string{"const a = 1", "const b = 2", "const t = 3"},
			snippet: []string{"const a = 1", "const b = 2", cs, cs},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "TG10 second anchor unreachable due to forward search",
			lang:    edit.LangTypeScript,
			orig:    []string{"const a = 1", "const b = 2", "const c = 3"},
			snippet: []string{"const c = 3", "const new_mid = 9", "const a = 1"},
			// "const c = 3"@2 found, then "const a = 1" search from 3 → not found → fallthrough
			wantErr: edit.ErrFallthrough,
		},

		// ── JA: JavaScript arrow functions (10) ───────────────────────────────

		{
			name:    "JA01 arrow body anchor splice",
			lang:    edit.LangJavaScript,
			orig:    []string{"const result = x + 1", "const interim = oldCompute()", "return result"},
			snippet: []string{"const result = x + 1", "const interim = newCompute()", "return result"},
			want:    []string{"const result = x + 1", "const interim = newCompute()", "return result"},
		},
		{
			name:    "JA02 arrow with cont marker preserves body",
			lang:    edit.LangJavaScript,
			orig:    []string{"const result = transform(x)", "const a = 1", "const b = 2", "return result"},
			snippet: []string{"const result = transform(x)", cs, "return result"},
			want:    []string{"const result = transform(x)", "const a = 1", "const b = 2", "return result"},
		},
		{
			name:    "JA03 arrow assigned to const",
			lang:    edit.LangJavaScript,
			orig:    []string{"const fn = (x) => {", "  return oldOp(x)", "}"},
			snippet: []string{"const fn = (x) => {", "  return newOp(x)", "}"},
			want:    []string{"const fn = (x) => {", "  return newOp(x)", "}"},
		},
		{
			name:    "JA04 single-line arrow inside expression",
			lang:    edit.LangJavaScript,
			orig:    []string{"const items = source()", "const out = items.map((x) => oldT(x))", "return out"},
			snippet: []string{"const items = source()", "const out = items.map((x) => newT(x))", "return out"},
			want:    []string{"const items = source()", "const out = items.map((x) => newT(x))", "return out"},
		},
		{
			name:    "JA05 arrow returning object literal",
			lang:    edit.LangJavaScript,
			orig:    []string{"const make = () => ({", "  k: oldVal,", "  ok: true", "})"},
			snippet: []string{"const make = () => ({", "  k: newVal,", "  ok: true", "})"},
			want:    []string{"const make = () => ({", "  k: newVal,", "  ok: true", "})"},
		},
		{
			name:    "JA06 nested arrow expressions",
			lang:    edit.LangJavaScript,
			orig:    []string{"const wrap = fn => (x) => {", "  return oldRun(fn, x)", "}"},
			snippet: []string{"const wrap = fn => (x) => {", "  return newRun(fn, x)", "}"},
			want:    []string{"const wrap = fn => (x) => {", "  return newRun(fn, x)", "}"},
		},
		{
			name:    "JA07 arrow with destructured params",
			lang:    edit.LangJavaScript,
			orig:    []string{"const fn = ({ a, b }) => {", "  return oldOp(a, b)", "}"},
			snippet: []string{"const fn = ({ a, b }) => {", "  return newOp(a, b)", "}"},
			want:    []string{"const fn = ({ a, b }) => {", "  return newOp(a, b)", "}"},
		},
		{
			name:    "JA08 arrow in promise chain",
			lang:    edit.LangJavaScript,
			orig:    []string{"return fetch(url)", "  .then(r => oldParse(r))", "  .catch(handle)"},
			snippet: []string{"return fetch(url)", "  .then(r => newParse(r))", "  .catch(handle)"},
			want:    []string{"return fetch(url)", "  .then(r => newParse(r))", "  .catch(handle)"},
		},
		{
			name:    "JA09 arrow exporting const handler",
			lang:    edit.LangJavaScript,
			orig:    []string{"export const handler = (req) => {", "  return oldShape(req)", "}"},
			snippet: []string{"export const handler = (req) => {", "  return newShape(req)", "}"},
			want:    []string{"export const handler = (req) => {", "  return newShape(req)", "}"},
		},
		{
			name:    "JA10 arrow async/await body replace",
			lang:    edit.LangJavaScript,
			orig:    []string{"const load = async () => {", "  const r = await oldFetch()", "  return r", "}"},
			snippet: []string{"const load = async () => {", "  const r = await newFetch()", "  return r", "}"},
			want:    []string{"const load = async () => {", "  const r = await newFetch()", "  return r", "}"},
		},

		// ── JB: JavaScript computed keys [Symbol.iterator] (10) ────────────────

		{
			name: "JB01 Symbol.iterator generator method body",
			lang: edit.LangJavaScript,
			orig: []string{
				"  *[Symbol.iterator]() {",
				"    yield oldFirst()",
				"    yield oldSecond()",
				"  }",
			},
			snippet: []string{
				"  *[Symbol.iterator]() {",
				"    yield newFirst()",
				"    yield newSecond()",
				"  }",
			},
			want: []string{
				"  *[Symbol.iterator]() {",
				"    yield newFirst()",
				"    yield newSecond()",
				"  }",
			},
		},
		{
			name: "JB02 computed key method preserved across cont",
			lang: edit.LangJavaScript,
			orig: []string{
				"  [SYM]() {",
				"    const a = step()",
				"    const b = step2(a)",
				"    return b",
				"  }",
			},
			snippet: []string{
				"  [SYM]() {",
				cs,
				"    return b",
				"  }",
			},
			want: []string{
				"  [SYM]() {",
				"    const a = step()",
				"    const b = step2(a)",
				"    return b",
				"  }",
			},
		},
		{
			name: "JB03 Symbol.asyncIterator method",
			lang: edit.LangJavaScript,
			orig: []string{
				"  async *[Symbol.asyncIterator]() {",
				"    yield await oldFetch()",
				"  }",
			},
			snippet: []string{
				"  async *[Symbol.asyncIterator]() {",
				"    yield await newFetch()",
				"  }",
			},
			want: []string{
				"  async *[Symbol.asyncIterator]() {",
				"    yield await newFetch()",
				"  }",
			},
		},
		{
			name: "JB04 Symbol.toPrimitive method",
			lang: edit.LangJavaScript,
			orig: []string{
				"  [Symbol.toPrimitive](hint) {",
				"    return oldConvert(this, hint)",
				"  }",
			},
			snippet: []string{
				"  [Symbol.toPrimitive](hint) {",
				"    return newConvert(this, hint)",
				"  }",
			},
			want: []string{
				"  [Symbol.toPrimitive](hint) {",
				"    return newConvert(this, hint)",
				"  }",
			},
		},
		{
			name: "JB05 computed key from string template",
			lang: edit.LangJavaScript,
			orig: []string{
				"  [`evt_${name}`]() {",
				"    return oldHandle(name)",
				"  }",
			},
			snippet: []string{
				"  [`evt_${name}`]() {",
				"    return newHandle(name)",
				"  }",
			},
			want: []string{
				"  [`evt_${name}`]() {",
				"    return newHandle(name)",
				"  }",
			},
		},
		{
			name: "JB06 Symbol.iterator with conditional yield",
			lang: edit.LangJavaScript,
			orig: []string{
				"  *[Symbol.iterator]() {",
				"    if (this.empty) return",
				"    for (const v of this.items) yield oldShape(v)",
				"  }",
			},
			snippet: []string{
				"  *[Symbol.iterator]() {",
				"    if (this.empty) return",
				"    for (const v of this.items) yield newShape(v)",
				"  }",
			},
			want: []string{
				"  *[Symbol.iterator]() {",
				"    if (this.empty) return",
				"    for (const v of this.items) yield newShape(v)",
				"  }",
			},
		},
		{
			name: "JB07 computed key with binary expression",
			lang: edit.LangJavaScript,
			orig: []string{
				"  [PREFIX + name]() {",
				"    return oldDispatch(name)",
				"  }",
			},
			snippet: []string{
				"  [PREFIX + name]() {",
				"    return newDispatch(name)",
				"  }",
			},
			want: []string{
				"  [PREFIX + name]() {",
				"    return newDispatch(name)",
				"  }",
			},
		},
		{
			name: "JB08 Symbol.hasInstance",
			lang: edit.LangJavaScript,
			orig: []string{
				"  static [Symbol.hasInstance](v) {",
				"    return oldCheck(v)",
				"  }",
			},
			snippet: []string{
				"  static [Symbol.hasInstance](v) {",
				"    return newCheck(v)",
				"  }",
			},
			want: []string{
				"  static [Symbol.hasInstance](v) {",
				"    return newCheck(v)",
				"  }",
			},
		},
		{
			name: "JB09 computed key in object literal",
			lang: edit.LangJavaScript,
			orig: []string{
				"const handlers = {",
				"  [eventName]() {",
				"    return oldHandle()",
				"  },",
				"  default() { return null }",
				"}",
			},
			snippet: []string{
				"const handlers = {",
				"  [eventName]() {",
				"    return newHandle()",
				"  },",
				"  default() { return null }",
				"}",
			},
			want: []string{
				"const handlers = {",
				"  [eventName]() {",
				"    return newHandle()",
				"  },",
				"  default() { return null }",
				"}",
			},
		},
		{
			name: "JB10 computed key with multiple methods",
			lang: edit.LangJavaScript,
			orig: []string{
				"  init() { return null }",
				"  [Symbol.iterator]() { return oldA() }",
				"  [Symbol.asyncIterator]() { return oldB() }",
				"  end() { return null }",
			},
			snippet: []string{
				"  init() { return null }",
				"  [Symbol.iterator]() { return newA() }",
				"  [Symbol.asyncIterator]() { return newB() }",
				"  end() { return null }",
			},
			want: []string{
				"  init() { return null }",
				"  [Symbol.iterator]() { return newA() }",
				"  [Symbol.asyncIterator]() { return newB() }",
				"  end() { return null }",
			},
		},

		// ── JC: JavaScript JSX returning conditional (10) ─────────────────────

		{
			name: "JC01 JSX conditional return: replace branch",
			lang: edit.LangJavaScript,
			orig: []string{
				"const View = ({ ok }) => {",
				"  return ok ? <Done /> : <OldEmpty />",
				"}",
			},
			snippet: []string{
				"const View = ({ ok }) => {",
				"  return ok ? <Done /> : <NewEmpty />",
				"}",
			},
			want: []string{
				"const View = ({ ok }) => {",
				"  return ok ? <Done /> : <NewEmpty />",
				"}",
			},
		},
		{
			name: "JC02 JSX with logical and",
			lang: edit.LangJavaScript,
			orig: []string{
				"function App({ user }) {",
				"  return user && <OldGreeting name={user.name} />",
				"}",
			},
			snippet: []string{
				"function App({ user }) {",
				"  return user && <NewGreeting name={user.name} />",
				"}",
			},
			want: []string{
				"function App({ user }) {",
				"  return user && <NewGreeting name={user.name} />",
				"}",
			},
		},
		{
			name: "JC03 JSX nested conditional",
			lang: edit.LangJavaScript,
			orig: []string{
				"const Item = ({ s }) => {",
				"  return s === 'a' ? <A /> : s === 'b' ? <B /> : <OldDefault />",
				"}",
			},
			snippet: []string{
				"const Item = ({ s }) => {",
				"  return s === 'a' ? <A /> : s === 'b' ? <B /> : <NewDefault />",
				"}",
			},
			want: []string{
				"const Item = ({ s }) => {",
				"  return s === 'a' ? <A /> : s === 'b' ? <B /> : <NewDefault />",
				"}",
			},
		},
		{
			name: "JC04 JSX block with cont marker",
			lang: edit.LangJavaScript,
			orig: []string{
				"const Page = () => {",
				"  if (loading) return <Spinner />",
				"  if (error) return <Err />",
				"  return <Content />",
				"}",
			},
			snippet: []string{
				"const Page = () => {",
				cs,
				"  return <Content />",
				"}",
			},
			want: []string{
				"const Page = () => {",
				"  if (loading) return <Spinner />",
				"  if (error) return <Err />",
				"  return <Content />",
				"}",
			},
		},
		{
			name: "JC05 JSX child mapping replace",
			lang: edit.LangJavaScript,
			orig: []string{
				"const List = ({ items }) => {",
				"  return <ul>{items.map(it => <Old key={it.id} />)}</ul>",
				"}",
			},
			snippet: []string{
				"const List = ({ items }) => {",
				"  return <ul>{items.map(it => <New key={it.id} />)}</ul>",
				"}",
			},
			want: []string{
				"const List = ({ items }) => {",
				"  return <ul>{items.map(it => <New key={it.id} />)}</ul>",
				"}",
			},
		},
		{
			name: "JC06 JSX fragment conditional",
			lang: edit.LangJavaScript,
			orig: []string{
				"function Header() {",
				"  return showSub ? <><H1 /><OldSub /></> : <H1 />",
				"}",
			},
			snippet: []string{
				"function Header() {",
				"  return showSub ? <><H1 /><NewSub /></> : <H1 />",
				"}",
			},
			want: []string{
				"function Header() {",
				"  return showSub ? <><H1 /><NewSub /></> : <H1 />",
				"}",
			},
		},
		{
			name: "JC07 JSX with spread props conditional",
			lang: edit.LangJavaScript,
			orig: []string{
				"const Btn = (props) => {",
				"  return enabled ? <button {...props} /> : <OldDisabled />",
				"}",
			},
			snippet: []string{
				"const Btn = (props) => {",
				"  return enabled ? <button {...props} /> : <NewDisabled />",
				"}",
			},
			want: []string{
				"const Btn = (props) => {",
				"  return enabled ? <button {...props} /> : <NewDisabled />",
				"}",
			},
		},
		{
			name: "JC08 JSX null branch replace",
			lang: edit.LangJavaScript,
			orig: []string{
				"const Banner = ({ show }) => {",
				"  return show ? <OldBanner /> : null",
				"}",
			},
			snippet: []string{
				"const Banner = ({ show }) => {",
				"  return show ? <NewBanner /> : null",
				"}",
			},
			want: []string{
				"const Banner = ({ show }) => {",
				"  return show ? <NewBanner /> : null",
				"}",
			},
		},
		{
			name: "JC09 JSX with hooks and conditional",
			lang: edit.LangJavaScript,
			orig: []string{
				"const View = () => {",
				"  const [n, setN] = useState(0)",
				"  return n > 0 ? <OldShow n={n} /> : <Empty />",
				"}",
			},
			snippet: []string{
				"const View = () => {",
				"  const [n, setN] = useState(0)",
				"  return n > 0 ? <NewShow n={n} /> : <Empty />",
				"}",
			},
			want: []string{
				"const View = () => {",
				"  const [n, setN] = useState(0)",
				"  return n > 0 ? <NewShow n={n} /> : <Empty />",
				"}",
			},
		},
		{
			name: "JC10 JSX returning conditional with computed prop",
			lang: edit.LangJavaScript,
			orig: []string{
				"const Card = ({ item }) => {",
				"  return item ? <Item key={oldKey(item)} /> : <Skel />",
				"}",
			},
			snippet: []string{
				"const Card = ({ item }) => {",
				"  return item ? <Item key={newKey(item)} /> : <Skel />",
				"}",
			},
			want: []string{
				"const Card = ({ item }) => {",
				"  return item ? <Item key={newKey(item)} /> : <Skel />",
				"}",
			},
		},

		// ── JD: JavaScript basic replace and continuation (10) ────────────────

		{
			name:    "JD01 basic two-anchor replace",
			lang:    edit.LangJavaScript,
			orig:    []string{"const x = 1", "const old = 2", "return x"},
			snippet: []string{"const x = 1", "const next = 99", "return x"},
			want:    []string{"const x = 1", "const next = 99", "return x"},
		},
		{
			name:    "JD02 cont marker preserves multi-line gap",
			lang:    edit.LangJavaScript,
			orig:    []string{"const a = 1", "const g1 = 1", "const g2 = 2", "const g3 = 3", "const b = 5"},
			snippet: []string{"const a = 1", cs, "const b = 5"},
			want:    []string{"const a = 1", "const g1 = 1", "const g2 = 2", "const g3 = 3", "const b = 5"},
		},
		{
			name:    "JD03 new line before cont marker",
			lang:    edit.LangJavaScript,
			orig:    []string{"const a = 1", "const g1 = 1", "const g2 = 2", "const b = 5"},
			snippet: []string{"const a = 1", "const fresh = 99", cs, "const b = 5"},
			want:    []string{"const a = 1", "const fresh = 99", "const g1 = 1", "const g2 = 2", "const b = 5"},
		},
		{
			name:    "JD04 new line after cont marker",
			lang:    edit.LangJavaScript,
			orig:    []string{"const a = 1", "const g1 = 1", "const g2 = 2", "const b = 5"},
			snippet: []string{"const a = 1", cs, "const fresh = 99", "const b = 5"},
			want:    []string{"const a = 1", "const g1 = 1", "const g2 = 2", "const fresh = 99", "const b = 5"},
		},
		{
			name:    "JD05 three anchors with cont marker (right)",
			lang:    edit.LangJavaScript,
			orig:    []string{"const a = 1", "const b = 2", "const g = 5", "const c = 3"},
			snippet: []string{"const a = 1", "const fresh = 9", "const b = 2", cs, "const c = 3"},
			want:    []string{"const a = 1", "const fresh = 9", "const b = 2", "const g = 5", "const c = 3"},
		},
		{
			name:    "JD06 cont marker spans empty original gap",
			lang:    edit.LangJavaScript,
			orig:    []string{"const a = 1", "const b = 2"},
			snippet: []string{"const a = 1", cs, "const b = 2"},
			want:    []string{"const a = 1", "const b = 2"},
		},
		{
			name:    "JD07 pre region: cont marker preserves head",
			lang:    edit.LangJavaScript,
			orig:    []string{"const h1 = 1", "const h2 = 2", "const a = 3", "const b = 4"},
			snippet: []string{cs, "const a = 3", "const b = 4"},
			want:    []string{"const h1 = 1", "const h2 = 2", "const a = 3", "const b = 4"},
		},
		{
			name:    "JD08 post region: cont marker preserves tail",
			lang:    edit.LangJavaScript,
			orig:    []string{"const a = 1", "const b = 2", "const t1 = 3", "const t2 = 4"},
			snippet: []string{"const a = 1", "const b = 2", cs},
			want:    []string{"const a = 1", "const b = 2", "const t1 = 3", "const t2 = 4"},
		},
		{
			name:    "JD09 destructured spread arrow body",
			lang:    edit.LangJavaScript,
			orig:    []string{"const merge = (a, b) => ({", "  ...a,", "  k: oldVal,", "})"},
			snippet: []string{"const merge = (a, b) => ({", "  ...a,", "  k: newVal,", "})"},
			want:    []string{"const merge = (a, b) => ({", "  ...a,", "  k: newVal,", "})"},
		},
		{
			name:    "JD10 multi-line tagged template",
			lang:    edit.LangJavaScript,
			orig:    []string{"const sql = tag`", "  SELECT *", "  FROM oldTable", "`"},
			snippet: []string{"const sql = tag`", "  SELECT *", "  FROM newTable", "`"},
			want:    []string{"const sql = tag`", "  SELECT *", "  FROM newTable", "`"},
		},

		// ── JE: JavaScript EFALLTHROUGH (10) ──────────────────────────────────

		{
			name:    "JE01 only one anchor",
			lang:    edit.LangJavaScript,
			orig:    []string{"const a = 1", "const b = 2"},
			snippet: []string{"const a = 1", "const fresh = 9"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "JE02 zero anchors",
			lang:    edit.LangJavaScript,
			orig:    []string{"const x = 1", "const y = 2"},
			snippet: []string{"const q = 0", "const r = 1"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "JE03 two cont markers in inter",
			lang:    edit.LangJavaScript,
			orig:    []string{"const a = 1", "const g = 0", "const b = 2"},
			snippet: []string{"const a = 1", cs, cs, "const b = 2"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "JE04 anchor order violated",
			lang:    edit.LangJavaScript,
			orig:    []string{"const a = 1", "const b = 2"},
			snippet: []string{"const b = 2", "const fresh = 5", "const a = 1"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "JE05 only cont marker, no anchor",
			lang:    edit.LangJavaScript,
			orig:    []string{"const a = 1", "const b = 2"},
			snippet: []string{cs, "const fresh = 1"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "JE06 single anchor with new lines on both sides",
			lang:    edit.LangJavaScript,
			orig:    []string{"const a = 1", "const b = 2", "const c = 3"},
			snippet: []string{"const fresh1 = 9", "const a = 1", "const fresh2 = 8"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "JE07 empty snippet",
			lang:    edit.LangJavaScript,
			orig:    []string{"const a = 1", "const b = 2"},
			snippet: []string{},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "JE08 two cont markers in pre",
			lang:    edit.LangJavaScript,
			orig:    []string{"const h = 1", "const a = 2", "const b = 3"},
			snippet: []string{cs, cs, "const a = 2", "const b = 3"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "JE09 two cont markers in post",
			lang:    edit.LangJavaScript,
			orig:    []string{"const a = 1", "const b = 2", "const t = 3"},
			snippet: []string{"const a = 1", "const b = 2", cs, cs},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "JE10 second anchor unreachable",
			lang:    edit.LangJavaScript,
			orig:    []string{"const a = 1", "const b = 2", "const c = 3"},
			snippet: []string{"const c = 3", "const new_mid = 9", "const a = 1"},
			wantErr: edit.ErrFallthrough,
		},
	}

	if len(cases) != 250 {
		t.Fatalf("corpus must contain exactly 250 cases (100 Python + 100 TS + 50 JS), got %d", len(cases))
	}

	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := edit.SplicePerLang(c.lang, c.orig, c.snippet)

			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("case %d %q: SplicePerLang() error = %v, want %v", i+1, c.name, err, c.wantErr)
				}
				if got != nil {
					t.Fatalf("case %d %q: SplicePerLang() body = %v, want nil on error", i+1, c.name, got)
				}
				return
			}

			if err != nil {
				t.Fatalf("case %d %q: SplicePerLang() unexpected error: %v", i+1, c.name, err)
			}
			if !slicesEqual(got, c.want) {
				t.Fatalf("case %d %q:\ngot:  %v\nwant: %v", i+1, c.name, got, c.want)
			}
		})
	}
}

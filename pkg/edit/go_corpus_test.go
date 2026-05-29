package edit_test

import (
	"errors"
	"testing"

	"oro/pkg/edit"
)

// TestGoCorpus is the 200-case Go edit corpus (§7.4 per-language gotchas,
// §7.8 bench/accuracy targets). Each case exercises SplicePerLang(LangGo).
//
// Groups:
//
//	A (30)  Two-anchor basic replace
//	B (25)  Continuation marker
//	C (10)  Pre-anchor region
//	D (10)  Post-anchor region
//	E (20)  Three-or-more anchors
//	F (15)  Generic functions (§7.4 gotcha)
//	G (15)  Pointer vs value receivers (§7.4 gotcha)
//	H (10)  Struct field tags (§7.4 gotcha)
//	I (15)  Edge cases
//	J (50)  EFALLTHROUGH — ineligible snippets
func TestGoCorpus(t *testing.T) {
	const m = "// ..." // Go continuation marker

	type tc struct {
		name    string
		orig    []string
		snippet []string
		want    []string
		wantErr error
	}

	cases := []tc{
		// ── A: Two-anchor basic replace ───────────────────────────────────────

		{
			name:    "A01 replace single-line gap",
			orig:    []string{"x := 1", "old_line", "return x"},
			snippet: []string{"x := 1", "new_line", "return x"},
			want:    []string{"x := 1", "new_line", "return x"},
		},
		{
			name:    "A02 replace two-line gap with one line",
			orig:    []string{"a := 1", "old1", "old2", "b := 2"},
			snippet: []string{"a := 1", "merged", "b := 2"},
			want:    []string{"a := 1", "merged", "b := 2"},
		},
		{
			name:    "A03 replace one-line gap with two lines",
			orig:    []string{"a := 1", "old", "b := 2"},
			snippet: []string{"a := 1", "new1", "new2", "b := 2"},
			want:    []string{"a := 1", "new1", "new2", "b := 2"},
		},
		{
			name:    "A04 adjacent anchors: insert new line between",
			orig:    []string{"a := 1", "b := 2"},
			snippet: []string{"a := 1", "inserted", "b := 2"},
			want:    []string{"a := 1", "inserted", "b := 2"},
		},
		{
			name:    "A05 empty inter-segment preserves original gap",
			orig:    []string{"a := 1", "keep_me", "b := 2"},
			snippet: []string{"a := 1", "b := 2"},
			want:    []string{"a := 1", "keep_me", "b := 2"},
		},
		{
			name:    "A06 replace variable declaration",
			orig:    []string{"mu.Lock()", "count := old + 1", "mu.Unlock()"},
			snippet: []string{"mu.Lock()", "count := new + 1", "mu.Unlock()"},
			want:    []string{"mu.Lock()", "count := new + 1", "mu.Unlock()"},
		},
		{
			name:    "A07 replace goroutine body",
			orig:    []string{"go func() {", "doOldWork()", "}()"},
			snippet: []string{"go func() {", "doNewWork()", "}()"},
			want:    []string{"go func() {", "doNewWork()", "}()"},
		},
		{
			name:    "A08 replace defer call",
			orig:    []string{"f, _ := os.Open(name)", "defer f.Close()", "return f"},
			snippet: []string{"f, _ := os.Open(name)", "defer func() { _ = f.Close() }()", "return f"},
			want:    []string{"f, _ := os.Open(name)", "defer func() { _ = f.Close() }()", "return f"},
		},
		{
			name:    "A09 replace map lookup result",
			orig:    []string{"v := m[key]", "result := v + old", "return result"},
			snippet: []string{"v := m[key]", "result := v * 2", "return result"},
			want:    []string{"v := m[key]", "result := v * 2", "return result"},
		},
		{
			name:    "A10 replace slice append",
			orig:    []string{"result := make([]int, 0)", "result = append(result, oldItems...)", "return result"},
			snippet: []string{"result := make([]int, 0)", "result = append(result, newItems...)", "return result"},
			want:    []string{"result := make([]int, 0)", "result = append(result, newItems...)", "return result"},
		},
		{
			name:    "A11 replace channel receive",
			orig:    []string{"select {", "case v := <-ch:", "return v"},
			snippet: []string{"select {", "case v, ok := <-ch:", "return v"},
			want:    []string{"select {", "case v, ok := <-ch:", "return v"},
		},
		{
			name:    "A12 replace context timeout creation",
			orig:    []string{"ctx, cancel := context.WithTimeout(parent, oldTimeout)", "defer cancel()", "return ctx"},
			snippet: []string{"ctx, cancel := context.WithTimeout(parent, oldTimeout)", "defer cancel()", "return ctx"},
			// anchors: first and last; middle is also anchor (defer cancel() is in origSet)
			// three anchors, all three present → no change (all anchors, empty inter-segments)
			want: []string{"ctx, cancel := context.WithTimeout(parent, oldTimeout)", "defer cancel()", "return ctx"},
		},
		{
			name:    "A13 replace log line",
			orig:    []string{"log.Printf(\"start\")", "doOldWork()", "log.Printf(\"done\")"},
			snippet: []string{"log.Printf(\"start\")", "doNewWork()", "log.Printf(\"done\")"},
			want:    []string{"log.Printf(\"start\")", "doNewWork()", "log.Printf(\"done\")"},
		},
		{
			name:    "A14 replace sync pool usage",
			orig:    []string{"buf := pool.Get().(*bytes.Buffer)", "buf.WriteString(oldData)", "pool.Put(buf)"},
			snippet: []string{"buf := pool.Get().(*bytes.Buffer)", "buf.WriteString(newData)", "pool.Put(buf)"},
			want:    []string{"buf := pool.Get().(*bytes.Buffer)", "buf.WriteString(newData)", "pool.Put(buf)"},
		},
		{
			name:    "A15 replace string conversion",
			orig:    []string{"b := getBytes()", "s := string(b)", "return s"},
			snippet: []string{"b := getBytes()", "s := strings.TrimSpace(string(b))", "return s"},
			want:    []string{"b := getBytes()", "s := strings.TrimSpace(string(b))", "return s"},
		},
		{
			name:    "A16 replace atomic load",
			orig:    []string{"atomic.AddInt64(&counter, 1)", "old := atomic.LoadInt64(&val)", "return old"},
			snippet: []string{"atomic.AddInt64(&counter, 1)", "new := atomic.LoadInt64(&val) + 1", "return old"},
			want:    []string{"atomic.AddInt64(&counter, 1)", "new := atomic.LoadInt64(&val) + 1", "return old"},
		},
		{
			name:    "A17 replace for-loop body",
			orig:    []string{"for i := 0; i < len(items); i++ {", "oldProcess(items[i])", "}"},
			snippet: []string{"for i := 0; i < len(items); i++ {", "newProcess(items[i])", "}"},
			want:    []string{"for i := 0; i < len(items); i++ {", "newProcess(items[i])", "}"},
		},
		{
			name:    "A18 replace with empty-string line clears gap",
			orig:    []string{"header := makeHeader()", "oldBody := makeBody()", "return header"},
			snippet: []string{"header := makeHeader()", "", "return header"},
			// "" is lineNew; inter-segment = [{""  lineNew}]; processSegment → [""]
			want: []string{"header := makeHeader()", "", "return header"},
		},
		{
			name:    "A19 replace middle of three-line body",
			orig:    []string{"n, err := w.Write(data)", "written += n", "return written, err"},
			snippet: []string{"n, err := w.Write(data)", "written += n + overhead", "return written, err"},
			want:    []string{"n, err := w.Write(data)", "written += n + overhead", "return written, err"},
		},
		{
			name: "A20 replace two-line gap with three lines",
			orig: []string{"setup()", "old1()", "old2()", "teardown()"},
			snippet: []string{
				"setup()",
				"new1()",
				"new2()",
				"new3()",
				"teardown()",
			},
			want: []string{"setup()", "new1()", "new2()", "new3()", "teardown()"},
		},
		{
			name:    "A21 replace type conversion",
			orig:    []string{"raw := getData()", "val := int(raw)", "return val"},
			snippet: []string{"raw := getData()", "val := int64(raw)", "return val"},
			want:    []string{"raw := getData()", "val := int64(raw)", "return val"},
		},
		{
			name:    "A22 replace err-only assignment",
			orig:    []string{"err := doFirst()", "err = doSecond()", "return err"},
			snippet: []string{"err := doFirst()", "err = doThird()", "return err"},
			want:    []string{"err := doFirst()", "err = doThird()", "return err"},
		},
		{
			name:    "A23 replace multi-line gap with single line",
			orig:    []string{"start()", "stepA()", "stepB()", "stepC()", "end()"},
			snippet: []string{"start()", "combined()", "end()"},
			want:    []string{"start()", "combined()", "end()"},
		},
		{
			name:    "A24 replace range-over-slice body",
			orig:    []string{"for _, v := range items {", "oldAccumulate(v)", "}"},
			snippet: []string{"for _, v := range items {", "newAccumulate(v)", "}"},
			want:    []string{"for _, v := range items {", "newAccumulate(v)", "}"},
		},
		{
			name:    "A25 replace range-over-map body",
			orig:    []string{"for k, v := range m {", "oldHandle(k, v)", "}"},
			snippet: []string{"for k, v := range m {", "newHandle(k, v)", "}"},
			want:    []string{"for k, v := range m {", "newHandle(k, v)", "}"},
		},
		{
			name:    "A26 replace with multiple new lines (expand)",
			orig:    []string{"acquire()", "use()", "release()"},
			snippet: []string{"acquire()", "useA()", "useB()", "useC()", "release()"},
			want:    []string{"acquire()", "useA()", "useB()", "useC()", "release()"},
		},
		{
			name:    "A27 replace single-element switch case",
			orig:    []string{"switch x {", "case 1:", "return \"one\""},
			snippet: []string{"switch x {", "case 1:", "return \"uno\""},
			want:    []string{"switch x {", "case 1:", "return \"uno\""},
		},
		{
			name:    "A28 replace send to channel",
			orig:    []string{"ch <- oldValue", "wg.Done()"},
			snippet: []string{"ch <- newValue", "wg.Done()"},
			// "ch <- oldValue" is NOT in origSet (it's the first line to replace, but wait...)
			// orig = ["ch <- oldValue", "wg.Done()"]
			// snippet = ["ch <- newValue", "wg.Done()"]
			// "ch <- newValue" is lineNew (not in origSet)
			// "wg.Done()" is lineAnchor (in origSet)
			// Only 1 anchor → EFALLTHROUGH
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "A29 replace middle assignment with both endpoints unique",
			orig:    []string{"initA()", "doOld()", "finalB()"},
			snippet: []string{"initA()", "doNew()", "finalB()"},
			want:    []string{"initA()", "doNew()", "finalB()"},
		},
		{
			name:    "A30 replace with function call including error check",
			orig:    []string{"begin()", "oldOp(x)", "end()"},
			snippet: []string{"begin()", "newOp(x)", "end()"},
			want:    []string{"begin()", "newOp(x)", "end()"},
		},

		// ── B: Continuation marker ────────────────────────────────────────────

		{
			name:    "B01 continuation preserves single-line gap",
			orig:    []string{"a := 1", "preserved", "b := 2"},
			snippet: []string{"a := 1", m, "b := 2"},
			want:    []string{"a := 1", "preserved", "b := 2"},
		},
		{
			name:    "B02 continuation preserves multi-line gap",
			orig:    []string{"a := 1", "p1", "p2", "p3", "b := 2"},
			snippet: []string{"a := 1", m, "b := 2"},
			want:    []string{"a := 1", "p1", "p2", "p3", "b := 2"},
		},
		{
			name:    "B03 new line before continuation: insert then preserve",
			orig:    []string{"a := 1", "mid1", "mid2", "b := 2"},
			snippet: []string{"a := 1", "inserted", m, "b := 2"},
			want:    []string{"a := 1", "inserted", "mid1", "mid2", "b := 2"},
		},
		{
			name:    "B04 new line after continuation: preserve then insert",
			orig:    []string{"a := 1", "mid1", "mid2", "b := 2"},
			snippet: []string{"a := 1", m, "appended", "b := 2"},
			want:    []string{"a := 1", "mid1", "mid2", "appended", "b := 2"},
		},
		{
			name:    "B05 new lines on both sides of continuation",
			orig:    []string{"a := 1", "mid1", "mid2", "b := 2"},
			snippet: []string{"a := 1", "before", m, "after", "b := 2"},
			want:    []string{"a := 1", "before", "mid1", "mid2", "after", "b := 2"},
		},
		{
			name:    "B06 continuation preserves empty inter-anchor gap",
			orig:    []string{"a := 1", "b := 2"},
			snippet: []string{"a := 1", m, "b := 2"},
			want:    []string{"a := 1", "b := 2"},
		},
		{
			name:    "B07 continuation in pre-anchor segment preserves pre lines",
			orig:    []string{"preLine", "anchor1", "mid", "anchor2"},
			snippet: []string{m, "anchor1", "newMid", "anchor2"},
			want:    []string{"preLine", "anchor1", "newMid", "anchor2"},
		},
		{
			name:    "B08 new line before continuation in pre-anchor",
			orig:    []string{"preLine", "anchor1", "mid", "anchor2"},
			snippet: []string{"newPre", m, "anchor1", "newMid", "anchor2"},
			want:    []string{"newPre", "preLine", "anchor1", "newMid", "anchor2"},
		},
		{
			name:    "B09 continuation after new line in pre-anchor",
			orig:    []string{"preLine", "anchor1", "mid", "anchor2"},
			snippet: []string{m, "newPreAfter", "anchor1", "newMid", "anchor2"},
			want:    []string{"preLine", "newPreAfter", "anchor1", "newMid", "anchor2"},
		},
		{
			name:    "B10 continuation in post-anchor segment preserves post lines",
			orig:    []string{"anchor1", "mid", "anchor2", "post1", "post2"},
			snippet: []string{"anchor1", "newMid", "anchor2", m},
			want:    []string{"anchor1", "newMid", "anchor2", "post1", "post2"},
		},
		{
			name:    "B11 new line before continuation in post-anchor",
			orig:    []string{"anchor1", "mid", "anchor2", "post1"},
			snippet: []string{"anchor1", "newMid", "anchor2", "appended", m},
			want:    []string{"anchor1", "newMid", "anchor2", "appended", "post1"},
		},
		{
			name:    "B12 continuation then new line in post-anchor",
			orig:    []string{"anchor1", "mid", "anchor2", "post1"},
			snippet: []string{"anchor1", "newMid", "anchor2", m, "suffix"},
			want:    []string{"anchor1", "newMid", "anchor2", "post1", "suffix"},
		},
		{
			name:    "B13 continuation with add-before pattern (log before existing)",
			orig:    []string{"open()", "process()", "close()"},
			snippet: []string{"open()", "log.Printf(\"before\")", m, "close()"},
			want:    []string{"open()", "log.Printf(\"before\")", "process()", "close()"},
		},
		{
			name:    "B14 continuation with add-after pattern (log after existing)",
			orig:    []string{"open()", "process()", "close()"},
			snippet: []string{"open()", m, "log.Printf(\"after\")", "close()"},
			want:    []string{"open()", "process()", "log.Printf(\"after\")", "close()"},
		},
		{
			name:    "B15 continuation preserves gap: no change to body",
			orig:    []string{"a := foo()", "b := bar()", "return a, b"},
			snippet: []string{"a := foo()", m, "return a, b"},
			want:    []string{"a := foo()", "b := bar()", "return a, b"},
		},
		{
			name:    "B16 continuation in three-anchor: first inter preserved",
			orig:    []string{"a := 1", "keep1", "b := 2", "old", "c := 3"},
			snippet: []string{"a := 1", m, "b := 2", "new", "c := 3"},
			want:    []string{"a := 1", "keep1", "b := 2", "new", "c := 3"},
		},
		{
			name:    "B17 continuation in three-anchor: second inter preserved",
			orig:    []string{"a := 1", "old", "b := 2", "keep2", "c := 3"},
			snippet: []string{"a := 1", "new", "b := 2", m, "c := 3"},
			want:    []string{"a := 1", "new", "b := 2", "keep2", "c := 3"},
		},
		{
			name:    "B18 continuation in three-anchor: both inter gaps preserved",
			orig:    []string{"a := 1", "keep1", "b := 2", "keep2", "c := 3"},
			snippet: []string{"a := 1", m, "b := 2", m, "c := 3"},
			want:    []string{"a := 1", "keep1", "b := 2", "keep2", "c := 3"},
		},
		{
			name:    "B19 add mutex lock around existing via continuation",
			orig:    []string{"start()", "doWork()", "end()"},
			snippet: []string{"start()", "mu.Lock()", m, "mu.Unlock()", "end()"},
			want:    []string{"start()", "mu.Lock()", "doWork()", "mu.Unlock()", "end()"},
		},
		{
			name:    "B20 add trace span around existing",
			orig:    []string{"begin()", "coreLogic()", "finish()"},
			snippet: []string{"begin()", "span.Start()", m, "span.End()", "finish()"},
			want:    []string{"begin()", "span.Start()", "coreLogic()", "span.End()", "finish()"},
		},
		{
			name:    "B21 continuation with empty original gap (adjacent anchors)",
			orig:    []string{"header()", "footer()"},
			snippet: []string{"header()", m, "footer()"},
			want:    []string{"header()", "footer()"},
		},
		{
			name:    "B22 insert metric counter before and preserve rest",
			orig:    []string{"init()", "doThings()", "cleanup()"},
			snippet: []string{"init()", "metrics.Inc(\"calls\")", m, "cleanup()"},
			want:    []string{"init()", "metrics.Inc(\"calls\")", "doThings()", "cleanup()"},
		},
		{
			name:    "B23 continuation preserves large block",
			orig:    []string{"start()", "line1", "line2", "line3", "line4", "line5", "end()"},
			snippet: []string{"start()", m, "end()"},
			want:    []string{"start()", "line1", "line2", "line3", "line4", "line5", "end()"},
		},
		{
			name:    "B24 insert recovery block after continuation",
			orig:    []string{"tryStart()", "doSomething()", "tryEnd()"},
			snippet: []string{"tryStart()", m, "recover()", "tryEnd()"},
			want:    []string{"tryStart()", "doSomething()", "recover()", "tryEnd()"},
		},
		{
			name:    "B25 continuation in post preserves trailing single line",
			orig:    []string{"anchorA", "midX", "anchorB", "trailer"},
			snippet: []string{"anchorA", "midNew", "anchorB", m},
			want:    []string{"anchorA", "midNew", "anchorB", "trailer"},
		},

		// ── C: Pre-anchor region ──────────────────────────────────────────────

		{
			name:    "C01 pre-anchor orig preserved when snippet has no pre lines",
			orig:    []string{"pre1", "anchorA", "mid", "anchorB"},
			snippet: []string{"anchorA", "newMid", "anchorB"},
			want:    []string{"pre1", "anchorA", "newMid", "anchorB"},
		},
		{
			name:    "C02 pre-anchor orig replaced by new pre lines in snippet",
			orig:    []string{"pre1", "anchorA", "mid", "anchorB"},
			snippet: []string{"newPre", "anchorA", "newMid", "anchorB"},
			want:    []string{"newPre", "anchorA", "newMid", "anchorB"},
		},
		{
			name:    "C03 pre-anchor continuation preserves original pre lines",
			orig:    []string{"pre1", "pre2", "anchorA", "mid", "anchorB"},
			snippet: []string{m, "anchorA", "newMid", "anchorB"},
			want:    []string{"pre1", "pre2", "anchorA", "newMid", "anchorB"},
		},
		{
			name:    "C04 new pre then continuation: insert before and preserve",
			orig:    []string{"pre1", "anchorA", "mid", "anchorB"},
			snippet: []string{"newFirst", m, "anchorA", "newMid", "anchorB"},
			want:    []string{"newFirst", "pre1", "anchorA", "newMid", "anchorB"},
		},
		{
			name:    "C05 continuation then new pre: preserve then insert",
			orig:    []string{"pre1", "anchorA", "mid", "anchorB"},
			snippet: []string{m, "newLast", "anchorA", "newMid", "anchorB"},
			want:    []string{"pre1", "newLast", "anchorA", "newMid", "anchorB"},
		},
		{
			name:    "C06 multi-line new pre replaces single pre orig line",
			orig:    []string{"pre1", "anchorA", "mid", "anchorB"},
			snippet: []string{"newA", "newB", "newC", "anchorA", "newMid", "anchorB"},
			want:    []string{"newA", "newB", "newC", "anchorA", "newMid", "anchorB"},
		},
		{
			name:    "C07 no pre orig, snippet adds pre lines",
			orig:    []string{"anchorA", "mid", "anchorB"},
			snippet: []string{"addedPre", "anchorA", "newMid", "anchorB"},
			want:    []string{"addedPre", "anchorA", "newMid", "anchorB"},
		},
		{
			name:    "C08 no pre orig, snippet has no pre lines: no change to pre",
			orig:    []string{"anchorA", "mid", "anchorB"},
			snippet: []string{"anchorA", "newMid", "anchorB"},
			want:    []string{"anchorA", "newMid", "anchorB"},
		},
		{
			name:    "C09 multi-line pre orig preserved when snippet starts at first anchor",
			orig:    []string{"pre1", "pre2", "pre3", "anchorA", "mid", "anchorB"},
			snippet: []string{"anchorA", "newMid", "anchorB"},
			want:    []string{"pre1", "pre2", "pre3", "anchorA", "newMid", "anchorB"},
		},
		{
			name:    "C10 empty-string new pre line replaces pre orig",
			orig:    []string{"pre1", "anchorA", "mid", "anchorB"},
			snippet: []string{"", "anchorA", "newMid", "anchorB"},
			// "" is lineNew; pre=[{"", lineNew}]; processSegment → [""]
			want: []string{"", "anchorA", "newMid", "anchorB"},
		},

		// ── D: Post-anchor region ─────────────────────────────────────────────

		{
			name:    "D01 post-anchor orig preserved when snippet ends at last anchor",
			orig:    []string{"anchorA", "mid", "anchorB", "post1"},
			snippet: []string{"anchorA", "newMid", "anchorB"},
			want:    []string{"anchorA", "newMid", "anchorB", "post1"},
		},
		{
			name:    "D02 post-anchor orig replaced by new post lines in snippet",
			orig:    []string{"anchorA", "mid", "anchorB", "post1"},
			snippet: []string{"anchorA", "newMid", "anchorB", "newPost"},
			want:    []string{"anchorA", "newMid", "anchorB", "newPost"},
		},
		{
			name:    "D03 post-anchor continuation preserves original post lines",
			orig:    []string{"anchorA", "mid", "anchorB", "post1", "post2"},
			snippet: []string{"anchorA", "newMid", "anchorB", m},
			want:    []string{"anchorA", "newMid", "anchorB", "post1", "post2"},
		},
		{
			name:    "D04 new post then continuation: insert post before preserve",
			orig:    []string{"anchorA", "mid", "anchorB", "post1"},
			snippet: []string{"anchorA", "newMid", "anchorB", "newFirst", m},
			want:    []string{"anchorA", "newMid", "anchorB", "newFirst", "post1"},
		},
		{
			name:    "D05 continuation then new post: preserve then insert",
			orig:    []string{"anchorA", "mid", "anchorB", "post1"},
			snippet: []string{"anchorA", "newMid", "anchorB", m, "newLast"},
			want:    []string{"anchorA", "newMid", "anchorB", "post1", "newLast"},
		},
		{
			name:    "D06 multi-line new post replaces single post orig line",
			orig:    []string{"anchorA", "mid", "anchorB", "post1"},
			snippet: []string{"anchorA", "newMid", "anchorB", "newA", "newB", "newC"},
			want:    []string{"anchorA", "newMid", "anchorB", "newA", "newB", "newC"},
		},
		{
			name:    "D07 no post orig, snippet adds post lines",
			orig:    []string{"anchorA", "mid", "anchorB"},
			snippet: []string{"anchorA", "newMid", "anchorB", "addedPost"},
			want:    []string{"anchorA", "newMid", "anchorB", "addedPost"},
		},
		{
			name:    "D08 multi-line post orig preserved when snippet ends at last anchor",
			orig:    []string{"anchorA", "mid", "anchorB", "post1", "post2", "post3"},
			snippet: []string{"anchorA", "newMid", "anchorB"},
			want:    []string{"anchorA", "newMid", "anchorB", "post1", "post2", "post3"},
		},
		{
			name:    "D09 empty-string new post line replaces post orig",
			orig:    []string{"anchorA", "mid", "anchorB", "post1"},
			snippet: []string{"anchorA", "newMid", "anchorB", ""},
			want:    []string{"anchorA", "newMid", "anchorB", ""},
		},
		{
			name:    "D10 post orig replaced by single new line",
			orig:    []string{"anchorA", "mid", "anchorB", "oldPost1", "oldPost2"},
			snippet: []string{"anchorA", "newMid", "anchorB", "singleNewPost"},
			want:    []string{"anchorA", "newMid", "anchorB", "singleNewPost"},
		},

		// ── E: Three-or-more anchors ──────────────────────────────────────────

		{
			name:    "E01 three anchors: replace first gap, preserve second",
			orig:    []string{"a", "gap1", "b", "gap2", "c"},
			snippet: []string{"a", "new1", "b", m, "c"},
			want:    []string{"a", "new1", "b", "gap2", "c"},
		},
		{
			name:    "E02 three anchors: preserve first gap, replace second",
			orig:    []string{"a", "gap1", "b", "gap2", "c"},
			snippet: []string{"a", m, "b", "new2", "c"},
			want:    []string{"a", "gap1", "b", "new2", "c"},
		},
		{
			name:    "E03 three anchors: replace both gaps",
			orig:    []string{"a", "gap1", "b", "gap2", "c"},
			snippet: []string{"a", "new1", "b", "new2", "c"},
			want:    []string{"a", "new1", "b", "new2", "c"},
		},
		{
			name:    "E04 three anchors: preserve both via continuation",
			orig:    []string{"a", "gap1", "b", "gap2", "c"},
			snippet: []string{"a", m, "b", m, "c"},
			want:    []string{"a", "gap1", "b", "gap2", "c"},
		},
		{
			name:    "E05 three anchors: adjacent first two, gap at second",
			orig:    []string{"a", "b", "gap", "c"},
			snippet: []string{"a", "b", "newGap", "c"},
			// "a"@0, "b"@1, "newGap" lineNew, "c"@3
			// inter[0]=[] b/w @0,@1 → preserve []; inter[1]=[newGap] b/w @1,@3 → ["newGap"]
			want: []string{"a", "b", "newGap", "c"},
		},
		{
			name:    "E06 three anchors: gap at first, adjacent last two",
			orig:    []string{"a", "gap", "b", "c"},
			snippet: []string{"a", "newGap", "b", "c"},
			// "a" anchor@0, "newGap" lineNew, "b" anchor@2, "c" anchor@3
			// inter[0]=["newGap"] (b/w a@0 and b@2): orig[1:2]=["gap"] → ["newGap"]
			// inter[1]=[] (b/w b@2 and c@3): orig[3:3]=[] → [] → preserve []
			// result: ["a", "newGap", "b", "c"]
			want: []string{"a", "newGap", "b", "c"},
		},
		{
			name:    "E07 four anchors: replace second gap only",
			orig:    []string{"a", "g1", "b", "g2", "c", "g3", "d"},
			snippet: []string{"a", m, "b", "new2", "c", m, "d"},
			want:    []string{"a", "g1", "b", "new2", "c", "g3", "d"},
		},
		{
			name:    "E08 four anchors: replace all gaps",
			orig:    []string{"a", "g1", "b", "g2", "c", "g3", "d"},
			snippet: []string{"a", "n1", "b", "n2", "c", "n3", "d"},
			want:    []string{"a", "n1", "b", "n2", "c", "n3", "d"},
		},
		{
			name:    "E09 five anchors: preserve all gaps via continuation",
			orig:    []string{"a", "g1", "b", "g2", "c", "g3", "d", "g4", "e"},
			snippet: []string{"a", m, "b", m, "c", m, "d", m, "e"},
			want:    []string{"a", "g1", "b", "g2", "c", "g3", "d", "g4", "e"},
		},
		{
			name:    "E10 three anchors with pre region: pre preserved, both gaps changed",
			orig:    []string{"pre", "a", "g1", "b", "g2", "c"},
			snippet: []string{"a", "new1", "b", "new2", "c"},
			want:    []string{"pre", "a", "new1", "b", "new2", "c"},
		},
		{
			name:    "E11 three anchors with post region: post preserved, both gaps changed",
			orig:    []string{"a", "g1", "b", "g2", "c", "post"},
			snippet: []string{"a", "new1", "b", "new2", "c"},
			want:    []string{"a", "new1", "b", "new2", "c", "post"},
		},
		{
			name:    "E12 three anchors with pre and post: both sides preserved",
			orig:    []string{"pre", "a", "g1", "b", "g2", "c", "post"},
			snippet: []string{"a", "new1", "b", "new2", "c"},
			want:    []string{"pre", "a", "new1", "b", "new2", "c", "post"},
		},
		{
			name:    "E13 four anchors with new lines inserted between all",
			orig:    []string{"a", "b", "c", "d"},
			snippet: []string{"a", "ins1", "b", "ins2", "c", "ins3", "d"},
			// "ins1","ins2","ins3" are lineNew; b/w a@0 and b@1: inter[0]=[ins1] → orig[1:1]=[] → ["ins1"]
			// b/w b@1 and c@2: inter[1]=[ins2] → orig[2:2]=[] → ["ins2"]
			// b/w c@2 and d@3: inter[2]=[ins3] → orig[3:3]=[] → ["ins3"]
			// result: ["a","ins1","b","ins2","c","ins3","d"]
			want: []string{"a", "ins1", "b", "ins2", "c", "ins3", "d"},
		},
		{
			name: "E14 six anchors: alternating replace and preserve",
			orig: []string{"a", "g1", "b", "g2", "c", "g3", "d", "g4", "e", "g5", "f"},
			snippet: []string{
				"a", "n1",
				"b", m,
				"c", "n3",
				"d", m,
				"e", "n5",
				"f",
			},
			want: []string{"a", "n1", "b", "g2", "c", "n3", "d", "g4", "e", "n5", "f"},
		},
		{
			name:    "E15 three adjacent anchors: no gaps, snippet preserves",
			orig:    []string{"a", "b", "c"},
			snippet: []string{"a", "b", "c"},
			// all are anchors; no inter-anchor lines in snippet either; no change
			want: []string{"a", "b", "c"},
		},
		{
			name:    "E16 three anchors, multi-line new content in each gap",
			orig:    []string{"a", "og1", "b", "og2", "c"},
			snippet: []string{"a", "n1a", "n1b", "b", "n2a", "n2b", "c"},
			want:    []string{"a", "n1a", "n1b", "b", "n2a", "n2b", "c"},
		},
		{
			name:    "E17 three anchors: first gap gets continuation, post gets new lines",
			orig:    []string{"a", "g1", "b", "g2", "c", "old_post"},
			snippet: []string{"a", m, "b", "newG2", "c", "new_post"},
			want:    []string{"a", "g1", "b", "newG2", "c", "new_post"},
		},
		{
			name:    "E18 three anchors: pre gets continuation, gaps get new lines",
			orig:    []string{"oldPre", "a", "g1", "b", "g2", "c"},
			snippet: []string{m, "a", "new1", "b", "new2", "c"},
			want:    []string{"oldPre", "a", "new1", "b", "new2", "c"},
		},
		{
			name:    "E19 three anchors, all empty original inter-gaps, insert between each",
			orig:    []string{"a", "b", "c"},
			snippet: []string{"a", "ins1", "b", "ins2", "c"},
			// inter[0]=["ins1"] b/w a@0 and b@1, orig[1:1]=[] → ["ins1"]
			// inter[1]=["ins2"] b/w b@1 and c@2, orig[2:2]=[] → ["ins2"]
			want: []string{"a", "ins1", "b", "ins2", "c"},
		},
		{
			name:    "E20 three anchors with multiple new lines between each anchor pair",
			orig:    []string{"a", "b", "c"},
			snippet: []string{"a", "n1", "n2", "b", "n3", "n4", "c"},
			want:    []string{"a", "n1", "n2", "b", "n3", "n4", "c"},
		},

		// ── F: Go generic functions (§7.4 gotcha) ─────────────────────────────

		{
			name:    "F01 generic func: replace body between type-param usage lines",
			orig:    []string{"var zero T", "return doOld[T](zero)", "return zero"},
			snippet: []string{"var zero T", "return doNew[T](zero)", "return zero"},
			want:    []string{"var zero T", "return doNew[T](zero)", "return zero"},
		},
		{
			name:    "F02 generic func: continuation preserves constraint check",
			orig:    []string{"if v == zero {", "return defaultT", "}", "return processT(v)"},
			snippet: []string{"if v == zero {", m, "}", "return processT(v)"},
			want:    []string{"if v == zero {", "return defaultT", "}", "return processT(v)"},
		},
		{
			name:    "F03 generic func with K,V: replace map-set body",
			orig:    []string{"m := make(map[K]V)", "m[key] = oldVal", "return m"},
			snippet: []string{"m := make(map[K]V)", "m[key] = newVal", "return m"},
			want:    []string{"m := make(map[K]V)", "m[key] = newVal", "return m"},
		},
		{
			name:    "F04 generic func: add nil guard before existing logic",
			orig:    []string{"validate[T](v)", "result := process[T](v)", "return result"},
			snippet: []string{"validate[T](v)", "if v == nil {", "return zero", "}", m, "return result"},
			// anchors: "validate[T](v)"@0, "return result"@2
			// inter[0] = [{lineNew:"if v == nil {"}, {lineNew:"return zero"}, {lineNew:"}"}, {lineCont}, ]
			// Wait: "return result" is in origSet, so it's an anchor in snippet.
			// Snippet lines: "validate[T](v)" anchor, "if v == nil {" new, "return zero" new, "}" new, m cont, "return result" anchor
			// pre=[], anchorTexts=["validate[T](v)","return result"], inter[0]=[new,new,new,cont]
			// processSegment([new,new,new,cont], orig[1:2]=["result := process[T](v)"])
			// contIdx=3; linesBeforeMarker=[if,return zero,}] + orig + linesAfterMarker=[]
			// → ["if v == nil {", "return zero", "}", "result := process[T](v)"]
			// result: ["validate[T](v)", "if v == nil {", "return zero", "}", "result := process[T](v)", "return result"]
			want: []string{
				"validate[T](v)",
				"if v == nil {",
				"return zero",
				"}",
				"result := process[T](v)",
				"return result",
			},
		},
		{
			name: "F05 generic func: replace slice filter implementation",
			orig: []string{
				"out := make([]T, 0, len(in))",
				"for _, v := range in {",
				"if oldPred(v) {",
				"out = append(out, v)",
				"}",
				"}",
				"return out",
			},
			snippet: []string{
				"out := make([]T, 0, len(in))",
				"for _, v := range in {",
				"if newPred(v) {",
				"out = append(out, v)",
				"}",
				"}",
				"return out",
			},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "F06 generic func: continuation preserves existing loop",
			orig:    []string{"results := []T{}", "for _, item := range items {", "results = append(results, transform(item))", "}", "return results"},
			snippet: []string{"results := []T{}", m, "return results"},
			want:    []string{"results := []T{}", "for _, item := range items {", "results = append(results, transform(item))", "}", "return results"},
		},
		{
			name:    "F07 generic comparable func: replace equality check",
			orig:    []string{"if a == b {", "return oldResult", "}", "return a"},
			snippet: []string{"if a == b {", "return newResult", "}", "return a"},
			// "if a == b {" anchor@0, "return oldResult" not in snippet (lineNew in orig only)
			// snippet: "if a == b {" anchor, "return newResult" lineNew, "}" anchor, "return a" anchor
			// 3 anchors: 0(if a==b), 2(}), 3(return a)
			// inter[0]=[newResult] b/w @0 and @2: orig[1:2]=["return oldResult"] → ["return newResult"]
			// inter[1]=[] b/w @2 and @3: orig[3:3]=[] → []
			// result: ["if a == b {", "return newResult", "}", "return a"]
			want: []string{"if a == b {", "return newResult", "}", "return a"},
		},
		{
			name:    "F08 generic func with any constraint: replace type-switch",
			orig:    []string{"switch v := any(val).(type) {", "case int:", "return handleInt(v)", "}"},
			snippet: []string{"switch v := any(val).(type) {", "case int:", "return handleIntFast(v)", "}"},
			// "switch..."@0, "case int:"@1 (anchor), "return handleInt..."@2 NOT in snippet → lineNew in snippet
			// snippet: "switch..." anchor, "case int:" anchor, "return handleIntFast(v)" lineNew, "}" anchor
			// 3 anchors: 0,1,3; inter[0]=[] b/w 0,1 → []; inter[1]=[handleIntFast] b/w 1,3 → orig[2:3]=["return handleInt(v)"] → ["return handleIntFast(v)"]
			// result: ["switch...", "case int:", "return handleIntFast(v)", "}"]
			want: []string{"switch v := any(val).(type) {", "case int:", "return handleIntFast(v)", "}"},
		},
		{
			name:    "F09 generic reduce: replace accumulation",
			orig:    []string{"acc := initial", "for _, v := range items {", "acc = oldCombine(acc, v)", "}", "return acc"},
			snippet: []string{"acc := initial", "for _, v := range items {", "acc = newCombine(acc, v)", "}", "return acc"},
			// "acc := initial"@0 anchor, "for ..."@1 anchor, "acc = oldCombine"@2 NOT in snippet
			// snippet: acc@0, for@1, "acc = newCombine"(new), "}"@3, "return acc"@4
			// 4 anchors: 0,1,3,4; inter[1]=["newCombine"] b/w @1,@3: orig[2:3]=["acc = oldCombine..."] → ["acc = newCombine..."]
			want: []string{"acc := initial", "for _, v := range items {", "acc = newCombine(acc, v)", "}", "return acc"},
		},
		{
			name:    "F10 generic func: preserve body via continuation, add metric",
			orig:    []string{"start[T]()", "doWork[T](v)", "finish[T]()"},
			snippet: []string{"start[T]()", "metrics.Record(\"generic\")", m, "finish[T]()"},
			// "start[T]()"@0 anchor, "metrics.Record..." lineNew, cont, "finish[T]()"@2 anchor
			// 2 anchors: 0,2; inter[0]=[new,cont]; processSegment([new,cont], orig[1:2]=["doWork[T](v)"])
			// contIdx=1; before=[metrics.Record...] + origRegion + after=[] → ["metrics.Record...", "doWork[T](v)"]
			want: []string{"start[T]()", "metrics.Record(\"generic\")", "doWork[T](v)", "finish[T]()"},
		},
		{
			name:    "F11 generic func: two type params, replace body",
			orig:    []string{"pairs := make([]Pair[K, V], 0)", "for k, v := range m {", "pairs = append(pairs, Pair[K, V]{k, v})", "}", "return pairs"},
			snippet: []string{"pairs := make([]Pair[K, V], 0)", "for k, v := range m {", "pairs = append(pairs, NewPair[K, V](k, v))", "}", "return pairs"},
			// anchors: 0,1,3,4; inter[1] replaces line@2
			want: []string{"pairs := make([]Pair[K, V], 0)", "for k, v := range m {", "pairs = append(pairs, NewPair[K, V](k, v))", "}", "return pairs"},
		},
		{
			name:    "F12 generic func: replace error return in constraint path",
			orig:    []string{"if err != nil {", "return zero, err", "}", "return result, nil"},
			snippet: []string{"if err != nil {", "return zero, fmt.Errorf(\"op: %w\", err)", "}", "return result, nil"},
			// "if err != nil {"@0, "return zero, err"@1 not in snippet (lineNew), "}"@2, "return result, nil"@3
			// snippet: anchor@0, new, anchor@2, anchor@3
			// inter[0]=[{fmt.Errorf...}] b/w @0,@2 → orig[1:2]=["return zero, err"] → new line
			// inter[1]=[] b/w @2,@3 → []
			want: []string{"if err != nil {", "return zero, fmt.Errorf(\"op: %w\", err)", "}", "return result, nil"},
		},
		{
			name:    "F13 generic func: continuation preserves original, appends sort",
			orig:    []string{"items := collect[T](src)", "filter[T](items)", "return items"},
			snippet: []string{"items := collect[T](src)", m, "sort.Slice(items, less)", "return items"},
			// anchors: @0, @2; inter[0]=[cont, {sort lineNew}]
			// contIdx=0; before=[] + origRegion=["filter[T](items)"] + after=["sort.Slice..."]
			// → ["filter[T](items)", "sort.Slice(items, less)"]
			want: []string{"items := collect[T](src)", "filter[T](items)", "sort.Slice(items, less)", "return items"},
		},
		{
			name:    "F14 generic queue: replace enqueue logic",
			orig:    []string{"q.mu.Lock()", "q.items = append(q.items, oldItem)", "q.mu.Unlock()"},
			snippet: []string{"q.mu.Lock()", "q.items = append(q.items, newItem)", "q.mu.Unlock()"},
			want:    []string{"q.mu.Lock()", "q.items = append(q.items, newItem)", "q.mu.Unlock()"},
		},
		{
			name:    "F15 generic func: replace zero-value initialization",
			orig:    []string{"var result T", "result = oldInit[T]()", "return result"},
			snippet: []string{"var result T", "result = newInit[T]()", "return result"},
			want:    []string{"var result T", "result = newInit[T]()", "return result"},
		},

		// ── G: Pointer vs value receivers (§7.4 gotcha) ──────────────────────

		{
			name:    "G01 pointer receiver: replace field update",
			orig:    []string{"s.mu.Lock()", "s.count = oldValue", "s.mu.Unlock()"},
			snippet: []string{"s.mu.Lock()", "s.count = newValue", "s.mu.Unlock()"},
			want:    []string{"s.mu.Lock()", "s.count = newValue", "s.mu.Unlock()"},
		},
		{
			name:    "G02 value receiver: simple field read and return",
			orig:    []string{"v := s.field", "result := v + oldCalc()", "return result"},
			snippet: []string{"v := s.field", "result := v + newCalc()", "return result"},
			want:    []string{"v := s.field", "result := v + newCalc()", "return result"},
		},
		{
			name:    "G03 pointer receiver: continuation preserves state",
			orig:    []string{"s.mu.Lock()", "s.doOldSetup()", "s.active = true", "s.mu.Unlock()"},
			snippet: []string{"s.mu.Lock()", m, "s.mu.Unlock()"},
			want:    []string{"s.mu.Lock()", "s.doOldSetup()", "s.active = true", "s.mu.Unlock()"},
		},
		{
			name:    "G04 value receiver: replace computation",
			orig:    []string{"x := s.X", "y := s.Y", "return math.Sqrt(x*x + y*y)"},
			snippet: []string{"x := s.X", "y := s.Y", "return math.Hypot(x, y)"},
			// "x := s.X"@0, "y := s.Y"@1, "return math.Sqrt..."@2 not in snippet
			// snippet: anchor@0, anchor@1, lineNew
			// Only 2 anchors; inter[0]=[] b/w 0,1 → preserve [] ; inter[0]... wait 2 anchors means 1 inter
			// inter[0]=[] b/w @0,@1; processSegment([], orig[1:1]=[]) → []
			// post=[{return math.Hypot...}]; processSegment([...Hypot...], orig[2:]=["return math.Sqrt..."]) → ["return math.Hypot(x, y)"]
			// result: ["x := s.X", "y := s.Y", "return math.Hypot(x, y)"]
			want: []string{"x := s.X", "y := s.Y", "return math.Hypot(x, y)"},
		},
		{
			name:    "G05 pointer receiver: add nil check at top",
			orig:    []string{"s.doWork()", "return s.result"},
			snippet: []string{"if s == nil {", "return nil", "}", "s.doWork()", "return s.result"},
			// "if s == nil {", "return nil", "}" are lineNew (not in origSet)
			// "s.doWork()"@0 anchor, "return s.result"@1 anchor
			// pre=[{if s==nil,new},{return nil,new},{},new}, but wait "}" is NOT in origSet → lineNew too
			// pre=[{lineNew:"if s == nil {"}, {lineNew:"return nil"}, {lineNew:"}"}]
			// processSegment(pre, orig[:0]=[]) → ["if s == nil {", "return nil", "}"]
			// inter[0]=[] b/w @0,@1: processSegment([], orig[1:1]=[]) → []
			// result: ["if s == nil {", "return nil", "}", "s.doWork()", "return s.result"]
			want: []string{"if s == nil {", "return nil", "}", "s.doWork()", "return s.result"},
		},
		{
			name:    "G06 value receiver string method: replace format",
			orig:    []string{"name := s.Name", "ret := fmt.Sprintf(\"old(%s)\", name)", "return ret"},
			snippet: []string{"name := s.Name", "ret := fmt.Sprintf(\"new(%s,%d)\", name, s.ID)", "return ret"},
			want:    []string{"name := s.Name", "ret := fmt.Sprintf(\"new(%s,%d)\", name, s.ID)", "return ret"},
		},
		{
			name:    "G07 pointer receiver: replace error validation",
			orig:    []string{"if s.count < 0 {", "return ErrOldBad", "}", "return nil"},
			snippet: []string{"if s.count < 0 {", "return ErrNewBad", "}", "return nil"},
			// "if s.count < 0 {"@0, "return ErrOldBad"@1 not in snippet, "}"@2, "return nil"@3
			// snippet: anchor@0, new, anchor@2, anchor@3
			// inter[0]=[ErrNewBad] b/w @0,@2 → orig[1:2]=["return ErrOldBad"] → ["return ErrNewBad"]
			// inter[1]=[] b/w @2,@3 → []
			want: []string{"if s.count < 0 {", "return ErrNewBad", "}", "return nil"},
		},
		{
			name:    "G08 pointer receiver: add logging around operation",
			orig:    []string{"s.mu.Lock()", "s.process()", "s.mu.Unlock()"},
			snippet: []string{"s.mu.Lock()", "log.Printf(\"processing\")", m, "s.mu.Unlock()"},
			want:    []string{"s.mu.Lock()", "log.Printf(\"processing\")", "s.process()", "s.mu.Unlock()"},
		},
		{
			name:    "G09 value receiver: replace multi-step calculation",
			orig:    []string{"a := s.A", "b := s.B", "c := a + b", "return c"},
			snippet: []string{"a := s.A", "b := s.B", "c := a * b + s.Offset", "return c"},
			// anchors: @0,@1,@3; inter[1]=[{c := a*b+...}] b/w @1,@3 → orig[2:3]=["c := a + b"] → new
			want: []string{"a := s.A", "b := s.B", "c := a * b + s.Offset", "return c"},
		},
		{
			name:    "G10 pointer receiver: replace teardown sequence",
			orig:    []string{"s.cancel()", "s.oldFlush()", "s.wg.Wait()"},
			snippet: []string{"s.cancel()", "s.newFlush()", "s.wg.Wait()"},
			want:    []string{"s.cancel()", "s.newFlush()", "s.wg.Wait()"},
		},
		{
			name:    "G11 pointer receiver: insert metric before existing work",
			orig:    []string{"s.prepare()", "s.execute()", "s.finalize()"},
			snippet: []string{"s.prepare()", "s.metrics.Inc(\"calls\")", m, "s.finalize()"},
			want:    []string{"s.prepare()", "s.metrics.Inc(\"calls\")", "s.execute()", "s.finalize()"},
		},
		{
			name:    "G12 value receiver: replace multi-field return expression",
			orig:    []string{"x := s.X", "sum := x + s.Y + s.Z", "return sum"},
			snippet: []string{"x := s.X", "sum := x + s.Y + s.Z + s.W", "return sum"},
			want:    []string{"x := s.X", "sum := x + s.Y + s.Z + s.W", "return sum"},
		},
		{
			name:    "G13 pointer receiver: replace nil-safe field access",
			orig:    []string{"if s == nil {", "return 0", "}", "return s.Value"},
			snippet: []string{"if s == nil {", "return 0", "}", "return s.Value"},
			// All lines are in origSet; snippet matches orig exactly; all anchors; no change
			want: []string{"if s == nil {", "return 0", "}", "return s.Value"},
		},
		{
			name:    "G14 pointer receiver: replace locking strategy",
			orig:    []string{"s.mu.Lock()", "defer s.mu.Unlock()", "return s.data"},
			snippet: []string{"s.mu.RLock()", "defer s.mu.RUnlock()", "return s.data"},
			// "s.mu.Lock()"@0 → anchor; "s.mu.RLock()" is NOT in origSet → lineNew
			// Wait: orig = ["s.mu.Lock()", "defer s.mu.Unlock()", "return s.data"]
			// snippet = ["s.mu.RLock()", "defer s.mu.RUnlock()", "return s.data"]
			// "s.mu.RLock()" → lineNew (not in origSet)
			// "defer s.mu.RUnlock()" → lineNew (not in origSet)
			// "return s.data" → anchor
			// Only 1 anchor → EFALLTHROUGH
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "G15 pointer receiver: three-anchor body with one gap replaced",
			orig:    []string{"s.begin()", "s.oldWork()", "s.checkpoint()", "s.finish()"},
			snippet: []string{"s.begin()", "s.newWork()", "s.checkpoint()", "s.finish()"},
			// "s.begin()"@0, "s.oldWork()"@1 not in snippet → lineNew, "s.checkpoint()"@2 anchor, "s.finish()"@3 anchor
			// snippet: @0, new, @2, @3
			// inter[0]=[{s.newWork()}] b/w @0,@2 → orig[1:2]=["s.oldWork()"] → ["s.newWork()"]
			// inter[1]=[] b/w @2,@3 → []
			want: []string{"s.begin()", "s.newWork()", "s.checkpoint()", "s.finish()"},
		},

		// ── H: Struct field tags (§7.4 gotcha) ───────────────────────────────

		{
			name:    "H01 struct body: replace one field between two anchor fields",
			orig:    []string{"ID int", "OldName string", "Age int"},
			snippet: []string{"ID int", "NewName string", "Age int"},
			want:    []string{"ID int", "NewName string", "Age int"},
		},
		{
			name:    "H02 struct body: add field via continuation after anchor",
			orig:    []string{"ID int", "Name string"},
			snippet: []string{"ID int", "Extra string", "Name string"},
			// "ID int"@0 anchor, "Extra string" lineNew, "Name string"@1 anchor
			// inter[0]=[Extra] b/w @0,@1 → orig[1:1]=[] → ["Extra string"]
			want: []string{"ID int", "Extra string", "Name string"},
		},
		{
			name:    "H03 struct body: replace field with JSON tag",
			orig:    []string{"ID   int", "Name string", "Age  int"},
			snippet: []string{"ID   int", "Name string `json:\"name\"`", "Age  int"},
			// "Name string"@1 is in origSet; "Name string `json:\"name\"`" is NOT → lineNew
			// anchors: "ID   int"@0, "Age  int"@2
			// inter[0]=[{Name string `json:...`}] b/w @0,@2 → orig[1:2]=["Name string"] → new
			want: []string{"ID   int", "Name string `json:\"name\"`", "Age  int"},
		},
		{
			name:    "H04 struct body: replace embedded field",
			orig:    []string{"OldEmbed", "Name string", "Age int"},
			snippet: []string{"NewEmbed", "Name string", "Age int"},
			// "OldEmbed" not in snippet → lineNew in snippet
			// "NewEmbed" not in origSet → lineNew
			// "Name string"@1, "Age int"@2 → 2 anchors
			// pre=[{NewEmbed}] → processSegment([NewEmbed], orig[:1]=["OldEmbed"]) → ["NewEmbed"]
			want: []string{"NewEmbed", "Name string", "Age int"},
		},
		{
			name:    "H05 struct body: continuation between first and third field preserves middle",
			orig:    []string{"X float64", "Y float64", "Z float64"},
			snippet: []string{"X float64", m, "Z float64"},
			// anchors: X@0, Z@2; inter[0]=[cont] → orig[1:2]=["Y float64"] preserved
			want: []string{"X float64", "Y float64", "Z float64"},
		},
		{
			name:    "H06 struct body: anchor on tagged field, replace next",
			orig:    []string{"ID int `db:\"id\"`", "OldField string", "Active bool"},
			snippet: []string{"ID int `db:\"id\"`", "NewField string", "Active bool"},
			want:    []string{"ID int `db:\"id\"`", "NewField string", "Active bool"},
		},
		{
			name: "H07 struct body: multiple tagged fields, replace one",
			orig: []string{
				"ID   int    `json:\"id\"`",
				"Name string `json:\"name\"`",
				"Age  int    `json:\"age\"`",
			},
			snippet: []string{
				"ID   int    `json:\"id\"`",
				"Nick string `json:\"nick\"`",
				"Age  int    `json:\"age\"`",
			},
			// "Name string `json:\"name\"`"@1 not in snippet → lineNew; "Nick..." not in origSet → lineNew
			// anchors: @0,@2; inter[0]=[Nick...] → orig[1:2]=["Name..."] → ["Nick string `json:\"nick\"`"]
			want: []string{
				"ID   int    `json:\"id\"`",
				"Nick string `json:\"nick\"`",
				"Age  int    `json:\"age\"`",
			},
		},
		{
			name:    "H08 struct body: remove optional field by empty segment preservation",
			orig:    []string{"Required string", "Optional string", "Extra string"},
			snippet: []string{"Required string", "Extra string"},
			// "Required string"@0, "Extra string"@2 anchors; inter[0]=[] → preserve orig[1:2]=["Optional string"]
			want: []string{"Required string", "Optional string", "Extra string"},
		},
		{
			name: "H09 struct body: add validate tag to existing field",
			orig: []string{
				"Name string `json:\"name\"`",
				"Email string `json:\"email\"`",
				"Age int",
			},
			snippet: []string{
				"Name string `json:\"name\"`",
				"Email string `json:\"email\" validate:\"email\"`",
				"Age int",
			},
			// "Email string `json:\"email\"`"@1 not in snippet → lineNew; new version not in origSet → lineNew
			// anchors: @0,@2; inter[0]=[newEmail] → orig[1:2]=["Email string..."] → [new]
			want: []string{
				"Name string `json:\"name\"`",
				"Email string `json:\"email\" validate:\"email\"`",
				"Age int",
			},
		},
		{
			name: "H10 struct body: continuation preserves all fields, append one",
			orig: []string{
				"A string",
				"B string",
				"C string",
			},
			snippet: []string{
				"A string",
				m,
				"C string",
				"D string",
			},
			// anchors: "A string"@0, "C string"@2; inter[0]=[cont] b/w @0,@2 → orig[1:2]=["B string"] → ["B string"]
			// post=[{D string}] → processSegment([D string], orig[3:]=[]) → ["D string"]
			want: []string{"A string", "B string", "C string", "D string"},
		},

		// ── I: Edge cases ─────────────────────────────────────────────────────

		{
			name:    "I01 duplicate anchor text in orig is ambiguous",
			orig:    []string{"x", "mid", "x", "end"},
			snippet: []string{"x", "replaced", "x", m},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "I02 duplicate anchor text: second occurrence used for second anchor",
			orig:    []string{"a", "x", "b", "x", "c"},
			snippet: []string{"a", "n1", "b", "n2", "c"},
			// "a"@0, "x"@1 NOT in snippet as text, "b"@2, "x"@3 NOT, "c"@4
			// snippet: "a" anchor, "n1" new, "b" anchor, "n2" new, "c" anchor
			// 3 anchors: @0,@2,@4; inter[0]=[n1]→orig[1:2]=["x"]→["n1"]; inter[1]=[n2]→orig[3:4]=["x"]→["n2"]
			want: []string{"a", "n1", "b", "n2", "c"},
		},
		{
			name:    "I03 empty line in snippet is lineNew, not anchor",
			orig:    []string{"a", "mid", "b"},
			snippet: []string{"a", "", "b"},
			// ""→lineNew; inter[0]=[""] → orig[1:2]=["mid"] → [""]
			want: []string{"a", "", "b"},
		},
		{
			name:    "I04 empty line in orig does not become anchor even if snippet has empty",
			orig:    []string{"a", "", "b"},
			snippet: []string{"a", "", "b"},
			// origSet = {"a","b"} (empty lines excluded)
			// "" in snippet → lineNew; "a"@0 anchor, ""@1 lineNew, "b"@2 anchor
			// inter[0]=[{"",lineNew}] b/w @0,@2 → orig[1:2]=[""] → [""]
			want: []string{"a", "", "b"},
		},
		{
			name:    "I05 line that appears in both orig and snippet as last item: anchor-terminated body",
			orig:    []string{"open()", "doOld()", "close()"},
			snippet: []string{"open()", "doNew()", "close()"},
			want:    []string{"open()", "doNew()", "close()"},
		},
		{
			name: "I06 anchor on long line",
			orig: []string{
				"result := someVeryLongFunctionName(parameterOne, parameterTwo, parameterThree)",
				"oldShort()",
				"return result",
			},
			snippet: []string{
				"result := someVeryLongFunctionName(parameterOne, parameterTwo, parameterThree)",
				"newShort()",
				"return result",
			},
			want: []string{
				"result := someVeryLongFunctionName(parameterOne, parameterTwo, parameterThree)",
				"newShort()",
				"return result",
			},
		},
		{
			name:    "I07 anchor is a common Go idiom: if err != nil",
			orig:    []string{"if err != nil {", "oldHandle(err)", "return err"},
			snippet: []string{"if err != nil {", "newHandle(err)", "return err"},
			want:    []string{"if err != nil {", "newHandle(err)", "return err"},
		},
		{
			name:    "I08 orig has trailing empty lines: preserved in post",
			orig:    []string{"anchor1", "mid", "anchor2", "", ""},
			snippet: []string{"anchor1", "new", "anchor2"},
			// post=[{""},{"}] orig[3:]=["",""] → processSegment([], ["",""]) → preserve ["",""]
			want: []string{"anchor1", "new", "anchor2", "", ""},
		},
		{
			name:    "I09 snippet has leading empty line in pre: empty replaces pre orig",
			orig:    []string{"pre1", "anchor1", "mid", "anchor2"},
			snippet: []string{"", "anchor1", "new", "anchor2"},
			// pre=[{"",lineNew}]; processSegment([""], orig[:1]=["pre1"]) → [""]
			want: []string{"", "anchor1", "new", "anchor2"},
		},
		{
			name:    "I10 single-character anchor lines",
			orig:    []string{"a", "old_content", "b"},
			snippet: []string{"a", "new_content", "b"},
			want:    []string{"a", "new_content", "b"},
		},
		{
			name:    "I11 very short body: two lines, no gap",
			orig:    []string{"start()", "end()"},
			snippet: []string{"start()", "inserted()", "end()"},
			want:    []string{"start()", "inserted()", "end()"},
		},
		{
			name: "I12 preserve large block with continuation between two far-apart anchors",
			orig: []string{
				"begin()",
				"line01", "line02", "line03", "line04", "line05",
				"line06", "line07", "line08", "line09", "line10",
				"end()",
			},
			snippet: []string{"begin()", m, "end()"},
			want: []string{
				"begin()",
				"line01", "line02", "line03", "line04", "line05",
				"line06", "line07", "line08", "line09", "line10",
				"end()",
			},
		},
		{
			name: "I13 anchor match is exact (leading space matters)",
			orig: []string{"	indented", "other", "end"},
			// Tab-indented line; snippet uses same tab → anchor
			snippet: []string{"	indented", "new_other", "end"},
			want:    []string{"	indented", "new_other", "end"},
		},
		{
			name:    "I14 anchor not found because of trailing space difference",
			orig:    []string{"line_a ", "middle", "line_b"},
			snippet: []string{"line_a", "new_middle", "line_b"},
			// "line_a " (with trailing space) is in origSet; "line_a" (no trailing space) is lineNew
			// "line_b"@2 is in origSet → anchor
			// Only 1 anchor ("line_b") → EFALLTHROUGH
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "I15 multi-line replacement narrows to single line",
			orig:    []string{"first()", "alpha()", "beta()", "gamma()", "last()"},
			snippet: []string{"first()", "single()", "last()"},
			want:    []string{"first()", "single()", "last()"},
		},

		// ── J: EFALLTHROUGH — ineligible snippets ─────────────────────────────

		{
			name:    "J01 zero anchor lines: all new",
			orig:    []string{"a := 1", "b := 2"},
			snippet: []string{"totally_new"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J02 only one anchor line",
			orig:    []string{"a := 1", "b := 2"},
			snippet: []string{"a := 1", "new_line"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J03 anchor text not found in original",
			orig:    []string{"a := 1", "b := 2"},
			snippet: []string{"a := 1", "new", "x := 999"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J04 anchors in wrong order",
			orig:    []string{"b := 2", "a := 1"},
			snippet: []string{"a := 1", "new", "b := 2"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J05 two continuation markers in same inter-anchor segment",
			orig:    []string{"a := 1", "mid1", "mid2", "b := 2"},
			snippet: []string{"a := 1", m, m, "b := 2"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J06 two continuation markers in pre-anchor segment",
			orig:    []string{"pre1", "anchor1", "mid", "anchor2"},
			snippet: []string{m, m, "anchor1", "new", "anchor2"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J07 two continuation markers in post-anchor segment",
			orig:    []string{"anchor1", "mid", "anchor2", "post1"},
			snippet: []string{"anchor1", "new", "anchor2", m, m},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J08 empty original body",
			orig:    []string{},
			snippet: []string{"some line", "other line"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J09 empty snippet",
			orig:    []string{"a := 1", "b := 2"},
			snippet: []string{},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J10 snippet with only continuation markers",
			orig:    []string{"a := 1", "b := 2"},
			snippet: []string{m},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J11 snippet with only empty lines",
			orig:    []string{"a := 1", "b := 2"},
			snippet: []string{"", ""},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J12 misspelled anchor (one char off)",
			orig:    []string{"return err", "other"},
			snippet: []string{"return err", "new", "return erR"},
			// "return erR" is NOT in origSet → lineNew; only 1 anchor → EFALLTHROUGH
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J13 same line twice in snippet but only one occurrence reachable",
			orig:    []string{"a", "b"},
			snippet: []string{"a", "new", "a"},
			// "a"@0, "new" new, "a" → searchFrom=1; "a" at position 0 is not ≥ 1; not found → EFALLTHROUGH
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J14 snippet with all-anchor lines but only one unique match",
			orig:    []string{"a := 1"},
			snippet: []string{"a := 1", "a := 1"},
			// orig has only 1 line; "a := 1"@0, then searching from 1 → not found → EFALLTHROUGH
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J15 single-line orig, snippet has two new lines",
			orig:    []string{"only_line"},
			snippet: []string{"new1", "new2"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J16 single-line orig, single-line snippet matching",
			orig:    []string{"only_line"},
			snippet: []string{"only_line"},
			// 1 anchor, need ≥ 2 → EFALLTHROUGH
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J17 anchor found then second not reachable (consumed by first)",
			orig:    []string{"x", "y"},
			snippet: []string{"x", "mid", "y", "mid2", "x"},
			// "x"@0 anchor, "mid" new, "y"@1 anchor, "mid2" new, "x" → searchFrom=2; no "x" at ≥2 → EFALLTHROUGH
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J18 snippet matches no orig lines (all new)",
			orig:    []string{"alpha", "beta", "gamma"},
			snippet: []string{"delta", "epsilon", "zeta"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J19 only one anchor despite three snippet lines",
			orig:    []string{"x := foo()", "bar()"},
			snippet: []string{"new_pre", "x := foo()", "new_post"},
			// "x := foo()"@0 is the only anchor → EFALLTHROUGH
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J20 continuation marker alone is not an anchor",
			orig:    []string{"a := 1", "b := 2"},
			snippet: []string{m, "a := 1"},
			// cont (pre), "a := 1"@0 → 1 anchor → EFALLTHROUGH
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J21 anchors in reverse with nothing new between them",
			orig:    []string{"first", "second", "third"},
			snippet: []string{"third", "first"},
			// "third"@2, "first" → searchFrom=3; no more "first" → EFALLTHROUGH
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J22 all snippet lines are continuation markers",
			orig:    []string{"a := 1", "b := 2", "c := 3"},
			snippet: []string{m, m, m},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J23 zero-length orig, non-empty snippet",
			orig:    []string{},
			snippet: []string{"a := 1"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J24 snippet has one valid anchor and one out-of-order anchor",
			orig:    []string{"b := 2", "a := 1", "c := 3"},
			snippet: []string{"a := 1", "new", "b := 2"},
			// "a := 1"@1 anchor, "new" new, "b := 2" → searchFrom=2; "b := 2" at 0 < 2 → not found → EFALLTHROUGH
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J25 three continuation markers spread across segments (inter has 2)",
			orig:    []string{"a", "g1", "b", "g2", "c"},
			snippet: []string{"a", m, m, "b", m, "c"},
			// "a"@0, cont, cont → only 1 cont in inter[0] allowed; here inter[0] has 2 conts → EFALLTHROUGH
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J26 single anchor with continuation before it",
			orig:    []string{"pre", "anchor"},
			snippet: []string{m, "anchor"},
			// cont (pre), "anchor"@1 → only 1 anchor → EFALLTHROUGH
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J27 snippet where anchor appears after new lines but second anchor missing",
			orig:    []string{"a := 1", "b := 2"},
			snippet: []string{"new_pre1", "new_pre2", "a := 1", "new_post"},
			// "a := 1"@0 is the only anchor → EFALLTHROUGH
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J28 snippet with anchor then continuation then no second anchor",
			orig:    []string{"a := 1", "b := 2"},
			snippet: []string{"a := 1", m},
			// "a := 1"@0 anchor, cont → 1 anchor → EFALLTHROUGH
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J29 anchor line appears only once in orig but snippet references it twice in order",
			orig:    []string{"unique", "other"},
			snippet: []string{"unique", "new", "unique"},
			// "unique"@0 anchor, "new" new, "unique" → searchFrom=1; "unique" not at ≥1 → EFALLTHROUGH
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J30 orig with only empty lines: no anchors possible",
			orig:    []string{"", "", ""},
			snippet: []string{"a := 1", "b := 2"},
			// origSet is empty (all lines empty); no anchors → EFALLTHROUGH
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J31 snippet with one anchor from orig but all others are new",
			orig:    []string{"only_anchor", "other1", "other2"},
			snippet: []string{"new1", "only_anchor", "new2"},
			// "only_anchor"@0 → 1 anchor → EFALLTHROUGH
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J32 inter-segment has 2 continuation markers (non-adjacent)",
			orig:    []string{"a", "g1", "g2", "b"},
			snippet: []string{"a", m, "inserted", m, "b"},
			// 2 conts in inter[0] → EFALLTHROUGH
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J33 pre has 2 continuation markers: new line then 2 conts",
			orig:    []string{"pre", "anchor1", "mid", "anchor2"},
			snippet: []string{"newline", m, m, "anchor1", "new", "anchor2"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J34 post has 2 continuation markers",
			orig:    []string{"anchor1", "mid", "anchor2", "post"},
			snippet: []string{"anchor1", "new", "anchor2", m, "x", m},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J35 snippet with valid anchors but continuation in wrong inter",
			orig:    []string{"a", "g1", "b", "g2", "g3", "c"},
			snippet: []string{"a", m, "b", m, m, "c"},
			// inter[1] has 2 conts → EFALLTHROUGH
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J36 case-sensitive mismatch for anchor",
			orig:    []string{"Return x", "other"},
			snippet: []string{"return x", "new", "other"},
			// "return x" is NOT in origSet (origSet has "Return x") → lineNew
			// "other"@1 → 1 anchor → EFALLTHROUGH
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J37 anchor with extra space at start",
			orig:    []string{"x := 1", "y := 2"},
			snippet: []string{" x := 1", "new", "y := 2"},
			// " x := 1" (leading space) NOT in origSet → lineNew; only "y := 2"@1 is anchor → 1 anchor → EFALLTHROUGH
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J38 no anchors: snippet has only continuation and new lines",
			orig:    []string{"a := 1", "b := 2"},
			snippet: []string{m, "totally_new_line"},
			// cont (pre), "totally_new_line" lineNew → 0 anchors → EFALLTHROUGH
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J39 snippet matches orig exactly but only has 1 unique line",
			orig:    []string{"x"},
			snippet: []string{"x"},
			// 1 anchor → EFALLTHROUGH
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J40 anchors separated by 2 conts in one inter segment",
			orig:    []string{"a", "mid", "b"},
			snippet: []string{"a", m, m, "b"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J41 snippet with three conts and two anchors but first inter has 2 conts",
			orig:    []string{"a", "g1", "b", "g2", "c"},
			snippet: []string{"a", m, m, "b", m, "c"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J42 forward progress exhausted before second anchor found",
			orig:    []string{"p", "q"},
			snippet: []string{"q", "new", "p"},
			// "q"@1 anchor, "new" new, "p" → searchFrom=2; "p" not at ≥2 → EFALLTHROUGH
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J43 orig has two identical lines: snippet tries to anchor past second",
			orig:    []string{"dup", "dup", "z"},
			snippet: []string{"dup", "dup", "new", "dup"},
			// "dup"@0, "dup"@1 (searchFrom=1 finds @1), "new" new, "dup" → searchFrom=2; "dup" not at ≥2 → EFALLTHROUGH
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J44 snippet anchors skip over each other backward",
			orig:    []string{"z", "y", "x"},
			snippet: []string{"x", "mid", "y"},
			// "x"@2, "y" → searchFrom=3; "y" at 1 < 3 → EFALLTHROUGH
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J45 snippet with single new line only",
			orig:    []string{"a := 1"},
			snippet: []string{"new_line"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J46 two continuation markers with new line between (pre segment)",
			orig:    []string{"pre", "anchor1", "mid", "anchor2"},
			snippet: []string{m, "newbetween", m, "anchor1", "newMid", "anchor2"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J47 valid anchors but inter has 3 continuation markers",
			orig:    []string{"a", "g1", "g2", "g3", "b"},
			snippet: []string{"a", m, m, m, "b"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J48 second anchor is continuation marker (not an anchor)",
			orig:    []string{"a := 1", "b := 2"},
			snippet: []string{"a := 1", m, "new"},
			// "a := 1"@0 → 1 anchor; cont is lineCont not lineAnchor → EFALLTHROUGH
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J49 all lines in orig are empty: origSet empty, no anchors",
			orig:    []string{"", "", "", ""},
			snippet: []string{"x", "y"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "J50 snippet references orig line but in reverse with three lines total",
			orig:    []string{"c := 3", "b := 2", "a := 1"},
			snippet: []string{"a := 1", "new_mid", "c := 3"},
			// "a := 1"@2, "new_mid" new, "c := 3" → searchFrom=3; "c := 3" is at position 0 < 3 → EFALLTHROUGH
			wantErr: edit.ErrFallthrough,
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := edit.SplicePerLang(edit.LangGo, tc.orig, tc.snippet)

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("case %d %q: SplicePerLang() error = %v, want %v", i+1, tc.name, err, tc.wantErr)
				}
				if got != nil {
					t.Fatalf("case %d %q: SplicePerLang() body = %v, want nil on error", i+1, tc.name, got)
				}
				return
			}

			if err != nil {
				t.Fatalf("case %d %q: SplicePerLang() unexpected error: %v", i+1, tc.name, err)
			}
			if !slicesEqual(got, tc.want) {
				t.Fatalf("case %d %q:\ngot:  %v\nwant: %v", i+1, tc.name, got, tc.want)
			}
		})
	}
}

package edit_test

import (
	"errors"
	"testing"

	"oro/pkg/edit"
)

// TestPerLangSplice verifies per-language indentation/decorator handling (§7.4).
// Covers: Go braced, Python indent, TS decorators, JS arrow-funcs.
func TestPerLangSplice(t *testing.T) {
	tests := []struct {
		name    string
		lang    edit.Language
		orig    []string
		snippet []string
		want    []string
		wantErr error
	}{
		// ── Go: braced bodies ────────────────────────────────────────────────
		{
			name: "go: anchor splice replaces inter-anchor gap",
			lang: edit.LangGo,
			orig: []string{
				"x := 1",
				"old := 2",
				"y := 3",
			},
			snippet: []string{
				"x := 1",
				"new := 99",
				"y := 3",
			},
			want: []string{
				"x := 1",
				"new := 99",
				"y := 3",
			},
		},
		{
			name: "go: continuation marker preserves original gap",
			lang: edit.LangGo,
			orig: []string{
				"x := 1",
				"mid1 := 2",
				"mid2 := 3",
				"return x",
			},
			snippet: []string{
				"x := 1",
				"// ...",
				"return x",
			},
			want: []string{
				"x := 1",
				"mid1 := 2",
				"mid2 := 3",
				"return x",
			},
		},

		// ── Python: indentation normalization ───────────────────────────────
		{
			name: "python: snippet with zero indent normalized to match four-space orig",
			lang: edit.LangPython,
			orig: []string{
				"    x = 1",
				"    y_old = 2",
				"    return x",
			},
			snippet: []string{
				"x = 1",
				"y_new = 99",
				"return x",
			},
			want: []string{
				"    x = 1",
				"    y_new = 99",
				"    return x",
			},
		},
		{
			name: "python: cont marker not re-indented; orig region preserved",
			lang: edit.LangPython,
			orig: []string{
				"    x = 1",
				"    gap1 = 10",
				"    gap2 = 20",
				"    return x",
			},
			snippet: []string{
				"x = 1",
				"# ...",
				"return x",
			},
			want: []string{
				"    x = 1",
				"    gap1 = 10",
				"    gap2 = 20",
				"    return x",
			},
		},
		{
			name: "python: snippet with two-space indent normalized to eight-space orig",
			lang: edit.LangPython,
			orig: []string{
				"        x = 1",
				"        old = 2",
				"        return x",
			},
			snippet: []string{
				"  x = 1",
				"  new_val = 3",
				"  return x",
			},
			want: []string{
				"        x = 1",
				"        new_val = 3",
				"        return x",
			},
		},
		{
			name: "python: nested indentation levels scaled correctly",
			lang: edit.LangPython,
			orig: []string{
				"    x = 1",
				"    if cond:",
				"        do_thing()",
				"    return x",
			},
			snippet: []string{
				"x = 1",
				"if cond:",
				"    do_thing()",
				"return x",
			},
			want: []string{
				"    x = 1",
				"    if cond:",
				"        do_thing()",
				"    return x",
			},
		},
		{
			name: "python: snippet already matches orig indent — no change",
			lang: edit.LangPython,
			orig: []string{
				"    x = 1",
				"    old = 2",
				"    return x",
			},
			snippet: []string{
				"    x = 1",
				"    new = 5",
				"    return x",
			},
			want: []string{
				"    x = 1",
				"    new = 5",
				"    return x",
			},
		},

		// ── TypeScript: decorator preservation ──────────────────────────────
		{
			name: "ts: decorator lines in pre-anchor region are preserved",
			lang: edit.LangTypeScript,
			orig: []string{
				"@Component({",
				"  selector: 'app-root'",
				"})",
				"ngOnInit(): void {}",
				"ngOnDestroy(): void {}",
			},
			snippet: []string{
				"ngOnInit(): void {}",
				"this.setup()",
				"ngOnDestroy(): void {}",
			},
			want: []string{
				"@Component({",
				"  selector: 'app-root'",
				"})",
				"ngOnInit(): void {}",
				"this.setup()",
				"ngOnDestroy(): void {}",
			},
		},
		{
			name: "ts: decorator anchor matched explicitly preserves surrounding structure",
			lang: edit.LangTypeScript,
			orig: []string{
				"@Injectable()",
				"constructor(private svc: Svc) {}",
				"doWork(): void {}",
			},
			snippet: []string{
				"@Injectable()",
				"// ...",
				"doWork(): void {}",
			},
			want: []string{
				"@Injectable()",
				"constructor(private svc: Svc) {}",
				"doWork(): void {}",
			},
		},

		// ── JavaScript: arrow functions ──────────────────────────────────────
		{
			name: "js: arrow function body spliced via anchors",
			lang: edit.LangJavaScript,
			orig: []string{
				"const result = x + 1",
				"const interim = computeOld()",
				"return result",
			},
			snippet: []string{
				"const result = x + 1",
				"const interim = computeNew()",
				"return result",
			},
			want: []string{
				"const result = x + 1",
				"const interim = computeNew()",
				"return result",
			},
		},
		{
			name: "js: continuation marker preserves arrow function body interior",
			lang: edit.LangJavaScript,
			orig: []string{
				"const result = transform(x)",
				"const a = 1",
				"const b = 2",
				"return result",
			},
			snippet: []string{
				"const result = transform(x)",
				"// ...",
				"return result",
			},
			want: []string{
				"const result = transform(x)",
				"const a = 1",
				"const b = 2",
				"return result",
			},
		},

		// ── EFALLTHROUGH passthrough ─────────────────────────────────────────
		{
			name:    "go: EFALLTHROUGH propagated when only one anchor",
			lang:    edit.LangGo,
			orig:    []string{"x := 1", "y := 2"},
			snippet: []string{"x := 1", "new line"},
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "python: EFALLTHROUGH propagated after indent normalisation",
			lang:    edit.LangPython,
			orig:    []string{"    x = 1", "    y = 2"},
			snippet: []string{"x = 1", "new line"},
			wantErr: edit.ErrFallthrough,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := edit.SplicePerLang(tc.lang, tc.orig, tc.snippet)

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("SplicePerLang() error = %v, want %v", err, tc.wantErr)
				}
				if got != nil {
					t.Fatalf("SplicePerLang() body = %v, want nil on error", got)
				}
				return
			}

			if err != nil {
				t.Fatalf("SplicePerLang() unexpected error: %v", err)
			}
			if !slicesEqual(got, tc.want) {
				t.Fatalf("SplicePerLang() =\n  %v\nwant\n  %v", got, tc.want)
			}
		})
	}
}

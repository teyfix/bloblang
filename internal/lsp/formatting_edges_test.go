package lsp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestFormattingOpinionatedCases(t *testing.T) {
	cases := []struct{ name, input, want string }{
		{"fold lambda header", `meta endpoint = this.
  collapse().
  key_values().
  fold(@endpoint,
  item -> item.tally.
    re_replace_all(
      ["{{", item.value.key, "}}"].join("\\s*"),
      item.value.value.string()
    )
  )`, `meta endpoint = this.
  collapse().
  key_values().
  fold(@endpoint, item -> item.tally.
    re_replace_all(
      ["{{", item.value.key, "}}"].join("\\s*"),
      item.value.value.string()
    )
  )`},
		{"object argument and outer chain", `root = this.data.seasons.or([]).map_each(s -> this.
  without("data").
  assign(
    {
      "data": this.data.
        with("id", "name", "original_name").
        assign({ "season": s })
    }
  )
)`, `root = this.data.seasons.
  or([]).
  map_each(s -> this.
    without("data").
    assign({
      "data": this.data.
        with("id", "name", "original_name").
        assign({ "season": s })
    })
  )`},
		{"nested objects and inline comma", `root = if this.data.overview.or("") == "" {
  deleted()
} else {
  this.
    without("data", "meta").
    assign(
      {
        "data": {
          "id": this.data.id,
          "overview": this.data.apply("embed_input"),
          "model": {
            "id": env("EMBEDDING_MODEL").catch(null),
            "dimensions": env("EMBEDDING_DIMENSIONS").number().catch(null),
          },
        },
        "meta": { "ts": now(), "v": 1, },
        "connect": @
      }
    )
}`, `root = if this.data.overview.or("") == "" {
  deleted()
} else {
  this.
    without("data", "meta").
    assign({
      "data": {
        "id": this.data.id,
        "overview": this.data.apply("embed_input"),
        "model": {
          "id": env("EMBEDDING_MODEL").catch(null),
          "dimensions": env("EMBEDDING_DIMENSIONS").number().catch(null),
        },
      },
      "meta": { "ts": now(), "v": 1 },
      "connect": @
    })
}`},
		{"lambda object closing", `root = this.data.also_known_as.
  map_each(aka -> {
    "person_id": this.data.id,
    "also_known_as": aka,
    "version_ts": this.meta.ts
  }
  )`, `root = this.data.also_known_as.
  map_each(aka -> {
    "person_id": this.data.id,
    "also_known_as": aka,
    "version_ts": this.meta.ts
  })`},
		{"collapsed object comma", `root = this.data.assign({
  "version_ts": this.meta.ts,
})`, `root = this.data.assign({ "version_ts": this.meta.ts })`},
		{"nested image lambdas", `root = this.data.images.
  with("profiles").
  key_values().
  map_each(kv -> kv.value.map_each(profile -> profile.
    with(
      "file_path",
      "aspect_ratio",
      "width",
      "height",
      "vote_average",
      "vote_count",
    ).
    assign(
      { "kind": kv.key, "person_id": this.data.id, "version_ts": this.meta.ts, }
    )
  )
  ).
  flatten()`, `root = this.data.images.
  with("profiles").
  key_values().
  map_each(kv -> kv.value.
    map_each(profile -> profile.
      with(
        "file_path",
        "aspect_ratio",
        "width",
        "height",
        "vote_average",
        "vote_count",
      ).
      assign({
        "kind": kv.key,
        "person_id": this.data.id,
        "version_ts": this.meta.ts,
      })
    )
  ).
  flatten()`},
		{"nested translation chains", `root = this.data.translations.map_each(tr -> tr.with("name", "biography").key_values().filter(kv -> ![
  0,
  null,
  ""
].
  contains(kv.value)
).map_each(kv -> tr.
  with("iso_3166_1", "iso_639_1").
  assign(
    {
      "field": kv.key,
      "value": kv.value.string(),
      "primary": tr.primary,
      "person_id": this.data.id,
      "version_ts": this.meta.ts,
    }
  )
)
).flatten()`, `root = this.data.translations.
  map_each(tr -> tr.
    with("name", "biography").
    key_values().
    filter(kv -> ![0, null, ""].contains(kv.value)).
    map_each(kv -> tr.
      with("iso_3166_1", "iso_639_1").
      assign({
        "field": kv.key,
        "value": kv.value.string(),
        "primary": tr.primary,
        "person_id": this.data.id,
        "version_ts": this.meta.ts,
      })
    )
  ).
  flatten()`},
		{"nested crew lambdas", `root = this.data.episodes.
  map_each(ep -> ep.crew.map_each(crew -> crew.
    with("department", "job").
    assign(
      {
        "credit_id": "00000000%s".format(crew.credit_id),
        "version_ts": this.meta.ts,
      }
    )
  )
  ).
  flatten()`, `root = this.data.episodes.
  map_each(ep -> ep.crew.
    map_each(crew -> crew.
      with("department", "job").
      assign({
        "credit_id": "00000000%s".format(crew.credit_id),
        "version_ts": this.meta.ts,
      })
    )
  ).
  flatten()`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, uri := featureHandler(t)
			for _, input := range []string{tc.input, tc.want} {
				formatted, ok := h.formatText(uri, input, 2)
				require.True(t, ok, input)
				require.Equal(t, tc.want+"\n", formatted)
				for _, line := range strings.Split(formatted, "\n") {
					require.LessOrEqual(t, utf8.RuneCountInString(line), 80, line)
				}
			}
		})
	}
	// With enough room the inner call stays flat while its enclosing chain wraps.
	t.Run("fold at wider print width", func(t *testing.T) {
		h, uri := featureHandler(t)
		require.NoError(t, os.WriteFile(filepath.Join(h.workspaceRoot, ".bloblangrc.json"), []byte(fmt.Sprintf(`{"formatter":{"printWidth":%d}}`, 100)), 0600))
		want := `meta endpoint = this.
  collapse().
  key_values().
  fold(@endpoint, item -> item.tally.
    re_replace_all(["{{", item.value.key, "}}"].join("\\s*"), item.value.value.string())
  )` + "\n"
		got, ok := h.formatText(uri, cases[0].input, 2)
		require.True(t, ok)
		require.Equal(t, want, got)
		twice, ok := h.formatText(uri, got, 2)
		require.True(t, ok)
		require.Equal(t, got, twice)
	})
}

func TestFormattingCollectionCommasAndComments(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"array", "root = [1, 2,]\n", "root = [1, 2]\n"},
		{"nested inline collections", "root = {a: [1, 2,], b: {c: 3,},}\n", "root = { a: [1, 2], b: { c: 3 } }\n"},
		{"call comma retained", "root = this.with(\"a\",)\n", "root = this.with(\"a\",)\n"},
		{"object final comment", "root = {a: 1, # keep\n}\n", "root = {\n  a: 1,\n  # keep\n}\n"},
		{"array final comment", "root = [1, # keep\n]\n", "root = [\n  1,\n  # keep\n]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, uri := featureHandler(t)
			got, ok := h.formatText(uri, tc.input, 2)
			require.True(t, ok)
			require.Equal(t, tc.want, got)
			twice, ok := h.formatText(uri, got, 2)
			require.True(t, ok)
			require.Equal(t, got, twice)
			_, err := h.benv.Parse(got)
			require.NoError(t, err)
		})
	}
}

func TestFormattingTokenValidation(t *testing.T) {
	h, _ := featureHandler(t)
	for _, tc := range []struct {
		name, before, after string
		valid               bool
	}{
		{"inline object comma", "root = {a: 1,}", "root = {a: 1}", true},
		{"collapsed array comma", "root = [\n1,\n]", "root = [1]", true},
		{"multiline comma", "root = [\n1,\n]", "root = [\n1\n]", false},
		{"argument comma", "root = this.with(\"a\",)", "root = this.with(\"a\")", false},
		{"item separator", "root = [1, 2]", "root = [1]", false},
		{"changed value", "root = {a: 1,}", "root = {a: 2}", false},
		{"dropped comment", "root = {a: 1, # keep\n}", "root = {a: 1}", false},
		{"added comma", "root = [1]", "root = [1,]", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var streams [2][]formatToken
			for i, input := range []string{tc.before, tc.after} {
				tree, err := h.parser.Parse(fmt.Sprintf("token-validation-%d", i), input)
				require.NoError(t, err)
				require.False(t, tree.RootNode().HasError())
				tokens(tree.RootNode(), []byte(input), &streams[i])
				tree.Close()
			}
			require.Equal(t, tc.valid, equivalentFormatTokens(streams[0], streams[1]))
		})
	}
}

func TestFormattingHuggedArgumentWidths(t *testing.T) {
	for _, width := range []int{20, 40, 80, 120} {
		for _, indent := range []int{2, 4} {
			t.Run(fmt.Sprintf("width%d-indent%d", width, indent), func(t *testing.T) {
				h, uri := featureHandler(t)
				require.NoError(t, os.WriteFile(filepath.Join(h.workspaceRoot, ".bloblangrc.json"), []byte(fmt.Sprintf(`{"formatter":{"printWidth":%d}}`, width)), 0600))
				// The object alone fits exactly after assign(; its closing call delimiter
				// requires one more column, so the object must expand.
				value := strings.Repeat("x", width-indent-16)
				inputs := []string{
					`root = this.assign({ a: "` + value + `" })`,
					`root = this.map_each(x -> { a: x.a, b: x.b, })`,
					`root = this.map_each(x -> [x.a, x.b,])`,
					`root = this.fold({a: 1,}, x -> x.tally.assign({b: x.v,}))`,
					"root = this.assign(\n# before\n{a: 1, b: 2,}\n# after\n)\n",
					"root = this.map_each(x -> {\na: x.a, # keep\nb: x.b,\n})\n",
				}
				for i, input := range inputs {
					got, ok := h.formatText(uri, input, indent)
					require.True(t, ok, input)
					if i == 0 {
						require.Contains(t, got, "assign({\n")
					}
					twice, ok := h.formatText(uri, got, indent)
					require.True(t, ok, got)
					require.Equal(t, got, twice, input)
					for _, line := range strings.Split(got, "\n") {
						require.LessOrEqual(t, utf8.RuneCountInString(line), width, got)
					}
					_, err := h.benv.Parse(got)
					require.NoError(t, err, got)
				}
			})
		}
	}
}

func TestFormattingBlockLambdaClosings(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"nested if", `root = this.data.
  map_each(kv -> if kv.value.exists("results") {
    kv.value.results
  } else {
    if kv.value.exists(kv.key) {
      kv.value.get(kv.key)
    } else {
      kv.value
    }
  }
  )`, `root = this.data.
  map_each(kv -> if kv.value.exists("results") {
    kv.value.results
  } else {
    if kv.value.exists(kv.key) {
      kv.value.get(kv.key)
    } else {
      kv.value
    }
  })`},
		{"else if", `root = this.data.map_each(kv -> if kv.value.exists("results") { kv.value.results } else if kv.value.exists(kv.key) { kv.value.get(kv.key) } else { kv.value })`, `root = this.data.
  map_each(kv -> if kv.value.exists("results") {
    kv.value.results
  } else if kv.value.exists(kv.key) {
    kv.value.get(kv.key)
  } else {
    kv.value
  })`},
		{"match and chained call", `root = this.data.map_each(kv -> match { kv.value.exists("results") => kv.value.results, _ => kv.value }).flatten()`, `root = this.data.
  map_each(kv -> match {
    kv.value.exists("results") => kv.value.results,
    _ => kv.value
  }).
  flatten()`},
		{"comment before closing block", `root = this.data.map_each(kv -> if kv.value.exists("results") {
kv.value.results
} else {
kv.value # keep fallback
})`, `root = this.data.
  map_each(kv -> if kv.value.exists("results") {
    kv.value.results
  } else {
    kv.value # keep fallback
  })`},
		{"comment after closing block", `root = this.data.map_each(kv -> if kv.value.exists("results") { kv.value.results } else { kv.value } # keep comment
)`, `root = this.data.
  map_each(kv -> if kv.value.exists("results") {
    kv.value.results
  } else {
    kv.value
  } # keep comment
  )`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, uri := featureHandler(t)
			_, err := h.benv.Parse(tc.input)
			require.NoError(t, err)
			for _, input := range []string{tc.input, tc.want} {
				formatted, ok := h.formatText(uri, input, 2)
				require.True(t, ok)
				require.Equal(t, tc.want+"\n", formatted)
				_, err := h.benv.Parse(formatted)
				require.NoError(t, err)
			}
		})
	}
}

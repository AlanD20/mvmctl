package download

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

// ─── resolveVersion ──────────────────────────────────────────────────────────
// Rationale: resolveVersion is the core version resolution logic for Apache
// directory listings. It handles skip patterns, codename mappings, version
// prefixes, and fallback passthrough. A bug would cause incorrect or missing
// version entries in listings.

func TestResolveVersion(t *testing.T) {
	tests := map[string]struct {
		dirName         string
		skipPatterns    []string
		versionPrefix   string
		codenameMapping map[string]string
		wantVer         string
		wantCodename    string
		wantOK          bool
	}{
		"dot_dir_skipped": {
			dirName: ".",
			wantOK:  false,
		},
		"dotdot_dir_skipped": {
			dirName: "..",
			wantOK:  false,
		},
		"skip_pattern_matches": {
			dirName:      "1.2.3-rc1",
			skipPatterns: []string{"rc", "alpha"},
			wantOK:       false,
		},
		"skip_pattern_matches_second": {
			dirName:      "1.2.3-alpha",
			skipPatterns: []string{"rc", "alpha"},
			wantOK:       false,
		},
		"multiple_skip_patterns_all_match": {
			dirName:      "1.2.3-alpha",
			skipPatterns: []string{"alpha", "beta", "rc"},
			wantOK:       false,
		},
		"codename_mapping_no_match": {
			dirName:         "bullseye",
			codenameMapping: map[string]string{"bookworm": "12"},
			wantOK:          false,
		},
		"version_prefix_no_match": {
			dirName:       "v1.2.3",
			versionPrefix: "rel-",
			wantOK:        false,
		},
		"skip_pattern_no_match": {
			dirName:      "1.2.3",
			skipPatterns: []string{"rc", "alpha"},
			wantVer:      "1.2.3",
			wantOK:       true,
		},
		"codename_mapping_match": {
			dirName:         "bookworm",
			codenameMapping: map[string]string{"bookworm": "12"},
			wantVer:         "12",
			wantCodename:    "bookworm",
			wantOK:          true,
		},
		"version_prefix_match": {
			dirName:       "v1.2.3",
			versionPrefix: "v",
			wantVer:       "1.2.3",
			wantOK:        true,
		},
		"no_prefix_no_mapping_passthrough": {
			dirName: "1.2.3",
			wantVer: "1.2.3",
			wantOK:  true,
		},
		"empty_skip_patterns": {
			dirName: "latest",
			wantVer: "latest",
			wantOK:  true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			gotVer, gotCodename, gotOK := resolveVersion(
				tc.dirName, tc.skipPatterns, tc.versionPrefix, tc.codenameMapping,
			)
			if !tc.wantOK {
				if gotOK {
					t.Errorf("expected ok=false, got ok=true (ver=%q, codename=%q)", gotVer, gotCodename)
				}
				return
			}
			if !gotOK {
				t.Fatal("expected ok=true, got ok=false")
			}
			if gotVer != tc.wantVer {
				t.Errorf("version: got %q, want %q", gotVer, tc.wantVer)
			}
			if gotCodename != tc.wantCodename {
				t.Errorf("codename: got %q, want %q", gotCodename, tc.wantCodename)
			}
		})
	}
}

// ─── parseDirectoryListing ──────────────────────────────────────────────────
// Rationale: parseDirectoryListing extracts directory names from Apache-style
// HTML directory listings using href="<dir>/" patterns. Dedup is applied so
// duplicate entries don't cause duplicate versions.

func TestParseDirectoryListing(t *testing.T) {
	tests := map[string]struct {
		html string
		want []string
	}{
		"empty_html": {
			html: "",
			want: []string{},
		},
		"html_no_href": {
			html: "<html><body><p>no links</p></body></html>",
			want: []string{},
		},
		"href_but_no_trailing_slash": {
			html: `<a href="file.txt">file.txt</a>`,
			want: []string{},
		},
		"single_dir": {
			html: `<a href="1.2.3/">1.2.3/</a>`,
			want: []string{"1.2.3"},
		},
		"multiple_dirs": {
			html: `<a href="1.2.3/">1.2.3/</a><a href="1.2.4/">1.2.4/</a>`,
			want: []string{"1.2.3", "1.2.4"},
		},
		"duplicates_deduplicated": {
			html: `<a href="1.2.3/">1.2.3/</a><a href="1.2.3/">1.2.3/</a>`,
			want: []string{"1.2.3"},
		},
		"mixed_duplicates_deduplicated": {
			html: `<a href="a/">a/</a><a href="b/">b/</a><a href="a/">a/</a>`,
			want: []string{"a", "b"},
		},
		"dirs_with_various_characters": {
			html: `<a href="v1.2.3-rc1/">v1.2.3-rc1/</a><a href="2024-01-01/">2024-01-01/</a>`,
			want: []string{"v1.2.3-rc1", "2024-01-01"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := parseDirectoryListing(tc.html)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("parseDirectoryListing() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── extractAllHrefs ─────────────────────────────────────────────────────────
// Rationale: extractAllHrefs extracts all href attribute values from arbitrary
// HTML. Used by file discovery in directory listings where file links may not
// end with "/".

func TestExtractAllHrefs(t *testing.T) {
	tests := map[string]struct {
		html string
		want []string
	}{
		"empty_html": {
			html: "",
			want: nil,
		},
		"html_no_href": {
			html: "<html><body><p>content</p></body></html>",
			want: nil,
		},
		"single_href": {
			html: `<a href="file.txt">file.txt</a>`,
			want: []string{"file.txt"},
		},
		"multiple_hrefs": {
			html: `<a href="a/">a/</a><a href="b/">b/</a><a href="c/">c/</a>`,
			want: []string{"a/", "b/", "c/"},
		},
		"urls_and_relative_paths": {
			html: `<a href="https://example.com">ext</a><a href="../up">up</a><a href="#anchor">anchor</a>`,
			want: []string{"https://example.com", "../up", "#anchor"},
		},
		"mixed_with_non_href_attrs": {
			html: `<a href="keep.me">keep</a><img src="skip.me" alt="nope"><a href="also.keep">also</a>`,
			want: []string{"keep.me", "also.keep"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := extractAllHrefs(tc.html)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("extractAllHrefs() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── versionSortKey ──────────────────────────────────────────────────────────
// Rationale: versionSortKey converts a dotted version string to a []int for
// sort.Slice comparison. Non-numeric parts or empty strings produce [0] so
// they sort before valid versions. Used to order versions newest-first.

func TestVersionSortKey(t *testing.T) {
	tests := map[string]struct {
		ver  string
		want []int
	}{
		"empty_string": {
			ver:  "",
			want: []int{0},
		},
		"non_numeric_part": {
			ver:  "abc",
			want: []int{0},
		},
		"mixed_non_numeric_prefix": {
			ver:  "v1.2.3",
			want: []int{0},
		},
		"single_number": {
			ver:  "42",
			want: []int{42},
		},
		"two_parts": {
			ver:  "2.0",
			want: []int{2, 0},
		},
		"three_parts": {
			ver:  "1.2.3",
			want: []int{1, 2, 3},
		},
		"double_digit_minor": {
			ver:  "1.15.0",
			want: []int{1, 15, 0},
		},
		"four_parts": {
			ver:  "1.2.3.4",
			want: []int{1, 2, 3, 4},
		},
		"partial_non_numeric_middle": {
			ver:  "1.x.3",
			want: []int{0},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := versionSortKey(tc.ver)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("versionSortKey(%q) mismatch (-want +got):\n%s", tc.ver, diff)
			}
		})
	}
}

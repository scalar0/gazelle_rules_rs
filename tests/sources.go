package rulesrs

import (
	"slices"
	"strings"
	"testing"
)

func TestSourceReferences(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"src/lib.rs": `
// mod ignored_comment;
/* outer /* nested */ mod ignored_block; */
const TEXT: &str = r###"mod ignored_string; include!("ignored.rs");"###;
const QUOTED: &str = "mod ignored_quoted;";
const CHAR: char = '{';
fn lifetime<'a>(x: &'a str) -> &'a str { x }
mod normal;
mod folder;
mod r#type;
#[cfg(target_os = "windows")] mod windows;
#[cfg(target_os = "none")] mod absent;
#[path = "../shared/renamed.rs"] mod renamed;
#[cfg_attr(unix, path = "unix.rs")]
#[cfg_attr(windows, path = "win.rs")]
mod platform;
mod inline { #[path = "chosen.rs"] mod child; }
include!("included.rs");
include! { r#"raw.rs"# }
include!(concat!(env!("OUT_DIR"), "/generated.rs"));
`,
		"src/normal.rs":               `mod child; mod inline { mod nested; }`,
		"src/normal/child.rs":         "",
		"src/normal/inline/nested.rs": "",
		"src/folder/mod.rs":           "mod child;",
		"src/folder/child.rs":         "",
		"src/type.rs":                 "",
		"src/windows.rs":              "",
		"shared/renamed.rs":           `mod child; #[path = "renamed.rs"] mod cycle;`,
		"shared/child.rs":             "",
		"src/unix.rs":                 "",
		"src/win.rs":                  "",
		"src/platform.rs":             "",
		"src/inline/chosen.rs":        "",
		"src/included.rs":             `include!("included_child.rs",);`,
		"src/included_child.rs":       "",
		"src/raw.rs":                  "",
	}
	var want []string
	for file, source := range files {
		put(t, root, file, source)
		want = append(want, file)
	}
	for _, file := range []string{"src/ignored_comment.rs", "src/ignored_block.rs", "src/ignored_string.rs", "src/ignored_quoted.rs", "src/ignored.rs", "src/unused.rs", "src/renamed.rs"} {
		put(t, root, file, "")
	}
	slices.Sort(want)
	got, err := sourceFiles(root, "src/lib.rs")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Source files = %v, want %v", got, want)
	}
}

func TestSourceBoundaries(t *testing.T) {
	for _, tc := range []struct{ name, source, extra, want string }{
		{"missing root", "", "", "read Rust source"},
		{"nested package", "mod child;", "src/child/BUILD.bazel", "crosses the Bazel package"},
		{"outside package", `#[path = "../../outside.rs"] mod outside;`, "outside.rs", "inside the Cargo package"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.name != "missing root" {
				put(t, root, "pkg/src/lib.rs", tc.source)
			}
			put(t, root, "pkg/src/child/mod.rs", "")
			if tc.extra != "" {
				name := "pkg/" + tc.extra
				if tc.name == "outside package" {
					name = tc.extra
				}
				put(t, root, name, "")
			}
			_, err := sourceFiles(root+"/pkg", "src/lib.rs")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Error = %v, want %s", err, tc.want)
			}
		})
	}
}

func TestSourceChanges(t *testing.T) {
	root := t.TempDir()
	put(t, root, "lib.rs", "mod first;")
	put(t, root, "first.rs", "")
	put(t, root, "second.rs", "")
	first, err := sourceFiles(root, "lib.rs")
	if err != nil {
		t.Fatal(err)
	}
	put(t, root, "lib.rs", "mod second;")
	second, err := sourceFiles(root, "lib.rs")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(first, []string{"first.rs", "lib.rs"}) || !slices.Equal(second, []string{"lib.rs", "second.rs"}) {
		t.Fatalf("Source change: %v -> %v", first, second)
	}
}

func TestRustPathStrings(t *testing.T) {
	for _, tc := range []struct{ source, path string }{
		{`#[path = "\u{66}oo.rs"] mod chosen;`, "foo.rs"},
		{`include!("f\x6fo.rs");`, "foo.rs"},
		{"include!(\"fo\\\n   o.rs\");", "foo.rs"},
		{`#[path = r##"foo.rs"##] mod chosen;`, "foo.rs"},
	} {
		t.Run(tc.source, func(t *testing.T) {
			root := t.TempDir()
			put(t, root, "lib.rs", tc.source)
			put(t, root, tc.path, "")
			got, err := sourceFiles(root, "lib.rs")
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, []string{tc.path, "lib.rs"}) {
				t.Fatalf("Source files = %v", got)
			}
		})
	}
}

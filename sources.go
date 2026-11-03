package rulesrs

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// sourceFiles follows source references without evaluating cfg expressions or macros.
// Keep all existing cfg branches so host settings cannot remove cross-platform inputs.
func sourceFiles(dir, root string) ([]string, error) {
	s := sourceScanner{dir: dir, files: map[string]bool{}, visited: map[string]bool{}, active: map[string]bool{}}
	if err := s.visit(root, filepath.Dir(root)); err != nil {
		return nil, err
	}
	files := make([]string, 0, len(s.files))
	for file := range s.files {
		files = append(files, filepath.ToSlash(file))
	}
	slices.Sort(files)
	return files, nil
}

type sourceScanner struct {
	dir                    string
	files, visited, active map[string]bool
}

func (s *sourceScanner) visit(file, modules string) error {
	file = filepath.Clean(file)
	if filepath.IsAbs(file) || file == ".." || strings.HasPrefix(file, ".."+string(filepath.Separator)) {
		return fmt.Errorf("Rust source must be inside the Cargo package: %s", file)
	}
	key := file + "\x00" + modules
	if s.visited[key] {
		return nil
	}
	// A path cycle can change the module directory. Stop cycles by source file too.
	if s.active[file] {
		return nil
	}
	s.active[file] = true
	defer delete(s.active, file)
	s.visited[key] = true
	for parent := filepath.Dir(file); parent != "."; parent = filepath.Dir(parent) {
		for _, name := range []string{"BUILD", "BUILD.bazel"} {
			if _, err := os.Stat(filepath.Join(s.dir, parent, name)); err == nil {
				return fmt.Errorf("Rust source %s crosses the Bazel package at %s", file, parent)
			}
		}
	}
	data, err := os.ReadFile(filepath.Join(s.dir, file))
	if err != nil {
		return fmt.Errorf("read Rust source %s: %w", file, err)
	}
	s.files[file] = true
	return s.walk(rustTokens(string(data)), file, modules, filepath.Dir(file))
}

// Missing cfg branches are valid when another platform supplies their source files.
func (s *sourceScanner) optional(file, modules string) error {
	file = filepath.Clean(file)
	if _, err := os.Stat(filepath.Join(s.dir, file)); os.IsNotExist(err) {
		return nil
	}
	return s.visit(file, modules)
}

func (s *sourceScanner) walk(tokens []rustToken, file, modules, pathBase string) error {
	var paths []string
	explicit := false
	for i := 0; i < len(tokens); {
		if tokens[i].literal {
			i++
			continue
		}
		if tokens[i].text == "#" && i+1 < len(tokens) && tokens[i+1].text == "[" {
			end := tokenGroupEnd(tokens, i+1)
			attr := tokens[i+2 : end]
			found := attributePaths(attr)
			paths = append(paths, found...)
			explicit = explicit || (len(attr) > 0 && attr[0].text == "path")
			i = end + 1
			continue
		}
		if tokens[i].text == "mod" && i+2 < len(tokens) && !tokens[i+1].literal {
			name, next := tokens[i+1].text, tokens[i+2].text
			if next == ";" {
				for _, path := range paths {
					child := sourcePath(pathBase, path)
					if err := s.optional(child, filepath.Dir(child)); err != nil {
						return err
					}
				}
				if !explicit {
					child := filepath.Join(modules, name)
					if err := s.optional(child+".rs", child); err != nil {
						return err
					}
					if err := s.optional(filepath.Join(child, "mod.rs"), child); err != nil {
						return err
					}
				}
				paths, explicit = nil, false
				i += 3
				continue
			}
			if next == "{" {
				end := tokenGroupEnd(tokens, i+2)
				dirs := []string{}
				for _, path := range paths {
					dirs = append(dirs, sourcePath(pathBase, path))
				}
				if !explicit {
					dirs = append(dirs, filepath.Join(modules, name))
				}
				for _, dir := range dirs {
					if err := s.walk(tokens[i+3:end], file, dir, dir); err != nil {
						return err
					}
				}
				paths, explicit = nil, false
				i = end + 1
				continue
			}
		}
		if tokens[i].text == "include" && i+2 < len(tokens) && tokens[i+1].text == "!" && isOpen(tokens[i+2].text) {
			end := tokenGroupEnd(tokens, i+2)
			expr := tokens[i+3 : end]
			if len(expr) > 0 && expr[len(expr)-1].text == "," {
				expr = expr[:len(expr)-1]
			}
			if len(expr) == 1 && expr[0].literal {
				child := sourcePath(filepath.Dir(file), expr[0].text)
				if err := s.optional(child, filepath.Dir(child)); err != nil {
					return err
				}
			}
			i = end + 1
			continue
		}
		if isOpen(tokens[i].text) {
			end := tokenGroupEnd(tokens, i)
			if err := s.walk(tokens[i+1:end], file, modules, pathBase); err != nil {
				return err
			}
			if tokens[i].text == "{" {
				paths, explicit = nil, false
			}
			i = end + 1
			continue
		}
		if tokens[i].text == ";" {
			paths, explicit = nil, false
		}
		i++
	}
	return nil
}

func attributePaths(tokens []rustToken) []string {
	var paths []string
	if len(tokens) >= 3 && tokens[0].text == "path" && tokens[1].text == "=" && tokens[2].literal {
		return []string{tokens[2].text}
	}
	if len(tokens) > 2 && tokens[0].text == "cfg_attr" {
		for i := 2; i+2 < len(tokens); i++ {
			if tokens[i].text == "path" && tokens[i+1].text == "=" && tokens[i+2].literal {
				paths = append(paths, tokens[i+2].text)
			}
		}
	}
	return paths
}

type rustToken struct {
	text    string
	literal bool
}

func isOpen(text string) bool { return text == "(" || text == "[" || text == "{" }

func tokenGroupEnd(tokens []rustToken, start int) int {
	depth := 0
	for i := start; i < len(tokens); i++ {
		switch tokens[i].text {
		case "(", "[", "{":
			if !tokens[i].literal {
				depth++
			}
		case ")", "]", "}":
			if !tokens[i].literal {
				depth--
				if depth == 0 {
					return i
				}
			}
		}
	}
	return len(tokens)
}

// Tokenize only source references. Ignore comments and keep string contents opaque.
func rustTokens(source string) []rustToken {
	text := []rune(source)
	var tokens []rustToken
	for i := 0; i < len(text); {
		if unicode.IsSpace(text[i]) {
			i++
			continue
		}
		if i+1 < len(text) && text[i] == '/' && text[i+1] == '/' {
			for i < len(text) && text[i] != '\n' {
				i++
			}
			continue
		}
		if i+1 < len(text) && text[i] == '/' && text[i+1] == '*' {
			i += 2
			for depth := 1; depth > 0 && i < len(text); {
				if i+1 < len(text) && text[i] == '/' && text[i+1] == '*' {
					depth++
					i += 2
				} else if i+1 < len(text) && text[i] == '*' && text[i+1] == '/' {
					depth--
					i += 2
				} else {
					i++
				}
			}
			continue
		}
		// Rust raw strings can contain quotes, comments, and Rust declarations.
		start := i
		if text[i] == 'b' || text[i] == 'c' {
			start++
		}
		if start < len(text) && text[start] == 'r' {
			quote := start + 1
			for quote < len(text) && text[quote] == '#' {
				quote++
			}
			if quote < len(text) && text[quote] == '"' {
				suffix := "\"" + strings.Repeat("#", quote-start-1)
				rest := string(text[quote+1:])
				if end := strings.Index(rest, suffix); end >= 0 {
					tokens = append(tokens, rustToken{rest[:end], true})
					i = quote + 1 + len([]rune(rest[:end]+suffix))
					continue
				}
			}
		}
		if start < len(text) && text[start] == '"' {
			end := start + 1
			for end < len(text) && text[end] != '"' {
				if text[end] == '\\' {
					end++
				}
				end++
			}
			if end < len(text) {
				value, err := rustString(string(text[start+1 : end]))
				if err == nil {
					tokens = append(tokens, rustToken{value, true})
				}
				i = end + 1
				continue
			}
		}
		// Skip character literals without consuming lifetime names.
		if text[i] == '\'' {
			end := i + 1
			if end < len(text) && text[end] == '\\' {
				end += 2
				for end < len(text) && text[end] != '\'' && end-i < 14 {
					end++
				}
			} else {
				end++
			}
			if end < len(text) && text[end] == '\'' {
				i = end + 1
				continue
			}
		}
		if unicode.IsLetter(text[i]) || text[i] == '_' {
			end := i + 1
			if text[i] == 'r' && end < len(text) && text[end] == '#' {
				i += 2
				end = i
			}
			for end < len(text) && (unicode.IsLetter(text[end]) || unicode.IsDigit(text[end]) || text[end] == '_') {
				end++
			}
			tokens = append(tokens, rustToken{text: string(text[i:end])})
			i = end
			continue
		}
		tokens = append(tokens, rustToken{text: string(text[i])})
		i++
	}
	return tokens
}

// Preserve absolute paths so visit can reject them instead of joining them locally.
func sourcePath(base, reference string) string {
	if filepath.IsAbs(reference) {
		return reference
	}
	return filepath.Join(base, reference)
}

func rustString(text string) (string, error) {
	var value strings.Builder
	for len(text) > 0 {
		switch {
		case strings.HasPrefix(text, "\\u{"):
			end := strings.IndexByte(text, '}')
			if end < 0 {
				return "", fmt.Errorf("incomplete Rust Unicode escape")
			}
			code, err := strconv.ParseUint(strings.ReplaceAll(text[3:end], "_", ""), 16, 32)
			if err != nil || code > unicode.MaxRune || (code >= 0xd800 && code <= 0xdfff) {
				return "", fmt.Errorf("invalid Rust Unicode escape")
			}
			value.WriteRune(rune(code))
			text = text[end+1:]
		case strings.HasPrefix(text, "\\0"):
			value.WriteByte(0)
			text = text[2:]
		case strings.HasPrefix(text, "\\'"):
			value.WriteByte('\'')
			text = text[2:]
		case strings.HasPrefix(text, "\\\n"), strings.HasPrefix(text, "\\\r\n"):
			text = strings.TrimLeftFunc(text[1:], unicode.IsSpace)
		case text[0] == '\n', text[0] == '\r':
			value.WriteByte(text[0])
			text = text[1:]
		default:
			char, _, rest, err := strconv.UnquoteChar(text, '"')
			if err != nil {
				return "", err
			}
			value.WriteRune(char)
			text = rest
		}
	}
	return value.String(), nil
}

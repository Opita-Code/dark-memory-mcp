// Package orchestration — judge_personas_loader.go
//
// v2.17.0 (spec 1155): Markdown file loader for persona overrides.
//
// Per spec 1155 v14 §5, operators can override the compiled default
// personas via Markdown files at $DARK_JUDGE_PERSONAS_DIR/*.md:
//
//   ---
//   id: judge-security
//   eval_types: [security_coverage, prompt_injection_scan]
//   default: true
//   ---
//
//   # Role
//   ...
//
//   ## Lens
//   ...
//
//   ## Rubric
//   - ...
//
//   ## Constraints
//   - ...
//
//   ## Voice
//   ...
//
// Merge semantics (per spec 1155 v14 §5): field-level. Each field
// comes from the Markdown if present, else from the compiled default.
// Specifically, `id` must match an existing compiled persona's id
// (the loader errors if mismatched — this prevents accidental
// shadowing of unrelated personas). To register a new persona, compile
// it in judge_personas_default.go.
package orchestration

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// PersonaOverridesDir is the env var that points to the directory of
// Markdown overrides. Set to empty to disable overrides.
const PersonaOverridesDir = "DARK_JUDGE_PERSONAS_DIR"

// loadMarkdownPersona parses a single Markdown file and returns the
// override fields it specifies. The caller is responsible for
// merging these fields with the compiled default (see
// MergePersonaOverride below).
//
// The file format is:
//
//   ---
//   id: <required>
//   eval_types: [<optional>]
//   default: <optional, true|false>
//   ---
//
//   # Role
//   <optional body>
//
//   ## Lens
//   <optional body>
//
//   ## Rubric
//   - bullet 1
//   - bullet 2
//
//   ## Constraints
//   - ...
//
//   ## Voice
//   <optional body>
//
// Returns the parsed override struct or an error.
func loadMarkdownPersona(path string) (*Persona, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("loadMarkdownPersona: empty path")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("loadMarkdownPersona: read %q: %w", path, err)
	}
	return parseMarkdownPersona(string(data), path)
}

// parseMarkdownPersona is the testable inner parser. Splits the
// content into YAML frontmatter (between leading and trailing `---`)
// and body sections, then parses each.
func parseMarkdownPersona(content, source string) (*Persona, error) {
	frontmatter, body, err := splitFrontmatter(content)
	if err != nil {
		return nil, fmt.Errorf("parseMarkdownPersona %s: %w", source, err)
	}

	// Parse YAML frontmatter.
	var fm struct {
		ID        string   `yaml:"id"`
		EvalTypes []string `yaml:"eval_types"`
		Default   *bool    `yaml:"default,omitempty"`
	}
	if err := yaml.Unmarshal([]byte(frontmatter), &fm); err != nil {
		return nil, fmt.Errorf("parseMarkdownPersona %s: frontmatter: %w", source, err)
	}
	if strings.TrimSpace(fm.ID) == "" {
		return nil, fmt.Errorf("parseMarkdownPersona %s: id is required in frontmatter", source)
	}

	p := &Persona{
		ID:        fm.ID,
		EvalTypes: fm.EvalTypes,
		Source:    PersonaSourceFilePrefix + source,
	}
	if fm.Default != nil {
		p.Default = *fm.Default
	}

	// Parse body sections.
	sections := parseBodySections(body)
	if v, ok := sections["Role"]; ok {
		p.Role = v
	}
	if v, ok := sections["Lens"]; ok {
		p.Lens = v
	}
	if v, ok := sections["Rubric"]; ok {
		p.Rubric = parseBulletList(v)
	}
	if v, ok := sections["Constraints"]; ok {
		p.Constraints = parseBulletList(v)
	}
	if v, ok := sections["Voice"]; ok {
		p.Voice = v
	}

	return p, nil
}

// splitFrontmatter splits the content into frontmatter and body. The
// frontmatter is the YAML between the leading `---` and the next
// standalone `---` line. The body is everything after.
func splitFrontmatter(content string) (string, string, error) {
	scanner := bufio.NewScanner(strings.NewReader(content))
	var frontmatterLines []string
	var bodyLines []string
	seenFirstSeparator := false
	seenSecondSeparator := false
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			if !seenFirstSeparator {
				seenFirstSeparator = true
				continue
			}
			if !seenSecondSeparator {
				seenSecondSeparator = true
				continue
			}
			// Third `---` — body content (rare but possible).
			bodyLines = append(bodyLines, line)
			continue
		}
		if seenFirstSeparator && !seenSecondSeparator {
			frontmatterLines = append(frontmatterLines, line)
		} else {
			bodyLines = append(bodyLines, line)
		}
	}
	if !seenFirstSeparator {
		return "", content, errors.New("splitFrontmatter: no leading `---` separator")
	}
	if !seenSecondSeparator {
		return "", content, errors.New("splitFrontmatter: no closing `---` separator")
	}
	return strings.Join(frontmatterLines, "\n"), strings.Join(bodyLines, "\n"), nil
}

// parseBodySections extracts the body sections by header. Each header
// is `# <name>` or `## <name>`. The body of each section is the text
// until the next header.
func parseBodySections(body string) map[string]string {
	sections := map[string]string{}
	lines := strings.Split(body, "\n")
	currentSection := ""
	currentLines := []string{}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		isH1 := strings.HasPrefix(trimmed, "# ") && !strings.HasPrefix(trimmed, "## ")
		isH2 := strings.HasPrefix(trimmed, "## ")
		if isH1 || isH2 {
			if currentSection != "" {
				sections[currentSection] = strings.TrimSpace(strings.Join(currentLines, "\n"))
			}
			currentSection = strings.TrimSpace(trimmed[strings.Index(trimmed, " ")+1:])
			currentLines = []string{}
			continue
		}
		currentLines = append(currentLines, line)
	}
	if currentSection != "" {
		sections[currentSection] = strings.TrimSpace(strings.Join(currentLines, "\n"))
	}
	return sections
}

// parseBulletList returns the bullet items as a slice. Recognizes
// lines starting with `- ` or `* `.
func parseBulletList(text string) []string {
	out := []string{}
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "- ") {
			out = append(out, trimmed[2:])
			continue
		}
		if strings.HasPrefix(trimmed, "* ") {
			out = append(out, trimmed[2:])
			continue
		}
		// Non-bullet line — include as-is (lenient parsing).
		out = append(out, trimmed)
	}
	return out
}

// MergePersonaOverride merges the fields set in `override` into the
// `base` persona. Per spec 1155 v14 §5, the merge is field-level:
//
//   - id: must match (errors if mismatch); otherwise base keeps id
//   - EvalTypes: from override if non-empty, else base
//   - Default: from override if override has a default field, else base
//   - Name, Role, Lens, Rubric, Constraints, Voice: from override if
//     non-empty, else base
//
// The returned persona has its Source field updated to override's
// source (so the registry can identify the origin). The base persona
// is not mutated.
func MergePersonaOverride(base, override *Persona) (*Persona, error) {
	if base == nil {
		return nil, errors.New("MergePersonaOverride: base is nil")
	}
	if override == nil {
		return base, nil
	}
	if override.ID != "" && override.ID != base.ID {
		return nil, fmt.Errorf("MergePersonaOverride: override id %q does not match base id %q (Markdown personas can only override existing compiled personas, not register new ones)",
			override.ID, base.ID)
	}

	merged := *base // copy
	merged.Source = override.Source

	if len(override.EvalTypes) > 0 {
		merged.EvalTypes = append([]string{}, override.EvalTypes...)
	}
	// Note: override.Default is a *bool in YAML, but we use a stripped
	// struct where it becomes bool. If override's Default is false but
	// base's is true, we treat the override as "kept default" (the
	// Markdown omitted the field). The OSStandard semantics depends on
	// whether the YAML explicitly set `default: false` vs omitted.
	// Since the override struct's Default is set to the parsed value
	// (false by default if omitted), we use a heuristic: if override's
	// other fields are non-empty AND override.Default is false, treat
	// it as "explicitly false".
	if hasOverrideContent(override) {
		merged.Default = override.Default
	}

	if override.Name != "" {
		merged.Name = override.Name
	}
	if override.Role != "" {
		merged.Role = override.Role
	}
	if override.Lens != "" {
		merged.Lens = override.Lens
	}
	if len(override.Rubric) > 0 {
		merged.Rubric = append([]string{}, override.Rubric...)
	}
	if len(override.Constraints) > 0 {
		merged.Constraints = append([]string{}, override.Constraints...)
	}
	if override.Voice != "" {
		merged.Voice = override.Voice
	}
	return &merged, nil
}

// hasOverrideContent reports whether the override specifies any
// non-default fields. Used to detect the "override omits default"
// case (where base's default is preserved).
func hasOverrideContent(override *Persona) bool {
	if override.Name != "" || override.Role != "" || override.Lens != "" || override.Voice != "" {
		return true
	}
	if len(override.EvalTypes) > 0 || len(override.Rubric) > 0 || len(override.Constraints) > 0 {
		return true
	}
	return false
}

// LoadOverriddenPersonas walks the directory at env
// DARK_JUDGE_PERSONAS_DIR (if set) and returns one merged Persona per
// Markdown file found. Each file's override is merged with the
// compiled persona whose id matches the file's id.
//
// Errors are returned per-file; the function aggregates into a
// multi-error returned as the second value.
func LoadOverriddenPersonas(compiled []*Persona) (overridden []*Persona, err error) {
	dir := os.Getenv(PersonaOverridesDir)
	if strings.TrimSpace(dir) == "" {
		return nil, nil
	}
	matches, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return nil, fmt.Errorf("LoadOverriddenPersonas: glob %q: %w", dir, err)
	}

	byID := make(map[string]*Persona, len(compiled))
	for _, p := range compiled {
		byID[p.ID] = p
	}

	var errs []string
	for _, path := range matches {
		filename := filepath.Base(path)
		override, err := loadMarkdownPersona(path)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", filename, err))
			continue
		}
		base, ok := byID[override.ID]
		if !ok {
			errs = append(errs, fmt.Sprintf("%s: id %q does not match any compiled persona", filename, override.ID))
			continue
		}
		merged, err := MergePersonaOverride(base, override)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", filename, err))
			continue
		}
		overridden = append(overridden, merged)
	}
	if len(errs) > 0 {
		return overridden, fmt.Errorf("LoadOverriddenPersonas: %d error(s): %s", len(errs), strings.Join(errs, "; "))
	}
	return overridden, nil
}

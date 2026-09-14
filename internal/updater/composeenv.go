package updater

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Environment of recreated containers (docs/operations/upgrading.md "Settings from .env", D-111).
//
// The compose engine recreates containers from their inspect data. A container created from an older compose file
// lacks settings that were added to .env later (the file did not pass them), so the environment is refreshed from the
// compose files of the project and its .env, following the precedence of `docker compose up`:
//
//  1. A value of the service's `environment` whose variables are all set in .env (or a literal) is set as compose
//     would compute it.
//  2. A value referencing a variable that .env does not set keeps the container's value: compose took it from the
//     shell or another env file when the container was created, which the updater cannot see.
//  3. A .env variable that the service's `environment` does not define is set when the service declares
//     `env_file: .env` (a user override). Otherwise, only when the compose files are older than the version being
//     installed (their x-openlog-compose-version / .bundle-version, none = older), OPENLOG_* variables that the
//     compose files do not reference at all are set: the settings those files predate. Image, updater and topology
//     variables (listen addresses, TLS/SASL of bundled dependencies) are never set this way.
//  4. Everything else in the container (docker-compose.override.yml values, unknown variables) is kept.
//
// When the compose files cannot be read (a file outside the project directory, a YAML error, an unknown service), only
// OPENLOG_* variables of .env that the container lacks or has empty are added.

// dotenv is a parsed compose .env file.
type dotenv struct {
	vals  map[string]string
	order []string
	// undetermined variables reference variables the file does not define (compose resolves them from the shell).
	undetermined map[string]bool
}

func (d *dotenv) lookup(name string) (string, bool) {
	if d == nil || d.undetermined[name] {
		return "", false
	}
	v, ok := d.vals[name]
	return v, ok
}

var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)

// parseDotenv parses the compose .env syntax: KEY=value, optional `export `, # comments, 'single quoted' (literal,
// may span lines), "double quoted" (escapes, interpolation, may span lines), unquoted (inline " #" comment,
// interpolation). ${VAR} references resolve against earlier variables of the file.
func parseDotenv(data []byte) (*dotenv, error) {
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	d := &dotenv{vals: map[string]string{}, undetermined: map[string]bool{}}
	line := 1
	for {
		trimmed := strings.TrimLeft(s, " \t\n")
		line += strings.Count(s[:len(s)-len(trimmed)], "\n")
		s = trimmed
		if s == "" {
			return d, nil
		}
		eol := strings.IndexByte(s, '\n')
		head := s
		if eol >= 0 {
			head = s[:eol]
		}
		if strings.HasPrefix(head, "#") {
			s = s[len(head):]
			continue
		}
		eq := strings.IndexByte(head, '=')
		if eq < 0 {
			return nil, fmt.Errorf("line %d: missing '='", line)
		}
		key := strings.TrimSpace(head[:eq])
		key = strings.TrimSpace(strings.TrimPrefix(key, "export "))
		if !envNameRe.MatchString(key) {
			return nil, fmt.Errorf("line %d: invalid variable name %q", line, key)
		}
		rest := strings.TrimLeft(s[eq+1:], " \t")
		var raw string
		interp := true
		switch {
		case strings.HasPrefix(rest, "'"):
			end := strings.IndexByte(rest[1:], '\'')
			if end < 0 {
				return nil, fmt.Errorf("line %d: unterminated single quote", line)
			}
			raw, interp = rest[1:1+end], false
			rest = rest[end+2:]
		case strings.HasPrefix(rest, `"`):
			var b strings.Builder
			closed := false
			i := 1
			for ; i < len(rest); i++ {
				c := rest[i]
				if c == '\\' && i+1 < len(rest) {
					i++
					switch rest[i] {
					case 'n':
						b.WriteByte('\n')
					case 'r':
						b.WriteByte('\r')
					case 't':
						b.WriteByte('\t')
					case '$':
						b.WriteString("$$")
					default:
						b.WriteByte(rest[i])
					}
					continue
				}
				if c == '"' {
					closed = true
					break
				}
				b.WriteByte(c)
			}
			if !closed {
				return nil, fmt.Errorf("line %d: unterminated double quote", line)
			}
			raw, rest = b.String(), rest[i+1:]
		default:
			v := rest
			if i := strings.IndexByte(v, '\n'); i >= 0 {
				v = v[:i]
			}
			rest = rest[len(v):]
			for i := 0; i < len(v); i++ {
				if v[i] == '#' && (i == 0 || v[i-1] == ' ' || v[i-1] == '\t') {
					v = v[:i]
					break
				}
			}
			raw = strings.TrimSpace(v)
		}
		line += strings.Count(s[:len(s)-len(rest)], "\n")
		// The rest of the line after a quoted value (spaces, a comment) is ignored.
		if i := strings.IndexByte(rest, '\n'); i >= 0 {
			rest = rest[i:]
		} else {
			rest = ""
		}
		s = rest
		val := raw
		delete(d.undetermined, key)
		if interp {
			if v, ok := interpolate(raw, d.lookup); ok {
				val = v
			} else {
				d.undetermined[key] = true
			}
		}
		if _, seen := d.vals[key]; !seen {
			d.order = append(d.order, key)
		}
		d.vals[key] = val
	}
}

func isNameStart(c byte) bool { return c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' }
func isNameChar(c byte) bool  { return isNameStart(c) || c >= '0' && c <= '9' }

// interpolate expands compose variable references in s ($$, $VAR, ${VAR}, ${VAR:-default}, ${VAR-default},
// ${VAR:+alt}, ${VAR+alt}, ${VAR:?err}, ${VAR?err}; defaults may nest). ok is false when the result depends on a
// variable lookup does not define (compose would read it from the shell) or the syntax is not understood.
func interpolate(s string, lookup func(string) (string, bool)) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		if c != '$' || i+1 >= len(s) {
			b.WriteByte(c)
			i++
			continue
		}
		switch n := s[i+1]; {
		case n == '$':
			b.WriteByte('$')
			i += 2
		case n == '{':
			end := matchBrace(s, i+1)
			if end < 0 {
				return "", false
			}
			v, ok := expandBraced(s[i+2:end], lookup)
			if !ok {
				return "", false
			}
			b.WriteString(v)
			i = end + 1
		case isNameStart(n):
			j := i + 1
			for j < len(s) && isNameChar(s[j]) {
				j++
			}
			v, ok := lookup(s[i+1 : j])
			if !ok {
				return "", false
			}
			b.WriteString(v)
			i = j
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), true
}

// matchBrace returns the index of the '}' closing the '{' at open.
func matchBrace(s string, open int) int {
	depth := 0
	for k := open; k < len(s); k++ {
		switch s[k] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return k
			}
		}
	}
	return -1
}

func expandBraced(expr string, lookup func(string) (string, bool)) (string, bool) {
	j := 0
	for j < len(expr) && isNameChar(expr[j]) {
		j++
	}
	if j == 0 || !isNameStart(expr[0]) {
		return "", false
	}
	v, set := lookup(expr[:j])
	op := expr[j:]
	if !set {
		return "", false
	}
	switch {
	case op == "":
		return v, true
	case strings.HasPrefix(op, ":-"):
		if v == "" {
			return interpolate(op[2:], lookup)
		}
		return v, true
	case strings.HasPrefix(op, "-"):
		return v, true
	case strings.HasPrefix(op, ":+"):
		if v != "" {
			return interpolate(op[2:], lookup)
		}
		return "", true
	case strings.HasPrefix(op, "+"):
		return interpolate(op[1:], lookup)
	case strings.HasPrefix(op, ":?"):
		return v, v != ""
	case strings.HasPrefix(op, "?"):
		return v, true
	}
	return "", false
}

// composeEnvValue is one entry of a service's `environment`.
type composeEnvValue struct {
	expr string
	// fromShell: `KEY:` / `- KEY` without a value (compose passes the shell's value through).
	fromShell bool
}

type composeService struct {
	env map[string]composeEnvValue
	// dotEnvFile: env_file is exactly the project's .env; otherEnvFiles: env_file names other files.
	dotEnvFile, otherEnvFiles bool
	// extends: the service extends another definition (not evaluated here).
	extends bool
}

// composeProject is the environment-relevant part of the compose files of a project.
type composeProject struct {
	services map[string]*composeService
	// refs are the variables referenced anywhere in the files (${NAME…} or $NAME).
	refs map[string]bool
}

var composeRefRe = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)`)

// parseComposeProject reads the compose files in order (later files override earlier ones, like `docker compose -f a
// -f b`). Anchors, merge keys (<<) and the !reset / !override tags of environment are honored.
func parseComposeProject(files [][]byte) (*composeProject, error) {
	p := &composeProject{services: map[string]*composeService{}, refs: map[string]bool{}}
	for i, data := range files {
		for _, m := range composeRefRe.FindAllStringSubmatch(string(data), -1) {
			if !strings.HasPrefix(m[0], "$$") {
				p.refs[m[1]] = true
			}
		}
		var doc yaml.Node
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("compose file %d: %w", i+1, err)
		}
		root := resolveNode(&doc)
		if root == nil {
			continue
		}
		top, err := mappingPairs(root)
		if err != nil {
			return nil, fmt.Errorf("compose file %d: %w", i+1, err)
		}
		for _, kv := range top {
			if kv[0].Value != "services" {
				continue
			}
			services, err := mappingPairs(kv[1])
			if err != nil {
				return nil, fmt.Errorf("compose file %d: services: %w", i+1, err)
			}
			for _, s := range services {
				if err := p.mergeService(s[0].Value, s[1]); err != nil {
					return nil, fmt.Errorf("compose file %d: service %s: %w", i+1, s[0].Value, err)
				}
			}
		}
	}
	return p, nil
}

func (p *composeProject) mergeService(name string, n *yaml.Node) error {
	svc := p.services[name]
	if svc == nil {
		svc = &composeService{env: map[string]composeEnvValue{}}
		p.services[name] = svc
	}
	if resolveNode(n) == nil || resolveNode(n).Tag == "!!null" {
		return nil
	}
	pairs, err := mappingPairs(n)
	if err != nil {
		return err
	}
	for _, kv := range pairs {
		v := kv[1]
		switch kv[0].Value {
		case "extends":
			svc.extends = true
		case "environment":
			if v.Tag == "!reset" || v.Tag == "!override" {
				svc.env = map[string]composeEnvValue{}
				if v.Tag == "!reset" {
					continue
				}
			}
			if err := parseEnvironment(resolveNode(v), svc.env); err != nil {
				return fmt.Errorf("environment: %w", err)
			}
			// A variable some service defines is known to the compose files: rule 3 never passes it to others.
			for k := range svc.env {
				p.refs[k] = true
			}
		case "env_file":
			if v.Tag == "!reset" {
				svc.dotEnvFile, svc.otherEnvFiles = false, false
				continue
			}
			dot, other, err := parseEnvFiles(resolveNode(v))
			if err != nil {
				return fmt.Errorf("env_file: %w", err)
			}
			svc.dotEnvFile, svc.otherEnvFiles = svc.dotEnvFile || dot, svc.otherEnvFiles || other
		}
	}
	return nil
}

func parseEnvironment(n *yaml.Node, env map[string]composeEnvValue) error {
	if n == nil || n.Tag == "!!null" {
		return nil
	}
	switch n.Kind {
	case yaml.MappingNode:
		pairs, err := mappingPairs(n)
		if err != nil {
			return err
		}
		for _, kv := range pairs {
			v := resolveNode(kv[1])
			if v == nil || v.Kind != yaml.ScalarNode {
				return fmt.Errorf("%s: not a scalar", kv[0].Value)
			}
			if v.Tag == "!!null" {
				env[kv[0].Value] = composeEnvValue{fromShell: true}
				continue
			}
			env[kv[0].Value] = composeEnvValue{expr: v.Value}
		}
	case yaml.SequenceNode:
		for _, item := range n.Content {
			item = resolveNode(item)
			if item == nil || item.Kind != yaml.ScalarNode {
				return errors.New("list entry is not a string")
			}
			k, v, ok := strings.Cut(item.Value, "=")
			if !ok {
				env[k] = composeEnvValue{fromShell: true}
				continue
			}
			env[k] = composeEnvValue{expr: v}
		}
	default:
		return errors.New("neither a mapping nor a list")
	}
	return nil
}

func parseEnvFiles(n *yaml.Node) (dot, other bool, err error) {
	if n == nil || n.Tag == "!!null" {
		return false, false, nil
	}
	isDot := func(path string) bool {
		return filepath.Clean(path) == ".env"
	}
	items := []*yaml.Node{n}
	if n.Kind == yaml.SequenceNode {
		items = n.Content
	}
	for _, it := range items {
		it = resolveNode(it)
		path := ""
		switch {
		case it == nil:
			continue
		case it.Kind == yaml.ScalarNode:
			path = it.Value
		case it.Kind == yaml.MappingNode:
			pairs, err := mappingPairs(it)
			if err != nil {
				return false, false, err
			}
			for _, kv := range pairs {
				if kv[0].Value == "path" {
					path = resolveNode(kv[1]).Value
				}
			}
		default:
			return false, false, errors.New("unsupported entry")
		}
		if isDot(path) {
			dot = true
		} else {
			other = true
		}
	}
	return dot, other, nil
}

// resolveNode follows document and alias nodes.
func resolveNode(n *yaml.Node) *yaml.Node {
	for n != nil {
		switch {
		case n.Kind == yaml.DocumentNode:
			if len(n.Content) == 0 {
				return nil
			}
			n = n.Content[0]
		case n.Kind == yaml.AliasNode:
			n = n.Alias
		default:
			return n
		}
	}
	return nil
}

// mappingPairs returns the key/value pairs of a mapping with merge keys (<<) expanded first, so explicit keys that
// follow override merged ones when the pairs are applied in order.
func mappingPairs(n *yaml.Node) ([][2]*yaml.Node, error) {
	n = resolveNode(n)
	if n == nil || n.Kind != yaml.MappingNode {
		return nil, errors.New("not a mapping")
	}
	var merged, own [][2]*yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Kind == yaml.ScalarNode && k.Value == "<<" && (k.Tag == "!!merge" || k.Tag == "") {
			v = resolveNode(v)
			srcs := []*yaml.Node{v}
			if v != nil && v.Kind == yaml.SequenceNode {
				srcs = v.Content
			}
			for _, src := range srcs {
				p, err := mappingPairs(src)
				if err != nil {
					return nil, fmt.Errorf("merge key: %w", err)
				}
				merged = append(merged, p...)
			}
			continue
		}
		own = append(own, [2]*yaml.Node{k, v})
	}
	return append(merged, own...), nil
}

// notFromDotenv are OPENLOG_* variables of .env that are never added to a container by rule 3 or the fallback: image
// selection, the updater's own settings, and settings the Compose topology fixes (listen addresses, the database
// name, TLS/SASL of the bundled dependencies; internal/config/envcoverage_test.go notInCompose).
func notFromDotenv(k string) bool {
	switch k {
	case "OPENLOG_IMAGE", "OPENLOG_UPDATER_IMAGE", "OPENLOG_RENDERER_IMAGE", "OPENLOG_CLICKHOUSE_DATABASE", "OPENLOG_MIGRATE_SKIP_KAFKA":
		return true
	}
	for _, p := range []string{"OPENLOG_UPDATER_", "OPENLOG_KAFKA_TLS_", "OPENLOG_KAFKA_SASL_", "OPENLOG_CLICKHOUSE_TLS_", "OPENLOG_POSTGRES_TLS_"} {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return strings.HasSuffix(k, "_ADDR")
}

// envMerge is the result of refreshing one container's environment.
type envMerge struct {
	env []string
	// changed lists the variables whose value was set or changed (names only, values may be secret).
	changed []string
	// fallback: the compose files were not usable, only missing OPENLOG_* variables were added.
	fallback bool
}

// mergeServiceEnv applies the precedence documented at the top of this file. proj may be nil (compose files not
// usable); dot may be nil (no .env: the environment is returned unchanged). stale: the compose files are older than
// the version being installed (or carry no version), so rule 3 adds the OPENLOG_* settings they do not reference.
func mergeServiceEnv(current []string, service string, proj *composeProject, dot *dotenv, stale bool) envMerge {
	out := envMerge{env: current}
	if dot == nil {
		return out
	}
	keys := make([]string, 0, len(current))
	vals := map[string]string{}
	raw := map[string]string{} // entries without '=' are kept verbatim
	for _, kv := range current {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			raw[k] = kv
		}
		if _, seen := vals[k]; !seen {
			keys = append(keys, k)
		}
		vals[k] = v
	}
	var added []string
	set := func(k, v string) {
		old, exists := vals[k]
		if exists && old == v && raw[k] == "" {
			return
		}
		if !exists {
			added = append(added, k)
		}
		delete(raw, k)
		vals[k] = v
		out.changed = append(out.changed, k)
	}
	var svc *composeService
	if proj != nil {
		svc = proj.services[service]
	}
	if svc == nil || svc.extends {
		out.fallback = true
		for _, k := range dot.order {
			v, ok := dot.lookup(k)
			if !ok || !strings.HasPrefix(k, "OPENLOG_") || notFromDotenv(k) {
				continue
			}
			if cur, exists := vals[k]; !exists || cur == "" {
				set(k, v)
			}
		}
	} else {
		names := make([]string, 0, len(svc.env))
		for k := range svc.env {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			ev := svc.env[k]
			if ev.fromShell {
				continue
			}
			if v, ok := interpolate(ev.expr, dot.lookup); ok {
				set(k, v)
			}
		}
		for _, k := range dot.order {
			if _, defined := svc.env[k]; defined {
				continue
			}
			v, ok := dot.lookup(k)
			if !ok {
				continue
			}
			switch {
			case svc.dotEnvFile && !svc.otherEnvFiles:
				set(k, v)
			case stale && strings.HasPrefix(k, "OPENLOG_") && !proj.refs[k] && !notFromDotenv(k):
				if cur, exists := vals[k]; svc.otherEnvFiles && exists && cur != "" {
					continue // may come from another env file
				}
				set(k, v)
			}
		}
	}
	if len(out.changed) == 0 {
		return out
	}
	sort.Strings(added)
	env := make([]string, 0, len(keys)+len(added))
	for _, k := range append(keys, added...) {
		if r, ok := raw[k]; ok {
			env = append(env, r)
			continue
		}
		env = append(env, k+"="+vals[k])
	}
	out.env = env
	sort.Strings(out.changed)
	return out
}

// composeFilePaths maps the compose files a container was created from (labels
// com.docker.compose.project.config_files and .working_dir, host paths) into dir, the project directory as mounted in
// the updater. replace substitutes files by relative path (a staged compose bundle).
func composeFilePaths(labels map[string]any, dir string, replace map[string]string) ([]string, error) {
	files, _ := labels["com.docker.compose.project.config_files"].(string)
	work, _ := labels["com.docker.compose.project.working_dir"].(string)
	if files == "" || work == "" {
		return nil, errors.New("the container has no compose config_files/working_dir labels")
	}
	var out []string
	for _, f := range strings.Split(files, ",") {
		rel, err := filepath.Rel(work, strings.TrimSpace(f))
		if err != nil || !filepath.IsLocal(rel) {
			return nil, fmt.Errorf("compose file %s is outside the project directory %s", f, work)
		}
		if r, ok := replace[filepath.ToSlash(rel)]; ok {
			out = append(out, r)
			continue
		}
		out = append(out, filepath.Join(dir, rel))
	}
	return out, nil
}

// loadComposeProject reads and parses the files.
func loadComposeProject(paths []string) (*composeProject, error) {
	var data [][]byte
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		data = append(data, b)
	}
	return parseComposeProject(data)
}

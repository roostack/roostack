package pkgspec

import (
	"maps"
	"reflect"
	"slices"
	"strings"

	"roostack.cloud/roostack/internal/quadlet"
)

// Description is what the package editor (internal/editor) needs to know
// to check a package as Validate and Policy do, taken from the values they
// use rather than copied, so the two can't drift apart (SPEC.md §8.3.7,
// "The package editor"). The checks that are code are written again in
// the editor's JavaScript; a test there runs both on the same packages.
type Description struct {
	Schema string `json:"schema"`
	// Fields is the package's structure, as Parse decodes it.
	Fields *Field `json:"fields"`
	// Patterns are the regular expressions Validate and Policy match
	// against, in a syntax JavaScript reads the same way.
	Patterns map[string]string `json:"patterns"`
	// Lists are the fixed lists of values fields take.
	Lists      map[string][]string `json:"lists"`
	Categories []Category          `json:"categories"`
	// Limits are lengths and sizes (characters, bytes or pixels, as
	// their names say).
	Limits map[string]int64 `json:"limits"`
	// Words are the values with a meaning of their own.
	Words map[string]string `json:"words"`
	// DatabaseMemory is what Roostack expects each database type to use
	// when the package doesn't say, in bytes.
	DatabaseMemory map[string]int64 `json:"databaseMemory"`
	Quadlet        QuadletPolicy    `json:"quadlet"`
}

// Field is one field of the package format: its YAML name, and its type,
// which is string, int, bool, any (any scalar, as a setting's default), a
// list or map of Item, an object of Fields, or quadlet (Quadlet's
// sections of keys). Ptr says it's nil when left out (Go's *T), rather
// than its zero value.
type Field struct {
	Name   string   `json:"name,omitempty"`
	Type   string   `json:"type"`
	Ptr    bool     `json:"ptr,omitempty"`
	Item   *Field   `json:"item,omitempty"`
	Fields []*Field `json:"fields,omitempty"`
}

// QuadletPolicy is the package policy's Quadlet allow-list, and the lines
// it refuses with what to use instead (quadletRules).
type QuadletPolicy struct {
	Allowed map[string][]string `json:"allowed"`
	Rules   []QuadletRule       `json:"rules"`
}

// QuadletRule is one rule of the deny-list. Denies names the value test
// the editor runs (valueTests); empty refuses any value.
type QuadletRule struct {
	Section string   `json:"section"`
	Keys    []string `json:"keys"`
	Denies  string   `json:"denies,omitempty"`
	Instead string   `json:"instead"`
}

// Describe returns the package format's Description.
func Describe() Description {
	rules := make([]QuadletRule, len(quadletRules))
	for i, r := range quadletRules {
		rules[i] = QuadletRule{Section: r.section, Keys: r.keys, Denies: r.denies, Instead: r.instead}
	}
	return Description{
		Schema: SchemaV1Alpha1,
		Fields: describeType(reflect.TypeFor[Package]()),
		Patterns: map[string]string{
			"name":           nameRe.String(),
			"settingKey":     settingKeyRe.String(),
			"envName":        envNameRe.String(),
			"subdomain":      subdomainRe.String(),
			"version":        versionRe.String(),
			"path":           pathRe.String(),
			"clientIPHeader": clientIPHeaderRe.String(),
			"pinned":         pinnedRe.String(),
			"spdxID":         spdxIDRe.String(),
			"icon":           iconRe.String(),
			"roostack":       roostackVersionRe.String(),
			"builtWith":      builtWithRe.String(),
			"memory":         memoryRe.String(),
			"quadletKey":     quadlet.KeyPattern,
			// Single sign-on's (sso.go).
			"ssoRedirectPath": redirectPathRe.String(),
			"ssoAppScheme":    appSchemeRe.String(),
			"ssoGroupName":    groupNameRe.String(),
		},
		Lists: map[string][]string{
			"ssoClientSecret":    {SSOClientSecret},
			"ssoExisting":        SSOExisting,
			"settingTypes":       {SettingBool, SettingString, SettingInt, SettingSecret},
			"classes":            {ClassCritical, ClassReplaceable},
			"locations":          {LocationService, LocationMedia},
			"databaseTypes":      {DBSQLite, DBPostgres, DBMySQL, DBMariaDB, DBRedis},
			"exposures":          {ExposurePrivate, ExposurePublic},
			"hookEvents":         {HookInstall, HookAction, HookSSO},
			"gpuKinds":           gpuKinds,
			"quadletSections":    quadletSections,
			"deprecatedLicences": deprecatedLicences,
		},
		Categories: Categories,
		Limits: map[string]int64{
			"maxCategories":   MaxCategories,
			"maxKeywords":     MaxKeywords,
			"maxBuiltWith":    MaxBuiltWith,
			"maxMakerBytes":   maxMaker,
			"maxKeywordBytes": maxKeyword,
			"maxStorageTitle": maxStorageTitle,
			"minHostPort":     MinHostPort,
			"iconMinSize":     IconMinSize,
			"iconMaxBytes":    IconMaxBytes,
			"minMemory":       minMemory,
			"maxMemory":       maxMemory,
		},
		Words: map[string]string{
			"licenceClosed":       LicenceClosed,
			"defaultAuto":         DefaultAuto,
			"releasesPlaceholder": ReleasesPlaceholder,
		},
		DatabaseMemory: map[string]int64{
			DBPostgres: DefaultPostgresMemory,
			DBRedis:    DefaultRedisMemory,
			DBMySQL:    DefaultMySQLMemory,
			DBMariaDB:  DefaultMariaDBMemory,
		},
		Quadlet: QuadletPolicy{Allowed: maps.Clone(quadletAllowed), Rules: rules},
	}
}

// ValueTests names the value tests Describe's Quadlet rules refer to,
// sorted, for the editor's test that it has each one.
func ValueTests() []string { return slices.Sorted(maps.Keys(valueTests)) }

// describeType describes a type of the package format by its yaml tags.
func describeType(t reflect.Type) *Field {
	if t == reflect.TypeFor[Quadlet]() {
		return &Field{Type: "quadlet"}
	}
	switch t.Kind() {
	case reflect.Pointer:
		f := describeType(t.Elem())
		f.Ptr = true
		return f
	case reflect.String:
		return &Field{Type: "string"}
	case reflect.Int:
		return &Field{Type: "int"}
	case reflect.Bool:
		return &Field{Type: "bool"}
	case reflect.Interface:
		return &Field{Type: "any"}
	case reflect.Slice:
		return &Field{Type: "list", Item: describeType(t.Elem())}
	case reflect.Map:
		return &Field{Type: "map", Item: describeType(t.Elem())}
	case reflect.Struct:
		f := &Field{Type: "object"}
		for i := range t.NumField() {
			sf := t.Field(i)
			name, _, _ := strings.Cut(sf.Tag.Get("yaml"), ",")
			field := describeType(sf.Type)
			field.Name = name
			f.Fields = append(f.Fields, field)
		}
		return f
	}
	panic("pkgspec: no description for " + t.String())
}

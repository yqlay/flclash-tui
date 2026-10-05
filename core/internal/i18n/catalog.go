// Package i18n provides offline translations for the Linux terminal frontend.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/text/feature/plural"
	"golang.org/x/text/language"
)

//go:embed locales/*.json metadata.json
var resources embed.FS

type Language struct {
	Code    string
	Name    string
	English string
	Complex bool
}

var languages = []Language{
	{"en", "English", "English", false},
	{"zh-Hans", "简体中文", "Chinese (Simplified)", false},
	{"zh-Hant", "繁體中文", "Chinese (Traditional)", false},
	{"ru", "Русский", "Russian", false},
	{"fa", "فارسی", "Persian", true},
	{"my", "မြန်မာ", "Burmese", true},
	{"tk", "Türkmençe", "Turkmen", false},
	{"ur", "اردو", "Urdu", true},
	{"hi", "हिन्दी", "Hindi", true},
	{"es", "Español", "Spanish", false},
	{"ar", "العربية", "Arabic", true},
	{"fr", "Français", "French", false},
	{"bn", "বাংলা", "Bengali", true},
	{"pt", "Português", "Portuguese", false},
	{"id", "Bahasa Indonesia", "Indonesian", false},
	{"de", "Deutsch", "German", false},
	{"ja", "日本語", "Japanese", false},
	{"pcm", "Naijá", "Nigerian Pidgin", false},
}

func Languages() []Language { return append([]Language(nil), languages...) }

func Normalize(code string) string {
	for _, item := range languages {
		if strings.EqualFold(strings.TrimSpace(code), item.Code) {
			return item.Code
		}
	}
	return "en"
}

func Lookup(code string) Language {
	code = Normalize(code)
	for _, item := range languages {
		if item.Code == code {
			return item
		}
	}
	return languages[0]
}

var once sync.Once
var catalogs map[string]map[string]string
var sourceInfo map[string]SourceInfo

type SourceInfo struct {
	Level    string `json:"level"`
	Kind     string `json:"kind"`
	Progress bool   `json:"progress"`
}

func Info(key string) SourceInfo {
	once.Do(load)
	return sourceInfo[key]
}

func load() {
	data, err := resources.ReadFile("metadata.json")
	if err != nil {
		panic(err)
	}
	if err := json.Unmarshal(data, &sourceInfo); err != nil {
		panic(err)
	}
	catalogs = make(map[string]map[string]string, len(languages))
	for _, item := range languages {
		data, err := resources.ReadFile("locales/" + item.Code + ".json")
		if err != nil {
			panic(fmt.Sprintf("embedded translation %s: %v", item.Code, err))
		}
		var entries map[string]string
		if err := json.Unmarshal(data, &entries); err != nil {
			panic(fmt.Sprintf("embedded translation %s: %v", item.Code, err))
		}
		catalogs[item.Code] = entries
	}
}

// Text translates a stable source key. Formatting and data values stay separate.
func Text(code, key string, args ...any) string {
	once.Do(load)
	value := catalogs[Normalize(code)][key]
	if value == "" {
		value = catalogs["en"][key]
	}
	if value == "" {
		return key
	}
	if len(args) != 0 {
		return fmt.Sprintf(value, args...)
	}
	return value
}

// Count uses CLDR cardinal forms for an integer count. Missing locale-specific
// forms use that locale's other form before falling back to English.
func Count(code, key string, count int, args ...any) string {
	once.Do(load)
	code = Normalize(code)
	if count < 0 {
		count = -(count % 10_000_000)
	}
	form := plural.Cardinal.MatchPlural(language.Make(code), count, 0, 0, 0, 0)
	forms := map[plural.Form]string{plural.Zero: "zero", plural.One: "one", plural.Two: "two", plural.Few: "few", plural.Many: "many", plural.Other: "other"}
	selected := key + "." + forms[form]
	if catalogs[code][selected] == "" {
		selected = key + ".other"
	}
	return Text(code, selected, args...)
}

// Entries returns a copy for catalog validation, never a mutable global map.
func Entries(code string) map[string]string {
	once.Do(load)
	result := map[string]string{}
	for key, value := range catalogs[Normalize(code)] {
		result[key] = value
	}
	return result
}

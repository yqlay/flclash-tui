package i18n

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCatalogsCompleteAndFormatSafe(t *testing.T) {
	if len(Languages()) != 18 || Languages()[0].Code != "en" {
		t.Fatal("unexpected language list")
	}
	formats := regexp.MustCompile(`%(?:\[[0-9]+\])?[-+# 0]*(?:[0-9]+|\*)?(?:\.(?:[0-9]+|\*))?[a-zA-Z%]`)
	english := Entries("en")
	for _, item := range Languages() {
		entries := Entries(item.Code)
		for key, source := range english {
			value := entries[key]
			if value == "" || !utf8.ValidString(value) || strings.ContainsAny(value, "\x1b\x00") {
				t.Errorf("%s: invalid %s", item.Code, key)
			}
			if !reflect.DeepEqual(formats.FindAllString(source, -1), formats.FindAllString(value, -1)) {
				t.Errorf("%s: changed format arguments in %s: %q", item.Code, key, value)
			}
		}
		if Normalize(item.Code) != item.Code {
			t.Errorf("invalid code %q", item.Code)
		}
	}
}

func TestFallbackAndOpaqueValues(t *testing.T) {
	if Normalize("ZH-hAnt") != "zh-Hant" || Normalize("invalid") != "en" {
		t.Fatal("language normalization")
	}
	if got := Text("invalid", "language.saved"); got != "Language saved" {
		t.Fatal(got)
	}
	if got := Text("en", "missing.key"); got != "missing.key" {
		t.Fatal(got)
	}
	// Unknown values must never be treated as translatable phrases or formats.
	value := "Language saved /tmp/节点.yaml 192.0.2.1 %s"
	for _, item := range Languages() {
		if got := Text(item.Code, "language.save_failed", value); !strings.Contains(got, value) {
			t.Fatalf("%s changed opaque data: %q", item.Code, got)
		}
	}
	copy := Entries("en")
	copy["language.saved"] = "changed"
	if Text("en", "language.saved") != "Language saved" {
		t.Fatal("mutable catalog escaped")
	}
}

func TestCardinalForms(t *testing.T) {
	if Count("en", "providers.count", 1, 1) != "   1 proxy" {
		t.Fatal("singular")
	}
	if Count("en", "providers.count", 2, 2) != "   2 proxies" {
		t.Fatal("plural")
	}
	if got := Count("ar", "providers.count", 3, 3); !strings.Contains(got, "وكلاء") {
		t.Fatal(got)
	}
	if got := Count("ar", "providers.count", 15, 15); !strings.Contains(got, "وكيلاً") {
		t.Fatal(got)
	}
}

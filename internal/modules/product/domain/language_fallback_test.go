package domain

import "testing"

func TestProductTranslationFallbackForDutch(t *testing.T) {
	descEN := "English description"
	descFR := "Description francaise"

	p := &Product{
		Translations: []Translation{
			{Language: "fr", Name: "Nom FR", Description: &descFR},
			{Language: "en", Name: "Name EN", Description: &descEN},
		},
	}

	got := p.GetTranslationFor("nl")
	if got == nil {
		t.Fatal("expected translation, got nil")
	}
	if got.Language != "fr" {
		t.Fatalf("expected french fallback for nl (FR is authoritative), got %q", got.Language)
	}
}

func TestProductTranslationFallbackSkipsEmptyDutch(t *testing.T) {
	descFR := "Description francaise"

	p := &Product{
		Translations: []Translation{
			{Language: "nl", Name: ""}, // blank NL row must not shadow FR
			{Language: "fr", Name: "Nom FR", Description: &descFR},
		},
	}

	got := p.GetTranslationFor("nl")
	if got == nil {
		t.Fatal("expected translation, got nil")
	}
	if got.Language != "fr" {
		t.Fatalf("expected fr when nl name is empty, got %q", got.Language)
	}
}

func TestCategoryTranslationFallbackForDutch(t *testing.T) {
	c := &Category{
		Translations: []Translation{
			{Language: "zh", Name: "ZH Name"},
			{Language: "fr", Name: "FR Name"},
		},
	}

	got := c.GetTranslationFor("nl")
	if got == nil {
		t.Fatal("expected translation, got nil")
	}
	if got.Language != "fr" {
		t.Fatalf("expected french fallback for nl when en missing, got %q", got.Language)
	}
}

func TestChoiceTranslationFallbackForDutch(t *testing.T) {
	choice := &ProductChoice{
		Translations: []ChoiceTranslation{
			{Locale: "zh", Name: "ZH"},
			{Locale: "fr", Name: "FR"},
		},
	}

	got := choice.GetTranslationFor("nl")
	if got != "FR" {
		t.Fatalf("expected french fallback for nl when en missing, got %q", got)
	}
}

func TestTranslationFallbackOrderForDutch(t *testing.T) {
	got := translationFallbackOrder("nl")
	want := []string{"nl", "fr", "en", "zh"}

	if len(got) != len(want) {
		t.Fatalf("expected %d fallback entries, got %d", len(want), len(got))
	}

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected fallback order at index %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestTranslationFallbackOrderForEnglishHasZhLast(t *testing.T) {
	got := translationFallbackOrder("en")
	want := []string{"en", "fr", "nl", "zh"}

	if len(got) != len(want) {
		t.Fatalf("expected %d fallback entries, got %d", len(want), len(got))
	}

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected fallback order at index %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestTranslationFallbackOrderForFrenchHasZhLast(t *testing.T) {
	got := translationFallbackOrder("fr")
	want := []string{"fr", "en", "nl", "zh"}

	if len(got) != len(want) {
		t.Fatalf("expected %d fallback entries, got %d", len(want), len(got))
	}

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected fallback order at index %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestTranslationFallbackOrderForUnknownHasZhLast(t *testing.T) {
	got := translationFallbackOrder("de")
	want := []string{"de", "fr", "en", "nl", "zh"}

	if len(got) != len(want) {
		t.Fatalf("expected %d fallback entries, got %d", len(want), len(got))
	}

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected fallback order at index %d: got %q want %q", i, got[i], want[i])
		}
	}
}

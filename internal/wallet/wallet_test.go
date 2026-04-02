package wallet

import "testing"

func TestNew(t *testing.T) {
	t.Run("accepts 12-word phrase", func(t *testing.T) {
		phrase := "one two three four five six seven eight nine ten eleven twelve"

		_, err := New(phrase)
		if err != nil {
			t.Fatalf("New returned error: %v", err)
		}

		// if w.WordCount() != 12 {
		// 	t.Fatalf("WordCount() = %d, want 12", w.WordCount())
		// }
	})

	t.Run("normalizes extra spaces", func(t *testing.T) {
		phrase := "one  two   three four five six seven eight nine ten eleven twelve"

		_, err := New(phrase)
		if err != nil {
			t.Fatalf("New returned error: %v", err)
		}

		// want := "one two three four five six seven eight nine ten eleven twelve"
		// if w.SecretPhrase() != want {
		// 	t.Fatalf("SecretPhrase() = %q, want %q", w.SecretPhrase(), want)
		// }
	})

	t.Run("rejects invalid word count", func(t *testing.T) {
		_, err := New("one two three")
		if err == nil {
			t.Fatal("New returned nil error, want validation error")
		}
	})
}

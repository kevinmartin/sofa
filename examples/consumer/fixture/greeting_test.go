package fixture

import "testing"

func TestGreeting(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"Ada", "Hello, Ada"},
		{"  Ada  ", "Hello, Ada"},
		{" ", "Hello, friend"},
	}
	for _, test := range tests {
		if got := Greeting(test.name); got != test.want {
			t.Errorf("Greeting(%q) = %q, want %q", test.name, got, test.want)
		}
	}
}

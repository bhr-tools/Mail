package addy

import "testing"

func TestEncodeDecode(t *testing.T) {
	const domain = "me.anonaddy.com"
	cases := []struct{ alias, recipient, reverse string }{
		{"shop", "bob@example.com", "shop+bob=example.com@me.anonaddy.com"},
		{"news.letter", "bob+tag@mail.example.org", "news.letter+bob+tag=mail.example.org@me.anonaddy.com"},
	}
	for _, c := range cases {
		if got := Encode(c.alias, domain, c.recipient); got != c.reverse {
			t.Errorf("Encode(%q, %q) = %q, want %q", c.alias, c.recipient, got, c.reverse)
		}
		alias, recipient, ok := Decode(c.reverse, domain)
		if !ok || alias != c.alias || recipient != c.recipient {
			t.Errorf("Decode(%q) = %q, %q, %v", c.reverse, alias, recipient, ok)
		}
	}
	if got := Encode("shop", domain, "other@ME.anonaddy.com"); got != "other@ME.anonaddy.com" {
		t.Errorf("an alias of the domain is rewritten: %q", got)
	}
	for _, a := range []string{"shop@me.anonaddy.com", "shop+bob=x.com@other.com", "+bob=x.com@me.anonaddy.com", "shop+bob@me.anonaddy.com", "shop+bob=@me.anonaddy.com"} {
		if _, _, ok := Decode(a, domain); ok {
			t.Errorf("Decode(%q) accepted", a)
		}
	}
	if ValidAlias("a+b") || ValidAlias("") || !ValidAlias("shop-2026") {
		t.Error("ValidAlias")
	}
}

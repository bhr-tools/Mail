// Package addy writes and reads addy.io reverse aliases. To send from the
// alias shop@me.anonaddy.com to bob@example.com, a verified mailbox writes to
// shop+bob=example.com@me.anonaddy.com; addy.io forwards the message to Bob
// with the alias as sender.
package addy

import (
	"regexp"
	"strings"
)

// aliasPattern is what an alias root may contain: neither "+" nor "=", which
// the reverse address uses as separators.
var aliasPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ValidAlias reports whether alias can be used as the root of an alias.
func ValidAlias(alias string) bool {
	return aliasPattern.MatchString(alias)
}

// ValidDomain reports whether domain looks like a domain name.
func ValidDomain(domain string) bool {
	return regexp.MustCompile(`^[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+$`).MatchString(domain)
}

// Encode returns the address that reaches recipient from alias@domain.
// Addresses of domain itself (another alias) are left as they are.
func Encode(alias, domain, recipient string) string {
	at := strings.LastIndex(recipient, "@")
	if at < 0 || InDomain(recipient, domain) {
		return recipient
	}
	return alias + "+" + recipient[:at] + "=" + recipient[at+1:] + "@" + domain
}

// Decode reads a reverse alias of domain: alias and the real recipient. ok
// is false for any other address.
func Decode(address, domain string) (alias, recipient string, ok bool) {
	if !InDomain(address, domain) {
		return "", "", false
	}
	local := address[:strings.LastIndex(address, "@")]
	plus := strings.Index(local, "+")
	eq := strings.LastIndex(local, "=")
	if plus <= 0 || eq < plus+2 || eq == len(local)-1 {
		return "", "", false
	}
	return local[:plus], local[plus+1:eq] + "@" + local[eq+1:], true
}

// InDomain reports whether address is at domain.
func InDomain(address, domain string) bool {
	at := strings.LastIndex(address, "@")
	return domain != "" && at > 0 && strings.EqualFold(address[at+1:], domain)
}

// Local returns the part of address before the @.
func Local(address string) string {
	if at := strings.LastIndex(address, "@"); at >= 0 {
		return address[:at]
	}
	return address
}

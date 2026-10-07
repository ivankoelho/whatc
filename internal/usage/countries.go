package usage

import "strings"

// callingCodes holds the ITU country calling codes, used only to turn a phone
// number into its country code (the ledger never stores the number itself).
// Codes are prefix-free, so the first match is the longest one. NANP countries
// all share "1".
var callingCodes = func() map[string]bool {
	m := map[string]bool{}
	add := func(codes ...string) {
		for _, c := range codes {
			m[c] = true
		}
	}
	addRange := func(prefix string, from, to int) {
		for i := from; i <= to; i++ {
			m[prefix+string(rune('0'+i))] = true
		}
	}
	add("1", "7")
	add("20", "27", "30", "31", "32", "33", "34", "36", "39", "40", "41", "43", "44", "45", "46", "47", "48", "49",
		"51", "52", "53", "54", "55", "56", "57", "58", "60", "61", "62", "63", "64", "65", "66",
		"81", "82", "84", "86", "90", "91", "92", "93", "94", "95", "98")
	add("211", "212", "213", "216", "218", "290", "291", "297", "298", "299", "420", "421", "423",
		"670", "672", "673", "674", "675", "676", "677", "678", "679", "680", "681", "682", "683", "685", "686", "687", "688", "689", "690", "691", "692",
		"850", "852", "853", "855", "856", "880", "886", "970", "971", "972", "973", "974", "975", "976", "977",
		"992", "993", "994", "995", "996", "998", "385", "386", "387", "389")
	for _, p := range []string{"22", "23", "24", "25", "26"} {
		addRange(p, 0, 9)
	}
	for _, p := range []string{"35", "50", "59", "96"} {
		addRange(p, 0, 9)
	}
	addRange("37", 0, 9)
	addRange("38", 0, 3)
	return m
}()

// CountryOf returns the calling code of phone (digits only, any formatting
// accepted), or "" when it does not start with a known one.
func CountryOf(phone string) string {
	var b strings.Builder
	for i := 0; i < len(phone) && b.Len() < 3; i++ {
		if c := phone[i]; c >= '0' && c <= '9' {
			b.WriteByte(c)
		}
	}
	d := b.String()
	for n := 1; n <= len(d); n++ {
		if callingCodes[d[:n]] {
			return d[:n]
		}
	}
	return ""
}

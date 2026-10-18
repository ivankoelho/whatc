package handlers

import (
	"regexp"
	"strings"
)

// regionOther labels contacts that cannot be placed in a Brazilian state: a
// number from another country, or one that is not a well-formed BR number.
const regionOther = "OUTROS"

// brazilStateByDDD maps a Brazilian area code (DDD) to its state (UF). Every
// DDD in use is listed; a code that is not here is treated as "other".
var brazilStateByDDD = map[string]string{
	"11": "SP", "12": "SP", "13": "SP", "14": "SP", "15": "SP", "16": "SP", "17": "SP", "18": "SP", "19": "SP",
	"21": "RJ", "22": "RJ", "24": "RJ",
	"27": "ES", "28": "ES",
	"31": "MG", "32": "MG", "33": "MG", "34": "MG", "35": "MG", "37": "MG", "38": "MG",
	"41": "PR", "42": "PR", "43": "PR", "44": "PR", "45": "PR", "46": "PR",
	"47": "SC", "48": "SC", "49": "SC",
	"51": "RS", "53": "RS", "54": "RS", "55": "RS",
	"61": "DF",
	"62": "GO", "64": "GO",
	"63": "TO",
	"65": "MT", "66": "MT",
	"67": "MS",
	"68": "AC",
	"69": "RO",
	"71": "BA", "73": "BA", "74": "BA", "75": "BA", "77": "BA",
	"79": "SE",
	"81": "PE", "87": "PE",
	"82": "AL",
	"83": "PB",
	"84": "RN",
	"85": "CE", "88": "CE",
	"86": "PI", "89": "PI",
	"91": "PA", "93": "PA", "94": "PA",
	"92": "AM", "97": "AM",
	"95": "RR",
	"96": "AP",
	"98": "MA", "99": "MA",
}

// brazilNumber matches a normalized Brazilian number: 55, a two-digit DDD, and a
// 8-digit number (landline 2-5, or a mobile from before the ninth digit, 6-9) or
// 9-digit mobile (starts 9). It is also the SQL
// pattern used to extract the DDD (see regionExpr), so both agree.
var brazilNumber = regexp.MustCompile(`^55([1-9][0-9])([2-9][0-9]{7}|9[0-9]{8})$`)

// stateOfPhone returns the UF of a Brazilian phone number, or regionOther.
func stateOfPhone(phone string) string {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, phone)
	m := brazilNumber.FindStringSubmatch(digits)
	if m == nil {
		return regionOther
	}
	if uf, ok := brazilStateByDDD[m[1]]; ok {
		return uf
	}
	return regionOther
}

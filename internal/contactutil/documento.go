package contactutil

import (
	"errors"
	"strings"
)

// ErrInvalidDocumento is returned by NormalizeDocumento when the value is not a
// valid CPF (11 digits) or CNPJ (14 digits).
var ErrInvalidDocumento = errors.New("documento must be a valid CPF (11 digits) or CNPJ (14 digits)")

// NormalizeDocumento is the single rule for a contact's CPF/CNPJ, shared by
// contact create/update, the X2 link and the contact search:
//
//	empty            -> "" (allowed: the document is optional)
//	11 digits        -> must be a valid CPF (check digits)
//	14 digits        -> must be a valid CNPJ (check digits)
//	any other length -> ErrInvalidDocumento
//
// Input may carry a mask ("123.456.789-09"); the result is digits only, which
// is how it is always stored (column contacts.cpfcnpj).
func NormalizeDocumento(raw string) (string, error) {
	digits := NormalizePhone(raw) // same "keep only 0-9" logic, reused rather than duplicated
	switch len(digits) {
	case 0:
		return "", nil
	case 11:
		if ValidCPF(digits) {
			return digits, nil
		}
	case 14:
		if ValidCNPJ(digits) {
			return digits, nil
		}
	}
	return "", ErrInvalidDocumento
}

// mod11 is the check-digit calculation shared by CPF and CNPJ: sum of
// digit*weight, remainder below 2 gives 0, otherwise 11 - remainder.
func mod11(digits string, weights []int) byte {
	sum := 0
	for i, w := range weights {
		sum += int(digits[i]-'0') * w
	}
	if r := sum % 11; r >= 2 {
		return byte('0' + 11 - r)
	}
	return '0'
}

// ValidCPF checks a digits-only CPF: 11 digits, not all equal, both check
// digits correct.
func ValidCPF(c string) bool {
	if len(c) != 11 || strings.Count(c, c[:1]) == 11 {
		return false
	}
	return c[9] == mod11(c, []int{10, 9, 8, 7, 6, 5, 4, 3, 2}) &&
		c[10] == mod11(c, []int{11, 10, 9, 8, 7, 6, 5, 4, 3, 2})
}

// ValidCNPJ checks a digits-only CNPJ: 14 digits, not all equal, both check
// digits correct.
func ValidCNPJ(c string) bool {
	if len(c) != 14 || strings.Count(c, c[:1]) == 14 {
		return false
	}
	return c[12] == mod11(c, []int{5, 4, 3, 2, 9, 8, 7, 6, 5, 4, 3, 2}) &&
		c[13] == mod11(c, []int{6, 5, 4, 3, 2, 9, 8, 7, 6, 5, 4, 3, 2})
}

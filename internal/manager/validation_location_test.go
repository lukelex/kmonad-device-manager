package manager

import "testing"

func TestValidatorRangeRejectsMultiplePositions(t *testing.T) {
	for _, output := range []string{
		"parse error at line 6, column 17\nparse error at line 8, column 4",
		"parse error at 6:17; another error at 8:4",
		"6:17\n8:4",
	} {
		if location, ok := parseValidatorRange(output); ok {
			t.Fatalf("ambiguous diagnostic %q mapped to %#v", output, location)
		}
	}
}

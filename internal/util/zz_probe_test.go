package util

import "testing"

func TestProbeBracketName(t *testing.T) {
	t.Logf("codes = %#v", ExtractCodeFromName("miaa-131.(mkv).strm"))
	t.Logf("base  = %q", javNameBase("miaa-131.(mkv).strm"))
}

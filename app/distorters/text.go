package distorters

import (
	"strings"
	"unicode"
)

func DistortText(text string, intensity int) string {
	i := clampIntensity(intensity)
	stride := TextStride(i)
	count := 0
	var b strings.Builder
	idx := 0
	for _, r := range text {
		idx++
		if stride > 1 && (idx-1)%stride != 0 {
			b.WriteRune(r)
			continue
		}
		count++
		if count%2 == 0 {
			b.WriteRune(unicode.ToUpper(r))
		} else {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// TextStride is how often characters are case-flipped (1 = every char).
func TextStride(intensity int) int {
	i := clampIntensity(intensity)
	stride := 1
	if i < 50 {
		stride += (49 - i) / 13
	}
	return stride
}

package cip

import (
	"bytes"
	"testing"
)

// symSeg builds an ANSI extended symbolic segment (0x91) with pad byte.
func symSeg(name string) []byte {
	out := append([]byte{0x91, byte(len(name))}, name...)
	if len(out)%2 != 0 {
		out = append(out, 0x00)
	}
	return out
}

func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// Pins the encodings of tag path forms that worked before multi-dimensional
// index support was added. These must remain byte-for-byte identical.
func TestSymbolEncodingPinned(t *testing.T) {
	cases := []struct {
		tag  string
		want []byte
	}{
		{"Tag", symSeg("Tag")},
		{"Tag[0]", cat(symSeg("Tag"), []byte{0x28, 0x00})},
		{"Tag[5]", cat(symSeg("Tag"), []byte{0x28, 0x05})},
		{"Tag[007]", cat(symSeg("Tag"), []byte{0x28, 0x07})},
		{"Tag[255]", cat(symSeg("Tag"), []byte{0x28, 0xFF})},
		{"Tag[256]", cat(symSeg("Tag"), []byte{0x29, 0x00, 0x00, 0x01})},
		{"Tag[300]", cat(symSeg("Tag"), []byte{0x29, 0x00, 0x2C, 0x01})},
		{"Tag[65535]", cat(symSeg("Tag"), []byte{0x29, 0x00, 0xFF, 0xFF})},
		{"Tag[65536]", cat(symSeg("Tag"), []byte{0x2A, 0x00, 0x00, 0x00, 0x01, 0x00})},
		{"Tag[70000]", cat(symSeg("Tag"), []byte{0x2A, 0x00, 0x70, 0x11, 0x01, 0x00})},
		{"Tag[4294967295]", cat(symSeg("Tag"), []byte{0x2A, 0x00, 0xFF, 0xFF, 0xFF, 0xFF})},
		{"Tag[ 7 ]", cat(symSeg("Tag"), []byte{0x28, 0x07})},
		{"Arr[5][10]", cat(symSeg("Arr"), []byte{0x28, 0x05, 0x28, 0x0A})},
		{"A[1].B[2].C", cat(symSeg("A"), []byte{0x28, 0x01}, symSeg("B"), []byte{0x28, 0x02}, symSeg("C"))},
		{"Program:MainProgram.X[3]", cat(symSeg("Program:MainProgram"), symSeg("X"), []byte{0x28, 0x03})},
		{"Program:MainProgram.Udt.Member", cat(symSeg("Program:MainProgram"), symSeg("Udt"), symSeg("Member"))},
		{"MyDint.5", cat(symSeg("MyDint"), symSeg("5"))},
		{"Arr[2].31", cat(symSeg("Arr"), []byte{0x28, 0x02}, symSeg("31"))},
		{"Udt.Arr[4].Flag", cat(symSeg("Udt"), symSeg("Arr"), []byte{0x28, 0x04}, symSeg("Flag"))},
	}
	for _, tc := range cases {
		got, err := EPath().Symbol(tc.tag).Build()
		if err != nil {
			t.Errorf("%q: unexpected error: %v", tc.tag, err)
			continue
		}
		if !bytes.Equal(got, tc.want) {
			t.Errorf("%q:\n got % X\nwant % X", tc.tag, got, tc.want)
		}
	}
}

// Multi-dimensional indexes encode as successive element segments.
func TestSymbolMultiDimIndex(t *testing.T) {
	cases := []struct {
		tag  string
		want []byte
	}{
		{"Arr[1,2]", cat(symSeg("Arr"), []byte{0x28, 0x01, 0x28, 0x02})},
		{"Arr[1, 2, 3]", cat(symSeg("Arr"), []byte{0x28, 0x01, 0x28, 0x02, 0x28, 0x03})},
		{"Arr[0,300,70000]", cat(symSeg("Arr"), []byte{0x28, 0x00, 0x29, 0x00, 0x2C, 0x01, 0x2A, 0x00, 0x70, 0x11, 0x01, 0x00})},
		{"Udt.Arr[1,2].Member", cat(symSeg("Udt"), symSeg("Arr"), []byte{0x28, 0x01, 0x28, 0x02}, symSeg("Member"))},
	}
	for _, tc := range cases {
		got, err := EPath().Symbol(tc.tag).Build()
		if err != nil {
			t.Errorf("%q: unexpected error: %v", tc.tag, err)
			continue
		}
		if !bytes.Equal(got, tc.want) {
			t.Errorf("%q:\n got % X\nwant % X", tc.tag, got, tc.want)
		}
	}
}

// Malformed indexes used to be silently mis-encoded (e.g. Arr[-1] -> 1,
// Arr[x] -> 0, Arr[1,2] -> 12, unclosed Arr[5 -> 5). They must now error.
func TestSymbolInvalidIndex(t *testing.T) {
	for _, tag := range []string{
		"Arr[]",
		"Arr[ ]",
		"Arr[-1]",
		"Arr[x]",
		"Arr[0x10]",
		"Arr[+1]",
		"Arr[1 2]",
		"Arr[1,]",
		"Arr[,1]",
		"Arr[1,,2]",
		"Arr[4294967296]",
		"Arr[99999999999999999999]",
		"Arr[5",
		"Arr[",
		"Udt.Arr[1.Member",
	} {
		got, err := EPath().Symbol(tag).Build()
		if err == nil {
			t.Errorf("%q: expected error, got % X", tag, got)
		}
	}
}

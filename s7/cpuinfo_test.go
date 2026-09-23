package s7

import (
	"encoding/hex"
	"strings"
	"testing"
)

// SZL 0x0011 data captured read-only from the owner's S7-1200 (CPU 1214C,
// firmware V4.4.1) on 2026-09-22.
const s71200SZL0011 = "0011000000 1c0003 0001 36455337203231342d31414734302d3058423020 0000 000c 2020" +
	"0006 36455337203231342d31414734302d3058423020 0000 000c 2020" +
	"0007 36455337203231342d31414734302d3058423020 0000 5604 0401"

func TestParseSZL0011FromS71200(t *testing.T) {
	d, err := hex.DecodeString(strings.ReplaceAll(s71200SZL0011, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	recs, err := parseSZLRecords(0x0011, d)
	if err != nil || len(recs) != 3 {
		t.Fatalf("recs=%d err=%v", len(recs), err)
	}
	if recs[0].index != 1 || szlText(recs[0].data[:20]) != "6ES7 214-1AG40-0XB0" {
		t.Fatalf("order code record %+v", recs[0])
	}
	fw := recs[2].data
	if recs[2].index != 7 || fw[22] != 'V' || fw[23] != 4 || fw[24] != 4 || fw[25] != 1 {
		t.Fatalf("firmware record %x", fw)
	}
	if _, err := parseSZLRecords(0x001C, d); err == nil {
		t.Fatal("SZL ID mismatch accepted")
	}
	if recs, err := parseSZLRecords(0x0011, d[:40]); err != nil || len(recs) != 1 {
		t.Fatalf("truncated data: recs=%d err=%v", len(recs), err)
	}
}
